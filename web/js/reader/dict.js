// 选词查字典：阅读时选中字词，弹出读音和释义（本地字典：中文、英文），可以让 AI 结合上下文解释（什么语言都行），
// 点喇叭听发音。电脑上浮在选中的文字旁边，手机上从底部弹出，不挡住系统的“复制”菜单。
// 弹窗最上面一行是划线工具：几种颜色、写笔记、复制（阅读设置里关了查字典时只显示这一行）。

import { h, fill, icon, toast } from '../ui.js'
import { api } from '../api.js'
import { swipeDown } from './reader.js'
import { COLORS } from './notes.js'

const LANG_TAGS = { zh: 'zh-CN', 'zh-TW': 'zh-TW', en: 'en-US', ja: 'ja-JP', ko: 'ko-KR', th: 'th-TH', vi: 'vi-VN', fr: 'fr-FR', de: 'de-DE', es: 'es-ES', it: 'it-IT', pt: 'pt-BR', ru: 'ru-RU' }

export class DictPopup {
  constructor(shell) {
    this.shell = shell
    this.seq = 0
    this.el = h('div', { class: 'r-dict', role: 'dialog', 'aria-label': '查字典' })
    // 点弹窗里的东西不要传到阅读器（不翻页、不切换工具栏）
    for (const ev of ['click', 'pointerdown', 'wheel']) this.el.addEventListener(ev, e => e.stopPropagation())
    shell.root.append(this.el)
    swipeDown(this.el, () => this.hide())
  }

  get open() { return this.el.classList.contains('show') }

  hide() {
    // 关掉以后，同一次选择晚到的“选中了文字”不再弹出来（选区没动过，只是事件慢了一步）
    if (this.open && this.sel?.gen != null) this.dismissedGen = this.sel.gen
    this.el.classList.remove('show')
    this.seq++
    this.text = ''
    try { this.audio?.pause() } catch {}
  }

  // sel：{ text, context, rect, ... }；tools：显示划线工具；lookup：查字典
  async show(sel, { tools = false, lookup = true } = {}) {
    const { context, rect } = sel
    const text = (sel.text || '').trim()
    if (!text || (text === this.text && this.open)) return
    if (sel.gen != null && sel.gen === this.dismissedGen) return
    const my = ++this.seq
    this.sel = sel
    this.text = text
    this.context = context || ''
    this.lang = this.shell.engine?.lang?.() || ''
    this.result = null
    this.lookup = lookup
    this.tools = tools ? this.toolRow(sel) : null
    this.aiBox = h('div', { class: 'ai' })
    this.body = h('div', { class: 'body' }, lookup ? h('p', { class: 'muted' }, '查询中…') : null)
    this.el.classList.toggle('tools-only', !lookup)
    this.render(text, '')
    this.el.classList.add('show')
    this.place(rect)
    if (!lookup) return
    let r
    try {
      r = await api(`/api/dict?q=${encodeURIComponent(text)}&lang=${encodeURIComponent(this.lang)}`)
    } catch (e) {
      if (my === this.seq) fill(this.body, h('p', { class: 'muted' }, e.message))
      return
    }
    if (my !== this.seq) return
    this.result = r
    this.draw(r)
    // 本地字典里查不到（其他语言、新词、整句话）：配好了 AI 就直接让 AI 解释
    if (!r.words.length && !(r.chars || []).length && r.ai) this.explain()
  }

  // 划线工具：颜色（点了就划线）、写笔记、复制
  toolRow(sel) {
    const notes = this.shell.notes
    const done = () => { this.hide(); this.shell.engine?.clearSelection?.() }
    return h('div', { class: 'seltools', role: 'toolbar', 'aria-label': '划线' },
      COLORS.map(([c, css, name]) => h('button', { class: 'dot', style: { '--c': css }, title: '划线（' + name + '）', 'aria-label': '划线（' + name + '）', onclick: async () => { done(); await notes.add(sel, c) } })),
      h('button', { class: 'btn small quiet', onclick: async () => { done(); await notes.add(sel, COLORS[0][0], true) } }, icon('edit'), '笔记'),
      h('button', { class: 'btn small quiet', onclick: () => { copyText(sel); done() } }, '复制'),
      !this.lookup ? h('button', { class: 'icon-btn', title: '关闭', 'aria-label': '关闭', onclick: () => this.hide() }, icon('close')) : null)
  }

