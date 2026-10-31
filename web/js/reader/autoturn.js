// 自动翻页：照着这个人平时的阅读速度自动翻到下一页，用得越多越准。
//
// 学习：平时自己往后翻一页（点、滑、按键、音量键），记下这一页停了多久、这一页有多少字，
// 算出“每 100 字读几秒”（PDF、漫画按“每页几秒”）。太快（扫一眼就翻）、太久（走开了）、页面在后台、
// 开着工具栏或设置、在听书的都不算。按书的种类（书、PDF、漫画）分开记，记在这台设备上（每台设备的屏幕、
// 字号不一样，读得也不一样快），按人分开。
//
// 自动翻：这一页字多就停久一点、字少就翻得快一点。开着的时候：
//   - 嫌慢自己先翻了：这一页的时间也学进去（下一页就快一些）
//   - 刚自动翻过去马上又翻回来：说明翻得太快，以后慢一点
//   - “快一点 / 慢一点”按钮直接调
// 叫出工具栏、打开设置和目录、查字典、选字、听书、切到后台时暂停计时，回来接着算（不从头算）。
// 墨水屏上不显示倒计时（每秒刷新会闪），只显示“每页约几秒”。

import { h, icon, iconBtn, toast, store, save, isEInk } from '../ui.js'
import { call } from '../native.js'

const KEY = uid => 'shuku.autoturn.' + uid
// 没学过时的默认值：中文大约每分钟 300 字（100 字 20 秒）；PDF 一页 60 秒；漫画一页 10 秒
const DEFAULTS = { flow: { cpc: 20, spp: 45 }, pdf: { spp: 60 }, comic: { spp: 10 }, 'comic-fxl': { spp: 12 } }
const MIN_SEC = 2, MAX_SEC = 15 * 60

// 一个样本是不是正常读完一页：比现在估计的快三倍、慢三倍以上的不算（头几次只去掉明显不对的）
export function plausible(sec, est, n) {
  if (!(sec >= 1.5) || sec > 10 * 60) return false
  if (!(n >= 3) || !(est > 0)) return true
  return sec >= est / 3 && sec <= est * 3
}

// 学一页：返回新的模型（不改原来的）。chars：这一页的字数（不知道就是 0）；weight：这次样本的分量
export function learn(m, sec, chars, weight) {
  m = { ...m }
  const n = m.n || 0
  const a = weight || (n < 5 ? 0.5 : 0.2) // 头几次学得快，之后慢慢修正
  m.spp = m.spp ? m.spp * (1 - a) + sec * a : sec
  if (chars >= 40) {
    const cpc = sec / chars * 100
    m.cpc = m.cpc ? m.cpc * (1 - a) + cpc * a : cpc
    m.nc = (m.nc || 0) + 1
  }
  m.n = n + 1
  return m
}

// 这一页该停多久（秒）
export function predict(m, kind, chars) {
  const d = DEFAULTS[kind] || DEFAULTS.flow
  let sec
  if (chars >= 40 && (m.cpc || d.cpc) && (m.nc >= 2 || !m.spp)) sec = chars / 100 * (m.cpc || d.cpc)
  else sec = m.spp || d.spp
  sec *= m.adj || 1
  return Math.max(MIN_SEC, Math.min(MAX_SEC, sec))
}

const fmtSec = s => s < 60 ? Math.round(s) + ' 秒' : (Math.round(s / 6) / 10) + ' 分钟'

export class AutoTurn {
  constructor(shell) {
    this.shell = shell
    this.on = false
    this.page = null       // 当前这一页：{ at, frac, pos, chars, elapsed, tainted }
    this.lastAuto = 0      // 上次自动翻页的时间
    this.autoFrac = null   // 自动翻页前的位置（看用户是不是马上翻回去）
    this.buildUI()
    const vis = () => { if (document.hidden && this.page) this.page.tainted = true }
    document.addEventListener('visibilitychange', vis)
    this.offVis = () => document.removeEventListener('visibilitychange', vis)
    // 每秒数一下这一页真正在看的时间（工具栏、面板开着、在后台时不算）：学习和自动翻页都用它
    this.timer = setInterval(() => this.tick(), 1000)
  }

