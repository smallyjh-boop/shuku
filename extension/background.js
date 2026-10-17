// 后台：右键菜单、加入书库的任务队列。
// 两种加入方式：
//   浏览器下载后上传：用浏览器下载（带着你在这个网站的登录状态），再传到书库的投递目录
//   让书库下载：把链接交给书库（POST /api/fetch），NAS 自己下载，浏览器关了也不影响
// 默认“自动”：公开书库让书库下载，其他网站用浏览器下载。

import { api, getConfig, isConfigured, isPublicLibrary, extOf, isBookName, extFromType, fileExt, filenameFromDisposition, safeFileName, AuthError, checkExtensionUpdate } from './lib/shuku.js'
import { formatOfURL } from './lib/detect.js'

const PARALLEL = 2
const MAX_BROWSER = 1024 * 1024 * 1024 // 浏览器下载最多 1GB，再大请让书库下载
const KEEP = 40
const ACTIVE = ['queued', 'downloading', 'uploading', 'fetching']

let tasks = []
let running = 0
let hasUpdate = false
const aborts = new Map() // 任务 id → 取消下载的函数（不能放进任务里，任务要存进 storage）
const ready = init()

async function init() {
  const s = await chrome.storage.session.get('tasks')
  tasks = s.tasks || []
  // 后台被浏览器停掉时，正在浏览器里下载的任务就断了；交给书库下载的还能接着查进度
  for (const t of tasks) {
    if (t.status === 'downloading' || t.status === 'uploading') {
      t.status = 'error'
      t.error = '浏览器中断了这个任务，请重试'
    }
  }
  // 清掉上次留下的请求头规则
  try {
    const old = await chrome.declarativeNetRequest.getSessionRules()
    if (old.length) await chrome.declarativeNetRequest.updateSessionRules({ removeRuleIds: old.map(r => r.id) })
  } catch {}
  persist()
  for (const t of tasks) if (t.status === 'fetching' && t.jobId) pollJob(t).catch(e => fail(t, e))
  pump()
}

// ---------- 任务列表 ----------

let persistTimer = 0
let keepAlive = 0
function persist(now = false) {
  clearTimeout(persistTimer)
  const write = () => {
    tasks = tasks.slice(-KEEP)
    chrome.storage.session.set({ tasks })
    const n = tasks.filter(t => ACTIVE.includes(t.status)).length
    // 没有正在加入的书时，插件有新版本就在图标上显示“新”
    chrome.action.setBadgeText({ text: n ? String(n) : hasUpdate ? '新' : '' })
    // 上传大文件时没有别的动静，定时调一下扩展接口，免得浏览器以为后台闲着把它停掉
    if (n && !keepAlive) keepAlive = setInterval(() => chrome.runtime.getPlatformInfo(() => {}), 20000)
    if (!n && keepAlive) { clearInterval(keepAlive); keepAlive = 0 }
  }
  if (now) write()
  else persistTimer = setTimeout(write, 250)
}

function newTask(item, page) {
  const t = {
    id: Math.random().toString(36).slice(2, 10),
    url: item.url,
    fmt: item.fmt || formatOfURL(item.url),
    title: item.title || '',
    name: item.file && isBookName(item.file) ? safeFileName(item.file) : '',
    page: page ? { url: page.url || '', title: page.title || '' } : null,
    mode: item.mode || '',
    status: 'queued',
    progress: 0,
    bytes: 0,
    total: 0,
    error: '',
    at: Date.now(),
  }
  tasks.push(t)
  return t
}

async function add(items, page) {
  await ready
  const c = await getConfig()
  if (!isConfigured(c)) throw new Error('还没有连接书库，请先在插件设置里连接')
  let n = 0
  for (const it of items) {
    // 同一个链接正在加入，就不重复加
    if (tasks.some(t => t.url === it.url && ACTIVE.includes(t.status))) continue
    newTask(it, page)
    n++
  }
  persist(true)
  pump()
  return n
}

function pump() {
  while (running < PARALLEL) {
    const t = tasks.find(x => x.status === 'queued')
    if (!t) return
    t.status = 'downloading'
    running++
    run(t).catch(e => fail(t, e)).finally(() => { running--; persist(); pump() })
  }
}

function fail(t, e) {
  if (t.status === 'done') return
  t.status = 'error'
  t.error = t.cancelled ? '已取消' : (e?.message || String(e))
  persist()
  if (!t.cancelled) notify(false, t)
}

