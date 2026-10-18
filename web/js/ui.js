// 小型 DOM 工具、图标、提示、对话框、抽屉、生成封面

export function h(tag, attrs, ...children) {
  const el = document.createElement(tag)
  if (attrs) for (const [k, v] of Object.entries(attrs)) {
    if (v == null || v === false) continue
    if (k === 'class') el.className = v
    else if (k === 'style' && typeof v === 'object') {
      for (const [sk, sv] of Object.entries(v)) sk.startsWith('--') ? el.style.setProperty(sk, sv) : (el.style[sk] = sv)
    }
    else if (k === 'html') el.innerHTML = v
    else if (k.startsWith('on') && typeof v === 'function') el.addEventListener(k.slice(2).toLowerCase(), v)
    else if (k === 'dataset') Object.assign(el.dataset, v)
    else if (v === true) el.setAttribute(k, '')
    else el.setAttribute(k, v)
  }
  append(el, children)
  return el
}

function append(el, children) {
  for (const c of children.flat(Infinity)) {
    if (c == null || c === false) continue
    el.append(c instanceof Node ? c : document.createTextNode(String(c)))
  }
}

export const $ = (s, root = document) => root.querySelector(s)
export const $$ = (s, root = document) => [...root.querySelectorAll(s)]
export function clear(el) { el.replaceChildren(); return el }
// 清空后填入内容：跳过 null / false，数组自动展开（原生 append 会把 null 显示成文字）
export function fill(el, ...children) { el.replaceChildren(); append(el, children); return el }
export function add(el, ...children) { append(el, children); return el }

