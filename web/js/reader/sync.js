// 阅读进度上传（网页；App 里交给原生的 ProgressSync）
//
// 每次上传带 base：这台设备知道的服务器进度时间（打开书时拿到，之后用每次上传成功返回的 updated_at）。
// 期间别的设备读过、位置又不一样，服务器不覆盖，回 conflict 和服务器上的进度，由阅读器提示要不要跳过去。
// 断网、服务器出错时留在本机队列里（按人分开），联网后再传；书删了（404）就不再传。

import { net } from '../api.js'
import { isNative } from '../native.js'

// 当前是谁：没联网时（打开离线书架）用上次登录的人
const LAST_UID = 'shuku.lastUid'
export function rememberUser(ctx) {
  const id = ctx?.user?.id
  if (id != null) { try { localStorage.setItem(LAST_UID, String(id)) } catch {} }
  return currentUid(ctx)
}
export function currentUid(ctx) {
  const id = ctx?.user?.id
  if (id != null) return +id
  try { return +(localStorage.getItem(LAST_UID) || 0) } catch { return 0 }
}

// 上传一次。返回 { ok, updated_at } | { conflict, current, updated_at } | { gone } | { failed, status }
export async function putProgress(bookId, body, { keepalive } = {}) {
  const url = (net.base || '') + `/api/books/${bookId}/progress`
  const headers = { 'Content-Type': 'application/json' }
  let tok = ''
  try { tok = localStorage.getItem('shuku.token') || '' } catch {}
  if (tok) headers.Authorization = 'Bearer ' + tok
  try {
    const r = await fetch(url, { method: 'PUT', body: JSON.stringify(body), headers, keepalive: !!keepalive, credentials: net.base ? 'omit' : 'same-origin', cache: 'no-store' })
    if (r.status === 404 || r.status === 403 || r.status === 400) return { gone: true, status: r.status }
    if (!r.ok) return { failed: true, status: r.status }
    const d = await r.json().catch(() => ({}))
    if (d.conflict) return { conflict: true, current: d.current || {}, updated_at: d.updated_at }
    return { ok: true, updated_at: d.updated_at || 0 }
  } catch {
    return { failed: true, status: 0 }
  }
}

// ---------- 本机队列：每本书只留最新的位置，阅读秒数累加 ----------

const qkey = uid => 'shuku.pq.' + uid
export function readQueue(uid) {
  try { return JSON.parse(localStorage.getItem(qkey(uid)) || '{}') || {} } catch { return {} }
}
function writeQueue(uid, q) {
  try { Object.keys(q).length ? localStorage.setItem(qkey(uid), JSON.stringify(q)) : localStorage.removeItem(qkey(uid)) } catch {}
}
export function queueProgress(uid, bookId, body, title) {
  const q = readQueue(uid)
  const old = q[bookId]
  const seconds = (old?.body?.seconds || 0) + (body.seconds || 0)
  q[bookId] = { body: { ...body, seconds, base: old?.body?.base ?? body.base }, title: title || old?.title || '', at: Date.now() }
  writeQueue(uid, q)
}
// 拿出某本书排着的进度（阅读器打开这本书时接手，免得两边一起传）
export function takeQueued(uid, bookId) {
  const q = readQueue(uid)
  const it = q[bookId]
  if (it) { delete q[bookId]; writeQueue(uid, q) }
  return it || null
}

// 阅读器正开着的那本书由阅读器自己重传（冲突时在阅读器里提示），这里跳过
let activeBook = 0
export function setActiveBook(id) { activeBook = +id || 0 }

let flushing = null
// 把队列里的都传上去。onConflict(bookId, item, current)：服务器上有更新的进度（别的设备读过）
export function flushQueue(uid, onConflict) {
  if (isNative) return Promise.resolve(0)
  if (flushing) return flushing
  flushing = (async () => {
    let sent = 0
    const q = readQueue(uid)
    for (const [id, item] of Object.entries(q)) {
      if (+id === activeBook) continue
      const r = await putProgress(+id, item.body)
      if (r.failed) {
        if (r.status === 0 || r.status === 401) break // 还没联网 / 没登录：下次再传
        continue
      }
      const cur = readQueue(uid)
      if (cur[id] && cur[id].at === item.at) delete cur[id]
      writeQueue(uid, cur)
      if (r.ok) sent++
      if (r.conflict) onConflict?.(+id, item, r.current)
    }
    return sent
  })().finally(() => { flushing = null })
  return flushing
}

export const queuedCount = uid => Object.keys(readQueue(uid)).length
