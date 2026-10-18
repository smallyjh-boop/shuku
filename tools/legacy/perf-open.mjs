// 量打开书有多快：旧版 Chromium（Chrome 64）模拟墨案（App 原生桥、墨水屏、936x1248），CPU 降速模拟墨水屏的慢处理器。
//
//   node tools/legacy/perf-open.mjs <旧版 chromium 路径> <书库地址> <登录令牌> <书的编号,…> [--rate=20] [--turn]
//
// 每本书打印：打开用了几秒、JS 内存、请求数和流量、脚本和排版各用了多少、最长卡住多久；--turn 再往后翻六页，看每页多久。
// --rate：CPU 慢几倍（20 倍大约是墨水屏阅读器）。同一个浏览器里第一本书多算了一次加载中文字体的时间，要比较时把同一本书放两遍看第二遍。
// 改了阅读器（flow.js、split.js、foliate-js）或者按需取书（server/zip*.go）以后，拿大书（几千章的 TXT、整本写在一个文件里的 EPUB、
// 每章几百 KB 的 EPUB、带大图的）量一下，和改之前比。

import { spawn } from 'node:child_process'
import fs from 'node:fs'

const [bin, base, token, idsArg] = process.argv.slice(2).filter(a => !a.startsWith('--'))
if (!bin || !base || !token || !idsArg) {
  console.log('用法：node perf-open.mjs <chromium> <书库地址> <令牌> <书的编号,…> [--rate=20] [--turn]')
  process.exit(2)
}
const rate = +((process.argv.find(a => a.startsWith('--rate=')) || '--rate=20').slice(7))
const turn = process.argv.includes('--turn')
const ids = idsArg.split(',').map(Number)
const port = 9600 + Math.floor(Math.random() * 300)
const ud = fs.mkdtempSync('/tmp/shuku-perf-')
const proc = spawn(bin, ['--headless', '--no-sandbox', '--disable-gpu', '--no-zygote', '--single-process', '--disable-dev-shm-usage',
  '--hide-scrollbars', `--remote-debugging-port=${port}`, `--user-data-dir=${ud}`, 'about:blank'], { stdio: 'ignore' })
const sleep = ms => new Promise(r => setTimeout(r, ms))
let ver
for (let i = 0; i < 60 && !ver; i++) { await sleep(250); try { ver = await (await fetch(`http://127.0.0.1:${port}/json/version`)).json() } catch {} }
if (!ver) { console.log('旧版 Chromium 没有启动'); process.exit(1) }
const target = (await (await fetch(`http://127.0.0.1:${port}/json/list`)).json()).find(t => t.type === 'page')
const ws = new WebSocket(target.webSocketDebuggerUrl)
await new Promise((r, j) => { ws.onopen = r; ws.onerror = j })
let seq = 0
const pending = new Map()
let net = { n: 0, bytes: 0 }
const errors = []
ws.onmessage = e => {
  const m = JSON.parse(e.data)
  if (m.id && pending.has(m.id)) { pending.get(m.id)(m); pending.delete(m.id); return }
  if (m.method === 'Network.loadingFinished') { net.n++; net.bytes += m.params.encodedDataLength || 0 }
  else if (m.method === 'Runtime.exceptionThrown') errors.push((m.params.exceptionDetails.exception || {}).description || m.params.exceptionDetails.text)
}
const send = (method, params = {}) => new Promise((resolve, reject) => {
  const id = ++seq
  pending.set(id, m => m.error ? reject(new Error(method + '：' + m.error.message)) : resolve(m.result))
  ws.send(JSON.stringify({ id, method, params }))
})
const evaluate = async expr => (await send('Runtime.evaluate', { expression: expr, returnByValue: true, awaitPromise: true })).result.value
const metrics = async () => Object.fromEntries((await send('Performance.getMetrics')).metrics.map(m => [m.name, m.value]))