// 线条图标（24×24，描边）
const P = {
  shelf: '<path d="M4 4h4v16H4zM10 4h4v16h-4z"/><path d="M16.5 4.8l3.8-1 3.7 15.5-3.8 1z" transform="translate(-2 0)"/>',
  book: '<path d="M5 4.5A1.5 1.5 0 0 1 6.5 3H19v15H6.5A1.5 1.5 0 0 0 5 19.5z"/><path d="M5 19.5A1.5 1.5 0 0 0 6.5 21H19v-3"/>',
  reading: '<path d="M3 5.5c2.8-1 5.8-1 9 1.2 3.2-2.2 6.2-2.2 9-1.2V19c-2.8-1-5.8-1-9 1.2C8.8 18 5.8 18 3 19z"/><path d="M12 6.7v13.5"/>',
  comic: '<rect x="3" y="3" width="18" height="18" rx="2"/><path d="M3 11h18M11 3v8M15 11v10"/>',
  folder: '<path d="M3 6.5A1.5 1.5 0 0 1 4.5 5H9l2 2.5h8.5A1.5 1.5 0 0 1 21 9v9.5a1.5 1.5 0 0 1-1.5 1.5h-15A1.5 1.5 0 0 1 3 18.5z"/>',
  sparkle: '<path d="M12 3l1.9 5.1L19 10l-5.1 1.9L12 17l-1.9-5.1L5 10l5.1-1.9z"/><path d="M19 15l.8 2.2L22 18l-2.2.8L19 21l-.8-2.2L16 18l2.2-.8z"/>',
  check: '<path d="M4.5 12.5l5 5 10-11"/>',
  settings: '<circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.6 1.6 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.6 1.6 0 0 0-1.8-.3 1.6 1.6 0 0 0-1 1.5V21a2 2 0 0 1-4 0v-.1a1.6 1.6 0 0 0-1-1.5 1.6 1.6 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.6 1.6 0 0 0 .3-1.8 1.6 1.6 0 0 0-1.5-1H3a2 2 0 0 1 0-4h.1a1.6 1.6 0 0 0 1.5-1 1.6 1.6 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.6 1.6 0 0 0 1.8.3H9a1.6 1.6 0 0 0 1-1.5V3a2 2 0 0 1 4 0v.1a1.6 1.6 0 0 0 1 1.5 1.6 1.6 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.6 1.6 0 0 0-.3 1.8V9a1.6 1.6 0 0 0 1.5 1H21a2 2 0 0 1 0 4h-.1a1.6 1.6 0 0 0-1.5 1z"/>',
  search: '<circle cx="11" cy="11" r="7"/><path d="M20 20l-3.5-3.5"/>',
  upload: '<path d="M12 16V4M7 9l5-5 5 5"/><path d="M4 16v3a1 1 0 0 0 1 1h14a1 1 0 0 0 1-1v-3"/>',
  close: '<path d="M6 6l12 12M18 6L6 18"/>',
  back: '<path d="M15 5l-7 7 7 7"/>',
  next: '<path d="M9 5l7 7-7 7"/>',
  list: '<path d="M8 6h13M8 12h13M8 18h13M3.5 6h.01M3.5 12h.01M3.5 18h.01"/>',
  bookmark: '<path d="M6 3.5h12V21l-6-4.5L6 21z"/>',
  type: '<path d="M4 7V5h16v2M9 19h6M12 5v14"/>',
  moon: '<path d="M20 14.5A8.5 8.5 0 0 1 9.5 4 8.5 8.5 0 1 0 20 14.5z"/>',
  sun: '<circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"/>',
  trash: '<path d="M4 7h16M10 11v6M14 11v6M6 7l1 13h10l1-13M9 7V4h6v3"/>',
  download: '<path d="M12 4v12M7 11l5 5 5-5"/><path d="M4 18v2h16v-2"/>',
  undo: '<path d="M9 14L4 9l5-5"/><path d="M4 9h10.5a5.5 5.5 0 0 1 0 11H11"/>',
  refresh: '<path d="M20 11a8 8 0 1 0-2.3 5.7"/><path d="M20 4v7h-7"/>',
  edit: '<path d="M4 20h4L19 9l-4-4L4 16z"/><path d="M13.5 6.5l4 4"/>',
  grid: '<rect x="3.5" y="3.5" width="7" height="7" rx="1"/><rect x="13.5" y="3.5" width="7" height="7" rx="1"/><rect x="3.5" y="13.5" width="7" height="7" rx="1"/><rect x="13.5" y="13.5" width="7" height="7" rx="1"/>',
  expand: '<path d="M4 9V4h5M20 9V4h-5M4 15v5h5M20 15v5h-5"/>',
  plus: '<path d="M12 5v14M5 12h14"/>',
  minus: '<path d="M5 12h14"/>',
  activity: '<path d="M3 12h4l3-8 4 16 3-8h4"/>',
  wifi: '<path d="M2 8.5a15 15 0 0 1 20 0M5 12a10 10 0 0 1 14 0M8.5 15.5a5 5 0 0 1 7 0"/><circle cx="12" cy="19" r="1"/>',
  logout: '<path d="M15 4h4v16h-4M10 8l-4 4 4 4M6 12h11"/>',
  pages: '<rect x="3" y="4" width="8" height="16" rx="1"/><rect x="13" y="4" width="8" height="16" rx="1"/>',
  play: '<path d="M7 4.5v15l13-7.5z"/>',
  pause: '<path d="M7 4.5v15M17 4.5v15"/>',
  volume: '<path d="M4 9.5h3.5L12 5v14l-4.5-4.5H4z"/><path d="M15.5 9a4 4 0 0 1 0 6M18 6.5a7.5 7.5 0 0 1 0 11"/>',
  headphones: '<path d="M4 15v-3a8 8 0 0 1 16 0v3"/><rect x="3" y="14" width="4.5" height="6.5" rx="1.5"/><rect x="16.5" y="14" width="4.5" height="6.5" rx="1.5"/>',
  timer: '<circle cx="12" cy="13" r="7.5"/><path d="M12 9v4l2.5 2M9.5 2.5h5"/>',
  prevSent: '<path d="M7 5v14M18 6l-8 6 8 6z"/>',
  nextSent: '<path d="M17 5v14M6 6l8 6-8 6z"/>',
  scroll: '<rect x="6" y="2" width="12" height="20" rx="1.5"/><path d="M6 8h12M6 15h12"/>',
  compass: '<circle cx="12" cy="12" r="9"/><path d="M15.6 8.4l-2.1 5.1-5.1 2.1 2.1-5.1z"/>',
  link: '<path d="M10 14a4 4 0 0 0 5.7 0l3-3a4 4 0 0 0-5.7-5.7l-1 1"/><path d="M14 10a4 4 0 0 0-5.7 0l-3 3a4 4 0 0 0 5.7 5.7l1-1"/>',
  phone: '<rect x="7" y="2.5" width="10" height="19" rx="2"/><path d="M11 18.5h2"/>',
  watch: '<rect x="6.5" y="6.5" width="11" height="11" rx="3"/><path d="M9 6.5l.7-3.5h4.6l.7 3.5M9 17.5l.7 3.5h4.6l.7-3.5"/>',
  puzzle: '<path d="M10 4.5a2 2 0 0 1 4 0V6h3a1 1 0 0 1 1 1v3h-1.5a2 2 0 0 0 0 4H18v3a1 1 0 0 1-1 1h-3v-1.5a2 2 0 0 0-4 0V18H7a1 1 0 0 1-1-1v-3h1.5a2 2 0 0 0 0-4H6V7a1 1 0 0 1 1-1h3z"/>',
  globe: '<circle cx="12" cy="12" r="9"/><path d="M3 12h18M12 3c2.5 2.6 3.8 5.6 3.8 9s-1.3 6.4-3.8 9c-2.5-2.6-3.8-5.6-3.8-9S9.5 5.6 12 3z"/>',
  offline: '<path d="M3 3l18 18"/><path d="M8.5 16.5a5 5 0 0 1 7 0M5 13a10 10 0 0 1 3.4-2.2M19 13a10 10 0 0 0-5-2.8"/><circle cx="12" cy="20" r="1"/>',
  stack: '<path d="M12 3l9 5-9 5-9-5z"/><path d="M3 13l9 5 9-5"/>',
  heart: '<path d="M12 20s-7.5-4.6-7.5-10A4.3 4.3 0 0 1 12 7.3 4.3 4.3 0 0 1 19.5 10c0 5.4-7.5 10-7.5 10z"/>',
  star: '<path d="M12 3.5l2.6 5.4 5.9.8-4.3 4.1 1 5.9L12 16.9l-5.2 2.8 1-5.9-4.3-4.1 5.9-.8z"/>',
  select: '<rect x="3.5" y="3.5" width="17" height="17" rx="3"/><path d="M8 12.5l3 3 5-6"/>',
  notes: '<path d="M5 3.5h10l4 4V20.5H5z"/><path d="M15 3.5v4h4M8.5 12h7M8.5 16h5"/>',
  users: '<circle cx="9" cy="8.5" r="3.5"/><path d="M2.5 20a6.5 6.5 0 0 1 13 0"/><path d="M15.5 5.2a3.5 3.5 0 0 1 0 6.6M18 14.5a6.5 6.5 0 0 1 3.5 5.5"/>',
  database: '<ellipse cx="12" cy="5.5" rx="7.5" ry="2.5"/><path d="M4.5 5.5v13c0 1.4 3.4 2.5 7.5 2.5s7.5-1.1 7.5-2.5v-13M4.5 12c0 1.4 3.4 2.5 7.5 2.5s7.5-1.1 7.5-2.5"/>',
  archive: '<rect x="3" y="4" width="18" height="4.5" rx="1"/><path d="M4.5 8.5V20h15V8.5M10 12.5h4"/>',
  more: '<circle cx="5.5" cy="12" r="1.2"/><circle cx="12" cy="12" r="1.2"/><circle cx="18.5" cy="12" r="1.2"/>',
  image: '<rect x="3.5" y="4.5" width="17" height="15" rx="2"/><circle cx="9" cy="10" r="1.8"/><path d="M20.5 16l-5-5-8.5 8.5"/>',
  hdd: '<rect x="3" y="13" width="18" height="7" rx="2"/><path d="M5 13l2.5-8h9L19 13M7 16.5h.01M10.5 16.5h.01"/>',
  tag: '<path d="M3.5 12.5V4.5a1 1 0 0 1 1-1h8l8 8-9 9z"/><circle cx="8.5" cy="8.5" r="1.5"/>',
  saved: '<path d="M6 3.5h12V21l-6-4.5L6 21z"/><path d="M9 9.5l2 2 4-4"/>',
}

