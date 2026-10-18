// 流式电子书：基于 foliate-js（EPUB、MOBI、AZW3、FB2，以及服务器转换成 EPUB 的 TXT 等）
// 划线（foliate 的 overlayer）、脚注弹窗、繁体转简体也在这里

import '/vendor/foliate-js/view.js'
import { Overlayer } from '/vendor/foliate-js/overlayer.js'
import * as CFI from '/vendor/foliate-js/epubcfi.js'
import { h, clear, debounce, toast, isEInk } from '../ui.js'
import { api, download } from '../api.js'
import { isNative, callJSON } from '../native.js'
import { splitSentences } from './tts.js'
import { around } from './dict.js'
import { colorOf } from './notes.js'
import { targetLang, loadDefaultLang, translateTexts, needsTranslation } from './translate.js'

const BLOCKS = 'p,h1,h2,h3,h4,h5,h6,li,blockquote,dd,dt,td,th,figcaption,pre,div'

// 翻译本页：译文放在段落的 data-shuku-tr 属性里，用 CSS（::after）显示在原文后面。
// 不往书里加节点：位置（CFI）、划线、听书、搜索都还按原文算，关掉翻译时去掉属性就行
const TR = 'data-shuku-tr'
const TR_LANG = 'data-shuku-tr-to' // 这一段已经按哪种语言处理过（不用翻的也记上，免得每次翻页都再看一遍）
const TR_AHEAD = 6 // 这一页后面再顺手翻几段，翻到下一页时不用等

const FONT_STACKS = {
  serif: '"Songti SC", "Noto Serif CJK SC", "Noto Serif SC", "Source Han Serif SC", "STSong", "SimSun", Georgia, serif',
  sans: 'system-ui, -apple-system, "PingFang SC", "Microsoft YaHei", "Noto Sans CJK SC", sans-serif',
  kai: '"Kaiti SC", "STKaiti", "KaiTi", "BiauKai", "AR PL UKai CN", serif',
}

// ---------- 繁体转简体：逐字替换文字节点，长度不变（CFI 位置、划线都还对得上） ----------
let T2S = null
async function t2sMap() {
  if (T2S) return T2S
  const r = await api('/api/dict/t2s')
  const t = [...(r.t || '')], s = [...(r.s || '')]
  const m = new Map()
  for (let i = 0; i < t.length && i < s.length; i++) if (t[i] !== s[i] && t[i].length === s[i].length) m.set(t[i], s[i])
  return (T2S = m)
}
const HAN = /[㐀-鿿豈-﫿]/
export function t2sText(s, map) {
  if (!s || !map?.size || !HAN.test(s)) return s
  let out = ''
  for (const ch of s) out += map.get(ch) || ch
  return out
}
function convertDoc(doc, map, orig) {
  if (!doc?.body || !map?.size) return
  const w = doc.createTreeWalker(doc.body, NodeFilter.SHOW_TEXT)
  let n
  while ((n = w.nextNode())) {
    const v = n.nodeValue
    if (!HAN.test(v)) continue
    const out = t2sText(v, map)
    if (out !== v) { if (orig && !orig.has(n)) orig.set(n, v); n.nodeValue = out }
  }
}
function revertDoc(doc, orig) {
  if (!doc?.body || !orig) return
  const w = doc.createTreeWalker(doc.body, NodeFilter.SHOW_TEXT)
  let n
  while ((n = w.nextNode())) if (orig.has(n)) { n.nodeValue = orig.get(n); orig.delete(n) }
}

