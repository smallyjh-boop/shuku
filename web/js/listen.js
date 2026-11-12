// 听书播放器（1.0.0.32）：音乐播放器式的界面，按书库的“听书稿”一句一句放，和阅读器的排版、翻页无关。
//
// 语音来自书库的“听书包”（server/listen.go）：书库在空闲时提前合成好（微软 → 电脑加速器 → NAS 本地），
// 一律 1 倍速，这里播放时自己变速（保持音调）。浏览器里直接放书库的语音；安卓 App 里交给原生的后台服务，
// 它把后面的语音下载到手机上再放（熄屏、断网都能接着听）。
// 播放时每分钟告诉书库读到哪了，书库从这里往后保持准备好至少 30 分钟（网络不好时多准备）。

import { h, fill, icon, iconBtn, toast, toastError, store, save, openMenu } from './ui.js'
import { api, apiUrl, net, deviceName } from './api.js'
import { isNative, call, callJSON, onNative } from './native.js'
import { putProgress } from './reader/sync.js'

const PREFS_KEY = 'shuku.tts' // 和阅读器里的听书共用：语速、每种语言选的音色
const SPEEDS = [0.8, 1, 1.2, 1.4, 1.6, 1.8, 2, 2.5]
const TIMERS = [[0, '不定时'], [15, '15 分钟'], [30, '30 分钟'], [60, '60 分钟'], [-1, '读完本章']]
const KBPS = 48 // 微软的语音大约 48kbps：1 分钟约 0.36MB

let current = null

export function closeListen() { current?.destroy(); current = null }

export async function openListen(id, { ctx, params, back }) {
  closeListen()
  current = new ListenPlayer(id, ctx, params, back)
  await current.start()
}

// 设备选过的音色（每种语言）：书库按它合成、按它找准备好的语音
function voicesParam() {
  const p = store(PREFS_KEY, {})
  const v = Object.assign({}, p.voices || {})
  if (p.voice && !v.zh) v.zh = p.voice
  for (const k of Object.keys(v)) if (!v[k]) delete v[k]
  return JSON.stringify(v)
}

const fmtDur = s => {
  s = Math.max(0, Math.round(s || 0))
  if (s < 60) return s + ' 秒'
  const m = Math.round(s / 60)
  if (m < 60) return m + ' 分钟'
  return Math.floor(m / 60) + ' 小时' + (m % 60 ? ' ' + (m % 60) + ' 分' : '')
}
const fmtMB = b => b >= 1 << 30 ? (b / (1 << 30)).toFixed(1) + 'GB' : Math.max(1, Math.round(b / (1 << 20))) + 'MB'

// 准备一段要多久：按书库现在能用的合成方式的速度（合成 1 秒语音要几秒）
export function prepareHint(secs, engines) {
  const e = engines || {}
  let how, rtf
  if (e.ms_ok) { how = '微软'; rtf = e.ms > 0 ? e.ms : 0.15 }
  else if (e.pc) { how = '电脑加速'; rtf = e.pc }
  else if (e.nas) { how = 'NAS 本地'; rtf = e.nas }
  else return `准备 ${fmtDur(secs)}：现在没有能用的合成方式（微软连不上，电脑、NAS 本地语音都没开）`
  return `准备 ${fmtDur(secs)}：约需 ${fmtDur(secs * rtf)}（${how}）· 占 ${fmtMB(secs * KBPS * 125)} · 可听 ${fmtDur(secs)}`
}

class ListenPlayer {
  constructor(id, ctx, params, back) {
    this.id = id
    this.ctx = ctx
    this.params = params
    this.back = back
    this.prefs = store(PREFS_KEY, { speed: 1 })
    this.voices = voicesParam()
    this.chapters = new Map() // 章 → 句子
    this.pos = { n: 0, i: 0 }
    this.playing = false
    this.timer = 0
    this.timerEnd = 0
    this.cleanups = []
    this.native = isNative && typeof window.ShukuNative?.tts === 'function'
    this.audio = this.native ? null : new Audio()
    this.listenSecs = 0
  }

  get speed() { return this.prefs.speed || 1 }
  q(extra) {
    const p = new URLSearchParams({ voices: this.voices, ...(extra || {}) })
    return '?' + p.toString()
  }

