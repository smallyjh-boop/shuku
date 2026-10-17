// 阅读器外壳：工具栏、目录/书签/搜索侧栏、阅读设置、夜间模式、进度同步
// 具体渲染交给 engines：flow（EPUB/MOBI/AZW3/FB2/TXT）、pdf、comic

import { h, clear, fill, add, icon, iconBtn, toast, toastError, store, save, debounce, pct } from '../ui.js'
import { api, apiUrl, deviceName, net } from '../api.js'
import { isNative, call, callJSON, onNative } from '../native.js'

export const THEMES = [
  { id: 'white', name: '白', bg: '#FFFFFF', fg: '#26292E', dark: false },
  { id: 'paper', name: '纸', bg: '#F2EDE0', fg: '#3B3327', dark: false },
  { id: 'green', name: '绿', bg: '#D3E4CF', fg: '#233221', dark: false },
  { id: 'gray', name: '灰', bg: '#1E1F21', fg: '#C9C7C2', dark: true },
  { id: 'black', name: '黑', bg: '#000000', fg: '#A9A9A6', dark: true },
  { id: 'amber', name: '暖', bg: '#19130E', fg: '#CDAE80', dark: true },
]
const themeById = id => THEMES.find(t => t.id === id) || THEMES[0]

export const FONTS = [
  ['book', '原书'], ['serif', '宋体'], ['sans', '黑体'], ['kai', '楷体'],
]

const DEFAULTS = {
  theme: 'white', nightTheme: 'gray', nightMode: 'system', nightStart: '21:30', nightEnd: '07:00',
  font: 'serif', fontSize: 19, lineHeight: 1.85, paraSpace: 0.5, margin: 1, width: 720,
  flow: 'paginated', columns: 'auto', indent: true, justify: true, animated: true, dict: true,
  brightness: 1, warm: false,
  comicMode: 'single', comicFit: 'contain', comicDir: 'auto', comicQuality: 'auto',
  pdfMode: 'scroll', pdfInvert: true, pdfZoom: 1,
}

let current = null

// 手机上从底部弹出的面板：内容在最上面时往下拖就关掉（拖滑块、输入框时不算）
export function swipeDown(el, close) {
  let y0 = null, x0 = 0, dy = 0, t0 = 0
  const bottomSheet = () => innerWidth < 900 || el.classList.contains('sheet')
  el.addEventListener('touchstart', e => {
    y0 = null
    if (!bottomSheet() || e.touches.length > 1 || e.target.closest('input, select, textarea') || el.scrollTop > 2) return
    y0 = e.touches[0].clientY; x0 = e.touches[0].clientX; dy = 0; t0 = e.timeStamp
  }, { passive: true })
  el.addEventListener('touchmove', e => {
    if (y0 == null) return
    const t = e.touches[0]
    dy = t.clientY - y0
    if (dy <= 0 || Math.abs(t.clientX - x0) > dy) { el.style.transform = ''; if (dy < -4) y0 = null; return }
    e.preventDefault()
    el.style.transition = 'none'
    el.style.transform = `translateY(${dy}px)`
  }, { passive: false })
  const end = e => {
    if (y0 == null) return
    y0 = null
    el.style.transition = ''
    el.style.transform = ''
    if (dy > 90 || (dy > 30 && dy / Math.max(1, e.timeStamp - t0) > 0.5)) close()
  }
  el.addEventListener('touchend', end)
  el.addEventListener('touchcancel', end)
}

export function closeReader() {
  if (!current) return
  current.destroy()
  current = null
}

export async function openReader(bookId, { ctx, params, back }) {
  closeReader()
  current = new Reader(bookId, ctx, params, back)
  await current.start()
}

// 夜间判断：手动覆盖 > 定时 / 跟随系统
function nightOverride() {
  try {
    const o = JSON.parse(localStorage.getItem('shuku.nightOverride') || 'null')
    if (o && Date.now() - o.at < 10 * 3600e3) return o.on
  } catch {}
  return null
}
function inTimeRange(start, end) {
  const now = new Date(), m = now.getHours() * 60 + now.getMinutes()
  const [sh, sm] = start.split(':').map(Number), [eh, em] = end.split(':').map(Number)
  const s = sh * 60 + sm, e = eh * 60 + em
  return s <= e ? m >= s && m < e : m >= s || m < e
}
export function isNight(p) {
  const o = nightOverride()
  if (o !== null) return o
  if (p.nightMode === 'system') return matchMedia('(prefers-color-scheme: dark)').matches
  if (p.nightMode === 'time') return inTimeRange(p.nightStart, p.nightEnd)
  return false
}

