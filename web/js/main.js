import { h, $, clear, fill, add, icon, iconBtn, toast, toastError, store, save, closeMenu, isEInk, applyEInk } from './ui.js'
import { api, auth, net, deviceName, setUnauthorizedHandler } from './api.js'
import { renderLibrary } from './library.js'
import { renderSeriesList } from './series.js'
import { openBook } from './book.js'
import { renderOrganize } from './organize.js'
import { renderSettings } from './settings.js'
import { initUploads, pickFiles } from './upload.js'
import { renderStats } from './stats.js'
import { renderDiscover } from './discover.js'
import { renderNotes } from './notes.js'
import { offlineSupported, renderOffline } from './offline.js'
import { isNative, call, onBack } from './native.js'

const app = $('#app')

export const ctx = {
  facets: null,
  server: { name: '我的书库' },
  appPrefs: store('shuku.app', { appearance: 'system' }),
  user: null,        // 当前登录的人：{ id, name, role, libraries }
  isAdmin: false,    // 管理员管书和设置；家人只读
  async refreshFacets() {
    try { ctx.facets = await api('/api/facets'); renderNav() } catch {}
    return ctx.facets
  },
  openBook: (id) => openBook(id, ctx),
  navigate: (hash) => { markFresh(); location.hash = hash },
  rerender: () => route(),
  pickFiles,
}

// 当前的人：记在本机，断网打开离线书架时也知道是谁
const USER_KEY = 'shuku.user'
function setUser(u) {
  ctx.user = u ? { id: u.id, name: u.name, role: u.role, libraries: u.libraries || [] } : null
  ctx.isAdmin = ctx.user?.role === 'admin'
  document.body.classList.toggle('member', !!ctx.user && !ctx.isAdmin)
  if (ctx.user) save(USER_KEY, ctx.user)
}

// ---------- 深浅色 ----------

const darkMQ = matchMedia('(prefers-color-scheme: dark)')
// 阅读器里手动开了夜间模式（10 小时内有效）时，书架也用深色
function readerNightOn() {
  try { const o = JSON.parse(localStorage.getItem('shuku.nightOverride') || 'null'); return !!(o && o.on && Date.now() - o.at < 10 * 3600e3) } catch { return false }
}
export function applyAppearance() {
  const mode = ctx.appPrefs.appearance
  // 墨水屏上“跟随系统”一律用浅色（深色只在自己选了“深色”时用）
  const eink = isEInk()
  const dark = (readerNightOn() && !eink) || mode === 'dark' || (mode === 'system' && !eink && darkMQ.matches)
  document.documentElement.dataset.theme = dark ? 'dark' : 'light'
  $('#theme-color').content = dark ? '#14181E' : '#F8F8F6'
  if (!document.querySelector('.reader')) call('setThemeColor', dark ? '#14181E' : '#F8F8F6', dark)
}
darkMQ.addEventListener('change', applyAppearance)
ctx.setAppearance = (mode) => { ctx.appPrefs.appearance = mode; save('shuku.app', ctx.appPrefs); applyAppearance() }
// 墨水屏模式（auto / on / off）、界面字号（auto 或倍数）
ctx.setDisplay = (k, v) => { ctx.appPrefs[k] = v; save('shuku.app', ctx.appPrefs); applyEInk(ctx.appPrefs); applyAppearance() }

// ---------- 登录 ----------
// 只有一个人：只输密码；家里有好几个人：先点自己的名字，再输密码（记住上次选的人）

const LAST_USER = 'shuku.lastUser'