  // ---------- 界面 ----------
  build() {
    this.titleEl = h('div', { class: 'ttl' })
    this.chapEl = h('div', { class: 'lp-chap' })
    this.cover = h('div', { class: 'lp-cover' })
    this.lines = h('div', { class: 'lp-lines', role: 'list', 'aria-label': '正文' })
    this.progEl = h('div', { class: 'lp-prog' })
    this.playBtn = iconBtn('play', '播放', () => this.toggle(), { class: 'icon-btn lp-play' })
    this.speedBtn = h('button', { class: 'lp-pill', type: 'button', onclick: e => this.pickSpeed(e) }, '1.0×')
    this.timerBtn = h('button', { class: 'lp-pill', type: 'button', onclick: e => this.pickTimer(e) }, icon('timer'), h('span', null, '定时'))
    this.readyEl = h('div', { class: 'lp-ready' })
    this.hintEl = h('div', { class: 'lp-hint' })
    this.prepBox = h('div', { class: 'lp-prep', hidden: true })
    this.stateEl = h('div', { class: 'lp-state' })
    const top = h('div', { class: 'lp-top' },
      iconBtn('back', '返回', () => this.back()),
      this.titleEl,
      iconBtn('list', '目录', e => this.pickChapter(e)),
      iconBtn('reading', '回到阅读', () => this.toReader()))
    const controls = h('div', { class: 'lp-controls' },
      iconBtn('prevSent', '上一章', () => this.jumpChapter(-1), { class: 'icon-btn lp-ch' }),
      iconBtn('back', '上一句', () => this.step(-1)),
      this.playBtn,
      iconBtn('next', '下一句', () => this.step(1)),
      iconBtn('nextSent', '下一章', () => this.jumpChapter(1), { class: 'icon-btn lp-ch' }))
    const opts = h('div', { class: 'lp-opts' }, this.speedBtn, this.timerBtn,
      h('button', { class: 'lp-pill', type: 'button', onclick: () => this.togglePrep() }, icon('download'), h('span', null, '提前准备')))
    this.root = h('div', { class: 'listen' },
      top,
      h('div', { class: 'lp-head' }, this.cover, h('div', { class: 'lp-meta' }, h('b', { class: 'lp-title' }), h('span', { class: 'lp-author' }), this.chapEl)),
      this.lines,
      h('div', { class: 'lp-foot' }, this.progEl, this.stateEl, controls, opts, this.readyEl, this.prepBox))
    document.body.append(this.root)
    document.body.style.overflow = 'hidden'
    const key = e => {
      if (e.target.closest?.('input, textarea, select')) return
      if (e.key === ' ') { e.preventDefault(); this.toggle() }
      else if (e.key === 'ArrowRight') this.step(1)
      else if (e.key === 'ArrowLeft') this.step(-1)
      else if (e.key === 'Escape') this.back()
    }
    document.addEventListener('keydown', key)
    this.cleanups.push(() => document.removeEventListener('keydown', key))
  }

  // ---------- 打开 ----------
  async start() {
    this.build()
    fill(this.lines, h('p', { class: 'muted lp-loading' }, '正在打开…'))
    const extra = {}
    if (this.params?.get('loc')) { extra.loc = this.params.get('loc'); extra.pct = this.params.get('pct') || '0' }
    try {
      this.info = await api(`/api/books/${this.id}/listen${this.q(extra)}`)
    } catch (e) {
      fill(this.lines, h('p', { class: 'err' }, e.message, h('div', { style: { marginTop: '12px' } }, h('button', { class: 'btn', onclick: () => this.back() }, '返回'))))
      return
    }
    if (this.destroyed) return
    const info = this.info
    document.title = '听书 · ' + info.title
    this.titleEl.textContent = '听书'
    this.root.querySelector('.lp-title').textContent = info.title
    this.root.querySelector('.lp-author').textContent = info.author || ''
    if (info.has_cover) fill(this.cover, h('img', { src: apiUrl(`/api/books/${this.id}/cover?w=240`), alt: '' }))
    this.pos = { n: info.resume?.n || 0, i: info.resume?.i || 0 }
    await this.showChapter(this.pos.n)
    this.updateUI()
    this.drawReady(info.status, info.engines)
    if (this.native) this.attachNative()
    else this.attachBrowser()
    // 每分钟：告诉书库读到哪了（准备任务从这里往后）、看看准备了多少
    this.tick = setInterval(() => this.heartbeat(), 60000)
    this.cleanups.push(() => clearInterval(this.tick))
    this.heartbeat()
  }

