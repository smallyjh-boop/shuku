// PDF：pdf.js 渲染，按需分段下载（大文件不用等全部下完），连续滚动或单页翻页，夜间反色
// 文字层：能选字（查字典、划线、复制）、书内搜索；划线用浏览器的 CSS 高亮画在文字层上

import { loadPdfjs } from './pdfjs.js'
import { h, clear, debounce, toast, isEInk } from '../ui.js'
import { splitSentences } from './tts.js'
import { api, apiUrl, auth, net } from '../api.js'
import { around } from './dict.js'

let pdfjs = null // 第一次打开 PDF 时加载
const lib = async () => pdfjs || (pdfjs = await loadPdfjs())
const VENDOR = '/vendor/pdfjs/'
const HL_COLORS = ['yellow', 'green', 'blue', 'pink']

// 扫描版（每页都是图片）：pdf.js 读不出字的页，请书库识别（/api/files/{fid}/ocr/{页}），铺一层透明文字。
// 书库没装文字识别（501）时这次打开书库不再请求，只提示一次
let ocrMissing = false
const OCR_MIN = 8 // 一页不到这么多个字（空白不算）就当是扫描的
const hasText = c => {
  let n = 0
  for (const it of (c && c.items) || []) { n += (it.str || '').replace(/\s+/g, '').length; if (n >= OCR_MIN) return true }
  return false
}
const isCJK = ch => /[぀-ヿ㐀-鿿豈-﫿＀-￯]/.test(ch || '')

// PDF 的划线位置：p:页:页内位置:起-止（起止是这一页文字里的第几个字）
const parseLoc = l => { const m = /^p:(\d+):([\d.]*):(\d+)-(\d+)$/.exec(String(l || '')); return m ? { page: +m[1], off: +m[2] || 0, start: +m[3], end: +m[4] } : null }

// 文字层里第 start~end 个字对应的 Range
function rangeFor(root, start, end) {
  const w = document.createTreeWalker(root, NodeFilter.SHOW_TEXT)
  let pos = 0, n, started = false
  const r = document.createRange()
  while ((n = w.nextNode())) {
    const len = n.nodeValue.length
    if (!started && start < pos + len) { r.setStart(n, Math.max(0, start - pos)); started = true }
    if (started && end <= pos + len) { r.setEnd(n, Math.max(0, end - pos)); return r }
    pos += len
  }
  return null
}

// 选区在文字层里是第几个字到第几个字
function textOffsets(root, range) {
  const w = document.createTreeWalker(root, NodeFilter.SHOW_TEXT)
  let pos = 0, n, start = null, end = null
  while ((n = w.nextNode())) {
    const len = n.nodeValue.length
    const a = n === range.startContainer ? range.startOffset : null
    const b = n === range.endContainer ? range.endOffset : null
    if (start == null && (a != null || range.comparePoint(n, 0) === 0)) start = pos + (a ?? 0)
    if (start != null) {
      if (b != null) { end = pos + b; break }
      if (range.comparePoint(n, len) === 0) end = pos + len
      else if (range.comparePoint(n, 0) > 0) break
    }
    pos += len
  }
  return start != null && end != null && end > start ? [start, end] : null
}

// 离线保存时用（offline.js）：把几页的文字读一遍，用到的字符映射表（很多中文 PDF 要）、标准字体就经 Service Worker 存进缓存，
// 断网时也画得出字
export async function warmFonts(data) {
  await lib()
  const doc = await pdfjs.getDocument({ data, cMapUrl: VENDOR + 'cmaps/', cMapPacked: true, standardFontDataUrl: VENDOR + 'standard_fonts/', isEvalSupported: false }).promise
  try {
    const n = doc.numPages
    for (const p of new Set([1, 2, 3, Math.ceil(n / 4), Math.ceil(n / 2), Math.ceil(n * 3 / 4), n])) {
      if (p >= 1 && p <= n) await (await doc.getPage(p)).getTextContent()
    }
  } finally { await doc.destroy() }
}

export class Engine {
  kind = 'pdf'
  toc = []
  page = 1

  constructor(shell) {
    this.shell = shell
    this.texts = new Map() // 页 → Promise<文字内容>
    this.ocr = new Map() // 页 → Promise<识别结果>（扫描版）
    this.ocrFailed = new Map() // 页 → 识别失败的时间（一分钟内不再请求）
  }