class Reader {
  constructor(id, ctx, params, back) {
    this.id = id
    this.ctx = ctx
    this.params = params
    this.back = back
    this.prefs = store('shuku.reader', DEFAULTS)
    this.loc = null
    this.bookmarks = []
    this.cleanups = []
  }

  get theme() { return themeById(isNight(this.prefs) ? this.prefs.nightTheme : this.prefs.theme) }

  async start() {
    this.buildDOM()
    try {
      this.info = await api(`/api/books/${this.id}/read${this.params.get('file') ? '?file=' + this.params.get('file') : ''}`)
    } catch (e) { return this.fail(e.message) }
    this.titleEl.textContent = this.info.title
    this.panelTitle.textContent = this.info.title
    this.panelAuthor.textContent = this.info.author || ''
    document.title = this.info.title
    const mod = this.info.reader === 'pdf' ? await import('./pdf.js') : this.info.reader === 'comic' ? await import('./comic.js') : this.info.reader === 'foliate' ? await import('./flow.js') : null
    if (!mod) return this.fail(`这个格式（${this.info.format.toUpperCase()}）暂时不能在线阅读，可以在详情里下载。`)
    this.engine = new mod.Engine(this)
    this.kind = this.engine.kind
    this.root.dataset.kind = this.kind
    // 进度：先用本机缓存，再用服务器上更新的
    let saved = null
    try {
      const p = await api(`/api/books/${this.id}/progress`)
      const local = store('shuku.pos.' + this.id, {})
      saved = (local.at || 0) > (p.updated_at || 0) * 1000 + 2000 ? local : p
      if (saved.file_id && saved.file_id !== this.info.file_id) saved = { percent: saved.percent } // 换了格式，只能按百分比
    } catch {}
    this.bookmarks = await api(`/api/books/${this.id}/bookmarks`).catch(() => [])
    try {
      await this.engine.open(this.info, saved)
    } catch (e) {
      console.error(e)
      return this.fail('打开失败：' + (e.message || e))
    }
    this.loading.remove()
    this.applyPrefs()
    this.drawPanel()
    this.ttsBtn.hidden = this.ttsTool.hidden = !this.engine.ttsSegments // 漫画没有文字，不显示听书
    // 从书内搜索结果跳过来：定位到那一章并找到那句话
    const q = this.params.get('q')
    if (q && this.engine.find) {
      const sec = parseInt(this.params.get('sec') ?? '-1')
      this.engine.find(q, isNaN(sec) ? -1 : sec).then(ok => toast(ok ? `已定位到“${q}”` : `没在这一章找到“${q}”`, { duration: 2000 })).catch(() => {})
    }
    this.startTimeTracking()
    // 第一次打开时短暂显示工具栏，让人知道在哪
    this.showUI(true)
    this.uiTimer = setTimeout(() => { if (!this.panel.classList.contains('show') && !this.sheet.classList.contains('show')) this.showUI(false) }, 1800)
  }

  // 旧版 App 打不开 EPUB、TXT 这类书：提示更新 App（1.0.0.3 起 App 里能直接更新，更早的交给浏览器下载）
  oldAppHint() {
    if (!this.loading.isConnected) return
    const canUpdate = !!callJSON('appInfo')
    const url = (net.base || location.origin) + '/dl/shuku-android.apk'
    fill(this.loading, h('div', { class: 'err' },
      '这个版本的书库 App 打不开 EPUB、TXT 这类书，更新 App 就好了（PDF 和漫画不受影响）。',
      h('div', { style: { marginTop: '14px', display: 'flex', gap: '10px', justifyContent: 'center', flexWrap: 'wrap' } },
        h('button', { class: 'btn primary', onclick: () => canUpdate ? call('updateApp') : call('openExternal', url) }, '更新 App'),
        h('button', { class: 'btn', onclick: () => this.back() }, '返回书架'))))
    this.showUI(true)
  }

  fail(msg) {
    fill(this.loading, h('div', { class: 'err' }, msg, h('div', { style: { marginTop: '14px' } }, h('button', { class: 'btn', onclick: () => this.back() }, '返回书架'))))
    this.showUI(true)
  }

