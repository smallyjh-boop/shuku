// 发现：粘贴链接下载入库；在公开书库（维基文库、古腾堡计划、互联网档案馆、任意 OPDS 书源）里搜索浏览，一键加入书库

import { h, clear, fill, add, icon, iconBtn, toast, toastError, openDrawer, confirmDialog, fmtBytes, genCover, store, save } from './ui.js'
import { api, apiUrl } from './api.js'

const PREF = 'shuku.discover'
const EXAMPLES = {
  wikisource: ['红楼梦', '史记', '聊斋志异', '鲁迅', '唐诗三百首'],
  ia: ['红楼梦', 'Sherlock Holmes', '鲁迅', '论语'],
  gutenberg: ['Pride and Prejudice', 'Sherlock Holmes', 'Dickens', '红楼梦'],
}

export async function renderDiscover(main, params, ctx) {
  const prefs = store(PREF, { source: 'wikisource' })
  let sources = []
  let source = null
  let stack = []        // OPDS 导航：[{ url, title }]
  let query = ''
  let gen = 0

  // ---------- 从链接添加 ----------
  const linkInput = h('input', { class: 'input', type: 'url', placeholder: '粘贴电子书的下载链接（epub、pdf、mobi、azw3、txt、cbz 等）', 'aria-label': '下载链接', enterkeyhint: 'go' })
  const linkBtn = h('button', { class: 'btn primary', type: 'submit' }, icon('download'), h('span', null, '下载到书库'))
  const linkForm = h('form', { class: 'link-add' }, h('label', { class: 'search link' }, icon('link'), linkInput), linkBtn)
  linkForm.onsubmit = async e => {
    e.preventDefault()
    const url = linkInput.value.trim()
    if (!url) { linkInput.focus(); return }
    linkBtn.disabled = true
    try {
      await api('/api/fetch', { method: 'POST', body: { url, source: '链接' } })
      linkInput.value = ''
      toast('已开始下载，完成后自动入库')
      jobs.poll(true)
    } catch (err) { toastError(err) }
    linkBtn.disabled = false
  }

  // ---------- 书源 ----------
  const chips = h('div', { class: 'src-chips', role: 'tablist', 'aria-label': '书源' })
  const note = h('p', { class: 'src-note' })
  const search = h('input', { class: 'input', type: 'search', 'aria-label': '搜索书源', enterkeyhint: 'search' })
  const searchForm = h('form', { class: 'src-search' }, h('label', { class: 'search' }, icon('search'), search))
  const crumbs = h('div', { class: 'crumbs' })
  const results = h('div', { class: 'disc-results' })
  const more = h('div', { class: 'loading-more' })

  add(main,
    h('div', { class: 'page-head' }, h('div', null, h('h1', null, '发现'), h('div', { class: 'sub' }, '在公开书库里找书，或者粘贴下载链接，一键加入书库'))),
    linkForm,
    h('div', { class: 'section-title src-title' }, h('h2', null, '公开书库'),
      h('button', { class: 'btn small quiet', type: 'button', onclick: () => manageSources() }, icon('settings'), '管理书源')),
    chips, note, searchForm, crumbs, results, more)

  searchForm.onsubmit = e => { e.preventDefault(); query = search.value.trim(); stack = []; load() }

  async function loadSources(selectId) {
    sources = await api('/api/catalogs')
    const want = selectId || params.get('src') || prefs.source
    source = sources.find(s => s.id === want) || sources[0] || null
    drawChips()
  }

  function drawChips() {
    fill(chips, sources.map(s => h('button', {
      class: 'chip' + (source?.id === s.id ? ' on' : ''), role: 'tab', 'aria-selected': source?.id === s.id, type: 'button',
      onclick: () => { if (source?.id !== s.id) { source = s; prefs.source = s.id; save(PREF, prefs); stack = []; query = ''; search.value = ''; drawChips(); load() } },
    }, s.name)),
    h('button', { class: 'chip add', type: 'button', onclick: () => addSource() }, icon('plus'), '添加书源'))
    note.textContent = source?.note || (source && !source.builtin ? source.url : '')
    search.placeholder = source ? `在${source.name}里搜索书名、作者` : ''
  }

  // ---------- 浏览 / 搜索 ----------
  async function load(nextUrl) {
    if (!source) { fill(results, h('div', { class: 'empty' }, h('h3', null, '还没有书源'), h('p', null, '点“添加书源”，填一个 OPDS 地址。'))); return }
    const my = ++gen
    const cur = stack[stack.length - 1]
    drawCrumbs()
    if (!nextUrl) fill(results, h('p', { class: 'muted' }, query ? `正在${source.name}里搜索“${query}”…` : '正在打开…'))
    fill(more)
    const p = new URLSearchParams()
    if (nextUrl) p.set('next', nextUrl)
    else if (query) p.set('q', query)
    else if (cur) p.set('url', cur.url)
    // 维基文库和档案馆只能搜索
    if (!nextUrl && !query && !cur && (source.kind === 'wikisource' || source.kind === 'ia')) {
      fill(results, h('div', { class: 'empty disc-hint' },
        h('h3', null, `搜索${source.name}`),
        h('p', null, source.kind === 'wikisource' ? '维基文库收录了大量公版的中文古籍和近代作品，搜到后会现场生成 EPUB 下载。简体、繁体书名都能搜。' : '只列出可以直接下载的公版书和扫描本（只能在线借阅的不显示）。'),
        h('div', { class: 'row wrap', style: { justifyContent: 'center' } }, (EXAMPLES[source.id] || []).map(x => h('button', { class: 'chip', type: 'button', onclick: () => { search.value = x; query = x; load() } }, x)))))
      return
    }
    let page
    try { page = await api(`/api/catalogs/${encodeURIComponent(source.id)}/browse?` + p) } catch (e) {
      if (my !== gen) return
      fill(results, h('div', { class: 'empty' }, h('h3', null, '打不开这个书源'), h('p', null, e.message), h('button', { class: 'btn', type: 'button', onclick: () => load(nextUrl) }, icon('refresh'), '重试')))
      return
    }
    if (my !== gen) return
    if (!nextUrl) clear(results)
    if (!page.entries.length && !nextUrl) {
      fill(results, h('div', { class: 'empty' }, h('h3', null, '没有找到'), h('p', null, query ? `${source.name}里没有和“${query}”相关的书。换个关键词，或者试试别的书源。` : '这个目录是空的。')))
      return
    }
    const navs = page.entries.filter(e => e.kind === 'nav')
    const books = page.entries.filter(e => e.kind === 'book')
    if (navs.length) add(results, h('div', { class: 'disc-navs' }, navs.map(e => navRow(e))))
    if (books.length) {
      let grid = results.querySelector('.grid.disc')
      if (!grid) { grid = h('div', { class: 'grid disc' }); add(results, grid) }
      for (const b of books) grid.append(bookCard(b))
    }
    if (page.next) fill(more, h('button', { class: 'btn', type: 'button', onclick: () => load(page.next) }, '加载更多'))
  }

  function drawCrumbs() {
    if (!stack.length && !query) { clear(crumbs); return }
    fill(crumbs,
      h('button', { class: 'btn small quiet', type: 'button', onclick: () => { if (query) { query = ''; search.value = '' } else stack.pop(); load() } }, icon('back'), '返回'),
      h('span', { class: 'muted small-text' }, query ? `搜索：${query}` : stack.map(s => s.title).join(' / ')))
  }

  function navRow(e) {
    return h('button', { class: 'disc-nav', type: 'button', onclick: () => { stack.push({ url: e.href, title: e.title }); query = ''; search.value = ''; load(); main.scrollIntoView?.({ block: 'start' }) } },
      icon('folder'), h('span', { class: 'grow' }, h('b', null, e.title), e.summary ? h('small', null, e.summary) : null), icon('next'))
  }

  function coverOf(b) {
    const el = h('div', { class: 'cv' })
    if (b.cover) {
      const img = h('img', { src: apiUrl(`/api/catalog-image?c=${encodeURIComponent(source.id)}&url=${encodeURIComponent(b.cover)}`), alt: '', loading: 'lazy', decoding: 'async' })
      img.onerror = () => img.replaceWith(genCover(b.title, b.author))
      el.append(img)
    } else el.append(genCover(b.title, b.author))
    if (b.id?.startsWith('have:')) el.append(h('span', { class: 'flag have' }, '已在书库'))
    return el
  }

  function bookCard(b) {
    const open = () => openEntry(b)
    return h('div', { class: 'book', role: 'button', tabindex: 0, title: b.title, onclick: open, onkeydown: e => e.key === 'Enter' && open() },
      coverOf(b), h('div', { class: 't' }, b.title), h('div', { class: 'a' }, b.author || b.language || ''))
  }

  function openEntry(b) {
    const d = openDrawer({ title: source.name })
    const fmts = h('div', { class: 'disc-formats' }, b.formats.map((f, i) => h('button', {
      class: 'btn ' + (i === 0 ? 'primary' : ''), type: 'button',
      onclick: async ev => {
        ev.currentTarget.disabled = true
        try {
          await api('/api/fetch', { method: 'POST', body: { url: f.url, name: [b.title, b.author].filter(Boolean).join(' - '), catalog: source.id, source: source.name } })
          toast(`《${b.title}》开始下载，完成后自动入库`)
          jobs.poll(true)
          d.close()
        } catch (e) { toastError(e); ev.currentTarget.disabled = false }
      },
    }, icon('download'), `加入书库 · ${f.label}${f.size ? '（' + fmtBytes(f.size) + '）' : ''}`)))
    fill(d.body,
      h('div', { class: 'detail-top' }, coverOf(b), h('div', null,
        h('h2', null, b.title),
        b.author ? h('div', { class: 'by' }, b.author) : null,
        h('div', { class: 'meta' }, b.language ? h('span', { class: 'tag' }, b.language) : null, b.year ? h('span', { class: 'tag' }, b.year + ' 年') : null,
          b.id?.startsWith('have:') ? h('span', { class: 'tag' }, '书库里已有同名的书') : null))),
      b.summary ? h('p', { class: 'desc' }, b.summary) : null,
      h('div', { class: 'sub-title' }, '下载格式'), fmts,
      h('p', { class: 'muted small-text', style: { marginTop: '16px' } }, '下载到 NAS 的投递目录后，会和拖进来的书一样自动识别、改名、归类。'))
  }

  // ---------- 书源管理 ----------
  function addSource() {
    const name = h('input', { class: 'input', placeholder: '不填就用书源自己的名字' })
    const url = h('input', { class: 'input', type: 'url', placeholder: 'http://192.168.1.10:8083/opds' })
    const user = h('input', { class: 'input', autocomplete: 'off' })
    const pass = h('input', { class: 'input', type: 'password', autocomplete: 'new-password' })
    const err = h('p', { class: 'small-text', style: { color: 'var(--danger)', margin: 0 } })
    const btn = h('button', { class: 'btn primary', type: 'button' }, '添加')
    const d = openDrawer({
      title: '添加书源',
      body: h('div', { class: 'stack' },
        h('p', { class: 'muted small-text', style: { margin: 0 } }, '支持 OPDS 书目：Calibre 内容服务器（地址后加 /opds）、talebook、Komga（/opds/v1.2/catalog）、Kavita、COPS 等，也可以是网上公开的 OPDS 书库。'),
        h('label', { class: 'field' }, h('span', null, 'OPDS 地址'), url),
        h('label', { class: 'field' }, h('span', null, '名称'), name),
        h('div', { class: 'form-grid' }, h('label', { class: 'field' }, h('span', null, '用户名'), user), h('label', { class: 'field' }, h('span', null, '密码'), pass)),
        h('small', { class: 'muted' }, '不需要登录的书源留空。密码只保存在 NAS 上。'),
        err),
      footer: btn,
    })
    btn.onclick = async () => {
      btn.disabled = true; err.textContent = ''
      try {
        const c = await api('/api/catalogs', { method: 'POST', body: { name: name.value.trim(), url: url.value.trim(), username: user.value.trim(), password: pass.value } })
        toast('已添加书源：' + c.name)
        d.close()
        await loadSources(c.id); stack = []; query = ''; load()
      } catch (e) { err.textContent = e.message }
      btn.disabled = false
    }
    setTimeout(() => url.focus(), 80)
  }

  function manageSources() {
    const list = h('div', { class: 'stack', style: { gap: '8px' } })
    const draw = () => fill(list, sources.map(s => h('div', { class: 'src-row' },
      h('div', { class: 'grow' }, h('b', null, s.name), h('small', null, s.builtin ? (s.note || '') : s.url)),
      h('button', { class: 'btn small quiet danger', type: 'button', onclick: async () => {
        if (!await confirmDialog({ title: `${s.builtin ? '隐藏' : '删除'}书源“${s.name}”？`, message: s.builtin ? '内置书源隐藏后可以随时恢复。' : '只是删除这个书源的设置，书库里的书不受影响。', ok: s.builtin ? '隐藏' : '删除', danger: true })) return
        try { await api('/api/catalogs/' + encodeURIComponent(s.id), { method: 'DELETE' }); await loadSources(); draw(); load() } catch (e) { toastError(e) }
      } }, s.builtin ? '隐藏' : '删除'))))
    draw()
    openDrawer({
      title: '管理书源',
      body: h('div', { class: 'stack' }, list,
        h('div', { class: 'row' },
          h('button', { class: 'btn', type: 'button', onclick: () => addSource() }, icon('plus'), '添加书源'),
          h('button', { class: 'btn quiet', type: 'button', onclick: async () => { await api('/api/catalogs/restore', { method: 'POST' }); await loadSources(); draw(); toast('已恢复内置书源') } }, '恢复内置书源'))),
    })
  }

  const jobs = jobPanel()
  try { await loadSources() } catch (e) { fill(results, h('p', { class: 'muted' }, e.message)); return }
  const q0 = params.get('q')
  if (q0) { query = q0; search.value = q0 }
  load()
  jobs.poll()
  return { destroy() { jobs.stop() }, refresh() {} }
}