  async open(info, saved) {
    this.info = info
    // 墨水屏、旧的网页组件在线时：每一页由书库画成图、文字另外取（server/pdfpages.go），不下载整本、不用 pdf.js。
    // 大 PDF 在设备上解析又慢又占内存（墨水屏上会卡死），旧的网页组件画某些字体还会丢字
    let local = false // 测试用：shuku.pdfLocal=1 时还是用 pdf.js
    try { local = localStorage.getItem('shuku.pdfLocal') === '1' } catch (e) {}
    if ((isEInk() || window.SHUKU_LEGACY) && net.route !== 'offline' && info.file_id && !local) {
      try {
        const r = await api(`/api/files/${info.file_id}/pdf/info`)
        if (r?.pages > 0) this.remote = r
      } catch (e) {}
    }
    if (this.remote) return this.openRemote(saved)
    await lib()
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
    this.build(saved)
    this.loadOutline()
    if (!this.info.has_cover) this.makeCover(first)
  }

  // 页面框架：每页一个占位，滚动到 / 翻到时再画
  build(saved) {
    this.wrap = h('div', { class: 'pdfv' })
    this.pagesEl = h('div', { class: 'pages' })
    this.wrap.append(this.pagesEl)
    this.shell.stage.append(this.wrap)
    this.pageEls = []
    // 按页翻时只把当前页和前后一页放进页面（几千页的 PDF 全放进去，墨水屏上光算样式就要好几秒）；滚动时全放
    for (let i = 1; i <= this.count; i++) {
      const el = h('div', { class: 'pg', dataset: { n: i } }, h('span', { class: 'no' }, i))
      this.pageEls.push(el)
      if (this.shell.prefs.pdfMode !== 'page') this.pagesEl.append(el)
    }
    this.rendered = new Map() // n -> {scale, task}
    this.io = new IntersectionObserver(es => {
      for (const e of es) if (e.isIntersecting) this.render(+e.target.dataset.n)
    }, { root: this.wrap, rootMargin: '150% 0px' })
    this.pageEls.forEach(el => this.io.observe(el))

    this.wrap.addEventListener('scroll', () => this.onScroll(), { passive: true })
    this.wrap.addEventListener('click', e => {
      const sel = getSelection()
      if (sel && !sel.isCollapsed && sel.toString().trim()) return // 刚选了字
      const hit = this.hitHighlight(e.clientX, e.clientY)
      if (hit) { this.shell.onAnnotationTap(hit.location, hit.rect); return }
      this.shell.tap(e.clientX, e.clientY)
    })
    // 选字：鼠标松开、手指抬起，或者手机上拖动选区的把手停下来以后
    let pressing = false
    const report = () => { if (!pressing) this.shell.onSelectText?.(this.selected()) }
    const later = debounce(report, 450)
    this.wrap.addEventListener('pointerdown', () => { pressing = true })
    this.wrap.addEventListener('pointerup', () => { pressing = false; setTimeout(report, 30) })
    this.wrap.addEventListener('pointercancel', () => { pressing = false; later() })
    this.onSelChange = () => { this.selGen = (this.selGen || 0) + 1; const s = getSelection(); if (s?.anchorNode && this.wrap.contains(s.anchorNode)) later() }
    document.addEventListener('selectionchange', this.onSelChange)
    this.ro = new ResizeObserver(() => this.layout())
    this.ro.observe(this.wrap)

    this.layout()
    let start = 1, off = 0
    if (saved?.location?.startsWith?.('p:')) { const [, n, o] = saved.location.split(':'); start = +n || 1; off = +o || 0 }
    else if (saved?.percent) start = Math.max(1, Math.round(saved.percent * this.count))
    this.goToPage(start, off)
  }

  // 书库画页面的方式打开：页数、每页大小、目录都是书库给的
  openRemote(saved) {
    const r = this.remote
    this.count = r.pages
    this.sizes = Array.from({ length: this.count }, (_, i) => { const s = r.sizes?.[i]; return s && s[0] > 0 && s[1] > 0 ? [s[0], s[1]] : null })
    const first = this.sizes.find(Boolean) || [595, 842]
    this.base = { width: first[0], height: first[1] }
    this.gray = isEInk() // 墨水屏只要 16 级灰度，图小一半多
    this.toc = this.outlineToc(r.outline || [])
    this.build(saved)
  }