  async sentences(n) {
    if (!this.chapters.has(n)) {
      const r = await api(`/api/books/${this.id}/listen/${n}${this.q()}`)
      this.chapters.set(n, r.sentences || [])
      if (this.chapters.size > 6) this.chapters.delete(this.chapters.keys().next().value)
    }
    return this.chapters.get(n)
  }

  async showChapter(n) {
    const list = await this.sentences(n)
    if (this.destroyed) return
    this.shownN = n
    const ch = this.info.chapters[n] || {}
    this.chapEl.textContent = `${ch.title || ''}`
    fill(this.lines, list.map(s => h('p', {
      class: 'lp-line', role: 'listitem', dataset: { i: s.i }, tabindex: 0,
      onclick: () => this.jump({ n, i: s.i }, true),
    }, s.text)))
    this.highlight()
  }

  highlight() {
    if (this.shownN !== this.pos.n) return
    this.lines.querySelector('.lp-line.cur')?.classList.remove('cur')
    const el = this.lines.querySelector(`.lp-line[data-i="${this.pos.i}"]`)
    if (el) {
      el.classList.add('cur')
      // 当前句保持在中间偏上
      const top = el.offsetTop - this.lines.clientHeight * 0.35
      this.lines.scrollTop = Math.max(0, top)
    }
  }

  updateUI() {
    this.playBtn.replaceChildren(icon(this.playing ? 'pause' : 'play'))
    this.playBtn.title = this.playing ? '暂停' : '播放'
    this.speedBtn.textContent = this.speed.toFixed(1) + '×'
    this.timerBtn.classList.toggle('on', !!this.timer)
    this.timerBtn.querySelector('span').textContent = this.timer === -1 ? '读完本章' : this.timer > 0 ? Math.max(1, Math.ceil((this.timerEnd - Date.now()) / 60000)) + ' 分' : '定时'
    const info = this.info
    if (!info) return
    const ch = info.chapters[this.pos.n] || {}
    const list = this.chapters.get(this.pos.n) || []
    const s = list[this.pos.i]
    const pct = s ? Math.round((s.pct || 0) * 1000) / 10 : 0
    this.progEl.textContent = `第 ${this.pos.n + 1} / ${info.chapters.length} 章 · ${ch.count ? (this.pos.i + 1) + ' / ' + ch.count + ' 句' : ''} · 全书 ${pct}%`
    if ('mediaSession' in navigator && !this.native) navigator.mediaSession.playbackState = this.playing ? 'playing' : 'paused'
  }

  setState(t) { this.stateEl.textContent = t || '' }

  // 准备情况：从读到的地方往后准备好了多少、书库在用什么合成
  drawReady(st, engines) {
    if (!st) return
    const how = { ms: '微软', pc: '电脑加速', nas: 'NAS 本地' }[this.job?.engine] || ''
    const parts = []
    if (this.nativeAhead != null) parts.push(`手机里已存 ${fmtDur(this.nativeAhead)}`)
    parts.push(st.ready_secs > 0 ? `书库已备好 ${fmtDur(st.ready_secs)}` : '书库还没开始准备')
    if (this.job && !this.job.finished && st.missing > 0) parts.push(`正在准备${how ? '（' + how + '）' : ''}`)
    else if (this.job?.finished) parts.push('准备完了')
    if (this.job?.last_error) parts.push(this.job.last_error)
    this.readyEl.textContent = parts.join(' · ')
    const left = Math.max(0, (st.target_secs || 30 * 60) - (st.ready_secs || 0))
    this.hintEl.textContent = left > 30 ? prepareHint(left, engines) : ''
  }

