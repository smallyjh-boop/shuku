// 流式电子书：基于 foliate-js（EPUB、MOBI、AZW3、FB2，以及服务器转换成 EPUB 的 TXT 等）

import '/vendor/foliate-js/view.js'
import { h, clear, fill } from '../ui.js'
import { splitSentences } from './tts.js'

const BLOCKS = 'p,h1,h2,h3,h4,h5,h6,li,blockquote,dd,dt,td,th,figcaption,pre,div'
import { download } from '../api.js'

const FONT_STACKS = {
  serif: '"Songti SC", "Noto Serif CJK SC", "Noto Serif SC", "Source Han Serif SC", "STSong", "SimSun", Georgia, serif',
  sans: 'system-ui, -apple-system, "PingFang SC", "Microsoft YaHei", "Noto Sans CJK SC", sans-serif',
  kai: '"Kaiti SC", "STKaiti", "KaiTi", "BiauKai", "AR PL UKai CN", serif',
}

export class Engine {
  kind = 'flow'
  toc = []

  constructor(shell) { this.shell = shell }

  async open(info, saved) {
    const loading = this.shell.loading
    const blob = await download(info.url, p => (loading.textContent = `正在下载 ${Math.round(p * 100)}%`))
    loading.textContent = info.url.includes('/epub') ? '正在排版…' : '正在打开…'
    const file = new File([blob], info.name)
    const view = this.view = document.createElement('foliate-view')
    this.shell.stage.append(view)
    await view.open(file)
    this.fxl = view.isFixedLayout
    if (this.fxl) this.kind = 'comic-fxl'
    this.toc = view.book.toc || []

    view.addEventListener('relocate', e => {
      const d = e.detail
      this.last = d
      const loc = d.location
      this.shell.onRelocate({
        location: d.cfi, fraction: d.fraction, chapter: d.tocItem?.label?.trim(),
        label: loc ? `${Math.round(d.fraction * 100)}%` : undefined,
      })
    })
    view.addEventListener('load', e => this.onDocLoad(e.detail.doc))
    this.applyPrefs(this.shell.prefs, this.shell.theme)
    let target = saved?.location
    const sec = /^sec:(\d+):?([\d.]*)/.exec(String(target || ''))
    if (target && !String(target).startsWith('epubcfi(')) target = null
    if (sec) {
      // 手表按章节读到的位置：第几节 + 节内比例
      await view.init({ showTextStart: true })
      await view.renderer.goTo({ index: +sec[1], anchor: Math.min(1, +sec[2] || 0) }).catch(() => {})
    }
    else if (target) {
      await view.init({ lastLocation: target }).catch(() => view.init({ showTextStart: true }))
      await this.checkRestored(saved)
    }
    else if (saved?.percent > 0) { await view.init({ showTextStart: true }); await view.goToFraction(saved.percent) }
    else await view.init({ showTextStart: true })
  }

  // 书的结构变了（比如 TXT 按新规则重新分了章）：记下的位置会落到别的章节去，改按百分比找回大概的位置
  async checkRestored(saved) {
    const p = +saved?.percent
    const got = this.last?.section?.current
    if (!(p > 0) || got == null) return
    const fr = this.view.getSectionFractions?.() || []
    const want = fr.findIndex((f, i) => i < fr.length - 1 && p >= f && p < fr[i + 1])
    if (want >= 0 && Math.abs(want - got) > 1) await this.view.goToFraction(p).catch(() => {})
  }

  onDocLoad(doc) {
    // 点击：换算成屏幕坐标后交给外壳判断左右翻页
    doc.addEventListener('click', e => {
      if (e.target.closest('a[href]')) return
      const sel = doc.getSelection?.()
      if (sel && !sel.isCollapsed) return
      const frame = doc.defaultView?.frameElement
      const r = frame?.getBoundingClientRect() || { left: 0, top: 0 }
      this.shell.tap(r.left + e.clientX, r.top + e.clientY)
    })
    doc.addEventListener('keydown', e => this.shell.onKey(e))
    // 在书里滚轮翻页
    let at = 0
    doc.addEventListener('wheel', e => {
      if (!this.paged()) return
      e.preventDefault()
      if (Date.now() - at < 350) return
      at = Date.now()
      ;(e.deltaY || e.deltaX) > 0 ? this.next() : this.prev()
    }, { passive: false })
  }

