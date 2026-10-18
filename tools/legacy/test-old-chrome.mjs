// 用旧版 Chromium 打开书库，检查兼容旧系统的那一套网页（模拟墨案 inkPad 这类旧安卓的网页组件）。
//
//   node tools/legacy/test-old-chrome.mjs <旧版 chromium 路径> <书库地址> <登录令牌> [截图目录] [--strip] [--samples] [--app] [--size=宽x高]
//
// 旧版 Chromium：npm 上 chrome-aws-lambda@64.0.3264 包里的 bin/chromium.br（Chrome 64 的 headless_shell，brotli 压缩），
// 用 node -e "require('zlib').brotliDecompressSync(...)" 解开。
// --strip：再删掉 Chrome 58～63 没有的功能（ResizeObserver、Promise.finally、元素的 scrollTo、DOMMatrix），检查补上的那些能用。
// --samples：先往书库里放几本测试书（EPUB、TXT、PDF、漫画，这里现做）并记上进度（“继续阅读”那一排），打包时在空的书库上检查用。
// --app：模拟书库 App 的原生桥（设置存在原生里、原生告诉网页是墨水屏和屏幕尺寸），和在 App 里打开时走一样的路。
// 走一遍：书架、EPUB 翻页、TXT、PDF（含翻页）、漫画，最后换几种屏幕尺寸再看书架和 EPUB；
// 页面报的错全部打印出来，页面比屏幕宽（能左右拖、阅读器排到屏幕外）也算错，有错就退出码 1。

import { spawn } from 'node:child_process'
import fs from 'node:fs'
import path from 'node:path'
import zlib from 'node:zlib'

const [bin, base, token, shotsArg] = process.argv.slice(2).filter(a => !a.startsWith('--'))
const strip = process.argv.includes('--strip')
const samples = process.argv.includes('--samples')
const appBridge = process.argv.includes('--app')
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
console.log('浏览器：' + ver.Browser + (strip ? '（去掉 Chrome 58～63 没有的功能）' : '') + (appBridge ? '，模拟书库 App' : ''))
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
// --size=宽x高：模拟的屏幕（网页像素），默认是墨案 inkPad X 实测的 936x1248（10 寸、密度 1.5，电脑那种带侧栏的排版）
const sizeArg = (process.argv.find(a => a.startsWith('--size=')) || '--size=936x1248').slice(7).split('x').map(Number)
await send('Emulation.setDeviceMetricsOverride', { width: sizeArg[0], height: sizeArg[1], deviceScaleFactor: 1, mobile: true })
await send('Page.addScriptToEvaluateOnNewDocument', {
  source: `localStorage.getItem('shuku.token') || localStorage.setItem('shuku.token', ${JSON.stringify(token)});` + (strip ? `
    delete window.ResizeObserver; delete Promise.prototype.finally; delete Element.prototype.scrollTo; delete Element.prototype.scrollBy;
    delete window.DOMMatrix; delete window.DOMMatrixReadOnly;` : '') + (appBridge ? `
    (function () {
      // 模拟书库 App 的原生桥（App 里网页走的是这一路：设置存在原生、墨水屏和屏幕尺寸由原生告诉网页）
      var ls = window.localStorage
      window.ShukuNative = {
        storageGet: function (k) { return ls.getItem(k) }, storageSet: function (k, v) { ls.setItem(k, v) }, storageRemove: function (k) { ls.removeItem(k) },
        storageKeys: function () { var a = []; for (var i = 0; i < ls.length; i++) a.push(ls.key(i)); return JSON.stringify(a) },
        version: function () { return '9.9' }, deviceName: function () { return '旧浏览器测试' }, isEInk: function () { return true },
        deviceInfo: function () { return JSON.stringify({ brand: 'Moaan', model: 'inkPad X', android: '8.1.0', webview: '64.0.3264.0', eink: true, abi: 'armeabi-v7a', screen: { w: 1404, h: 1872, density: 1.5, dpi: 240, inches: 10.3 } }) },
        downloads: function () { return '[]' }, prefs: function () { return '{}' }
      }
    })();` : ''),
})