// ---------- 下载任务面板 ----------

function jobPanel() {
  let panel = null, listEl = null, timer = 0, closed = false, seen = new Set(), first = true
  const stop = () => { clearTimeout(timer); timer = 0 }
  async function poll(force) {
    if (force) closed = false
    stop()
    let list
    try { list = await api('/api/fetch') } catch { timer = setTimeout(poll, 5000); return }
    const recent = list.filter(j => j.status === 'queued' || j.status === 'downloading' || Date.now() / 1000 - (j.finished_at || 0) < 600)
    const active = recent.some(j => j.status === 'queued' || j.status === 'downloading')
    // 打开页面前就失败的不再提示，之后失败的弹出提示
    for (const j of recent) {
      if (j.status === 'error' && !seen.has(j.id)) { seen.add(j.id); if (!first) toast(`下载失败：${j.error}`, { error: true, duration: 6000 }) }
    }
    first = false
    if (recent.length && !closed) draw(recent)
    else if (panel) { panel.remove(); panel = null }
    if (active) timer = setTimeout(poll, 1200)
  }
  function draw(list) {
    if (!panel || !document.body.contains(panel)) {
      listEl = h('ul')
      panel = h('div', { class: 'uploads jobs', role: 'status' }, h('header', null, icon('download'), h('span', null, '下载到书库'), iconBtn('close', '收起', () => { closed = true; panel.remove(); panel = null })), listEl)
      document.body.append(panel)
    }
    fill(listEl, list.map(j => {
      const st = j.status === 'done' ? '已下载，正在入库' : j.status === 'error' ? '失败' : j.status === 'queued' ? '排队中' : j.total > 0 ? Math.round(j.bytes / j.total * 100) + '%' : fmtBytes(j.bytes)
      let name = j.name
      if (!name) { try { name = decodeURIComponent(j.url.split('/').pop() || j.url) } catch { name = j.url } }
      const active = j.status === 'queued' || j.status === 'downloading'
      // 下载中、排队的能取消；完成、失败的点 × 从列表里去掉
      const x = h('button', { class: 'icon-btn tiny', type: 'button', title: active ? '取消下载' : '从列表去掉', 'aria-label': (active ? '取消下载：' : '从列表去掉：') + name, onclick: async e => {
        e.currentTarget.disabled = true
        try { await api('/api/fetch/' + encodeURIComponent(j.id), { method: 'DELETE' }); if (active) toast('已取消下载：' + name) } catch (err) { toastError(err) }
        poll(true)
      } }, icon('close'))
      return h('li', { class: j.status === 'done' ? 'ok' : j.status === 'error' ? 'err' : '' },
        h('div', { class: 'n' }, h('span', { title: j.url }, name), h('span', { class: 's' }, st), x),
        j.status === 'error' ? h('div', { class: 'e' }, j.error) : null,
        j.status === 'downloading' && j.total > 0 ? h('div', { class: 'bar' }, h('i', { style: { width: Math.round(j.bytes / j.total * 100) + '%' } })) : null)
    }))
  }
  return { poll, stop }
}
