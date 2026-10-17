// PDF：pdf.js 渲染，按需分段下载（大文件不用等全部下完），连续滚动或单页翻页，夜间反色

import * as pdfjs from '/vendor/pdfjs/pdf.mjs'
import { h, clear, fill } from '../ui.js'
import { splitSentences } from './tts.js'
import { api, auth, net } from '../api.js'

pdfjs.GlobalWorkerOptions.workerSrc = '/vendor/pdfjs/pdf.worker.mjs'
const VENDOR = '/vendor/pdfjs/'

export class Engine {
  kind = 'pdf'
  toc = []
  page = 1

  constructor(shell) { this.shell = shell }

  async open(info, saved) {
    this.info = info
    const headers = auth.token ? { Authorization: 'Bearer ' + auth.token } : {}
    const task = pdfjs.getDocument({
      url: (net.base || '') + info.url, httpHeaders: headers, withCredentials: false,
      rangeChunkSize: 1 << 20, disableAutoFetch: true,
      cMapUrl: VENDOR + 'cmaps/', cMapPacked: true, standardFontDataUrl: VENDOR + 'standard_fonts/',
      isEvalSupported: false,
    })
    task.onProgress = ({ loaded, total }) => { if (total) this.shell.loading.textContent = `正在加载 ${Math.round(loaded / total * 100)}%` }
    this.pdf = await task.promise
    this.count = this.pdf.numPages
    const first = await this.pdf.getPage(1)
    this.base = first.getViewport({ scale: 1 })
    this.sizes = new Array(this.count).fill(null)
    this.sizes[0] = [this.base.width, this.base.height]

    this.wrap = h('div', { class: 'pdfv' })
    this.pagesEl = h('div', { class: 'pages' })
    this.wrap.append(this.pagesEl)
    this.shell.stage.append(this.wrap)
    this.pageEls = []
    for (let i = 1; i <= this.count; i++) {
      const el = h('div', { class: 'pg', dataset: { n: i } }, h('span', { class: 'no' }, i))
      this.pageEls.push(el)
      this.pagesEl.append(el)
    }
    this.rendered = new Map() // n -> {scale, task}
    this.io = new IntersectionObserver(es => {
      for (const e of es) if (e.isIntersecting) this.render(+e.target.dataset.n)
    }, { root: this.wrap, rootMargin: '150% 0px' })
    this.pageEls.forEach(el => this.io.observe(el))

    this.wrap.addEventListener('scroll', () => this.onScroll(), { passive: true })
    this.wrap.addEventListener('click', e => this.shell.tap(e.clientX, e.clientY))
    this.ro = new ResizeObserver(() => this.layout())
    this.ro.observe(this.wrap)
    this.loadOutline()

    this.layout()
    let start = 1, off = 0
    if (saved?.location?.startsWith?.('p:')) { const [, n, o] = saved.location.split(':'); start = +n || 1; off = +o || 0 }
    else if (saved?.percent) start = Math.max(1, Math.round(saved.percent * this.count))
    this.goToPage(start, off)
    if (!info.has_cover) this.makeCover(first)
  }

  async loadOutline() {
    try {
      const outline = await this.pdf.getOutline()
      if (!outline) return
      const conv = async items => Promise.all(items.map(async it => {
        let page = null
        try {
          let dest = it.dest
          if (typeof dest === 'string') dest = await this.pdf.getDestination(dest)
          if (Array.isArray(dest)) page = (await this.pdf.getPageIndex(dest[0])) + 1
        } catch {}
        return { label: it.title, href: page ? 'p:' + page : null, subitems: it.items?.length ? await conv(it.items) : null }
      }))
      this.toc = await conv(outline)
      this.tocFlat = []
      const walk = l => l.forEach(x => { if (x.href) this.tocFlat.push([+x.href.slice(2), x.label]); x.subitems && walk(x.subitems) })
      walk(this.toc)
      this.tocFlat.sort((a, b) => a[0] - b[0])
    } catch {}
  }

  chapterOf(n) {
    let c = null
    for (const [p, l] of this.tocFlat || []) { if (p <= n) c = l; else break }
    return c
  }

