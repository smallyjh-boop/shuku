// 弹窗：列出当前页面上的电子书链接，勾选后一次加入书库；下面是加入记录。

import { getConfig, isConfigured, resolveRoute, routeLabel, fmtBytes, checkExtensionUpdate } from './lib/shuku.js'
import { scanPage } from './lib/detect.js'

const $ = id => document.getElementById(id)
function h(tag, attrs, ...kids) {
  const el = document.createElement(tag)
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v == null || v === false) continue
    if (k === 'class') el.className = v
    else if (k.startsWith('on')) el.addEventListener(k.slice(2), v)
    else el.setAttribute(k, v === true ? '' : v)
  }
  for (const c of kids.flat()) if (c != null && c !== false) el.append(c.nodeType ? c : String(c))
  return el
}

const MODE_HINT = {
  auto: '公开书库由书库直接下载，其他网站用浏览器下载后传到书库',
  browser: '用浏览器下载（带着你在这个网站的登录状态），再传到书库',
  nas: '由书库直接下载，浏览器关了也不影响',
}

let config = {}
let route = null
let probed = false
let groups = []
let page = { url: '', title: '' }
let scanError = ''
let justAdded = 0

init()

// 插件有新版本：下载新的插件包，解压覆盖原来的文件夹，再重新加载
function showUpdate(u) {
  if (!u || document.getElementById('update-note')) return
  const steps = h('div', { class: 'hint' }, '下载后解压，覆盖原来的插件文件夹（当初“加载已解压的扩展程序”时选的那个），再点“重新加载”。')
  $('main').prepend(h('div', { class: 'note', id: 'update-note' },
    h('b', null, `插件有新版本 ${u.version}`),
    `现在是 ${chrome.runtime.getManifest().version}。`, steps,
    h('div', { class: 'row' },
      h('button', { class: 'btn primary', type: 'button', onclick: () => chrome.tabs.create({ url: u.url }) }, '下载新版本'),
      h('button', { class: 'btn', type: 'button', onclick: () => chrome.runtime.reload() }, '已覆盖，重新加载'))))
}

async function init() {
  $('settings').onclick = () => chrome.runtime.openOptionsPage()
  $('open').onclick = openLibrary
  $('clear').onclick = () => chrome.runtime.sendMessage({ type: 'clear' })
  $('add').onclick = add
  config = await getConfig()
  $('server').textContent = config.name || ''
  const s = await chrome.storage.session.get('tasks')
  renderTasks(s.tasks || [])
  chrome.storage.onChanged.addListener((ch, area) => {
    if (area === 'session' && ch.tasks) renderTasks(ch.tasks.newValue || [])
  })
  if (!isConfigured(config)) {
    setRoute('未连接', 'off')
    renderSetup()
    return
  }
  const scanning = scan()
  resolveRoute().then(r => {
    route = r
    probed = true
    setRoute(routeLabel(r), r ? 'on' : 'off')
    updateAction()
    if (!r) $('main').prepend(h('div', { class: 'note warn' }, h('b', null, '连不上书库'), '内网和 Tailscale 地址都不通。确认书库开着，电脑在家里的网络或者打开了 Tailscale。'))
    else checkExtensionUpdate().then(showUpdate)
  })
  await scanning
  renderFound()
}

function setRoute(text, cls) {
  $('route').textContent = text
  $('route').className = 'route ' + cls
}

async function openLibrary() {
  const r = route || await resolveRoute()
  const base = r?.base || config.lan || config.remote
  if (base) chrome.tabs.create({ url: base + '/' })
  else chrome.runtime.openOptionsPage()
  window.close()
}

function renderSetup() {
  $('main').replaceChildren(h('div', { class: 'note' },
    h('b', null, '先连接你的书库'),
    '在书库网页的“设置 → 手机、手表和浏览器插件”里点“显示配对二维码”，把书库地址和配对码填到插件设置里。',
    h('div', null, h('button', { class: 'btn primary', type: 'button', onclick: () => chrome.runtime.openOptionsPage() }, '去设置'))))
}

