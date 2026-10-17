import { h, $, clear, fill, add, icon, iconBtn, toast, toastError, store, save } from './ui.js'
import { api, auth, net, deviceName, setUnauthorizedHandler } from './api.js'
import { renderLibrary } from './library.js'
import { openBook } from './book.js'
import { renderOrganize } from './organize.js'
import { renderSettings } from './settings.js'
import { initUploads, pickFiles } from './upload.js'
import { renderStats } from './stats.js'

const app = $('#app')

export const ctx = {
  facets: null,
  server: { name: '我的书库' },
  appPrefs: store('shuku.app', { appearance: 'system' }),
  async refreshFacets() {
    try { ctx.facets = await api('/api/facets'); renderNav() } catch {}
    return ctx.facets
  },
  openBook: (id) => openBook(id, ctx),
  navigate: (hash) => { location.hash = hash },
  rerender: () => route(),
  pickFiles,
}

// ---------- 深浅色 ----------

const darkMQ = matchMedia('(prefers-color-scheme: dark)')
export function applyAppearance() {
  const mode = ctx.appPrefs.appearance
  const night = localStorage.getItem('shuku.night') === '1'
  const dark = night || mode === 'dark' || (mode === 'system' && darkMQ.matches)
  document.documentElement.dataset.theme = dark ? 'dark' : 'light'
  $('#theme-color').content = dark ? '#14181E' : '#F8F8F6'
}
darkMQ.addEventListener('change', applyAppearance)
ctx.setAppearance = (mode) => { ctx.appPrefs.appearance = mode; save('shuku.app', ctx.appPrefs); applyAppearance() }

// ---------- 登录 ----------

async function showAuth(state) {
  const setup = !state.setup
  const err = h('p', { class: 'err' })
  const pw = h('input', { class: 'input', type: 'password', autocomplete: setup ? 'new-password' : 'current-password', placeholder: setup ? '至少 6 位' : '', required: true })
  const pw2 = setup ? h('input', { class: 'input', type: 'password', autocomplete: 'new-password', required: true }) : null
  const btn = h('button', { class: 'btn primary', type: 'submit' }, setup ? '设置密码并进入' : '进入书库')
  const form = h('form', null,
    h('h1', null, '书库'),
    h('p', null, setup ? '第一次使用，先设置一个访问密码。手机、手表登录都用它。' : state.name),
    h('label', { class: 'field' }, h('span', null, setup ? '访问密码' : '密码'), pw),
    pw2 ? h('label', { class: 'field' }, h('span', null, '再输入一次'), pw2) : null,
    err, btn)
  form.onsubmit = async (e) => {
    e.preventDefault()
    err.textContent = ''
    if (setup && pw.value !== pw2.value) { err.textContent = '两次输入的密码不一样'; return }
    btn.disabled = true
    try {
      const r = await api(setup ? '/api/auth/setup' : '/api/auth/login', { method: 'POST', body: { Password: pw.value, Device: deviceName } })
      auth.set(r.token)
      start()
    } catch (e2) { err.textContent = e2.message; btn.disabled = false }
  }
  fill(app, h('div', { class: 'auth' }, form))
  pw.focus()
}

setUnauthorizedHandler(() => { auth.set(''); boot() })

// ---------- 外壳：侧栏 + 手机底栏 ----------

let shell, mainEl, navEl, tabbarEl, netEls = []

function netLine() {
  const el = h('div', { class: 'netline', title: '点击重新检测线路', onclick: async () => { await net.resolve(true); toast('当前线路：' + net.label()) } }, h('i'), h('span'))
  netEls.push(el)
  updateNet()
  return el
}
function updateNet() {
  for (const el of netEls) {
    el.className = 'netline ' + ({ remote: 'remote', offline: 'off' }[net.route] || '')
    el.lastChild.textContent = net.label()
  }
}
net.on(updateNet)

function buildShell() {
  netEls = []
  navEl = h('nav', { class: 'nav' })
  tabbarEl = h('nav', { class: 'tabbar' })
  mainEl = h('main', { class: 'main', id: 'main' })
  const side = h('aside', { class: 'side' },
    h('div', { class: 'brand' }, h('b', null, '书库'), h('span', { id: 'server-name' }, ctx.server.name)),
    navEl,
    h('div', { class: 'side-foot' }, h('div', { id: 'side-status' }), netLine()))
  const top = h('div', { class: 'mobile-top' }, h('b', null, '书库'), netLine())
  shell = h('div', { class: 'shell' }, side, h('div', null, top, mainEl), tabbarEl)
  fill(app, shell)
  renderNav()
}

function navLink(hash, ic, label, extra) {
  const cur = (location.hash || '#/') .split('?')[0]
  return h('a', { href: hash, class: cur === hash ? 'on' : null }, icon(ic), h('span', null, label), extra)
}