  // 书库给的目录（每项带层级）→ 阅读器的目录树
  outlineToc(items) {
    const root = [], stack = [{ level: -1, subitems: root }]
    this.tocFlat = []
    for (const it of items) {
      const node = { label: it.title, href: it.page > 0 ? 'p:' + it.page : null, subitems: null }
      while (stack.length > 1 && stack[stack.length - 1].level >= it.level) stack.pop()
      const parent = stack[stack.length - 1]
      if (parent.node) (parent.node.subitems || (parent.node.subitems = [])).push(node)
      else root.push(node)
      stack.push({ level: it.level, node })
      if (it.page > 0) this.tocFlat.push([it.page, it.title])
    }
    this.tocFlat.sort((a, b) => a[0] - b[0])
    return root
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

  scaleFor(n, W = this.wrap.clientWidth, H = this.wrap.clientHeight) {
    const [w, h0] = this.sizes[n - 1] || [this.base.width, this.base.height]
    const p = this.shell.prefs
    if (p.pdfMode === 'page') return Math.min((W - 24) / w, (H - 24) / h0) * p.pdfZoom
    return Math.min(W - 24, 980) / w * p.pdfZoom
  }

  layout() {
    if (!this.wrap) return
    const keep = this.page, keepOff = this.offsetInPage()
    const paged = this.shell.prefs.pdfMode === 'page'
    this.wrap.classList.toggle('paged', paged)
    const W = this.wrap.clientWidth, H = this.wrap.clientHeight // 先量一次（每页都量的话，几千页要反复排版）
    for (let n = 1; n <= this.count; n++) {
      const [w, h0] = this.sizes[n - 1] || [this.base.width, this.base.height]
      const s = this.scaleFor(n, W, H)
      const el = this.pageEls[n - 1]
      el.style.width = Math.floor(w * s) + 'px'
      el.style.height = Math.floor(h0 * s) + 'px'
      const r = this.rendered.get(n)
      if (r && Math.abs(r.scale - s) > 0.01) { this.drop(n) }
    }
    this.page = keep
    this.attachPages()
    if (paged) this.showPage(keep)
    else this.scrollToPage(keep, keepOff)
    this.render(keep)
  }

  // 不要这一页的画布和文字层了（缩放变了、离得太远）
  drop(n) {
    const r = this.rendered.get(n)
    r?.task?.cancel?.(); r?.text?.cancel?.()
    this.rendered.delete(n)
    const el = this.pageEls[n - 1]
    el.querySelector('canvas, img.pgimg')?.remove()
    el.querySelector('.textLayer')?.remove()
  }

  async render(n) {
    if (n < 1 || n > this.count) return
    const s = this.scaleFor(n)
    if (this.rendered.has(n)) return
    const entry = { scale: s }
    this.rendered.set(n, entry)
    if (this.remote) return this.renderRemote(n, entry)
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
      el.prepend(canvas)
      this.evict(n)
      await this.renderText(n, page, entry)
    } catch (e) {
      if (e?.name !== 'RenderingCancelledException' && e?.name !== 'AbortException') { console.error('pdf render', n, e); this.rendered.delete(n) }
    }
  }