  // ---------- DOM ----------
  buildDOM() {
    this.stage = h('div', { class: 'r-stage' })
    this.dim = h('div', { class: 'r-dim' })
    this.warm = h('div', { class: 'r-warm' })
    this.loading = h('div', { class: 'r-loading' }, '正在打开…')
    this.titleEl = h('div', { class: 'ttl' })
    this.statusL = h('span')
    this.statusR = h('span')
    this.nightBtn = iconBtn('moon', '夜间模式', () => this.toggleNight())
    this.bmBtn = iconBtn('bookmark', '添加书签', () => this.toggleBookmark())
    this.ttsBtn = iconBtn('headphones', '听书', () => this.toggleNarration())
    const top = h('div', { class: 'r-bar r-top' },
      iconBtn('back', '返回书架', () => this.back()),
      this.titleEl,
      this.ttsBtn, this.bmBtn, this.nightBtn,
      iconBtn('type', '阅读设置', () => this.toggleSheet()))
    this.slider = h('input', { type: 'range', min: 0, max: 1000, value: 0, 'aria-label': '阅读进度' })
    this.slider.addEventListener('input', () => { this.sliderPreview.textContent = pct(this.slider.value / 1000) })
    this.slider.addEventListener('change', () => this.engine?.goToFraction(this.slider.value / 1000))
    this.chapEl = h('span', { class: 'chap' })
    this.sliderPreview = h('span', { style: { width: '42px', textAlign: 'right' } })
    const tool = (ic, label, fn) => h('button', { class: 'icon-btn', onclick: fn, title: label }, icon(ic), h('span', null, label))
    const bottom = h('div', { class: 'r-bar r-bottom' },
      h('div', { class: 'r-prog' }, this.chapEl, this.slider, this.sliderPreview),
      h('div', { class: 'r-tools' },
        tool('list', '目录', () => this.togglePanel()),
        tool('back', '上一页', () => this.engine?.prev()),
        tool('next', '下一页', () => this.engine?.next()),
        (this.ttsTool = tool('headphones', '听书', () => this.toggleNarration())),
        tool('expand', '全屏', () => this.fullscreen()),
        tool('type', '设置', () => this.toggleSheet())))
    this.panelTitle = h('div', { class: 't' })
    this.panelAuthor = h('div', { class: 'a' })
    this.ptabs = h('div', { class: 'ptabs' })
    this.pbody = h('div', { class: 'pbody' })
    this.panel = h('aside', { class: 'r-panel', 'aria-label': '目录' },
      h('header', null, h('div', { class: 'grow' }, this.panelTitle, this.panelAuthor), iconBtn('close', '关闭', () => this.closeOverlays())), this.ptabs, this.pbody)
    this.sheet = h('div', { class: 'r-sheet', role: 'dialog', 'aria-label': '阅读设置' })
    // 打开设置、目录时，点书页（包括书还没打开、只有“正在打开”的时候）就关掉
    this.scrim = h('div', { class: 'r-scrim', onclick: () => this.closeOverlays() })
    swipeDown(this.sheet, () => this.closeOverlays())
    this.root = h('div', { class: 'reader ui', dataset: { t: this.theme.id } },
      this.stage, this.warm, this.dim, h('div', { class: 'r-status' }, this.statusL, this.statusR),
      top, bottom, this.scrim, this.panel, this.sheet, this.loading)
    document.body.append(this.root)
    document.body.style.overflow = 'hidden'

    const onKey = e => this.onKey(e)
    document.addEventListener('keydown', onKey)
    this.cleanups.push(() => document.removeEventListener('keydown', onKey))
    // 离开页面前保存进度
    const flush = () => this.saveProgress(true)
    addEventListener('pagehide', flush)
    const vis = () => { if (document.hidden) flush(); else this.applyTheme() }
    document.addEventListener('visibilitychange', vis)
    this.cleanups.push(() => { removeEventListener('pagehide', flush); document.removeEventListener('visibilitychange', vis) })
    // 定时夜间：每分钟检查一次
    const t = setInterval(() => this.applyTheme(), 60000)
    const mq = matchMedia('(prefers-color-scheme: dark)')
    const onMq = () => this.applyTheme()
    mq.addEventListener('change', onMq)
    this.cleanups.push(() => { clearInterval(t); mq.removeEventListener('change', onMq) })
    // App 里：音量键翻页、阅读时屏幕常亮（App 的设置里可以关）
    if (isNative) {
      call('setReaderMode', true)
      this.cleanups.push(() => call('setReaderMode', false))
      this.cleanups.push(onNative('key', d => {
        if (!this.engine) return
        this.showUI(false)
        d.dir === 'prev' ? this.engine.prev() : this.engine.next()
        this.markActivity(); this.afterUserNav()
      }))
    }
    // 鼠标滚轮翻页（翻页模式）
    let wheelAt = 0
    this.stage.addEventListener('wheel', e => {
      if (!this.engine?.paged?.()) return
      if (Math.abs(e.deltaY) < 8 && Math.abs(e.deltaX) < 8) return
      e.preventDefault()
      if (Date.now() - wheelAt < 350) return
      wheelAt = Date.now()
      ;(e.deltaY || e.deltaX) > 0 ? this.engine.next() : this.engine.prev()
    }, { passive: false })
  }

