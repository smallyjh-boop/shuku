// 听书：按句读，高亮当前句、自动翻页；语速、音色、睡眠定时；锁屏控制
// 声音来源：服务器（微软自然语音 / 自定义接口，带缓存）或设备自带语音

import { h, fill, icon, iconBtn, toast, store, save, openMenu } from '../ui.js'
import { api, apiUrl, net } from '../api.js'
import { call, callJSON, onNative, isNative } from '../native.js'

const SPEEDS = [0.8, 1, 1.2, 1.4, 1.6, 1.8, 2, 2.5]

// 点语速按钮：弹出来直接选（以前点一下换一档，要点好几下）
function pickSpeed(nar, e) {
  const r = e.currentTarget.getBoundingClientRect()
  openMenu(SPEEDS.map(v => ({ label: v.toFixed(1) + '×' + (Math.abs(v - nar.speed) < 0.01 ? '　✓' : ''), run: () => nar.setSpeed(v) })),
    { x: r.left, y: r.top, above: true, title: '语速' })
}
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
  // 泰文这类没有句号、逗号的文字：一整段还是太长时按空格再切（泰文用空格隔开句子、分句）
  const out2 = []
  for (const s of res) {
    if (s.text.length <= 200 || !/\s/.test(s.text)) { out2.push(s); continue }
    let off = s.start, buf = ''
    for (const piece of s.text.match(/\S+\s*/g) || [s.text]) {
      buf += piece
      if (buf.length >= 100) { out2.push({ text: buf.trim(), start: off, end: off + buf.length }); off += buf.length; buf = '' }
    }
    if (buf.trim()) out2.push({ text: buf.trim(), start: off, end: s.end })
  }
  return out2
}

// ---------- 按书的语言挑声音 ----------

// 书的语言（元数据里的 en、zh-CN、tha……）→ 两个字母的语言代码
const LANG3 = { eng: 'en', tha: 'th', chi: 'zh', zho: 'zh', jpn: 'ja', kor: 'ko', fre: 'fr', fra: 'fr', ger: 'de', deu: 'de', spa: 'es', rus: 'ru', vie: 'vi', ita: 'it', por: 'pt' }
export function langCode(l) {
  l = String(l || '').toLowerCase().split(/[-_]/)[0]
  return LANG3[l] || (l.length === 2 ? l : '')
}

// 一段文字主要是什么文字（和书库的 scriptLang 一样：一个汉字、假名算 4 个字母，中文里夹个英文词不算英文）：
// th、ja、ko、zh、ru、ar，拉丁字母为主返回 latin，只有数字符号返回空
const SCRIPTS = [['th', /[\u0E00-\u0E7F]/g, 1], ['kana', /[\u3040-\u30FF]/g, 4], ['ko', /[\uAC00-\uD7AF]/g, 2], ['zh', /[\u4E00-\u9FFF\u3400-\u4DBF]/g, 4],
  ['ru', /[\u0400-\u04FF]/g, 1], ['ar', /[\u0600-\u06FF]/g, 1], ['latin', /[A-Za-z\u00C0-\u024F]/g, 1]]
export function textLang(t) {
  t = String(t || '')
  const n = {}
  let total = 0
  for (const [k, re, w] of SCRIPTS) { n[k] = ((t.match(re) || []).length) * w; total += n[k] }
  if (!total) return ''
  if (n.kana && n.kana + n.zh >= total / 2) return 'ja'
  let best = '', m = 0
  for (const k in n) if (n[k] > m) { best = k; m = n[k] }
  return best === 'kana' ? 'ja' : best
}
const NON_LATIN = { zh: 1, ja: 1, ko: 1, th: 1, ru: 1, uk: 1, bg: 1, ar: 1, fa: 1, he: 1, hi: 1, el: 1 }

// 一句用哪种语言的声音：看这句的文字；拉丁字母按书的语言（法文书用法语），书不是拉丁字母的语言时用英语
export function segLang(text, book) {
  const t = textLang(text)
  if (!t) return book || 'zh'
  if (t === 'latin') return book && !NON_LATIN[book] ? book : 'en'
  return t
}

let voicesReq = null
// 书库的声音列表（每个声音带语言代码）和每种语言的默认声音；同一次打开只问一次
export function voiceInfo() {
  if (!voicesReq) {
    voicesReq = api('/api/tts/voices').catch(e => { voicesReq = null; throw e })
  }
  return voicesReq
}

// 这本书用哪个声音：这种语言选过的 → 书库给这种语言的默认 → 以前选的（中文）
export function voiceFor(prefs, info, code) {
  const has = id => !!id && (info?.voices || []).some(v => v.id === id)
  const chosen = prefs.voices?.[code || 'zh']
  if (has(chosen)) return chosen
  if (!code || code === 'zh') return has(prefs.voice) ? prefs.voice : (info?.defaults?.zh || info?.default || '')
  // 自定义接口（OpenAI 兼容）的声音一般都是多语言的：不分语言
  if (!info?.defaults) return has(prefs.voice) ? prefs.voice : (info?.default || '')
  return info.defaults[code] || info.default || ''
}

// ---------- 读法：在线合成（书库）还是本机语音，每台设备自己选 ----------

// 读法有四种：auto 书库自动挑（NAS 本地语音够快就用它，不然用微软）、ms 先用微软、nas 先用 NAS 本地语音、device 这台设备自带的语音。
// 前三种都是书库合成：选的来源放在音色名前面（nas: / ms:）交给书库，旧版 App 原样转过来也认
const ENGINE_NOTE = {
  auto: '书库合成：NAS 本地语音够快就用它，不然用微软；微软连不上时用 NAS 兜底。',
  ms: '微软自然语音，最自然，要 NAS 能上外网；连不上时用 NAS 本地语音兜底。',
  nas: 'NAS 本地语音（中文、英文），不经过外网；NAS 合成得慢时第一句要多等一会儿。其他语言照旧用微软。',
  device: '用这台设备自带的语音，不经过书库，断网也能读。',
  server: '书库在线合成，声音更自然，要能连上书库。',
}

