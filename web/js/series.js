// 系列：所有系列一览（封面、本数、读完几本），点进去按序号看这个系列的书

import { h, fill, add, icon, debounce, store, save, genCover } from './ui.js'
import { api, apiUrl } from './api.js'

const SORTS = [['recent', '最近阅读'], ['name', '名称'], ['added', '最近添加']]
const KINDS = [['', '全部'], ['book', '图书'], ['comic', '漫画']]

export function seriesCover(s) {
  const el = h('div', { class: 'cv series-cv' })
  if (s.cover_id && s.cover) {
    const img = h('img', { src: apiUrl(`/api/books/${s.cover_id}/cover?${s.cover}&w=360`), alt: '', loading: 'lazy', decoding: 'async' })
    img.onerror = () => img.replaceWith(genCover(s.name, s.author))
    el.append(img)
  } else el.append(genCover(s.name, s.author))
  el.append(h('span', { class: 'count-badge' }, s.count + ' 本'))
  return el
}

// 书架上的系列卡片（书架“按系列折叠”时也用）
export function seriesCard(s, ctx) {
  const go = () => ctx.navigate('#/series/' + encodeURIComponent(s.name))
  const done = s.done ? (s.done >= s.count ? '全部读完' : `读完 ${s.done}`) : ''
  return h('div', {
    class: 'book series-card', role: 'link', tabindex: 0, 'aria-label': `系列：${s.name}，${s.count} 本${done ? '，' + done : ''}`,
    dataset: { series: s.name }, onclick: go, onkeydown: e => e.key === 'Enter' && go(),
  },
    h('div', { class: 'stackcv' }, seriesCover(s)),
    h('div', { class: 't' }, s.name),
    h('div', { class: 'a' }, [s.author, done].filter(Boolean).join(' · ') || `${s.count} 本`))
}

export function renderSeriesList(main, params, ctx) {
  const prefs = store('shuku.series', { sort: 'recent', kind: '' })
  if (params?.get('kind') != null) prefs.kind = params.get('kind')
  let q = ''
  let gen = 0
  const sub = h('div', { class: 'sub' })
  const search = h('input', { class: 'input', type: 'search', placeholder: '搜索系列名', 'aria-label': '搜索系列', enterkeyhint: 'search' })
  const grid = h('div', { class: 'grid' })
  const empty = h('div')
  const sortSel = h('select', { class: 'input', 'aria-label': '排序' }, SORTS.map(([v, l]) => h('option', { value: v, selected: prefs.sort === v }, l)))
  const kindChips = KINDS.map(([v, l]) => h('button', {
    class: 'chip' + (prefs.kind === v ? ' on' : ''), type: 'button', 'aria-pressed': String(prefs.kind === v),
    onclick: () => { prefs.kind = v; kindChips.forEach((c, i) => { c.classList.toggle('on', KINDS[i][0] === v); c.setAttribute('aria-pressed', String(KINDS[i][0] === v)) }); persist(); load() },
  }, l))
  const persist = () => save('shuku.series', prefs)
  sortSel.onchange = () => { prefs.sort = sortSel.value; persist(); load() }
  add(main,
    h('div', { class: 'page-head' }, h('div', null, h('h1', null, '系列'), sub), h('label', { class: 'search' }, icon('search'), search)),
    h('div', { class: 'filters' }, kindChips, sortSel),
    grid, empty)

  async function load() {
    const my = ++gen
    const p = new URLSearchParams({ sort: prefs.sort })
    if (prefs.kind) p.set('kind', prefs.kind)
    if (q) p.set('q', q)
    try {
      const list = await api('/api/series?' + p)
      if (my !== gen) return
      fill(grid, list.map(s => seriesCard(s, ctx)))
      sub.textContent = `${list.length} 个系列`
      fill(empty, list.length ? null : h('div', { class: 'empty' }, h('h3', null, q ? '没有找到' : '还没有系列'),
        h('p', null, q ? `没有名字里带“${q}”的系列。` : '识别出系列（比如“三体”第 1、2、3 本、漫画的各卷）的书会归到这里。可以在书的详情里填系列，或者在书架上多选几本一起设成一个系列。')))
    } catch (e) { if (my === gen) fill(empty, h('p', { class: 'muted' }, e.message)) }
  }
  search.addEventListener('input', debounce(() => { q = search.value.trim(); load() }, 300))
  load()
  return { refresh: load }
}