  // ---------- 阅读时长：页面在前台、最近 5 分钟有操作（或正在听书）才计时 ----------
  markActivity() { this.lastAct = Date.now() }

  startTimeTracking() {
    this.readSecs = 0
    this.markActivity()
    const tick = setInterval(() => {
      if (document.hidden && !this.narrator?.playing) return
      if (Date.now() - this.lastAct < 300000 || this.narrator?.playing) this.readSecs++
    }, 1000)
    const flushT = setInterval(() => { if (this.readSecs >= 30) this.saveProgress() }, 60000)
    const act = () => this.markActivity()
    for (const ev of ['pointerdown', 'keydown', 'wheel', 'touchstart']) addEventListener(ev, act, { passive: true })
    this.cleanups.push(() => {
      clearInterval(tick); clearInterval(flushT)
      for (const ev of ['pointerdown', 'keydown', 'wheel', 'touchstart']) removeEventListener(ev, act)
    })
  }

  // ---------- 听书 ----------
  async toggleNarration() {
    if (!this.narrator) await this.makeNarrator()
    if (this.narrator.active) this.narrator.toggle()
    else { this.showUI(false); this.narrator.start() }
  }

  // App 里声音交给原生后台服务；网页里用浏览器播放
  async makeNarrator() {
    const { Narrator, NativeNarrator } = await import('./tts.js')
    this.narrator ||= isNative ? new NativeNarrator(this) : new Narrator(this)
    return this.narrator
  }

  afterUserNav = debounce(() => this.narrator?.active && this.narrator.jumpToView(), 600)

  // 选中了文字：查字典（阅读设置里可以关）
  async onSelectText(sel) {
    if (!sel || this.prefs.dict === false) return
    if (!this.dict) {
      const { DictPopup } = await import('./dict.js')
      this.dict ||= new DictPopup(this)
    }
    this.dict.show(sel)
  }

  destroy() {
    this.dict?.destroy()
    this.saveProgress(true)
    this.narrator?.destroy()
    this.engine?.destroy()
    for (const c of this.cleanups) c()
    this.root.remove()
    document.body.style.overflow = ''
    document.title = this.ctx.server?.name || '书库'
    if (document.fullscreenElement) document.exitFullscreen().catch(() => {})
  }

  // ---------- 点击区域 ----------
  // 引擎把点击位置（屏幕坐标 x）交给这里：左 30% 上一页，右 30% 下一页，中间显示 / 隐藏工具栏
  tap(x, y) {
    if (this.dict?.open) { this.dict.hide(); return }
    if (this.sheet.classList.contains('show') || this.panel.classList.contains('show')) { this.closeOverlays(); return }
    const w = innerWidth
    const rtl = this.engine?.rtl?.()
    if (x < w * 0.3) { this.showUI(false); rtl ? this.engine.next() : this.engine.prev(); this.afterUserNav() }
    else if (x > w * 0.7) { this.showUI(false); rtl ? this.engine.prev() : this.engine.next(); this.afterUserNav() }
    else this.showUI(!this.root.classList.contains('ui'))
  }

  showUI(on) {
    clearTimeout(this.uiTimer)
    this.root.classList.toggle('ui', on)
    if (!on) this.closeOverlays()
  }

  closeOverlays() {
    this.sheet.classList.remove('show')
    this.panel.classList.remove('show')
    this.syncOverlay()
  }

  syncOverlay() {
    this.root.classList.toggle('overlay', this.sheet.classList.contains('show') || this.panel.classList.contains('show'))
  }