  // ---------- 提前准备（手动选范围） ----------
  togglePrep() {
    this.prepBox.hidden = !this.prepBox.hidden
    if (this.prepBox.hidden) return
    const opt = (label, body) => h('button', { class: 'btn small', type: 'button', onclick: () => this.prepare(body) }, label)
    const total = this.info?.total_secs || 0
    fill(this.prepBox,
      h('div', { class: 'lp-prep-t' }, '让书库从这里往后提前合成好（空闲时做：先用微软，连不上用电脑加速，再用 NAS 本地）：'),
      h('div', { class: 'row wrap', style: { gap: '6px' } },
        opt('1 小时', { scope: 'minutes', minutes: 60 }), opt('3 小时', { scope: 'minutes', minutes: 180 }),
        opt('10 小时', { scope: 'minutes', minutes: 600 }), opt('半本', { scope: 'half' }), opt('整本', { scope: 'all' }),
        this.job && this.job.scope !== 'auto' ? h('button', { class: 'btn small quiet', type: 'button', onclick: () => this.cancelPrep() }, '取消') : null),
      h('div', { class: 'lp-prep-t muted' }, `整本大约能听 ${fmtDur(total)}，占 ${fmtMB(total * KBPS * 125)}。`),
      this.hintEl)
  }

  async prepare(body) {
    try {
      const r = await api(`/api/books/${this.id}/listen/prepare`, { method: 'POST', body: { ...body, voices: this.voices, from: this.pos } })
      this.job = r.job
      this.drawReady(r.status, r.engines)
      const want = body.scope === 'all' ? '整本' : body.scope === 'half' ? '半本' : fmtDur(body.minutes * 60)
      toast(`书库开始准备${want}。${prepareHint(Math.max(0, r.status.target_secs - r.status.ready_secs), r.engines)}`, { duration: 6000 })
    } catch (e) { toastError(e) }
  }

  async cancelPrep() {
    try { await api(`/api/books/${this.id}/listen/prepare${this.q()}`, { method: 'DELETE' }); this.job = null; toast('已取消，准备好的留着'); this.togglePrep(); this.togglePrep() } catch (e) { toastError(e) }
  }

  // 每分钟：播放时告诉书库读到哪了（自动保持往后至少 30 分钟），刷新准备情况
  async heartbeat() {
    if (this.destroyed || !this.info) return
    try {
      let r
      if (this.playing || !this.job) {
        r = await api(`/api/books/${this.id}/listen/prepare`, { method: 'POST', body: { scope: 'auto', minutes: this.wantMinutes(), voices: this.voices, from: this.pos } })
        this.job = r.job
      } else {
        r = await api(`/api/books/${this.id}/listen${this.q()}`)
        this.job = r.job || this.job
      }
      this.drawReady(r.status, r.engines)
    } catch {}
  }

  // 要书库准备多久：至少 30 分钟；最近卡过（等语音）就多准备
  wantMinutes() {
    const stalls = this.stalls || 0
    return stalls >= 3 ? 120 : stalls >= 1 ? 60 : 30
  }

  // ---------- 控制 ----------
  toggle() { this.playing ? this.pause() : this.play() }

  async jump(pos, play) {
    this.pos = { n: pos.n, i: pos.i }
    if (this.shownN !== pos.n) await this.showChapter(pos.n)
    this.highlight()
    this.updateUI()
    if (this.native) { call('tts', 'jump', { n: pos.n, i: pos.i }); if (play && !this.playing) call('tts', 'resume', '') }
    else if (play || this.playing) this.playCurrent()
  }

  async step(d) {
    const list = await this.sentences(this.pos.n)
    let { n, i } = this.pos
    i += d
    if (i < 0) {
      if (n === 0) return
      n--
      i = Math.max(0, (await this.sentences(n)).length - 1)
    } else if (i >= list.length) {
      if (n + 1 >= this.info.chapters.length) return
      n++; i = 0
    }
    this.jump({ n, i }, false)
  }

  jumpChapter(d) {
    const n = this.pos.n + d
    if (n < 0 || n >= this.info.chapters.length) return
    this.jump({ n, i: 0 }, false)
  }

  pickChapter(e) {
    const r = e.currentTarget.getBoundingClientRect()
    openMenu(this.info.chapters.map(c => ({ label: (c.n === this.pos.n ? '▶ ' : '') + (c.title || `第 ${c.n + 1} 章`), run: () => this.jump({ n: c.n, i: 0 }, false) })),
      { x: r.left, y: r.bottom, title: '目录' })
  }