export function icon(name, cls) {
  const s = document.createElementNS('http://www.w3.org/2000/svg', 'svg')
  s.setAttribute('viewBox', '0 0 24 24')
  s.setAttribute('fill', 'none')
  s.setAttribute('stroke', 'currentColor')
  s.setAttribute('stroke-width', '1.6')
  s.setAttribute('stroke-linecap', 'round')
  s.setAttribute('stroke-linejoin', 'round')
  s.setAttribute('aria-hidden', 'true')
  if (cls) s.setAttribute('class', cls)
  s.innerHTML = P[name] || ''
  return s
}

export function iconBtn(name, title, onclick, extra = {}) {
  return h('button', { class: 'icon-btn', title, 'aria-label': title, type: 'button', onclick, ...extra }, icon(name))
}

// ---------- 提示 ----------

// 提示条：最多同时显示 3 条（多了先关掉最早的）。
// opts.key：同一类提示只留一条，已经有了就更新文字、重新计时（比如入库进度），不再一条条往上叠
const MAX_TOASTS = 3
export function toast(msg, opts = {}) {
  const box = document.getElementById('toasts')
  if (!box) return
  let el = opts.key ? [...box.children].find(x => x.dataset.key === opts.key) : null
  if (el) clearTimeout(el._timer)
  else {
    el = h('div', { role: 'status' })
    if (opts.key) el.dataset.key = opts.key
    box.append(el)
    while (box.children.length > MAX_TOASTS) box.firstElementChild.remove()
  }
  el.className = 'toast' + (opts.error ? ' err' : '')
  fill(el, h('span', null, msg),
    opts.action ? h('button', { onclick: () => { opts.action.run(); el.remove() } }, opts.action.label) : null)
  el._timer = setTimeout(() => el.remove(), opts.duration || (opts.action ? 7000 : 3200))
}
export const toastError = (e) => toast(e?.message || String(e), { error: true, duration: 5000 })

