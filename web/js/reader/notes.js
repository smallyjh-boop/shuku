// 划线和笔记：选中文字后划线（几种颜色）、写笔记；点书里的划线可以改颜色、改笔记、删除；
// 目录面板里的“笔记”列表（点了跳过去），导出成 Markdown。
// 和书签存在同一张表里：kind = highlight，location 是位置（EPUB 是 CFI，PDF 是 p:页:位置:起-止）

import { h, fill, icon, iconBtn, toast, toastError } from '../ui.js'
import { api, apiUrl } from '../api.js'
import { placePopup, copyText } from './dict.js'
import { swipeDown } from './reader.js'

export const COLORS = [['yellow', '#F2C94C', '黄'], ['green', '#6FCF97', '绿'], ['blue', '#56CCF2', '蓝'], ['pink', '#F28DB2', '粉']]
export const colorOf = c => (COLORS.find(x => x[0] === c) || COLORS[0])[1]

export class Notes {
  constructor(shell) {
    this.shell = shell
    this.list = []
    this.el = h('div', { class: 'r-note', role: 'dialog', 'aria-label': '笔记' })
    for (const ev of ['click', 'pointerdown', 'wheel']) this.el.addEventListener(ev, e => e.stopPropagation())
    shell.root.append(this.el)
    swipeDown(this.el, () => this.hide())
  }

  get open() { return this.el.classList.contains('show') }
  hide() {
    if (!this.open) return
    this.el.classList.remove('show')
    this.saveEditing?.()
    this.saveEditing = null
  }

  setAll(list) { this.list = list.slice() }
  byLocation(loc) { return this.list.find(b => b.location === loc) }

  // 划线（withNote：接着写笔记）
  async add(sel, color, withNote) {
    const sh = this.shell, eng = sh.engine
    const loc = eng?.annotationLocation?.(sel)
    if (!loc) { toast('这里不能划线'); return }
    try {
      const b = await api(`/api/books/${sh.id}/bookmarks`, { method: 'POST', body: { kind: 'highlight', location: loc, text: sel.text, color, note: '', label: sh.chapter || '' } })
      this.list.push(b)
      eng.drawAnnotation?.(b)
      if (sh.ptab === 'notes') sh.drawPanel()
      if (withNote) this.edit(b, sel.rect, true)
      else toast('已划线', { duration: 1200 })
    } catch (e) {
      toastError(e.status === 0 ? new Error('没连上书库，暂时不能划线') : e)
    }
  }

  async update(b, patch) {
    try {
      const nb = await api('/api/bookmarks/' + b.id, { method: 'PATCH', body: patch })
      Object.assign(b, nb)
      this.shell.engine?.drawAnnotation?.(b)
      if (this.shell.ptab === 'notes') this.shell.drawPanel()
    } catch (e) { toastError(e) }
  }

  async remove(b) {
    try {
      await api('/api/bookmarks/' + b.id, { method: 'DELETE' })
      this.list = this.list.filter(x => x !== b)
      this.shell.engine?.removeAnnotation?.(b)
      if (this.shell.ptab === 'notes') this.shell.drawPanel()
      toast('已删除划线', { duration: 1200 })
    } catch (e) { toastError(e) }
  }

  // 点了书里的划线：改颜色、写笔记、删除
  edit(b, rect, focus) {
    this.shell.dict?.hide()
    const ta = h('textarea', { class: 'input', rows: 3, placeholder: '写点笔记…', 'aria-label': '笔记' })
    ta.value = b.note || ''
    let saved = b.note || ''
    const save = () => {
      const v = ta.value.trim()
      if (v === saved) return
      saved = v
      this.update(b, { note: v })
    }
    this.saveEditing = save
    const dots = h('div', { class: 'seltools' },
      COLORS.map(([c, css, name]) => h('button', { class: 'dot' + (b.color === c ? ' on' : ''), style: { '--c': css }, title: name, 'aria-label': '换成' + name + '色', onclick: () => { b.color = c; this.update(b, { color: c }); dots.querySelectorAll('.dot').forEach(d => d.classList.toggle('on', d.title === name)) } })),
      h('span', { class: 'grow' }),
      iconBtn('trash', '删除划线', () => { this.saveEditing = null; this.hide(); this.remove(b) }),
      iconBtn('close', '关闭', () => this.hide()))
    fill(this.el,
      dots,
      h('blockquote', { class: 'q', style: { '--c': colorOf(b.color) } }, b.text || ''),
      ta,
      h('div', { class: 'row' },
        h('button', { class: 'btn small quiet', onclick: () => copyText({ text: b.text + (ta.value.trim() ? '\n\n' + ta.value.trim() : '') }) }, '复制'),
        h('span', { class: 'grow' }),
        h('button', { class: 'btn small primary', onclick: () => { save(); this.saveEditing = null; this.hide() } }, '完成')))
    ta.addEventListener('blur', save)
    ta.addEventListener('keydown', e => { e.stopPropagation(); if (e.key === 'Escape') this.hide() })
    this.el.classList.add('show')
    placePopup(this.el, rect, 360)
    if (focus) setTimeout(() => ta.focus(), 60)
  }

  // 目录面板里的“笔记”
  drawTab(box) {
    const list = this.list.slice().sort((a, b) => (this.shell.engine?.compareLocations?.(a.location, b.location) ?? 0) || a.created_at - b.created_at)
    const exp = h('a', { class: 'btn small', href: apiUrl(`/api/books/${this.shell.id}/notes.md`), download: '', title: '导出成 Markdown 文件' }, icon('download'), '导出')
    box.append(h('div', { class: 'notes-hd' }, h('span', null, list.length ? `${list.length} 条划线` : ''), list.length ? exp : null))
    if (!list.length) {
      box.append(h('p', { class: 'muted', style: { padding: '8px 18px' } }, '还没有划线。选中文字后点颜色就能划线，点“笔记”可以写下想法。'))
      return
    }
    for (const b of list) {
      box.append(h('div', { class: 'r-note-item', tabindex: 0, role: 'button', onclick: () => this.go(b), onkeydown: e => { if (e.key === 'Enter') this.go(b) } },
        h('div', { class: 'q', style: { '--c': colorOf(b.color) } }, b.text || ''),
        b.note ? h('div', { class: 'n' }, b.note) : null,
        h('div', { class: 'm' }, h('span', null, b.label || ''), h('span', { class: 'grow' }),
          iconBtn('edit', '写笔记', e => { e.stopPropagation(); this.go(b, true) }),
          iconBtn('trash', '删除划线', e => { e.stopPropagation(); this.remove(b) }))))
    }
  }

  async go(b, edit) {
    const sh = this.shell
    sh.markJump()
    sh.closeOverlays(); sh.showUI(false)
    await sh.engine.goTo(b.location)
    if (edit) setTimeout(() => this.edit(b, null, true), 350)
  }

  destroy() { this.saveEditing = null; this.el.remove() }
}
