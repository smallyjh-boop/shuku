import { h, clear, fill, add, icon, coverEl, debounce, store, save, pct, fmtTime, toast, toastError, confirmDialog, formDialog, openMenu } from './ui.js'
import { api, apiUrl, net } from './api.js'
import { isNative, isDownloaded, onNative, call, refreshDownloads } from './native.js'
import { offlineSupported, isOfflineBook, offlineButton } from './offline.js'
import { seriesCard } from './series.js'

// App 里下载完成或删除后，更新封面上“已下载到手机”的标记
if (isNative) onNative('download', () => setTimeout(() => {
  for (const el of document.querySelectorAll('.book[data-id] .cv')) {
    const id = +el.closest('.book').dataset.id
    const has = el.querySelector('.dl')
    if (isDownloaded(id) && !has) el.append(h('span', { class: 'dl', title: '已下载到手机' }, icon('phone')))
    else if (!isDownloaded(id) && has) has.remove()
  }
}, 0))

// 把片段里的关键词标出来
function markSnippet(text, m) {
  const i = text.indexOf(m)
  if (!m || i < 0) return [text]
  return [text.slice(0, i), h('mark', null, m), text.slice(i + m.length)]
}

const SORTS = [['recent', '最近阅读'], ['added', '最近添加'], ['title', '书名'], ['author', '作者'], ['series', '系列'], ['rating', '我的评分']]
const STATES = [['', '全部'], ['unread', '未读'], ['reading', '在读'], ['done', '读完']]

// 离开书架时记下搜索词、滚到哪、加载了多少本；从阅读器、浏览器“返回”回来时照原样恢复
const viewState = new Map()