  css(p, t) {
    const font = FONT_STACKS[p.font]
    const ff = font ? `font-family: ${font} !important;` : ''
    return `@namespace epub "http://www.idpf.org/2007/ops";
      html { color-scheme: ${t.dark ? 'dark' : 'light'}; background: transparent !important; }
      html, body { color: ${t.fg} !important; background: transparent !important; }
      body { font-size: ${p.fontSize}px !important; }
      body, p, div, span, li, blockquote, dd, dt, td, th, h1, h2, h3, h4, h5, h6 { ${ff} }
      p, li, blockquote, dd, div {
        line-height: ${p.lineHeight} !important;
        ${p.justify ? 'text-align: justify;' : ''}
      }
      p { margin-top: 0 !important; margin-bottom: ${p.paraSpace}em !important; ${p.indent ? 'text-indent: 2em;' : ''} }
      p[style*="center"], p[align="center"], .center, p.title { text-indent: 0; }
      h1, h2, h3, h4 { line-height: 1.4 !important; color: ${t.fg} !important; }
      * { color: inherit; border-color: ${t.dark ? 'rgba(255,255,255,.15)' : 'rgba(0,0,0,.15)'} }
      a:link, a:visited { color: ${t.dark ? '#86B6CB' : '#2F6F8F'} !important; }
      ${t.dark ? 'img, svg image { filter: brightness(.82) contrast(1.05); }' : ''}
      pre { white-space: pre-wrap !important; }
      ::highlight(tts) { background-color: ${t.dark ? 'rgba(134,182,203,.28)' : 'rgba(47,111,143,.16)'}; }
      ::selection { background: ${t.dark ? 'rgba(134,182,203,.35)' : 'rgba(47,111,143,.22)'}; }
      aside[epub|type~="footnote"], aside[epub|type~="endnote"] { display: none; }
    `
  }

  applyPrefs(p, t) {
    const r = this.view?.renderer
    if (!r) return
    if (!this.fxl) {
      r.setAttribute('flow', p.flow)
      const margins = [16, 40, 64, 96][p.margin] ?? 40
      const narrow = innerWidth < 700
      r.setAttribute('margin', (narrow ? 28 : 44) + 'px')
      r.setAttribute('gap', (narrow ? Math.max(6, margins / 4) : margins / 8 + 3) + '%')
      r.setAttribute('max-inline-size', Math.max(480, 860 - margins * 2) + 'px')
      r.setAttribute('max-column-count', p.columns === '1' ? '1' : '2')
      if (p.animated) r.setAttribute('animated', '')
      else r.removeAttribute('animated')
      r.setStyles?.(this.css(p, t))
    }
  }

  paged() { return this.fxl || this.shell.prefs.flow === 'paginated' }
  rtl() { return this.view?.book?.dir === 'rtl' }
  next() { return this.view?.next() }
  prev() { return this.view?.prev() }
  goTo(target) { return this.view?.goTo(target).catch(() => {}) }
  goToFraction(f) { return this.view?.goToFraction(f) }

  excerpt() {
    try { return this.last?.range?.toString().trim().slice(0, 120) } catch { return '' }
  }

