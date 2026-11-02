// 页面绘制前要做的事（同步加载，放在 <head> 里），然后加载网页本身。
// 不写成内联脚本：页面有内容安全策略（CSP），只允许本站的脚本文件。
// 这个文件要能在很旧的浏览器里运行（墨水屏阅读器、旧安卓的网页组件），只用 ES5 写法。

// 安卓 App 里：localStorage 交给原生保存。内网和 Tailscale 是两个地址，浏览器会分开存，
// 交给 App 统一保存后，阅读设置、进度、登录状态在两个地址之间通用
(function () {
  var N = window.ShukuNative
  if (!N || !N.storageGet) return
  var keys = function () { try { return JSON.parse(N.storageKeys() || '[]') } catch (e) { return [] } }
  var s = {
    getItem: function (k) { var v = N.storageGet(String(k)); return v == null ? null : v },
    setItem: function (k, v) { N.storageSet(String(k), String(v)) },
    removeItem: function (k) { N.storageRemove(String(k)) },
    clear: function () { keys().forEach(function (k) { N.storageRemove(k) }) },
    key: function (i) { var a = keys(); return i < a.length ? a[i] : null },
    get length() { return keys().length }
  }
  try { Object.defineProperty(window, 'localStorage', { value: s, configurable: true, enumerable: true }) } catch (e) {}
  document.documentElement.classList.add('in-app')
})();

// 墨水屏模式（设置 → 外观：自动 / 开 / 关）。自动：App 认出是墨水屏阅读器（墨案、文石等）时打开。
// 打开后：纯黑白、没有动画和阴影，阅读时按页翻。界面字号（设置里能调）：自动时墨水屏按屏幕的实际尺寸放大。
// web/js/ui.js 的 applyEInk 是同一套逻辑（设置改了马上生效用）
(function () {
  var root = document.documentElement
  try {
    var p = JSON.parse(localStorage.getItem('shuku.app') || '{}')
    var N = window.ShukuNative
    var detected = /ShukuEInk/.test(navigator.userAgent)
    if (!detected && N && N.isEInk) { try { detected = !!N.isEInk() } catch (e) {} }
    // 屏幕每英寸多少个网页像素（安卓手机大约 160）
    var ppi = 0
    if (N && N.deviceInfo) {
      try {
        var d = JSON.parse(N.deviceInfo() || '{}').screen || {}
        var x = d.xdpi > 100 && d.xdpi < 700 ? d.xdpi : d.dpi
        if (x && d.density) ppi = x / d.density
      } catch (e) {}
    }
    window.SHUKU_EINK_DETECTED = detected
    window.SHUKU_SCREEN_PPI = ppi || 160
    var mode = p.eink || 'auto'
    var on = mode === 'on' || (mode === 'auto' && detected)
    if (on) root.classList.add('eink')
    // 墨水屏拿得远一些、对比度低，字要大一点：每英寸的网页像素越多（字看起来越小），放得越大
    var auto = on ? Math.max(1, Math.min(1.4, Math.round((ppi || 160) / 160 * 115) / 100)) : 1
    var scale = +p.uiScale > 0 ? +p.uiScale : auto
    if (scale !== 1) root.style.setProperty('--ui-scale', String(scale))
  } catch (e) {}
})();

// 在页面绘制前确定界面颜色（浅色 / 护眼 / 深色 / 夜读纯黑），避免夜里打开时闪一下白屏。
// 规则和 web/js/ui.js 的 uiTone 一样：手动切换的夜间（10 小时内）> 自动夜读（跟随系统 / 定时，和阅读器共用）> 选的颜色；
// 墨水屏上只有选了深色、夜读才用深色
(function () {
  try {
    var root = document.documentElement
    var p = JSON.parse(localStorage.getItem('shuku.app') || '{}')
    var r = JSON.parse(localStorage.getItem('shuku.reader') || '{}')
    var a = p.appearance || 'system'
    var eink = root.classList.contains('eink')
    var osDark = !!(window.matchMedia && matchMedia('(prefers-color-scheme: dark)').matches)
    var night = null
    var o = JSON.parse(localStorage.getItem('shuku.nightOverride') || 'null')
    if (o && Date.now() - o.at < 36e6) night = !!o.on
    if (night === null) {
      var mode = r.nightMode || 'system'
      if (mode === 'system') night = osDark
      else if (mode === 'time') {
        var now = new Date(), m = now.getHours() * 60 + now.getMinutes()
        var s1 = String(r.nightStart || '21:30').split(':'), s2 = String(r.nightEnd || '07:00').split(':')
        var x = +s1[0] * 60 + +s1[1], y = +s2[0] * 60 + +s2[1]
        night = x <= y ? (m >= x && m < y) : (m >= x || m < y)
      } else night = false
    }
    var tone
    if (eink) tone = a === 'dark' || a === 'night' ? 'dark' : 'light'
    else if (a === 'night' || night) tone = 'night'
    else if (a === 'system') tone = osDark ? 'dark' : 'light'
    else tone = a === 'eyecare' || a === 'dark' ? a : 'light'
    root.setAttribute('data-theme', tone === 'night' || tone === 'dark' ? 'dark' : 'light')
    root.setAttribute('data-tone', tone)
  } catch (e) {}
})();

