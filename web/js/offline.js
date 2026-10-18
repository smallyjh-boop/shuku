// 离线读书（浏览器 / iPhone 主屏幕上的书库）：把书存进浏览器的 Cache Storage，断网时也能打开，
// 进度在离线时排队、联网后再同步。安卓 App 有自己的“下载到手机”，不用这里。
// 浏览器只在 https（比如 Tailscale 地址）或 localhost 下才有 Cache Storage。
//
// 接口（书架、书的详情页用）：
//   offlineSupported            —— 这个浏览器能不能离线存书
//   isOfflineBook(id)           —— 这本书已经存好了
//   offlineButton(book, ctx)    —— 书的详情里的“离线可读”按钮（不支持时返回 null）
//   renderOffline(main, ctx)    —— #/offline 离线书架
//
// 存法：Cache Storage 里的 shuku-offline-v1。阅读要用的接口响应按登录的人分开存（/__shuku/u/人/…），
// 书的文件、封面、漫画页面大家共用（/__shuku/c/…）。sw.js 断网时用它们回答；
// 令牌指纹 → 人 的对照表也存在这里，Service Worker 靠它判断请求的人能不能拿这本书（家人账号限定了书库时不会串）。

import { h, fill, icon, toast, toastError, fmtBytes, fmtTime, confirmDialog, genCover, pct, store, save } from './ui.js'
import { api, apiUrl, auth, net } from './api.js'
import { isNative } from './native.js'
import { currentUid, rememberUser, flushQueue, putProgress, readQueue } from './reader/sync.js'

export const offlineSupported = !isNative && typeof caches !== 'undefined' && !!window.isSecureContext && 'serviceWorker' in navigator && !!crypto?.subtle

const CACHE = 'shuku-offline-v1'
const INDEX = '/__shuku/offline.json'   // { books: { "人:书": { uid, id, title, author, kind, files, size, at, keys } } }
const WHO = '/__shuku/sessions.json'    // { 令牌指纹: 人 }

// ---------- 小工具 ----------

async function sha(s) {
  const b = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(s))
  return [...new Uint8Array(b)].map(x => x.toString(16).padStart(2, '0')).join('')
}
async function readJSON(cache, key, def) {
  const r = await cache.match(key)
  if (!r) return def
  try { return await r.json() } catch { return def }
}
const writeJSON = (cache, key, v) => cache.put(key, new Response(JSON.stringify(v), { headers: { 'Content-Type': 'application/json' } }))
// 改清单（读出来、改、写回去）一次只做一件，同时存两本书时不会互相覆盖
let lock = Promise.resolve()
function locked(fn) { const p = lock.then(fn, fn); lock = p.catch(() => {}); return p }
const canonical = path => path.split('?')[0]
function notifySW() {
  try { navigator.serviceWorker?.controller?.postMessage({ type: 'offline-changed' }) } catch {}
  navigator.serviceWorker?.getRegistration?.('/').then(r => r?.active?.postMessage({ type: 'offline-changed' })).catch(() => {})
}

// 本机记一份清单（同步读，书架上标记“离线”用）
const lsKey = uid => 'shuku.offline.' + uid
function lsList(uid) { try { return JSON.parse(localStorage.getItem(lsKey(uid)) || '[]') || [] } catch { return [] } }
function lsSave(uid, list) { try { list.length ? localStorage.setItem(lsKey(uid), JSON.stringify(list)) : localStorage.removeItem(lsKey(uid)) } catch {} }

export function isOfflineBook(id) {
  if (!offlineSupported) return false
  return lsList(currentUid()).some(b => b.id === +id)
}

// 这个人（令牌）是谁：Service Worker 断网时据此判断能不能把存好的书给他
async function registerSession(ctx) {
  const uid = ctx?.user?.id
  if (!offlineSupported || uid == null || !auth.token) return
  const hsh = await sha(auth.token)
  return locked(() => saveSession(hsh, uid))
}
async function saveSession(hsh, uid) {
  const cache = await caches.open(CACHE)
  const s = await readJSON(cache, WHO, {})
  if (s[hsh] === uid) return
  s[hsh] = uid
  const keys = Object.keys(s)
  if (keys.length > 30) for (const k of keys.slice(0, keys.length - 30)) delete s[k]
  await writeJSON(cache, WHO, s)
  notifySW()
}