// ---------- 对话框 ----------

export function confirmDialog({ title, message, ok = '确定', cancel = '取消', danger }) {
  return new Promise(resolve => {
    const scrim = h('div', { class: 'scrim', style: { zIndex: 70 } })
    const close = v => {
      scrim.classList.remove('show'); dlg.classList.remove('show')
      setTimeout(() => { scrim.remove(); dlg.remove() }, 200)
      document.removeEventListener('keydown', onKey)
      resolve(v)
    }
    const okBtn = h('button', { class: 'btn ' + (danger ? 'danger' : 'primary'), onclick: () => close(true) }, ok)
    const dlg = h('div', { class: 'dialog', role: 'dialog', 'aria-modal': 'true' },
      h('h3', null, title),
      message ? h('p', null, message) : null,
      h('div', { class: 'row' }, h('button', { class: 'btn quiet', onclick: () => close(false) }, cancel), okBtn))
    const onKey = e => { if (e.key === 'Escape') close(false) }
    scrim.onclick = () => close(false)
    document.addEventListener('keydown', onKey)
    document.body.append(scrim, dlg)
    requestAnimationFrame(() => { scrim.classList.add('show'); dlg.classList.add('show'); okBtn.focus() })
  })
}

// 填几项再确定：fields = [{ key, label, type: 'text'|'number'|'password'|'select'|'checks', value, options: [[值, 文字]], placeholder, hint }]
// 返回 { key: 值 }，取消时返回 null。checks 的值是选中的值数组
export function formDialog({ title, message, fields = [], ok = '确定', cancel = '取消', danger, validate }) {
  return new Promise(resolve => {
    const scrim = h('div', { class: 'scrim', style: { zIndex: 70 } })
    const inputs = {}
    const err = h('p', { class: 'err', role: 'alert' })
    const close = v => {
      scrim.classList.remove('show'); dlg.classList.remove('show')
      setTimeout(() => { scrim.remove(); dlg.remove() }, 200)
      document.removeEventListener('keydown', onKey)
      resolve(v)
    }
    const values = () => Object.fromEntries(Object.entries(inputs).map(([k, el]) =>
      [k, Array.isArray(el) ? el.filter(c => c.checked).map(c => c.value) : el.value]))
    const rows = fields.map(f => {
      if (f.type === 'checks') {
        inputs[f.key] = (f.options || []).map(([v, l]) => h('input', { type: 'checkbox', value: v, checked: (f.value || []).map(String).includes(String(v)) }))
        return h('div', { class: 'field' }, h('span', null, f.label),
          h('div', { class: 'checks' }, inputs[f.key].map((c, i) => h('label', { class: 'check' }, c, h('span', null, f.options[i][1])))),
          f.hint ? h('small', null, f.hint) : null)
      }
      const el = f.type === 'select'
        ? h('select', { class: 'input' }, (f.options || []).map(([v, l]) => h('option', { value: v, selected: String(v) === String(f.value ?? '') }, l)))
        : h('input', { class: 'input', type: f.type || 'text', value: f.value ?? '', placeholder: f.placeholder || '', autocomplete: f.type === 'password' ? 'new-password' : 'off', step: f.type === 'number' ? 'any' : null, min: f.min ?? null })
      inputs[f.key] = el
      return h('label', { class: 'field' }, h('span', null, f.label), el, f.hint ? h('small', null, f.hint) : null)
    })
    const okBtn = h('button', { class: 'btn ' + (danger ? 'danger' : 'primary'), type: 'submit' }, ok)
    const form = h('form', { class: 'stack', style: { gap: '14px' } }, rows, err,
      h('div', { class: 'row' }, h('button', { class: 'btn quiet', type: 'button', onclick: () => close(null) }, cancel), okBtn))
    form.onsubmit = e => {
      e.preventDefault()
      const v = values()
      const msg = validate?.(v)
      if (msg) { err.textContent = msg; return }
      close(v)
    }
    const dlg = h('div', { class: 'dialog form-dialog', role: 'dialog', 'aria-modal': 'true', 'aria-label': title },
      h('h3', null, title), message ? h('p', null, message) : null, form)
    const onKey = e => { if (e.key === 'Escape') close(null) }
    scrim.onclick = () => close(null)
    document.addEventListener('keydown', onKey)
    document.body.append(scrim, dlg)
    requestAnimationFrame(() => {
      scrim.classList.add('show'); dlg.classList.add('show')
      const first = dlg.querySelector('input:not([type=checkbox]), select')
      ;(first || okBtn).focus()
    })
  })
}