  // 第一次打开、服务器没能生成封面时，用第一页做封面
  async makeCover(page) {
    try {
      const vp = page.getViewport({ scale: 480 / this.base.width })
      const c = document.createElement('canvas')
      c.width = vp.width; c.height = vp.height
      await page.render({ canvasContext: c.getContext('2d'), viewport: vp }).promise
      const blob = await new Promise(r => c.toBlob(r, 'image/jpeg', 0.82))
      if (blob) await api(`/api/books/${this.shell.id}/cover`, { method: 'PUT', body: blob })
    } catch {}
  }

  scaleFor(n) {
    const [w, h0] = this.sizes[n - 1] || [this.base.width, this.base.height]
    const p = this.shell.prefs
    const W = this.wrap.clientWidth, H = this.wrap.clientHeight
    if (p.pdfMode === 'page') return Math.min((W - 24) / w, (H - 24) / h0) * p.pdfZoom
    return Math.min(W - 24, 980) / w * p.pdfZoom
  }

  layout() {
    if (!this.wrap) return
    const keep = this.page, keepOff = this.offsetInPage()
    const paged = this.shell.prefs.pdfMode === 'page'
    this.wrap.classList.toggle('paged', paged)
    for (let n = 1; n <= this.count; n++) {
      const [w, h0] = this.sizes[n - 1] || [this.base.width, this.base.height]
      const s = this.scaleFor(n)
      const el = this.pageEls[n - 1]
      el.style.width = Math.floor(w * s) + 'px'
      el.style.height = Math.floor(h0 * s) + 'px'
      const r = this.rendered.get(n)
      if (r && Math.abs(r.scale - s) > 0.01) { r.task?.cancel?.(); this.rendered.delete(n); el.querySelector('canvas')?.remove() }
    }
    if (paged) this.showPage(keep)
    else this.scrollToPage(keep, keepOff)
    this.render(keep)
  }

  async render(n) {
    if (n < 1 || n > this.count) return
    const s = this.scaleFor(n)
    if (this.rendered.has(n)) return
    const entry = { scale: s }
    this.rendered.set(n, entry)
    try {
      const page = await this.pdf.getPage(n)
      const vp1 = page.getViewport({ scale: 1 })
      if (!this.sizes[n - 1] || this.sizes[n - 1][0] !== vp1.width) {
        this.sizes[n - 1] = [vp1.width, vp1.height]
        const el = this.pageEls[n - 1]
        const s2 = this.scaleFor(n)
        el.style.width = Math.floor(vp1.width * s2) + 'px'; el.style.height = Math.floor(vp1.height * s2) + 'px'
        entry.scale = s2
      }
      const dpr = Math.min(devicePixelRatio || 1, 2.5)
      const vp = page.getViewport({ scale: entry.scale * dpr })
      const canvas = document.createElement('canvas')
      canvas.width = Math.floor(vp.width); canvas.height = Math.floor(vp.height)
      entry.task = page.render({ canvasContext: canvas.getContext('2d', { alpha: false }), viewport: vp })
      await entry.task.promise
      if (this.rendered.get(n) !== entry) return
      const el = this.pageEls[n - 1]
      el.querySelector('canvas')?.remove()
      el.append(canvas)
      this.evict(n)
    } catch (e) {
      if (e?.name !== 'RenderingCancelledException') { console.error('pdf render', n, e); this.rendered.delete(n) }
    }
  }

  // 内存里最多保留 14 页画布
  evict(center) {
    if (this.rendered.size <= 14) return
    const far = [...this.rendered.keys()].sort((a, b) => Math.abs(b - center) - Math.abs(a - center))
    for (const n of far.slice(0, this.rendered.size - 14)) {
      this.rendered.get(n)?.task?.cancel?.()
      this.rendered.delete(n)
      this.pageEls[n - 1].querySelector('canvas')?.remove()
    }
  }

  offsetInPage() {
    if (!this.wrap || this.shell.prefs.pdfMode === 'page') return 0
    const el = this.pageEls[this.page - 1]
    if (!el) return 0
    return Math.max(0, Math.min(1, (this.wrap.scrollTop - el.offsetTop) / el.offsetHeight))
  }