const SRC_LABEL = { auto: '自动', ms: '微软', nas: 'NAS' }

export function srcVoice(prefs, voice) {
  const src = prefs?.src
  return src === 'nas' || src === 'ms' ? `${src}:${voice || ''}` : voice
}

// 阅读设置里的“读法”切换（和别的阅读设置一样的样式）；NAS 上没开本地语音时不显示“NAS”
function engineSeg(cur, onPick, nasOk) {
  const opts = [['auto', '自动'], ['ms', '微软'], ...(nasOk || cur === 'nas' ? [['nas', 'NAS']] : []), ['device', '本机']]
  return h('div', { class: 'seg', role: 'group', 'aria-label': '读法', style: { marginBottom: '6px' } },
    opts.map(([v, l]) => h('button', {
      type: 'button', class: cur === v ? 'on' : '', 'aria-pressed': cur === v ? 'true' : 'false', onclick: () => onPick(v),
    }, l)))
}

// 播放条上的“声音”按钮：一点弹出菜单，读法（在线合成 / 本机语音）和音色直接选，不用进阅读设置
function openVoiceMenu(e, { cur, serverOk = true, nasOk, voices, curVoice, onEngine, onVoice }) {
  const r = e.currentTarget.getBoundingClientRect()
  const mark = on => on ? '　✓' : ''
  const items = []
  if (serverOk) items.push(
    { label: '自动（NAS 够快用 NAS，不然用微软）' + mark(cur === 'auto'), run: () => onEngine('auto') },
    { label: '微软在线合成（最自然）' + mark(cur === 'ms'), run: () => onEngine('ms') },
    ...(nasOk || cur === 'nas' ? [{ label: 'NAS 本地语音（中英，不经过外网）' + mark(cur === 'nas'), run: () => onEngine('nas') }] : []),
    { label: '本机语音（不联网也能读）' + mark(cur === 'device'), run: () => onEngine('device') })
  if (voices.length) {
    if (items.length) items.push(null)
    for (const v of voices.slice(0, 40)) items.push({ label: v.name + mark(v.id === curVoice), run: () => onVoice(v.id) })
  }
  openMenu(items, { x: r.left, y: r.top, above: true, title: '声音 · ' + ({ auto: '自动', ms: '微软', nas: 'NAS 本地', device: '本机语音' }[cur] || '书库合成') })
}

// 请书库在后台提前合成后面的句子（网络不好时不用一句一句等）。items：{ text, voice }，和正式合成时的一样
function serverPrefetch(items, lang, speed) {
  if (!items.length) return
  api('/api/tts/prefetch', { method: 'POST', body: { items: items.slice(0, 300), lang: lang || '', speed } }).catch(() => {})
}

// 没在听也先准备：在读哪本书，就从看到的这一页往后，把这一节（章、PDF 的这一页）交给书库提前合成。
// 这台设备用本机语音、书库设成设备语音、断网时不做。阅读器在读了一会儿、翻到新的一节时调用
export async function prefetchFromView(shell) {
  const eng = shell.engine
  if (!eng?.ttsSegments || net.route === 'offline') return
  const prefs = store(PREFS_KEY, { speed: 1 })
  if (isNative ? (callJSON('prefs') || {}).tts_engine === 'device' : prefs.engine === 'device') return
  let info
  try { info = await voiceInfo() } catch { return }
  if (info.engine === 'browser') return
  const segs = await eng.ttsSegments(true).catch(() => [])
  if (!segs?.length) return
  const code = bookCode(shell, segs)
  serverPrefetch(segs.map(x => ({ text: x.text, voice: srcVoice(prefs, voiceFor(prefs, info, segLang(x.text, code))) })), code, prefs.speed || 1)
}

// 书库提前合成到哪了：“已准备好约 12 分钟”（没有在准备的就是空的）
export async function prefetchStatus() {
  try {
    const r = await api('/api/tts/prefetch')
    if (!r.total) return ''
    const min = r.minutes >= 1 ? `约 ${Math.round(r.minutes)} 分钟` : `${r.done} 句`
    if (r.running) return `正在提前合成，已备好${min}（${r.done}/${r.total} 句）`
    return r.done ? `已提前备好${min}，网络不好也不用等` : r.failed ? '提前合成失败（书库连不上语音服务）' : ''
  } catch { return '' }
}

// 听书时播放条上显示书库提前备好了多少（每 15 秒看一次）
function watchReady(nar, on) {
  clearInterval(nar.readyT)
  nar.readyEl.textContent = ''
  if (!on) return
  const tick = async () => {
    const device = nar instanceof Narrator ? nar.mode === 'browser' : nar.engine === 'device'
    if (device || !nar.active) { nar.readyEl.textContent = ''; return }
    try {
      const r = await api('/api/tts/prefetch')
      nar.readyEl.textContent = r.total && r.done ? `已备 ${r.minutes >= 1 ? Math.round(r.minutes) + ' 分' : r.done + ' 句'}` : r.running ? '提前合成中' : ''
    } catch { nar.readyEl.textContent = '' }
  }
  setTimeout(tick, 3000)
  nar.readyT = setInterval(tick, 15000)
}

// 书库的声音里这种语言的（和多语言的），给菜单用
function serverVoices(info, code) {
  let list = info?.voices || []
  if (info?.defaults && code) {
    const same = list.filter(v => v.code === code || v.multi)
    if (same.some(v => v.code === code)) list = same
  }
  return list.map(v => ({ id: v.id, name: v.name + (v.gender ? '（' + v.gender + '）' : '') }))
}

