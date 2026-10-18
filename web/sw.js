// 书库的 Service Worker
//  1. 缓存网页本身（界面、阅读内核），断网或换网络时也能秒开
//  2. 离线读书：书的详情里点了“离线可读”的书（存在 shuku-offline-v1 里，见 js/offline.js），
//     连不上书库时用存好的内容回答阅读要用的接口；别的接口照旧不缓存。
//     按登录的人分开：请求带的令牌对不上存书的人，就拿不到（家人账号限定了书库时不会串）
const VERSION = 'shuku-v2'
const OFFLINE = 'shuku-offline-v1'

self.addEventListener('install', () => { self.skipWaiting() })
self.addEventListener('activate', e => {
  // 只清掉旧版本的网页缓存，离线存的书留着
  e.waitUntil(caches.keys().then(ks => Promise.all(ks.filter(k => k.startsWith('shuku-v') && k !== VERSION).map(k => caches.delete(k)))).then(() => self.clients.claim()))
})

// 存了哪些书、令牌是谁：页面改了以后会发消息过来，重新读
let meta = null
self.addEventListener('message', e => { if (e.data && e.data.type === 'offline-changed') meta = null })
async function loadMeta() {
  if (meta) return meta
  const c = await caches.open(OFFLINE)
  const rd = async k => { const r = await c.match(k); try { return r ? await r.json() : {} } catch (e) { return {} } }
  meta = { index: await rd('/__shuku/offline.json'), sessions: await rd('/__shuku/sessions.json') }
  return meta
}

// 能离线回答的接口
function offlineRoute(u) {
  let m
  if ((m = /^\/api\/books\/(\d+)\/(read|progress|bookmarks)$/.exec(u.pathname))) return { book: +m[1], user: true }
  if ((m = /^\/api\/books\/(\d+)\/cover$/.exec(u.pathname))) return { book: +m[1] }
  if ((m = /^\/api\/files\/(\d+)\/(raw|epub|pdf|pages)(\/\d+)?$/.exec(u.pathname))) return { file: +m[1] }
  if (u.pathname === '/api/dict/t2s') return { any: true }
  return null
}

const hashes = new Map()
async function tokenHash(t) {
  if (!hashes.has(t)) {
    const b = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(t))
    hashes.set(t, [...new Uint8Array(b)].map(x => x.toString(16).padStart(2, '0')).join(''))
  }
  return hashes.get(t)
}

// 这个请求能不能用存好的内容回答：返回缓存里的那一条（还没读内容）
async function offlineMatch(req, u, r) {
  const a = req.headers.get('Authorization') || ''
  const token = a.startsWith('Bearer ') ? a.slice(7) : u.searchParams.get('t')
  if (!token) return null
  const { index, sessions } = await loadMeta()
  const uid = sessions[await tokenHash(token)]
  if (uid == null) return null
  const mine = Object.values(index.books || {}).filter(b => b.uid === uid)
  if (!r.any && !mine.some(b => r.book ? b.id === r.book : (b.files || []).includes(r.file))) return null
  const c = await caches.open(OFFLINE)
  return (await c.match((r.user ? `/__shuku/u/${uid}` : '/__shuku/c') + u.pathname)) || null
}

// 把缓存里的内容变成回答：不带 Accept-Ranges（PDF 阅读器就整个读，不再分段要）；分段请求切一段给它
async function answer(req, res) {
  const range = req.headers.get('Range')
  if (range) {
    const blob = await res.blob()
    const m = /bytes=(\d*)-(\d*)/.exec(range) || []
    let start = m[1] ? +m[1] : 0
    let end = m[2] ? +m[2] : blob.size - 1
    if (!m[1] && m[2]) { start = Math.max(0, blob.size - +m[2]); end = blob.size - 1 }
    end = Math.min(end, blob.size - 1)
    if (start > end) return new Response(null, { status: 416, headers: { 'Content-Range': `bytes */${blob.size}` } })
    return new Response(blob.slice(start, end + 1), { status: 206, headers: { 'Content-Type': res.headers.get('Content-Type') || 'application/octet-stream', 'Content-Range': `bytes ${start}-${end}/${blob.size}`, 'Content-Length': String(end - start + 1), 'X-Shuku-Offline': '1' } })
  }
  const headers = new Headers(res.headers)
  headers.delete('Accept-Ranges')
  headers.set('X-Shuku-Offline', '1')
  return new Response(res.body, { status: 200, headers })
}

const timeout = (p, ms) => new Promise((resolve, reject) => { const t = setTimeout(() => reject(new Error('timeout')), ms); p.then(v => { clearTimeout(t); resolve(v) }, e => { clearTimeout(t); reject(e) }) })

async function apiFetch(req, u, r) {
  let saved = null
  try { saved = await offlineMatch(req, u, r) } catch (e) {}
  // 没有离线存这本书：和平常一样
  if (!saved) return fetch(req)
  if (self.navigator.onLine === false) return answer(req, saved)
  // 先走网络（拿最新的）；连不上、等太久时用存好的
  try { return await timeout(fetch(req), r.file ? 12000 : 6000) } catch (e) { return answer(req, saved) }
}

self.addEventListener('fetch', e => {
  const req = e.request
  const u = new URL(req.url)
  if (req.method !== 'GET' || u.origin !== location.origin) return
  if (u.pathname.startsWith('/api/')) {
    const r = offlineRoute(u)
    if (r) e.respondWith(apiFetch(req, u, r))
    return
  }
  if (u.pathname.startsWith('/__shuku/') || u.pathname.startsWith('/dl/') || u.pathname.startsWith('/opds')) return
  // 网页本身：先用网络（拿到最新版本），失败时用缓存；打开网页（任何 #/… 地址）拿不到时用存好的首页
  e.respondWith(fetch(req).then(res => {
    if (res.ok) { const c = res.clone(); e.waitUntil(caches.open(VERSION).then(cache => cache.put(req, c)).catch(() => {})) }
    return res
  }).catch(async () => (await caches.match(req)) || (req.mode === 'navigate' && (await caches.match('/'))) || Response.error()))
})