// ---------- 弹出菜单（右键、长按） ----------
// items: [{ label, icon, run, danger, disabled }] 或 null（分隔线）或现成的元素。
// 有鼠标时在点击的位置弹出；手机上从底部弹出
let menuClose = null
export function openMenu(items, { x, y, title, above } = {}) {
  menuClose?.()
  const sheet = matchMedia('(hover: none), (max-width: 599px)').matches || x == null
  const scrim = h('div', { class: 'menu-scrim' + (sheet ? ' sheet' : '') })
  const menu = h('div', { class: 'ctx-menu' + (sheet ? ' sheet' : ''), role: 'menu', 'aria-label': title || '菜单' })
  if (title) menu.append(h('div', { class: 'ctx-title' }, title))
  for (const it of items) {
    if (!it) { menu.append(h('div', { class: 'ctx-sep', role: 'separator' })); continue }
    if (it instanceof Node) { menu.append(it); continue }
    menu.append(h('button', {
      type: 'button', role: 'menuitem', class: it.danger ? 'danger' : null, disabled: it.disabled,
      onclick: () => { close(); it.run?.() },
    }, it.icon ? icon(it.icon) : null, h('span', null, it.label)))
  }
  const close = () => {
    if (menuClose !== close) return
    menuClose = null
    document.removeEventListener('keydown', onKey, true)
    removeEventListener('resize', close)
    scrim.remove(); menu.remove()
  }
  const onKey = e => {
    const btns = [...menu.querySelectorAll('button:not([disabled])')]
    const i = btns.indexOf(document.activeElement)
    if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); close() }
    else if (e.key === 'ArrowDown') { e.preventDefault(); btns[(i + 1) % btns.length]?.focus() }
    else if (e.key === 'ArrowUp') { e.preventDefault(); btns[(i - 1 + btns.length) % btns.length]?.focus() }
    else if (e.key === 'Tab') { e.preventDefault(); btns[(i + (e.shiftKey ? -1 : 1) + btns.length) % btns.length]?.focus() }
  }
  scrim.onclick = close
  scrim.oncontextmenu = e => { e.preventDefault(); close() }
  document.addEventListener('keydown', onKey, true)
  addEventListener('resize', close)
  document.body.append(scrim, menu)
  if (!sheet) {
    const r = menu.getBoundingClientRect()
    menu.style.left = Math.max(8, Math.min(x, innerWidth - r.width - 8)) + 'px'
    menu.style.top = Math.max(8, Math.min(above ? y - r.height : y, innerHeight - r.height - 8)) + 'px'
  }
  requestAnimationFrame(() => { menu.classList.add('show'); menu.querySelector('button:not([disabled])')?.focus({ preventScroll: true }) })
  menuClose = close
  return close
}
export const closeMenu = () => menuClose?.()