  sameLocation(a, b) {
    // 同一章且位置相近就算同一页：比较 CFI 去掉字符偏移后的部分
    const base = s => String(s).replace(/:\d+\)$/, ')')
    return a === b || base(a) === base(b)
  }

  panelTabs() { return [['toc', '目录'], ['bm', '书签'], ['search', '搜索']] }

  drawPanelTab(tab, box) {
    if (tab !== 'search') return
    const input = h('input', { type: 'search', placeholder: '在书中搜索', 'aria-label': '在书中搜索', value: this.lastQuery || '' })
    const results = h('div')
    let token = 0
    const run = async () => {
      const q = input.value.trim()
      this.lastQuery = q
      const my = ++token
      clear(results)
      this.view.clearSearch?.()
      if (!q) return
      const status = h('p', { class: 'muted', style: { padding: '8px 18px', fontSize: '12.5px' } }, '搜索中…')
      results.append(status)
      let n = 0
      for await (const r of this.view.search({ query: q })) {
        if (my !== token) return
        if (r === 'done') break
        if (r.progress != null) { status.textContent = `搜索中 ${Math.round(r.progress * 100)}%`; continue }
        for (const it of r.subitems || []) {
          if (n++ > 300) break
          const ex = it.excerpt || {}
          results.append(h('div', { class: 'hit', onclick: () => { this.view.goTo(it.cfi); this.shell.closeOverlays(); this.shell.showUI(false) } },
            r.label ? h('div', { style: { fontSize: '12px', opacity: .6 } }, r.label) : null,
            ex.pre || '', h('mark', null, ex.match || q), ex.post || ''))
        }
      }
      if (my === token) status.textContent = n ? `找到 ${n} 处` : '没有找到'
    }
    input.addEventListener('keydown', e => { if (e.key === 'Enter') run() })
    box.append(h('div', { class: 'r-search' }, input), results)
    if (this.lastQuery) run()
    setTimeout(() => input.focus(), 50)
  }

  // ---------- 搜索结果跳转 ----------
  async find(q, sec) {
    if (sec >= 0) {
      await this.view.goTo(sec).catch(() => {})
      for await (const r of this.view.search({ query: q, index: sec })) {
        if (r?.cfi) { await this.view.goTo(r.cfi); this.view.clearSearch?.(); return true }
        if (r === 'done') break
      }
      return false
    }
    for await (const r of this.view.search({ query: q })) {
      const cfi = r?.subitems?.[0]?.cfi
      if (cfi) { await this.view.goTo(cfi); this.view.clearSearch?.(); return true }
      if (r === 'done') break
    }
    return false
  }

  // ---------- 听书 ----------
  current() { return this.view.renderer.getContents?.()?.[0] }

  // 当前章节的句子（每句带一个 Range，用来高亮和翻页）
  async ttsSegments(fromView) {
    const c = this.current()
    if (!c?.doc) return []
    const doc = c.doc
    const segs = segmentsOf(doc, true)
    this.ttsLens.set(c.index, segs.map(x => x.text.length))
    if (!fromView) return segs
    // 从当前这一页开始读：跳过已经在前面的句子
    const vis = this.last?.range
    if (!vis || vis.startContainer.ownerDocument !== doc) return segs
    const i = segs.findIndex(s => {
      try { return vis.comparePoint(s.range.endContainer, s.range.endOffset) >= 0 } catch { return true }
    })
    return i > 0 ? segs.slice(i) : segs
  }

  async ttsNextUnit() {
    const before = this.current()?.index
    const total = this.view.book.sections.length
    if (before == null || before >= total - 1) return false
    await this.view.goTo(before + 1)
    // 等新章节加载好
    for (let k = 0; k < 40; k++) {
      if (this.current()?.index !== before) return true
      await new Promise(r => setTimeout(r, 100))
    }
    return this.current()?.index !== before
  }

  // ---------- App 里的听书：不排版直接取后面章节的文字 ----------
  ttsLens = new Map() // 节序号 → 每句的字数（算进度用）
  ttsUnitKey() { return this.current()?.index }

  async ttsUnitAfter(key) {
    const sections = this.view.book.sections
    for (let i = (key ?? -1) + 1; i < sections.length; i++) {
      const sec = sections[i]
      if (sec.linear === 'no' || !sec.createDocument) continue
      const doc = await sec.createDocument()
      const segs = segmentsOf(doc, false)
      if (segs.length) {
        this.ttsLens.set(i, segs.map(x => x.text.length))
        return { key: i, segs: segs.map((x, n) => ({ text: x.text, i: n })) }
      }
    }
    return null
  }

  async ttsShowUnit(key) {
    if (this.ttsUnitKey() === key) return
    await this.view.goTo(key).catch(() => {})
    for (let k = 0; k < 40 && this.ttsUnitKey() !== key; k++) await new Promise(r => setTimeout(r, 100))
  }

  async ttsSegmentsOf(key) {
    if (this.ttsUnitKey() !== key) return []
    return this.ttsSegments(false)
  }

  // 熄屏时读到哪里：sec:节:节内比例（服务器和手表也认这个格式）
  ttsLocation(key, i) {
    const lens = this.ttsLens.get(key)
    if (!lens?.length) return null
    const total = lens.reduce((a, b) => a + b, 0) || 1
    const before = lens.slice(0, i).reduce((a, b) => a + b, 0)
    const frac = before / total
    const fr = this.view.getSectionFractions?.() || []
    const a = fr[key] ?? 0, b = fr[key + 1] ?? 1
    return { location: `sec:${key}:${frac.toFixed(4)}`, fraction: Math.min(1, a + (b - a) * frac) }
  }

  ttsHighlight(seg) {
    if (!seg?.range) return
    const win = seg.range.startContainer.ownerDocument?.defaultView
    try {
      if (win?.CSS?.highlights && win.Highlight) win.CSS.highlights.set('tts', new win.Highlight(seg.range))
    } catch {}
    // 读到页面外就翻页
    this.view.renderer.scrollToAnchor?.(seg.range)
  }

  ttsClear() {
    try { this.current()?.doc?.defaultView?.CSS?.highlights?.delete('tts') } catch {}
  }

  destroy() {
    try { this.view?.close() } catch {}
    this.view?.remove()
  }
}

// 一个文档里要朗读的句子：只取最内层的块，避免 li > p 这种重复朗读
function segmentsOf(doc, withRanges) {
  if (!doc?.body) return []
  const all = [...doc.body.querySelectorAll(BLOCKS)]
  const set = new Set(all)
  const leaves = all.filter(el => el.textContent.trim() && ![...el.querySelectorAll(BLOCKS)].some(x => set.has(x) && x.textContent.trim()))
  if (!leaves.length && doc.body.textContent.trim()) leaves.push(doc.body)
  const segs = []
  for (const el of leaves) {
    for (const s of splitSentences(el.textContent)) {
      if (!withRanges) { segs.push({ text: s.text }); continue }
      const range = rangeOf(doc, el, s.start, s.end)
      if (range) segs.push({ text: s.text, range })
    }
  }
  return segs
}

// 元素内第 start~end 个字符对应的 Range
function rangeOf(doc, el, start, end) {
  const walker = doc.createTreeWalker(el, NodeFilter.SHOW_TEXT)
  let pos = 0, node, r = doc.createRange(), started = false
  while ((node = walker.nextNode())) {
    const len = node.nodeValue.length
    if (!started && start < pos + len) { r.setStart(node, Math.max(0, start - pos)); started = true }
    if (started && end <= pos + len) { r.setEnd(node, Math.max(0, end - pos)); return r }
    pos += len
  }
  if (started) { r.setEndAfter(el.lastChild || el); return r }
  return null
}