function renderNav() {
  if (!navEl) return
  const f = ctx.facets || {}
  const kinds = Object.fromEntries((f.kinds || []).map(k => [k.name, k.count]))
  const cnt = n => n ? h('span', { class: 'count' }, n) : null
  const pending = (f.review || 0) + (f.suggestions || 0)
  fill(navEl, ...[
    navLink('#/', 'shelf', '书架', cnt(f.total)),
    navLink('#/reading', 'reading', '在读', cnt(f.reading)),
    navLink('#/stats', 'activity', '阅读统计'),
    navLink('#/books', 'book', '图书', cnt(kinds.book)),
    navLink('#/comics', 'comic', '漫画', cnt(kinds.comic)),
    (f.categories || []).length ? h('div', { class: 'nav-group' }, '分类') : null,
    (f.categories || []).filter(c => c.name !== '漫画').map(c => navLink('#/cat/' + encodeURIComponent(c.name), 'folder', c.name, cnt(c.count))),
    h('div', { class: 'nav-group' }, '管理'),
    navLink('#/organize', 'sparkle', '整理', pending ? h('span', { class: 'badge warn' }, pending) : null),
    navLink('#/settings', 'settings', '设置'),
  ].flat().filter(Boolean))
  const cur = (location.hash || '#/').split('?')[0]
  const tab = (hash, ic, label, badge) => h('a', { href: hash, class: cur === hash || (hash === '#/' && ['#/books', '#/comics', '#/all'].includes(cur)) || (hash === '#/' && cur.startsWith('#/cat/')) ? 'on' : null }, icon(ic), h('span', null, label), badge ? h('span', { class: 'badge warn' }, badge) : null)
  fill(tabbarEl, 
    tab('#/', 'shelf', '书架'),
    tab('#/reading', 'reading', '在读'),
    tab('#/organize', 'sparkle', '整理', pending || null),
    tab('#/settings', 'settings', '设置'))
}

// ---------- 路由 ----------

let readerMod = null
let lastNonReader = '#/'
let currentView = null

async function route() {
  const hash = location.hash || '#/'
  const [path, qs] = hash.slice(1).split('?')
  const params = new URLSearchParams(qs || '')
  const parts = path.split('/').filter(Boolean).map(decodeURIComponent)

  if (parts[0] === 'read') {
    readerMod ||= await import('./reader/reader.js')
    currentView?.destroy?.()
    currentView = null
    const back = () => { location.hash = lastNonReader }
    readerMod.openReader(+parts[1], { ctx, params, back })
    return
  }
  readerMod?.closeReader?.()
  lastNonReader = hash
  if (!shell || !document.body.contains(shell)) buildShell()
  renderNav()
  currentView?.destroy?.()
  clear(mainEl)
  window.scrollTo(0, 0)
  const views = {
    '': () => renderLibrary(mainEl, { view: 'home' }, ctx),
    all: () => renderLibrary(mainEl, { view: 'all' }, ctx),
    reading: () => renderLibrary(mainEl, { view: 'reading' }, ctx),
    books: () => renderLibrary(mainEl, { view: 'kind', kind: 'book' }, ctx),
    comics: () => renderLibrary(mainEl, { view: 'kind', kind: 'comic' }, ctx),
    cat: () => renderLibrary(mainEl, { view: 'cat', category: parts[1] }, ctx),
    author: () => renderLibrary(mainEl, { view: 'author', author: parts[1] }, ctx),
    series: () => renderLibrary(mainEl, { view: 'series', series: parts[1] }, ctx),
    organize: () => renderOrganize(mainEl, params, ctx),
    settings: () => renderSettings(mainEl, ctx),
    stats: () => renderStats(mainEl, ctx),
  }
  currentView = await (views[parts[0] || ''] || views[''])()
}

addEventListener('hashchange', route)

// ---------- 后台动态：入库完成后提示并刷新 ----------

let lastEvent = 0
async function pollStatus() {
  try {
    const s = await api('/api/status')
    const side = $('#side-status')
    if (side) {
      const parts = []
      if (s.inbox.pending) parts.push(`投递目录 ${s.inbox.pending} 个文件待处理`)
      if (s.scan.running) parts.push(`正在扫描书库（${s.scan.found}）`)
      side.textContent = parts.join('，')
    }
    if (lastEvent && s.last_event > lastEvent) {
      const evs = await api('/api/events?since=' + lastEvent)
      for (const e of evs.slice(0, 4).reverse()) {
        toast(e.message, e.book_id ? { action: { label: '查看', run: () => ctx.openBook(e.book_id) } } : {})
      }
      await ctx.refreshFacets()
      currentView?.refresh?.()
    }
    lastEvent = s.last_event
  } catch {}
}

// ---------- 启动 ----------

async function start() {
  try {
    const st = await api('/api/settings')
    ctx.server.name = st.server_name
    ctx.settings = st
    // 服务器上配置的内网 / Tailscale 地址同步到本机，用于自动切换
    const n = st.network || {}
    const cur = net.config
    if ((n.lan_url || '') !== cur.lan || (n.remote_url || '') !== cur.remote) net.setConfig({ lan: n.lan_url || '', remote: n.remote_url || '' })
  } catch (e) { if (e.status === 401) return }
  document.title = ctx.server.name === '我的书库' ? '书库' : ctx.server.name
  await ctx.refreshFacets()
  buildShell()
  initUploads(ctx)
  route()
  pollStatus()
  setInterval(pollStatus, 6000)
}

async function boot() {
  applyAppearance()
  await net.resolve()
  let st
  try { st = await api('/api/auth/state') } catch (e) {
    fill(app, h('div', { class: 'auth' }, h('div', null, h('h1', { class: 'serif' }, '书库'), h('p', { class: 'muted' }, e.message), h('button', { class: 'btn', onclick: boot }, '重试'))))
    return
  }
  ctx.server.name = st.name
  if (!st.authed) { showAuth(st); return }
  start()
}

boot()

if ('serviceWorker' in navigator && (location.protocol === 'https:' || location.hostname === 'localhost')) {
  navigator.serviceWorker.register('/sw.js').catch(() => {})
}

export { toastError }