const api = async (p) => (await fetch(base + p, { headers: { Authorization: 'Bearer ' + token } })).json()
const t0 = Math.floor(Date.now() / 1000) - 5
if (samples) await addSamples()
const books = (await api('/api/books?limit=200&sort=title')).items || []
// 放了测试书时优先用刚放进去的（书库里原有的书可能是扫描版 PDF 之类，没有文字层）
const fmtOf = b => [].concat(b.formats || []).join(',')
const pick = (re, ok = () => true) => (samples && books.find(b => re.test(fmtOf(b)) && ok(b) && b.created_at >= t0)) || books.find(b => re.test(fmtOf(b)) && ok(b))
console.log(`书库里有 ${books.length} 本书`)

async function step(name, fn) {
  try { await fn(); if (flushErrors(name)) ok(name) } catch (e) { fail(`${name}：${e.message}`); flushErrors(name); await shot('fail-' + name).catch(() => {}) }
}

// 页面不能比屏幕宽：旧的网页组件会把整页撑宽（能左右拖），阅读器跟着排到屏幕外面，一片空白。
// 列出跨过屏幕右边的元素（在能左右滑的容器里面的不算）
async function checkLayout(where, W) {
  const r = await evaluate(`(() => {
    const W = ${W}, out = []
    const scroller = el => { for (let p = el.parentElement; p && p !== document.body; p = p.parentElement) { const o = getComputedStyle(p).overflowX; if (o !== 'visible') return true } return false }
    for (const el of document.querySelectorAll('body *')) {
      const b = el.getBoundingClientRect()
      if (b.width > 0 && b.left < W - 1 && b.right > W + 1 && !scroller(el))
        out.push(el.tagName.toLowerCase() + (typeof el.className === 'string' && el.className.trim() ? '.' + el.className.trim().split(/\\s+/).join('.') : '') + ' 到 ' + Math.round(b.right))
    }
    return { iw: innerWidth, sw: document.documentElement.scrollWidth, out: out.slice(0, 6) }
  })()`)
  if (r.iw !== W || r.sw > W + 1 || r.out.length) throw new Error(`${where}比屏幕宽（屏幕 ${W}，网页 ${r.iw}，内容 ${r.sw}）${r.out.length ? '：' + r.out.join('；') : ''}`)
}

// 阅读器要和屏幕一样宽；EPUB、TXT 竖着拿（高大于宽）时一页一栏（旧浏览器没有容器查询，靠 build.mjs 改的 paginator）
async function checkReader(W, H) {
  const r = await evaluate(`(() => {
    const rd = document.querySelector('.reader').getBoundingClientRect()
    const v = document.querySelector('foliate-view')
    const d = v && v.renderer && v.renderer.getContents ? v.renderer.getContents()[0] : null
    let cols = 0
    if (d) { const cw = parseFloat(d.doc.defaultView.getComputedStyle(d.doc.documentElement).columnWidth); if (cw > 0) cols = cw > v.getBoundingClientRect().width / 2 ? 1 : 2 }
    return { iw: innerWidth, rw: Math.round(rd.width), cols, paged: !!(v && v.renderer && v.renderer.getAttribute('flow') !== 'scrolled') }
  })()`)
  if (r.iw !== W || r.rw > W + 1) throw new Error(`阅读器比屏幕宽（屏幕 ${W}，网页 ${r.iw}，阅读器 ${r.rw}）`)
  if (r.paged && H > W && r.cols === 2) throw new Error('竖屏排成了两栏')
  return r
}

await step('打开书库（兼容旧系统的那一套）', async () => {
  await send('Page.navigate', { url: base + '/#/' })
  await waitFor("document.querySelector('#app') && document.querySelector('#app').children.length > 0", 40000, '网页启动')
  const legacy = await evaluate('!!window.SHUKU_LEGACY')
  if (!legacy) throw new Error('没有用兼容旧系统的那一套')
  const eink = await evaluate("document.documentElement.classList.contains('eink')")
  if (!eink) throw new Error('没有打开墨水屏模式')
  await waitFor("document.querySelectorAll('.book, .card, [data-id]').length > 0", 20000, '书架上的书')
  await sleep(1500)
  await shot('01-library')
  await checkLayout('书架', sizeArg[0])
})