// ---------- 抽屉 ----------

export function openDrawer({ title, body, footer, onClose }) {
  const scrim = h('div', { class: 'scrim' })
  const panel = h('aside', { class: 'drawer', role: 'dialog', 'aria-modal': 'true', 'aria-label': title })
  const close = () => {
    scrim.classList.remove('show'); panel.classList.remove('show')
    document.removeEventListener('keydown', onKey)
    setTimeout(() => { scrim.remove(); panel.remove() }, 260)
    onClose?.()
  }
  const onKey = e => { if (e.key === 'Escape' && !document.querySelector('.dialog')) close() }
  const head = h('header', null, h('h2', null, title), iconBtn('close', '关闭', close))
  const bodyEl = h('div', { class: 'body' }, body)
  panel.append(head, bodyEl)
  if (footer) panel.append(h('footer', null, footer))
  scrim.onclick = close
  document.addEventListener('keydown', onKey)
  document.body.append(scrim, panel)
  requestAnimationFrame(() => { scrim.classList.add('show'); panel.classList.add('show') })
  return { close, panel, body: bodyEl, setTitle: t => (head.firstChild.textContent = t) }
}

// ---------- 生成封面 ----------

// 低饱和的书衣颜色：靛、黛、赭、苍、檀、墨绿、绛、鸦青
const CLOTH = ['#2E4A62', '#3D5A54', '#6B4A33', '#4F5B66', '#7A5A45', '#34503F', '#6E3A3A', '#3C4250', '#5A4E6A', '#4A6070']

function hashStr(s) {
  let h = 2166136261
  for (const c of s) { h ^= c.codePointAt(0); h = Math.imul(h, 16777619) }
  return h >>> 0
}