  onScroll() {
    if (this.shell.prefs.pdfMode === 'page') return
    const y = this.wrap.scrollTop + this.wrap.clientHeight * 0.3
    let lo = 0, hi = this.count - 1
    while (lo < hi) { const mid = (lo + hi + 1) >> 1; if (this.pageEls[mid].offsetTop <= y) lo = mid; else hi = mid - 1 }
    this.page = lo + 1
    this.report()
  }

  report() {
    const off = this.offsetInPage()
    this.shell.onRelocate({
      location: `p:${this.page}:${off.toFixed(3)}`,
      fraction: this.count > 1 ? (this.page - 1 + off) / (this.count - 1 + (this.shell.prefs.pdfMode === 'page' ? 0 : 1)) : 1,
      chapter: this.chapterOf(this.page), label: `${this.page} / ${this.count}`,
    })
  }

  scrollToPage(n, off = 0) {
    const el = this.pageEls[n - 1]
    if (el) this.wrap.scrollTop = el.offsetTop + off * el.offsetHeight - 8
  }

  showPage(n) {
    this.pageEls.forEach((el, i) => el.classList.toggle('cur', i === n - 1))
    this.render(n); this.render(n + 1)
  }

  goToPage(n, off = 0) {
    n = Math.max(1, Math.min(this.count, n))
    this.page = n
    if (this.shell.prefs.pdfMode === 'page') this.showPage(n)
    else this.scrollToPage(n, off)
    this.render(n)
    this.report()
  }

  paged() { return this.shell.prefs.pdfMode === 'page' }
  next() {
    if (this.paged()) return this.goToPage(this.page + 1)
    this.wrap.scrollBy({ top: this.wrap.clientHeight * 0.9, behavior: 'smooth' })
  }
  prev() {
    if (this.paged()) return this.goToPage(this.page - 1)
    this.wrap.scrollBy({ top: -this.wrap.clientHeight * 0.9, behavior: 'smooth' })
  }
  goTo(loc) {
    if (typeof loc === 'string' && loc.startsWith('p:')) { const [, n, o] = loc.split(':'); this.goToPage(+n, +o || 0) }
  }
  goToFraction(f) { this.goToPage(Math.round(f * (this.count - 1)) + 1) }
  sameLocation(a, b) { return String(a).split(':')[1] === String(b).split(':')[1] }

  applyPrefs() {
    if (!this.wrap) return
    const sig = this.shell.prefs.pdfMode + this.shell.prefs.pdfZoom
    if (sig !== this.sig) { this.sig = sig; this.layout() }
  }

  // ---------- 书内搜索跳转（sec 是页码） ----------
  async find(q, sec) {
    if (sec > 0) { this.goToPage(sec); return true }
    return false
  }

  // ---------- 听书：按页读 ----------
  async ttsSegments(fromView) {
    if (fromView || !this.ttsPage) this.ttsPage = this.page
    const page = await this.pdf.getPage(this.ttsPage)
    const tc = await page.getTextContent()
    let text = ''
    for (const it of tc.items) { text += it.str; if (it.hasEOL) text += /[。！？.!?：:]$/.test(it.str.trim()) ? '\n' : '' }
    return splitSentences(text).map(s => ({ text: s.text, page: this.ttsPage }))
  }

  async ttsNextUnit() {
    if ((this.ttsPage || this.page) >= this.count) return false
    this.ttsPage = (this.ttsPage || this.page) + 1
    return true
  }

  ttsHighlight(seg) {
    if (!seg?.page) return
    if (seg.page !== this.page) this.goToPage(seg.page)
    this.pageEls.forEach((el, i) => el.classList.toggle('tts-cur', i === seg.page - 1))
  }

  ttsClear() { this.ttsPage = 0; this.pageEls?.forEach(el => el.classList.remove('tts-cur')) }

  destroy() {
    this.io?.disconnect(); this.ro?.disconnect()
    for (const r of this.rendered?.values() || []) r.task?.cancel?.()
    this.pdf?.destroy()
    this.wrap?.remove()
  }
}