  // 书库画好的一页：按这一页在屏幕上的宽度要图（墨水屏要 16 级灰度）
  async renderRemote(n, entry) {
    const el = this.pageEls[n - 1]
    const dpr = Math.min(devicePixelRatio || 1, 2)
    const w = Math.max(200, Math.min(2400, Math.ceil((parseFloat(el.style.width) || this.wrap.clientWidth) * dpr / 8) * 8))
    const img = new Image()
    img.className = 'pgimg'
    img.alt = ''
    img.draggable = false
    entry.task = { cancel: () => { img.onload = img.onerror = null; img.removeAttribute('src') } }
    try {
      await new Promise((resolve, reject) => {
        img.onload = resolve
        img.onerror = () => reject(new Error('这一页没有取到'))
        img.src = apiUrl(`/api/files/${this.info.file_id}/pdf/page/${n}?w=${w}${this.gray ? '&g=1' : ''}`)
      })
    } catch (e) {
      if (this.rendered.get(n) === entry) this.rendered.delete(n) // 下次翻到时再试
      return
    }
    if (this.rendered.get(n) !== entry) return
    // 书库读不出这一页的大小时，按图的比例补上
    if (!this.sizes[n - 1] && img.naturalWidth) {
      this.sizes[n - 1] = [this.base.width, this.base.width * img.naturalHeight / img.naturalWidth]
      const s2 = this.scaleFor(n)
      el.style.width = Math.floor(this.sizes[n - 1][0] * s2) + 'px'; el.style.height = Math.floor(this.sizes[n - 1][1] * s2) + 'px'
      entry.scale = s2
    }
    el.querySelector('canvas, img.pgimg')?.remove()
    el.prepend(img)
    this.evict(n)
    await this.renderText(n, null, entry)
  }

  // 文字层：透明的文字叠在画布上，能选、能搜、能划线
  async renderText(n, page, entry) {
    const el = this.pageEls[n - 1]
    el.querySelector('.textLayer')?.remove()
    const tl = h('div', { class: 'textLayer' })
    el.style.setProperty('--scale-factor', String(entry.scale))
    el.append(tl)
    const content = await this.textOf(n)
    if (this.rendered.get(n) !== entry) { tl.remove(); return }
    if (!hasText(content) && this.canOcr()) { await this.renderOcr(n, entry, tl); return }
    if (this.remote) { this.layLines(n, tl, content.lines || []); return }
    entry.text = new pdfjs.TextLayer({ textContentSource: content, container: tl, viewport: page.getViewport({ scale: entry.scale }) })
    await entry.text.render()
    if (this.rendered.get(n) !== entry) return
    tl.append(h('div', { class: 'endOfContent' }))
    tl.dataset.ready = '1'
    this.paintHighlights()
    this.paintSearch()
  }

  textOf(n) {
    if (!this.texts.has(n)) {
      const p = this.remote
        ? api(`/api/files/${this.info.file_id}/pdf/text/${n}`).then(r => ({ items: (r.lines || []).map(l => ({ str: l.text, hasEOL: true })), lines: r.lines || [] }))
        : this.pdf.getPage(n).then(pg => pg.getTextContent())
      p.catch(() => this.texts.delete(n))
      this.texts.set(n, p)
      if (this.texts.size > 400) this.texts.delete(this.texts.keys().next().value)
    }
    return this.texts.get(n)
  }

  // 一页的文字（行末补个空格，搜索结果里好看一点）。扫描版用识别出的文字
  async pageText(n) {
    const c = await this.contentOf(n)
    return c.items.map(i => (i.str || '') + (i.hasEOL ? ' ' : '')).join('')
  }

  // ---------- 扫描版：识别出的文字 ----------
  canOcr() { return !ocrMissing && !!this.info?.file_id }

  ocrOf(n) {
    if (!this.ocr.has(n)) {
      const failed = this.ocrFailed.get(n)
      if (failed && Date.now() - failed < 60000) return Promise.reject(new Error('识别失败'))
      const p = api(`/api/files/${this.info.file_id}/ocr/${n}`)
      p.catch(e => {
        this.ocr.delete(n)
        if (e?.status === 501) this.noOcr()
        else this.ocrFailed.set(n, Date.now())
      })
      this.ocr.set(n, p)
    }
    return this.ocr.get(n)
  }

  noOcr() {
    if (ocrMissing) return
    ocrMissing = true
    toast('这个书库没有安装文字识别，扫描版 PDF 选不了字（更新书库后就有）', { duration: 5000 })
  }

  // 一页的文字内容：pdf.js 读出来的；几乎没有字（扫描版）时用书库识别出的（每行一项）
  async contentOf(n) {
    const c = await this.textOf(n)
    if (hasText(c) || !this.canOcr()) return c
    const r = await this.ocrOf(n).catch(() => null)
    if (!r?.lines?.length) return c
    return { items: r.lines.map(l => ({ str: l.text, hasEOL: true })), ocr: true }
  }

