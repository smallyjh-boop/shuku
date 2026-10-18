// 用旧版 Chromium 打开书库，检查兼容旧系统的那一套网页（模拟墨案 inkPad 这类旧安卓的网页组件）。
//
//   node tools/legacy/test-old-chrome.mjs <旧版 chromium 路径> <书库地址> <登录令牌> [截图目录] [--strip]
//
// 旧版 Chromium：npm 上 chrome-aws-lambda@64.0.3264 包里的 bin/chromium.br（Chrome 64 的 headless_shell，brotli 压缩），
// 用 node -e "require('zlib').brotliDecompressSync(...)" 解开。
// --strip：再删掉 Chrome 58～63 没有的功能（ResizeObserver、Promise.finally、元素的 scrollTo、DOMMatrix），检查补上的那些能用。
// 走一遍：书架、EPUB 翻页、TXT、PDF（含翻页）、漫画；页面报的错全部打印出来，有错就退出码 1。

import { spawn } from 'node:child_process'
import fs from 'node:fs'
import path from 'node:path'

const [bin, base, token, shotsArg] = process.argv.slice(2).filter(a => !a.startsWith('--'))
const strip = process.argv.includes('--strip')
if (!bin || !base || !token) {
  console.log('用法：node test-old-chrome.mjs <chromium> <书库地址> <令牌> [截图目录] [--strip]')
  process.exit(2)
}
const shots = shotsArg || path.join(process.cwd(), 'old-chrome-shots')
fs.mkdirSync(shots, { recursive: true })
const port = 9300 + Math.floor(Math.random() * 500)
const ud = fs.mkdtempSync('/tmp/shuku-oldchrome-')
const proc = spawn(bin, ['--headless', '--no-sandbox', '--disable-gpu', '--no-zygote', '--single-process', '--disable-dev-shm-usage',
  '--hide-scrollbars', `--remote-debugging-port=${port}`, `--user-data-dir=${ud}`, 'about:blank'], { stdio: ['ignore', 'ignore', 'pipe'] })
let chromeLog = ''
proc.stderr.on('data', d => { chromeLog += d })
const sleep = ms => new Promise(r => setTimeout(r, ms))
let failures = 0
const fail = (msg) => { failures++; console.log('  ✗ ' + msg) }
const ok = (msg) => console.log('  ✓ ' + msg)

let ver
for (let i = 0; i < 60 && !ver; i++) { await sleep(250); try { ver = await (await fetch(`http://127.0.0.1:${port}/json/version`)).json() } catch {} }
if (!ver) { console.log(chromeLog.slice(-3000)); process.exit(1) }
console.log('浏览器：' + ver.Browser + (strip ? '（去掉 Chrome 58～63 没有的功能）' : ''))
const target = (await (await fetch(`http://127.0.0.1:${port}/json/list`)).json()).find(t => t.type === 'page')
const ws = new WebSocket(target.webSocketDebuggerUrl)
await new Promise((r, j) => { ws.onopen = r; ws.onerror = j })
let seq = 0
const pending = new Map()
const errors = []
ws.onmessage = e => {
  const m = JSON.parse(e.data)
  if (m.id && pending.has(m.id)) { pending.get(m.id)(m); pending.delete(m.id); return }
  if (m.method === 'Runtime.exceptionThrown') {
    const d = m.params.exceptionDetails
    errors.push('异常：' + ((d.exception && d.exception.description) || d.text) + ` @ ${d.url || ''}:${d.lineNumber}:${d.columnNumber}`)
  } else if (m.method === 'Runtime.consoleAPICalled' && (m.params.type === 'error' || m.params.type === 'warning')) {
    errors.push(`console.${m.params.type}：` + m.params.args.map(a => a.value !== undefined ? a.value : a.description).join(' '))
  } else if (m.method === 'Log.entryAdded' && m.params.entry.level === 'error') {
    errors.push('日志：' + m.params.entry.text + (m.params.entry.url ? ' ' + m.params.entry.url : ''))
  }
}
const send = (method, params = {}) => new Promise((resolve, reject) => {
  const id = ++seq
  pending.set(id, m => m.error ? reject(new Error(method + '：' + m.error.message)) : resolve(m.result))
  ws.send(JSON.stringify({ id, method, params }))
})
const evaluate = async (expr) => {
  const r = await send('Runtime.evaluate', { expression: expr, returnByValue: true, awaitPromise: true })
  if (r.exceptionDetails) throw new Error((r.exceptionDetails.exception && r.exceptionDetails.exception.description) || r.exceptionDetails.text)
  return r.result.value
}
const waitFor = async (expr, ms = 30000, what = expr) => {
  const t0 = Date.now()
  while (Date.now() - t0 < ms) {
    try { if (await evaluate(expr)) return true } catch {}
    await sleep(300)
  }
  throw new Error('等不到：' + what)
}
const shot = async (name) => {
  const r = await send('Page.captureScreenshot', { format: 'png' })
  fs.writeFileSync(path.join(shots, name + '.png'), Buffer.from(r.data, 'base64'))
}
const key = async (k, code) => {
  for (const type of ['rawKeyDown', 'keyUp']) await send('Input.dispatchKeyEvent', { type, key: k, code: k, windowsVirtualKeyCode: code, nativeVirtualKeyCode: code })
}
// 清掉已经打印过的错，看这一步有没有新的（旧浏览器不认识 CSP 的 wasm-unsafe-eval、测试环境里的 Tailscale 地址连不上，不算）
const IGNORE = /wasm-unsafe-eval|ts\.net\/api\/ping|ERR_TUNNEL_CONNECTION_FAILED|ERR_NAME_NOT_RESOLVED/
const flushErrors = (step) => {
  const list = errors.splice(0).filter(e => !IGNORE.test(e))
  for (const e of list) fail(`${step}：${e}`)
  return list.length === 0
}

