// 翻译：交给书库（POST /api/translate，微软翻译或 AI，书库那边翻过的段落有缓存）。
// 这里再在内存里记一份：同一段（同一种目标语言）这次打开书库只请求一次。
// 一次最多发 40 段、1.2 万字，多了分几次发（书库一次最多收 200 段）

import { api } from '../api.js'

// 能译成的语言（和书库 server/translate.go 的 translateLangs 一致）
export const LANGS = [
  ['zh-Hans', '简体中文'], ['zh-Hant', '繁體中文'], ['en', 'English'], ['ja', '日本語'], ['ko', '한국어'],
  ['fr', 'Français'], ['de', 'Deutsch'], ['es', 'Español'], ['ru', 'Русский'], ['th', 'ภาษาไทย'],
]
const KEY = 'shuku.translateTo'
let serverDefault = null

// 译成哪种语言：自己在阅读器里选过的 > 书库设置里的默认语言 > 简体中文
export function targetLang() {
  try {
    const v = localStorage.getItem(KEY)
    if (v && LANGS.some(l => l[0] === v)) return v
  } catch (e) {}
  return serverDefault || 'zh-Hans'
}

export function setTargetLang(v) {
  try { localStorage.setItem(KEY, v) } catch (e) {}
}

// 书库设置里的默认语言（家人账号也能读到）。只问一次
export async function loadDefaultLang() {
  if (serverDefault) return serverDefault
  try {
    const s = await api('/api/settings')
    const to = s && s.translate && s.translate.to
    if (to && LANGS.some(l => l[0] === to)) serverDefault = to
  } catch (e) {}
  return targetLang()
}

export const langName = v => (LANGS.find(l => l[0] === v) || [v, v])[1]

// 语言下拉框（阅读设置、PDF 的对照面板里用）
export function langOptions(cur, h) {
  return LANGS.map(([v, l]) => h('option', { value: v, selected: v === cur }, l))
}

const cache = new Map() // 目标语言 + 原文 → 译文
const MAX_BATCH = 40
const MAX_CHARS = 12000

// 一批原文 → 译文（顺序一一对应）。翻译失败时抛出错误（书库给的原因）
export async function translateTexts(texts, to) {
  const key = t => to + '\u0001' + t
  const need = []
  const seen = new Set()
  for (const t of texts) {
    if (!t || cache.has(key(t)) || seen.has(t)) continue
    seen.add(t)
    need.push(t)
  }
  let batch = [], chars = 0
  const send = async () => {
    if (!batch.length) return
    const list = batch
    batch = []; chars = 0
    const r = await api('/api/translate', { method: 'POST', body: { texts: list, to } })
    const out = (r && r.translations) || []
    list.forEach((t, i) => { if (typeof out[i] === 'string') cache.set(key(t), out[i]) })
  }
  for (const t of need) {
    if (batch.length >= MAX_BATCH || (batch.length && chars + t.length > MAX_CHARS)) await send()
    batch.push(t)
    chars += t.length
  }
  await send()
  // 内存里最多记 5000 段，多了丢掉最早的
  if (cache.size > 5000) {
    let n = cache.size - 4000
    for (const k of cache.keys()) { if (n-- <= 0) break; cache.delete(k) }
  }
  return texts.map(t => (t && cache.get(key(t))) || '')
}

// ---------- 要不要翻译：已经是目标语言的、没有字的（纯数字、标点）跳过 ----------

function scripts(text) {
  const n = { han: 0, kana: 0, hangul: 0, latin: 0, cyrillic: 0, thai: 0, other: 0 }
  for (let i = 0; i < text.length; i++) {
    const c = text.charCodeAt(i)
    if ((c >= 0x4E00 && c <= 0x9FFF) || (c >= 0x3400 && c <= 0x4DBF) || (c >= 0xF900 && c <= 0xFAFF) || (c >= 0xD840 && c <= 0xD87F)) n.han++
    else if (c >= 0x3040 && c <= 0x30FF) n.kana++
    else if ((c >= 0xAC00 && c <= 0xD7AF) || (c >= 0x1100 && c <= 0x11FF) || (c >= 0x3130 && c <= 0x318F)) n.hangul++
    else if ((c >= 0x41 && c <= 0x5A) || (c >= 0x61 && c <= 0x7A) || (c >= 0xC0 && c <= 0x24F)) n.latin++
    else if (c >= 0x400 && c <= 0x4FF) n.cyrillic++
    else if (c >= 0xE00 && c <= 0xE7F) n.thai++
    else if (c > 0x2FF && !(c >= 0x2000 && c <= 0x2BFF) && !(c >= 0x3000 && c <= 0x303F) && !(c >= 0xFF00 && c <= 0xFFEF)) n.other++
  }
  n.total = n.han + n.kana + n.hangul + n.latin + n.cyrillic + n.thai + n.other
  return n
}

const EN_WORDS = /\b(the|and|of|to|is|in|that|it|was|for|with|as|he|she|you|this|are|be|not|have|but|on)\b/gi

// text：一段原文；to：目标语言；bookLang：书的语言（元数据里的，可能没有）
export function needsTranslation(text, to, bookLang) {
  const n = scripts(text || '')
  if (n.total < 1) return false // 只有数字、标点、空白
  const r = k => n[k] / n.total
  if (to === 'zh-Hans' || to === 'zh-Hant') return !(r('han') >= 0.6 && n.kana === 0)
  if (to === 'ja') return !(n.kana > 0 && r('han') + r('kana') >= 0.6)
  if (to === 'ko') return r('hangul') < 0.5
  if (to === 'ru') return r('cyrillic') < 0.6
  if (to === 'th') return r('thai') < 0.5
  // 拉丁字母的几种：书本来就是这种语言就不翻；英文再看看常用词
  if (r('latin') < 0.8) return true
  const bl = String(bookLang || '').toLowerCase()
  if (bl && (bl === to || bl.indexOf(to + '-') === 0)) return false
  if (to === 'en') {
    const words = (text.match(/[a-z]+/gi) || []).length
    const common = (text.match(EN_WORDS) || []).length
    return !(words >= 3 && common / words >= 0.12)
  }
  return true
}