  get kind() { return this.shell.kind || 'flow' }
  get all() { return store(KEY(this.shell.uid), {}) }
  get model() { return this.all[this.kind] || {} }
  setModel(m) { const a = this.all; a[this.kind] = m; save(KEY(this.shell.uid), a) }

  buildUI() {
    this.label = h('span')
    this.pauseBtn = iconBtn('pause', '暂停自动翻页', () => this.togglePause())
    this.chip = h('div', { class: 'r-auto', hidden: true, role: 'status' },
      icon('autoturn'), this.label,
      iconBtn('minus', '慢一点', () => this.adjust(1.15)),
      iconBtn('plus', '快一点', () => this.adjust(1 / 1.15)),
      this.pauseBtn,
      iconBtn('close', '关掉自动翻页', () => this.stop()))
    for (const ev of ['click', 'pointerdown']) this.chip.addEventListener(ev, e => e.stopPropagation())
    this.shell.root.append(this.chip)
    // 看书时（工具栏收起）只在底下的状态栏（页边空白里）显示一小行，不压在正文上；
    // 点屏幕中间叫出工具栏时才出现上面那条带“－ ＋ 暂停 关掉”的按钮（这时也不计时）
    this.mini = h('span', { class: 'r-auto-mini', hidden: true })
    this.shell.statusR?.before(this.mini)
  }

  // ---------- 每翻到一页（Reader.onRelocate 调用） ----------
  // act：这次翻页之前最后一次操作的时间（点、滑、按键）
  onPage({ fraction, units }, act) {
    const now = Date.now()
    const prev = this.page
    const auto = now - this.lastAuto < 1500 // 这次是自动翻的
    const chars = this.shell.engine?.pageChars?.() || 0
    const pos = units?.pos
    this.page = { at: now, frac: fraction || 0, pos, chars, elapsed: 0, tainted: false }
    if (!prev) return this.showLabel()
    const d = (fraction || 0) - prev.frac
    // 往后翻了一页（不是跳章、拖进度条）
    const step = pos != null && prev.pos != null ? pos - prev.pos : null
    const forward = d > 0 && (step == null ? d < 0.03 : step <= Math.max(2, (units?.jump || 20) / 5))
    const manual = !auto && now - (act || 0) < 4000
    // 刚自动翻过去，十秒内自己翻回来了：翻得太快
    if (this.on && d < 0 && this.autoFrac != null && now - this.lastAuto < 10000 && Math.abs(fraction - this.autoFrac) < 0.03) {
      this.autoFrac = null
      this.adjust(1.12, true)
      return this.showLabel()
    }
    if (forward && manual && !prev.tainted && !this.busy()) {
      const sec = prev.elapsed
      const est = predict(this.model, this.kind, prev.chars)
      if (plausible(sec, est, this.model.n)) {
        // 开着自动翻页还自己先翻：说明嫌慢，这次分量大一点
        this.setModel(learn(this.model, sec / (this.model.adj || 1), prev.chars, this.on && sec < est ? 0.35 : 0))
      }
    }
    this.showLabel()
    if (this.on && fraction >= 0.995) { this.stop(); toast('读到最后一页了，自动翻页已关掉', { duration: 3000 }) }
  }

  // 现在不该计时：页面在后台、工具栏或面板开着、查字典、选字、在听书
  busy() {
    const s = this.shell
    if (document.hidden || s.destroyed || !s.engine) return true
    if (s.root.classList.contains('ui') || s.root.classList.contains('overlay')) return true
    if (s.popOpen || s.notes?.open || s.dict?.open || s.trOpen) return true
    if (s.narrator?.active) return true
    return false
  }

  // ---------- 开、关 ----------
  toggle() { this.on ? this.stop() : this.start() }

  start() {
    if (!this.shell.engine) return
    if (this.shell.narrator?.active) { toast('正在听书，听书会自己翻页', { duration: 2000 }); return }
    this.on = true
    this.paused = false
    if (this.page) { this.page.elapsed = 0; this.page.tainted = false }
    this.chip.hidden = false
    this.mini.hidden = false
    this.shell.root.classList.add('autoturn')
    this.shell.showUI(false)
    // 阅读时屏幕一直亮着（App 里平时 10 分钟没碰屏幕就交给系统锁屏；浏览器用 Wake Lock）
    call('keepAwake', true)
    this.wakeAt = 0
    this.lockScreen()
    this.showLabel()
    const m = this.model
    toast((m.n >= 3 ? '自动翻页：按你平时的速度翻。' : '自动翻页：还在学你的速度，自己翻页也会学。') + '点屏幕中间可以调快慢、暂停、关掉', { duration: 3500 })
  }