async function ensureSW() {
  let reg = await navigator.serviceWorker.getRegistration('/')
  if (!reg) reg = await navigator.serviceWorker.register('/sw.js')
  if (!reg.active) {
    await new Promise(resolve => {
      const w = reg.installing || reg.waiting
      if (!w) return resolve()
      w.addEventListener('statechange', () => { if (w.state === 'activated' || w.state === 'redundant') resolve() })
      setTimeout(resolve, 8000)
    })
  }
  return reg
}

// 断网时还要打得开网页和阅读器：网页本身、阅读器、这种书要用的内核，经 Service Worker 再取一次（它会顺手存进网页缓存）。
// 第一次打开书库就存书、或者在这个浏览器里还没读过 PDF 时，这些文件还不在缓存里
// 兼容旧系统的那一套网页（window.SHUKU_LEGACY，见 boot.js）：整个网页是一个脚本，PDF 另外两个
const LEGACY = !!window.SHUKU_LEGACY
const READER_FILES = LEGACY ? ['/legacy/polyfills.js', '/legacy/app.js', '/legacy/app.css']
  : ['reader', 'pdfjs', 'flow', 'pdf', 'comic', 'dict', 'notes', 'sync', 'tts', 'translate'].map(f => `/js/reader/${f}.js`)
const ENGINE_FILES = {
  foliate: LEGACY ? [] : ['view', 'epubcfi', 'progress', 'overlayer', 'text-walker', 'paginator', 'fixed-layout', 'epub', 'search', 'vendor/zip', 'vendor/fflate', 'mobi', 'fb2', 'comic-book']
    .map(f => `/vendor/foliate-js/${f}.js`),
  pdf: LEGACY ? ['/legacy/pdf.js', '/legacy/pdf.worker.js'] : ['/vendor/pdfjs/pdf.mjs', '/vendor/pdfjs/pdf.worker.mjs'],
}