  onKey(e) {
    if (e.target.closest?.('input, textarea, select')) { if (e.key === 'Escape') e.target.blur(); return }
    const rtl = this.engine?.rtl?.()
    const k = e.key
    if (k === 'ArrowRight' || k === 'PageDown' || (k === ' ' && !e.shiftKey)) { e.preventDefault(); (k === 'ArrowRight' && rtl) ? this.engine?.prev() : this.engine?.next() }
    else if (k === 'ArrowLeft' || k === 'PageUp' || (k === ' ' && e.shiftKey)) { e.preventDefault(); (k === 'ArrowLeft' && rtl) ? this.engine?.next() : this.engine?.prev() }
    else if (k === 'ArrowDown' && this.engine?.paged?.()) { e.preventDefault(); this.engine.next() }
    else if (k === 'ArrowUp' && this.engine?.paged?.()) { e.preventDefault(); this.engine.prev() }
    else if (k === 'Escape' && this.dict?.open) this.dict.hide()
    else if (k === 'Escape') { if (this.sheet.classList.contains('show') || this.panel.classList.contains('show')) this.closeOverlays(); else if (document.fullscreenElement) document.exitFullscreen(); else this.back() }
    else if (k === 't') this.togglePanel()
    else if (k === 'n') this.toggleNight()
    else if (k === 'b') this.toggleBookmark()
    else if (k === 'f') this.fullscreen()
    else if (k === 'Enter') this.showUI(!this.root.classList.contains('ui'))
    else if (k === 'r') this.toggleNarration()
    if (['ArrowRight', 'ArrowLeft', 'PageDown', 'PageUp', ' ', 'ArrowDown', 'ArrowUp'].includes(k)) this.afterUserNav()
  }

  fullscreen() {
    if (document.fullscreenElement) document.exitFullscreen().catch(() => {})
    else this.root.requestFullscreen?.().catch(() => toast('这个浏览器不支持全屏，可以添加到主屏幕后打开'))
  }

  // ---------- 位置、进度 ----------
  onRelocate({ location, fraction, chapter, label }) {
    this.dict?.hide()
    this.loc = { location, fraction }
    this.slider.value = Math.round((fraction || 0) * 1000)
    this.sliderPreview.textContent = pct(fraction)
    this.chapEl.textContent = chapter || ''
    this.statusL.textContent = chapter || ''
    this.statusR.textContent = label || pct(fraction)
    this.chapter = chapter
    this.markBookmark()
    this.markToc()
    save('shuku.pos.' + this.id, { file_id: this.info.file_id, location, percent: fraction, at: Date.now() })
    this.saveSoon()
  }

  saveSoon = debounce(() => this.saveProgress(), 1500)

  // 熄屏听书时没有排版，只记下读到的位置
  saveLocation(location, fraction) {
    this.loc = { location, fraction }
    save('shuku.pos.' + this.id, { file_id: this.info?.file_id, location, percent: fraction, at: Date.now() })
    this.saveSoon()
  }

  saveProgress(keepalive) {
    if (!this.loc || !this.info) return
    const seconds = this.readSecs || 0
    this.readSecs = 0
    const body = JSON.stringify({ file_id: this.info.file_id, location: String(this.loc.location ?? ''), percent: this.loc.fraction || 0, device: deviceName, seconds })
    // App 里交给原生保存：离线时先存在手机上，联网后再同步
    if (isNative) { call('saveProgress', this.id, body); return }
    const url = (net.base || '') + `/api/books/${this.id}/progress`
    const headers = { 'Content-Type': 'application/json' }
    const tok = localStorage.getItem('shuku.token')
    if (tok) headers.Authorization = 'Bearer ' + tok
    fetch(url, { method: 'PUT', body, headers, keepalive: !!keepalive, credentials: net.base ? 'omit' : 'same-origin' }).catch(() => {})
  }

  // ---------- 主题、排版 ----------
  applyTheme() {
    const t = this.theme
    this.root.dataset.t = t.id
    this.root.classList.toggle('invert', t.dark && this.prefs.pdfInvert)
    this.nightBtn.replaceChildren(icon(t.dark ? 'sun' : 'moon'))
    this.nightBtn.title = t.dark ? '切换到日间' : '切换到夜间'
    document.querySelector('#theme-color')?.setAttribute('content', t.bg)
    call('setThemeColor', t.bg, t.dark)
    this.dim.style.opacity = String(1 - this.prefs.brightness)
    this.warm.classList.toggle('on', !!this.prefs.warm)
    this.engine?.applyPrefs(this.prefs, t)
  }

  applyPrefs() {
    save('shuku.reader', this.prefs)
    this.applyTheme()
  }

  setPref(k, v) {
    this.prefs[k] = v
    this.applyPrefs()
    this.drawSheet()
  }