// ---------- 扫描当前页面 ----------

async function scan() {
  // ?tab=标签页 ID：测试时在普通标签页里打开弹窗，指定要读取的页面
  const forced = +new URLSearchParams(location.search).get('tab')
  const tab = forced ? await chrome.tabs.get(forced).catch(() => null) : (await chrome.tabs.query({ active: true, currentWindow: true }))[0]
  page = { url: tab?.url || '', title: tab?.title || '' }
  if (!tab?.id || !/^https?:/i.test(tab.url || '')) {
    scanError = '这是浏览器自带的页面，读不到上面的链接。'
    return
  }
  let results
  try {
    results = await chrome.scripting.executeScript({ target: { tabId: tab.id, allFrames: true }, func: scanPage })
  } catch {
    try {
      results = await chrome.scripting.executeScript({ target: { tabId: tab.id }, func: scanPage })
    } catch (e) {
      scanError = '浏览器不允许插件读取这个页面' + (e?.message ? `（${e.message}）` : '')
      return
    }
  }
  const frames = (results || []).sort((a, b) => a.frameId - b.frameId).map(r => r.result).filter(Boolean)
  if (frames[0]?.title) page.title = frames[0].title
  groups = buildGroups(frames)
}

function hostOf(url, fallback) {
  try { return url.startsWith('ia:') ? 'archive.org' : new URL(url).hostname } catch { return fallback || '' }
}

/** 同一本书的几种格式合成一组，每组默认勾选最合适的格式 */
function buildGroups(frames) {
  const map = new Map()
  const seen = new Set()
  for (const f of frames) {
    for (const it of f.items || []) {
      if (seen.has(it.url)) continue
      seen.add(it.url)
      let g = map.get(it.key)
      if (!g) map.set(it.key, g = { key: it.key, title: '', items: [], host: hostOf(it.url, f.host), sel: new Set() })
      if (!g.title && it.title) g.title = it.title
      g.items.push(it)
    }
  }
  const list = [...map.values()]
  for (const g of list) {
    g.items.sort((a, b) => a.rank - b.rank)
    g.weak = g.items.every(i => i.weak)
    g.display = g.title || g.items.find(i => i.file)?.file || g.items[0].text || g.items[0].url
    // 同一组里标签重名（几个 EPUB）时加编号
    const used = {}
    for (const i of g.items) {
      used[i.label] = (used[i.label] || 0) + 1
      if (used[i.label] > 1) i.label += ' ' + used[i.label]
    }
  }
  // 默认勾选：书（不是 txt、zip 这类不一定是书的文件）；PDF 很多时（比如资料页）先不勾
  const books = list.filter(g => !g.weak)
  for (const g of books) {
    const best = g.items[0]
    if (best.fmt === 'pdf' && books.length > 5) continue
    g.sel.add(best.url)
  }
  return books.concat(list.filter(g => g.weak))
}

// ---------- 列表 ----------

