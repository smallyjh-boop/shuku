// 书库插件的公共部分：连接信息、内网 / Tailscale 自动切换、调用书库接口、文件格式判断。
// 后台（background.js）、弹窗、设置页都用这一份；离屏页面（offscreen.js）只用里面的格式判断，
// 那里只有 chrome.runtime，没有 chrome.storage。

// ---------- 格式 ----------

// 书库能入库的扩展名（和服务端 formats.go 一致）
export const BOOK_EXTS = ['epub', 'kepub', 'mobi', 'azw', 'azw3', 'prc', 'fb2', 'fbz', 'txt', 'pdf', 'djvu', 'djv', 'cbz', 'cbr', 'cb7', 'cbt',
  'zip', 'rar', '7z', 'docx', 'doc', 'rtf', 'odt', 'md', 'chm', 'lit', 'pdb', 'lrf', 'snb', 'umd']

const TYPE_EXT = {
  'application/epub+zip': 'epub', 'application/pdf': 'pdf', 'application/x-mobipocket-ebook': 'mobi',
  'application/vnd.amazon.ebook': 'azw3', 'application/x-mobi8-ebook': 'azw3', 'application/x-fictionbook+xml': 'fb2',
  'application/vnd.comicbook+zip': 'cbz', 'application/x-cbz': 'cbz', 'application/vnd.comicbook-rar': 'cbr', 'application/x-cbr': 'cbr',
  'application/x-rar-compressed': 'rar', 'application/vnd.rar': 'rar', 'application/x-7z-compressed': '7z', 'image/vnd.djvu': 'djvu',
  'application/vnd.openxmlformats-officedocument.wordprocessingml.document': 'docx', 'application/rtf': 'rtf', 'text/plain': 'txt',
}

/** 文件名的扩展名（.fb2.zip 算 fbz，.kepub.epub 算 epub） */
export function extOf(name) {
  const n = String(name || '').toLowerCase().trim()
  if (n.endsWith('.fb2.zip')) return 'fbz'
  const m = n.match(/\.([a-z0-9]{1,5})$/)
  return m ? m[1] : ''
}
export const isBookName = name => BOOK_EXTS.includes(extOf(name))
export const extFromType = type => TYPE_EXT[String(type || '').split(';')[0].trim().toLowerCase()] || ''
/** 保存时用的扩展名 */
export const fileExt = fmt => ({ fbz: 'fb2.zip', kepub: 'kepub.epub', djv: 'djvu' })[fmt] || fmt

/** 从 Content-Disposition 里取文件名 */
export function filenameFromDisposition(cd) {
  if (!cd) return ''
  let m = cd.match(/filename\*\s*=\s*(?:UTF-8|utf-8)?'[^']*'([^;]+)/i)
  if (m) { try { return decodeURIComponent(m[1].trim().replace(/^"|"$/g, '')) } catch {} }
  m = cd.match(/filename\s*=\s*"([^"]*)"/i) || cd.match(/filename\s*=\s*([^;]+)/i)
  if (!m) return ''
  let s = m[1].trim()
  // 有些网站直接塞 UTF-8 字节，按 latin1 读出来是乱码，转回去
  try { if (/[\u0080-ÿ]/.test(s)) s = decodeURIComponent(escape(s)) } catch {}
  return s
}