async function showAuth(state) {
  const setup = !state.setup
  const users = state.users || []
  let picked = null
  if (users.length > 1) picked = users.find(u => String(u.id) === localStorage.getItem(LAST_USER)) || null

  function draw() {
    const err = h('p', { class: 'err', role: 'alert' })
    const choose = !setup && users.length > 1 && !picked
    if (choose) {
      fill(app, h('div', { class: 'auth' }, h('div', { class: 'auth-box' },
        h('h1', null, '书库'),
        h('p', null, state.name ? `${state.name} · 你是谁？` : '你是谁？'),
        h('div', { class: 'who', role: 'group', 'aria-label': '选择你的名字' }, users.map(u => h('button', {
          class: 'who-btn', type: 'button', 'aria-label': u.name + (u.admin ? '（管理员）' : ''),
          onclick: () => { picked = u; draw() },
        }, h('span', { class: 'av', 'aria-hidden': 'true' }, [...u.name][0] || '?'), h('span', null, u.name), u.admin && u.name !== '管理员' ? h('small', null, '管理员') : null))))))
      app.querySelector('.who-btn')?.focus()
      return
    }
    const name = setup ? h('input', { class: 'input', value: '管理员', autocomplete: 'username', maxlength: 20 }) : null
    const pw = h('input', { class: 'input', type: 'password', autocomplete: setup ? 'new-password' : 'current-password', placeholder: setup ? '至少 6 位' : '', required: true, 'aria-label': '密码' })
    const pw2 = setup ? h('input', { class: 'input', type: 'password', autocomplete: 'new-password', required: true, 'aria-label': '再输入一次密码' }) : null
    const btn = h('button', { class: 'btn primary', type: 'submit' }, setup ? '设置密码并进入' : '进入书库')
    const form = h('form', null,
      h('h1', null, '书库'),
      setup ? h('p', null, '第一次使用，先设置管理员的名字和密码。手机、手表登录都用它；以后可以在“设置 → 家人账号”里给家人各开一个账号。')
        : picked ? h('p', { class: 'hello' }, h('span', { class: 'av', 'aria-hidden': 'true' }, [...picked.name][0]), `${picked.name}，输入你的密码`,
          h('button', { class: 'linkish', type: 'button', onclick: () => { picked = null; localStorage.removeItem(LAST_USER); draw() } }, '换一个人'))
          : h('p', null, state.name),
      setup ? h('label', { class: 'field' }, h('span', null, '名字'), name) : null,
      h('label', { class: 'field' }, h('span', null, setup ? '访问密码' : '密码'), pw),
      pw2 ? h('label', { class: 'field' }, h('span', null, '再输入一次'), pw2) : null,
      err, btn)
    form.onsubmit = async (e) => {
      e.preventDefault()
      err.textContent = ''
      if (setup && pw.value !== pw2.value) { err.textContent = '两次输入的密码不一样'; return }
      btn.disabled = true
      try {
        const body = setup ? { Password: pw.value, Device: deviceName, Name: name.value.trim() } : { Password: pw.value, Device: deviceName }
        if (picked) body.user_id = picked.id
        const r = await api(setup ? '/api/auth/setup' : '/api/auth/login', { method: 'POST', body })
        if (picked) localStorage.setItem(LAST_USER, String(picked.id))
        auth.set(r.token)
        start()
      } catch (e2) { err.textContent = e2.message; btn.disabled = false }
    }
    fill(app, h('div', { class: 'auth' }, form))
    ;(name || pw).focus()
  }
  draw()
}

setUnauthorizedHandler(() => { auth.set(''); boot() })

// 退出登录（设置页用）
ctx.logout = async () => {
  await api('/api/auth/logout', { method: 'POST' }).catch(() => {})
  auth.set('')
  try { localStorage.removeItem(USER_KEY) } catch {}
  location.hash = ''
  location.reload()
}

// ---------- 外壳：侧栏 + 手机底栏 ----------

let shell, mainEl, navEl, tabbarEl, netEls = []

