// 听书：按句读，高亮当前句、自动翻页；语速、音色、睡眠定时；锁屏控制
// 声音来源：服务器（微软自然语音 / 自定义接口，带缓存）或设备自带语音

import { h, fill, icon, iconBtn, toast, store, save } from '../ui.js'
import { api, apiUrl } from '../api.js'

const SPEEDS = [0.8, 1, 1.2, 1.4, 1.6, 1.8, 2, 2.5]
const TIMERS = [0, 15, 30, 60, -1] // -1：读完本章
const PREFS_KEY = 'shuku.tts'

// 一小段静音 WAV：iPhone 只允许在点击时开始播放，先用它“解锁”音频
function silentWav() {
  const n = 2205, buf = new ArrayBuffer(44 + n * 2), v = new DataView(buf)
  const w = (o, s) => [...s].forEach((c, i) => v.setUint8(o + i, c.charCodeAt(0)))
  w(0, 'RIFF'); v.setUint32(4, 36 + n * 2, true); w(8, 'WAVE'); w(12, 'fmt ')
  v.setUint32(16, 16, true); v.setUint16(20, 1, true); v.setUint16(22, 1, true); v.setUint32(24, 22050, true)
  v.setUint32(28, 44100, true); v.setUint16(32, 2, true); v.setUint16(34, 16, true); w(36, 'data'); v.setUint32(40, n * 2, true)
  return URL.createObjectURL(new Blob([buf], { type: 'audio/wav' }))
}