  toggleNight() {
    const night = !this.theme.dark
    // 手动切换在 10 小时内有效；书架界面也会跟着变深，避免从阅读器退出时被白屏晃眼
    localStorage.setItem('shuku.nightOverride', JSON.stringify({ on: night, at: Date.now() }))
    this.applyTheme()
    this.drawSheet()
    toast(night ? '已切换到夜间模式' : '已切换到日间模式', { duration: 1400 })
  }

  // ---------- 侧栏：目录、书签、搜索 ----------
  togglePanel(tab) {
    clearTimeout(this.uiTimer) // 刚打开书时工具栏会自动隐藏，用户已经在操作就不要再收起面板
    const show = !this.panel.classList.contains('show') || tab
    this.sheet.classList.remove('show')
    this.panel.classList.toggle('show', !!show)
    this.syncOverlay()
    if (show) { if (tab) this.ptab = tab; this.drawPanel(); this.panel.querySelector('.cur')?.scrollIntoView({ block: 'center' }) }
  }

  drawPanel() {
    const tabs = this.engine?.panelTabs?.() || [['toc', '目录'], ['bm', '书签']]
    if (!this.ptab || !tabs.some(t => t[0] === this.ptab)) this.ptab = tabs[0][0]
    fill(this.ptabs, ...tabs.map(([k, l]) => h('button', { class: this.ptab === k ? 'on' : '', onclick: () => { this.ptab = k; this.drawPanel() } }, l)))
    clear(this.pbody)
    if (this.ptab === 'toc') this.drawToc()
    else if (this.ptab === 'bm') this.drawBookmarks()
    else this.engine?.drawPanelTab?.(this.ptab, this.pbody)
  }

  drawToc() {
    const toc = this.engine?.toc || []
    if (!toc.length) { this.pbody.append(h('p', { class: 'muted', style: { padding: '16px 18px' } }, '这本书没有目录')); return }
    const box = h('div', { class: 'toc' })
    const walk = (items, lvl) => {
      for (const it of items) {
        box.append(h('a', { class: 'l' + lvl, dataset: { href: it.href ?? '' }, onclick: () => { this.engine.goTo(it.href); this.closeOverlays(); this.showUI(false) } }, it.label))
        if (it.subitems?.length && lvl < 3) walk(it.subitems, lvl + 1)
      }
    }
    walk(toc, 1)
    this.pbody.append(box)
    this.markToc()
  }

  markToc() {
    if (!this.panel.classList.contains('show') || this.ptab !== 'toc') return
    const links = this.pbody.querySelectorAll('.toc a')
    let cur = null
    for (const a of links) { a.classList.remove('cur'); if (a.textContent === this.chapter) cur = a }
    cur?.classList.add('cur')
  }

  drawBookmarks() {
    if (!this.bookmarks.length) { this.pbody.append(h('p', { class: 'muted', style: { padding: '16px 18px' } }, '还没有书签。点右上角的书签图标，或按 B 键添加。')); return }
    for (const b of this.bookmarks) {
      this.pbody.append(h('div', { class: 'bm', onclick: () => { this.engine.goTo(b.location); this.closeOverlays(); this.showUI(false) } },
        h('div', { class: 'grow' }, h('div', null, b.label || '书签'), h('small', null, new Date(b.created_at * 1000).toLocaleDateString('zh-CN'))),
        iconBtn('trash', '删除书签', async e => { e.stopPropagation(); await api('/api/bookmarks/' + b.id, { method: 'DELETE' }).catch(toastError); this.bookmarks = this.bookmarks.filter(x => x.id !== b.id); this.drawPanel(); this.markBookmark() })))
    }
  }

  bookmarkHere() {
    const l = String(this.loc?.location ?? '')
    return this.bookmarks.find(b => this.engine?.sameLocation ? this.engine.sameLocation(b.location, l) : b.location === l)
  }

  markBookmark() { this.bmBtn.classList.toggle('on', !!this.bookmarkHere()) }

  async toggleBookmark() {
    if (!this.loc) return
    const here = this.bookmarkHere()
    try {
      if (here) {
        await api('/api/bookmarks/' + here.id, { method: 'DELETE' })
        this.bookmarks = this.bookmarks.filter(b => b !== here)
        toast('已删除书签', { duration: 1400 })
      } else {
        const label = `${this.chapter || ''} · ${pct(this.loc.fraction)}`.replace(/^ · /, '')
        const b = await api(`/api/books/${this.id}/bookmarks`, { method: 'POST', body: { location: String(this.loc.location), label, text: this.engine?.excerpt?.() || '' } })
        this.bookmarks.push(b)
        toast('已添加书签', { duration: 1400 })
      }
    } catch (e) { toastError(e) }
    this.markBookmark()
    if (this.ptab === 'bm') this.drawPanel()
  }