function netLine() {
  const el = h('div', { class: 'netline', title: '点击重新检测线路', role: 'button', tabindex: 0, onclick: async () => { await net.resolve(true); toast('当前线路：' + net.label()) } }, h('i'), h('span'))
  netEls.push(el)
  updateNet()
  return el
}
function updateNet() {
  for (const el of netEls) {
    el.className = 'netline ' + ({ remote: 'remote', offline: 'off' }[net.route] || '')
    el.lastChild.textContent = isNative && net.route === 'offline' ? '离线 · 手机里的书' : net.label()
  }
  document.body.classList.toggle('offline', net.route === 'offline')
}
net.on(updateNet)
// App 里断网 / 恢复网络时，书架在“手机里的书”和书库之间切换
let lastRoute = null
net.on(route => {
  const wasOff = lastRoute === 'offline', isOff = route === 'offline'
  if (isNative && lastRoute !== null && wasOff !== isOff && shell && document.body.contains(shell)) {
    ctx.refreshFacets(); currentView?.refresh?.()
    toast(isOff ? '连不上书库，现在显示下载到手机的书' : '已连上书库：' + net.label())
  }
  lastRoute = route
})

function buildShell() {
  netEls = []
  navEl = h('nav', { class: 'nav', 'aria-label': '导航' })
  tabbarEl = h('nav', { class: 'tabbar', 'aria-label': '导航' })
  mainEl = h('main', { class: 'main', id: 'main' })
  const side = h('aside', { class: 'side' },
    h('div', { class: 'brand' }, h('b', null, '书库'), h('span', { id: 'server-name' }, ctx.server.name)),
    navEl,
    h('div', { class: 'side-foot' }, ctx.user && ctx.user.name ? h('a', { class: 'side-user', href: '#/settings', title: '账号和设置' }, h('span', { class: 'av', 'aria-hidden': 'true' }, [...ctx.user.name][0]), h('span', null, ctx.user.name)) : null,
      h('div', { id: 'side-status', 'aria-live': 'polite' }), netLine()))
  const top = h('div', { class: 'mobile-top' }, h('b', null, '书库'), netLine())
  shell = h('div', { class: 'shell' }, side, h('div', null, top, mainEl), tabbarEl)
  fill(app, shell)
  renderNav()
}

const curPath = () => (location.hash || '#/').split('?')[0]

function navLink(hash, ic, label, extra) {
  const on = curPath() === hash
  return h('a', { href: hash, class: on ? 'on' : null, 'aria-current': on ? 'page' : null }, icon(ic), h('span', null, label), extra)
}

function renderNav() {
  if (!navEl) return
  const f = ctx.facets || {}
  const kinds = Object.fromEntries((f.kinds || []).map(k => [k.name, k.count]))
  const cnt = n => n ? h('span', { class: 'count' }, n) : null
  const admin = ctx.isAdmin
  const pending = admin ? (f.review || 0) + (f.suggestions || 0) : 0
  fill(navEl, ...[
    navLink('#/', 'shelf', '书架', cnt(f.total)),
    navLink('#/reading', 'reading', '在读', cnt(f.reading)),
    navLink('#/want', 'heart', '想读', cnt(f.want)),
    navLink('#/notes', 'notes', '笔记'),
    navLink('#/stats', 'activity', '阅读统计'),
    navLink('#/books', 'book', '图书', cnt(kinds.book)),
    navLink('#/comics', 'comic', '漫画', cnt(kinds.comic)),
    navLink('#/series', 'stack', '系列', cnt(f.series)),
    offlineSupported ? navLink('#/offline', 'saved', '离线书架') : null,
    admin ? navLink('#/discover', 'compass', '发现') : null,
    (f.categories || []).length ? h('div', { class: 'nav-group' }, '分类') : null,
    (f.categories || []).filter(c => c.name !== '漫画').map(c => navLink('#/cat/' + encodeURIComponent(c.name), 'folder', c.name, cnt(c.count))),
    h('div', { class: 'nav-group' }, admin ? '管理' : '我的'),
    admin ? navLink('#/organize', 'sparkle', '整理', pending ? h('span', { class: 'badge warn' }, pending) : null) : null,
    navLink('#/settings', 'settings', '设置'),
  ].flat().filter(Boolean))
  const cur = curPath()
  const shelfish = ['#/', '#/books', '#/comics', '#/all', '#/want', '#/series'].includes(cur) || cur.startsWith('#/cat/') || cur.startsWith('#/series/') || cur.startsWith('#/author/')
  const tab = (hash, ic, label, badge) => {
    const on = cur === hash || (hash === '#/' && shelfish)
    return h('a', { href: hash, class: on ? 'on' : null, 'aria-current': on ? 'page' : null }, icon(ic), h('span', null, label), badge ? h('span', { class: 'badge warn' }, badge) : null)
  }
  fill(tabbarEl, ...(admin
    ? [tab('#/', 'shelf', '书架'), tab('#/reading', 'reading', '在读'), tab('#/discover', 'compass', '发现'), tab('#/organize', 'sparkle', '整理', pending || null), tab('#/settings', 'settings', '设置')]
    : [tab('#/', 'shelf', '书架'), tab('#/reading', 'reading', '在读'), tab('#/notes', 'notes', '笔记'), tab('#/stats', 'activity', '统计'), tab('#/settings', 'settings', '设置')]))
}

