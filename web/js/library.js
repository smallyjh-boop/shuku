import { h, clear, fill, add, icon, coverEl, debounce, store, save, pct, fmtTime } from './ui.js'
import { api, apiUrl } from './api.js'

// 把片段里的关键词标出来
function markSnippet(text, m) {
  const i = text.indexOf(m)
  if (!m || i < 0) return [text]
  return [text.slice(0, i), h('mark', null, m), text.slice(i + m.length)]
}

const SORTS = [['recent', '最近阅读'], ['added', '最近添加'], ['title', '书名'], ['author', '作者'], ['series', '系列']]
const STATES = [['', '全部'], ['unread', '未读'], ['reading', '在读'], ['done', '读完']]

export function renderLibrary(main, opts, ctx) {
  const prefKey = 'shuku.lib.' + opts.view + (opts.kind || '')
  const prefs = store(prefKey, { sort: opts.view === 'kind' && opts.kind === 'comic' ? 'series' : opts.view === 'series' ? 'series' : 'recent', format: '', state: '' })
  let q = ''
  let offset = 0, total = 0, loading = false, done = false, gen = 0

  const titles = {
    home: '书架', all: '全部藏书', reading: '在读', cat: opts.category,
    author: opts.author, series: opts.series,
    kind: opts.kind === 'comic' ? '漫画' : '图书',
  }
  const subEl = h('div', { class: 'sub' })
  const search = h('input', { class: 'input', type: 'search', placeholder: '搜索书名、作者、系列，支持拼音和首字母', 'aria-label': '搜索', enterkeyhint: 'search' })
  const head = h('div', { class: 'page-head' },
    h('div', null, h('h1', null, titles[opts.view]), subEl),
    h('label', { class: 'search' }, icon('search'), search, h('kbd', null, '/')),
    h('div', { class: 'actions' }, h('button', { class: 'btn', onclick: () => ctx.pickFiles() }, icon('upload'), h('span', null, '添加书'))))

  const continueWrap = h('div')
  const grid = h('div', { class: 'grid' })
  const totalEl = h('span', { class: 'total' })
  const sentinel = h('div', { class: 'loading-more' })
  const emptyEl = h('div')

  const sortSel = h('select', { class: 'input', 'aria-label': '排序' }, SORTS.map(([v, l]) => h('option', { value: v, selected: prefs.sort === v }, l)))
  const fmtSel = h('select', { class: 'input', 'aria-label': '格式' }, h('option', { value: '' }, '全部格式'),
    (ctx.facets?.formats || []).map(f => h('option', { value: f.name, selected: prefs.format === f.name }, `${f.name.toUpperCase()}（${f.count}）`)))
  const stateChips = STATES.map(([v, l]) => h('button', { class: 'chip' + (prefs.state === v ? ' on' : ''), type: 'button', onclick: () => { prefs.state = v; stateChips.forEach((c, i) => c.classList.toggle('on', STATES[i][0] === v)); persist(); reload() } }, l))
  const filters = h('div', { class: 'filters' }, opts.view === 'reading' ? null : stateChips, sortSel, fmtSel, totalEl)
  sortSel.onchange = () => { prefs.sort = sortSel.value; persist(); reload() }
  fmtSel.onchange = () => { prefs.format = fmtSel.value; persist(); reload() }
  const persist = () => save(prefKey, prefs)

  const gridTitle = opts.view === 'home' ? h('div', { class: 'section-title' }, h('h2', null, '全部藏书')) : null
  const ftWrap = h('div')
  if (opts.view === 'reading') head.querySelector('.actions').prepend(h('a', { class: 'btn quiet', href: '#/stats' }, icon('activity'), '阅读统计'))
  add(main, head, continueWrap, gridTitle, filters, grid, emptyEl, sentinel, ftWrap)

  function query(extra = {}) {
    const p = new URLSearchParams()
    if (q) p.set('q', q)
    if (opts.kind) p.set('kind', opts.kind)
    if (opts.category) p.set('category', opts.category)
    if (opts.author) p.set('author', opts.author)
    if (opts.series) p.set('series', opts.series)
    if (opts.view === 'reading') p.set('reading', '1')
    if (prefs.format) p.set('format', prefs.format)
    if (prefs.state && opts.view !== 'reading') p.set('state', prefs.state)
    p.set('sort', opts.view === 'reading' ? 'recent' : prefs.sort)
    for (const [k, v] of Object.entries(extra)) p.set(k, v)
    return p
  }

  async function loadMore() {
    if (loading || done) return
    loading = true
    const my = gen
    sentinel.textContent = '加载中…'
    try {
      const r = await api('/api/books?' + query({ offset, limit: 90 }))
      if (my !== gen) return
      total = r.total
      for (const b of r.items) grid.append(bookCard(b, ctx))
      offset += r.items.length
      done = offset >= total || r.items.length === 0
      totalEl.textContent = `${total} 本`
      subEl.textContent = opts.view === 'home' ? '' : `${total} 本`
      renderEmpty()
      if (total === 0 && ftWrap.childElementCount) fill(emptyEl)
    } catch (e) {
      sentinel.textContent = e.message
      loading = false
      return
    }
    sentinel.textContent = ''
    loading = false
    // 内容不够一屏时继续加载
    if (!done && sentinel.getBoundingClientRect().top < innerHeight + 400) loadMore()
  }

  function renderEmpty() {
    clear(emptyEl)
    if (total > 0) return
    if (q || prefs.format || prefs.state) {
      emptyEl.append(h('div', { class: 'empty' }, h('h3', null, '没有找到'), h('p', null, q ? `没有和“${q}”相关的书。试试作者名、拼音首字母，或者换个关键词。` : '这个筛选条件下没有书。')))
      return
    }
    if (opts.view === 'reading') {
      emptyEl.append(h('div', { class: 'empty' }, h('h3', null, '还没有在读的书'), h('p', null, '打开任意一本书，阅读进度会自动同步到这里，电脑、手机、手表都能接着读。')))
      return
    }
    emptyEl.append(h('div', { class: 'empty' },
      h('h3', null, '书架还是空的'),
      h('p', null, '把电子书或漫画拖进这个窗口，或者放进 NAS 上的投递文件夹。书库会自动识别书名、作者，按规范改名并归类。'),
      h('button', { class: 'btn primary', onclick: () => ctx.pickFiles() }, icon('upload'), '选择文件')))
  }

  function reload() {
    gen++
    offset = 0; total = 0; done = false; loading = false
    clear(grid)
    loadMore()
  }

  async function loadContinue() {
    clear(continueWrap)
    if (opts.view !== 'home' || q) { if (gridTitle) gridTitle.hidden = !!q; return }
    if (gridTitle) gridTitle.hidden = false
    try {
      const r = await api('/api/books?reading=1&sort=recent&limit=12')
      if (!r.items.length) return
      continueWrap.append(
        h('div', { class: 'section-title' }, h('h2', null, '继续阅读'), r.total > 12 ? h('a', { href: '#/reading' }, `全部 ${r.total} 本`) : null),
        h('div', { class: 'continue' }, r.items.map(b => continueCard(b))))
    } catch {}
  }

  function continueCard(b) {
    return h('div', { class: 'continue-item', role: 'button', tabindex: 0, onclick: () => ctx.navigate('#/read/' + b.id), onkeydown: e => e.key === 'Enter' && ctx.navigate('#/read/' + b.id) },
      coverEl(b, apiUrl, { flags: false }),
      h('div', { style: { minWidth: 0 } },
        h('div', { class: 't' }, b.title),
        h('div', { class: 'a' }, b.author || (b.kind === 'comic' ? '漫画' : '')),
        h('div', { class: 'p' }, h('div', { class: 'bar' }, h('i', { style: { width: pct(b.percent) } })), h('span', null, pct(b.percent))),
        h('div', { class: 'a', style: { marginTop: '6px', fontSize: '12px' } }, fmtTime(b.last_read))))
  }

  const io = new IntersectionObserver(es => { if (es.some(e => e.isIntersecting)) loadMore() }, { rootMargin: '600px' })
  io.observe(sentinel)

  // 书中内容：搜索词出现在哪些书的正文里
  let ftGen = 0
  async function loadFulltext() {
    const my = ++ftGen
    fill(ftWrap)
    if ([...q].length < 2) return
    try {
      const r = await api('/api/fulltext?q=' + encodeURIComponent(q) + '&limit=12')
      if (my !== ftGen || !r.books.length) return
      if (total === 0) fill(emptyEl) // 书名没搜到但正文里有，就不显示“没有找到”
      fill(ftWrap, h('div', { class: 'ft-sec' },
        h('div', { class: 'section-title' }, h('h2', null, '书中内容'), h('span', { class: 'muted' }, `${r.books.length} 本书的正文里提到“${q}”`)),
        r.books.map(b => h('div', { class: 'ft-book' },
          h('div', { style: { cursor: 'pointer' }, onclick: () => ctx.openBook(b.id) }, coverEl({ ...b, formats: '' }, apiUrl, { flags: false })),
          h('div', { style: { minWidth: 0 } },
            h('div', { class: 'bt' }, b.title, h('small', null, [b.author, b.total > b.hits.length ? `共 ${b.total} 处` : ''].filter(Boolean).join(' · '))),
            b.hits.map(x => h('a', { class: 'ft-hit', href: `#/read/${b.id}?file=${x.file_id}&sec=${x.sec}&q=${encodeURIComponent(x.match || q)}` },
              x.label ? h('b', null, x.label) : null, ...markSnippet(x.label && x.snippet.startsWith(x.label + ' ') ? x.snippet.slice(x.label.length + 1) : x.snippet, x.match || q))))))))
    } catch {}
  }

  search.addEventListener('input', debounce(() => { q = search.value.trim(); loadContinue(); reload(); loadFulltext() }, 300))
  const onKey = e => {
    if (e.key === '/' && document.activeElement?.tagName !== 'INPUT' && document.activeElement?.tagName !== 'TEXTAREA') { e.preventDefault(); search.focus() }
  }
  document.addEventListener('keydown', onKey)

  loadContinue()
  reload()

  return {
    refresh() { loadContinue(); reload() },
    destroy() { io.disconnect(); document.removeEventListener('keydown', onKey) },
  }
}

