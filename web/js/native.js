// 在安卓 App 里运行时，网页和原生代码之间的桥。
// App 用 WebView 打开书库网页，并注入 window.ShukuNative：
//   - 网络：原生代码也会探测内网 / Tailscale，离线时接管 /api/ 的读取请求，用下载到手机的书
//   - 存储：localStorage 由原生代码保存（index.html 里替换），换地址打开时设置不丢
//   - 进度：交给原生保存，离线时排队，联网后再同步
//   - 听书：交给原生的后台服务播放（锁屏、耳机按键、手表都能控制）
//   - 音量键翻页、屏幕常亮、状态栏颜色、下载到手机
// 原生代码回调网页：window.shukuNativeEvent(类型, JSON)

const N = window.ShukuNative || null
export const isNative = !!N

export function call(fn, ...args) {
  if (!N || typeof N[fn] !== 'function') return undefined
  try { return N[fn](...args.map(a => (a !== null && typeof a === 'object') ? JSON.stringify(a) : a)) } catch (e) { console.warn('ShukuNative.' + fn, e) }
}

export function callJSON(fn, ...args) {
  const r = call(fn, ...args)
  if (typeof r !== 'string' || !r) return null
  try { return JSON.parse(r) } catch { return null }
}

const listeners = {}
window.shukuNativeEvent = (type, json) => {
  let d = {}
  try { d = typeof json === 'string' && json ? JSON.parse(json) : (json || {}) } catch {}
  for (const fn of [...(listeners[type] || [])]) {
    try { fn(d) } catch (e) { console.error(e) }
  }
}

export function onNative(type, fn) {
  (listeners[type] ||= new Set()).add(fn)
  return () => listeners[type].delete(fn)
}

// 返回键：网页能处理（关掉面板、退出阅读器）就返回 true，否则 App 退到后台
const backHandlers = []
export function onBack(fn) { backHandlers.push(fn); return () => backHandlers.splice(backHandlers.indexOf(fn), 1) }
window.shukuBack = () => {
  for (let i = backHandlers.length - 1; i >= 0; i--) if (backHandlers[i]()) return true
  return false
}

// 已下载到手机的书
let downloaded = new Set()
export function refreshDownloads() {
  const list = callJSON('downloads') || []
  downloaded = new Set(list.filter(d => d.state === 'done').map(d => d.id))
  return list
}
export const isDownloaded = id => downloaded.has(id)
if (isNative) {
  refreshDownloads()
  onNative('download', () => refreshDownloads())
}