// 句子切分：中文标点、英文句号、换行；太短的合并，太长的再切
export function splitSentences(text) {
  const parts = text.match(/[^。！？!?；;…\n]+[。！？!?；;…]*[”’」』）)"'》]*\s*|\n+/g) || []
  const out = []
  let cur = '', start = 0, pos = 0
  for (const p of parts) {
    if (!cur) start = pos
    cur += p
    pos += p.length
    const clean = cur.trim()
    if (clean.length >= 24 || /\n/.test(p)) {
      if (clean) out.push({ text: clean, start, end: pos })
      cur = ''
    }
  }
  if (cur.trim()) out.push({ text: cur.trim(), start, end: pos })
  // 超长句按逗号切开（合成更快，高亮也更准）
  const res = []
  for (const s of out) {
    if (s.text.length <= 180) { res.push(s); continue }
    let off = s.start, buf = ''
    for (const piece of s.text.match(/[^，,、：:]+[，,、：:]?/g) || [s.text]) {
      buf += piece
      if (buf.length >= 90) { res.push({ text: buf, start: off, end: off + buf.length }); off += buf.length; buf = '' }
    }
    if (buf) res.push({ text: buf, start: off, end: s.end })
  }
  return res
}

export class Narrator {
  constructor(shell) {
    this.shell = shell
    this.prefs = store(PREFS_KEY, { speed: 1, voice: '', engine: '' })
    this.queue = []
    this.i = 0
    this.playing = false
    this.active = false
    this.cache = new Map() // 段落文字 → Promise<blob url>
    this.timerMin = 0
    this.timerEnd = 0
    this.audio = new Audio()
    this.audio.preload = 'auto'
    this.audio.addEventListener('ended', () => this.advance())
    this.audio.addEventListener('error', () => { if (this.playing && this.audio.src.startsWith('blob:')) this.onAudioError(new Error('音频无法播放')) })
    this.buildUI()
  }

  get speed() { return this.prefs.speed || 1 }

  async engineInfo() {
    if (this.info) return this.info
    try {
      const r = await api('/api/tts/voices')
      this.info = r
      if (!this.prefs.voice || !r.voices.some(v => v.id === this.prefs.voice)) this.prefs.voice = r.default
    } catch { this.info = { engine: 'browser', voices: [] } }
    return this.info
  }

  get mode() { return this.forceBrowser || this.info?.engine === 'browser' ? 'browser' : 'server' }

  buildUI() {
    this.playBtn = iconBtn('pause', '暂停', () => this.toggle(), { class: 'icon-btn play' })
    this.speedBtn = h('button', { class: 'speed', title: '语速', onclick: () => this.cycleSpeed() }, '1.0×')
    this.timerBtn = h('button', { class: 'icon-btn', title: '睡眠定时', onclick: () => this.cycleTimer() }, icon('timer'))
    this.timerText = h('span', { class: 'timer' })
    this.stateEl = h('span', { class: 'state' })
    this.el = h('div', { class: 'r-player', hidden: true, role: 'region', 'aria-label': '听书' },
      iconBtn('prevSent', '上一句', () => this.step(-1)),
      this.playBtn,
      iconBtn('nextSent', '下一句', () => this.step(1)),
      this.speedBtn, this.timerBtn, this.timerText, this.stateEl,
      iconBtn('close', '停止听书', () => this.stop()))
    this.el.addEventListener('click', e => e.stopPropagation())
    this.shell.root.append(this.el)
    this.updateUI()
  }

  updateUI() {
    this.playBtn.replaceChildren(icon(this.playing ? 'pause' : 'play'))
    this.playBtn.title = this.playing ? '暂停' : '继续'
    this.speedBtn.textContent = this.speed.toFixed(1) + '×'
    this.timerBtn.classList.toggle('on', !!this.timerMin)
    this.timerText.textContent = this.timerMin === -1 ? '本章' : this.timerEnd ? Math.max(0, Math.ceil((this.timerEnd - Date.now()) / 60000)) + '分' : ''
    if ('mediaSession' in navigator) navigator.mediaSession.playbackState = this.playing ? 'playing' : 'paused'
  }

  setState(t) { this.stateEl.textContent = t || '' }

  // ---------- 开始 / 停止 ----------
  async start() {
    const eng = this.shell.engine
    if (!eng?.ttsSegments) { toast('漫画没有文字，不能朗读'); return }
    // 在点击事件里先播一下静音，解锁 iPhone 的音频
    this.audio.src = silentWav()
    this.audio.play().catch(() => {})
    await this.engineInfo()
    this.active = true
    this.el.hidden = false
    this.shell.root.classList.add('narrating')
    document.body.classList.add('narrating')
    this.setupMediaSession()
    this.setState('准备中…')
    await this.load(true)
    this.play()
  }

  stop() {
    this.active = false
    this.playing = false
    this.audio.pause()
    speechSynthesis?.cancel()
    this.shell.engine?.ttsClear?.()
    this.el.hidden = true
    this.shell.root.classList.remove('narrating')
    document.body.classList.remove('narrating')
    clearInterval(this.timerTick); this.timerMin = 0; this.timerEnd = 0
    for (const p of this.cache.values()) p.then(u => u && URL.revokeObjectURL(u)).catch(() => {})
    this.cache.clear()
    this.updateUI()
    this.setState('')
  }

  destroy() { this.stop(); this.el.remove() }

  // 从当前看到的位置取句子
  async load(fromView) {
    this.queue = await this.shell.engine.ttsSegments(fromView)
    this.i = 0
    while (!this.queue.length) {
      if (!(await this.shell.engine.ttsNextUnit())) { this.finish(); return }
      this.queue = await this.shell.engine.ttsSegments(false)
    }
  }

  // 用户自己翻页后，从新位置接着读
  async jumpToView() {
    if (!this.active) return
    const was = this.playing
    this.halt()
    await this.load(true)
    if (was) this.play()
    else this.shell.engine.ttsHighlight?.(this.queue[this.i])
  }

  toggle() { this.playing ? this.pause() : this.play() }

  pause() {
    this.playing = false
    this.audio.pause()
    if (this.mode === 'browser') speechSynthesis.pause()
    this.updateUI()
  }

  halt() {
    this.playing = false
    this.audio.pause()
    speechSynthesis?.cancel()
  }

  async play() {
    if (!this.active) return
    if (this.mode === 'browser' && speechSynthesis.paused && speechSynthesis.speaking) {
      this.playing = true; speechSynthesis.resume(); this.updateUI(); return
    }
    this.playing = true
    this.updateUI()
    await this.speakCurrent()
  }

  async step(d) {
    if (!this.queue.length) return
    this.halt()
    this.i = Math.max(0, Math.min(this.queue.length - 1, this.i + d))
    this.playing = true
    this.updateUI()
    await this.speakCurrent()
  }

  finish() {
    this.stop()
    toast('全书读完了')
  }

  // ---------- 朗读 ----------
  async speakCurrent() {
    const seg = this.queue[this.i]
    if (!seg || !this.playing) return
    this.shell.engine.ttsHighlight?.(seg)
    this.shell.markActivity?.()
    if (this.mode === 'browser') return this.speakBrowser(seg)
    const token = (this.token = {})
    try {
      this.setState(this.cache.has(seg.text) ? '' : '合成中…')
      const url = await this.fetchAudio(seg.text)
      if (token !== this.token || !this.playing) return
      this.setState('')
      this.audio.src = url
      this.audio.playbackRate = 1
      await this.audio.play()
      // 预先合成后面两句，避免停顿
      for (let k = 1; k <= 2; k++) { const nx = this.queue[this.i + k]; if (nx) this.fetchAudio(nx.text).catch(() => {}) }
    } catch (e) {
      if (token !== this.token) return
      if (e?.name === 'NotAllowedError') { this.pause(); this.setState('点播放键继续'); return }
      this.onAudioError(e)
    }
  }

  fetchAudio(text) {
    if (!this.cache.has(text)) {
      const p = api('/api/tts', { method: 'POST', raw: true, body: { text, voice: this.prefs.voice, speed: this.speed } })
        .then(r => r.blob()).then(b => URL.createObjectURL(b))
      p.catch(() => this.cache.delete(text))
      this.cache.set(text, p)
      // 只留最近 30 段
      if (this.cache.size > 30) {
        const [k, old] = this.cache.entries().next().value
        this.cache.delete(k); old.then(u => URL.revokeObjectURL(u)).catch(() => {})
      }
    }
    return this.cache.get(text)
  }

  onAudioError(e) {
    if (this.forceBrowser || !('speechSynthesis' in window)) {
      this.pause(); this.setState('朗读失败'); toast(e?.message || '语音合成失败', { error: true }); return
    }
    // 服务器语音用不了（NAS 连不上微软、接口填错等），改用设备自带语音
    this.forceBrowser = true
    toast((e?.message || '服务器语音不可用') + '，已改用设备自带语音', { duration: 5000 })
    this.speakCurrent()
  }

  speakBrowser(seg) {
    this.setState('')
    speechSynthesis.cancel()
    const u = new SpeechSynthesisUtterance(seg.text)
    const lang = /[぀-ヿ]/.test(seg.text) ? 'ja-JP' : /[一-鿿]/.test(seg.text) ? 'zh-CN' : 'en-US'
    u.lang = lang
    const voices = speechSynthesis.getVoices().filter(v => v.lang.replace('_', '-').startsWith(lang.slice(0, 2)))
    // 优先选“自然”“增强”的声音（Edge 的 Natural、苹果的 Enhanced / Premium）
    const pick = voices.find(v => /natural|neural|enhanced|premium|siri/i.test(v.name)) || voices.find(v => v.lang.replace('_', '-') === lang) || voices[0]
    if (pick) u.voice = pick
    u.rate = this.speed
    const token = (this.token = {})
    u.onend = () => { if (token === this.token) this.advance() }
    u.onerror = () => {}
    speechSynthesis.speak(u)
  }

  async advance() {
    if (!this.playing || !this.active) return
    if (this.timerMin > 0 && Date.now() >= this.timerEnd) { this.pause(); this.timerMin = 0; this.timerEnd = 0; this.updateUI(); toast('定时结束，已暂停'); return }
    this.i++
    if (this.i >= this.queue.length) {
      if (this.timerMin === -1) { this.pause(); this.timerMin = 0; this.updateUI(); toast('本章读完，已暂停'); return }
      if (!(await this.shell.engine.ttsNextUnit())) { this.finish(); return }
      await this.load(false)
      if (!this.active) return
    }
    this.speakCurrent()
  }

  // ---------- 控制 ----------
  cycleSpeed() {
    const i = SPEEDS.indexOf(this.speed)
    this.prefs.speed = SPEEDS[(i + 1) % SPEEDS.length]
    save(PREFS_KEY, this.prefs)
    this.updateUI()
    this.restartCurrent()
  }

  setVoice(v) {
    this.prefs.voice = v
    save(PREFS_KEY, this.prefs)
    this.restartCurrent()
  }

  restartCurrent() {
    for (const p of this.cache.values()) p.then(u => URL.revokeObjectURL(u)).catch(() => {})
    this.cache.clear()
    if (this.playing) { this.halt(); this.playing = true; this.speakCurrent() }
  }

  cycleTimer() {
    const i = TIMERS.indexOf(this.timerMin)
    this.timerMin = TIMERS[(i + 1) % TIMERS.length]
    this.timerEnd = this.timerMin > 0 ? Date.now() + this.timerMin * 60000 : 0
    clearInterval(this.timerTick)
    if (this.timerMin > 0) this.timerTick = setInterval(() => {
      this.updateUI()
      if (Date.now() >= this.timerEnd && this.playing) { this.pause(); this.timerMin = 0; this.timerEnd = 0; clearInterval(this.timerTick); this.updateUI(); toast('定时结束，已暂停') }
    }, 15000)
    this.updateUI()
    toast(this.timerMin === 0 ? '已关闭定时' : this.timerMin === -1 ? '读完本章后停止' : `${this.timerMin} 分钟后停止`, { duration: 1500 })
  }

  setupMediaSession() {
    if (!('mediaSession' in navigator)) return
    const info = this.shell.info || {}
    navigator.mediaSession.metadata = new MediaMetadata({
      title: info.title || '', artist: info.author || '', album: '书库',
      artwork: info.has_cover ? [{ src: new URL(apiUrl(`/api/books/${this.shell.id}/cover`), location.href).href, sizes: '480x720', type: 'image/jpeg' }] : [],
    })
    const set = (a, f) => { try { navigator.mediaSession.setActionHandler(a, f) } catch {} }
    set('play', () => this.play())
    set('pause', () => this.pause())
    set('previoustrack', () => this.step(-1))
    set('nexttrack', () => this.step(1))
    set('stop', () => this.stop())
  }

  // 阅读设置里的“听书”部分
  async sheetSection() {
    const info = await this.engineInfo()
    const parts = [h('h4', null, '听书')]
    if (info.engine === 'browser' || this.forceBrowser) {
      parts.push(h('small', { style: { display: 'block', fontSize: '12px', opacity: .7 } }, '正在使用设备自带语音。在“设置 → 听书”里可以换成更自然的微软语音。'))
    } else if (info.voices?.length) {
      const sel = h('select', { class: 'input', 'aria-label': '音色' }, info.voices.map(v => h('option', { value: v.id, selected: v.id === this.prefs.voice }, `${v.name}${v.lang ? '（' + v.lang + (v.gender ? ' · ' + v.gender : '') + '）' : ''}`)))
      sel.onchange = () => this.setVoice(sel.value)
      parts.push(sel)
    }
    parts.push(h('div', { class: 'row', style: { gap: '8px', marginTop: '8px' } },
      h('button', { class: 'btn small', style: { flex: 1, background: 'transparent', borderColor: 'var(--r-rule)', color: 'inherit' }, onclick: () => { this.shell.closeOverlays(); this.active ? this.toggle() : this.start() } }, icon('headphones'), this.active ? (this.playing ? '暂停朗读' : '继续朗读') : '从这里开始朗读')))
    return parts
  }
}