async function run(t) {
  const c = await getConfig()
  const pref = c.mode || 'auto'
  // ia: 只能让书库下载（书库去互联网档案馆挑格式）
  let mode = t.mode || (t.url.startsWith('ia:') ? 'nas' : pref === 'auto' ? (isPublicLibrary(t.url) ? 'nas' : 'browser') : pref)
  t.mode = mode
  if (mode === 'nas') {
    try {
      return await viaNas(t)
    } catch (e) {
      // 自动方式下书库下载失败（比如 NAS 访问不了这个网站），换浏览器再试一次
      // 用户点了“让书库下载”重试的，就不再换回浏览器
      if (pref !== 'auto' || t.forced || t.cancelled || e instanceof AuthError || t.url.startsWith('ia:') || t.triedBrowser) throw e
      t.triedBrowser = true
      t.mode = 'browser'
      t.note = '书库下载失败，改用浏览器下载'
    }
  }
  return viaBrowser(t)
}

// ---------- 让书库下载 ----------

async function viaNas(t) {
  t.status = 'fetching'
  persist()
  // 书名给书库用来命名文件；没有可靠的书名就让书库用网站给的文件名
  const job = await api('/api/fetch', { method: 'POST', json: { url: t.url, name: t.title || '', source: '浏览器插件', referer: t.page?.url || '' } })
  t.jobId = job.id
  persist()
  await pollJob(t)
}

async function pollJob(t) {
  for (;;) {
    await sleep(1500)
    if (t.cancelled) {
      api('/api/fetch/' + encodeURIComponent(t.jobId), { method: 'DELETE' }).catch(() => {})
      throw new Error('已取消')
    }
    const list = await api('/api/fetch')
    const j = (list || []).find(x => x.id === t.jobId)
    if (!j) throw new Error('书库里找不到这个下载任务（可能被取消了）')
    t.bytes = j.bytes || 0
    t.total = j.total > 0 ? j.total : 0
    t.progress = t.total ? t.bytes / t.total : 0
    if (j.name && !t.name) t.name = j.name
    if (j.status === 'done') {
      t.status = 'done'
      t.name = j.file || j.name || t.name
      t.progress = 1
      persist()
      notify(true, t)
      return
    }
    if (j.status === 'error') throw new Error(serverError(j.error))
    persist()
  }
}

// ---------- 浏览器下载后上传 ----------

// 有的网站要检查来源页（Referer）才给下载：下载期间临时加一条请求头规则
async function addHeaderRule(url, referer) {
  if (!referer || !/^https?:/.test(referer)) return 0
  try {
    const id = 1 + Math.floor(Math.random() * 2e9)
    await chrome.declarativeNetRequest.updateSessionRules({
      addRules: [{
        id, priority: 1,
        action: { type: 'modifyHeaders', requestHeaders: [{ header: 'Referer', operation: 'set', value: referer }, { header: 'Origin', operation: 'remove' }] },
        condition: { requestDomains: [new URL(url).hostname], tabIds: [chrome.tabs.TAB_ID_NONE], resourceTypes: ['xmlhttprequest', 'other'] },
      }],
    })
    return id
  } catch { return 0 }
}
const dropHeaderRule = id => id && chrome.declarativeNetRequest.updateSessionRules({ removeRuleIds: [id] }).catch(() => {})

async function viaBrowser(t) {
  t.status = 'downloading'
  t.progress = 0
  persist()
  const ctl = new AbortController()
  aborts.set(t.id, () => ctl.abort())
  try {
    await download(t, ctl)
  } finally {
    aborts.delete(t.id)
  }
}

