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
      const t = h('input', { class: 'input', value: b.title, 'aria-label': '书名' })
      const a = h('input', { class: 'input', value: b.author || '', placeholder: '作者', 'aria-label': '作者' })
      const c = h('select', { class: 'input', 'aria-label': '分类' }, [...new Set([...cats, b.category].filter(Boolean))].map(x => h('option', { value: x, selected: x === b.category }, x)))
      const conf = h('div', { class: 'conf' }, `把握 ${pct(b.confidence)}`)
      const was = h('div', { class: 'was' })
      const okBtn = h('button', { class: 'btn primary small', type: 'button' }, icon('check'), '确认并整理')
      const item = h('div', { class: 'item review' },
        h('div', { style: { cursor: 'pointer' }, onclick: () => ctx.openBook(b.id) }, coverEl(b, apiUrl, { flags: false })),
        h('div', null, conf, was,
          h('div', { class: 'form-grid' }, t, a, c),
          h('div', { class: 'acts' },
            okBtn,
            h('button', { class: 'btn small', type: 'button', onclick: () => ctx.openBook(b.id) }, '详情'),
            h('a', { class: 'btn small quiet', href: '#/read/' + b.id }, '打开看看'))))
      // 正在提交时不能再点（连点、和“全部确认”同时进行都只提交一次）
      let busy = false
      const confirm1 = async (silent) => {
        if (busy || !item.isConnected) return false
        busy = true; okBtn.disabled = true
        try {
          await api('/api/books/' + b.id, { method: 'PATCH', body: { title: t.value, author: a.value, category: c.value, confirm: true } })
          item.remove()
          if (!silent) { toast(`《${t.value}》已确认并整理`); ctx.refreshFacets().then(drawTabs) }
          return true
        } catch (e) { toastError(e); return false } finally { busy = false; okBtn.disabled = false }
      }
      okBtn.onclick = () => confirm1()
      // 原文件名、识别来源：详情慢慢补上，不用等全部取完才显示
      const more = async () => {
        try {
          const full = await api('/api/books/' + b.id)
          conf.textContent = `${full.source || '规则识别'} · 把握 ${pct(full.confidence)}`
          was.textContent = '原文件名：' + (full.orig_name || '')
        } catch {}
      }
      rows.push({ confirm1, item, more })
      list.append(item)
    }
    // 同时最多取 4 本的详情
    ;(async () => {
      const queue = rows.slice()
      await Promise.all([1, 2, 3, 4].map(async () => { while (queue.length && body.contains(list)) await queue.shift().more() }))
    })()
    const allBtn = h('button', { class: 'btn small', type: 'button' }, '全部确认')
    let allRunning = false
    allBtn.onclick = async () => {
      if (allRunning) return
      const pending = rows.filter(x => x.item.isConnected)
      if (!pending.length) return
      if (!await confirmDialog({ title: `全部确认 ${pending.length} 本？`, message: '按当前填写的书名、作者和分类整理这些书。可以在操作记录里撤销。', ok: '全部确认' })) return
      allRunning = true; allBtn.disabled = true
      let n = 0
      // 一本一本来：文件要挨个移动，并发提交只会互相等
      for (const [i, x] of pending.entries()) {
        allBtn.textContent = `正在确认 ${i + 1} / ${pending.length}…`
        if (await x.confirm1(true)) n++
      }
      allRunning = false
      toast(`已确认 ${n} 本`); await ctx.refreshFacets(); show()
    }
    body.append(
      h('div', { class: 'row', style: { marginBottom: '14px' } }, h('span', { class: 'muted grow small-text' }, `${r.total} 本书等你核对。改好书名作者后确认，文件会按命名规则移到正式位置。`), allBtn),
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
        h('div', { class: 'acts' }, applyBtn, h('button', { class: 'btn small quiet', type: 'button', onclick: ignore }, '忽略这条')))
      applyBtn.onclick = async () => {
        applyBtn.disabled = true
        try {
          const r = await api('/api/suggestions/apply', { method: 'POST', body: { key: s.key } })
          item.remove()
          toast(r.message, r.batch ? { action: { label: '撤销', run: () => undo(r.batch) } } : {})
          ctx.refreshFacets().then(drawTabs)
        } catch (e) { toastError(e); applyBtn.disabled = false }
      }
      async function ignore(e) {
        const btn = e?.currentTarget
        if (btn) btn.disabled = true
        applyBtn.disabled = true
        try {
          await api('/api/suggestions/ignore', { method: 'POST', body: { key: s.key } })
          item.remove(); ctx.refreshFacets().then(drawTabs)
        } catch (err) { toastError(err); if (btn) btn.disabled = false; applyBtn.disabled = false }
      }
      box.append(item)
    }
    body.append(box)
  }

  const undoing = new Set()
  async function undo(batch, btn) {
    if (undoing.has(batch)) return
    undoing.add(batch)
    if (btn) btn.disabled = true
    try { const r = await api(`/api/ops/${batch}/undo`, { method: 'POST' }); toast(r.message); ctx.refreshFacets(); show() } catch (e) { toastError(e); if (btn) btn.disabled = false }
    undoing.delete(batch)
  }

  // ---------- 操作记录 ----------
  async function showOps() {
    const list = await api('/api/ops')
    clear(body)
    if (!list.length) { body.append(h('div', { class: 'empty' }, h('h3', null, '还没有移动过文件'), h('p', null, '入库整理、合并文件夹、改名等每一次文件移动都会记录在这里，可以整批撤销。'))); return }
    body.append(h('div', { class: 'list' }, list.map(o => h('div', { class: 'item' },
      h('div', { class: 'row' },
        h('div', { class: 'grow' }, h('h3', null, o.count ? `${o.label} · ${o.count} 个文件` : o.label), h('div', { class: 'd' }, `${fmtTime(o.created_at)}　${short(o.sample)}`)),
        o.undone ? h('span', { class: 'muted small-text' }, '已撤销') : h('button', { class: 'btn small', type: 'button', onclick: async e => {
          const btn = e.currentTarget
          if (await confirmDialog({ title: '撤销这次操作？', message: o.count ? `把这 ${o.count} 个文件移回原来的位置。` : '把这次改动改回去。', ok: '撤销' })) undo(o.batch, btn)
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
