// 接口请求 + 内网 / Tailscale 自动切换
//
// 做法：把“内网地址”和“Tailscale 地址”都记在本机。打开时同时探测两个地址的 /api/ping，
// 内网通就走内网（快、不绕路），不通就走 Tailscale。网络变化、请求失败时重新探测。
// 两个地址连的是同一台服务器（用服务器 ID 核对），所以登录令牌两边通用。

import { store, save } from './ui.js'
import { isNative, call, onNative } from './native.js'

const NET_KEY = 'shuku.net'
const TOKEN_KEY = 'shuku.token'

const state = {
  base: '',            // '' 表示就用当前网页的地址
  route: 'origin',     // lan | remote | origin | offline
  token: localStorage.getItem(TOKEN_KEY) || '',
  net: store(NET_KEY, { lan: '', remote: '', serverId: '' }),
  listeners: new Set(),
  resolving: null,
}

export const net = {
  get route() { return state.route },
  get base() { return state.base },
  get config() { return { ...state.net } },
  on(fn) { state.listeners.add(fn); return () => state.listeners.delete(fn) },
  label() {
    return { lan: '内网直连', remote: 'Tailscale', origin: '已连接', offline: '连不上服务器' }[state.route]
  },
  setConfig(c) {
    state.net = { ...state.net, ...c }
    save(NET_KEY, state.net)
    call('setNetwork', state.net)
    return resolveRoute(true)
  },
  resolve: (force) => resolveRoute(force),
}

function emit() { for (const fn of state.listeners) fn(state.route) }

const norm = u => (u || '').trim().replace(/\/+$/, '')

async function ping(base, ms) {
  const ctl = new AbortController()
  const t = setTimeout(() => ctl.abort(), ms)
  try {
    const r = await fetch((base || '') + '/api/ping', { cache: 'no-store', signal: ctl.signal, mode: base ? 'cors' : 'same-origin' })
    if (!r.ok) return null
    return await r.json()
  } catch { return null } finally { clearTimeout(t) }
}

async function resolveRoute(force) {
  if (state.resolving && !force) return state.resolving
  state.resolving = (async () => {
    const origin = location.origin
    const https = location.protocol === 'https:'
    const lan = norm(state.net.lan), remote = norm(state.net.remote)
    // https 页面不能请求 http 地址（浏览器会拦截），这种情况只用当前地址；App 里允许
    const usable = u => u && (isNative || !(https && u.startsWith('http:')))
    const tasks = []
    // 书库 Windows 版的窗口经本机代理（localhost:27231）打开：内网、Tailscale 由代理去换，网页一直用本机地址
    // （离线存书、电脑上存听书语音都靠经过它）
    const desk = location.port === '27231' && (location.hostname === 'localhost' || location.hostname === '127.0.0.1')
    if (usable(lan) && !desk) tasks.push(['lan', lan, ping(lan === origin ? '' : lan, 1200)])
    if (usable(remote) && !desk) tasks.push(['remote', remote, ping(remote === origin ? '' : remote, 4000)])
    const self = ping('', 4000)
    let pick = null
    for (const [route, url, p] of tasks) {
      const r = await p
      if (r && (!state.net.serverId || r.id === state.net.serverId)) { pick = { route, url, id: r.id }; break }
    }
    if (!pick) {
      const r = await self
      if (r) {
        const route = origin === lan ? 'lan' : origin === remote ? 'remote' : 'origin'
        pick = { route, url: origin, id: r.id }
      }
    }
    if (pick) {
      state.base = pick.url === origin ? '' : pick.url
      state.route = pick.route
      if (pick.id && !state.net.serverId) { state.net.serverId = pick.id; save(NET_KEY, state.net); call('setNetwork', state.net) }
    } else {
      state.route = 'offline'
      // App 里离线时请求发到当前页面地址，由 App 用下载到手机的书来回应
      if (isNative) state.base = ''
    }
    call('setRoute', { route: state.route, base: state.route === 'offline' ? '' : (state.base || origin) })
    emit()
    state.resolving = null
    return state.route
  })()
  return state.resolving
}

// 网络变化时重新选线路
addEventListener('online', () => resolveRoute(true))
onNative('network', () => resolveRoute(true))
addEventListener('offline', () => { state.route = 'offline'; emit() })
let hiddenAt = 0
document.addEventListener('visibilitychange', () => {
  if (document.hidden) hiddenAt = Date.now()
  else if (Date.now() - hiddenAt > 30000) resolveRoute(true)
})
navigator.connection?.addEventListener?.('change', () => resolveRoute(true))