  // 识别出的文字铺成透明的文字层：每行一个 span，按识别出的位置和宽度摆好（选字、划线、查字典、搜索都用它）
  async renderOcr(n, entry, tl) {
    const el = this.pageEls[n - 1]
    const busy = h('div', { class: 'ocr-busy', role: 'status' }, '正在识别文字…')
    const t = setTimeout(() => el.append(busy), 500) // 书库已经识别过的话很快，不闪一下
    let r = null
    try { r = await this.ocrOf(n) } catch (e) {} finally { clearTimeout(t); busy.remove() }
    if (this.rendered.get(n) !== entry || !r?.lines?.length) return
    this.layLines(n, tl, r.lines)
    // 下一页也先识别着（扫描版通常整本都是）
    if (n < this.count) this.textOf(n + 1).then(c => { if (!hasText(c) && this.canOcr()) this.ocrOf(n + 1).catch(() => {}) }).catch(() => {})
  }

  // 一行一个透明的 span，按位置（0～1）和宽度摆好：识别出的文字、书库从 PDF 里取出的文字都这样铺
  layLines(n, tl, lines) {
    const el = this.pageEls[n - 1]
    tl.classList.add('ocr')
    const H = parseFloat(el.style.height) || el.clientHeight, W = parseFloat(el.style.width) || el.clientWidth
    const spans = []
    for (const line of lines) {
      const s = h('span', null, line.text)
      s.style.left = (line.x * 100).toFixed(3) + '%'
      s.style.top = (line.y * 100).toFixed(3) + '%'
      s.style.fontSize = Math.max(4, line.h * H * 0.9).toFixed(1) + 'px'
      tl.append(s, h('br', { role: 'presentation' }))
      spans.push([s, line.w * W])
    }
    tl.append(h('div', { class: 'endOfContent' }))
    tl._fit = spans
    if (el.getClientRects().length) this.fitLines(tl) // 还没显示出来的页（下一页）等翻到时再量
    tl.dataset.ready = '1'
    this.paintHighlights()
    this.paintSearch()
  }

  // 字体和原书不一样：每行横向拉伸到识别出的宽度，选字的位置才对得上（先量完再改，免得来回排版）
  fitLines(tl) {
    const spans = tl._fit
    if (!spans) return
    tl._fit = null
    const widths = spans.map(([s]) => s.getBoundingClientRect().width)
    spans.forEach(([s, w], i) => { if (widths[i] > 0 && w > 0) s.style.transform = `scaleX(${(w / widths[i]).toFixed(4)})` })
  }

  // 按页翻：页面里只放当前页和前后一页；滚动：全部按顺序放好
  attachPages() {
    if (!this.paged()) {
      if (this.pagesEl.childElementCount !== this.count) this.pageEls.forEach(el => this.pagesEl.append(el))
      return
    }
    const want = new Set([this.page - 1, this.page, this.page + 1].filter(n => n >= 1 && n <= this.count))
    for (const el of Array.from(this.pagesEl.children)) if (!want.has(+el.dataset.n)) el.remove()
    for (const n of [...want].sort((a, b) => a - b)) {
      const el = this.pageEls[n - 1]
      if (el.parentNode !== this.pagesEl) {
        const after = Array.from(this.pagesEl.children).find(c => +c.dataset.n > n)
        this.pagesEl.insertBefore(el, after || null)
      }
    }
  }

  // ---------- 翻译本页：外壳的对照面板来要这一页的段落 ----------
  translateKey() { return this.page }

  async translateParas() {
    const n = this.page
    const paras = await this.pageParagraphs(n)
    return { key: n, title: `第 ${n} 页`, paras, scanned: !paras.length && !ocrMissing }
  }

