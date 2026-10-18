// 离屏页面：“浏览器下载后上传”的下载和上传都在这里做。
// 后台（service worker）里一个请求 30 秒还没有响应，浏览器就可能把后台停掉，大文件上传会断；
// 离屏页面不受这个限制。后台照常可以休眠，这里用消息把进度告诉后台（消息也会把后台叫醒）。
// 离屏页面里只有 chrome.runtime：读设置、选线路（内网 / Tailscale）、加来源页请求头都由后台做。

import { filenameFromDisposition, isBookName, extOf, extFromType, fileExt, safeFileName, netMessage, ADMIN_ONLY } from './lib/shuku.js'

const MAX = 1024 * 1024 * 1024 // 浏览器下载最多 1GB，再大请让书库下载
const TOO_BIG = '文件超过 1GB，浏览器下载不了，请点“让书库下载”'
const STALL_DOWN = 3 * 60 * 1000 // 下载时网站这么久不发数据，就算断了
const STALL_UP = 2 * 60 * 1000 // 上传时这么久没有进展，就断开重传
const RETRY_UP = [2000, 5000, 15000, 30000] // 上传时连不上书库，隔多久再试（每次重新选线路）
const KEEP = 5 * 60 * 1000 // 结束的任务结果留 5 分钟：后台正好被停掉重启时还能问到
const HTML = /^(text\/html|application\/xhtml)/

const jobs = new Map() // 键 → 正在做的任务
const ended = new Map() // 键 → 结束时发给后台的消息
let idle = 0
let idleMs = 60000 // 闲这么久就请后台关掉离屏页面（后台交任务时会带上）

chrome.runtime.onMessage.addListener((msg, sender, reply) => {
  if (msg?.to !== 'offscreen' || sender.id !== chrome.runtime.id) return
  switch (msg.type) {
    case 'start':
      start(msg.job)
      reply({ ok: true })
      break
    case 'cancel': {
      const j = jobs.get(msg.key)
      j?.cancel()
      reply({ ok: !!j || ended.has(msg.key) })
      break
    }
    case 'list':
      reply({ jobs: [...jobs.keys()], ended: [...ended.values()] })
      break
    default:
      reply({ ok: false })
  }
})

async function start(job) {
  if (jobs.has(job.key) || ended.has(job.key)) return
  clearTimeout(idle)
  if (job.idle > 0) idleMs = job.idle
  const j = { key: job.key, stage: 'downloading', bytes: 0, total: 0, name: '', told: 0, alive: 0, why: '', cancelled: false, ctl: new AbortController(), xhr: null }
  j.cancel = () => {
    j.cancelled = true
    j.ctl.abort()
    j.xhr?.abort()
  }
  jobs.set(j.key, j)
  let end
  try {
    const got = await download(job, j)
    end = got.handoff ? { stage: 'handoff', total: got.total, name: got.name } : { stage: 'done', ...(await upload(j, got.blob, got.name)) }
  } catch (e) {
    j.ctl.abort()
    end = { stage: 'error', error: j.cancelled ? '已取消' : e.message || String(e), auth: !!e.auth, admin: !!e.admin }
  }
  jobs.delete(j.key)
  const m = { type: 'xfer', key: j.key, ...end }
  ended.set(j.key, m)
  setTimeout(() => ended.delete(j.key), KEEP)
  await tell(m, 6)
  // 一分钟没有新任务，请后台把离屏页面关掉
  if (!jobs.size) {
    clearTimeout(idle)
    idle = setTimeout(() => tell({ type: 'offscreen-idle' }), idleMs)
  }
}

/** 发消息给后台（后台休眠了会被叫醒）。结束消息要多试几次 */
async function tell(m, tries = 1) {
  for (let i = 1; ; i++) {
    try {
      return await chrome.runtime.sendMessage(m)
    } catch {}
    if (i >= tries) return
    await sleep(500 * i)
  }
}

/** 进度：最多半秒报一次 */
function progress(j, force = false) {
  if (!force && Date.now() - j.told < 500) return
  j.told = Date.now()
  tell({ type: 'xfer', key: j.key, stage: j.stage, bytes: j.bytes, total: j.total, name: j.name })
}