await send('Runtime.enable')
await send('Log.enable')
await send('Page.enable')
await send('Network.enable')
// 模拟墨案 inkPad X：10 寸竖屏，安卓 8 的网页组件 + 书库 App 的标记（墨水屏模式）
await send('Network.setUserAgentOverride', { userAgent: ver['User-Agent'].replace('HeadlessChrome', 'Chrome') + ' ShukuApp/9.9 ShukuEInk' })
await send('Emulation.setDeviceMetricsOverride', { width: 702, height: 936, deviceScaleFactor: 1, mobile: true })
await send('Page.addScriptToEvaluateOnNewDocument', {
  source: `localStorage.getItem('shuku.token') || localStorage.setItem('shuku.token', ${JSON.stringify(token)});` + (strip ? `
    delete window.ResizeObserver; delete Promise.prototype.finally; delete Element.prototype.scrollTo; delete Element.prototype.scrollBy;
    delete window.DOMMatrix; delete window.DOMMatrixReadOnly;` : ''),
})

const api = async (p) => (await fetch(base + p, { headers: { Authorization: 'Bearer ' + token } })).json()
const books = (await api('/api/books?limit=200&sort=title')).items || []
const pick = (re) => books.find(b => re.test([].concat(b.formats || []).join(',')))
console.log(`书库里有 ${books.length} 本书`)

async function step(name, fn) {
  try { await fn(); if (flushErrors(name)) ok(name) } catch (e) { fail(`${name}：${e.message}`); flushErrors(name); await shot('fail-' + name).catch(() => {}) }
}

await step('打开书库（兼容旧系统的那一套）', async () => {
  await send('Page.navigate', { url: base + '/#/all' })
  await waitFor("document.querySelector('#app') && document.querySelector('#app').children.length > 0", 40000, '网页启动')
  const legacy = await evaluate('!!window.SHUKU_LEGACY')
  if (!legacy) throw new Error('没有用兼容旧系统的那一套')
  const eink = await evaluate("document.documentElement.classList.contains('eink')")
  if (!eink) throw new Error('没有打开墨水屏模式')
  await waitFor("document.querySelectorAll('.book, .card, [data-id]').length > 0", 20000, '书架上的书')
  await sleep(1500)
  await shot('01-library')
})

async function openReader(name, book, after) {
  if (!book) { fail(name + '：书库里没有这种书'); return }
  await step(name, async () => {
    await evaluate(`location.hash = '#/read/${book.id}'`)
    await waitFor("document.querySelector('.reader') && !document.querySelector('.reader .r-loading')", 40000, '阅读器打开')
    const msg = await evaluate("(document.querySelector('.reader .r-fail, .reader .r-error') || {}).textContent || ''")
    if (msg) throw new Error('阅读器报错：' + msg)
    await sleep(2500)
    await shot(name + '-1')
    if (after) await after()
  })
}

const epub = pick(/epub/i)
await openReader('epub', epub, async () => {
  const text = await evaluate("(() => { const v = document.querySelector('foliate-view'); const docs = v && v.renderer && v.renderer.getContents ? v.renderer.getContents() : []; return docs.map(d => d.doc.body.textContent).join('').length })()")
  if (!(text > 20)) throw new Error('EPUB 正文没有显示出来（' + text + '）')
  await key('ArrowRight', 39); await sleep(1500); await shot('epub-2')
  await key('PageDown', 34); await sleep(1500); await shot('epub-3')
})
await openReader('txt', pick(/txt/i), async () => { await key('ArrowRight', 39); await sleep(1500); await shot('txt-2') })
await openReader('pdf', pick(/pdf/i), async () => {
  await waitFor("[...document.querySelectorAll('.reader canvas')].some(c => c.width > 50)", 30000, 'PDF 画出来')
  await sleep(1500)
  await shot('pdf-2')
  const t = await evaluate("[...document.querySelectorAll('.reader .textLayer')].map(x => x.textContent).join('').length")
  if (!(t > 5)) throw new Error('PDF 文字层是空的')
})
await openReader('comic', pick(/cbz|cbr|zip/i), async () => { await key('ArrowLeft', 37); await sleep(1500); await shot('comic-2') })

await step('回到书架', async () => {
  await evaluate("location.hash = '#/'")
  await waitFor("!document.querySelector('.reader')", 10000, '退出阅读器')
  await sleep(1000)
  await shot('99-home')
})

ws.close()
proc.kill('SIGKILL')
fs.rmSync(ud, { recursive: true, force: true })
console.log(failures ? `\n${failures} 项有问题，截图在 ${shots}` : `\n全部通过，截图在 ${shots}`)
process.exit(failures ? 1 : 0)