  // 一页的文字按行拼成段落：行末是句号、问号这类，或者这一行明显比别的行短，就是一段的结尾
  async pageParagraphs(n) {
    const c = await this.contentOf(n)
    const lines = []
    let cur = ''
    for (const it of c.items || []) {
      cur += it.str || ''
      if (it.hasEOL) { lines.push(cur); cur = '' }
    }
    if (cur) lines.push(cur)
    const clean = lines.map(l => l.replace(/\s+/g, ' ').trim()).filter(Boolean)
    const longest = clean.reduce((m, l) => Math.max(m, l.length), 0)
    const paras = []
    let p = ''
    for (const l of clean) {
      if (!p) p = l
      else if (/[a-z]-$/i.test(p) && /^[a-z]/.test(l)) p = p.slice(0, -1) + l // 英文行末断开的词
      else p += (isCJK(p.slice(-1)) && isCJK(l[0]) ? '' : ' ') + l
      if (/[。！？.!?:：」』”"]$/.test(l) || l.length < longest * 0.6) { paras.push(p); p = '' }
    }
    if (p) paras.push(p)
    return paras.slice(0, 200)
  }

  // 内存里最多保留 14 页画布（书库画的图：墨水屏内存小，留 4 页）
  evict(center) {
    const keep = this.remote ? 4 : 14
    if (this.rendered.size <= keep) return
    const far = [...this.rendered.keys()].sort((a, b) => Math.abs(b - center) - Math.abs(a - center))
    for (const n of far.slice(0, this.rendered.size - keep)) this.drop(n)
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
    const paged = this.shell.prefs.pdfMode === 'page'
    const next = (this.tocFlat || []).find(([p]) => p > this.page)
    this.shell.onRelocate({
      location: `p:${this.page}:${off.toFixed(3)}`,
      fraction: this.count > 1 ? (this.page - 1 + off) / (this.count - 1 + (paged ? 0 : 1)) : 1,
      chapter: this.chapterOf(this.page), label: `${this.page} / ${this.count}`,
      units: { pos: this.page - 1 + off, total: this.count, chapterEnd: next ? next[0] - 1 : null, jump: 12 },
    })
  }

  scrollToPage(n, off = 0) {
    const el = this.pageEls[n - 1]
    if (el) this.wrap.scrollTop = el.offsetTop + off * el.offsetHeight - 8
  }

  showPage(n) {
    this.page = n
    this.attachPages()
    this.cur?.classList.remove('cur')
    this.cur = this.pageEls[n - 1]
    this.cur?.classList.add('cur')
    const tl = this.cur?.querySelector('.textLayer')
    if (tl?._fit) this.fitLines(tl)
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
  lang() { return this.info?.language || '' }
  next() {
    if (this.paged()) return this.goToPage(this.page + 1)
    this.wrap.scrollBy({ top: this.wrap.clientHeight * 0.9, behavior: 'smooth' })
  }
  prev() {
    if (this.paged()) return this.goToPage(this.page - 1)
    this.wrap.scrollBy({ top: -this.wrap.clientHeight * 0.9, behavior: 'smooth' })
  }
  goTo(loc) {
    if (typeof loc !== 'string' || !loc.startsWith('p:')) return
    const [, n, o, span] = loc.split(':')
    // 划线：往上留一点，别让那句话贴着屏幕顶
    this.goToPage(+n, span ? Math.max(0, (+o || 0) - 0.05) : (+o || 0))
  }
  goToFraction(f) { this.goToPage(Math.round(f * (this.count - 1)) + 1) }
  restart() { this.goToPage(1) }
  sameLocation(a, b) { return String(a).split(':')[1] === String(b).split(':')[1] }
  compareLocations(a, b) {
    const x = parseLoc(a), y = parseLoc(b)
    if (!x || !y) return 0
    return x.page - y.page || x.start - y.start
  }

  applyPrefs() {
    if (!this.wrap) return
    const sig = this.shell.prefs.pdfMode + this.shell.prefs.pdfZoom
    if (sig !== this.sig) { this.sig = sig; this.layout() }
  }

  // ---------- 选字、划线 ----------
  selected() {
    const sel = getSelection()
    if (!sel || sel.isCollapsed || !sel.rangeCount) return null
    const text = sel.toString().replace(/\s+/g, ' ').trim()
    if (!text || text.length > 300) return null
    const range = sel.getRangeAt(0)
    const elOf = n => (n?.nodeType === 1 ? n : n?.parentElement)
    const tl = elOf(range.startContainer)?.closest?.('.textLayer')
    if (!tl || !this.wrap.contains(tl)) return null
    const sameLayer = elOf(range.endContainer)?.closest?.('.textLayer') === tl
    const pg = tl.closest('.pg')
    const page = +pg.dataset.n
    const offs = sameLayer ? textOffsets(tl, range) : null
    const r = range.getBoundingClientRect()
    const pr = pg.getBoundingClientRect()
    const off = pr.height ? Math.max(0, Math.min(1, (r.top - pr.top) / pr.height)) : 0
    const full = tl.textContent || ''
    return {
      text, context: around(full, text), rect: { left: r.left, right: r.right, top: r.top, bottom: r.bottom },
      page, off, offs, doc: document, gen: this.selGen || 0,
    }
  }

  annotationLocation(sel) {
    if (!sel?.offs || !sel.page) return null
    return `p:${sel.page}:${(sel.off || 0).toFixed(3)}:${sel.offs[0]}-${sel.offs[1]}`
  }

  clearSelection() { try { getSelection().removeAllRanges() } catch {} }

  highlightRange(b) {
    const m = parseLoc(b.location)
    if (!m) return null
    const tl = this.pageEls?.[m.page - 1]?.querySelector('.textLayer')
    if (!tl?.dataset.ready) return null
    return rangeFor(tl, m.start, m.end)
  }

  paintHighlights() {
    if (!window.CSS?.highlights || !window.Highlight) return
    const groups = Object.fromEntries(HL_COLORS.map(c => [c, []]))
    for (const b of this.shell.notes?.list || []) {
      const r = this.highlightRange(b)
      if (r) (groups[b.color] || groups.yellow).push(r)
    }
    for (const [c, rs] of Object.entries(groups)) CSS.highlights.set('pdf-hl-' + c, new Highlight(...rs))
  }

  drawAnnotation() { this.paintHighlights() }
  removeAnnotation() { this.paintHighlights() }

  // 点在了哪条划线上
  hitHighlight(x, y) {
    for (const b of this.shell.notes?.list || []) {
      const r = this.highlightRange(b)
      if (!r) continue
      for (const q of r.getClientRects()) {
        if (x >= q.left - 2 && x <= q.right + 2 && y >= q.top - 2 && y <= q.bottom + 2) {
          const br = r.getBoundingClientRect()
          return { location: b.location, rect: { left: br.left, right: br.right, top: br.top, bottom: br.bottom } }
        }
      }
    }
    return null
  }

  // ---------- 书内搜索 ----------
  paintSearch() {
    if (!window.CSS?.highlights || !window.Highlight) return
    const q = (this.searchQ || '').toLowerCase()
    const rs = []
    if (q) {
      for (const [n] of this.rendered) {
        const tl = this.pageEls[n - 1]?.querySelector('.textLayer')
        if (!tl?.dataset.ready) continue
        const text = (tl.textContent || '').toLowerCase()
        for (let i = text.indexOf(q); i >= 0 && rs.length < 500; i = text.indexOf(q, i + q.length)) {
          const r = rangeFor(tl, i, i + q.length)
          if (r) rs.push(r)
        }
      }
    }
    CSS.highlights.set('pdf-search', new Highlight(...rs))
  }

  panelTabs() { return [['toc', '目录'], ['bm', '书签'], ['notes', '笔记'], ['search', '搜索']] }

  drawPanelTab(tab, box) {
    if (tab !== 'search') return
    const input = h('input', { type: 'search', placeholder: '在书中搜索', 'aria-label': '在书中搜索', value: this.lastQuery || '' })
    const results = h('div')
    let token = 0
    const run = async () => {
      const q = input.value.trim()
      this.lastQuery = q
      this.searchQ = q
      this.paintSearch()
      const my = ++token
      clear(results)
      if (!q) return
      const status = h('p', { class: 'muted', style: { padding: '8px 18px', fontSize: '12.5px' } }, '搜索中…')
      results.append(status)
      // 书库画页面时：用全文索引一次找出在哪几页（一页页取文字太慢）
      if (this.remote) {
        const r = await api(`/api/files/${this.info.file_id}/search?q=${encodeURIComponent(q)}`).catch(() => null)
        if (my !== token) return
        if (r?.indexed) {
          for (const hit of r.hits || []) {
            const page = hit.sec
            const go = () => { this.shell.markJump(); this.searchQ = q; this.goToPage(page); this.paintSearch(); this.shell.closeOverlays(); this.shell.showUI(false) }
            const sn = hit.snippet || '', i = hit.match ? sn.indexOf(hit.match) : -1
            results.append(h('div', { class: 'hit', tabindex: 0, role: 'button', onclick: go, onkeydown: e => { if (e.key === 'Enter') go() } },
              h('div', { style: { fontSize: '12px', opacity: .6 } }, `第 ${page} 页`),
              ...(i >= 0 ? [sn.slice(0, i), h('mark', null, hit.match), sn.slice(i + hit.match.length)] : [sn])))
          }
          status.textContent = r.hits?.length ? `在 ${r.hits.length} 页里找到` : '没有找到'
          return
        }
      }
      const lq = q.toLowerCase()
      let found = 0
      for (let p = 1; p <= this.count && found < 300; p++) {
        if (my !== token) return
        // 关掉了搜索面板：别再往下搜（扫描版要一页页识别文字，很费书库的 CPU）
        if (!results.isConnected || !this.shell.panel.classList.contains('show')) return
        if (p % 10 === 0) status.textContent = `搜索中 ${Math.round(p / this.count * 100)}%`
        let text
        try { text = await this.pageText(p) } catch { continue }
        const low = text.toLowerCase()
        for (let i = low.indexOf(lq); i >= 0 && found < 300; i = low.indexOf(lq, i + lq.length)) {
          found++
          const page = p
          const go = () => { this.shell.markJump(); this.searchQ = q; this.goToPage(page); this.paintSearch(); this.shell.closeOverlays(); this.shell.showUI(false) }
          results.append(h('div', { class: 'hit', tabindex: 0, role: 'button', onclick: go, onkeydown: e => { if (e.key === 'Enter') go() } },
            h('div', { style: { fontSize: '12px', opacity: .6 } }, `第 ${p} 页`),
            text.slice(Math.max(0, i - 30), i), h('mark', null, text.slice(i, i + q.length)), text.slice(i + q.length, i + q.length + 40)))
        }
      }
      if (my === token) status.textContent = found ? `找到 ${found} 处` : '没有找到'
    }
    input.addEventListener('keydown', e => { if (e.key === 'Enter') run() })
    box.append(h('div', { class: 'r-search' }, input), results)
    if (this.lastQuery) run()
    setTimeout(() => input.focus(), 50)
  }

  // ---------- 书内搜索跳转（sec 是页码） ----------
  async find(q, sec) {
    this.searchQ = q
    if (sec > 0) { this.goToPage(sec); this.paintSearch(); return true }
    for (let p = 1; p <= this.count; p++) {
      if ((await this.pageText(p).catch(() => '')).toLowerCase().includes(q.toLowerCase())) { this.goToPage(p); this.paintSearch(); return true }
    }
    return false
  }

  // ---------- 听书：按页读 ----------
  async ttsSegments(fromView) {
    if (fromView || !this.ttsPage) this.ttsPage = this.page
    return this.pageSegments(this.ttsPage)
  }

  async pageSegments(n) {
    const tc = await this.contentOf(n)
    let text = ''
    for (const it of tc.items) { text += it.str; if (it.hasEOL) text += /[。！？.!?：:]$/.test(it.str.trim()) ? '\n' : '' }
    return splitSentences(text).map(s => ({ text: s.text, page: n }))
  }

  // App 里的听书：按页往后取文字，不需要渲染
  ttsUnitKey() { return this.ttsPage || this.page }
  async ttsUnitAfter(key) {
    for (let p = (key || 0) + 1; p <= this.count; p++) {
      const segs = await this.pageSegments(p)
      if (segs.length) return { key: p, segs: segs.map((x, n) => ({ text: x.text, i: n })) }
    }
    return null
  }
  async ttsShowUnit(key) { if (this.page !== key) this.goToPage(key) }
  async ttsSegmentsOf(key) { return this.pageSegments(key) }
  ttsLocation(key) { return { location: `p:${key}:0`, fraction: this.count > 1 ? (key - 1) / (this.count - 1) : 1 } }

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
    if (this.onSelChange) document.removeEventListener('selectionchange', this.onSelChange)
    for (const r of this.rendered?.values() || []) { r.task?.cancel?.(); r.text?.cancel?.() }
    try { for (const c of [...HL_COLORS.map(c => 'pdf-hl-' + c), 'pdf-search']) CSS.highlights?.delete(c) } catch {}
    this.pdf?.destroy()
    this.wrap?.remove()
  }
}
