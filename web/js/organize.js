// 整理：待确认、整理建议（合并文件夹等）、操作记录（可撤销）、入库动态

import { h, clear, fill, add, icon, coverEl, toast, toastError, confirmDialog, fmtTime, pct } from './ui.js'
import { api, apiUrl } from './api.js'

const TABS = [['review', '待确认'], ['suggest', '整理建议'], ['ops', '操作记录'], ['events', '入库动态']]

export function renderOrganize(main, params, ctx) {
  let tab = params.get('tab') || ((ctx.facets?.review || 0) === 0 && (ctx.facets?.suggestions || 0) > 0 ? 'suggest' : 'review')
  const tabsEl = h('div', { class: 'tabs', role: 'tablist' })
  const body = h('div')
  const scanBtn = h('button', { class: 'btn', onclick: scan }, icon('refresh'), '扫描书库')
  main.append(
    h('div', { class: 'page-head' },
      h('div', null, h('h1', null, '整理'), h('div', { class: 'sub' }, '入库时把握不大的书、需要合并的文件夹、所有移动记录都在这里')),
      h('div', { class: 'actions' }, scanBtn)),
    tabsEl, body)

  async function scan() {
    scanBtn.disabled = true
    try { await api('/api/scan', { method: 'POST' }); toast('已开始扫描书库，新文件会自动登记') } catch (e) { toastError(e) }
    setTimeout(() => (scanBtn.disabled = false), 3000)
  }

  function drawTabs() {
    const f = ctx.facets || {}
    const counts = { review: f.review, suggest: f.suggestions }
    fill(tabsEl, ...TABS.map(([k, l]) => h('button', { class: tab === k ? 'on' : '', role: 'tab', 'aria-selected': tab === k, onclick: () => { tab = k; history.replaceState(null, '', '#/organize?tab=' + k); show() } },
      l, counts[k] ? h('span', { class: 'badge warn' }, counts[k]) : null)))
  }

  async function show() {
    drawTabs()
    fill(body, h('p', { class: 'muted' }, '加载中…'))
    try {
      if (tab === 'review') await showReview()
      else if (tab === 'suggest') await showSuggest()
      else if (tab === 'ops') await showOps()
      else await showEvents()
    } catch (e) { fill(body, h('p', { class: 'muted' }, e.message)) }
  }

  // ---------- 待确认 ----------
  async function showReview() {
    const r = await api('/api/books?status=review&sort=added&limit=200')
    clear(body)
    if (!r.items.length) {
      body.append(h('div', { class: 'empty' }, h('h3', null, '没有待确认的书'), h('p', null, '识别把握不大的书会出现在这里。在“设置 → 识别与整理”里配置 AI，可以大幅减少需要手动确认的书。')))
      return
    }
    const cats = ctx.settings?.categories || []
    const list = h('div', { class: 'list' })
    const rows = []
    for (const b of r.items) {
      const full = await api('/api/books/' + b.id).catch(() => b)
      const t = h('input', { class: 'input', value: full.title, 'aria-label': '书名' })
      const a = h('input', { class: 'input', value: full.author || '', placeholder: '作者', 'aria-label': '作者' })
      const c = h('select', { class: 'input', 'aria-label': '分类' }, [...new Set([...cats, full.category].filter(Boolean))].map(x => h('option', { value: x, selected: x === full.category }, x)))
      const item = h('div', { class: 'item review' },
        h('div', { style: { cursor: 'pointer' }, onclick: () => ctx.openBook(b.id) }, coverEl(b, apiUrl, { flags: false })),
        h('div', null,
          h('div', { class: 'conf' }, `${full.source || '规则识别'} · 把握 ${pct(full.confidence)}`),
          h('div', { class: 'was' }, '原文件名：' + (full.orig_name || '')),
          h('div', { class: 'form-grid' }, t, a, c),
          h('div', { class: 'acts' },
            h('button', { class: 'btn primary small', onclick: () => confirm1() }, icon('check'), '确认并整理'),
            h('button', { class: 'btn small', onclick: () => ctx.openBook(b.id) }, '详情'),
            h('a', { class: 'btn small quiet', href: '#/read/' + b.id }, '打开看看'))))
      const confirm1 = async (silent) => {
        try {
          await api('/api/books/' + b.id, { method: 'PATCH', body: { title: t.value, author: a.value, category: c.value, confirm: true } })
          item.remove()
          if (!silent) { toast(`《${t.value}》已确认并整理`); ctx.refreshFacets().then(drawTabs) }
          return true
        } catch (e) { toastError(e); return false }
      }
      rows.push(confirm1)
      list.append(item)
    }
    body.append(
      h('div', { class: 'row', style: { marginBottom: '14px' } }, h('span', { class: 'muted grow small-text' }, `${r.total} 本书等你核对。改好书名作者后确认，文件会按命名规则移到正式位置。`),
        h('button', { class: 'btn small', onclick: async () => {
          if (!await confirmDialog({ title: `全部确认 ${rows.length} 本？`, message: '按当前填写的书名、作者和分类整理这些书。可以在操作记录里撤销。', ok: '全部确认' })) return
          let n = 0
          for (const fn of rows) if (await fn(true)) n++
          toast(`已确认 ${n} 本`); await ctx.refreshFacets(); show()
        } }, '全部确认')),
      list)
  }

  // ---------- 整理建议 ----------
  async function showSuggest() {
    const list = await api('/api/suggestions?fresh=1')
    clear(body)
    if (!list.length) {
      body.append(h('div', { class: 'empty' }, h('h3', null, '书库很整齐'), h('p', null, '没有需要合并的文件夹、重复文件或命名不规范的书。书库每次变动后会自动重新检查。')))
      return
    }
    const box = h('div', { class: 'list' })
    for (const s of list) {
      const moves = h('ul', { class: 'moves' }, s.items.map(it => it.to
        ? h('li', null, h('span', null, short(it.from)), h('i', null, '→'), h('span', null, short(it.to)))
        : h('li', { class: 'single' }, h('span', null, short(it.from)))))
      const applyBtn = h('button', { class: 'btn primary small' }, actionLabel(s.type))
      const item = h('div', { class: 'item' },
        h('h3', null, s.title), h('div', { class: 'd' }, s.detail), moves,
        h('div', { class: 'acts' }, applyBtn, h('button', { class: 'btn small quiet', onclick: ignore }, '忽略这条')))
      applyBtn.onclick = async () => {
        applyBtn.disabled = true
        try {
          const r = await api('/api/suggestions/apply', { method: 'POST', body: { key: s.key } })
          item.remove()
          toast(r.message, r.batch ? { action: { label: '撤销', run: () => undo(r.batch) } } : {})
          ctx.refreshFacets().then(drawTabs)
        } catch (e) { toastError(e); applyBtn.disabled = false }
      }
      async function ignore() {
        await api('/api/suggestions/ignore', { method: 'POST', body: { key: s.key } }).catch(toastError)
        item.remove(); ctx.refreshFacets().then(drawTabs)
      }
      box.append(item)
    }
    body.append(box)
  }

  async function undo(batch) {
    try { const r = await api(`/api/ops/${batch}/undo`, { method: 'POST' }); toast(r.message); ctx.refreshFacets(); show() } catch (e) { toastError(e) }
  }

  // ---------- 操作记录 ----------
  async function showOps() {
    const list = await api('/api/ops')
    clear(body)
    if (!list.length) { body.append(h('div', { class: 'empty' }, h('h3', null, '还没有移动过文件'), h('p', null, '入库整理、合并文件夹、改名等每一次文件移动都会记录在这里，可以整批撤销。'))); return }
    body.append(h('div', { class: 'list' }, list.map(o => h('div', { class: 'item' },
      h('div', { class: 'row' },
        h('div', { class: 'grow' }, h('h3', null, `${o.label} · ${o.count} 个文件`), h('div', { class: 'd' }, `${fmtTime(o.created_at)}　${short(o.sample)}`)),
        o.undone ? h('span', { class: 'muted small-text' }, '已撤销') : h('button', { class: 'btn small', onclick: async () => {
          if (await confirmDialog({ title: '撤销这次操作？', message: `把这 ${o.count} 个文件移回原来的位置。`, ok: '撤销' })) undo(o.batch)
        } }, icon('undo'), '撤销'))))))
  }

  // ---------- 入库动态 ----------
  async function showEvents() {
    const list = await api('/api/events')
    clear(body)
    if (!list.length) { body.append(h('div', { class: 'empty' }, h('h3', null, '暂无动态'), h('p', null, '上传或扫描到新书后，处理结果会显示在这里。'))); return }
    body.append(h('ul', { class: 'events' }, list.map(e => h('li', { class: e.level },
      h('time', null, fmtTime(e.created_at)),
      h('span', { style: e.book_id ? { cursor: 'pointer' } : null, onclick: e.book_id ? () => ctx.openBook(e.book_id) : null }, e.message)))))
  }

  show()
  return { refresh: () => show() }
}

function actionLabel(type) {
  return { folder: '合并文件夹', author: '统一作者名', series: '归到一起', samebook: '合并成一本', duplicate: '移走重复文件', standard: '按规范整理', empty: '清理空文件夹' }[type] || '执行'
}

const short = p => (p || '').replace(/^\/.*?\/(?=[^/]+\/[^/]+\/[^/]+$)/, '…/')