function renderFound() {
  const main = $('main')
  main.querySelectorAll('.empty, .found-head, .groups').forEach(el => el.remove())
  if (scanError) {
    main.append(h('div', { class: 'empty' }, h('b', null, '读不到这个页面'), scanError))
    return updateAction()
  }
  if (!groups.length) {
    main.append(h('div', { class: 'empty' }, h('b', null, '这个页面上没有找到电子书'), '也可以在任何下载链接上点右键，选“添加到书库”。'))
    return updateAction()
  }
  const nBooks = groups.filter(g => !g.weak).length
  const nOther = groups.length - nBooks
  const all = h('input', { type: 'checkbox', 'aria-label': '全选' })
  all.onchange = () => {
    for (const g of groups) {
      if (all.checked) { if (!g.sel.size) g.sel.add(g.items[0].url) } else g.sel.clear()
    }
    sync()
  }
  const label = nBooks && nOther ? `本页找到 ${nBooks} 本书、${nOther} 个其他文件` : nBooks ? `本页找到 ${nBooks} 本书` : `本页找到 ${nOther} 个文件（不一定是书）`
  main.append(h('div', { class: 'found-head' }, h('label', { class: 'check' }, all, h('span', null, label))))
  const ul = h('ul', { class: 'groups' })
  for (const g of groups) {
    const cb = h('input', { type: 'checkbox', 'aria-label': '选择 ' + g.display })
    cb.onchange = () => {
      if (cb.checked) g.sel.add(g.items[0].url)
      else g.sel.clear()
      sync()
    }
    const chips = g.items.map(it => {
      const b = h('button', { class: 'fmt', type: 'button', title: it.file || it.url }, it.label)
      b.onclick = () => {
        if (g.sel.has(it.url)) g.sel.delete(it.url)
        else g.sel.add(it.url)
        sync()
      }
      return [it.url, b]
    })
    g.el = { cb, chips: new Map(chips), all }
    const titleEl = h('div', { class: 'g-title', title: g.display }, g.display)
    titleEl.onclick = () => cb.click()
    ul.append(h('li', { class: 'group' + (g.weak ? ' weak' : '') },
      h('label', { class: 'check' }, cb),
      h('div', null, titleEl, h('div', { class: 'g-meta' }, g.host), h('div', { class: 'fmts' }, chips.map(c => c[1])))))
  }
  main.append(ul)
  sync()
}

function sync() {
  let picked = 0
  for (const g of groups) {
    if (!g.el) continue
    g.el.cb.checked = g.sel.size > 0
    for (const [url, b] of g.el.chips) b.classList.toggle('on', g.sel.has(url))
    if (g.sel.size) picked++
  }
  const all = groups[0]?.el?.all
  if (all) {
    all.checked = picked === groups.length
    all.indeterminate = picked > 0 && picked < groups.length
  }
  updateAction()
}

function updateAction() {
  const n = groups.reduce((a, g) => a + g.sel.size, 0)
  $('action').hidden = !isConfigured(config) || !groups.length
  const btn = $('add')
  if (justAdded) return
  if (probed && !route) {
    btn.disabled = true
    btn.textContent = '连不上书库'
  } else {
    btn.disabled = !n
    btn.textContent = n ? `加入书库（${n} 个文件）` : '先勾选要加入的书'
  }
  const hint = $('add-hint')
  hint.className = 'hint'
  hint.textContent = MODE_HINT[config.mode || 'auto']
}

async function add() {
  const items = []
  for (const g of groups) {
    for (const url of g.sel) {
      const it = g.items.find(i => i.url === url)
      if (it) items.push({ url, fmt: it.fmt, file: it.file, title: g.title })
    }
  }
  if (!items.length) return
  const btn = $('add')
  btn.disabled = true
  const r = await chrome.runtime.sendMessage({ type: 'add', items, page }).catch(e => ({ ok: false, error: e.message }))
  if (!r?.ok) {
    btn.disabled = false
    $('add-hint').textContent = r?.error || '加入失败'
    $('add-hint').className = 'hint err'
    return
  }
  for (const g of groups) g.sel.clear()
  justAdded = 1
  btn.textContent = r.count ? `已开始加入 ${r.count} 个文件` : '这些文件已经在加入了'
  sync()
  setTimeout(() => { justAdded = 0; updateAction() }, 2000)
}

// ---------- 加入记录 ----------

const ACTIVE = ['queued', 'downloading', 'uploading', 'fetching']

