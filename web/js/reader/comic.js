// 漫画：单页 / 双页（自动识别跨页大图）/ 条漫，支持日漫从右到左，按网络线路选择图片质量

import { h, clear, fill, add, isEInk } from '../ui.js'
import { api, apiUrl, net } from '../api.js'

export class Engine {
  kind = 'comic'
  toc = []
  index = 0

  constructor(shell) { this.shell = shell }

  async open(info, saved) {
    this.info = info
    const r = await api(`/api/files/${info.file_id}/pages`)
    this.count = r.count
    this.dims = r.dims || []
    if (!this.count) throw new Error('压缩包里没有图片')
    this.box = h('div', { class: 'comic' })
    this.shell.stage.append(this.box)
    this.box.addEventListener('click', e => this.shell.tap(e.clientX, e.clientY))
    this.box.addEventListener('dblclick', () => this.shell.setPref('comicFit', this.shell.prefs.comicFit === 'width' ? 'contain' : 'width'))
    this.bindSwipe(this.box)
    this.ro = new ResizeObserver(() => this.render())
    this.ro.observe(this.shell.stage)
    let start = 0
    if (saved?.location?.startsWith?.('i:')) start = +saved.location.split(':')[1] || 0
    else if (saved?.percent) start = Math.round(saved.percent * (this.count - 1))
    this.index = Math.max(0, Math.min(this.count - 1, start))
    this.mode = null
    this.applyPrefs()
  }

  get dir() {
    const p = this.shell.prefs.comicDir
    return p === 'auto' ? (this.info.direction === 'rtl' ? 'rtl' : 'ltr') : p
  }
  rtl() { return this.dir === 'rtl' && this.shell.prefs.comicMode !== 'webtoon' }
  paged() { return this.shell.prefs.comicMode !== 'webtoon' }

  // 图片地址：内网看原图；Tailscale 下按屏幕宽度压缩
  url(n, thumb) {
    const q = this.shell.prefs.comicQuality
    let w = 0
    // 墨水屏、旧的网页组件：一律按屏幕宽度要缩小的图（扫描的原图几千像素，解开要几十 MB 内存，墨水屏上会卡死）
    const small = (isEInk() || window.SHUKU_LEGACY) && q !== 'original'
    if (thumb) w = 240
    else if (small) w = Math.min(1600, Math.ceil(innerWidth * Math.min(devicePixelRatio || 1, 2) / (this.shell.prefs.comicMode === 'double' && innerWidth > innerHeight * 0.9 ? 2 : 1)))
    else if (q === 'saver') w = Math.min(1200, Math.ceil(innerWidth * Math.min(devicePixelRatio, 2)))
    else if (q === 'auto' && net.route !== 'lan') w = Math.min(2000, Math.ceil(innerWidth * Math.min(devicePixelRatio, 2) / (this.shell.prefs.comicMode === 'double' ? 2 : 1)))
    return apiUrl(`/api/files/${this.info.file_id}/pages/${n}${w ? '?w=' + w : ''}`)
  }

  wide(n) { const d = this.dims[n]; return d && d[0] > d[1] * 1.05 }

  // 双页模式：封面单独一页，跨页大图单独一页，其余两两成对
  buildSpreads() {
    const s = []
    let i = 0
    while (i < this.count) {
      if (i === 0 || this.wide(i) || i === this.count - 1 || this.wide(i + 1)) { s.push([i]); i++ }
      else { s.push([i, i + 1]); i += 2 }
    }
    this.spreads = s
  }

  spreadIndexOf(n) { return Math.max(0, this.spreads.findIndex(s => s.includes(n))) }

  render() {
    const p = this.shell.prefs
    if (p.comicMode === 'webtoon') return this.renderStrip()
    this.strip?.remove(); this.strip = null
    this.box.hidden = false
    this.box.className = 'comic' + (p.comicFit === 'width' ? ' fit-width' : '')
    const pages = p.comicMode === 'double' && innerWidth > innerHeight * 0.9 ? (this.spreads[this.spreadIndexOf(this.index)] || [this.index]) : [this.index]
    this.cur = pages
    const spread = h('div', { class: 'spread' + (pages.length === 2 ? ' two' : '') + (this.dir === 'rtl' ? ' rtl' : '') },
      pages.map(n => h('img', { class: 'pg', src: this.url(n), alt: `第 ${n + 1} 页`, draggable: 'false', decoding: 'async' })))
    fill(this.box, spread)
    this.box.scrollTop = 0
    this.preload(pages[pages.length - 1] + 1, isEInk() || window.SHUKU_LEGACY ? 1 : 3) // 墨水屏内存小，只先取下一页
    this.report()
  }

  preload(from, n) {
    for (let i = from; i < Math.min(this.count, from + n); i++) { const im = new Image(); im.decoding = 'async'; im.src = this.url(i) }
  }