// 汉字（不用 \p{Script=Han}：旧浏览器不认识）
const hasHan = s => /[\u2E80-\u2FDF\u3005\u3007\u3021-\u3029\u3038-\u303B\u3400-\u4DBF\u4E00-\u9FFF\uF900-\uFAFF]|[\uD840-\uD87F][\uDC00-\uDFFF]/.test(s)

export function genCover(title, author) {
  const t = (title || '未命名').trim()
  const latin = !hasHan(t)
  const len = [...t].length
  // 竖排题签：字越多字越小
  const fs = latin ? (len > 40 ? 11 : len > 22 ? 13 : 15) : (len <= 4 ? 17 : len <= 7 ? 15 : len <= 10 ? 12.5 : 10.5)
  const display = latin ? t : [...t].slice(0, 16).join('')
  return h('div', { class: 'gen' + (latin ? ' latin' : ''), style: { '--c1': CLOTH[hashStr(t) % CLOTH.length], '--fs': fs + 'px' } },
    h('div', { class: 'slip' }, h('b', null, display)),
    author ? h('div', { class: 'by' }, author.split(/\s*\/\s*/)[0]) : null)
}

export function coverEl(book, apiUrl, opts = {}) {
  const el = h('div', { class: 'cv' })
  if (book.cover) {
    const img = h('img', { src: apiUrl(`/api/books/${book.id}/cover?${book.cover}`), alt: '', loading: 'lazy', decoding: 'async' })
    img.onerror = () => img.replaceWith(genCover(book.title, book.author))
    el.append(img)
  } else el.append(genCover(book.title, book.author))
  if (opts.flags !== false) {
    if (book.status === 'review') el.append(h('span', { class: 'flag' }, '待确认'))
    if (book.read_state === 'done') el.append(h('span', { class: 'done', title: '已读完' }, icon('check')))
    const f = (book.formats || '').split(',')[0]
    if (f && opts.format !== false) el.append(h('span', { class: 'fmt' }, f.toUpperCase()))
  }
  return el
}

// ---------- 格式化 ----------

export function fmtBytes(n) {
  if (!n) return '0 B'
  const u = ['B', 'KB', 'MB', 'GB']
  let i = 0
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++ }
  return (i ? n.toFixed(n < 10 ? 1 : 0) : n) + ' ' + u[i]
}

export function fmtTime(ts) {
  if (!ts) return ''
  const d = new Date(ts * 1000), now = new Date()
  const diff = (now - d) / 1000
  if (diff < 60) return '刚刚'
  if (diff < 3600) return Math.floor(diff / 60) + ' 分钟前'
  if (diff < 86400 && d.getDate() === now.getDate()) return d.toTimeString().slice(0, 5)
  if (diff < 86400 * 2) return '昨天 ' + d.toTimeString().slice(0, 5)
  if (d.getFullYear() === now.getFullYear()) return `${d.getMonth() + 1}月${d.getDate()}日`
  return `${d.getFullYear()}年${d.getMonth() + 1}月${d.getDate()}日`
}

export const pct = f => Math.round((f || 0) * 100) + '%'

// 完整的日期时间：2026年10月18日 03:12
export function fmtDateTime(ts) {
  if (!ts) return ''
  const d = new Date(ts * 1000)
  return `${d.getFullYear()}年${d.getMonth() + 1}月${d.getDate()}日 ${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
}

// 用登录令牌下载文件（备份、笔记导出）：带上令牌的链接，浏览器自己保存
export function downloadLink(url, name) {
  const a = h('a', { href: url, download: name || '' })
  document.body.append(a); a.click(); a.remove()
}

export function debounce(fn, ms) {
  let t
  return (...a) => { clearTimeout(t); t = setTimeout(() => fn(...a), ms) }
}

export function store(key, def) {
  try { return { ...def, ...JSON.parse(localStorage.getItem(key) || '{}') } } catch { return { ...def } }
}
export function save(key, v) {
  try { localStorage.setItem(key, JSON.stringify(v)) } catch {}
}