/** 网站一直不发数据时中断下载 */
function watchdog(j, ms, why) {
  j.alive = Date.now()
  const timer = setInterval(() => {
    if (Date.now() - j.alive < ms) return
    j.why = why
    j.ctl.abort()
  }, 5000)
  return () => clearInterval(timer)
}

// ---------- 下载 ----------

async function download(job, j) {
  const stop = watchdog(j, STALL_DOWN, '下载卡住了：网站 3 分钟没有发来数据')
  try {
    let res
    try {
      res = await fetch(job.url, { credentials: 'include', cache: 'no-store', redirect: 'follow', signal: j.ctl.signal })
    } catch (e) {
      throw new Error(j.cancelled ? '已取消' : j.why || '下载失败：' + netMessage(e))
    }
    if (res.status === 401 || res.status === 403) throw new Error(`网站拒绝下载（${res.status}），可能需要先在这个网站登录`)
    if (!res.ok) throw new Error(`下载失败：网站返回 ${res.status}`)
    const type = (res.headers.get('content-type') || '').toLowerCase()
    if (HTML.test(type)) throw new Error('这个链接打开的是网页，不是电子书文件。可能要先登录，或者要在网页上再点一次下载')
    const total = +res.headers.get('content-length') || 0
    const name = pickName(job, res, type)
    j.name = name
    j.total = total
    // 文件很大、不用登录也能下载：交给书库下载，不用先下到这台电脑再传一遍，浏览器关了也不影响
    if (job.big && total >= job.big && await worksWithoutLogin(job.url, total)) {
      j.ctl.abort()
      return { handoff: true, total, name }
    }
    if (total > MAX) throw new Error(TOO_BIG)
    progress(j, true)
    const reader = res.body.getReader()
    const chunks = []
    let got = 0
    for (;;) {
      let r
      try {
        r = await reader.read()
      } catch (e) {
        throw new Error(j.cancelled ? '已取消' : j.why || '下载中断：' + netMessage(e))
      }
      if (r.done) break
      j.alive = Date.now()
      chunks.push(r.value)
      got += r.value.length
      if (got > MAX) throw new Error(TOO_BIG)
      j.bytes = got
      progress(j)
    }
    if (!got) throw new Error('下载到的文件是空的')
    const blob = new Blob(chunks)
    // 有的网站出错时返回 200 + 网页（只看文件开头：EPUB 这类 zip 包里也会有 <html>）
    if (await looksLikeHTML(blob)) throw new Error('下载到的是网页，不是电子书文件。可能要先登录，或者要在网页上再点一次下载')
    j.bytes = got
    return { blob, name }
  } finally {
    stop()
  }
}

/**
 * 不带这个网站的登录状态（Cookie）再问一次（HEAD，只要响应头）：还是同样大小的文件，说明谁都能下载，书库也能直接下。
 * 网站不支持 HEAD、要登录、给的是网页，都当成“要登录”，照常用浏览器下载。
 */
async function worksWithoutLogin(url, total) {
  const ctl = new AbortController()
  const timer = setTimeout(() => ctl.abort(), 15000)
  try {
    const r = await fetch(url, { method: 'HEAD', credentials: 'omit', cache: 'no-store', redirect: 'follow', signal: ctl.signal })
    return r.ok && !HTML.test((r.headers.get('content-type') || '').toLowerCase()) && +r.headers.get('content-length') === total
  } catch {
    return false
  } finally {
    clearTimeout(timer)
    ctl.abort()
  }
}

async function looksLikeHTML(blob) {
  const b = new Uint8Array(await blob.slice(0, 512).arrayBuffer())
  if (b[0] === 0x50 && b[1] === 0x4b) return false // zip（EPUB、CBZ……）
  const text = new TextDecoder().decode(b).replace(/^﻿/, '').trimStart().toLowerCase()
  return /^(<\?xml[^>]*>\s*)?(<!doctype html|<html[\s>]|<head[\s>]|<body[\s>])/.test(text)
}

/**
 * 决定保存的文件名：网站给的文件名 → 链接里的文件名 → 书名 + 扩展名。
 * 文件名没意义（pg1342-images.epub、download.pdf）而知道书名时用书名。入库时书库还会按元数据重新命名。
 */