  pickSpeed(e) {
    const r = e.currentTarget.getBoundingClientRect()
    openMenu(SPEEDS.map(v => ({ label: v.toFixed(1) + '×' + (Math.abs(v - this.speed) < 0.01 ? '　✓' : ''), run: () => this.setSpeed(v) })), { x: r.left, y: r.top, above: true, title: '语速' })
  }

  setSpeed(v) {
    this.prefs.speed = v
    save(PREFS_KEY, this.prefs)
    if (this.native) call('tts', 'speed', String(v))
    else if (this.audio) this.audio.playbackRate = v
    this.updateUI()
  }

  pickTimer(e) {
    const r = e.currentTarget.getBoundingClientRect()
    openMenu(TIMERS.map(([v, l]) => ({ label: l + (v === this.timer ? '　✓' : ''), run: () => this.setTimer(v) })), { x: r.left, y: r.top, above: true, title: '定时停止' })
  }

  setTimer(v) {
    this.timer = v
    this.timerEnd = v > 0 ? Date.now() + v * 60000 : 0
    if (this.native) call('tts', 'timer', String(v))
    this.updateUI()
    toast(v === 0 ? '已关闭定时' : v === -1 ? '读完本章后停止' : `${v} 分钟后停止`, { duration: 1500 })
  }

  toReader() {
    const s = (this.chapters.get(this.pos.n) || [])[this.pos.i]
    this.saveProgress()
    location.hash = `#/read/${this.id}` + (s?.loc ? '?at=' + encodeURIComponent(s.loc) : '')
  }

  // ---------- 浏览器里播放：直接放书库的语音 ----------
  attachBrowser() {
    const a = this.audio
    a.preload = 'auto'
    a.addEventListener('ended', () => this.advance())
    a.addEventListener('waiting', () => { if (this.playing) { this.setState('等语音…'); this.waitAt = Date.now() } })
    a.addEventListener('playing', () => {
      if (this.waitAt && Date.now() - this.waitAt > 2000) this.stalls = (this.stalls || 0) + 1
      this.waitAt = 0
      this.setState('')
    })
    a.addEventListener('error', () => { if (this.playing) { this.setState('这一句没取到，跳过'); setTimeout(() => this.advance(), 800) } })
    if ('mediaSession' in navigator) {
      const info = this.info
      try {
        navigator.mediaSession.metadata = new MediaMetadata({ title: info.title, artist: info.author || '', album: '书库',
          artwork: info.has_cover ? [{ src: new URL(apiUrl(`/api/books/${this.id}/cover?w=480`), location.href).href, sizes: '480x720', type: 'image/jpeg' }] : [] })
      } catch {}
      const set = (k, f) => { try { navigator.mediaSession.setActionHandler(k, f) } catch {} }
      set('play', () => this.play()); set('pause', () => this.pause())
      set('previoustrack', () => this.step(-1)); set('nexttrack', () => this.step(1))
    }
    // 阅读时长：在播放就算
    const t = setInterval(() => {
      if (!this.playing) return
      this.listenSecs++
      if (this.timer > 0 && Date.now() >= this.timerEnd) { this.timer = 0; this.pause(); toast('定时结束，已暂停') }
      if (this.listenSecs % 30 === 0) this.saveProgress()
    }, 1000)
    this.cleanups.push(() => clearInterval(t))
  }

  audioUrl(n, i) { return apiUrl(`/api/books/${this.id}/listen/${n}/${i}${this.q()}`) }

  async playCurrent() {
    const { n, i } = this.pos
    this.highlight()
    this.updateUI()
    const a = this.audio
    a.src = this.audioUrl(n, i)
    a.playbackRate = this.speed
    try { await a.play() } catch (e) {
      if (e?.name === 'NotAllowedError') { this.playing = false; this.updateUI(); this.setState('点播放键开始'); return }
    }
    // 后面几句先让浏览器取一下（书库没准备好的会现合成，下次就快了）
    for (let k = 1; k <= 3; k++) this.warm(n, i + k)
  }