  // 标题行：选中的词、读音、发音 / AI / 关闭
  render(word, pron) {
    if (!this.lookup) { fill(this.el, this.tools); return }
    const speak = h('button', { class: 'icon-btn', title: '发音', 'aria-label': '发音', onclick: () => this.speak(this.speakText()) }, icon('volume'))
    const ai = h('button', { class: 'btn small quiet ai-btn', title: '让 AI 结合上下文解释', onclick: () => this.explain() }, icon('sparkle'), 'AI 解释')
    const close = h('button', { class: 'icon-btn', title: '关闭', 'aria-label': '关闭', onclick: () => this.hide() }, icon('close'))
    this.hd = h('div', { class: 'hd' }, h('div', { class: 'hw' }, h('b', null, word), pron ? h('span', { class: 'py' }, pron) : null), speak, ai, close)
    fill(this.el, this.tools, this.hd, this.body, this.aiBox)
  }

  speakText() {
    const w = this.result?.words?.[0]
    return (w && (w.word || '')) || this.text
  }

  draw(r) {
    const words = r.words || [], chars = r.chars || []
    const w0 = words[0]
    const zh = (r.lang || '').startsWith('zh')
    const pron = w0 ? (zh ? (w0.pinyin || []).join(' / ') : (w0.ph ? `/${w0.ph}/` : '')) : (zh && chars.length ? chars.map(c => (c.pinyin || [])[0] || '').join(' ') : '')
    this.render(w0?.word || this.text, pron)
    const parts = []
    if (zh) {
      if (w0) parts.push(this.zhWord(w0))
      if (chars.length && !(w0 && chars.length === 1 && chars[0].ch === w0.word)) {
        parts.push(h('div', { class: 'chars' }, h('h5', null, '单字'), chars.map(c => h('div', { class: 'ch-row' },
          h('b', { class: 'ch' }, c.ch), h('span', { class: 'py' }, (c.pinyin || []).join(' / ')), h('span', { class: 'def' }, c.def || '')))))
      }
    } else {
      for (const w of words) parts.push(this.enWord(w, w !== w0))
    }
    if (!parts.length) {
      parts.push(h('p', { class: 'muted' }, !r.installed ? '这个书库没有装本地字典。'
        : (zh || r.lang === 'en') ? `本地字典里没有“${r.q}”。` : '本地字典只有中文和英文，其他语言请用 AI 解释。'))
      if (!r.ai) parts.push(h('p', { class: 'muted small' }, '在“设置 → 识别与整理”里配好 AI 服务，就能让 AI 结合上下文解释，任何语言都行。'))
    }
    if (parts.length && r.source && (w0 || chars.length)) parts.push(h('div', { class: 'src' }, r.source))
    fill(this.body, ...parts)
  }

  zhWord(w) {
    const box = h('div', { class: 'zh' })
    const rds = w.readings || []
    const head = (w.pinyin || [])[0] || ''
    let shown = 0, more = []
    for (const rd of rds) {
      // 读音和标题行一样就不重复写；和大陆读音对不上的标“台湾读音”
      const label = (rds.length > 1 || rd.tw || rd.py !== head) ? h('div', { class: 'rd' }, rd.py, rd.tw ? h('small', null, '台湾读音') : null) : null
      const ol = h('ol')
      for (const s of rd.senses || []) {
        const li = h('li', null, s.pos ? h('span', { class: 'pos' }, s.pos) : null, s.def,
          s.ex?.length ? h('div', { class: 'ex' }, s.ex.join(' ')) : null,
          s.see?.length ? h('div', { class: 'ex' }, s.see.join(' ')) : null,
          s.syn ? h('div', { class: 'ex' }, '近义：' + s.syn) : null,
          s.ant ? h('div', { class: 'ex' }, '反义：' + s.ant) : null)
        if (shown++ >= 6) { li.hidden = true; more.push(li) }
        ol.append(li)
      }
      box.append(label || '', ol)
    }
    if (more.length) box.append(h('button', { class: 'more', onclick: e => { more.forEach(li => (li.hidden = false)); e.target.remove() } }, `展开全部（还有 ${more.length} 条）`))
    if (!rds.length && w.en) box.append(h('p', { class: 'en' }, w.en))
    return box
  }

  enWord(w, sub) {
    return h('div', { class: 'enw' + (sub ? ' sub' : '') },
      sub ? h('div', { class: 'rd' }, h('b', null, w.word), w.ph ? h('span', { class: 'py' }, `/${w.ph}/`) : null) : null,
      w.note ? h('div', { class: 'note' }, w.note) : null,
      (w.tr || []).map(l => h('div', { class: 'tr' }, l)),
      w.def?.length ? h('div', { class: 'ex' }, w.def.join('；')) : null)
  }