// ---------- 脚注：注释链接（epub:type="noteref"、上标数字、[1] 这种）点了弹出内容，不跳走 ----------
const OPS = 'http://www.idpf.org/2007/ops'
const typesOf = el => new Set(((el?.getAttributeNS?.(OPS, 'type') || el?.getAttribute?.('epub:type') || '') + ' ' + (el?.getAttribute?.('role') || '')).split(/\s+/).filter(Boolean))
function isSuper(el) {
  if (!el?.matches) return false
  if (el.matches('sup')) return true
  try {
    const va = el.ownerDocument.defaultView.getComputedStyle(el).verticalAlign
    return va === 'super' || va === 'top' || va === 'text-top' || /^\d/.test(va)
  } catch { return false }
}
function footnoteRef(a) {
  const t = typesOf(a)
  if (['noteref', 'biblioref', 'glossref', 'doc-noteref', 'doc-biblioref', 'doc-glossref'].some(x => t.has(x))) return 'yes'
  if (t.has('backlink') || t.has('doc-backlink')) return null
  if (isSuper(a) || (a.children.length === 1 && isSuper(a.children[0])) || isSuper(a.parentElement)) return 'maybe'
  // 不是上标但写成 [1]、（1）、①、注1、* 这样的（光是数字的可能是章节目录，不算）
  if (/^\s*([\[［(（〔]\s*\d{1,3}\s*[\]］)）〕]|[①-⑳]|[*†‡]|注\s*\d{1,3})\s*$/.test(a.textContent || '')) return 'maybe'
  return null
}
const INLINE = 'a, span, sup, sub, em, strong, i, b, small, big'
function noteBlock(el) {
  const target = el
  while (el?.matches?.(INLINE) && el.parentElement) el = el.parentElement
  if (!el || el === el.ownerDocument.body) return target.nextElementSibling && !target.nextElementSibling.matches(INLINE) ? target.nextElementSibling : target
  return el
}
function noteText(el) {
  const sel = 'p, li, dd, blockquote, div'
  const blocks = [...el.querySelectorAll(sel)].filter(b => !b.querySelector(sel))
  const list = blocks.length > 1 ? blocks : [el]
  return list.map(b => b.textContent.replace(/\s+/g, ' ').trim()).filter(Boolean).join('\n').slice(0, 4000)
}

export class Engine {
  kind = 'flow'
  toc = []

  constructor(shell) {
    this.shell = shell
    this.orig = new WeakMap() // 繁转简前的原文，关掉时换回来
    this.secIndex = new Map() // 位置 → 第几节
  }

  async open(info, saved) {
    const loading = this.shell.loading
    const blob = await download(info.url, p => (loading.textContent = `正在下载 ${Math.round(p * 100)}%`))
    loading.textContent = info.url.includes('/epub') ? '正在排版…' : '正在打开…'
    // 1.0.0.5 以前的 App 会把每一章的内嵌页面（blob: 地址）当成外部链接拦掉，这类书一直打不开：过几秒还没打开就提示更新 App
    const oldApp = isNative && ((callJSON('appInfo') || {}).versionCode || 0) < 5
    const hint = oldApp ? setTimeout(() => this.shell.oldAppHint?.(), 6000) : 0
    try {
      await this.load(blob, info, saved)
    } finally {
      clearTimeout(hint)
    }
  }