// ---------- 令牌 ----------

export const auth = {
  get token() { return state.token },
  set(t) { state.token = t || ''; t ? localStorage.setItem(TOKEN_KEY, t) : localStorage.removeItem(TOKEN_KEY); call('setToken', state.token) },
}

// 给 <img>、下载链接等无法带请求头的地址用：把令牌放到参数里（跨地址时没有 Cookie）
export function apiUrl(path) {
  const sep = path.includes('?') ? '&' : '?'
  return state.base + path + (state.token ? sep + 't=' + encodeURIComponent(state.token) : '')
}

export class ApiError extends Error {
  constructor(msg, status) { super(msg); this.status = status }
}

let onUnauthorized = () => {}
export function setUnauthorizedHandler(fn) { onUnauthorized = fn }

export async function api(path, { method = 'GET', body, raw, signal, headers = {}, retry = true } = {}) {
  const opts = { method, signal, headers: { ...headers } }
  if (state.token) opts.headers.Authorization = 'Bearer ' + state.token
  if (body !== undefined) {
    if (body instanceof Blob || body instanceof ArrayBuffer) opts.body = body
    else { opts.body = JSON.stringify(body); opts.headers['Content-Type'] = 'application/json' }
  }
  if (!state.base) opts.credentials = 'same-origin'
  let res
  try {
    res = await fetch(state.base + path, opts)
  } catch (e) {
    if (e.name === 'AbortError') throw e
    // 可能换了网络：重新选线路后再试一次
    if (retry) {
      const before = state.base
      await resolveRoute(true)
      if (state.route !== 'offline' && (state.base !== before || method === 'GET')) return api(path, { method, body, raw, signal, headers, retry: false })
    }
    throw new ApiError('连不上书库服务器，请检查网络', 0)
  }
  if (res.status === 401 && !path.startsWith('/api/auth/')) {
    onUnauthorized()
    throw new ApiError('请先登录', 401)
  }
  if (raw) {
    if (!res.ok) throw new ApiError(await errText(res), res.status)
    return res
  }
  const ct = res.headers.get('Content-Type') || ''
  const data = ct.includes('json') ? await res.json() : await res.text()
  if (!res.ok) throw new ApiError(data?.error || `请求失败（${res.status}）`, res.status)
  return data
}

async function errText(res) {
  try { const j = await res.json(); return j.error || res.statusText } catch { return `请求失败（${res.status}）` }
}

// 带进度的下载（打开大书时显示百分比）
export async function download(path, onProgress, signal) {
  const res = await api(path, { raw: true, signal })
  const total = +res.headers.get('Content-Length') || 0
  if (!res.body || !total || !onProgress) return res.blob()
  const reader = res.body.getReader()
  const chunks = []
  let got = 0
  for (;;) {
    const { done, value } = await reader.read()
    if (done) break
    chunks.push(value); got += value.length
    onProgress(got / total)
  }
  return new Blob(chunks, { type: res.headers.get('Content-Type') || '' })
}

// 带进度的上传（XHR 才有上传进度）
export function uploadFile(file, relPath, onProgress) {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest()
    xhr.open('POST', state.base + '/api/upload?path=' + encodeURIComponent(relPath))
    if (state.token) xhr.setRequestHeader('Authorization', 'Bearer ' + state.token)
    xhr.upload.onprogress = e => e.lengthComputable && onProgress?.(e.loaded / e.total)
    xhr.onload = () => {
      let d = {}
      try { d = JSON.parse(xhr.responseText) } catch {}
      xhr.status < 300 ? resolve(d) : reject(new ApiError(d.error || `上传失败（${xhr.status}）`, xhr.status))
    }
    xhr.onerror = () => reject(new ApiError('上传中断，请检查网络', 0))
    xhr.send(file)
  })
}

export const deviceName = (() => {
  if (isNative) return call('deviceName') || '安卓 App'
  const ua = navigator.userAgent
  if (/iPhone/.test(ua)) return 'iPhone 网页'
  if (/iPad/.test(ua)) return 'iPad 网页'
  if (/Android/.test(ua)) return 'Android 网页'
  if (/Windows/.test(ua)) return 'Windows 网页'
  if (/Mac/.test(ua)) return 'Mac 网页'
  return '网页'
})()