export function safeFileName(name) {
  return String(name || '').replace(/[\\/:*?"<>|\u0000-\u001f]+/g, ' ').replace(/\s+/g, ' ').trim().slice(0, 150)
}

// 公开书库：“自动”方式下让书库直接下载（不经过浏览器，浏览器关了也不影响）
const PUBLIC_HOSTS = [/(^|\.)gutenberg\.org$/, /(^|\.)standardebooks\.org$/, /(^|\.)archive\.org$/, /(^|\.)wikisource\.org$/, /^ws-export\.wmcloud\.org$/]
export function isPublicLibrary(url) {
  if (String(url).startsWith('ia:')) return true
  try { const h = new URL(url).hostname; return PUBLIC_HOSTS.some(re => re.test(h)) } catch { return false }
}

// ---------- 连接信息 ----------

export function norm(u) {
  let s = String(u || '').trim().replace(/\/+$/, '')
  if (s && !/^https?:\/\//i.test(s)) s = 'http://' + s
  return s
}

export async function getConfig() {
  const { config } = await chrome.storage.local.get('config')
  return config || {}
}

export async function saveConfig(patch) {
  const c = { ...(await getConfig()), ...patch }
  await chrome.storage.local.set({ config: c })
  route = null
  return c
}

export const isConfigured = c => !!(c && c.token && (c.lan || c.remote))

export function deviceName() {
  const brands = (navigator.userAgentData?.brands || []).map(b => b.brand).join(' ')
  const edge = /Edge/i.test(brands) || /\bEdg\//.test(navigator.userAgent)
  return `浏览器插件（${edge ? 'Edge' : 'Chrome'}）`
}

// Tailscale 地址：100.64.0.0/10，或者 MagicDNS 的 *.ts.net
export function isTailscaleURL(u) {
  try {
    const h = new URL(u).hostname
    if (h.endsWith('.ts.net')) return true
    const m = h.match(/^100\.(\d+)\.\d+\.\d+$/)
    return !!m && +m[1] >= 64 && +m[1] <= 127
  } catch { return false }
}

/** 浏览器给的网络错误是英文（Failed to fetch），换成中文 */
export function netMessage(e) {
  const m = String(e?.message || '')
  return !m || e?.name === 'TypeError' || /failed to fetch|network ?error|load failed/i.test(m) ? '网络不通' : m
}

export async function fetchT(url, opts = {}, timeout = 15000) {
  const ctl = new AbortController()
  const t = timeout > 0 ? setTimeout(() => ctl.abort(), timeout) : 0
  try {
    return await fetch(url, { cache: 'no-store', ...opts, signal: ctl.signal })
  } catch (e) {
    throw new Error(e.name === 'AbortError' ? '超时' : netMessage(e))
  } finally {
    clearTimeout(t)
  }
}

export async function ping(base, timeout) {
  try {
    const r = await fetchT(base + '/api/ping', {}, timeout)
    if (!r.ok) return null
    const j = await r.json()
    return j && j.app === 'shuku' ? j : null
  } catch { return null }
}

// ---------- 线路：内网优先，不通走 Tailscale ----------

let route = null // { base, kind: 'lan' | 'remote', at }
// 设置页改了地址或登录：别的页面（后台、弹窗）也重新选线路（离屏页面里没有 chrome.storage）
chrome.storage?.onChanged.addListener((ch, area) => { if (area === 'local' && ch.config) route = null })

export async function resolveRoute(force = false) {
  if (!force && route && Date.now() - route.at < 60000) return route
  const c = await getConfig()
  const cands = []
  if (c.lan) cands.push({ kind: 'lan', base: c.lan, timeout: 1500 })
  if (c.remote && c.remote !== c.lan) cands.push({ kind: 'remote', base: c.remote, timeout: 4500 })
  // 同时探测，但按顺序取结果：内网通了就不等 Tailscale
  const probes = cands.map(x => ({ ...x, p: ping(x.base, x.timeout) }))
  for (const x of probes) {
    const info = await x.p
    if (!info) continue
    if (c.serverId && info.id && info.id !== c.serverId) continue // 不是同一台书库
    route = { base: x.base, kind: x.kind, at: Date.now() }
    return route
  }
  route = null
  return null
}

export const routeLabel = r => !r ? '连不上' : r.kind === 'lan' ? '内网' : 'Tailscale'

export class AuthError extends Error {}
/** 连不上书库（内网和 Tailscale 都不通，或者请求半路断了）：过一会儿可能就好了 */
export class NetError extends Error {}
/** 书库拒绝（403）：往书库里加书只有管理员能做，家人账号配对的插件做不了。重试、换一种方式都没用 */
export class AdminError extends Error {}
export const ADMIN_ONLY = '只有管理员账号能往书库里加书，请用管理员账号配对插件'

/** 书库说插件的令牌无效：清掉登录状态，让用户重新连接 */
export async function loginExpired() {
  await saveConfig({ token: '' })
  return new AuthError('插件的登录已失效（可能在书库的设备列表里被移除了），请在插件设置里重新连接')
}

/** 调书库接口。网络出错时重选一次线路再试；令牌失效时清掉登录状态 */
export async function api(path, { method = 'GET', body, headers = {}, timeout = 30000, json } = {}) {
  const c = await getConfig()
  if (!isConfigured(c)) throw new AuthError('还没有连接书库，请先在插件设置里连接')
  if (json !== undefined) {
    body = JSON.stringify(json)
    headers = { 'Content-Type': 'application/json', ...headers }
  }
  let lastErr
  for (let attempt = 0; attempt < 2; attempt++) {
    const r = await resolveRoute(attempt > 0)
    if (!r) throw new NetError('连不上书库（内网和 Tailscale 都不通）')
    let res
    try {
      res = await fetchT(r.base + path, { method, body, headers: { Authorization: 'Bearer ' + c.token, ...headers } }, timeout)
    } catch (e) {
      route = null
      lastErr = e
      continue
    }
    if (res.status === 401) throw await loginExpired()
    if (res.status === 403) throw new AdminError(ADMIN_ONLY)
    const text = await res.text()
    let data = null
    try { data = text ? JSON.parse(text) : null } catch {}
    if (!res.ok) throw Object.assign(new Error(data?.error || `书库返回错误（${res.status}）`), { status: res.status })
    return data
  }
  throw new NetError('连不上书库：' + (lastErr?.message || '网络不通'))
}

/**
 * 用配对码（或密码）连接书库。address 是用户填的地址，内网、Tailscale 都行。
 * 成功后保存书库告诉我们的内网地址和 Tailscale 地址，之后自动切换。
 */
export async function pair(address, code, password) {
  const base = norm(address)
  if (!base) throw new Error('请填写书库地址')
  const info = await ping(base, 6000)
  if (!info) throw new Error(`连不上 ${base}。请检查地址和端口，确认电脑和书库在同一个网络，或者已经打开 Tailscale`)
  const device = deviceName()
  const post = (path, payload) => fetchT(base + path, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(payload) }, 15000)
  let res, data, srv
  try {
    res = code ? await post('/api/pair/claim', { code: code.trim(), device }) : await post('/api/auth/login', { Password: password, Device: device })
    data = await res.json().catch(() => ({}))
    if (res.ok && !('lan_url' in data)) {
      // 密码登录只返回令牌，再取一次书库信息
      const r2 = await fetchT(base + '/api/info', { headers: { Authorization: 'Bearer ' + data.token } }, 10000)
      srv = { ...(await r2.json().catch(() => ({}))), token: data.token }
    }
  } catch (e) {
    throw new Error(`连接 ${base} 失败：${e.message}`)
  }
  if (!res.ok) throw new Error(data.error || `连接失败（${res.status}）`)
  srv ||= data
  let lan = norm(srv.lan_url), remote = norm(srv.remote_url)
  // 书库没设置地址时，按用户填的地址归类
  if (base !== lan && base !== remote) {
    if (isTailscaleURL(base)) remote ||= base
    else lan ||= base
  }
  if (!lan && !remote) lan = base
  // 重新配对：把旧的登录注销掉，免得设备列表里留重复的
  const old = await getConfig()
  if (old.token && old.token !== srv.token) {
    fetchT(base + '/api/auth/logout', { method: 'POST', headers: { Authorization: 'Bearer ' + old.token } }, 5000).catch(() => {})
  }
  return saveConfig({ token: srv.token, serverId: srv.id || info.id || '', name: srv.name || info.name || '书库', lan, remote })
}

export async function logout() {
  try { await api('/api/auth/logout', { method: 'POST', timeout: 5000 }) } catch {}
  await saveConfig({ token: '' })
}

// ---------- 小工具 ----------

export function fmtBytes(n) {
  if (!n) return '0 B'
  const u = ['B', 'KB', 'MB', 'GB']
  let i = 0
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++ }
  return (i ? n.toFixed(n < 10 ? 1 : 0) : n) + ' ' + u[i]
}

// ---------- 插件的新版本 ----------
// 新的插件包跟着书库服务器一起发布（/api/clients、/dl/shuku-extension.zip）。
// “已解压”的插件 Chrome 不会自动更新：提示下载新版本，解压覆盖原来的文件夹后点“重新加载”

export function newerVersion(a, b) {
  const pa = String(a || '').split('.').map(Number), pb = String(b || '').split('.').map(Number)
  for (let i = 0; i < Math.max(pa.length, pb.length); i++) {
    const d = (pa[i] || 0) - (pb[i] || 0)
    if (d) return d > 0
  }
  return false
}

/** 检查书库上有没有更新的插件。有就返回 { version, url }，并记下来（图标上显示“新”） */
export async function checkExtensionUpdate() {
  const r = await resolveRoute()
  if (!r) return (await chrome.storage.local.get('update')).update || null
  try {
    const res = await fetchT(r.base + '/api/clients', {}, 8000)
    const j = res.ok ? await res.json() : null
    const e = j && j.extension
    const cur = chrome.runtime.getManifest().version
    const update = e && newerVersion(e.version, cur) ? { version: e.version, url: r.base + e.url, at: Date.now() } : null
    await chrome.storage.local.set({ update })
    return update
  } catch {
    return (await chrome.storage.local.get('update')).update || null
  }
}