  async explain() {
    if (this.aiBusy) return
    const my = this.seq
    this.aiBusy = true
    const head = h('div', { class: 'ai-h' }, icon('sparkle'), 'AI 解释')
    fill(this.aiBox, head, h('p', { class: 'muted' }, 'AI 正在看上下文…'))
    try {
      const r = await api('/api/dict/explain', { method: 'POST', body: { text: this.text, context: this.context, title: this.shell.info?.title || '', author: this.shell.info?.author || '', lang: this.lang } })
      if (my !== this.seq) return
      fill(this.aiBox, head, r.ok ? h('div', { class: 'ai-a' }, r.answer) : h('p', { class: 'muted' }, r.reason || 'AI 没有回答'))
    } catch (e) {
      if (my === this.seq) fill(this.aiBox, head, h('p', { class: 'muted' }, e.message))
    } finally {
      this.aiBusy = false
    }
  }

  async speak(text) {
    const lang = this.result?.lang || this.lang
    try {
      try { this.audio?.pause() } catch {}
      const res = await api('/api/dict/speak', { method: 'POST', body: { text, lang }, raw: true })
      const url = URL.createObjectURL(await res.blob())
      this.audio = new Audio(url)
      this.audio.onended = () => URL.revokeObjectURL(url)
      await this.audio.play()
    } catch (e) {
      // 书库的语音服务用不了：用浏览器自带的语音
      if (window.speechSynthesis && window.SpeechSynthesisUtterance) {
        const u = new SpeechSynthesisUtterance(text)
        u.lang = LANG_TAGS[lang] || lang || 'zh-CN'
        speechSynthesis.cancel()
        speechSynthesis.speak(u)
      } else toast('发音失败：' + (e.message || e), { error: true })
    }
  }

  place(rect) { placePopup(this.el, rect) }

  destroy() {
    this.hide()
    this.el.remove()
  }
}

// 电脑上放在选中文字的下面（放不下就放上面），手机上从底部弹出
export function placePopup(el, rect, width = 380) {
  const sheet = innerWidth < 700 || !rect
  el.classList.toggle('sheet', sheet)
  if (sheet) { el.style.left = el.style.top = el.style.bottom = el.style.width = el.style.maxHeight = ''; return }
  const w = Math.min(width, innerWidth - 24)
  el.style.width = w + 'px'
  el.style.left = Math.max(12, Math.min(innerWidth - w - 12, (rect.left + rect.right) / 2 - w / 2)) + 'px'
  // 选中的文字已经不在屏幕上（比如滚走了）：也放在屏幕里
  const top = Math.min(Math.max(rect.top, 8), innerHeight - 8), bottom = Math.min(Math.max(rect.bottom, top), innerHeight - 8)
  const below = innerHeight - bottom - 16, above = top - 16
  if (below >= 240 || below >= above) {
    el.style.top = Math.min(bottom + 8, innerHeight - 168) + 'px'; el.style.bottom = ''; el.style.maxHeight = Math.max(160, below) + 'px'
  } else {
    el.style.top = ''; el.style.bottom = Math.min(innerHeight - top + 8, innerHeight - 168) + 'px'; el.style.maxHeight = Math.max(160, above) + 'px'
  }
}

// 复制文字：http 地址没有剪贴板接口，就放进一个临时的输入框里复制
export async function copyText(sel) {
  const text = sel?.text || ''
  try {
    if (navigator.clipboard && window.isSecureContext) { await navigator.clipboard.writeText(text); toast('已复制', { duration: 1200 }); return }
  } catch {}
  try {
    const ta = h('textarea', { readonly: true, style: { position: 'fixed', left: '-9999px', top: '0', opacity: '0' } })
    ta.value = text
    document.body.append(ta)
    ta.select()
    const ok = document.execCommand('copy')
    ta.remove()
    if (ok) { toast('已复制', { duration: 1200 }); return }
  } catch {}
  toast('复制失败，请用系统的复制', { error: true })
}

// 选中文字所在的那段话，截取前后各 n 个字给 AI 当上下文
export function around(block, text, n = 160) {
  const s = (block || '').replace(/\s+/g, ' ').trim()
  const i = s.indexOf(text)
  if (i < 0) return s.slice(0, n * 2)
  return s.slice(Math.max(0, i - n), i + text.length + n)
}
