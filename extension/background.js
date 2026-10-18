// 后台：右键菜单、加入书库的任务队列。
// 两种加入方式：
//   浏览器下载后上传：用浏览器下载（带着你在这个网站的登录状态），再传到书库的投递目录。
//     下载和上传都在离屏页面（offscreen.html）里做：后台（service worker）里一个请求 30 秒还没响应，
//     浏览器就可能把后台停掉，大文件上传会断；离屏页面不受影响，进度用消息传回后台
//   让书库下载：把链接交给书库（POST /api/fetch），NAS 自己下载，浏览器关了也不影响
// 默认“自动”：公开书库让书库下载，其他网站用浏览器下载；
// 浏览器下载时发现文件很大、又不用登录就能下载（比如公开的直链），改让书库下载。
// 后台随时可能被浏览器停掉、再被叫醒：任务状态都存在 storage.session 里，醒来后接着做。

import { api, getConfig, isConfigured, isPublicLibrary, isBookName, safeFileName, fmtBytes, resolveRoute, loginExpired, AuthError, NetError, AdminError, ADMIN_ONLY, checkExtensionUpdate } from './lib/shuku.js'
import { formatOfURL } from './lib/detect.js'

const PARALLEL = 2
const KEEP = 40
const ACTIVE = ['queued', 'downloading', 'uploading', 'fetching']
const BUSY = ['downloading', 'uploading', 'fetching']
const OFFSCREEN = 'offscreen.html'
const TUNE = {
  big: 50 * 1024 * 1024, // 自动方式下，浏览器下载的文件超过这么大、不用登录也能下载，就改让书库下载
  poll: 1500, // 查询书库下载进度的间隔
  retry: [3000, 6000, 12000], // 查询时连不上书库，隔多久再查；还连不上就每分钟查一次（定时器叫醒后台）
  stall: 30 * 60 * 1000, // 书库的下载任务这么久没有进展（包括一直连不上书库），就不再等了
  limit: 3 * 60 * 60 * 1000, // 书库的下载任务最多等这么久（正常情况下书库一小时内就会下完或者放弃）
  idle: 60000, // 离屏页面闲这么久就关掉
}
// 测试时在后台里设置 globalThis.shukuTune 可以把这些数改小
const tune = k => globalThis.shukuTune?.[k] ?? TUNE[k]

// 任务每次（重新）开始时清空的字段
const FRESH = {
  status: 'queued', error: '', admin: false, note: '', wait: '', progress: 0, bytes: 0, total: 0, cancelled: false,
  jobId: '', jobStatus: '', began: 0, seen: 0, idle: 0, parked: false, // 让书库下载：书库的任务、等了多久
  xfer: '', heard: 0, rule: 0, // 浏览器下载：离屏页面里的任务、最后一次收到它的消息、来源页规则
  triedBrowser: false, triedNas: false,
}

let tasks = []
let hasUpdate = false
const live = new Set() // 这次后台启动后正在处理的任务 id
const waiters = new Map() // 离屏任务的键 → 等它结束的回调
const results = new Map() // 离屏任务的键 → 先到了、还没人等的结束消息
const wakers = new Map() // 任务 id → 提前结束等待（取消时用）
const ready = init()

async function init() {
  const s = await chrome.storage.session.get('tasks')
  tasks = s.tasks || []
  // 清掉上次留下的请求头规则
  try {
    const old = await chrome.declarativeNetRequest.getSessionRules()
    if (old.length) await chrome.declarativeNetRequest.updateSessionRules({ removeRuleIds: old.map(r => r.id) })
  } catch {}
  for (const t of tasks) t.rule = 0
  // 浏览器下载的任务在离屏页面里，后台被停掉不影响它：问问离屏页面哪些还在做、哪些刚做完
  const xfers = tasks.filter(t => t.status === 'downloading' || t.status === 'uploading')
  const off = xfers.length ? await offscreenJobs() : null
  for (const t of xfers) {
    const end = off?.ended.find(m => m.key === t.xfer)
    if (end) results.set(t.xfer, end)
    else if (!t.xfer) t.status = 'queued' // 还没开始下载：重新排队
    else if (!off?.jobs.includes(t.xfer)) Object.assign(t, { status: 'error', error: '浏览器中断了这个任务，请重试', xfer: '' })
  }
  // 交给书库之前后台就被停掉了：重新排队（书库认得同一个链接，不会下载两遍）
  for (const t of tasks) if (t.status === 'fetching' && !t.jobId) t.status = 'queued'
  persist()
  for (const t of tasks) if (BUSY.includes(t.status)) start(t, true)
  pump()
}