async function download(t, ctl) {
  const rule = await addHeaderRule(t.url, t.page?.url)
  let res
  try {
    res = await fetch(t.url, { credentials: 'include', cache: 'no-store', redirect: 'follow', signal: ctl.signal })
  } catch (e) {
    throw new Error(t.cancelled ? '已取消' : '下载失败：' + (e.message || '网络错误'))
  } finally {
    dropHeaderRule(rule)
  }
  if (res.status === 401 || res.status === 403) throw new Error(`网站拒绝下载（${res.status}），可能需要先在这个网站登录`)
  if (!res.ok) throw new Error(`下载失败：网站返回 ${res.status}`)
  const type = (res.headers.get('content-type') || '').toLowerCase()
  if (/^(text\/html|application\/xhtml)/.test(type)) throw new Error('这个链接打开的是网页，不是电子书文件。可能要先登录，或者要在网页上再点一次下载')
  const total = +res.headers.get('content-length') || 0
  if (total > MAX_BROWSER) throw new Error('文件超过 1GB，请在插件设置里改成“让书库下载”')
  const name = pickName(t, res, type)
  t.name = name
  t.total = total
  // 一边下载一边记进度（这些调用也让浏览器知道后台还在忙，不会中途停掉）
  const reader = res.body.getReader()
  const chunks = []
  let got = 0, tick = 0
  for (;;) {
    const { done, value } = await reader.read().catch(e => { throw new Error(t.cancelled ? '已取消' : '下载中断：' + e.message) })
    if (done) break
    chunks.push(value)
    got += value.length
    if (got > MAX_BROWSER) { ctl.abort(); throw new Error('文件超过 1GB，请在插件设置里改成“让书库下载”') }
    if (Date.now() - tick > 400) {
      tick = Date.now()
      t.bytes = got
      t.progress = total ? got / total : 0
      persist()
    }
  }
  if (!got) throw new Error('下载到的文件是空的')
  const blob = new Blob(chunks)
  // 有的网站出错时返回 200 + 网页（只看文件开头：EPUB 这类 zip 包里也会有 <html>）
  if (await looksLikeHTML(blob)) throw new Error('下载到的是网页，不是电子书文件。可能要先登录，或者要在网页上再点一次下载')
  t.status = 'uploading'
  t.bytes = got
  t.progress = 1
  persist()
  const r = await api('/api/upload?path=' + encodeURIComponent(name), {
    method: 'POST', body: blob, headers: { 'Content-Type': 'application/octet-stream' }, timeout: 30 * 60 * 1000,
  })
  t.status = 'done'
  t.bytes = r?.size || got
  persist()
  notify(true, t)
}

async function looksLikeHTML(blob) {
  const b = new Uint8Array(await blob.slice(0, 512).arrayBuffer())
  if (b[0] === 0x50 && b[1] === 0x4b) return false // zip（EPUB、CBZ……）
  const text = new TextDecoder().decode(b).replace(/^\uFEFF/, '').trimStart().toLowerCase()
  return /^(<\?xml[^>]*>\s*)?(<!doctype html|<html[\s>]|<head[\s>]|<body[\s>])/.test(text)
}

/**
 * 决定保存的文件名：网站给的文件名 → 链接里的文件名 → 书名 + 扩展名。
 * 文件名没意义（pg1342-images.epub、download.pdf）而知道书名时用书名。入库时书库还会按元数据重新命名。
 */
function pickName(t, res, type) {
  const cands = [filenameFromDisposition(res.headers.get('content-disposition') || '')]
  try { cands.push(decodeURIComponent(new URL(res.url).pathname.split('/').pop() || '')) } catch {}
  cands.push(t.name)
  const file = cands.find(n => n && isBookName(n)) || ''
  const ext = file ? fullExt(file) : fileExt(extFromType(type) || (t.fmt && t.fmt !== 'auto' ? t.fmt : ''))
  if (!ext || !isBookName('x.' + ext)) throw new Error('看不出这个文件是什么格式' + (type ? `（${type.split(';')[0]}）` : ''))
  const generic = !file || /^(pg)?\d+([-_.]|$)|^(download|file|book|ebook|get|index|dl)\b/i.test(file)
  if (generic && t.title) return safeFileName(t.title) + '.' + ext
  if (file) return safeFileName(file)
  return safeFileName(t.page?.title || '下载的书') + '.' + ext
}

/** 文件名的完整扩展名：保留 .kepub.epub、.fb2.zip */
function fullExt(name) {
  const n = name.toLowerCase()
  if (n.endsWith('.fb2.zip')) return 'fb2.zip'
  if (n.endsWith('.kepub.epub')) return 'kepub.epub'
  return extOf(n)
}

const sleep = ms => new Promise(r => setTimeout(r, ms))

// 书库的出错说明是写给网页“从链接添加”的，里面建议“用浏览器插件添加”，在插件里换个说法
function serverError(msg) {
  msg = String(msg || '书库下载失败')
  if (msg.startsWith('这个链接打开的是网页')) return '书库打开这个链接得到的是网页，不是电子书文件（可能要先登录这个网站）'
  return msg.replace(/[，,]\s*(?:或者|可以)?用浏览器插件添加/g, '')
}

// ---------- 通知：一批任务结束后合并成一条 ----------