export function bookCard(b, ctx) {
  const open = () => b.status === 'review' ? ctx.openBook(b.id) : ctx.navigate('#/read/' + b.id)
  let pressTimer = 0, longPressed = false
  const el = h('div', {
    class: 'book', role: 'button', tabindex: 0, title: b.title + (b.author ? ' · ' + b.author : ''),
    onclick: e => { if (longPressed) { longPressed = false; return } open() },
    onkeydown: e => { if (e.key === 'Enter') open(); if (e.key === 'i') ctx.openBook(b.id) },
    oncontextmenu: e => { e.preventDefault(); ctx.openBook(b.id) },
    ontouchstart: () => { longPressed = false; pressTimer = setTimeout(() => { longPressed = true; ctx.openBook(b.id) }, 520) },
    ontouchend: () => clearTimeout(pressTimer),
    ontouchmove: () => clearTimeout(pressTimer),
  },
    withMore(coverEl(b, apiUrl), () => ctx.openBook(b.id)),
    b.percent > 0 && b.read_state !== 'done' ? h('div', { class: 'bar' }, h('i', { style: { width: pct(b.percent) } })) : null,
    h('div', { class: 't' }, b.title),
    h('div', { class: 'a', onclick: e => { e.stopPropagation(); ctx.openBook(b.id) } }, b.author || (b.series && b.kind === 'comic' ? b.series : '') || '详情'))
  return el
}

function withMore(cv, fn) {
  cv.append(h('button', { class: 'more', type: 'button', title: '详情与编辑', 'aria-label': '详情与编辑', onclick: e => { e.stopPropagation(); fn() } }, icon('edit')))
  return cv
}