function pickName(job, res, type) {
  const cands = [filenameFromDisposition(res.headers.get('content-disposition') || '')]
  try { cands.push(decodeURIComponent(new URL(res.url).pathname.split('/').pop() || '')) } catch {}
  cands.push(job.name)
  const file = cands.find(n => n && isBookName(n)) || ''
  const ext = file ? fullExt(file) : fileExt(extFromType(type) || (job.fmt && job.fmt !== 'auto' ? job.fmt : ''))
  if (!ext || !isBookName('x.' + ext)) throw new Error('看不出这个文件是什么格式' + (type ? `（${type.split(';')[0]}）` : ''))
  const generic = !file || /^(pg)?\d+([-_.]|$)|^(download|file|book|ebook|get|index|dl)\b/i.test(file)
  if (generic && job.title) return safeFileName(job.title) + '.' + ext
  if (file) return safeFileName(file)
  return safeFileName(job.pageTitle || '下载的书') + '.' + ext
}

/** 文件名的完整扩展名：保留 .kepub.epub、.fb2.zip */
function fullExt(name) {
  const n = name.toLowerCase()
  if (n.endsWith('.fb2.zip')) return 'fb2.zip'
  if (n.endsWith('.kepub.epub')) return 'kepub.epub'
  return extOf(n)
}

// ---------- 上传 ----------

async function upload(j, blob, name) {
  j.stage = 'uploading'
  j.bytes = 0
  j.total = blob.size
  progress(j, true)
  for (let i = 0; ; i++) {
    // 每次都问后台走哪条线路：下载期间可能从家里的网络换到了 Tailscale
    const to = await tell({ type: 'upload-target', key: j.key, fresh: i > 0 }, 3)
    if (j.cancelled) throw new Error('已取消')
    let err
    if (to?.base) {
      try {
        return await post(j, to, blob, name)
      } catch (e) {
        err = e
      }
    } else {
      err = Object.assign(new Error(to?.error || '连不上书库'), { auth: !!to?.auth, net: !to || !!to.offline })
    }
    if (j.cancelled) throw new Error('已取消')
    if (!err.net) throw err
    if (i >= RETRY_UP.length) throw new Error('文件没能传到书库：' + err.message)
    j.bytes = 0
    progress(j, true)
    await sleep(RETRY_UP[i])
  }
}

/** 用 XMLHttpRequest 上传：能拿到上传进度（fetch 拿不到） */
function post(j, { base, token }, blob, name) {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest()
    let alive = Date.now(), why = ''
    const timer = setInterval(() => {
      if (Date.now() - alive < STALL_UP) return
      why = '上传卡住了'
      xhr.abort()
    }, 5000)
    const fin = (err, val) => {
      clearInterval(timer)
      j.xhr = null
      if (err) reject(err)
      else resolve(val)
    }
    j.xhr = xhr
    xhr.open('POST', base + '/api/upload?path=' + encodeURIComponent(name))
    xhr.setRequestHeader('Authorization', 'Bearer ' + token)
    xhr.setRequestHeader('Content-Type', 'application/octet-stream')
    xhr.upload.onprogress = e => {
      alive = Date.now()
      j.bytes = e.loaded
      progress(j)
    }
    xhr.onprogress = () => { alive = Date.now() }
    xhr.onload = () => {
      let data = null
      try { data = xhr.responseText ? JSON.parse(xhr.responseText) : null } catch {}
      if (xhr.status === 401) return fin(Object.assign(new Error('插件的登录已失效'), { auth: true }))
      // 家人账号配对的插件：书库只让管理员往书库里加书，重传也没用
      if (xhr.status === 403) return fin(Object.assign(new Error(ADMIN_ONLY), { admin: true }))
      // 502～504：书库正在重启（比如刚升级），过一会儿再传
      if (xhr.status < 200 || xhr.status >= 300) return fin(Object.assign(new Error(data?.error || `书库返回错误（${xhr.status}）`), { net: xhr.status >= 502 && xhr.status <= 504 }))
      j.bytes = blob.size
      fin(null, { size: data?.size || blob.size, name })
    }
    xhr.onerror = () => fin(Object.assign(new Error('网络不通'), { net: true }))
    xhr.onabort = () => fin(Object.assign(new Error(j.cancelled ? '已取消' : why || '连接断了'), { net: !j.cancelled }))
    xhr.send(blob)
  })
}

const sleep = ms => new Promise(r => setTimeout(r, ms))
