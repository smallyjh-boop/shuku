// 页面绘制前要做的事（同步加载，放在 <head> 里）。
// 不写成内联脚本：页面有内容安全策略（CSP），只允许本站的脚本文件
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
// 在页面绘制前确定深浅色，避免夜里打开时闪一下白屏
(function () {
  try {
    var p = JSON.parse(localStorage.getItem('shuku.app') || '{}')
    var mode = p.appearance || 'system'
    var dark = mode === 'dark' || (mode === 'system' && matchMedia('(prefers-color-scheme: dark)').matches)
    var o = JSON.parse(localStorage.getItem('shuku.nightOverride') || 'null')
    if (o && o.on && Date.now() - o.at < 36e6) dark = true
    document.documentElement.dataset.theme = dark ? 'dark' : 'light'
  } catch (e) {}
})()