const smallNote = (t) => h('small', { style: { display: 'block', fontSize: '12px', opacity: .7, marginBottom: '6px' } }, t)

// 浏览器自带的语音（有的浏览器第一次取是空的，要等 voiceschanged）
const BCP = { zh: 'zh-CN', ja: 'ja-JP', ko: 'ko-KR', th: 'th-TH', ru: 'ru-RU', ar: 'ar-SA', en: 'en-US', fr: 'fr-FR', de: 'de-DE', es: 'es-ES', vi: 'vi-VN', it: 'it-IT', pt: 'pt-BR' }
function browserVoices(code) {
  const ss = window.speechSynthesis
  if (!ss) return []
  const lang = (BCP[code] || code || 'zh').slice(0, 2)
  try { return ss.getVoices().filter(v => String(v.lang).replace('_', '-').slice(0, 2) === lang) } catch { return [] }
}

// 本机语音读混着几种文字的句子：按文字种类切成几段，每段用那种语言的声音读（和 App 的 MixedText 一样）。
// 主要是中日韩文字的句子里夹的短英文（几个字母、一两个词）不切开，中文声音读得了
const RUN_KIND = [['th', /[\u0E00-\u0E7F]/], ['ja', /[\u3040-\u30FF]/], ['ko', /[\uAC00-\uD7AF]/], ['zh', /[\u4E00-\u9FFF\u3400-\u4DBF]/],
  ['ru', /[\u0400-\u04FF]/], ['ar', /[\u0600-\u06FF]/], ['latin', /[A-Za-z\u00C0-\u024F]/]]
export function mixedRuns(text, book) {
  const t = String(text || '')
  const hasKana = /[\u3040-\u30FF]/.test(t), hasHangul = /[\uAC00-\uD7AF]/.test(t)
  const kindOf = c => {
    for (const [k, re] of RUN_KIND) if (re.test(c)) return k === 'zh' && hasKana ? 'ja' : k === 'zh' && hasHangul ? 'ko' : k
    return ''
  }
  let parts = []
  for (const c of t) {
    const k = kindOf(c)
    const last = parts[parts.length - 1]
    if (!k || (last && last.k === k)) { if (last) last.s += c; else parts.push({ k: '', s: c }) }
    else if (parts.length === 1 && !parts[0].k) { parts[0].k = k; parts[0].s += c }
    else parts.push({ k, s: c })
  }
  const CJK = { zh: 1, ja: 1, ko: 1 }
  let cjk = 0, latin = 0
  for (const c of t) { const k = kindOf(c); if (CJK[k]) cjk += 4; else if (k === 'latin') latin++ }
  if (cjk >= latin) {
    for (let i = 0; i < parts.length; i++) {
      const p = parts[i]
      if (p.k !== 'latin' || parts.length < 2) continue
      const letters = (p.s.match(/[A-Za-z\u00C0-\u024F]/g) || []).length, words = p.s.trim().split(/\s+/).filter(w => /[A-Za-z]/.test(w)).length
      if (letters > 12 && words > 2) continue
      if (i > 0 && CJK[parts[i - 1].k]) { parts[i - 1].s += p.s; parts.splice(i--, 1) }
      else if (parts[i + 1] && CJK[parts[i + 1].k]) { parts[i + 1].s = p.s + parts[i + 1].s; parts.splice(i--, 1) }
    }
  }
  const out = []
  for (const p of parts) { const l = out[out.length - 1]; if (l && l.k === p.k) l.s += p.s; else out.push({ ...p }) }
  const latinLang = book && !NON_LATIN[book] ? book : 'en'
  return out.filter(p => p.s.trim()).map(p => ({ text: p.s.trim(), code: !p.k || p.k === 'latin' ? latinLang : p.k }))
}

const LANG_NAME = { zh: '中文', ja: '日语', ko: '韩语', th: '泰语', ru: '俄语', ar: '阿拉伯语', en: '英语', fr: '法语', de: '德语', es: '西班牙语', it: '意大利语', pt: '葡萄牙语', vi: '越南语' }

// 阅读设置里的声音下拉框：只列这种语言的（和多语言的），选了记在这种语言下
function voiceSelect(info, code, cur, onChange) {
  let list = info.voices || []
  if (info.defaults && code) {
    const same = list.filter(v => v.code === code || v.multi)
    if (same.some(v => v.code === code)) list = same
  }
  const sel = h('select', { class: 'input', 'aria-label': '音色' }, list.map(v => h('option', { value: v.id, selected: v.id === cur }, `${v.name}${v.lang ? '（' + v.lang + (v.gender ? ' · ' + v.gender : '') + '）' : ''}`)))
  sel.onchange = () => onChange(sel.value)
  return sel
}

// 记下这种语言选的声音（中文的同时记在以前的 voice 里，旧版也认）
function rememberVoice(prefs, code, v) {
  prefs.voices = Object.assign({}, prefs.voices, { [code || 'zh']: v })
  if (!code || code === 'zh') prefs.voice = v
}

// 这本书的语言：看正文是什么文字为主（元数据常常不对，比如中文网站做的英文书写着 zh）；
// 拉丁字母的书再看元数据是英、法、德还是别的
function bookCode(shell, segs) {
  const meta = langCode(shell.info?.language)
  const sample = (segs || []).slice(0, 40).map(s => s.text).join(' ').slice(0, 4000)
  const t = textLang(sample)
  if (!t) return meta || 'zh'
  if (t === 'latin') return meta && !NON_LATIN[meta] ? meta : 'en'
  if (t === 'ru' && meta && ['uk', 'bg', 'sr', 'kk'].includes(meta)) return meta
  return t
}