// ---------- 路由 ----------
// 点链接、点按钮去别的页面是“新打开”；浏览器返回、从阅读器退出是“回来”，书架恢复原来的搜索词和滚动位置
let freshNav = false, freshTimer = 0
function markFresh() { freshNav = true; clearTimeout(freshTimer); freshTimer = setTimeout(() => { freshNav = false }, 800) }
document.addEventListener('click', e => { if (e.target.closest?.('a[href^="#"]')) markFresh() }, true)

let readerMod = null
let lastNonReader = '#/'
let currentView = null
let routeGen = 0

async function route() {
  const gen = ++routeGen
  // 这次是不是点链接打开的（打开阅读器也算，用掉这个标记；从阅读器退出回来时就是“回来”）
  const fresh = freshNav
  freshNav = false
  const hash = location.hash || '#/'
  const [path, qs] = hash.slice(1).split('?')
  const params = new URLSearchParams(qs || '')
  const parts = path.split('/').filter(Boolean).map(decodeURIComponent)
  closeMenu()

  if (parts[0] === 'read') {
    readerMod ||= await import('./reader/reader.js')
    currentView?.destroy?.()
    currentView = null
    const back = () => { location.hash = lastNonReader }
    readerMod.openReader(+parts[1], { ctx, params, back })
    return
  }
  readerMod?.closeReader?.()
  const restore = !fresh
  applyAppearance()
  // 管理页面家人打不开：回书架
  if (!ctx.isAdmin && ctx.user && ['organize', 'discover'].includes(parts[0])) { location.replace('#/'); return }
  lastNonReader = hash
  if (!shell || !document.body.contains(shell)) buildShell()
  renderNav()
  currentView?.destroy?.()
  currentView = null
  clear(mainEl)
  window.scrollTo(0, 0)
  const lib = (o) => () => renderLibrary(mainEl, { ...o, params, restore }, ctx)
  const views = {
    '': lib({ view: 'home' }),
    all: lib({ view: 'all' }),
    reading: lib({ view: 'reading' }),
    want: lib({ view: 'want' }),
    books: lib({ view: 'kind', kind: 'book' }),
    comics: lib({ view: 'kind', kind: 'comic' }),
    cat: lib({ view: 'cat', category: parts[1] }),
    author: lib({ view: 'author', author: parts[1] }),
    series: () => parts[1] ? renderLibrary(mainEl, { view: 'series', series: parts[1], params, restore }, ctx) : renderSeriesList(mainEl, params, ctx),
    notes: () => renderNotes(mainEl, params, ctx),
    offline: () => renderOffline(mainEl, ctx),
    organize: () => renderOrganize(mainEl, params, ctx),
    settings: () => renderSettings(mainEl, ctx),
    stats: () => renderStats(mainEl, ctx),
    discover: () => renderDiscover(mainEl, params, ctx),
  }
  const v = await (views[parts[0] || ''] || views[''])()
  // 等待期间又打开了别的页面（或者刷新了这一页）：这个视图已经没用了
  if (gen !== routeGen) { v?.destroy?.(); return }
  currentView = v
}