async function openReader(name, book, after) {
  if (!book) { console.log(`  - ${name}：书库里没有这种书，跳过`); return }
  await step(name, async () => {
    await evaluate(`location.hash = '#/read/${book.id}'`)
    await waitFor("document.querySelector('.reader') && !document.querySelector('.reader .r-loading')", 40000, '阅读器打开')
    const msg = await evaluate("(document.querySelector('.reader .r-fail, .reader .r-error') || {}).textContent || ''")
    if (msg) throw new Error('阅读器报错：' + msg)
    await sleep(2500)
    await shot(name + '-1')
    await checkReader(sizeArg[0], sizeArg[1])
    if (after) await after()
  })
}

const epub = pick(/epub/i, b => !/整本/.test(b.title))
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
// 整本写在一个文件里的大章节：书库拆成几段（server/zipsplit.go），只取要看的那一段；跳到后面的段也能显示
await openReader('big', samples ? books.find(b => /整本/.test(b.title)) : null, async () => {
  const n = await evaluate("document.querySelector('foliate-view').book.sections.length")
  if (!(n >= 3)) throw new Error('大章节没有拆开（' + n + ' 节）')
  await evaluate("document.querySelector('foliate-view').goToFraction(0.7)")
  await waitFor("(() => { const v = document.querySelector('foliate-view'); const c = v.renderer.getContents()[0]; return c && c.index > 0 && c.doc.body.textContent.indexOf('第') >= 0 })()", 30000, '跳到后面的段')
  await sleep(1500)
  await shot('big-2')
})

await step('回到书架', async () => {
  await evaluate("location.hash = '#/'")
  await waitFor("!document.querySelector('.reader')", 10000, '退出阅读器')
  await sleep(1000)
  await shot('99-home')
  await checkLayout('书架', sizeArg[0])
})

// 换几种屏幕再看书架和 EPUB：6 寸（600x800）、10 寸密度 2（702x936，手机那种排版）、横着拿（1248x936）
for (const [w, hh] of [[600, 800], [702, 936], [1248, 936]]) {
  if (w === sizeArg[0] && hh === sizeArg[1]) continue
  await step(`屏幕 ${w}x${hh}`, async () => {
    await send('Emulation.setDeviceMetricsOverride', { width: w, height: hh, deviceScaleFactor: 1, mobile: true })
    await evaluate("window.__beforeReload = 1; location.hash = '#/'")
    await send('Page.reload')
    // 等新页面（重新加载前的旧页面还在的那一下不算）
    await waitFor("!window.__beforeReload && document.querySelector('#app') && document.querySelector('#app').children.length > 0 && document.querySelectorAll('.book, .card, [data-id]').length > 0", 40000, '书架')
    await sleep(1500)
    await shot(`size-${w}x${hh}-shelf`)
    await checkLayout('书架', w)
    if (!epub) return
    await evaluate(`location.hash = '#/read/${epub.id}'`)
    await waitFor("document.querySelector('.reader') && !document.querySelector('.reader .r-loading')", 40000, '阅读器打开')
    await sleep(2500)
    await shot(`size-${w}x${hh}-epub`)
    const r = await checkReader(w, hh)
    if (!r.cols) throw new Error('EPUB 正文没有排出来')
    await evaluate("location.hash = '#/'")
    await waitFor("!document.querySelector('.reader')", 10000, '退出阅读器')
  })
}

ws.close()
proc.kill('SIGKILL')
fs.rmSync(ud, { recursive: true, force: true })
console.log(failures ? `\n${failures} 项有问题，截图在 ${shots}` : `\n全部通过，截图在 ${shots}`)
process.exit(failures ? 1 : 0)

// ---------- 测试书（--samples）：不靠别的工具，现做 ----------