export class Narrator {
  constructor(shell) {
    this.shell = shell
    this.prefs = store(PREFS_KEY, { speed: 1, voice: '', engine: '' })
    this.queue = []
    this.i = 0
    this.unreadable = 0
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
      this.info = await voiceInfo()
    } catch { this.info = { engine: 'browser', voices: [] } }
    return this.info
  }

  get voice() { return voiceFor(this.prefs, this.info, this.code) }

  // 读法：书库设置成“设备自带语音”、这台设备选了“本机语音”、书库语音暂时用不了（forceBrowser）时用浏览器的语音
  get engineChoice() { return this.prefs.engine === 'device' ? 'device' : 'server' }
  get serverUsable() { return this.info?.engine !== 'browser' }
  get mode() { return this.forceBrowser || !this.serverUsable || this.engineChoice === 'device' ? 'browser' : 'server' }

  // 读法四选一（auto / ms / nas / device）
  get choice() { return this.engineChoice === 'device' ? 'device' : (this.prefs.src || 'auto') }

  pickChoice(v) {
    if (v === 'device') return this.setEngine('device')
    this.prefs.src = v === 'auto' ? '' : v
    if (this.engineChoice === 'device') return this.setEngine('server')
    save(PREFS_KEY, this.prefs)
    this.forceBrowser = false
    toast({ auto: '书库自动挑声音', ms: '先用微软合成', nas: '先用 NAS 本地语音' }[v], { duration: 1500 })
    this.restartCurrent()
  }

  setEngine(v) {
    if (v === 'device' && !window.speechSynthesis) { toast('这个浏览器没有自带的语音，只能用在线合成', { error: true }); return }
    this.prefs.engine = v === 'device' ? 'device' : 'server'
    save(PREFS_KEY, this.prefs)
    this.forceBrowser = false // 自己选了在线合成：书库语音重新试
    toast(v === 'device' ? '改用本机语音朗读' : '改用书库在线合成', { duration: 1500 })
    this.restartCurrent()
  }

  async voiceMenu(e) {
    const info = await this.engineInfo()
    if (!this.code) this.code = bookCode(this.shell, this.queue)
    const dev = this.mode === 'browser'
    const code = segLang('', this.code)
    openVoiceMenu(e, {
      cur: dev ? 'device' : this.choice, serverOk: this.serverUsable && !!window.speechSynthesis, nasOk: !!info.local?.ok,
      voices: dev ? browserVoices(code).map(v => ({ id: v.name, name: v.name })) : serverVoices(info, this.code),
      curVoice: dev ? this.prefs.deviceVoices?.[code] : this.voice,
      onEngine: v => { this.pickChoice(v); this.updateUI() },
      onVoice: v => dev ? this.setDeviceVoice(v) : this.setVoice(v),
    })
  }

  setDeviceVoice(name) {
    const code = segLang('', this.code)
    this.prefs.deviceVoices = Object.assign({}, this.prefs.deviceVoices, { [code || 'zh']: name })
    save(PREFS_KEY, this.prefs)
    this.restartCurrent()
  }

  buildUI() {
    this.playBtn = iconBtn('pause', '暂停', () => this.toggle(), { class: 'icon-btn play' })
    this.speedBtn = h('button', { class: 'speed', title: '语速', onclick: e => pickSpeed(this, e) }, '1.0×')
    this.voiceBtn = h('button', { class: 'speed voice', title: '声音：读法、音色', onclick: e => this.voiceMenu(e) }, '在线')
    this.timerBtn = h('button', { class: 'icon-btn', title: '睡眠定时', onclick: () => this.cycleTimer() }, icon('timer'))
    this.timerText = h('span', { class: 'timer' })
    this.stateEl = h('span', { class: 'state' })
    this.el = h('div', { class: 'r-player', hidden: true, role: 'region', 'aria-label': '听书' },
      iconBtn('prevSent', '上一句', () => this.step(-1)),
      this.playBtn,
      iconBtn('nextSent', '下一句', () => this.step(1)),
      this.speedBtn, this.voiceBtn, this.timerBtn, this.timerText, this.stateEl, (this.readyEl = h('span', { class: 'state ready', title: '书库已经提前合成好的' })),
      iconBtn('close', '停止听书', () => this.stop()))
    this.el.addEventListener('click', e => e.stopPropagation())
    this.shell.root.append(this.el)
    this.updateUI()
  }

  updateUI() {
    this.playBtn.replaceChildren(icon(this.playing ? 'pause' : 'play'))
    this.playBtn.title = this.playing ? '暂停' : '继续'
    this.speedBtn.textContent = this.speed.toFixed(1) + '×'
    this.voiceBtn.textContent = this.mode === 'browser' ? '本机' : SRC_LABEL[this.prefs.src || 'auto']
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
    this.code = bookCode(this.shell, this.queue)
    this.prefetchAhead()
    watchReady(this, true)
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
    watchReady(this, false)
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

  // 这一节后面的句子交给书库提前合成（用书库语音时）
  prefetchAhead() {
    if (this.mode !== 'server' || !this.queue.length) return
    serverPrefetch(this.queue.slice(this.i + 1).map(s => ({ text: s.text, voice: srcVoice(this.prefs, voiceFor(this.prefs, this.info, segLang(s.text, this.code))) })), this.code, this.speed)
  }

  // 用户自己翻页后，从新位置接着读
  async jumpToView() {
    if (!this.active) return
    const was = this.playing
    this.halt()
    await this.load(true)
    this.prefetchAhead()
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
      // 每一句按它的文字挑声音：中英对照的书里英文句子用英文声音、中文句子用中文声音（各用这种语言选过的）
      const voice = srcVoice(this.prefs, voiceFor(this.prefs, this.info, segLang(text, this.code)))
      const p = api('/api/tts', { method: 'POST', raw: true, body: { text, voice, lang: this.code || '', speed: this.speed } })
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
    if (!window.speechSynthesis) { this.pause(); this.setState('朗读失败'); toast('这个浏览器没有自带的语音', { error: true }); return }
    speechSynthesis.cancel()
    // 一句里混着几种文字：切成几段，每段用那种语言的声音（中文用中文声音、英文用英文声音……），最后一段读完才算这句读完
    const token = (this.token = {})
    const any = (() => { try { return speechSynthesis.getVoices().length > 0 } catch { return false } })()
    const missing = []
    let last = null
    for (const run of mixedRuns(seg.text, this.code)) {
      const code = run.code
      const lang = BCP[code] || code
      const voices = browserVoices(code)
      // 这台设备没有这种语言的声音：跳过这一段（不拿英文声音硬读中文）；声音列表还没取到时照样交给浏览器按语言挑
      if (any && !voices.length) { missing.push(LANG_NAME[code] || code); continue }
      const u = new SpeechSynthesisUtterance(run.text)
      u.lang = lang
      // 自己选过的 → “自然”“增强”的声音（Edge 的 Natural、苹果的 Enhanced / Premium）→ 这种语言的第一个
      const mine = this.prefs.deviceVoices?.[code]
      const pick = voices.find(v => v.name === mine) || voices.find(v => /natural|neural|enhanced|premium|siri/i.test(v.name)) || voices.find(v => v.lang.replace('_', '-') === lang) || voices[0]
      if (pick) u.voice = pick
      u.rate = this.speed
      u.onerror = () => {}
      speechSynthesis.speak(u)
      last = u
    }
    const fresh = missing.filter(m => !(this.warned ||= new Set()).has(m))
    fresh.forEach(m => this.warned.add(m))
    if (fresh.length) toast(`这台设备没有${fresh.join('、')}的声音，这部分读不出来。可以在系统设置里装语音包，或者换成在线合成`, { duration: 6000 })
    if (!last) {
      // 一个字也读不了：跳过这句；连着好几句都读不了就停下（不然整本书一下子就跳完了）
      if (++this.unreadable >= 3) { this.unreadable = 0; this.pause(); this.setState('本机读不了这种文字'); return }
      setTimeout(() => { if (token === this.token) this.advance() }, 300)
      return
    }
    this.unreadable = 0
    last.onend = () => { if (token === this.token) this.advance() }
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
      this.prefetchAhead()
    }
    this.speakCurrent()
  }

  // ---------- 控制 ----------
  cycleSpeed() { this.setSpeed(SPEEDS[(SPEEDS.indexOf(this.speed) + 1) % SPEEDS.length]) }

  setSpeed(v) {
    this.prefs.speed = v
    save(PREFS_KEY, this.prefs)
    this.updateUI()
    this.restartCurrent()
  }

  setVoice(v) {
    rememberVoice(this.prefs, this.code, v)
    save(PREFS_KEY, this.prefs)
    this.restartCurrent()
  }

  restartCurrent() {
    for (const p of this.cache.values()) p.then(u => URL.revokeObjectURL(u)).catch(() => {})
    this.cache.clear()
    if (this.playing) { this.halt(); this.playing = true; this.speakCurrent() }
    else window.speechSynthesis?.cancel() // 暂停着换了读法、音色：继续时从这句重新读
    this.prefetchAhead() // 换了语速、音色、读法：按新的重新提前合成
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

  // 阅读设置里的“听书”部分：读法（在线合成 / 本机语音）、音色
  async sheetSection() {
    const info = await this.engineInfo()
    const parts = [h('h4', null, '听书')]
    const box = h('div')
    const ready = smallNote('')
    ready.hidden = true
    prefetchStatus().then(t => { ready.textContent = t; ready.hidden = !t })
    parts.push(box, ready)
    if (!this.code) this.code = bookCode(this.shell, await this.shell.engine?.ttsSegments?.(true).catch(() => []))
    const draw = () => {
      const items = []
      if (!this.serverUsable) {
        items.push(smallNote('书库设置成了“设备自带语音”，现在用这台设备的语音朗读。管理员可以在“设置 → 听书”里换成在线合成。'))
      } else {
        const cur = this.choice
        items.push(engineSeg(cur, v => { this.pickChoice(v); draw() }, !!info.local?.ok))
        items.push(smallNote(this.forceBrowser && cur !== 'device' ? '书库语音暂时用不了，现在临时用本机语音；点上面的“自动”重新试。' : ENGINE_NOTE[cur]))
      }
      if (this.mode === 'server') {
        if (info.voices?.length) items.push(voiceSelect(info, this.code, this.voice, v => this.setVoice(v)))
      } else {
        const code = segLang('', this.code)
        const list = browserVoices(code)
        if (list.length) {
          const mine = this.prefs.deviceVoices?.[code]
          const sel = h('select', { class: 'input', 'aria-label': '本机音色' },
            h('option', { value: '', selected: !mine }, '自动挑选'),
            list.map(v => h('option', { value: v.name, selected: v.name === mine }, v.name + (v.localService === false ? '（联网）' : ''))))
          sel.onchange = () => this.setDeviceVoice(sel.value)
          items.push(sel)
        } else if (window.speechSynthesis) {
          // 有的浏览器声音列表要过一会儿才有
          speechSynthesis.addEventListener?.('voiceschanged', () => { if (box.isConnected) draw() }, { once: true })
        }
      }
      fill(box, ...items)
    }
    draw()
    parts.push(h('div', { class: 'row', style: { gap: '8px', marginTop: '8px' } },
      h('button', { class: 'btn small', style: { flex: 1, background: 'transparent', borderColor: 'var(--r-rule)', color: 'inherit' }, onclick: () => { this.shell.closeOverlays(); this.active ? this.toggle() : this.start() } }, icon('headphones'), this.active ? (this.playing ? '暂停朗读' : '继续朗读') : '从这里开始朗读')))
    return parts
  }
}