  stop() {
    if (!this.on) return
    this.on = false
    this.chip.hidden = true
    this.mini.hidden = true
    this.shell.root.classList.remove('autoturn')
    call('keepAwake', false)
    try { this.wake?.release?.() } catch {}
    this.wake = null
    this.shell.syncAutoTool?.()
  }

  togglePause() {
    this.paused = !this.paused
    this.pauseBtn.replaceChildren(icon(this.paused ? 'play' : 'pause'))
    this.pauseBtn.title = this.paused ? '继续自动翻页' : '暂停自动翻页'
    this.pauseBtn.setAttribute('aria-label', this.pauseBtn.title)
    this.showLabel()
  }

  async lockScreen() {
    if (!navigator.wakeLock || document.hidden || Date.now() - (this.wakeAt || 0) < 30000) return
    this.wakeAt = Date.now()
    try {
      const w = await navigator.wakeLock.request('screen')
      if (!this.on) { w.release(); return }
      this.wake = w
      w.addEventListener?.('release', () => { if (this.wake === w) this.wake = null })
    } catch {}
  }

  // 快一点 / 慢一点：乘到这种书的速度上，以后也按这个
  adjust(f, quiet) {
    const m = { ...this.model }
    m.adj = Math.max(0.3, Math.min(3, (m.adj || 1) * f))
    this.setModel(m)
    this.showLabel()
    if (!quiet) toast(f > 1 ? '慢一点' : '快一点', { duration: 900 })
    else toast('翻得太快了？以后慢一点', { duration: 1500 })
  }

  tick() {
    if (!this.page || this.busy()) return
    if (!this.on) { this.page.elapsed++; return }
    // 切回来时浏览器的 Wake Lock 会自动放掉，重新要一次
    if (!this.wake && !document.hidden) this.lockScreen()
    if (this.paused) return
    this.page.elapsed++
    const need = predict(this.model, this.kind, this.page.chars)
    if (this.page.elapsed >= need) this.turn()
    else if (!isEInk()) this.mini.textContent = `自动翻页 · ${Math.ceil(need - this.page.elapsed)} 秒`
  }

  async turn() {
    const before = this.page.frac
    this.lastAuto = Date.now()
    this.autoFrac = before
    this.page.elapsed = 0
    try { await this.shell.engine.next() } catch {}
    // 翻不动了（到最后一页）
    setTimeout(() => {
      if (this.on && this.page && this.page.frac === before && Date.now() - this.page.at > 1500) { this.stop(); toast('到最后一页了，自动翻页已关掉', { duration: 3000 }) }
    }, 1600)
  }

  showLabel() {
    if (!this.on) return
    const sec = predict(this.model, this.kind, this.page?.chars || 0)
    this.label.textContent = this.paused ? '自动翻页 · 暂停' : `自动翻页 · 这页约 ${fmtSec(sec)}`
    this.mini.textContent = this.paused ? '自动翻页 · 暂停' : `自动翻页 · ${fmtSec(sec)}`
  }

  // 阅读设置里的说明：学了多少页、现在多快
  summary() {
    const m = this.model
    if (!(m.n > 0)) return '还没学过：先自己翻几页，或者开着自动翻页用“－”“＋”调'
    const parts = [`已经学了 ${m.n} 页`]
    if (m.cpc && this.kind === 'flow') parts.push(`大约每分钟 ${Math.round(6000 / (m.cpc * (m.adj || 1)))} 字`)
    else if (m.spp) parts.push(`大约每页 ${fmtSec(m.spp * (m.adj || 1))}`)
    return parts.join('，')
  }

  forget() {
    const a = this.all
    delete a[this.kind]
    save(KEY(this.shell.uid), a)
    this.showLabel()
    toast('已清除，重新学习你的阅读速度', { duration: 2000 })
  }

  destroy() { this.stop(); clearInterval(this.timer); this.offVis(); this.chip.remove(); this.mini.remove() }
}