export function renderLibrary(main, opts, ctx) {
  const params = opts.params || new URLSearchParams()
  const stateKey = location.hash || '#/'
  const saved = opts.restore !== false ? viewState.get(stateKey) : null
  viewState.delete(stateKey)
  const prefKey = 'shuku.lib.' + opts.view + (opts.kind || '')
  const prefs = store(prefKey, { sort: opts.view === 'kind' && opts.kind === 'comic' ? 'series' : opts.view === 'series' ? 'series' : 'recent', format: '', state: '', want: false, fold: false })
  const tag = params.get('tag') || ''
  let missing = params.get('missing') === '1'
  let q = saved?.q || ''
  let offset = 0, total = 0, loading = false, done = false, gen = 0
  const canFold = !['series', 'reading'].includes(opts.view)
  let seriesMap = null, shownSeries = new Set()

  const titles = {
    home: '书架', all: '全部藏书', reading: '在读', want: '想读', cat: opts.category,
    author: opts.author, series: opts.series,
    kind: opts.kind === 'comic' ? '漫画' : '图书',
  }
  const subEl = h('div', { class: 'sub' })
  const search = h('input', { class: 'input', type: 'search', value: q, placeholder: '搜索书名、作者、系列，支持拼音和首字母', 'aria-label': '搜索', enterkeyhint: 'search' })
  const selBtn = h('button', { class: 'btn quiet', type: 'button', onclick: () => setSelMode(!selMode), title: '多选以后一起标记、整理' }, icon('select'), h('span', null, '选择'))
  const head = h('div', { class: 'page-head' },
    h('div', null,
      opts.view === 'series' ? h('a', { class: 'crumb', href: '#/series' }, icon('back'), '系列') : null,
      h('h1', null, titles[opts.view]), subEl),
    h('label', { class: 'search' }, icon('search'), search, h('kbd', null, '/')),
    h('div', { class: 'actions' },
      opts.view === 'reading' ? h('a', { class: 'btn quiet', href: '#/stats' }, icon('activity'), '阅读统计') : null,
      selBtn,
      ctx.isAdmin ? h('button', { class: 'btn', type: 'button', onclick: () => ctx.pickFiles() }, icon('upload'), h('span', null, '添加书')) : null))

  const continueWrap = h('div')
  const grid = h('div', { class: 'grid' })
  const totalEl = h('span', { class: 'total' })
  const sentinel = h('div', { class: 'loading-more' })
  const emptyEl = h('div')

  const sortSel = h('select', { class: 'input', 'aria-label': '排序' }, SORTS.map(([v, l]) => h('option', { value: v, selected: prefs.sort === v }, l)))
  const fmtSel = h('select', { class: 'input', 'aria-label': '格式' }, h('option', { value: '' }, '全部格式'),
    (ctx.facets?.formats || []).map(f => h('option', { value: f.name, selected: prefs.format === f.name }, `${f.name.toUpperCase()}（${f.count}）`)))
  const chip = (label, on, onclick, extra = {}) => h('button', { class: 'chip' + (on ? ' on' : ''), type: 'button', 'aria-pressed': String(!!on), onclick, ...extra }, label)
  const setOn = (el, on) => { el.classList.toggle('on', on); el.setAttribute('aria-pressed', String(on)) }
  const stateChips = STATES.map(([v, l]) => chip(l, prefs.state === v, () => { prefs.state = v; stateChips.forEach((c, i) => setOn(c, STATES[i][0] === v)); persist(); reload() }))
  const wantChip = opts.view === 'want' || opts.view === 'reading' ? null : chip([icon('heart'), '想读'], prefs.want, () => { prefs.want = !prefs.want; setOn(wantChip, prefs.want); persist(); reload() }, { class: 'chip ic' + (prefs.want ? ' on' : '') })
  const missingCount = ctx.facets?.missing || 0
  const missingChip = missingCount || missing ? chip(`文件找不到（${missingCount}）`, missing, () => { missing = !missing; setOn(missingChip, missing); reload() }, { class: 'chip warnish' + (missing ? ' on' : '') }) : null
  const foldChip = canFold ? chip([icon('stack'), '按系列'], prefs.fold, () => { prefs.fold = !prefs.fold; setOn(foldChip, prefs.fold); persist(); reload() }, { class: 'chip ic' + (prefs.fold ? ' on' : ''), title: '同一个系列只显示一张卡片' }) : null
  const tagChip = tag ? h('a', { class: 'chip on ic', href: location.hash.replace(/([?&])tag=[^&]*&?/, '$1').replace(/[?&]$/, ''), title: '去掉标签筛选' }, icon('tag'), tag, icon('close')) : null
  const filters = h('div', { class: 'filters' }, tagChip, opts.view === 'reading' ? null : stateChips, wantChip, missingChip, foldChip, sortSel, fmtSel, totalEl)
  sortSel.onchange = () => { prefs.sort = sortSel.value; persist(); reload() }
  fmtSel.onchange = () => { prefs.format = fmtSel.value; persist(); reload() }
  const persist = () => save(prefKey, prefs)

  const gridTitle = opts.view === 'home' ? h('div', { class: 'section-title' }, h('h2', null, tag ? '标签：' + tag : '全部藏书')) : null
  const ftWrap = h('div')
  // 手机上没有侧栏：书架首页放一排入口
  const f = ctx.facets || {}
  const quick = opts.view === 'home' && !tag ? h('nav', { class: 'quick-links', 'aria-label': '更多入口' },
    h('a', { href: '#/want' }, icon('heart'), '想读', f.want ? h('small', null, f.want) : null),
    h('a', { href: '#/series' }, icon('stack'), '系列', f.series ? h('small', null, f.series) : null),
    h('a', { href: '#/notes' }, icon('notes'), '笔记'),
    h('a', { href: '#/stats' }, icon('activity'), '统计'),
    h('a', { href: '#/books' }, icon('book'), '图书'),
    h('a', { href: '#/comics' }, icon('comic'), '漫画'),
    offlineSupported ? h('a', { href: '#/offline' }, icon('saved'), '离线书架') : null) : null
  add(main, head, quick, continueWrap, gridTitle, filters, grid, emptyEl, sentinel, ftWrap)

  function query(extra = {}) {
    const p = new URLSearchParams()
    if (q) p.set('q', q)
    if (opts.kind) p.set('kind', opts.kind)
    if (opts.category) p.set('category', opts.category)
    if (opts.author) p.set('author', opts.author)
    if (opts.series) p.set('series', opts.series)
    if (tag) p.set('tag', tag)
    if (missing) p.set('missing', '1')
    if (opts.view === 'reading') p.set('reading', '1')
    if (opts.view === 'want' || (prefs.want && opts.view !== 'reading')) p.set('want', '1')
    if (prefs.format) p.set('format', prefs.format)
    if (prefs.state && opts.view !== 'reading') p.set('state', prefs.state)
    p.set('sort', opts.view === 'reading' ? 'recent' : prefs.sort)
    for (const [k, v] of Object.entries(extra)) p.set(k, v)
    return p
  }

  // 按系列折叠：先拿到各系列的本数，书架上同一个系列只放一张卡片
  async function loadSeries() {
    seriesMap = null
    if (!canFold || !prefs.fold) return
    const p = new URLSearchParams()
    if (opts.kind) p.set('kind', opts.kind)
    try {
      const list = await api('/api/series?' + p)
      seriesMap = Object.fromEntries(list.filter(s => s.count > 1).map(s => [s.name, s]))
    } catch { seriesMap = null }
  }

  function cardFor(b) {
    if (seriesMap && b.series && seriesMap[b.series]) {
      if (shownSeries.has(b.series)) return null
      shownSeries.add(b.series)
      return seriesCard(seriesMap[b.series], ctx)
    }
    const el = bookCard(b, ctx, {
      click: () => { if (!selMode) return false; toggleSel(b.id, el); return true },
      select: () => { setSelMode(true); toggleSel(b.id, el, true) },
      changed: nb => { Object.assign(b, nb); const ne = cardFor(b); if (ne) { if (sel.has(b.id)) ne.classList.add('selected'); el.replaceWith(ne) } },
    })
    if (sel.has(b.id)) el.classList.add('selected')
    if (selMode) el.setAttribute('aria-pressed', String(sel.has(b.id)))
    return el
  }

  async function loadMore(restoreTo) {
    if (loading || done) return
    loading = true
    const my = gen
    const first = offset === 0
    // 刷新时先留着原来的书，新数据到了再一次换掉，书架不会闪成空白
    if (!first || !grid.childElementCount) sentinel.textContent = '加载中…'
    try {
      if (first) await loadSeries()
      if (my !== gen) return
      const limit = first && restoreTo?.count > 90 ? Math.min(500, restoreTo.count) : 90
      const r = await api('/api/books?' + query({ offset, limit }))
      if (my !== gen) return
      if (first) { clear(grid); shownSeries = new Set() }
      total = r.total
      for (const b of r.items) { const el = cardFor(b); if (el) grid.append(el) }
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
    if (restoreTo) {
      // 等书都排好版再滚回原来的位置
      requestAnimationFrame(() => requestAnimationFrame(() => window.scrollTo(0, restoreTo.y)))
      return
    }
    // 内容不够一屏时继续加载
    if (!done && sentinel.getBoundingClientRect().top < innerHeight + 400) loadMore()
  }

  function renderEmpty() {
    clear(emptyEl)
    if (total > 0) return
    if (q || prefs.format || prefs.state || tag || missing || (prefs.want && opts.view !== 'reading')) {
      emptyEl.append(h('div', { class: 'empty' }, h('h3', null, '没有找到'), h('p', null, q ? `没有和“${q}”相关的书。试试作者名、拼音首字母，或者换个关键词。` : '这个筛选条件下没有书。')))
      return
    }
    if (opts.view === 'reading') {
      emptyEl.append(h('div', { class: 'empty' }, h('h3', null, '还没有在读的书'), h('p', null, '打开任意一本书，阅读进度会自动同步到这里，电脑、手机、手表都能接着读。')))
      return
    }
    if (opts.view === 'want') {
      emptyEl.append(h('div', { class: 'empty' }, h('h3', null, '还没有想读的书'), h('p', null, '在书的详情里点“想读”，或者长按（电脑上右键）书的封面选“想读”，想读的书都会列在这里。')))
      return
    }
    emptyEl.append(h('div', { class: 'empty' },
      h('h3', null, '书架还是空的'),
      ctx.isAdmin
        ? [h('p', null, '把电子书或漫画拖进这个窗口，或者放进 NAS 上的投递文件夹。书库会自动识别书名、作者，按规范改名并归类。'),
          h('button', { class: 'btn primary', onclick: () => ctx.pickFiles() }, icon('upload'), '选择文件')]
        : h('p', null, '管理员还没有往你能看的书库里放书。')))
  }

  function reload(restoreTo) {
    gen++
    offset = 0; total = 0; done = false; loading = false
    loadMore(restoreTo)
  }

  async function loadContinue() {
    clear(continueWrap)
    if (opts.view !== 'home' || q || tag) { if (gridTitle) gridTitle.hidden = !!q; return }
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
    return h('div', {
      class: 'continue-item', role: 'button', tabindex: 0, 'aria-label': `继续阅读《${b.title}》，已读 ${pct(b.percent)}`,
      onclick: () => ctx.navigate('#/read/' + b.id), onkeydown: e => e.key === 'Enter' && ctx.navigate('#/read/' + b.id),
      oncontextmenu: e => { e.preventDefault(); bookMenu(b, ctx, e.clientX, e.clientY, { changed: () => loadContinue() }) },
    },
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

  // ---------- 多选 ----------
  let selMode = false
  const sel = new Set()
  let bar = null
  const selCount = h('b')
  function setSelMode(on) {
    selMode = on
    main.classList.toggle('selecting', on)
    setOn(selBtn, on)
    if (!on) {
      sel.clear()
      for (const el of grid.querySelectorAll('.book.selected')) el.classList.remove('selected')
      for (const el of grid.querySelectorAll('.book[aria-pressed]')) el.removeAttribute('aria-pressed')
      bar?.remove(); bar = null
      return
    }
    for (const el of grid.querySelectorAll('.book[data-id]')) el.setAttribute('aria-pressed', String(sel.has(+el.dataset.id)))
    if (!bar) {
      bar = buildBar()
      document.body.append(bar)
    }
    updateBar()
  }
  function toggleSel(id, el, force) {
    const on = force ?? !sel.has(id)
    on ? sel.add(id) : sel.delete(id)
    el.classList.toggle('selected', on)
    el.setAttribute('aria-pressed', String(on))
    updateBar()
  }
  function updateBar() {
    if (!bar) return
    selCount.textContent = sel.size ? `已选 ${sel.size} 本` : '点书选择'
    for (const b of bar.querySelectorAll('[data-need]')) b.disabled = !sel.size
  }
  async function batch(body, label) {
    const ids = [...sel]
    if (!ids.length) return
    for (const b of bar.querySelectorAll('button')) b.disabled = true
    try {
      const r = await api('/api/books/batch', { method: 'POST', body: { ids, ...body } })
      toast(label ? `${label}：${r.message}` : r.message, r.ok ? {} : { error: true, duration: 7000 })
      setSelMode(false)
      ctx.refreshFacets()
      reload()
    } catch (e) { toastError(e); updateBar(); for (const b of bar?.querySelectorAll('button:not([data-need])') || []) b.disabled = false }
  }
  function at(btn) { const r = btn.getBoundingClientRect(); return { x: r.left, y: r.top - 6, above: true } }
  function markMenu(btn) {
    openMenu([
      { label: '标为读完', icon: 'check', run: () => batch({ read_state: 'done' }) },
      { label: '标为在读', icon: 'reading', run: () => batch({ read_state: 'reading' }) },
      { label: '标为未读（清除进度）', icon: 'undo', run: () => batch({ read_state: 'unread' }) },
      null,
      { label: '想读', icon: 'heart', run: () => batch({ want: true }) },
      { label: '取消想读', run: () => batch({ want: false }) },
      { label: '评分…', icon: 'star', run: async () => {
        const v = await formDialog({ title: `给 ${sel.size} 本书评分`, fields: [{ key: 'r', label: '评分', type: 'select', value: '5', options: [['5', '★★★★★'], ['4', '★★★★'], ['3', '★★★'], ['2', '★★'], ['1', '★'], ['0', '清除评分']] }], ok: '评分' })
        if (v) batch({ rating: +v.r })
      } },
    ], { ...at(btn), title: '标记' })
  }
  function manageMenu(btn) {
    const cats = ctx.settings?.categories || []
    openMenu([
      { label: '设成同一个系列…', icon: 'stack', run: async () => {
        const v = await formDialog({
          title: `把 ${sel.size} 本书设成一个系列`, message: '按选中的先后顺序编号。只想改系列名、不改序号时，起始序号留空。',
          fields: [{ key: 'series', label: '系列名', placeholder: '留空表示移出系列' }, { key: 'from', label: '起始序号', type: 'number', placeholder: '比如 1', min: 0 }], ok: '保存',
        })
        if (v) batch({ series: v.series.trim(), ...(v.from !== '' ? { series_from: +v.from } : {}) }, '系列')
      } },
      { label: '改分类…', icon: 'folder', run: async () => {
        const v = await formDialog({ title: `改 ${sel.size} 本书的分类`, fields: [{ key: 'c', label: '分类', type: 'select', options: cats.map(c => [c, c]), value: cats[0] }], ok: '保存' })
        if (v) batch({ category: v.c }, '分类')
      } },
      { label: '加标签、去标签…', icon: 'tag', run: async () => {
        const v = await formDialog({ title: `${sel.size} 本书的标签`, fields: [{ key: 'add', label: '加上这些标签', placeholder: '用逗号分隔' }, { key: 'rm', label: '去掉这些标签', placeholder: '用逗号分隔' }], ok: '保存' })
        const split = s => s.split(/[,，、]/).map(x => x.trim()).filter(Boolean)
        if (v && (v.add.trim() || v.rm.trim())) batch({ tags_add: split(v.add), tags_remove: split(v.rm) }, '标签')
      } },
      { label: '确认识别结果并整理', icon: 'check', run: () => batch({ confirm: true }, '确认') },
    ], { ...at(btn), title: '整理' })
  }
  function buildBar() {
    const btn = (ic, label, fn, need = true, cls = '') => h('button', { class: 'btn small ' + cls, type: 'button', dataset: need ? { need: '1' } : {}, onclick: e => fn(e.currentTarget) }, icon(ic), h('span', null, label))
    return h('div', { class: 'sel-bar', role: 'toolbar', 'aria-label': '多选操作' },
      selCount,
      h('button', { class: 'btn small quiet', type: 'button', onclick: () => { for (const el of grid.querySelectorAll('.book[data-id]')) toggleSel(+el.dataset.id, el, true) } }, '全选'),
      h('span', { class: 'grow' }),
      btn('check', '标记', markMenu),
      ctx.isAdmin ? btn('edit', '整理', manageMenu) : null,
      ctx.isAdmin ? btn('trash', '删除', async () => {
        if (!await confirmDialog({ title: `删除选中的 ${sel.size} 本书？`, message: '文件移到书库的“_回收站”文件夹，30 天内可以在“设置 → 回收站”里恢复（连同阅读进度和笔记）。', ok: '删除', danger: true })) return
        batch({ delete: true }, '删除')
      }, true, 'danger') : null,
      h('button', { class: 'icon-btn', type: 'button', title: '退出多选', 'aria-label': '退出多选', onclick: () => setSelMode(false) }, icon('close')))
  }

  search.addEventListener('input', debounce(() => { q = search.value.trim(); loadContinue(); reload(); loadFulltext() }, 300))
  const onKey = e => {
    if (e.key === '/' && document.activeElement?.tagName !== 'INPUT' && document.activeElement?.tagName !== 'TEXTAREA') { e.preventDefault(); search.focus() }
    if (e.key === 'Escape' && selMode && !document.querySelector('.dialog, .ctx-menu, .drawer.show')) setSelMode(false)
  }
  document.addEventListener('keydown', onKey)

  loadContinue()
  reload(saved)
  if (q) loadFulltext()

  return {
    refresh() { loadContinue(); reload() },
    destroy() {
      viewState.set(stateKey, { q, y: scrollY, count: offset })
      io.disconnect(); document.removeEventListener('keydown', onKey)
      bar?.remove()
    },
  }
}

export function bookCard(b, ctx, hooks = {}) {
  const open = () => b.status === 'review' && ctx.isAdmin ? ctx.openBook(b.id) : ctx.navigate('#/read/' + b.id)
  const menu = (x, y) => bookMenu(b, ctx, x, y, hooks)
  let pressTimer = 0, longPressed = false, sx = 0, sy = 0
  const label = [b.title, b.author, b.read_state === 'done' ? '已读完' : b.percent > 0 ? `已读 ${pct(b.percent)}` : '', b.missing ? '文件找不到' : ''].filter(Boolean).join('，')
  const el = h('div', {
    class: 'book', role: 'button', tabindex: 0, title: b.title + (b.author ? ' · ' + b.author : ''), 'aria-label': label, dataset: { id: b.id },
    onclick: e => { if (longPressed) { longPressed = false; return } if (hooks.click?.(e)) return; open() },
    onkeydown: e => {
      if (e.target !== el) return
      if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); if (!hooks.click?.(e)) open() }
      else if (e.key === 'i') ctx.openBook(b.id)
      else if (e.key === 'ContextMenu' || (e.shiftKey && e.key === 'F10')) { e.preventDefault(); const r = el.getBoundingClientRect(); menu(r.left + 16, r.top + 40) }
    },
    // 电脑右键、手机长按：弹出菜单（安卓长按也会触发 contextmenu，和计时器只算一次）
    oncontextmenu: e => {
      e.preventDefault()
      clearTimeout(pressTimer)
      if (longPressed && e.pointerType !== 'mouse') return
      if (e.pointerType === 'touch') { longPressed = true; menu(null, null) } else menu(e.clientX, e.clientY)
    },
    ontouchstart: e => {
      longPressed = false
      const t = e.touches[0]; sx = t.clientX; sy = t.clientY
      pressTimer = setTimeout(() => { longPressed = true; navigator.vibrate?.(8); menu(null, null) }, 520)
    },
    ontouchend: () => clearTimeout(pressTimer),
    ontouchmove: e => { const t = e.touches[0]; if (Math.abs(t.clientX - sx) > 8 || Math.abs(t.clientY - sy) > 8) clearTimeout(pressTimer) },
  },
    withMore(withBadges(coverEl(b, apiUrl), b), () => ctx.openBook(b.id)),
    b.percent > 0 && b.read_state !== 'done' ? h('div', { class: 'bar' }, h('i', { style: { width: pct(b.percent) } })) : null,
    h('div', { class: 't' }, b.title),
    h('div', { class: 'a', onclick: e => { e.stopPropagation(); ctx.openBook(b.id) } }, b.author || (b.series && b.kind === 'comic' ? b.series : '') || '详情'),
    h('span', { class: 'sel-mark', 'aria-hidden': 'true' }, icon('check')))
  return el
}

// 改一本书自己的状态（读完、想读、评分），所有人都能做
export async function setMine(id, body) {
  return api(`/api/books/${id}/me`, { method: 'PATCH', body })
}

// 书卡片的菜单：打开、详情、读完、想读、下载到手机 / 离线可读、选择、删除
export function bookMenu(b, ctx, x, y, hooks = {}) {
  const changed = nb => { ctx.refreshFacets(); hooks.changed?.(nb) }
  const items = [
    { label: b.percent > 0 && b.read_state !== 'done' ? '继续阅读' : '打开', icon: 'reading', run: () => ctx.navigate('#/read/' + b.id) },
    { label: '详情', icon: 'book', run: () => ctx.openBook(b.id) },
    null,
    b.read_state === 'done'
      ? { label: '标为未读', icon: 'undo', run: async () => { try { await setMine(b.id, { read_state: 'unread' }); toast(`《${b.title}》已标为未读`); changed({ read_state: 'unread', percent: 0 }) } catch (e) { toastError(e) } } }
      : { label: '标为读完', icon: 'check', run: async () => { try { await setMine(b.id, { read_state: 'done' }); toast(`《${b.title}》已标为读完`); changed({ read_state: 'done' }) } catch (e) { toastError(e) } } },
    { label: b.want ? '取消想读' : '想读', icon: 'heart', run: async () => { try { await setMine(b.id, { want: !b.want }); toast(b.want ? '已取消想读' : `已加入想读：《${b.title}》`); changed({ want: !b.want }) } catch (e) { toastError(e) } } },
  ]
  if (isNative) {
    const st = refreshDownloads().find(d => d.id === b.id)
    items.push(st?.state === 'done'
      ? { label: '从手机删除', icon: 'phone', run: () => { call('removeDownload', b.id); toast('已从手机删除') } }
      : { label: st?.state === 'downloading' ? '正在下载到手机…' : '下载到手机', icon: 'download', disabled: st?.state === 'downloading', run: () => downloadToPhone(b.id) })
  }
  let offSlot = null
  if (offlineSupported) {
    offSlot = h('div', { class: 'ctx-slot' })
    items.push(offSlot)
  }
  if (hooks.select) items.push({ label: '选择', icon: 'select', run: () => hooks.select() })
  if (ctx.isAdmin) items.push(null, { label: '删除', icon: 'trash', danger: true, run: () => deleteBook(b, ctx) })
  openMenu(items, { x, y, title: b.title })
  if (offSlot) api('/api/books/' + b.id).then(full => { const btn = offlineButton(full, ctx); if (btn) offSlot.append(btn) }).catch(() => {})
}

// 下载到手机：和书的详情里的按钮一样，交给 App
export async function downloadToPhone(id) {
  if (net.route === 'offline') { toast('现在连不上书库，联网后再下载', { error: true }); return }
  try {
    const b = await api('/api/books/' + id)
    call('download', b.id, { ...b, files: undefined, description: b.description })
    toast(`《${b.title}》开始下载到手机`)
  } catch (e) { toastError(e) }
}

export async function deleteBook(b, ctx, after) {
  const ok = await confirmDialog({ title: `删除《${b.title}》？`, message: '文件移到书库的“_回收站”文件夹，30 天内可以在“设置 → 回收站”里恢复，阅读进度和笔记也会一起回来。', ok: '删除', danger: true })
  if (!ok) return false
  try {
    await api('/api/books/' + b.id, { method: 'DELETE' })
    toast('已删除，可以在“设置 → 回收站”里恢复')
    ctx.refreshFacets(); after ? after() : ctx.rerender()
    return true
  } catch (e) { toastError(e); return false }
}

// 封面上的小标记：已下载到手机、已存到这台设备、文件找不到
function withBadges(cv, b) {
  if (isDownloaded(b.id)) cv.append(h('span', { class: 'dl', title: '已下载到手机' }, icon('phone')))
  else if (offlineSupported && isOfflineBook(b.id)) cv.append(h('span', { class: 'dl off', title: '已存到这台设备，没网也能读' }, icon('saved')))
  if (b.missing) {
    cv.querySelector('.flag')?.remove()
    cv.append(h('span', { class: 'flag missing', title: '书库里找不到这本书的文件了（被移走、改名，或者共享文件夹没挂上）' }, '文件找不到'))
  }
  return cv
}

function withMore(cv, fn) {
  cv.append(h('button', { class: 'more', type: 'button', title: '详情与编辑', 'aria-label': '详情与编辑', onclick: e => { e.stopPropagation(); fn() } }, icon('edit')))
  return cv
}