// =====================================================================
// 安卓 App 里的听书：声音交给原生的后台服务播放（锁屏、切到别的 App、耳机按键、手表都能控制），
// 这里负责把正文切成句子交给它、高亮正在读的句子、翻页和记录进度。
// 后面的章节不经过排版直接从书里取文字，所以熄屏时也能接着读下去。
// =====================================================================

export class NativeNarrator {
  constructor(shell) {
    this.shell = shell
    this.prefs = store(PREFS_KEY, { speed: 1, voice: '', engine: '' })
    this.active = false
    this.playing = false
    this.segs = new Map()   // id → { key, i, text }
    this.order = []          // 已交给原生的句子 id
    this.lastKey = null
    this.timerMin = 0
    this.off = onNative('tts', e => this.onEvent(e))
    this.buildUI()
  }

  get speed() { return this.prefs.speed || 1 }

  buildUI() {
    this.playBtn = iconBtn('pause', '暂停', () => this.toggle(), { class: 'icon-btn play' })
    this.speedBtn = h('button', { class: 'speed', title: '语速', onclick: e => pickSpeed(this, e) }, '1.0×')
    this.voiceBtn = h('button', { class: 'speed voice', title: '声音：读法、音色', onclick: e => this.voiceMenu(e) }, '在线')
    this.timerBtn = h('button', { class: 'icon-btn', title: '睡眠定时', onclick: () => this.cycleTimer() }, icon('timer'))
    this.timerText = h('span', { class: 'timer' })
    this.stateEl = h('span', { class: 'state' })
    this.el = h('div', { class: 'r-player', hidden: true, role: 'region', 'aria-label': '听书' },
      iconBtn('prevSent', '上一句', () => call('tts', 'prev', '')),
      this.playBtn,
      iconBtn('nextSent', '下一句', () => call('tts', 'next', '')),
      this.speedBtn, this.voiceBtn, this.timerBtn, this.timerText, this.stateEl, (this.readyEl = h('span', { class: 'state ready', title: '书库已经提前合成好的' })),
      iconBtn('close', '停止听书', () => this.stop()))
    this.el.addEventListener('click', e => e.stopPropagation())
    this.shell.root.append(this.el)
    this.updateUI()
  }