// 传到书库以后的入库结果（旧版书库没有这个结果，一直显示“正在整理入库”）
function doneText(t) {
  const got = t.mode === 'nas' ? '书库已下载' : '已传到书库'
  switch (t.ingest) {
    case 'done': return '已入库' + (t.ingestTitle ? `《${t.ingestTitle}》` : '')
    case 'review': return '已入库' + (t.ingestTitle ? `《${t.ingestTitle}》` : '') + '，识别把握不大，请在书库“待确认”里看一下'
    case 'duplicate': return '书库里已经有这本书，没有重复加入'
    case 'unsupported': return got + '，但书库认不出这个文件的格式（放在投递目录的 _无法识别 里）'
    case 'error': return got + '，但入库失败' + (t.ingestMsg ? '：' + t.ingestMsg : '') + '（放在投递目录的 _出错 里）'
    case 'slow': return got + '，整理入库比较慢，过一会儿在书库里看看'
    case 'unknown': return got
  }
  return got + '，正在整理入库'
}

function statusText(t) {
  const pct = t.total ? ` ${Math.round((t.bytes / t.total) * 100)}%` : t.bytes ? ` ${fmtBytes(t.bytes)}` : ''
  switch (t.status) {
    case 'queued': return '排队中'
    case 'downloading': return '浏览器下载中' + pct
    case 'uploading': return '正在传到书库' + pct
    case 'fetching': return (t.jobStatus === 'queued' ? '书库排队中' : '书库正在下载') + pct
    case 'done': return doneText(t)
    case 'error': return t.error || '失败'
  }
  return ''
}

function renderTasks(list) {
  const box = $('tasks')
  const ul = $('task-list')
  const shown = list.slice(-8).reverse()
  box.hidden = !shown.length
  $('clear').hidden = !list.some(t => !ACTIVE.includes(t.status))
  ul.replaceChildren(...shown.map(t => {
    const active = ACTIVE.includes(t.status)
    const name = t.name || t.title || decodeURIComponent((t.url.split('?')[0].split('/').pop() || t.url))
    const acts = h('span', { class: 't-acts' })
    if (active) acts.append(h('button', { class: 'link muted', type: 'button', onclick: () => chrome.runtime.sendMessage({ type: 'cancel', id: t.id }) }, '取消'))
    if (t.status === 'error' && t.admin) {
      // 家人账号配对的插件不能往书库里加书：重试没用，去设置里用管理员账号重新配对
      acts.append(h('button', { class: 'link', type: 'button', onclick: () => chrome.runtime.openOptionsPage() }, '重新配对'))
    } else if (t.status === 'error') {
      acts.append(h('button', { class: 'link', type: 'button', onclick: () => chrome.runtime.sendMessage({ type: 'retry', id: t.id }) }, '重试'))
      if (t.mode !== 'nas' && !t.url.startsWith('ia:')) acts.append(h('button', { class: 'link', type: 'button', title: '让书库自己去下载这个链接', onclick: () => chrome.runtime.sendMessage({ type: 'retry', id: t.id, mode: 'nas' }) }, '让书库下载'))
      else if (t.mode === 'nas' && !t.url.startsWith('ia:')) acts.append(h('button', { class: 'link', type: 'button', title: '用浏览器下载后传到书库', onclick: () => chrome.runtime.sendMessage({ type: 'retry', id: t.id, mode: 'browser' }) }, '用浏览器下载'))
    }
    // 进行中的任务后面加上说明：连不上书库正在等、改用了另一种方式
    const extra = active ? [t.wait, t.note].filter(Boolean).join('；') : ''
    // 传上去了但没入库（认不出格式、出错）：和失败一样用红字
    const bad = t.status === 'done' && (t.ingest === 'unsupported' || t.ingest === 'error')
    const li = h('li', { class: 'task ' + (active ? 'active' : bad ? 'error' : t.status) + (active && t.wait ? ' wait' : '') },
      h('div', { class: 't-name', title: t.url }, name),
      h('div', { class: 't-line' }, h('span', { class: 's' }, statusText(t) + (extra ? `（${extra}）` : '')), acts))
    if (active) {
      const known = t.total && t.status !== 'queued'
      const bar = h('div', { class: 't-bar' + (known ? '' : ' busy') }, h('i', { style: known ? `width:${Math.min(100, (t.bytes / t.total) * 100).toFixed(1)}%` : '' }))
      li.append(bar)
    }
    return li
  }))
}