  async warm(n, i) {
    const list = await this.sentences(n).catch(() => [])
    if (i >= list.length) { n++; i -= list.length; if (n >= this.info.chapters.length) return }
    const key = n + ':' + i
    this.warmed ||= new Set()
    if (this.warmed.has(key)) return
    this.warmed.add(key)
    fetch(this.audioUrl(n, i), { credentials: net.base ? 'omit' : 'same-origin' }).catch(() => this.warmed.delete(key))
  }

  async advance() {
    if (!this.playing) return
    const list = await this.sentences(this.pos.n)
    if (this.pos.i + 1 < list.length) { this.pos.i++ }
    else {
      if (this.timer === -1) { this.timer = 0; this.pause(); toast('本章读完了，已暂停'); return }
      if (this.pos.n + 1 >= this.info.chapters.length) { this.pause(); toast('全书读完了'); return }
      this.pos = { n: this.pos.n + 1, i: 0 }
      await this.showChapter(this.pos.n)
    }
    this.playCurrent()
  }

  play() {
    // App：服务已经在放这本书（暂停着）就接着放，不然从当前句开始
    if (this.native) { this.nativeActive ? call('tts', 'resume', '') : this.startNative(); return }
    this.playing = true
    this.updateUI()
    this.playCurrent()
    this.heartbeat()
  }

  pause() {
    if (this.native) { call('tts', 'pause', ''); return }
    this.playing = false
    this.audio?.pause()
    this.updateUI()
    this.saveProgress()
  }

  saveProgress() {
    if (this.native || !this.info) return
    const s = (this.chapters.get(this.pos.n) || [])[this.pos.i]
    if (!s?.loc && !s?.pct) return
    const secs = this.listenSecs
    this.listenSecs = 0
    putProgress(this.id, { file_id: this.info.file_id, location: s.loc || '', percent: s.pct || 0, device: deviceName + ' · 听书', seconds: secs, at: Date.now() }).catch(() => {})
  }

  // ---------- App 里：交给原生的后台服务（语音存到手机上再放） ----------
  attachNative() {
    this.cleanups.push(onNative('tts', e => this.onNative(e)))
    const st = callJSON('ttsState') || {}
    // 已经在听这本书（从通知栏、手表回来）：跟上它的位置
    if (st.active && st.mode === 'pack' && +st.book_id === +this.id) {
      this.nativeActive = true
      this.playing = !!st.playing
      if (st.n != null) this.jump({ n: st.n, i: st.i }, false)
      this.updateUI()
    }
  }

  startNative() {
    const info = this.info
    call('tts', 'start', {
      mode: 'pack',
      book: { id: this.id, file_id: info.file_id, title: info.title, author: info.author || '', lang: info.lang || '',
        cover: info.has_cover ? new URL(apiUrl(`/api/books/${this.id}/cover?w=480`), location.href).href : '' },
      voicesParam: this.voices, from: this.pos, speed: this.speed, timer: this.timer,
      chapters: info.chapters.map(c => ({ n: c.n, count: c.count, title: c.title })),
    })
  }

  onNative(e) {
    if (e.type === 'seg' && e.n != null) {
      if (e.n !== this.pos.n || e.i !== this.pos.i) {
        this.pos = { n: e.n, i: e.i }
        if (this.shownN !== e.n && document.visibilityState === 'visible') this.showChapter(e.n)
        else this.highlight()
        this.updateUI()
      }
    } else if (e.type === 'state') {
      this.nativeActive = e.active !== false && e.mode === 'pack'
      this.playing = !!e.playing
      if (e.timer !== undefined) this.timer = e.timer
      if (e.speed) { this.prefs.speed = e.speed; save(PREFS_KEY, this.prefs) }
      if (e.ahead != null) this.nativeAhead = e.ahead
      this.setState(e.status || '')
      this.updateUI()
    } else if (e.type === 'stopped') {
      this.playing = false
      this.nativeActive = false
      this.updateUI()
      if (e.reason) toast(e.reason)
    } else if (e.type === 'error') toast(e.message || '听书出错了', { error: true })
  }

  destroy() {
    this.destroyed = true
    if (!this.native) { this.audio?.pause(); this.saveProgress() }
    for (const c of this.cleanups) c()
    this.root?.remove()
    document.body.style.overflow = ''
    document.title = this.ctx.server?.name || '书库'
  }
}