  updateUI() {
    this.playBtn.replaceChildren(icon(this.playing ? 'pause' : 'play'))
    this.playBtn.title = this.playing ? '暂停' : '继续'
    this.speedBtn.textContent = this.speed.toFixed(1) + '×'
    this.voiceBtn.textContent = this.engine === 'device' ? '本机' : SRC_LABEL[this.prefs.src || 'auto']
    this.timerBtn.classList.toggle('on', !!this.timerMin)
    this.timerText.textContent = this.timerMin === -1 ? '本章' : this.timerMin > 0 ? this.timerMin + '分' : ''
  }

  // 一段（EPUB 的一节、PDF 的一页）的句子；第一段从当前看到的位置开始
  async firstUnit() {
    const eng = this.shell.engine
    const segs = await eng.ttsSegments(true)
    const key = eng.ttsUnitKey()
    const all = await eng.ttsSegments(false)
    const offset = Math.max(0, all.length - segs.length) // 从当前页开始时跳过了前面的句子
    return { key, segs: segs.map((s, n) => ({ text: s.text, i: n + offset })) }
  }

  // 交给原生的句子：带上位置（和进度同一种格式）和百分比，熄屏读到哪里原生自己记进度
  pack(unit) {
    const eng = this.shell.engine
    return unit.segs.map(s => {
      const id = `${unit.key}:${s.i}`
      this.segs.set(id, { key: unit.key, i: s.i, text: s.text })
      this.order.push(id)
      const loc = eng?.ttsLocation?.(unit.key, s.i)
      const o = loc ? { id, text: s.text, loc: loc.location, pct: Math.round((loc.fraction || 0) * 10000) / 10000 } : { id, text: s.text }
      // 每句按文字挑声音（中英对照的书里英文句子用英文声音）；旧版 App 不认这个字段，照样用整本的声音
      if (this.voiceInfo) o.voice = srcVoice(this.prefs, voiceFor(this.prefs, this.voiceInfo, segLang(s.text, this.code)))
      return o
    })
  }

