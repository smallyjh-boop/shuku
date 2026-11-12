// 阅读器侧栏的“AI 助手”：前情提要、问这本书、整理划线（服务器 server/airead.go，用设置里的 AI 接口）
// 不剧透：前情提要、问答默认只用已经读到的部分（把当前位置带给书库）

import { h, fill, icon, toast, toastError, downloadLink } from '../ui.js'
import { api } from '../api.js'

// 简单的 Markdown 显示（只认标题、引用、列表、粗体），不用 innerHTML
function mdBlock(md) {
  const box = h('div', { class: 'ai-md' })
  let list = null
  for (const raw of String(md || '').split('\n')) {
    const line = raw.trimEnd()
    const inline = t => {
      const out = []
      String(t).split(/(\*\*[^*]+\*\*)/).forEach(p => out.push(/^\*\*.+\*\*$/.test(p) ? h('b', null, p.slice(2, -2)) : p))
      return out
    }
    if (/^\s*([-*•]|\d+[.、])\s+/.test(line)) {
      if (!list) { list = h('ul'); box.append(list) }
      list.append(h('li', null, inline(line.replace(/^\s*([-*•]|\d+[.、])\s+/, ''))))
      continue
    }
    list = null
    if (!line.trim()) continue
    if (/^#{1,6}\s/.test(line)) box.append(h('h5', null, line.replace(/^#+\s*/, '')))
    else if (/^>\s?/.test(line)) box.append(h('blockquote', null, inline(line.replace(/^>\s?/, ''))))
    else box.append(h('p', null, inline(line)))
  }
  return box
}

const btnStyle = { flex: 1, background: 'transparent', borderColor: 'var(--r-rule)', color: 'inherit' }

export class AIPanel {
  constructor(shell) {
    this.shell = shell
    this.recap = null   // { text, chapter }
    this.answer = null  // { q, answer, sources }
    this.notes = null   // markdown
    this.busy = {}
  }

  get posQuery() {
    const l = this.shell.loc
    return l ? `?loc=${encodeURIComponent(String(l.location ?? ''))}&pct=${+(l.fraction || 0)}` : ''
  }

  redraw() { if (this.box?.isConnected) this.draw(this.box) }

  draw(box) {
    this.box = box
    const parts = []
    if (this.shell.ctx.aiReady === false) {
      parts.push(h('p', { class: 'muted ai-note' }, 'AI 还没配置：管理员在“设置 → 识别与整理”里填好 AI 接口就能用。'))
    }
    // 前情提要
    parts.push(h('h4', null, '前情提要'),
      h('p', { class: 'muted ai-note' }, '隔了几天再读，帮你想起前面讲了什么、上次读到哪。只总结已经读过的，不剧透。'))
    if (this.busy.recap) parts.push(h('p', { class: 'muted' }, '正在整理前面的内容…（第一次要读几章，稍等十几秒）'))
    else if (this.recap) parts.push(mdBlock(this.recap.text))
    parts.push(h('div', { class: 'row', style: { gap: '8px', margin: '8px 0 4px' } },
      h('button', { class: 'btn small', type: 'button', style: btnStyle, disabled: !!this.busy.recap, onclick: () => this.loadRecap(!!this.recap) }, icon('sparkle'), this.recap ? '重新生成' : '生成前情提要')))

    // 问这本书
    const q = h('textarea', { class: 'input ai-q', rows: 2, placeholder: '比如：某某是谁？这件事前面是怎么说的？', 'aria-label': '问这本书' })
    if (this.answer) q.value = this.answer.q
    const all = h('input', { type: 'checkbox' })
    const ask = () => this.ask(q.value, all.checked)
    q.addEventListener('keydown', e => { if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); ask() } })
    parts.push(h('h4', null, '问这本书'),
      h('p', { class: 'muted ai-note' }, '在书里找相关的段落来回答，点编号跳到原文。'),
      q,
      h('label', { class: 'ai-check' }, all, ' 也看还没读到的部分（会剧透）'),
      h('div', { class: 'row', style: { gap: '8px', margin: '6px 0 4px' } },
        h('button', { class: 'btn small', type: 'button', style: btnStyle, disabled: !!this.busy.ask, onclick: ask }, icon('search'), this.busy.ask ? '正在找…' : '问')))
    if (this.answer && !this.busy.ask) parts.push(this.answerBlock())

    // 整理划线
    parts.push(h('h4', null, '整理划线'),
      h('p', { class: 'muted ai-note' }, '把这本书里的划线和批注整理成一份读书笔记（要点、按章节、你的想法），可以复制、下载。'))
    if (this.busy.notes) parts.push(h('p', { class: 'muted' }, '正在整理…'))
    else if (this.notes) {
      parts.push(mdBlock(this.notes), h('div', { class: 'row', style: { gap: '8px', margin: '6px 0' } },
        h('button', { class: 'btn small', type: 'button', style: btnStyle, onclick: () => this.copyNotes() }, '复制'),
        h('button', { class: 'btn small', type: 'button', style: btnStyle, onclick: () => this.downloadNotes() }, icon('download'), '下载 .md')))
    }
    parts.push(h('div', { class: 'row', style: { gap: '8px', margin: '6px 0 12px' } },
      h('button', { class: 'btn small', type: 'button', style: btnStyle, disabled: !!this.busy.notes, onclick: () => this.loadNotes(!!this.notes) }, icon('notes'), this.notes ? '重新整理' : '整理我的划线')))
    fill(box, h('div', { class: 'ai-panel' }, ...parts))
  }

  answerBlock() {
    const a = this.answer
    const srcByN = Object.fromEntries((a.sources || []).map(s => [s.n, s]))
    // 回答里的 [1] [2] 变成能点的小标签
    const para = h('div', { class: 'ai-answer' })
    for (const seg of String(a.answer).split(/(\[\d+\])/)) {
      const m = /^\[(\d+)\]$/.exec(seg)
      if (m && srcByN[m[1]]) para.append(h('button', { class: 'ai-ref', type: 'button', title: '跳到原文', onclick: () => this.jump(srcByN[m[1]]) }, m[1]))
      else para.append(seg)
    }
    return h('div', null, para, a.sources?.length ? h('div', { class: 'ai-srcs' }, a.sources.map(s => h('button', {
      class: 'ai-src', type: 'button', onclick: () => this.jump(s),
    }, h('b', null, `[${s.n}] ${s.chapter || ''}`), h('span', null, s.text)))) : null)
  }

  jump(s) {
    const sh = this.shell
    sh.markJump()
    if (s.loc) sh.engine?.goTo(s.loc)
    else sh.engine?.goToFraction(s.pct || 0)
    sh.closeOverlays(); sh.showUI(false)
  }

  async loadRecap(force) {
    this.busy.recap = true; this.redraw()
    try {
      const r = await api(`/api/books/${this.shell.id}/ai/recap${this.posQuery}${force ? '&force=1' : ''}`)
      this.recap = { text: r.recap, chapter: r.chapter }
    } catch (e) { toastError(e) }
    this.busy.recap = false; this.redraw()
  }

  async ask(question, all) {
    question = String(question || '').trim()
    if (!question) { toast('先输入要问的问题', { duration: 1500 }); return }
    this.busy.ask = true; this.answer = { q: question, answer: '', sources: [] }; this.redraw()
    try {
      const r = await api(`/api/books/${this.shell.id}/ai/ask${this.posQuery}`, { method: 'POST', body: { question, all } })
      this.answer = { q: question, answer: r.answer, sources: r.sources || [] }
    } catch (e) { toastError(e); this.answer = null }
    this.busy.ask = false; this.redraw()
  }

  async loadNotes(force) {
    this.busy.notes = true; this.redraw()
    try {
      const r = await api(`/api/books/${this.shell.id}/ai/notes${force ? '?force=1' : ''}`, { method: 'POST' })
      this.notes = r.markdown
    } catch (e) { toastError(e) }
    this.busy.notes = false; this.redraw()
  }

  notesText() { return `# 《${this.shell.info?.title || ''}》读书笔记\n\n${this.notes || ''}\n` }

  async copyNotes() {
    try { await navigator.clipboard.writeText(this.notesText()); toast('已复制', { duration: 1200 }) }
    catch { toast('复制不了，请用“下载 .md”', { error: true }) }
  }

  downloadNotes() {
    const url = URL.createObjectURL(new Blob([this.notesText()], { type: 'text/markdown;charset=utf-8' }))
    downloadLink(url, `${this.shell.info?.title || '读书笔记'}-读书笔记.md`)
    setTimeout(() => URL.revokeObjectURL(url), 10000)
  }
}