addEventListener('hashchange', route)

// ---------- 后台动态：入库完成后提示并刷新 ----------

let lastEvent = 0
let lastRefresh = 0, refreshPending = false, wasBusy = false, upgradeNoticed = false
// 入库、整理的动态都放在同一条提示里（key: 'import'）：只来一条时照原样显示（能点“查看”）；
// 连着来好几条（一次放进很多本书）时合成一条会更新的汇总——“已入库 N 本，M 本识别把握不大放进了待确认”，点“去整理”。
// 以前每条单独弹一个、每 6 秒弹一轮，每条停 7 秒，手机上提示条堆满屏幕
let importRun = null // 这一轮（60 秒内连着来的）动态的统计
function showEvents(evs) {
  if (!evs.length) return
  const now = Date.now()
  if (!importRun || now - importRun.at > 60000) importRun = { n: 0, ok: 0, review: 0, err: 0, other: 0, titles: [] }
  const r = importRun
  r.at = now
  // 从旧到新记下书名（同一秒入库好几本时，最新的那条不一定是刚加的那本，所以列最近的几本）
  for (const e of [...evs].reverse()) {
    const m = /《[^》]+》/.exec(e.message)
    if (m) r.titles = [m[0], ...r.titles.filter(x => x !== m[0])].slice(0, 3)
  }
  for (const e of evs) {
    r.n++
    if (/^已入库/.test(e.message)) r.ok++
    else if (/待确认/.test(e.message)) r.review++
    else if (e.level === 'error') r.err++
    else if (!/^已下载/.test(e.message)) r.other++ // “已下载，正在入库”后面跟着“已入库”，不单算
  }
  const latest = evs[0] // 新的在前
  if (r.n === 1) {
    toast(latest.message, { key: 'import', error: latest.level === 'error', ...(latest.book_id ? { action: { label: '查看', run: () => ctx.openBook(latest.book_id) } } : {}) })
    return
  }
  const parts = []
  if (r.ok) parts.push(`已入库 ${r.ok} 本`)
  if (r.review) parts.push(`${r.review} 本识别把握不大，放进了待确认`)
  if (r.err) parts.push(`${r.err} 个出错`)
  if (r.other) parts.push(`另有 ${r.other} 条动态`)
  let msg = parts.join('，') || latest.message
  if (r.titles.length) msg += `（最近：${r.titles.join('')}${r.ok + r.review > r.titles.length ? '等' : ''}）`
  const go = r.review || r.err ? { label: '去整理', run: () => ctx.navigate('#/organize') } : { label: '看书架', run: () => ctx.navigate('#/all') }
  toast(msg, { key: 'import', error: r.err > 0 && !r.ok && !r.review, action: go, duration: 10000 })
}

async function pollStatus() {
  if (!ctx.user) return
  try {
    const s = await api('/api/status')
    const busy = !!(s.inbox.pending || s.scan.running)
    const side = $('#side-status')
    if (side) {
      const parts = []
      if (ctx.isAdmin && s.inbox.pending) parts.push(`投递目录 ${s.inbox.pending} 个文件待处理`)
      if (ctx.isAdmin && s.scan.running) parts.push(`正在扫描书库（${s.scan.found}）`)
      side.textContent = parts.join('，')
    }
    if (lastEvent && s.last_event > lastEvent) {
      // 入库动态只有管理员看得到；家人只刷新书架
      if (ctx.isAdmin) showEvents(await api('/api/events?since=' + lastEvent))
      await ctx.refreshFacets()
      refreshPending = true
    }
    // 书架刷新：入库进行中最多 20 秒刷新一次；往下翻着看时先不刷新，免得跳回顶部；处理完再刷新一次
    const due = !busy || Date.now() - lastRefresh > 20000 || (wasBusy && !busy)
    if (refreshPending && due && scrollY < 600 && !document.querySelector('.sel-bar')) {
      refreshPending = false
      lastRefresh = Date.now()
      currentView?.refresh?.()
    }
    wasBusy = busy
    // 书库升级了（NAS 上的更新服务换了新版本）：提示刷新页面
    if (s.version && ctx.settings?.version && s.version !== ctx.settings.version && !upgradeNoticed) {
      upgradeNoticed = true
      toast(`书库已升级到 ${s.version}`, { action: { label: '刷新', run: () => location.reload() }, duration: 600000 })
    }
    lastEvent = s.last_event
  } catch {}
}