  async start() {
    const eng = this.shell.engine
    if (!eng?.ttsSegments) { toast('漫画没有文字，不能朗读'); return }
    this.active = true
    this.el.hidden = false
    this.shell.root.classList.add('narrating')
    document.body.classList.add('narrating')
    this.setState('准备中…')
    this.segs.clear(); this.order = []
    let unit = await this.firstUnit()
    // 当前这一节已经读完（比如在一节的最后一页），从下一节开始
    while (unit && !unit.segs.length) unit = await this.shell.engine.ttsUnitAfter(unit.key)
    if (!unit) { this.stop(); toast('后面没有可以朗读的文字了'); return }
    this.lastKey = unit.key
    const info = this.shell.info || {}
    this.code = bookCode(this.shell, unit.segs)
    let voices = null
    try { voices = await voiceInfo() } catch (e) {}
    this.voiceInfo = voices && voices.defaults ? voices : null // 自定义接口的声音不分语言
    // 每种语言用哪个声音：App 熄屏后自己从书库取的句子也按文字挑
    const byLang = {}
    if (this.voiceInfo) for (const c of new Set(['zh', 'en', 'th', 'ja', 'ko', 'ru', 'fr', 'de', 'es', 'vi', this.code])) byLang[c] = srcVoice(this.prefs, voiceFor(this.prefs, voices, c))
    call('tts', 'start', {
      book: { id: this.shell.id, file_id: info.file_id || 0, title: info.title || '', author: info.author || '', cover: info.has_cover ? new URL(apiUrl(`/api/books/${this.shell.id}/cover?w=480`), location.href).href : '' },
      voice: srcVoice(this.prefs, voices ? voiceFor(this.prefs, voices, this.code) : (this.prefs.voice || '')), voices: byLang,
      deviceVoice: this.prefs.deviceVoice || '', // 手机自带语音选的音色（旧版 App 不认，用 voice） lang: this.code, speed: this.speed, segments: this.pack(unit),
      // 打开这本书时书库进度的版本：App 自己存进度时带上，别的设备读到了后面就不直接覆盖
      progressBase: this.shell.base ?? null,
    })
    this.prefetchPacked(this.order.map(id => this.segs.get(id)), srcVoice(this.prefs, voices ? voiceFor(this.prefs, voices, this.code) : (this.prefs.voice || '')))
    watchReady(this, true)
  }

  // 交给 App 的句子也请书库提前合成（App 用书库语音时；App 自己只提前几十句）
  prefetchPacked(list, fallbackVoice) {
    if ((callJSON('prefs') || {}).tts_engine === 'device') return
    this.prefVoice = fallbackVoice ?? this.prefVoice
    serverPrefetch(list.filter(Boolean).map(s => ({ text: s.text, voice: this.voiceInfo ? srcVoice(this.prefs, voiceFor(this.prefs, this.voiceInfo, segLang(s.text, this.code))) : (this.prefVoice || '') })), this.code, this.speed)
  }

  async onEvent(e) {
    if (!this.active && e.type !== 'state') return
    switch (e.type) {
      case 'seg': return this.onSeg(e.id, e)
      case 'need': return this.onNeed()
      case 'state':
        this.playing = !!e.playing
        if (e.timer !== undefined) this.timerMin = e.timer
        if (e.speed) { this.prefs.speed = e.speed; save(PREFS_KEY, this.prefs) }
        if (e.engine) this.engineNow = e.engine // 正在用的读法（连不上书库时 App 会临时改用手机语音）
        this.setState(e.status || '')
        this.updateUI()
        return
      case 'error': toast(e.message || '朗读出错了', { error: true, duration: 5000 }); return
      case 'stopped': this.cleanup(); if (e.reason) toast(e.reason); return
    }
  }

  async onSeg(id, e) {
    const seg = this.segs.get(id)
    if (!seg) {
      // 网页被冻结、回收期间后台从书库接着读的句子：记下位置，看着书时翻过去
      if (e?.loc) {
        this.shell.saveLocation(e.loc, e.pct)
        if (document.visibilityState === 'visible' && e.loc !== this.lastFeedLoc) {
          this.lastFeedLoc = e.loc
          try { await this.shell.engine.goTo?.(e.loc) } catch (err) {}
        }
      }
      return
    }
    this.cur = seg
    this.shell.markActivity?.()
    const eng = this.shell.engine
    if (document.visibilityState === 'visible') {
      // 读到了还没显示的那一节：翻过去再高亮
      if (eng.ttsUnitKey() !== seg.key) await eng.ttsShowUnit(seg.key)
      const segs = await eng.ttsSegmentsOf(seg.key)
      const s = segs[seg.i]
      if (s) eng.ttsHighlight(s)
      const loc = eng.ttsLocation?.(seg.key, seg.i)
      if (loc && eng.kind === 'pdf') this.shell.saveLocation(loc.location, loc.fraction)
    } else {
      // 熄屏或在后台：不排版，只记下位置，回来时从这里接着看
      const loc = eng.ttsLocation?.(seg.key, seg.i)
      if (loc) this.shell.saveLocation(loc.location, loc.fraction)
    }
  }

  async onNeed() {
    if (this.loadingMore) return
    this.loadingMore = true
    try {
      const unit = await this.shell.engine.ttsUnitAfter(this.lastKey)
      if (!unit) { call('tts', 'end', ''); return }
      this.lastKey = unit.key
      if (this.timerMin === -1) { call('tts', 'end', ''); return } // 读完本章就停
      const packed = this.pack(unit)
      call('tts', 'append', { segments: packed })
      this.prefetchPacked(packed)
    } catch (e) { console.error(e); call('tts', 'end', '') } finally { this.loadingMore = false }
  }

  // 用户自己翻页后，从新位置接着读
  async jumpToView() { if (this.active) this.start() }

  toggle() { call('tts', this.playing ? 'pause' : 'resume', '') }
  play() { call('tts', 'resume', '') }
  pause() { call('tts', 'pause', '') }

  cycleSpeed() { this.setSpeed(SPEEDS[(SPEEDS.indexOf(this.speed) + 1) % SPEEDS.length]) }

  setSpeed(v) {
    this.prefs.speed = v
    save(PREFS_KEY, this.prefs)
    call('tts', 'speed', String(v))
    this.updateUI()
  }

  cycleTimer() {
    const i = TIMERS.indexOf(this.timerMin)
    this.timerMin = TIMERS[(i + 1) % TIMERS.length]
    call('tts', 'timer', String(this.timerMin))
    this.updateUI()
    toast(this.timerMin === 0 ? '已关闭定时' : this.timerMin === -1 ? '读完本章后停止' : `${this.timerMin} 分钟后停止`, { duration: 1500 })
  }