async function warmUp(reader, file) {
  const sw = navigator.serviceWorker
  if (!sw.controller) await new Promise(resolve => { sw.addEventListener('controllerchange', resolve, { once: true }); setTimeout(resolve, 5000) })
  if (!sw.controller) return
  const urls = new Set(['/', ...READER_FILES, ...(ENGINE_FILES[reader] || [])])
  for (const e of performance.getEntriesByType('resource')) {
    const u = new URL(e.name, location.href)
    if (u.origin === location.origin && /\.(m?js|css|svg|png|woff2?)$/.test(u.pathname) && !/^\/(api|dl|__shuku)\//.test(u.pathname) && u.pathname !== '/sw.js') urls.add(u.pathname + u.search)
  }
  await Promise.all([...urls].map(u => fetch(u).then(r => r.blob()).catch(() => {})))
  if (reader === 'pdf' && file && file.size < 200 * 1024 * 1024) {
    const { warmFonts } = await import('./reader/pdf.js')
    await warmFonts(new Uint8Array(await file.arrayBuffer()))
  }
}

// ---------- 存一本书 ----------

async function saveBook(book, ctx, onProgress) {
  const uid = ctx?.user?.id ?? currentUid(ctx)
  await ensureSW()
  await registerSession(ctx)
  try { await navigator.storage?.persist?.() } catch {}
  const cache = await caches.open(CACHE)
  const base = net.base || ''
  const headers = auth.token ? { Authorization: 'Bearer ' + auth.token } : {}
  const keys = []
  let size = 0
  // 下载一个接口 / 文件，存进缓存（user：按人分开存）
  const get = async (path, { user, onBytes } = {}) => {
    const res = await fetch(base + path, { headers, cache: 'no-store', credentials: base ? 'omit' : 'same-origin' })
    if (!res.ok) {
      let msg = `下载失败（${res.status}）`
      try { msg = (await res.json()).error || msg } catch {}
      throw new Error(msg)
    }
    let blob
    const total = +res.headers.get('Content-Length') || 0
    if (onBytes && res.body && total) {
      const reader = res.body.getReader()
      const chunks = []
      let got = 0
      for (;;) {
        const { done, value } = await reader.read()
        if (done) break
        chunks.push(value); got += value.length
        onBytes(got / total)
      }
      blob = new Blob(chunks, { type: res.headers.get('Content-Type') || '' })
    } else blob = await res.blob()
    const key = (user ? `/__shuku/u/${uid}` : '/__shuku/c') + canonical(path)
    await cache.put(key, new Response(blob, { headers: { 'Content-Type': res.headers.get('Content-Type') || 'application/octet-stream', 'Content-Length': String(blob.size) } }))
    keys.push(key)
    size += blob.size
    return blob
  }
  onProgress?.(0)
  const info = JSON.parse(await (await get(`/api/books/${book.id}/read`, { user: true })).text())
  if (!['foliate', 'pdf', 'comic'].includes(info.reader)) throw new Error('这个格式不能在线阅读，也就不能离线读')
  await get(`/api/books/${book.id}/progress`, { user: true }).catch(() => {})
  await get(`/api/books/${book.id}/bookmarks`, { user: true }).catch(() => {})
  if (info.has_cover) await get(`/api/books/${book.id}/cover`).catch(() => {})
  let file = null
  if (info.reader === 'comic') {
    const { count } = JSON.parse(await (await get(`/api/files/${info.file_id}/pages`)).text())
    // 按这台设备的屏幕存（比原图小很多）
    const w = Math.min(2000, Math.ceil(Math.max(screen.width, screen.height) * Math.min(devicePixelRatio || 1, 2)))
    for (let i = 0; i < count; i++) {
      await get(`/api/files/${info.file_id}/pages/${i}?w=${w}`)
      onProgress?.((i + 1) / count)
    }
  } else {
    file = await get(info.url, { onBytes: f => onProgress?.(f) })
  }
  // 繁转简字表：离线时也能转（只存一次）
  if (!(await cache.match('/__shuku/c/api/dict/t2s'))) await get('/api/dict/t2s').catch(() => {})
  await warmUp(info.reader, file).catch(() => {})
  const entry = { uid, id: book.id, title: info.title || book.title || '', author: info.author || book.author || '', kind: info.kind || book.kind || 'book', has_cover: !!info.has_cover, files: [info.file_id], size, at: Date.now(), keys }
  await locked(async () => {
    const idx = await readJSON(cache, INDEX, { books: {} })
    idx.books ||= {}
    idx.books[`${uid}:${book.id}`] = entry
    await writeJSON(cache, INDEX, idx)
  })
  lsSave(uid, [...lsList(uid).filter(b => b.id !== book.id), { id: book.id, title: entry.title, author: entry.author, kind: entry.kind, size, at: entry.at }])
  notifySW()
  return entry
}

function removeBook(uid, id) { return locked(() => removeBookNow(uid, id)) }

async function removeBookNow(uid, id) {
  const cache = await caches.open(CACHE)
  const idx = await readJSON(cache, INDEX, { books: {} })
  const key = `${uid}:${id}`
  const entry = idx.books?.[key]
  delete idx.books?.[key]
  if (entry) {
    // 别人（或者别的书）还在用的共用文件不删
    const used = new Set(Object.values(idx.books || {}).flatMap(b => b.keys || []))
    for (const k of entry.keys || []) if (!used.has(k)) await cache.delete(k)
  }
  await writeJSON(cache, INDEX, idx)
  lsSave(uid, lsList(uid).filter(b => b.id !== +id))
  notifySW()
}

async function listBooks(uid) {
  try {
    const cache = await caches.open(CACHE)
    const idx = await readJSON(cache, INDEX, { books: {} })
    const list = Object.values(idx.books || {}).filter(b => b.uid === uid)
    // 浏览器清掉了缓存：本机清单跟着更新
    lsSave(uid, list.map(b => ({ id: b.id, title: b.title, author: b.author, kind: b.kind, size: b.size, at: b.at })))
    return list
  } catch { return lsList(uid) }
}

// ---------- 书的详情里的按钮 ----------

export function offlineButton(book, ctx) {
  if (!offlineSupported || !book?.id) return null
  rememberUser(ctx)
  registerSession(ctx).catch(() => {})
  const uid = ctx?.user?.id ?? currentUid(ctx)
  const btn = h('button', { class: 'btn offline-btn', type: 'button' })
  let busy = false
  const draw = () => {
    const saved = lsList(uid).find(b => b.id === +book.id)
    fill(btn, icon(saved ? 'check' : 'download'), h('span', null, saved ? `离线可读 · ${fmtBytes(saved.size)}` : '离线可读'))
    btn.classList.toggle('on', !!saved)
    btn.title = saved ? '已经存在这个浏览器里，断网也能读。点一下可以删掉' : '把这本书存在这个浏览器里，断网也能读'
    btn.setAttribute('aria-pressed', saved ? 'true' : 'false')
  }
  btn.onclick = async () => {
    if (busy) return
    if (lsList(uid).some(b => b.id === +book.id)) {
      if (!await confirmDialog({ title: '不再离线保存？', message: `《${book.title}》会从这个浏览器里删掉（书库里的书、进度和笔记不受影响）。`, ok: '删掉' })) return
      await removeBook(uid, book.id).catch(toastError)
      draw()
      toast('已从这个浏览器里删掉')
      return
    }
    busy = true
    btn.disabled = true
    try {
      await saveBook(book, ctx, f => fill(btn, icon('download'), h('span', null, `正在保存 ${Math.round(f * 100)}%`)))
      toast(`《${book.title}》已存好，断网也能读`)
    } catch (e) {
      toastError(e.name === 'QuotaExceededError' ? new Error('浏览器的存储空间不够了，删掉几本离线的书再试') : e)
    } finally {
      busy = false
      btn.disabled = false
      draw()
    }
  }
  draw()
  return btn
}

// ---------- 离线书架（#/offline） ----------

export async function renderOffline(main, ctx) {
  rememberUser(ctx)
  registerSession(ctx).catch(() => {})
  const uid = currentUid(ctx)
  const box = h('div', { class: 'offline-page' })
  main.append(box)
  const draw = async () => {
    if (!offlineSupported) {
      fill(box, h('div', { class: 'page-head' }, h('h1', null, '离线可读')),
        h('p', { class: 'muted' }, isNative ? '在 App 里请用书的详情里的“下载到手机”，没网的时候也能读。'
          : '离线读书要用 https 地址打开书库（比如 Tailscale 的地址），最好再“添加到主屏幕”。现在这个地址浏览器不让存书。'))
      return
    }
    const list = (await listBooks(uid)).sort((a, b) => b.at - a.at)
    const used = list.reduce((a, b) => a + (b.size || 0), 0)
    let est = null
    try { est = await navigator.storage?.estimate?.() } catch {}
    const queued = Object.keys(readQueue(uid)).length
    const offline = net.route === 'offline' || navigator.onLine === false
    const row = b => {
      const pos = store(`shuku.pos.${uid}.${b.id}`, {})
      const cv = h('div', { class: 'cv' })
      if (b.has_cover) {
        const img = h('img', { src: apiUrl(`/api/books/${b.id}/cover`), alt: '', loading: 'lazy' })
        img.onerror = () => img.replaceWith(genCover(b.title, b.author))
        cv.append(img)
      } else cv.append(genCover(b.title, b.author))
      const open = () => { location.hash = '#/read/' + b.id }
      return h('div', { class: 'off-book' },
        h('button', { class: 'off-cover', type: 'button', 'aria-label': '阅读《' + b.title + '》', onclick: open }, cv),
        h('div', { class: 'grow' },
          h('div', { class: 't' }, b.title),
          h('div', { class: 'm' }, [b.author, fmtBytes(b.size), pos.percent ? '读到 ' + pct(pos.percent) : '', '存于 ' + fmtTime(Math.round(b.at / 1000))].filter(Boolean).join(' · ')),
          h('div', { class: 'row' },
            h('button', { class: 'btn small primary', type: 'button', onclick: open }, icon('book'), '阅读'),
            h('button', { class: 'btn small quiet', type: 'button', onclick: async () => {
              if (!await confirmDialog({ title: '不再离线保存？', message: `《${b.title}》会从这个浏览器里删掉（书库里的书、进度和笔记不受影响）。`, ok: '删掉' })) return
              await removeBook(uid, b.id).catch(toastError)
              draw()
            } }, '删掉'))))
    }
    fill(box,
      h('div', { class: 'page-head' }, h('h1', null, '离线可读'), h('span', { class: 'muted' }, list.length ? `${list.length} 本 · ${fmtBytes(used)}` : '')),
      offline ? h('p', { class: 'off-note' }, icon('offline'), '现在连不上书库，只能读下面这些存在浏览器里的书。进度会先记在这台设备上，联网后自动同步。') : null,
      queued ? h('p', { class: 'off-note' }, `${queued} 本书的进度还没同步。`, offline ? null : h('button', { class: 'btn small', type: 'button', onclick: async () => { await flushPending(); draw() } }, '立即同步')) : null,
      list.length ? h('div', { class: 'off-list' }, list.map(row))
        : h('p', { class: 'muted' }, '还没有离线保存的书。在书的详情里点“离线可读”，断网的时候也能读。'),
      est?.quota ? h('p', { class: 'muted small-text' }, `这个浏览器已用 ${fmtBytes(est.usage || 0)}，最多大约能用 ${fmtBytes(est.quota)}。`) : null)
  }
  await draw()
  return { refresh: draw, destroy() {} }
}

// ---------- 离线时没传上去的进度：联网后、下次打开时传 ----------

function onQueueConflict(uid, id, item, cur) {
  const title = item.title ? `《${item.title}》` : '有一本书'
  // 先以那边为准：这台记的位置也换成那边的，下次打开这本书从那里接着读
  const key = `shuku.pos.${uid}.${id}`
  const mine = store(key, {})
  save(key, { file_id: cur.file_id, location: cur.location, percent: cur.percent, at: (cur.updated_at || 0) * 1000, base: cur.updated_at })
  toast(`${title}在另一台设备${cur.device ? '（' + cur.device + '）' : ''}上读到了 ${pct(cur.percent)}，这台离线时读到 ${pct(item.body.percent)}，先以那边为准`, {
    action: {
      label: '用这台的', run: () => {
        const body = { ...item.body, seconds: 0 }
        delete body.base
        putProgress(id, body).then(r => { if (r.ok) save(key, { ...mine, at: Date.now(), base: r.updated_at }) })
      },
    },
    duration: 15000,
  })
}

let pending = null
export function flushPending() {
  if (isNative) return Promise.resolve()
  pending ||= (async () => {
    // 有没有排着的（不管是谁的）
    let any = false
    try { for (let i = 0; i < localStorage.length; i++) if (localStorage.key(i)?.startsWith('shuku.pq.')) { any = true; break } } catch {}
    if (!any) return
    // 现在登录的是谁：只传这个人的，别人的留着等他自己登录
    let me
    try { me = await api('/api/me') } catch { return }
    if (me?.id == null) return
    await flushQueue(me.id, (id, item, cur) => onQueueConflict(me.id, id, item, cur))
  })().finally(() => { pending = null })
  return pending
}

if (!isNative) {
  addEventListener('online', () => setTimeout(flushPending, 1500))
  setTimeout(flushPending, 5000)
}