  // ---------- 阅读设置 ----------
  toggleSheet() {
    clearTimeout(this.uiTimer)
    const show = !this.sheet.classList.contains('show')
    this.panel.classList.remove('show')
    if (show) this.drawSheet()
    this.sheet.classList.toggle('show', show)
    this.syncOverlay()
  }

  drawSheet() {
    if (!this.sheet) return
    const p = this.prefs
    const night = isNight(p)
    const seg = (key, opts, onPick) => h('div', { class: 'seg' }, opts.map(([v, l]) => h('button', { class: p[key] === v ? 'on' : '', onclick: () => onPick ? onPick(v) : this.setPref(key, v) }, l)))
    const stepper = (label, key, min, max, step, fmt) => {
      const val = h('span', { class: 'val' }, fmt(p[key]))
      const r = h('input', { type: 'range', min, max, step, value: p[key], 'aria-label': label })
      r.oninput = () => { val.textContent = fmt(+r.value); this.prefs[key] = +r.value; this.applyPrefsSoon() }
      return h('div', { class: 'stepper' }, h('span', { class: 'lbl' }, label), r, val)
    }
    const swatch = (t, key) => h('button', { class: p[key] === t.id ? 'on' : '', style: { background: t.bg, color: t.fg }, title: t.name, onclick: () => { this.prefs[key] = t.id; localStorage.removeItem('shuku.nightOverride'); this.applyPrefs(); this.drawSheet() } }, t.name)
    const kind = this.kind
    const parts = []
    parts.push(h('h4', null, night ? '夜间主题' : '主题'),
      h('div', { class: 'swatches' }, night ? THEMES.filter(t => t.dark).map(t => swatch(t, 'nightTheme')) : THEMES.map(t => swatch(t, 'theme'))),
      h('small', null, night ? '现在是夜间模式。点月亮 / 太阳图标可以临时切换' : '日间使用的主题。夜间模式下单独选择深色主题'))
    parts.push(h('h4', null, '自动夜间模式'),
      seg('nightMode', [['system', '跟随系统'], ['time', '定时'], ['off', '关闭']], v => { localStorage.removeItem('shuku.nightOverride'); this.setPref('nightMode', v) }))
    if (p.nightMode === 'time') {
      const s = h('input', { class: 'input', type: 'time', value: p.nightStart }), e = h('input', { class: 'input', type: 'time', value: p.nightEnd })
      s.onchange = () => this.setPref('nightStart', s.value); e.onchange = () => this.setPref('nightEnd', e.value)
      parts.push(h('div', { class: 'time-row' }, s, '到', e))
    }
    parts.push(h('h4', null, '亮度'), stepper('亮度', 'brightness', 0.3, 1, 0.05, v => Math.round(v * 100) + '%'),
      h('div', { class: 'set-row' }, h('div', null, h('div', { class: 'l' }, '暖色滤镜'), h('div', { class: 'h', style: { fontSize: '12px', opacity: .7 } }, '减少蓝光，适合睡前')), h('label', { class: 'switch' }, h('input', { type: 'checkbox', checked: p.warm, onchange: e => this.setPref('warm', e.target.checked) }), h('i'))))
    if (kind === 'flow') {
      parts.push(h('h4', null, '字体'), seg('font', FONTS),
        h('h4', null, '排版'),
        stepper('字号', 'fontSize', 13, 34, 1, v => v + ''),
        stepper('行距', 'lineHeight', 1.3, 2.6, 0.05, v => v.toFixed(2)),
        stepper('段距', 'paraSpace', 0, 1.6, 0.1, v => v.toFixed(1)),
        stepper('页边距', 'margin', 0, 3, 1, v => ['窄', '标准', '宽', '很宽'][v]),
        h('div', { class: 'row', style: { gap: '8px', marginTop: '10px' } },
          ['紧凑', '标准', '舒适'].map((l, i) => h('button', { class: 'btn small', style: { flex: 1, background: 'transparent', borderColor: 'var(--r-rule)', color: 'inherit' }, onclick: () => {
            Object.assign(this.prefs, [{ fontSize: 17, lineHeight: 1.6, paraSpace: 0.3, margin: 0 }, { fontSize: 19, lineHeight: 1.85, paraSpace: 0.5, margin: 1 }, { fontSize: 21, lineHeight: 2.1, paraSpace: 0.8, margin: 2 }][i])
            this.applyPrefs(); this.drawSheet()
          } }, l))),
        h('h4', null, '翻页方式'), seg('flow', [['paginated', '左右翻页'], ['scrolled', '上下滚动']]),
        p.flow === 'paginated' ? h('div', { style: { marginTop: '8px' } }, seg('columns', [['auto', '宽屏双栏'], ['1', '始终单栏']])) : null,
        h('div', { class: 'set-row' }, h('div', null, h('div', { class: 'l' }, '首行缩进')), h('label', { class: 'switch' }, h('input', { type: 'checkbox', checked: p.indent, onchange: e => this.setPref('indent', e.target.checked) }), h('i'))),
        h('div', { class: 'set-row' }, h('div', null, h('div', { class: 'l' }, '两端对齐')), h('label', { class: 'switch' }, h('input', { type: 'checkbox', checked: p.justify, onchange: e => this.setPref('justify', e.target.checked) }), h('i'))),
        h('div', { class: 'set-row' }, h('div', null, h('div', { class: 'l' }, '翻页动画')), h('label', { class: 'switch' }, h('input', { type: 'checkbox', checked: p.animated, onchange: e => this.setPref('animated', e.target.checked) }), h('i'))),
        h('div', { class: 'set-row' }, h('div', null, h('div', { class: 'l' }, '选中文字查字典'), h('div', { class: 'h', style: { fontSize: '12px', opacity: .7 } }, '弹出读音、释义，可以让 AI 结合上下文解释')), h('label', { class: 'switch' }, h('input', { type: 'checkbox', checked: p.dict !== false, onchange: e => this.setPref('dict', e.target.checked) }), h('i'))))
    } else if (kind === 'comic') {
      parts.push(h('h4', null, '阅读模式'), seg('comicMode', [['single', '单页'], ['double', '双页'], ['webtoon', '条漫']]),
        h('h4', null, '翻页方向'), seg('comicDir', [['auto', '自动'], ['ltr', '从左到右'], ['rtl', '从右到左']]),
        p.comicMode !== 'webtoon' ? [h('h4', null, '缩放'), seg('comicFit', [['contain', '整页'], ['width', '适应宽度']])] : null,
        h('h4', null, '图片质量'), seg('comicQuality', [['auto', '自动'], ['original', '原图'], ['saver', '省流量']]),
        h('small', { style: { display: 'block', marginTop: '6px', fontSize: '12px', opacity: .7 } }, '自动：内网看原图，Tailscale 下按屏幕大小压缩'))
    } else if (kind === 'pdf') {
      parts.push(h('h4', null, '阅读模式'), seg('pdfMode', [['scroll', '连续滚动'], ['page', '单页翻页']]),
        stepper('缩放', 'pdfZoom', 0.6, 2.5, 0.1, v => Math.round(v * 100) + '%'),
        h('div', { class: 'set-row' }, h('div', null, h('div', { class: 'l' }, '夜间反色'), h('div', { class: 'h', style: { fontSize: '12px', opacity: .7 } }, '深色主题下把白底黑字的页面反转成深底浅字')), h('label', { class: 'switch' }, h('input', { type: 'checkbox', checked: p.pdfInvert, onchange: e => this.setPref('pdfInvert', e.target.checked) }), h('i'))))
    }
    if (this.info?.files?.length > 1) {
      parts.push(h('h4', null, '换个格式读'), h('div', { class: 'row wrap', style: { gap: '6px' } }, this.info.files.map(f => h('button', {
        class: 'btn small', style: { background: f.id === this.info.file_id ? 'var(--r-rule)' : 'transparent', borderColor: 'var(--r-rule)', color: 'inherit' },
        onclick: () => { if (f.id !== this.info.file_id) location.hash = `#/read/${this.id}?file=${f.id}` },
      }, f.format.toUpperCase()))))
    }
    if (this.engine?.ttsSegments) {
      const ttsBox = h('div')
      parts.push(ttsBox)
      ;(async () => {
        if (!this.narrator) await this.makeNarrator()
        fill(ttsBox, ...(await this.narrator.sheetSection()))
      })()
    }
    fill(this.sheet, h('div', { class: 'r-sheet-hd' }, h('span', { class: 'grab' }), h('b', null, '阅读设置'), iconBtn('close', '关闭', () => this.closeOverlays())), ...parts.flat())
  }

  applyPrefsSoon = debounce(() => this.applyPrefs(), 120)
}