  setVoice(v, device) {
    if (device) this.prefs.deviceVoice = v // 手机自带语音：不分语言，和书库的声音分开记
    else rememberVoice(this.prefs, this.code, v)
    save(PREFS_KEY, this.prefs)
    call('tts', device ? 'deviceVoice' : 'voice', v)
  }

  setState(t) { this.stateEl.textContent = t || '' }

  stop() { call('tts', 'stop', ''); this.cleanup() }

  cleanup() {
    watchReady(this, false)
    this.active = false
    this.playing = false
    this.shell.engine?.ttsClear?.()
    this.el.hidden = true
    this.shell.root.classList.remove('narrating')
    document.body.classList.remove('narrating')
    this.timerMin = 0
    this.updateUI()
    this.setState('')
  }

  destroy() { if (this.active) call('tts', 'stop', ''); this.cleanup(); this.off(); this.el.remove() }

  // 现在用的读法：正在听时以 App 报来的为准（连不上书库时会临时用手机语音），不然看设置
  get engine() {
    if (this.active && this.engineNow) return this.engineNow
    return (callJSON('prefs') || {}).tts_engine === 'device' ? 'device' : 'server'
  }

  // 读法四选一（auto / ms / nas / device）：device 是 App 的设置，前三种是这台设备选的书库合成来源
  get choice() { return this.engine === 'device' ? 'device' : (this.prefs.src || 'auto') }

  pickChoice(v) {
    if (v === 'device') return this.setEngine('device')
    this.prefs.src = v === 'auto' ? '' : v
    save(PREFS_KEY, this.prefs)
    toast({ auto: '书库自动挑声音', ms: '先用微软合成', nas: '先用 NAS 本地语音' }[v], { duration: 1500 })
    if ((callJSON('prefs') || {}).tts_engine === 'device') call('setPref', 'tts_engine', 'server')
    // 正在听：从这里按新的来源重新开始读（交给 App 的句子带着来源）
    if (this.active) { call('tts', 'stop', ''); this.cleanup(); this.start() }
    else this.updateUI()
  }

  // 换读法：记在 App 里（“设置 → 手机 App”里也是这一项），正在听的话马上换
  setEngine(v) {
    call('setPref', 'tts_engine', v)
    toast(v === 'device' ? '改用手机自带语音朗读' : '改用书库在线合成', { duration: 1500 })
    if (!this.active) { this.updateUI(); return }
    this.engineNow = v
    // 1.0.0.24 起的 App 能在听的时候直接换；更早的不认这个命令，停下来从这里重新开始读（开始时按新的读法）
    if ((callJSON('appInfo') || {}).versionCode >= 24) call('tts', 'engine', v)
    else { call('tts', 'stop', ''); this.cleanup(); this.start() }
    this.updateUI()
  }

  async voiceMenu(e) {
    const dev = this.engine === 'device'
    let info = null
    if (!dev) { try { info = await voiceInfo() } catch {} }
    if (!this.code) this.code = bookCode(this.shell, await this.shell.engine?.ttsSegments?.(true).catch(() => []))
    if (!info) { try { info = await voiceInfo() } catch {} }
    openVoiceMenu(e, {
      cur: this.choice, nasOk: !!info?.local?.ok,
      voices: dev ? (callJSON('ttsVoices') || []) : serverVoices(info, this.code),
      curVoice: dev ? this.prefs.deviceVoice : voiceFor(this.prefs, info, this.code),
      onEngine: v => this.pickChoice(v),
      onVoice: v => this.setVoice(v, dev),
    })
  }

  async sheetSection() {
    const parts = [h('h4', null, '听书')]
    const box = h('div')
    const ready = smallNote('')
    ready.hidden = true
    prefetchStatus().then(t => { ready.textContent = t; ready.hidden = !t })
    parts.push(box, ready)
    let info = null
    try { info = await voiceInfo() } catch {}
    if (!this.code) this.code = bookCode(this.shell, await this.shell.engine?.ttsSegments?.(true).catch(() => []))
    const draw = () => {
      const app = callJSON('prefs') || {}
      const cur = app.tts_engine === 'device' ? 'device' : (this.prefs.src || 'auto')
      const items = [engineSeg(cur, v => { this.pickChoice(v); draw() }, !!info?.local?.ok)]
      if (cur !== 'device' && this.active && this.engineNow === 'device') items.push(smallNote('书库语音暂时用不了，现在临时用手机语音；网络好了点“自动”重新试。'))
      else if (cur !== 'device' && !info && net.route === 'offline') items.push(smallNote('现在连不上书库，听书时先用手机自带语音。'))
      else items.push(smallNote(ENGINE_NOTE[cur] + (cur === 'device' ? '' : '锁屏、切到别的 App 也会继续读。')))
      if (cur === 'device') {
        const voices = callJSON('ttsVoices') || []
        if (voices.length) {
          const sel = h('select', { class: 'input', 'aria-label': '音色' }, voices.map(v => h('option', { value: v.id, selected: v.id === this.prefs.deviceVoice }, v.name)))
          sel.onchange = () => this.setVoice(sel.value, true)
          items.push(sel)
        }
      } else if (info?.voices?.length) {
        items.push(voiceSelect(info, this.code, voiceFor(this.prefs, info, this.code), v => this.setVoice(v)))
      }
      fill(box, ...items)
    }
    draw()
    parts.push(h('div', { class: 'row', style: { gap: '8px', marginTop: '8px' } },
      h('button', { class: 'btn small', style: { flex: 1, background: 'transparent', borderColor: 'var(--r-rule)', color: 'inherit' }, onclick: () => { this.shell.closeOverlays(); this.active ? this.toggle() : this.start() } }, icon('headphones'), this.active ? (this.playing ? '暂停朗读' : '继续朗读') : '从这里开始朗读')))
    return parts
  }
}