  async load(blob, info, saved) {
    const file = new File([blob], info.name)
    const view = this.view = document.createElement('foliate-view')
    this.shell.stage.append(view)
    await view.open(file)
    this.fxl = view.isFixedLayout
    if (this.fxl) this.kind = 'comic-fxl'
    // 搜索、听书、脚注直接读章节内容（不排版）：繁转简打开时也转换
    for (const s of view.book.sections || []) {
      if (typeof s.createDocument !== 'function') continue
      const make = s.createDocument.bind(s)
      s.createDocument = async () => { const d = await make(); if (this.t2sOn && this.t2s) convertDoc(d, this.t2s); return d }
    }
    if (this.shell.prefs.t2s && !this.fxl) { try { this.t2s = await t2sMap(); this.t2sOn = true } catch {} }
    this.toc = this.tocLabels(view.book.toc || [])

    view.addEventListener('relocate', e => {
      this.last = e.detail
      this.emitRelocate()
      if (this.trOn) this.translateSoon()
    })
    view.addEventListener('load', e => this.onDocLoad(e.detail.doc))
    // 划线：每一节排好版时把这一节的划线画上
    view.addEventListener('create-overlay', e => this.drawAnnotationsIn(e.detail.index))
    view.addEventListener('draw-annotation', e => {
      const { draw, annotation } = e.detail
      draw(Overlayer.highlight, { color: colorOf(annotation.color) })
    })
    view.addEventListener('show-annotation', e => {
      const { value, range } = e.detail
      this.shell.onAnnotationTap(value, this.rectOf(range))
    })
    // 链接：注释弹出来看；真的跳走（目录、正文里的链接）时记下原来的位置，好“返回原处”
    view.addEventListener('link', e => {
      const { a, href } = e.detail
      const kind = footnoteRef(a)
      if (kind) {
        e.preventDefault()
        this.showFootnote(a, href).catch(() => { this.shell.markJump(); view.goTo(href) })
        return
      }
      this.shell.markJump()
    })
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

  emitRelocate() {
    const d = this.last
    if (!d) return
    this.shell.onRelocate({
      location: d.cfi, fraction: d.fraction, chapter: this.tr(d.tocItem?.label?.trim()),
      label: d.location ? `${Math.round(d.fraction * 100)}%` : undefined,
      units: this.units(d),
    })
  }

  // 剩余时间的计量单位：foliate 的“位置”（约 1500 字节一个）
  units(d) {
    const loc = d.location
    if (!loc?.total) return null
    const fr = this.view.getSectionFractions?.() || []
    const end = fr[(d.section?.current ?? -2) + 1]
    return { pos: loc.current, total: loc.total, chapterEnd: end != null ? Math.round(end * loc.total) : null, jump: 25 }
  }

  onDocLoad(doc) {
    if (this.t2sOn && this.t2s) convertDoc(doc, this.t2s, this.orig)
    // 书页里的点按、滑动、按键都算在读（阅读时长）
    for (const ev of ['pointerdown', 'keydown', 'touchstart', 'wheel']) doc.addEventListener(ev, () => this.shell.markActivity(), { passive: true })
    // 点击：换算成屏幕坐标后交给外壳判断左右翻页；点在划线上的交给划线
    doc.addEventListener('click', e => {
      if (e.target.closest('a[href]')) return
      const sel = doc.getSelection?.()
      if (sel && !sel.isCollapsed) return
      const c = this.view.renderer.getContents?.().find(x => x.doc === doc)
      const hit = c?.overlayer?.hitTest?.(e)
      if (hit?.[0] && !String(hit[0]).startsWith('foliate-search:')) return
      const frame = doc.defaultView?.frameElement
      const r = frame?.getBoundingClientRect() || { left: 0, top: 0 }
      this.shell.tap(r.left + e.clientX, r.top + e.clientY)
    })
    doc.addEventListener('keydown', e => this.shell.onKey(e))
    // 选中文字：划线、查字典。鼠标松开、手指抬起，或者手机上拖动选区的把手停下来以后
    let pressing = false
    const report = () => { if (!pressing) this.shell.onSelectText?.(this.selected(doc)) }
    const later = debounce(report, 450)
    doc.addEventListener('pointerdown', () => { pressing = true })
    doc.addEventListener('pointerup', () => { pressing = false; setTimeout(report, 30) })
    doc.addEventListener('pointercancel', () => { pressing = false; later() })
    doc.addEventListener('selectionchange', () => { this.selGen = (this.selGen || 0) + 1; later() })
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
      ${this.trCSS(t)}
      ::selection { background: ${t.dark ? 'rgba(134,182,203,.35)' : 'rgba(47,111,143,.22)'}; }
      aside[epub|type~="footnote"], aside[epub|type~="endnote"] { display: none; }
    `
  }

  applyPrefs(p, t) {
    const r = this.view?.renderer
    if (!r) return
    if (this.fxl) {
      r.setAttribute('zoom', p.fxlZoom === 'fit-width' ? 'fit-width' : 'fit-page')
      return
    }
    r.setAttribute('flow', p.flow)
    const margins = [16, 40, 64, 96][p.margin] ?? 40
    const narrow = innerWidth < 700
    r.setAttribute('margin', (narrow ? 28 : 44) + 'px')
    r.setAttribute('gap', (narrow ? Math.max(6, margins / 4) : margins / 8 + 3) + '%')
    r.setAttribute('max-inline-size', Math.max(480, 860 - margins * 2) + 'px')
    r.setAttribute('max-column-count', p.columns === '1' ? '1' : '2')
    if (p.animated && !isEInk()) r.setAttribute('animated', '') // 墨水屏不要翻页动画（一路残影）
    else r.removeAttribute('animated')
    r.setStyles?.(this.css(p, t))
    // overlayer 的划线颜色在深色主题下淡一点
    this.view.style.setProperty('--overlayer-highlight-opacity', t.dark ? '.28' : '.34')
    if (!this.t2sFailed && !!p.t2s !== !!this.t2sOn) this.setT2S(!!p.t2s)
  }

  // ---------- 繁体转简体 ----------
  tr(s) { return this.t2sOn && this.t2s ? t2sText(s, this.t2s) : s }
  tocLabels(items) {
    return (items || []).map(it => ({ ...it, label: this.tr(it.label), subitems: it.subitems?.length ? this.tocLabels(it.subitems) : it.subitems }))
  }
  async setT2S(on) {
    if (this.t2sBusy) return
    this.t2sBusy = true
    try {
      if (on && !this.t2s) {
        try { this.t2s = await t2sMap() } catch (e) { this.t2sFailed = true; toast('繁转简字表加载失败：' + (e.message || e), { error: true }); return }
      }
      this.t2sOn = on
      for (const c of this.view.renderer.getContents?.() || []) {
        if (on) convertDoc(c.doc, this.t2s, this.orig)
        else revertDoc(c.doc, this.orig)
        this.drawAnnotationsIn(c.index) // 文字换过以后重新画划线
      }
      this.toc = this.tocLabels(this.view.book.toc || [])
      if (this.shell.ptab === 'toc') this.shell.drawPanel()
      this.emitRelocate() // 章节名也跟着换
    } finally {
      this.t2sBusy = false
      if (!this.t2sFailed && !!this.shell.prefs.t2s !== !!this.t2sOn) this.setT2S(!!this.shell.prefs.t2s)
    }
  }

  // ---------- 翻译本页（译文、原文对照） ----------
  // 译文的样子：小一点、淡一点、左边一道竖线；墨水屏上灰色看不清，用纯黑
  trCSS(t) {
    const eink = isEInk()
    const rule = eink ? '#000' : t.dark ? 'rgba(255,255,255,.22)' : 'rgba(0,0,0,.16)'
    return `[${TR}]::after {
        content: attr(${TR}); display: block; white-space: pre-line; text-indent: 0; text-align: start;
        margin: .3em 0 .25em; padding-left: .7em; border-left: ${eink ? 3 : 2}px solid ${rule};
        font-size: .9em; line-height: 1.6; font-weight: normal; font-style: normal; letter-spacing: normal;
        color: ${eink ? '#000' : 'inherit'}; opacity: ${eink ? 1 : 0.72};
      }
      h1[${TR}]::after, h2[${TR}]::after, h3[${TR}]::after, h4[${TR}]::after, h5[${TR}]::after, h6[${TR}]::after { font-size: .62em; }`
  }

  async toggleTranslate() {
    if (this.fxl) return false
    this.trOn = !this.trOn
    this.trFailed = false
    if (this.trOn) {
      await loadDefaultLang()
      await this.translateVisible(true)
    } else this.clearTranslations()
    return this.trOn
  }

  // 换了目标语言：去掉旧的译文，开着翻译的话按新语言重翻这一页
  setTranslateLang() {
    if (!this.trOn) return
    this.trFailed = false
    this.clearTranslations(false)
    this.translateVisible(true)
  }

  translateSoon = debounce(() => this.translateVisible(false), 350)

  clearTranslations(relayout = true) {
    let n = 0
    for (const c of this.view?.renderer.getContents?.() || []) {
      for (const el of c.doc.querySelectorAll(`[${TR}], [${TR_LANG}]`)) { el.removeAttribute(TR); el.removeAttribute(TR_LANG); n++ }
    }
    if (n && relayout) this.relayout()
  }

  // 译文改变了版面：让 foliate 重新分页（停在原来那一页开头）
  relayout() { try { this.view?.renderer?.render?.() } catch (e) {} }

  // 这一页能看到的段落，再加后面几段
  visibleBlocks() {
    const range = this.last?.range
    const doc = range?.startContainer?.ownerDocument
    if (!doc?.body) return []
    const out = []
    let ahead = 0
    for (const el of leafBlocks(doc)) {
      let hit = false
      try { hit = range.intersectsNode(el) } catch (e) {}
      if (hit) out.push(el)
      else if (out.length) { if (ahead++ >= TR_AHEAD) break; out.push(el) }
    }
    return out
  }

  async translateVisible(first) {
    if (!this.trOn || this.trFailed) return
    if (this.trBusy) { this.trAgain = true; return }
    const to = targetLang()
    const lang = this.lang()
    const items = []
    for (const el of this.visibleBlocks()) {
      if (el.getAttribute(TR_LANG) === to) continue
      if (!el.getClientRects().length) continue // 看不见的（隐藏的注释之类）
      const text = el.textContent.replace(/\s+/g, ' ').trim()
      if (!needsTranslation(text, to, lang)) { el.setAttribute(TR_LANG, to); continue }
      items.push([el, text])
    }
    if (!items.length) {
      if (first) toast('这一页不用翻译（已经是要译成的语言）', { key: 'translate', duration: 2200 })
      return
    }
    this.trBusy = true
    if (first) toast('正在翻译…', { key: 'translate', duration: 20000 })
    try {
      const out = await translateTexts(items.map(x => x[1]), to)
      if (!this.trOn || targetLang() !== to) return
      items.forEach(([el, text], i) => {
        const tr = (out[i] || '').trim()
        if (tr && tr !== text) el.setAttribute(TR, tr)
        el.setAttribute(TR_LANG, to)
      })
      this.relayout()
      if (first) toast('译文在每段原文后面，翻页时接着翻', { key: 'translate', duration: 2400 })
    } catch (e) {
      // 出错就停下，不每翻一页都重试；再点一次“翻译”重新开始
      this.trFailed = true
      toast('翻译失败：' + (e.message || e) + '。再点一次“翻译”重试', { key: 'translate', error: true, duration: 6000 })
    } finally {
      this.trBusy = false
      if (this.trAgain) { this.trAgain = false; if (this.trOn && !this.trFailed) this.translateVisible(false) }
    }
  }

  paged() { return this.fxl || this.shell.prefs.flow === 'paginated' }
  lang() { const l = this.view?.book?.metadata?.language; return (Array.isArray(l) ? l[0] : l) || '' }

  // 选中的文字、所在的那段话、在屏幕上的位置（划线要用 Range 和第几节）
  selected(doc) {
    const sel = doc.getSelection?.()
    if (!sel || sel.isCollapsed || !sel.rangeCount) return null
    const text = sel.toString().replace(/\s+/g, ' ').trim()
    if (!text || text.length > 300) return null
    const range = sel.getRangeAt(0)
    let node = range.commonAncestorContainer
    if (node.nodeType !== 1) node = node.parentElement
    const block = node?.closest?.(BLOCKS) || node
    const index = this.view.renderer.getContents?.().find(c => c.doc === doc)?.index
    return { text, context: around(block?.textContent, text), rect: this.rectOf(range), range: range.cloneRange(), index, doc, gen: this.selGen || 0 }
  }

  rectOf(range) {
    try {
      const r = range.getBoundingClientRect()
      const f = range.startContainer.ownerDocument.defaultView?.frameElement?.getBoundingClientRect() || { left: 0, top: 0 }
      return { left: r.left + f.left, right: r.right + f.left, top: r.top + f.top, bottom: r.bottom + f.top }
    } catch { return null }
  }

  clearSelection() {
    for (const c of this.view?.renderer.getContents?.() || []) { try { c.doc.getSelection().removeAllRanges() } catch {} }
  }

  rtl() { return this.view?.book?.dir === 'rtl' }
  next() { return this.view?.next() }
  prev() { return this.view?.prev() }
  goTo(target) {
    const sec = /^sec:(\d+):?([\d.]*)/.exec(String(target ?? ''))
    if (sec) return this.view?.renderer.goTo({ index: +sec[1], anchor: Math.min(1, +sec[2] || 0) }).catch(() => {})
    return this.view?.goTo(target).catch(() => {})
  }
  goToFraction(f) { return this.view?.goToFraction(f) }
  restart() { return this.view?.goToTextStart().catch(() => this.view.goToFraction(0)) }

  excerpt() {
    try { return this.last?.range?.toString().trim().slice(0, 120) } catch { return '' }
  }

  sameLocation(a, b) {
    // 同一章且位置相近就算同一页：比较 CFI 去掉字符偏移后的部分
    const base = s => String(s).replace(/:\d+\)$/, ')')
    return a === b || base(a) === base(b)
  }

  compareLocations(a, b) { try { return CFI.compare(a, b) } catch { return 0 } }

  // ---------- 划线 ----------
  canAnnotate() { return !this.fxl } // 固定版式（漫画式的 EPUB）不能划线

  annotationLocation(sel) {
    if (this.fxl || !sel?.range || sel.index == null) return null
    try { return this.view.getCFI(sel.index, sel.range) } catch { return null }
  }

  sectionOf(cfi) {
    if (!this.secIndex.has(cfi)) {
      let i = -1
      try { i = this.view.resolveNavigation(cfi)?.index ?? -1 } catch {}
      this.secIndex.set(cfi, i)
    }
    return this.secIndex.get(cfi)
  }

  drawAnnotationsIn(index) {
    if (this.fxl) return
    for (const b of this.shell.notes?.list || []) {
      if (this.sectionOf(b.location) === index) this.view.addAnnotation({ value: b.location, color: b.color }).catch?.(() => {})
    }
  }

  drawAnnotation(b) { if (!this.fxl) this.view.addAnnotation({ value: b.location, color: b.color })?.catch?.(() => {}) }
  removeAnnotation(b) { this.view.deleteAnnotation({ value: b.location })?.catch?.(() => {}) }

  // ---------- 脚注 ----------
  async showFootnote(a, href) {
    const book = this.view.book
    const target = await book.resolveHref(href)
    if (!target || target.index == null || !target.anchor) throw new Error('找不到注释')
    const loaded = this.view.renderer.getContents?.().find(c => c.index === target.index)
    const doc = loaded?.doc || await book.sections[target.index].createDocument()
    let el = target.anchor(doc)
    if (el?.startContainer) el = el.startContainer.nodeType === 1 ? el.startContainer : el.startContainer.parentElement
    if (!el) throw new Error('找不到注释')
    const block = noteBlock(el)
    // 指向的是标题、或者一大段正文：不像注释，照常跳过去
    if (block.matches?.('h1, h2, h3, h4, h5, h6, body, section, nav')) throw new Error('不是注释')
    const text = this.tr(noteText(block))
    if (!text || text.length > 2000) throw new Error('不是注释')
    const ar = a.getBoundingClientRect()
    const f = a.ownerDocument.defaultView?.frameElement?.getBoundingClientRect() || { left: 0, top: 0 }
    const rect = { left: ar.left + f.left, right: ar.right + f.left, top: ar.top + f.top, bottom: ar.bottom + f.top }
    this.shell.showPopover([
      h('div', { class: 'fn-text' }, text.split('\n').map(p => h('p', null, p))),
      h('div', { class: 'row' },
        h('button', { class: 'btn small quiet', type: 'button', onclick: () => { this.shell.hidePopover(); this.shell.markJump(); this.view.goTo(href) } }, '跳到注释所在处'),
        h('span', { class: 'grow' }),
        h('button', { class: 'btn small', type: 'button', onclick: () => this.shell.hidePopover() }, '关闭')),
    ], rect)
  }

  panelTabs() { return this.fxl ? [['toc', '目录'], ['bm', '书签'], ['search', '搜索']] : [['toc', '目录'], ['bm', '书签'], ['notes', '笔记'], ['search', '搜索']] }

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
          const go = () => { this.shell.markJump(); this.view.goTo(it.cfi); this.shell.closeOverlays(); this.shell.showUI(false) }
          results.append(h('div', { class: 'hit', tabindex: 0, role: 'button', onclick: go, onkeydown: e => { if (e.key === 'Enter') go() } },
            r.label ? h('div', { style: { fontSize: '12px', opacity: .6 } }, this.tr(r.label)) : null,
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

// 要翻译的段落：最内层的块（和听书一样），代码块（pre）不翻。一个文档只算一次（翻译只改属性，结构不变）
const leafCache = new WeakMap()
function leafBlocks(doc) {
  let list = leafCache.get(doc)
  if (!list) {
    const all = [...doc.body.querySelectorAll(BLOCKS)]
    list = all.filter(el => !el.closest('pre') && el.textContent.trim() && ![...el.querySelectorAll(BLOCKS)].some(x => x.textContent.trim()))
    leafCache.set(doc, list)
  }
  return list
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
