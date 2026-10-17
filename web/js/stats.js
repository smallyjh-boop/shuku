// 阅读统计：今天 / 本周 / 连续天数 / 今年读完，近 30 天每天阅读时长，最近读完，读得最多

import { h, fill, add, coverEl, fmtTime, pct } from './ui.js'
import { api, apiUrl } from './api.js'

export const fmtDur = (s) => {
  s = Math.round(s || 0)
  if (s < 60) return s ? '不到 1 分钟' : '0 分钟'
  const m = Math.round(s / 60)
  if (m < 60) return `${m} 分钟`
  const hr = Math.floor(m / 60), mm = m % 60
  return mm ? `${hr} 小时 ${mm} 分` : `${hr} 小时`
}

export async function renderStats(main, ctx) {
  const box = h('div', { class: 'stats' }, h('p', { class: 'muted' }, '加载中…'))
  add(main, h('div', { class: 'page-head' }, h('div', null, h('h1', null, '阅读统计'), h('div', { class: 'sub' }, '每次翻开书都会自动记录时长，电脑、手机、手表一起算'))), box)
  let s
  try { s = await api('/api/stats') } catch (e) { fill(box, h('p', null, e.message)); return }
  const tile = (label, value, note) => h('div', { class: 'tile' }, h('div', { class: 'v' }, value), h('div', { class: 'l' }, label), note ? h('div', { class: 'n' }, note) : null)
  fill(box,
    h('div', { class: 'tiles' },
      tile('今天', fmtDur(s.today)),
      tile('本周', fmtDur(s.week)),
      tile('连续阅读', s.streak + ' 天', s.streak ? '每天读满 1 分钟就算' : '今天读一会儿就开始计数'),
      tile('今年读完', s.finished_year + ' 本', `累计 ${fmtDur(s.total)}`)),
    chart(s.days.slice(-30), Math.min(920, box.clientWidth || main.clientWidth || 720)),
    s.top.length ? h('div', { class: 'stat-sec' }, h('h2', null, '最近 30 天读得最多'), bookRows(s.top, ctx, b => fmtDur(b.seconds))) : null,
    s.finished.length ? h('div', { class: 'stat-sec' }, h('h2', null, '最近读完'), bookRows(s.finished, ctx, b => `${fmtTime(b.at)} 读完${b.seconds >= 60 ? ' · 共读了 ' + fmtDur(b.seconds) : ''}`)) : null,
    !s.total ? h('div', { class: 'empty' }, h('h3', null, '还没有阅读记录'), h('p', null, '打开一本书读几分钟，这里就会出现你的阅读时长。')) : null)
}

function bookRows(list, ctx, meta) {
  return h('div', { class: 'stat-books' }, list.map(b => h('div', { class: 'stat-book', role: 'button', tabindex: 0, onclick: () => ctx.navigate('#/read/' + b.id), onkeydown: e => e.key === 'Enter' && ctx.navigate('#/read/' + b.id) },
    coverEl({ ...b, formats: '' }, apiUrl, { flags: false }),
    h('div', { class: 'grow' }, h('div', { class: 't' }, b.title), h('div', { class: 'a' }, b.author || ''), h('div', { class: 'm' }, meta(b))),
    b.percent > 0 && b.percent < 1 ? h('span', { class: 'muted small-text' }, pct(b.percent)) : null)))
}

// 近 30 天每天阅读分钟数：单系列柱状图，悬停看具体时长
function chart(days, width) {
  const W = Math.max(300, Math.round(width)), H = W < 500 ? 180 : 220, padL = 34, padR = 6, padT = 12, padB = 26
  const mins = days.map(d => d.seconds / 60)
  const max = Math.max(...mins, 10)
  const step = niceStep(max)
  const top = Math.ceil(max / step) * step
  const iw = W - padL - padR, ih = H - padT - padB
  const slot = iw / days.length
  const bw = Math.min(16, slot - 2)
  const y = v => padT + ih - (v / top) * ih
  const NS = 'http://www.w3.org/2000/svg'
  const el = (tag, attrs, text) => {
    const e = document.createElementNS(NS, tag)
    for (const [k, v] of Object.entries(attrs)) e.setAttribute(k, v)
    if (text != null) e.textContent = text
    return e
  }
  const svg = el('svg', { viewBox: `0 0 ${W} ${H}`, class: 'bars', role: 'img', 'aria-label': `近 30 天每天阅读时长，合计 ${fmtDur(days.reduce((a, d) => a + d.seconds, 0))}` })
  for (let v = 0; v <= top; v += step) {
    svg.append(el('line', { x1: padL, x2: W - padR, y1: y(v), y2: y(v), class: 'grid' }))
    svg.append(el('text', { x: padL - 6, y: y(v) + 4, class: 'tick', 'text-anchor': 'end' }, String(v)))
  }
  const tip = h('div', { class: 'tip', hidden: true })
  days.forEach((d, i) => {
    const x = padL + i * slot + (slot - bw) / 2
    const v = mins[i]
    const g = el('g', { class: 'bar', tabindex: 0 })
    if (v > 0) {
      const hgt = Math.max(2, ih - (y(v) - padT))
      const r = Math.min(4, bw / 2, hgt)
      const x0 = x, x1 = x + bw, yb = padT + ih, yt = yb - hgt
      g.append(el('path', { d: `M${x0},${yb}V${yt + r}Q${x0},${yt} ${x0 + r},${yt}H${x1 - r}Q${x1},${yt} ${x1},${yt + r}V${yb}Z`, class: 'mark' }))
    }
    // 命中区比柱子大，空白的日子也能悬停
    g.append(el('rect', { x: padL + i * slot, y: padT, width: slot, height: ih, class: 'hit' }))
    const date = new Date(d.day + 'T00:00:00')
    const label = `${date.getMonth() + 1}月${date.getDate()}日`
    const show = () => {
      tip.hidden = false
      tip.textContent = `${label}　${d.seconds ? fmtDur(d.seconds) : '没有阅读'}`
      const box = svg.getBoundingClientRect()
      const k = box.width / W
      tip.style.left = Math.min(box.width - 150, Math.max(0, (x + bw / 2) * k - 70)) + 'px'
      tip.style.top = Math.max(0, y(v) * k - 40) + 'px'
      g.classList.add('on')
    }
    const hide = () => { tip.hidden = true; g.classList.remove('on') }
    g.addEventListener('pointerenter', show); g.addEventListener('pointerleave', hide)
    g.addEventListener('focus', show); g.addEventListener('blur', hide)
    svg.append(g)
    if ((days.length - 1 - i) % (W < 500 ? 10 : 7) === 0) {
      svg.append(el('text', { x: x + bw / 2, y: H - 8, class: 'tick', 'text-anchor': 'middle' }, label))
    }
  })
  const total = days.reduce((a, d) => a + d.seconds, 0)
  const active = days.filter(d => d.seconds >= 60).length
  return h('div', { class: 'stat-sec' },
    h('h2', null, '近 30 天'),
    h('p', { class: 'muted small-text', style: { margin: '-6px 0 12px' } }, `共 ${fmtDur(total)}，有 ${active} 天在读，单位：分钟`),
    h('div', { class: 'chart' }, svg, tip),
    h('table', { class: 'sr-only' }, h('caption', null, '近 30 天每天阅读时长'), days.map(d => h('tr', null, h('td', null, d.day), h('td', null, fmtDur(d.seconds))))))
}

function niceStep(max) {
  for (const s of [5, 10, 15, 30, 60, 120, 180, 240]) if (max / s <= 4) return s
  return 300
}