let doneBatch = [], failBatch = [], notifyTimer = 0
function notify(ok, t) {
  (ok ? doneBatch : failBatch).push(t)
  clearTimeout(notifyTimer)
  notifyTimer = setTimeout(() => {
    const show = (title, message) => chrome.notifications.create({ type: 'basic', iconUrl: 'icons/128.png', title, message, priority: 0 }).catch(() => {})
    if (doneBatch.length === 1) show('已加入书库', `${doneBatch[0].name || doneBatch[0].title}，书库正在整理`)
    else if (doneBatch.length > 1) show(`已加入书库 ${doneBatch.length} 个文件`, doneBatch.map(x => x.name || x.title).slice(0, 4).join('\n'))
    if (failBatch.length === 1) show('没能加入书库', `${failBatch[0].name || failBatch[0].title || failBatch[0].url}\n${failBatch[0].error}`)
    else if (failBatch.length > 1) show(`${failBatch.length} 个文件没能加入书库`, '打开插件查看原因')
    doneBatch = []
    failBatch = []
  }, 1500)
}

// ---------- 消息（弹窗、设置页） ----------

chrome.runtime.onMessage.addListener((msg, sender, reply) => {
  (async () => {
    await ready
    switch (msg.type) {
      case 'add':
        return { ok: true, count: await add(msg.items || [], msg.page) }
      case 'retry': {
        const t = tasks.find(x => x.id === msg.id)
        if (t && !ACTIVE.includes(t.status)) {
          Object.assign(t, { status: 'queued', error: '', note: '', progress: 0, bytes: 0, total: 0, cancelled: false, jobId: '', triedBrowser: false, mode: msg.mode || '', forced: !!msg.mode })
          persist(true)
          pump()
        }
        return { ok: true }
      }
      case 'cancel': {
        const t = tasks.find(x => x.id === msg.id)
        if (t && ACTIVE.includes(t.status)) {
          t.cancelled = true
          if (t.status === 'queued') { t.status = 'error'; t.error = '已取消' }
          aborts.get(t.id)?.()
          persist(true)
        }
        return { ok: true }
      }
      case 'clear':
        tasks = tasks.filter(t => ACTIVE.includes(t.status))
        persist(true)
        return { ok: true }
    }
    return { ok: false }
  })().then(reply, e => reply({ ok: false, error: e.message || String(e) }))
  return true
})

// ---------- 右键菜单 ----------

chrome.runtime.onInstalled.addListener(details => {
  chrome.contextMenus.removeAll(() => {
    chrome.contextMenus.create({ id: 'add-link', title: '添加到书库', contexts: ['link'] })
  })
  chrome.action.setBadgeBackgroundColor({ color: '#2F6F8F' })
  if (details.reason === 'install') chrome.runtime.openOptionsPage()
})
chrome.runtime.onStartup.addListener(() => chrome.action.setBadgeBackgroundColor({ color: '#2F6F8F' }))

// ---------- 插件的新版本：浏览器启动时、之后每 6 小时检查一次 ----------

async function checkUpdate() {
  const c = await getConfig()
  if (!isConfigured(c)) return
  hasUpdate = !!(await checkExtensionUpdate())
  persist(true)
}
// 后台每次被唤醒都会跑到这里：已经有这个定时就别重建，免得一直推迟
chrome.alarms.get('check-update').then(a => { if (!a) chrome.alarms.create('check-update', { delayInMinutes: 1, periodInMinutes: 360 }) })
chrome.alarms.onAlarm.addListener(a => { if (a.name === 'check-update') checkUpdate() })
chrome.storage.onChanged.addListener((ch, area) => {
  if (area === 'local' && 'update' in ch) { hasUpdate = !!ch.update.newValue; persist(true) }
})
chrome.storage.local.get('update').then(s => { hasUpdate = !!s.update; persist(true) })

chrome.contextMenus.onClicked.addListener(async (info, tab) => {
  if (info.menuItemId !== 'add-link' || !info.linkUrl) return
  const page = { url: tab?.url || info.pageUrl || '', title: tab?.title || '' }
  const fmt = formatOfURL(info.linkUrl)
  let file = ''
  try { file = decodeURIComponent(new URL(info.linkUrl).pathname.split('/').pop() || '') } catch {}
  // 链接文字（选中的文字）能当书名就用上
  const text = (info.selectionText || '').trim()
  try {
    const n = await add([{ url: info.linkUrl, fmt, file: isBookName(file) ? file : '', title: text.length >= 2 && text.length <= 120 ? text : '' }], page)
    if (!n) chrome.notifications.create({ type: 'basic', iconUrl: 'icons/128.png', title: '书库', message: '这个链接已经在加入了' })
  } catch (e) {
    chrome.notifications.create({ type: 'basic', iconUrl: 'icons/128.png', title: '没能加入书库', message: e.message })
    if (e instanceof AuthError || /还没有连接/.test(e.message)) chrome.runtime.openOptionsPage()
  }
})