// 加载网页。新浏览器：/js/main.js（ES 模块）。旧浏览器（没有 ES 模块，或者不认识新的写法、新的样式）：
// /legacy/ 里降级过的一套（tools/legacy 构建），功能一样。
// 二十秒还没打开（或者一开始就出错），显示原因和浏览器版本，方便排查；新的那套出错时自动换成兼容的再试一次
(function () {
  var root = document.documentElement
  var q = location.search
  var forced = null
  try {
    forced = /[?&]legacy=1/.test(q) ? '1' : /[?&]legacy=0/.test(q) ? '0' : localStorage.getItem('shuku.legacy')
    // 新的那套出错后自动换过来的：记一个星期（期间书库更新了，过后再试新的那套）
    if (forced && forced.indexOf('auto:') === 0) forced = Date.now() - +forced.slice(5) < 7 * 864e5 ? '1' : null
  } catch (e) {}
  var legacy = forced === '1'
  if (forced !== '1' && forced !== '0') {
    try {
      legacy = !('noModule' in document.createElement('script')) || typeof Object.groupBy !== 'function' ||
        typeof Array.prototype.findLast !== 'function' ||
        !(window.CSS && CSS.supports && CSS.supports('selector(:has(a))') && CSS.supports('color', 'color-mix(in srgb, red, blue)'))
    } catch (e) { legacy = true }
  }
  window.SHUKU_LEGACY = legacy
  if (legacy) root.classList.add('legacy')

  var errors = []
  var shown = false
  function started() { var a = document.getElementById('app'); return !!(a && a.firstChild) }
  window.addEventListener('error', function (e) {
    if (started()) return
    var t = e.target
    if (t && t !== window && (t.src || t.href)) {
      errors.push('没有加载上 ' + (t.src || t.href))
      return
    }
    var msg = e.message || '出错'
    errors.push(msg + (e.filename ? ' @ ' + e.filename.replace(location.origin, '') + ':' + e.lineno + ':' + e.colno : ''))
    // 不认识的写法、没有的功能：多半是浏览器太旧
    if (/SyntaxError|ReferenceError|TypeError|Unexpected|not defined|not a function|not a constructor|Invalid regular/.test(msg)) fallback()
  }, true)
  window.addEventListener('unhandledrejection', function (e) {
    if (started()) return
    var r = e.reason
    errors.push('Promise：' + (r && (r.message || String(r))))
  })

  // 新的那套刚开始就出错：自动换成兼容的那套重新打开（一次）
  function fallback() {
    if (legacy || forced === '0') return
    var key = 'shuku.legacyRetry'
    try {
      if (sessionStorage.getItem(key)) return
      sessionStorage.setItem(key, '1')
    } catch (e) { return }
    setTimeout(function () {
      if (started()) return
      try { localStorage.setItem('shuku.legacy', 'auto:' + Date.now()) } catch (e) {}
      location.reload()
    }, 300)
  }

  function load(src, module) {
    var s = document.createElement('script')
    if (module) s.type = 'module'
    else s.async = false // 按顺序执行
    s.src = src
    document.body.appendChild(s)
  }

  function start() {
    if (legacy) {
      var css = document.getElementById('app-css')
      if (css) css.href = '/legacy/app.css'
      load('/legacy/polyfills.js')
      load('/legacy/app.js')
    } else {
      load('/js/main.js', true)
    }
    setTimeout(function () { if (!started()) diagnose() }, 20000)
    setTimeout(function () { if (!started() && errors.length) diagnose() }, 4000)
  }

  function diagnose() {
    if (shown || started()) return
    shown = true
    var info = ''
    try {
      var N = window.ShukuNative
      if (N && N.deviceInfo) {
        var d = JSON.parse(N.deviceInfo() || '{}')
        info = (d.brand || '') + ' ' + (d.model || '') + '，安卓 ' + (d.android || '?') + '，网页组件 ' + (d.webview || '?')
      }
    } catch (e) {}
    var box = document.createElement('div')
    box.setAttribute('style', 'max-width:640px;margin:40px auto;padding:20px;font:16px/1.7 sans-serif;color:#000;background:#fff;border:2px solid #000;word-break:break-all')
    var lines = [
      ['b', '书库没有打开'],
      ['p', '网页：' + (legacy ? '兼容旧系统的那一套' : '新的那一套')],
      ['p', '浏览器：' + navigator.userAgent],
      info ? ['p', '设备：' + info] : null,
      ['p', errors.length ? '出错：' + errors.slice(0, 5).join('；') : '没有报错，可能是网络太慢或者书库没有响应'],
      ['p', '把这个页面拍照发给书库的作者，就能知道是哪里不兼容。']
    ]
    for (var i = 0; i < lines.length; i++) {
      if (!lines[i]) continue
      var el = document.createElement(lines[i][0] === 'b' ? 'h2' : 'p')
      el.textContent = lines[i][1]
      el.setAttribute('style', 'margin:0 0 8px')
      box.appendChild(el)
    }
    var retry = document.createElement('button')
    retry.textContent = '重试'
    retry.onclick = function () { location.reload() }
    var swap = document.createElement('button')
    swap.textContent = legacy ? '换成新的那一套再试' : '换成兼容旧系统的那一套再试'
    swap.onclick = function () {
      try { localStorage.setItem('shuku.legacy', legacy ? '0' : '1'); sessionStorage.removeItem('shuku.legacyRetry') } catch (e) {}
      location.reload()
    }
    var btnStyle = 'margin:8px 8px 0 0;padding:10px 16px;font:16px sans-serif;background:#fff;color:#000;border:2px solid #000'
    retry.setAttribute('style', btnStyle)
    swap.setAttribute('style', btnStyle)
    box.appendChild(retry)
    box.appendChild(swap)
    document.body.appendChild(box)
    // 后来又打开了（只是慢）：收起来
    var t = setInterval(function () { if (started()) { clearInterval(t); box.parentNode && box.parentNode.removeChild(box) } }, 2000)
  }

  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', start)
  else start()
})()