// ---------- 启动 ----------

async function start() {
  try {
    const [me, st] = await Promise.all([api('/api/me'), api('/api/settings')])
    setUser(me)
    ctx.server.name = st.server_name
    ctx.settings = st
    // 服务器上配置的内网 / Tailscale 地址同步到本机，用于自动切换
    const n = st.network || {}
    const cur = net.config
    if ((n.lan_url || '') !== cur.lan || (n.remote_url || '') !== cur.remote) net.setConfig({ lan: n.lan_url || '', remote: n.remote_url || '' })
  } catch (e) {
    if (e.status === 401) return
    if (!ctx.user) setUser(store(USER_KEY, {}).id ? store(USER_KEY, {}) : null)
  }
  document.title = ctx.server.name === '我的书库' ? '书库' : ctx.server.name
  await ctx.refreshFacets()
  buildShell()
  initUploads(ctx)
  route()
  pollStatus()
  // 重新登录时 start() 会再调用一次，计时器只建一个
  pollTimer ||= setInterval(pollStatus, 6000)
}
let pollTimer = 0

// 连不上书库时：浏览器里存过书的话直接打开离线书架
function offlineBoot(message) {
  const cached = store(USER_KEY, {})
  if (cached.id) setUser(cached)
  ctx.offline = true
  buildShell()
  const bar = h('div', { class: 'offline-bar', role: 'status' }, icon('offline'), h('span', null, message || '连不上书库，现在显示存在这台设备上的书'),
    h('button', { class: 'btn small', type: 'button', onclick: () => location.reload() }, '重新连接'))
  mainEl.before(bar)
  if (!location.hash.startsWith('#/read/')) location.replace('#/offline')
  route()
}

async function boot() {
  applyAppearance()
  await net.resolve()
  let st
  try { st = await api('/api/auth/state') } catch (e) {
    if (offlineSupported && e.status === 0) { offlineBoot(); return }
    fill(app, h('div', { class: 'auth' }, h('div', null, h('h1', { class: 'serif' }, '书库'), h('p', { class: 'muted' }, e.message), h('button', { class: 'btn', onclick: boot }, '重试'))))
    return
  }
  ctx.server.name = st.name
  if (!st.authed) { setUser(null); showAuth(st); return }
  if (st.user) setUser(st.user)
  start()
}

boot()

// App 里由原生代码缓存网页，不用 Service Worker
if (!isNative && 'serviceWorker' in navigator && (location.protocol === 'https:' || location.hostname === 'localhost')) {
  navigator.serviceWorker.register('/sw.js').catch(() => {})
}

// 安卓返回键：先关菜单、弹出的面板、多选，再退出阅读器，再回书架首页，最后才退出 App
onBack(() => {
  const esc = () => document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
  if (document.querySelector('.ctx-menu')) { closeMenu(); return true }
  if (document.querySelector('.dialog.show, .drawer.show, .sel-bar')) { esc(); return true }
  if (document.querySelector('.reader')) { esc(); return true }
  const hash = location.hash || '#/'
  if (hash !== '#/' && hash !== '#') { history.length > 1 ? history.back() : (location.hash = '#/'); return true }
  return false
})

export { toastError }