await send('Runtime.enable'); await send('Page.enable'); await send('Network.enable'); await send('Performance.enable')
await send('Network.setUserAgentOverride', { userAgent: ver['User-Agent'].replace('HeadlessChrome', 'Chrome') + ' ShukuApp/9.9 ShukuEInk' })
await send('Emulation.setDeviceMetricsOverride', { width: 936, height: 1248, deviceScaleFactor: 1, mobile: true })
await send('Page.addScriptToEvaluateOnNewDocument', {
  source: `localStorage.getItem('shuku.token') || localStorage.setItem('shuku.token', ${JSON.stringify(token)});
  (function () {
    var ls = window.localStorage
    window.ShukuNative = {
      storageGet: function (k) { return ls.getItem(k) }, storageSet: function (k, v) { ls.setItem(k, v) }, storageRemove: function (k) { ls.removeItem(k) },
      storageKeys: function () { var a = []; for (var i = 0; i < ls.length; i++) a.push(ls.key(i)); return JSON.stringify(a) },
      version: function () { return '9.9' }, deviceName: function () { return '性能测试' }, isEInk: function () { return true },
      deviceInfo: function () { return JSON.stringify({ brand: 'Moaan', model: 'inkPad X', android: '8.1.0', webview: '64', eink: true, screen: { w: 1404, h: 1872, density: 1.5, dpi: 240 } }) },
      downloads: function () { return '[]' }, prefs: function () { return '{}' }
    }
    window.__longTasks = []
    try { new PerformanceObserver(function (l) { l.getEntries().forEach(function (e) { window.__longTasks.push(e.duration) }) }).observe({ entryTypes: ['longtask'] }) } catch (e) {}
  })();`,
})
await send('Page.navigate', { url: base + '/#/' })
for (let i = 0; i < 100; i++) { await sleep(300); if (await evaluate("!!document.querySelector('.book')").catch(() => false)) break }
await sleep(1000)
await send('Emulation.setCPUThrottlingRate', { rate })
console.log(`${ver.Browser}，CPU 慢 ${rate} 倍，936x1248 墨水屏`)

const READY = `(() => { const r = document.querySelector('.reader'); if (!r || r.querySelector('.r-loading')) return false
  const v = r.querySelector('foliate-view')
  if (v) { const d = v.renderer && v.renderer.getContents ? v.renderer.getContents() : []; return !!(d.length && d[0].doc.body && d[0].doc.body.textContent.trim().length > 10) }
  return !!r.querySelector('canvas, img') })()`
for (const id of ids) {
  await evaluate("location.hash = '#/'")
  await sleep(1500)
  await send('HeapProfiler.collectGarbage').catch(() => {})
  const m0 = await metrics()
  net = { n: 0, bytes: 0 }; errors.length = 0
  await evaluate('window.__longTasks = []')
  const t0 = Date.now()
  await evaluate(`location.hash = '#/read/${id}'`)
  let peak = 0, ok = false
  while (Date.now() - t0 < 240000) {
    await sleep(400)
    try { peak = Math.max(peak, (await metrics()).JSHeapUsedSize || 0) } catch {}
    if (await evaluate(READY).catch(() => false)) { ok = true; break }
  }
  const dt = (Date.now() - t0) / 1000
  const m1 = await metrics()
  const lt = await evaluate('window.__longTasks.slice()').catch(() => [])
  const title = await evaluate('document.title').catch(() => '')
  const mb = x => (x / 1048576).toFixed(1)
  console.log(`书 ${id}《${title}》：${ok ? '打开' : '没打开，等了'} ${dt.toFixed(1)} 秒；JS 内存最多 ${mb(peak)} MB，DOM 节点 ${m1.Nodes}，` +
    `请求 ${net.n} 个 ${mb(net.bytes)} MB；脚本 ${(m1.ScriptDuration - m0.ScriptDuration).toFixed(1)} 秒、排版 ${(m1.LayoutDuration - m0.LayoutDuration).toFixed(1)} 秒；` +
    `最长卡住 ${((lt.length ? Math.max(...lt) : 0) / 1000).toFixed(1)} 秒`)
  if (errors.length) console.log('  页面报错：' + errors.slice(0, 3).join('；'))
  if (!ok || !turn) continue
  await evaluate("window.__rel = 0; (document.querySelector('foliate-view') || document.body).addEventListener('relocate', function () { window.__rel++ })")
  const turns = []
  for (let k = 0; k < 6; k++) {
    const r0 = await evaluate('window.__rel')
    const t1 = Date.now()
    for (const type of ['rawKeyDown', 'keyUp']) await send('Input.dispatchKeyEvent', { type, key: 'ArrowRight', code: 'ArrowRight', windowsVirtualKeyCode: 39 })
    let moved = false
    while (Date.now() - t1 < 30000) { await sleep(100); if ((await evaluate('window.__rel').catch(() => r0)) > r0) { moved = true; break } }
    turns.push(moved ? ((Date.now() - t1) / 1000).toFixed(1) : '没翻')
    await sleep(500)
  }
  console.log('  往后翻六页：' + turns.join('、') + ' 秒')
}
ws.close()
proc.kill('SIGKILL')
fs.rmSync(ud, { recursive: true, force: true })