async function addSamples() {
  const files = {
    '旧浏览器测试-红楼梦.epub': makeEpub('旧浏览器测试红楼梦', '曹雪芹', [['第一回', '甄士隐梦幻识通灵，贾雨村风尘怀闺秀。'.repeat(40)], ['第二回', '贾夫人仙逝扬州城，冷子兴演说荣国府。'.repeat(40)]]),
    '旧浏览器测试-活着.txt': Buffer.from(['第一章', '我比现在年轻十岁的时候，获得了一个游手好闲的职业。'.repeat(30), '第二章', '那天傍晚，我走到了一棵大树下。'.repeat(30)].join('\n'), 'utf8'),
    '旧浏览器测试-深度学习.pdf': makePdf(['Shuku legacy test page one', 'Neural networks learn representations', 'from data, layer by layer.'], 3),
    '旧浏览器测试-漫画 第1卷.cbz': makeZip([1, 2, 3].map(n => [`${String(n).padStart(3, '0')}.png`, makePng(300, 450, n)])),
    // 整本写在一个文件里（100 多 KB 的一节）：墨水屏上由书库拆开
    '旧浏览器测试-整本一个文件.epub': makeEpub('旧浏览器测试整本一个文件', '佚名', [['全书', Array.from({ length: 360 }, (_, i) => `第${i + 1}段：` + '天下大势，分久必合，合久必分。'.repeat(8))]]),
  }
  for (const [name, data] of Object.entries(files)) {
    const r = await fetch(base + '/api/upload?path=' + encodeURIComponent(name), { method: 'POST', headers: { Authorization: 'Bearer ' + token }, body: data })
    if (!r.ok) throw new Error('上传测试书失败：' + name + ' ' + r.status)
  }
  // 等入库
  let items = []
  for (let i = 0; i < 120; i++) {
    items = (await api('/api/books?limit=200')).items || []
    const have = f => items.some(b => [].concat(b.formats || []).join(',').includes(f))
    if (['epub', 'txt', 'pdf', 'cbz'].every(have) && items.some(b => /整本/.test(b.title))) break
    if (i === 119) console.log('  （测试书没有全部入库，后面缺的跳过）')
    await sleep(1000)
  }
  // 记上进度：书架上有“继续阅读”那一排（只给百分比，打开时按百分比跳）
  for (const b of items.filter(b => b.created_at >= t0)) {
    await fetch(base + `/api/books/${b.id}/progress`, {
      method: 'PUT', headers: { Authorization: 'Bearer ' + token, 'Content-Type': 'application/json' },
      body: JSON.stringify({ location: '', percent: 0.1, device: '旧浏览器测试' }),
    })
  }
}

// （函数声明会提升：上面先调用 addSamples 时这些已经能用）
var crcTable = null // var：上面先调用时也能用（let 会报未初始化）
function CRC(buf) {
  if (!crcTable) {
    crcTable = new Uint32Array(256)
    for (let n = 0; n < 256; n++) { let c = n; for (let k = 0; k < 8; k++) c = c & 1 ? 0xEDB88320 ^ (c >>> 1) : c >>> 1; crcTable[n] = c >>> 0 }
  }
  let c = 0xFFFFFFFF
  for (const b of buf) c = crcTable[(c ^ b) & 0xFF] ^ (c >>> 8)
  return (c ^ 0xFFFFFFFF) >>> 0
}

// 不压缩的 zip（EPUB 的 mimetype 必须在第一个、不压缩）
function makeZip(entries) {
  const locals = [], centrals = []
  let off = 0
  for (const [name, content] of entries) {
    const data = Buffer.isBuffer(content) ? content : Buffer.from(content, 'utf8')
    const fn = Buffer.from(name, 'utf8')
    const crc = CRC(data)
    const lh = Buffer.alloc(30)
    lh.writeUInt32LE(0x04034b50, 0); lh.writeUInt16LE(20, 4); lh.writeUInt16LE(0x0800, 6); lh.writeUInt16LE(0, 8)
    lh.writeUInt32LE(crc, 14); lh.writeUInt32LE(data.length, 18); lh.writeUInt32LE(data.length, 22); lh.writeUInt16LE(fn.length, 26)
    const ch = Buffer.alloc(46)
    ch.writeUInt32LE(0x02014b50, 0); ch.writeUInt16LE(20, 4); ch.writeUInt16LE(20, 6); ch.writeUInt16LE(0x0800, 8)
    ch.writeUInt32LE(crc, 16); ch.writeUInt32LE(data.length, 20); ch.writeUInt32LE(data.length, 24); ch.writeUInt16LE(fn.length, 28); ch.writeUInt32LE(off, 42)
    locals.push(lh, fn, data)
    centrals.push(ch, fn)
    off += 30 + fn.length + data.length
  }
  const cd = Buffer.concat(centrals)
  const end = Buffer.alloc(22)
  end.writeUInt32LE(0x06054b50, 0); end.writeUInt16LE(entries.length, 8); end.writeUInt16LE(entries.length, 10); end.writeUInt32LE(cd.length, 12); end.writeUInt32LE(off, 16)
  return Buffer.concat([...locals, cd, end])
}