  renderStrip() {
    this.box.hidden = true
    if (this.strip) return
    const col = h('div', { class: 'col' })
    this.strip = h('div', { class: 'webtoon' }, col)
    this.shell.stage.append(this.strip)
    this.stripImgs = []
    for (let i = 0; i < this.count; i++) {
      const d = this.dims[i]
      const img = h('img', { alt: `第 ${i + 1} 页`, loading: 'lazy', decoding: 'async', draggable: 'false', style: d && d[0] ? { aspectRatio: `${d[0]} / ${d[1]}` } : null })
      img.onload = () => img.classList.add('ok')
      this.stripImgs.push(img)
      col.append(img)
    }
    // 只给附近的图设置地址，避免一次请求几百张
    this.stripIO = new IntersectionObserver(es => { for (const e of es) if (e.isIntersecting && !e.target.src) e.target.src = this.url(this.stripImgs.indexOf(e.target)) }, { root: this.strip, rootMargin: '200% 0px' })
    this.stripImgs.forEach(im => this.stripIO.observe(im))
    this.strip.addEventListener('click', e => this.shell.tap(e.clientX, e.clientY))
    this.strip.addEventListener('scroll', () => {
      const y = this.strip.scrollTop + this.strip.clientHeight * 0.2
      let i = this.stripImgs.findIndex(im => im.offsetTop + im.offsetHeight > y)
      if (i < 0) i = this.count - 1
      if (i !== this.index) { this.index = i; this.report() }
    }, { passive: true })
    requestAnimationFrame(() => { this.stripImgs[this.index]?.scrollIntoView(); this.report() })
  }

  report() {
    const last = this.cur && this.paged() ? this.cur[this.cur.length - 1] : this.index
    this.shell.onRelocate({
      location: `i:${this.index}`, fraction: this.count > 1 ? last / (this.count - 1) : 1,
      chapter: '', label: `${(this.cur && this.paged() ? this.cur.map(n => n + 1).join('-') : this.index + 1)} / ${this.count}`,
      units: { pos: last, total: this.count - 1, chapterEnd: null, jump: 30 },
    })
  }

  next() {
    if (!this.paged()) return this.strip?.scrollBy({ top: this.strip.clientHeight * 0.85, behavior: 'smooth' })
    if (this.shell.prefs.comicFit === 'width' && this.box.scrollTop + this.box.clientHeight < this.box.scrollHeight - 4) {
      return this.box.scrollBy({ top: this.box.clientHeight * 0.85, behavior: 'smooth' })
    }
    const last = this.cur ? this.cur[this.cur.length - 1] : this.index
    if (last >= this.count - 1) return this.shell.showUI(true)
    this.index = last + 1
    this.render()
  }

  prev() {
    if (!this.paged()) return this.strip?.scrollBy({ top: -this.strip.clientHeight * 0.85, behavior: 'smooth' })
    if (this.shell.prefs.comicFit === 'width' && this.box.scrollTop > 4) return this.box.scrollBy({ top: -this.box.clientHeight * 0.85, behavior: 'smooth' })
    if (this.index <= 0) return
    if (this.cur?.length && this.shell.prefs.comicMode === 'double' && innerWidth > innerHeight * 0.9) {
      const si = this.spreadIndexOf(this.index)
      this.index = this.spreads[Math.max(0, si - 1)][0]
    } else this.index--
    this.render()
  }

  goTo(loc) {
    const n = typeof loc === 'string' && loc.startsWith('i:') ? +loc.slice(2) : +loc
    if (Number.isNaN(n)) return
    this.index = Math.max(0, Math.min(this.count - 1, n))
    if (this.paged()) this.render()
    else this.stripImgs?.[this.index]?.scrollIntoView()
  }
  goToFraction(f) { this.goTo(Math.round(f * (this.count - 1))) }
  restart() { this.goTo(0) }
  sameLocation(a, b) { return a === b }

  bindSwipe(el) {
    let x0 = 0, y0 = 0, t0 = 0
    el.addEventListener('touchstart', e => { const t = e.touches[0]; x0 = t.clientX; y0 = t.clientY; t0 = Date.now() }, { passive: true })
    el.addEventListener('touchend', e => {
      if (e.touches.length || e.changedTouches.length !== 1) return
      const vv = window.visualViewport // Chrome 61 以前没有
      if (vv && vv.scale > 1.05) return // 用户正在放大看图
      const t = e.changedTouches[0], dx = t.clientX - x0, dy = t.clientY - y0
      // 滑过屏幕宽的 6%（至少 30 像素）就翻，和书、PDF 一样
      if (Date.now() - t0 > 600 || Math.abs(dx) < Math.max(30, el.clientWidth * 0.06) || Math.abs(dx) < Math.abs(dy) * 1.5) return
      const forward = dx < 0
      ;(forward !== (this.dir === 'rtl')) ? this.next() : this.prev()
    }, { passive: true })
  }

  panelTabs() { return [['pages', '页面'], ['bm', '书签']] }

  drawPanelTab(tab, box) {
    if (tab !== 'pages') return
    const grid = h('div', { class: 'pages-grid' })
    for (let i = 0; i < this.count; i++) {
      grid.append(h('button', { class: i === this.index ? 'cur' : '', onclick: () => { this.goTo(i); this.shell.closeOverlays(); this.shell.showUI(false) } },
        h('img', { src: this.url(i, true), loading: 'lazy', alt: '' }), h('span', null, i + 1)))
    }
    box.append(grid)
  }

  applyPrefs() {
    if (!this.box) return
    const p = this.shell.prefs
    const sig = [p.comicMode, p.comicFit, p.comicDir, p.comicQuality].join()
    if (sig === this.sig) return
    const modeChanged = this.sig && this.sig.split(',')[0] !== p.comicMode
    this.sig = sig
    this.buildSpreads()
    if (modeChanged && p.comicMode !== 'webtoon') { this.stripIO?.disconnect(); this.strip?.remove(); this.strip = null }
    if (this.strip && p.comicMode === 'webtoon') { this.stripIO?.disconnect(); this.strip.remove(); this.strip = null }
    this.render()
  }

  destroy() {
    this.ro?.disconnect(); this.stripIO?.disconnect()
    this.box?.remove(); this.strip?.remove()
  }
}
