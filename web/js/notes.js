// 笔记：我在所有书里的划线、笔记和书签，按书分组；能搜索、改笔记、删除，点一条回到书里那个位置；导出 Markdown

import { h, fill, add, icon, debounce, toast, toastError, confirmDialog, fmtTime } from './ui.js'
import { api, apiUrl } from './api.js'

const KINDS = [['', '全部'], ['highlight', '划线笔记'], ['bookmark', '书签']]
const COLORS = { yellow: '#E2B93B', green: '#6FAE5F', blue: '#5B9BD5', red: '#D9646F', pink: '#D97BA6', purple: '#9C82CF', orange: '#E0904A', underline: 'var(--accent)' }
const colorOf = c => !c ? COLORS.yellow : c.startsWith('#') ? c : COLORS[c] || COLORS.yellow

export async function renderNotes(main, params, ctx) {
  const onlyBook = +params.get('book') || 0
  let kind = params.get('kind') || ''
  let q = ''
  let all = []
  const sub = h('div', { class: 'sub' })
  const search = h('input', { class: 'input', type: 'search', placeholder: '搜索划线的原文、笔记、书名', 'aria-label': '搜索笔记', enterkeyhint: 'search' })
  const exportAll = h('a', { class: 'btn', href: apiUrl('/api/notes.md'), download: '书库笔记.md', title: '所有书的划线和笔记，导出成一个 Markdown 文件' }, icon('download'), h('span', null, '导出全部'))
  const kindChips = KINDS.map(([v, l]) => h('button', {
    class: 'chip' + (kind === v ? ' on' : ''), type: 'button', 'aria-pressed': String(kind === v),
    onclick: () => { kind = v; kindChips.forEach((c, i) => { c.classList.toggle('on', KINDS[i][0] === v); c.setAttribute('aria-pressed', String(KINDS[i][0] === v)) }); draw() },
  }, l))
  const bookChip = h('span')
  const list = h('div', { class: 'notes' })
  add(main,
    h('div', { class: 'page-head' }, h('div', null, h('h1', null, '笔记'), sub), h('label', { class: 'search' }, icon('search'), search), h('div', { class: 'actions' }, exportAll)),
    h('div', { class: 'filters' }, bookChip, kindChips),
    list)

  try { all = await api('/api/notes') } catch (e) { fill(list, h('p', { class: 'muted' }, e.message)); return }

  function draw() {
    const term = q.toLowerCase()
    const items = all.filter(n => (!onlyBook || n.book_id === onlyBook) && (!kind || n.kind === kind) &&
      (!term || [n.text, n.note, n.label, n.title, n.author].some(x => (x || '').toLowerCase().includes(term))))
    const books = new Map()
    for (const n of items) {
      if (!books.has(n.book_id)) books.set(n.book_id, { id: n.book_id, title: n.title, author: n.author, items: [] })
      books.get(n.book_id).items.push(n)
    }
    const hl = items.filter(n => n.kind === 'highlight').length
    sub.textContent = items.length ? `${hl} 条划线笔记 · ${items.length - hl} 个书签 · ${books.size} 本书` : ''
    const one = onlyBook ? all.find(n => n.book_id === onlyBook) : null
    fill(bookChip, onlyBook ? h('a', { class: 'chip on ic', href: '#/notes', title: '看所有书的笔记' }, icon('book'), one ? `《${one.title}》` : '这本书', icon('close')) : null)
    if (!items.length) {
      fill(list, h('div', { class: 'empty' },
        h('h3', null, all.length ? '没有找到' : '还没有笔记'),
        h('p', null, all.length ? '换个关键词试试。' : '读书时选中一段文字，可以划线、写笔记；按 B 或点书签图标可以加书签。电脑、手机上记的都会汇总到这里。')))
      return
    }
    fill(list, [...books.values()].map(bk => h('section', { class: 'note-book' },
      h('header', null,
        h('div', { class: 'grow' }, h('h2', null, bk.title), h('small', null, [bk.author, `${bk.items.length} 条`].filter(Boolean).join(' · '))),
        h('a', { class: 'btn small quiet', href: '#/read/' + bk.id }, icon('reading'), '打开'),
        h('a', { class: 'btn small quiet', href: apiUrl(`/api/books/${bk.id}/notes.md`), download: `${bk.title} 笔记.md`, title: '导出这本书的笔记' }, icon('download'), '导出')),
      bk.items.map(noteItem))))
  }

  function noteItem(n) {
    const at = `#/read/${n.book_id}?at=${encodeURIComponent(n.location)}`
    const noteText = h('div', { class: 'n-note' }, n.note || '')
    noteText.hidden = !n.note
    const el = h('article', { class: 'note-item ' + n.kind, style: { '--hl': colorOf(n.color) } },
      h('a', { class: 'n-body', href: at, title: '回到书里这个位置' },
        n.label ? h('div', { class: 'n-label' }, n.label) : null,
        n.kind === 'highlight' && n.text ? h('blockquote', null, n.text) : n.kind === 'bookmark' ? h('div', { class: 'n-bm' }, icon('bookmark'), n.text || n.label || '书签') : null),
      noteText,
      h('div', { class: 'n-foot' },
        h('time', null, fmtTime(n.updated_at || n.created_at)),
        h('span', { class: 'grow' }),
        n.kind === 'highlight' ? h('button', { class: 'btn small quiet', type: 'button', onclick: () => edit() }, icon('edit'), n.note ? '改笔记' : '写笔记') : null,
        h('button', { class: 'icon-btn', type: 'button', title: '删除', 'aria-label': '删除这条' + (n.kind === 'highlight' ? '笔记' : '书签'), onclick: () => remove() }, icon('trash'))))
    function edit() {
      if (el.querySelector('textarea')) return
      const ta = h('textarea', { class: 'input', rows: 3, 'aria-label': '笔记' }, n.note || '')
      const box = h('div', { class: 'n-edit' }, ta, h('div', { class: 'row' },
        h('button', { class: 'btn small primary', type: 'button', onclick: async e => {
          e.currentTarget.disabled = true
          try {
            const r = await api('/api/bookmarks/' + n.id, { method: 'PATCH', body: { note: ta.value.trim() } })
            n.note = r.note; n.updated_at = r.updated_at
            noteText.textContent = n.note; noteText.hidden = !n.note
            box.remove(); toast('笔记已保存')
          } catch (err) { toastError(err); e.currentTarget.disabled = false }
        } }, '保存'),
        h('button', { class: 'btn small quiet', type: 'button', onclick: () => box.remove() }, '取消')))
      noteText.after(box)
      ta.focus()
    }
    async function remove() {
      if (!await confirmDialog({ title: n.kind === 'highlight' ? '删除这条划线笔记？' : '删除这个书签？', message: n.text ? `“${n.text.slice(0, 60)}${n.text.length > 60 ? '…' : ''}”` : null, ok: '删除', danger: true })) return
      try {
        await api('/api/bookmarks/' + n.id, { method: 'DELETE' })
        all = all.filter(x => x.id !== n.id)
        draw()
      } catch (e) { toastError(e) }
    }
    return el
  }

  search.addEventListener('input', debounce(() => { q = search.value.trim(); draw() }, 200))
  draw()
  return {
    async refresh() { try { all = await api('/api/notes'); draw() } catch {} },
  }
}