function makeEpub(title, author, chapters) {
  const items = chapters.map((_, i) => `<item id="c${i}" href="c${i}.xhtml" media-type="application/xhtml+xml"/>`).join('')
  const spine = chapters.map((_, i) => `<itemref idref="c${i}"/>`).join('')
  return makeZip([
    ['mimetype', 'application/epub+zip'],
    ['META-INF/container.xml', '<?xml version="1.0"?><container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="content.opf" media-type="application/oebps-package+xml"/></rootfiles></container>'],
    ['content.opf', `<?xml version="1.0"?><package xmlns="http://www.idpf.org/2007/opf" version="2.0" unique-identifier="id"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:identifier id="id">shuku-legacy-test</dc:identifier><dc:title>${title}</dc:title><dc:creator>${author}</dc:creator><dc:language>zh</dc:language></metadata><manifest>${items}</manifest><spine>${spine}</spine></package>`],
    // 正文是字符串：两段一样的；是数组：每个一段
    ...chapters.map(([t, text], i) => [`c${i}.xhtml`, `<?xml version="1.0" encoding="utf-8"?><html xmlns="http://www.w3.org/1999/xhtml"><head><title>${t}</title></head><body><h2>${t}</h2>${(Array.isArray(text) ? text : [text, text]).map(x => `<p>${x}</p>`).join('\n')}</body></html>`]),
  ])
}

function makePdf(lines, pages) {
  const objs = []
  const kids = []
  objs[1] = '<< /Type /Catalog /Pages 2 0 R >>'
  objs[3] = '<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>'
  for (let p = 0; p < pages; p++) {
    const page = 4 + p * 2, content = page + 1
    kids.push(`${page} 0 R`)
    const text = lines.map((l, i) => `BT /F1 22 Tf 72 ${760 - i * 34} Td (${l}${i === 0 ? ' ' + (p + 1) : ''}) Tj ET`).join('\n')
    objs[page] = `<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Contents ${content} 0 R /Resources << /Font << /F1 3 0 R >> >> >>`
    objs[content] = `<< /Length ${Buffer.byteLength(text)} >>\nstream\n${text}\nendstream`
  }
  objs[2] = `<< /Type /Pages /Kids [${kids.join(' ')}] /Count ${pages} >>`
  let out = '%PDF-1.4\n'
  const offs = []
  for (let i = 1; i < objs.length; i++) { offs[i] = Buffer.byteLength(out); out += `${i} 0 obj\n${objs[i]}\nendobj\n` }
  const xref = Buffer.byteLength(out)
  out += `xref\n0 ${objs.length}\n0000000000 65535 f \n` + offs.slice(1).map(o => String(o).padStart(10, '0') + ' 00000 n \n').join('')
  out += `trailer\n<< /Size ${objs.length} /Root 1 0 R >>\nstartxref\n${xref}\n%%EOF\n`
  return Buffer.from(out, 'latin1')
}

// 灰度 PNG：边框、几条横线，深浅按页码变
function makePng(w, h, n) {
  const raw = Buffer.alloc((w + 1) * h)
  for (let y = 0; y < h; y++) {
    raw[y * (w + 1)] = 0
    for (let x = 0; x < w; x++) {
      const edge = x < 6 || y < 6 || x >= w - 6 || y >= h - 6 || (y % 60 < 4)
      raw[y * (w + 1) + 1 + x] = edge ? 20 : 245 - n * 30
    }
  }
  const chunk = (type, data) => {
    const len = Buffer.alloc(4); len.writeUInt32BE(data.length)
    const td = Buffer.concat([Buffer.from(type, 'latin1'), data])
    const crc = Buffer.alloc(4); crc.writeUInt32BE(CRC(td))
    return Buffer.concat([len, td, crc])
  }
  const ihdr = Buffer.alloc(13)
  ihdr.writeUInt32BE(w, 0); ihdr.writeUInt32BE(h, 4); ihdr[8] = 8; ihdr[9] = 0
  return Buffer.concat([Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]), chunk('IHDR', ihdr), chunk('IDAT', zlib.deflateSync(raw)), chunk('IEND', Buffer.alloc(0))])
}