// ---------- 任务列表 ----------

let persistTimer = 0
function persist(now = false) {
  clearTimeout(persistTimer)
  const write = () => {
    // 只保留最近 KEEP 条，先去掉最早结束的
    let extra = tasks.length - KEEP
    if (extra > 0) tasks = tasks.filter(t => ACTIVE.includes(t.status) || extra-- <= 0)
    chrome.storage.session.set({ tasks })
    const n = tasks.filter(t => ACTIVE.includes(t.status)).length
    // 没有正在加入的书时，插件有新版本就在图标上显示“新”
    chrome.action.setBadgeText({ text: n ? String(n) : hasUpdate ? '新' : '' })
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
    forced: false,
    at: Date.now(),
    ...FRESH,
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
  while (tasks.filter(t => BUSY.includes(t.status)).length < PARALLEL) {
    const t = tasks.find(x => x.status === 'queued')
    if (!t) break
    t.status = 'downloading'
    start(t)
  }
  watch()
}

/** 开始处理一个任务；resume：后台重启后接着处理（书库的任务接着查，离屏页面里的接着等） */
function start(t, resume = false) {
  if (live.has(t.id)) return
  live.add(t.id)
  run(t, resume).catch(e => fail(t, e)).finally(() => {
    live.delete(t.id)
    persist()
    pump()
  })
}

function fail(t, e) {
  if (t.status === 'done') return
  dropRule(t)
  // admin：家人账号配对的插件不能往书库里加书，弹窗里不给“重试”，给“重新配对”
  Object.assign(t, { status: 'error', error: t.cancelled ? '已取消' : (e?.message || String(e)), admin: !t.cancelled && e instanceof AdminError, wait: '', parked: false, xfer: '' })
  persist()
  if (!t.cancelled) notify(false, t)
}

/** 这个错误说明了原因，不用再换一种方式试 */
const final = msg => Object.assign(new Error(msg), { final: true })

async function run(t, resume) {
  const pref = (await getConfig()).mode || 'auto'
  // 用户点了“让书库下载”“用浏览器下载”重试的，就按他选的来，不再自动换
  const auto = pref === 'auto' && !t.forced
  // ia: 只能让书库下载（书库去互联网档案馆挑格式）
  t.mode ||= t.url.startsWith('ia:') ? 'nas' : pref === 'auto' ? (isPublicLibrary(t.url) ? 'nas' : 'browser') : pref
  for (;;) {
    if (t.mode === 'nas') {
      try {
        return await viaNas(t, resume)
      } catch (e) {
        // 自动方式下书库下载失败（比如 NAS 访问不了这个网站），换浏览器再试一次；
        // 连不上书库、登录失效、不是管理员账号（403）的，换一种方式也没用
        if (!auto || t.cancelled || e instanceof AuthError || e instanceof NetError || e instanceof AdminError || e.final || t.url.startsWith('ia:') || t.triedBrowser) throw e
        t.triedBrowser = true
        t.mode = 'browser'
        t.note = '书库下载失败，改用浏览器下载'
      }
    } else {
      // 文件很大、不用登录也能下载的，改让书库下载（只换一次；书库下载失败会再换回浏览器）
      const r = await viaBrowser(t, auto && !t.triedBrowser && !t.triedNas, resume)
      if (r !== 'nas') return
      t.triedNas = true
      t.mode = 'nas'
      t.note = `文件有 ${fmtBytes(t.total)}，不用登录也能下载，改让书库直接下载`
    }
    resume = false
    Object.assign(t, { jobId: '', jobStatus: '', bytes: 0, total: 0, progress: 0 })
  }
}

// ---------- 让书库下载 ----------

async function viaNas(t, resume) {
  t.status = 'fetching'
  if (!resume || !t.jobId) {
    t.jobId = ''
    persist()
    // 书名给书库用来命名文件；没有可靠的书名就让书库用网站给的文件名
    const job = await api('/api/fetch', { method: 'POST', json: { url: t.url, name: t.title || '', source: '浏览器插件', referer: t.page?.url || '' } })
    Object.assign(t, { jobId: job.id, jobStatus: job.status, began: Date.now(), seen: Date.now(), idle: 0, parked: false })
  }
  persist()
  await pollJob(t)
}

const mins = ms => Math.max(1, Math.round(ms / 60000))

/**
 * 查询书库下载任务的进度。书库那边很久没有进展、或者一直连不上书库，就不再等（免得后台永远不休眠）；
 * 连不上时先隔几秒再查，还不行就停下来，每分钟由定时器叫醒查一次。
 */
async function pollJob(t) {
  let fails = 0
  t.began ||= Date.now()
  for (let first = true; ; first = false) {
    if (!first) await nap(t, fails ? tune('retry')[fails - 1] : tune('poll'))
    if (t.cancelled) {
      api('/api/fetch/' + encodeURIComponent(t.jobId), { method: 'DELETE' }).catch(() => {})
      throw new Error('已取消')
    }
    // “没有进展”只算插件在看着的时间：电脑睡眠、浏览器关着的那段不算（两次查询之间最多算 2 分钟）
    const now = Date.now()
    t.idle = (t.idle || 0) + Math.min(Math.max(now - (t.seen || now), 0), 2 * 60 * 1000)
    t.seen = now
    let list
    try {
      list = await api('/api/fetch', { timeout: 15000 })
    } catch (e) {
      // 连不上书库，或者书库正在重启（5xx）：过一会儿再查
      if (!(e instanceof NetError) && !(e.status >= 500)) throw e
      if (t.idle > tune('stall')) throw final(`连不上书库已经 ${mins(t.idle)} 分钟，插件不再查询进度。书库那边可能已经下好了，可以打开书库网页看看，或者点“重试”`)
      fails++
      if (fails <= tune('retry').length && !t.parked) {
        t.wait = `连不上书库，${Math.round(tune('retry')[fails - 1] / 1000)} 秒后再查`
        persist()
        continue
      }
      // 还是连不上：先停下来，后台可以休眠，每分钟由定时器叫醒再查一次（见 heartbeat）
      t.parked = true
      t.wait = '连不上书库，每分钟再查一次'
      persist()
      return
    }
    fails = 0
    t.parked = false
    t.wait = ''
    const j = (list || []).find(x => x.id === t.jobId)
    if (!j) throw new Error('书库里找不到这个下载任务（可能被取消了）')
    if (j.status !== t.jobStatus || (j.bytes || 0) !== t.bytes) t.idle = 0
    t.jobStatus = j.status
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
    const stuck = t.idle > tune('stall') ? `书库那边${j.status === 'queued' ? '一直在排队，' : ''}已经 ${mins(t.idle)} 分钟没有进展`
      : now - t.began > tune('limit') ? `书库已经下载了 ${mins(now - t.began)} 分钟还没下完` : ''
    if (stuck) {
      // 书库那边卡住了：取消书库里的这个任务（免得它一直占着），说明原因
      api('/api/fetch/' + encodeURIComponent(t.jobId), { method: 'DELETE' }).catch(() => {})
      throw final(`${stuck}，插件取消了这个下载。可以点“重试”，或者“用浏览器下载”`)
    }
    persist()
  }
}

/** 等一会儿；取消时提前醒来 */
function nap(t, ms) {
  return new Promise(resolve => {
    const done = () => {
      clearTimeout(timer)
      wakers.delete(t.id)
      resolve()
    }
    const timer = setTimeout(done, ms)
    wakers.set(t.id, done)
  })
}

// ---------- 浏览器下载后上传（在离屏页面里做） ----------

// 有的网站要检查来源页（Referer）才给下载：下载期间临时加一条请求头规则（只管插件自己发的请求）
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

function dropRule(t) {
  if (!t.rule) return
  chrome.declarativeNetRequest.updateSessionRules({ removeRuleIds: [t.rule] }).catch(() => {})
  t.rule = 0
}

/** 交给离屏页面下载、上传，等它做完。返回 'done'，或者 'nas'（文件大、不用登录：改让书库下载） */
async function viaBrowser(t, handoff, resume) {
  if (!resume || !t.xfer) {
    Object.assign(t, { status: 'downloading', bytes: 0, total: 0, progress: 0, heard: Date.now(), xfer: t.id + '.' + Math.random().toString(36).slice(2, 8) })
    persist()
    t.rule = await addHeaderRule(t.url, t.page?.url)
    if (t.cancelled) throw new Error('已取消')
    const job = { key: t.xfer, url: t.url, fmt: t.fmt, title: t.title, name: t.name, pageTitle: t.page?.title || '', big: handoff ? tune('big') : 0, idle: tune('idle') }
    try {
      await toOffscreen({ type: 'start', job })
    } catch (e) {
      throw new Error('浏览器没能开始下载：' + (e.message || e))
    }
  }
  return waitBrowser(t)
}

function waitBrowser(t) {
  const key = t.xfer
  const early = results.get(key)
  if (early) {
    results.delete(key)
    return outcome(t, early)
  }
  return new Promise(resolve => waiters.set(key, resolve)).then(m => outcome(t, m))
}

/** 离屏页面里的任务结束了 */
async function outcome(t, m) {
  dropRule(t)
  t.xfer = ''
  if (m.stage === 'handoff') {
    t.total = m.total || 0
    if (m.name) t.name = m.name
    return 'nas'
  }
  if (m.stage === 'done') {
    Object.assign(t, { status: 'done', name: m.name || t.name, bytes: m.size || t.bytes, total: m.size || t.total, progress: 1 })
    persist()
    notify(true, t)
    return 'done'
  }
  if (m.auth) throw isConfigured(await getConfig()) ? await loginExpired() : new AuthError('还没有连接书库，请先在插件设置里连接')
  if (m.admin) throw new AdminError(ADMIN_ONLY)
  throw new Error(m.error || '浏览器下载失败')
}

/** 收到离屏任务的结束消息（也可能是后台自己判断它断了） */
function settle(key, m) {
  const w = waiters.get(key)
  if (w) {
    waiters.delete(key)
    w(m)
  } else if (tasks.some(t => t.xfer === key)) {
    results.set(key, m)
  }
}

/** 离屏页面发来的进度和结果 */
function onXfer(m) {
  const t = tasks.find(x => x.xfer && x.xfer === m.key)
  if (!t) return
  t.heard = Date.now()
  dropRule(t) // 网站已经回应了，来源页规则可以去掉
  if (m.stage !== 'downloading' && m.stage !== 'uploading') return settle(m.key, m)
  if (t.status !== 'downloading' && t.status !== 'uploading') return
  Object.assign(t, { status: m.stage, bytes: m.bytes || 0, total: m.total || 0 })
  t.progress = t.total ? t.bytes / t.total : 0
  if (m.name) t.name = m.name
  persist()
}

/** 离屏页面要上传了：告诉它现在走哪条线路、用什么令牌 */
async function uploadTarget(msg) {
  const t = tasks.find(x => x.xfer && x.xfer === msg.key)
  if (!t || t.cancelled) return { error: '已取消' }
  const c = await getConfig()
  if (!isConfigured(c)) return { error: '还没有连接书库，请先在插件设置里连接', auth: true }
  const r = await resolveRoute(!!msg.fresh)
  if (!r) return { error: '连不上书库（内网和 Tailscale 都不通）', offline: true }
  return { base: r.base, token: c.token }
}

async function cancelXfer(t) {
  const key = t.xfer
  if (!key) return
  const r = await chrome.runtime.sendMessage({ to: 'offscreen', type: 'cancel', key }).catch(() => null)
  if (!r?.ok) settle(key, { stage: 'error', error: '已取消' })
}

// 离屏页面的打开、关闭排队做，免得刚要交任务时它被关掉
let queue = Promise.resolve()
function serial(fn) {
  const p = queue.then(fn, fn)
  queue = p.catch(() => {})
  return p
}

async function hasOffscreen() {
  const c = await chrome.runtime.getContexts({ contextTypes: ['OFFSCREEN_DOCUMENT'], documentUrls: [chrome.runtime.getURL(OFFSCREEN)] })
  return c.length > 0
}

function toOffscreen(msg) {
  return serial(async () => {
    if (!(await hasOffscreen())) {
      await chrome.offscreen.createDocument({ url: OFFSCREEN, reasons: ['BLOBS'], justification: '在后台下载电子书、上传到书库（大文件要传好几分钟，放在后台里会被浏览器中断）' })
    }
    // 离屏页面刚打开时可能还没开始收消息，稍等再发
    for (let i = 0; ; i++) {
      const r = await chrome.runtime.sendMessage({ to: 'offscreen', ...msg }).catch(e => { if (i >= 30) throw e })
      if (r?.ok) return r
      if (i >= 30) throw new Error('离屏页面没有响应')
      await sleep(100)
    }
  })
}

/** 离屏页面里还在做的任务和刚结束的任务；没有离屏页面时返回 null */
async function offscreenJobs() {
  try {
    if (!(await hasOffscreen())) return null
    const r = await chrome.runtime.sendMessage({ to: 'offscreen', type: 'list' })
    return r?.jobs ? r : null
  } catch { return null }
}

/** 离屏页面闲了一分钟：没有任务就关掉 */
function closeIdle() {
  return serial(async () => {
    if (tasks.some(t => t.xfer)) return
    const off = await offscreenJobs()
    if (off && !off.jobs.length) await chrome.offscreen.closeDocument().catch(() => {})
  })
}

// ---------- 定时检查：有任务时每分钟一次 ----------

let watching = null
async function watch() {
  const need = tasks.some(t => BUSY.includes(t.status))
  if (need === watching) return
  watching = need
  if (!need) return chrome.alarms.clear('watch')
  const a = await chrome.alarms.get('watch')
  if (!a && watching) chrome.alarms.create('watch', { periodInMinutes: 1 })
}

/** 接着查停下来等的书库任务；离屏页面意外没了（比如崩溃）的任务，不再干等 */
async function heartbeat() {
  watching = null // 按实际情况重新决定要不要这个定时器（没有任务了就去掉）
  for (const t of tasks) if (t.status === 'fetching' && !live.has(t.id)) start(t, true)
  const quiet = tasks.filter(t => t.xfer && Date.now() - (t.heard || 0) > 60000)
  if (quiet.length) {
    const off = await offscreenJobs()
    for (const t of quiet) {
      if (off?.jobs.includes(t.xfer)) continue
      settle(t.xfer, off?.ended.find(m => m.key === t.xfer) || { stage: 'error', error: '浏览器中断了这个任务，请重试' })
    }
  }
  watch()
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

// ---------- 消息（弹窗、设置页、离屏页面） ----------

chrome.runtime.onMessage.addListener((msg, sender, reply) => {
  if (msg?.to === 'offscreen' || sender.id !== chrome.runtime.id) return
  (async () => {
    await ready
    switch (msg.type) {
      case 'add':
        return { ok: true, count: await add(msg.items || [], msg.page) }
      case 'retry': {
        const t = tasks.find(x => x.id === msg.id)
        if (t && !ACTIVE.includes(t.status)) {
          Object.assign(t, FRESH, { mode: msg.mode || '', forced: !!msg.mode })
          persist(true)
          pump()
        }
        return { ok: true }
      }
      case 'cancel': {
        const t = tasks.find(x => x.id === msg.id)
        if (t && ACTIVE.includes(t.status)) {
          t.cancelled = true
          if (t.status === 'queued') Object.assign(t, { status: 'error', error: '已取消' })
          wakers.get(t.id)?.() // 正在等下一次查询的，马上处理
          if (t.xfer) cancelXfer(t)
          if (t.status === 'fetching' && !live.has(t.id)) start(t, true) // 停下来等的书库任务
          persist(true)
        }
        return { ok: true }
      }
      case 'clear':
        tasks = tasks.filter(t => ACTIVE.includes(t.status))
        persist(true)
        return { ok: true }
      case 'xfer':
        onXfer(msg)
        return { ok: true }
      case 'upload-target':
        return uploadTarget(msg)
      case 'offscreen-idle':
        closeIdle()
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
chrome.alarms.onAlarm.addListener(a => {
  if (a.name === 'check-update') checkUpdate()
  if (a.name === 'watch') ready.then(heartbeat)
})
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
