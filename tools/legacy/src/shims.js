// 页面和 worker 里都要的：没法真正实现、只要不报错就行的几个
const G = typeof self !== 'undefined' ? self : window

// WeakRef（Chrome 84）：一直留着引用（多占一点内存，不影响功能）
if (typeof G.WeakRef === 'undefined') {
  G.WeakRef = class WeakRef {
    constructor(t) { this._t = t }
    deref() { return this._t }
  }
}
// FinalizationRegistry（Chrome 84）：不回调（只是少了一点清理）
if (typeof G.FinalizationRegistry === 'undefined') {
  G.FinalizationRegistry = class FinalizationRegistry {
    register() {}
    unregister() { return false }
  }
}
// postMessage(消息, { transfer })（Chrome 70）：旧浏览器第二个参数只认数组。pdf.js 的页面和 worker 两边都这么发
try { new MessageChannel().port1.postMessage(0, { transfer: [] }) } catch (e) {
  const fix = proto => {
    if (!proto || typeof proto.postMessage !== 'function') return
    const orig = proto.postMessage
    proto.postMessage = function (msg, opt, ...rest) {
      if (opt && typeof opt === 'object' && !Array.isArray(opt) && !(Symbol.iterator in opt)) return orig.call(this, msg, opt.transfer || [])
      return opt == null ? orig.call(this, msg) : orig.call(this, msg, opt, ...rest) // null 也当没有（新浏览器把它当空的选项）
    }
  }
  if (typeof Worker !== 'undefined') fix(Worker.prototype)
  if (typeof MessagePort !== 'undefined') fix(MessagePort.prototype)
  if (typeof DedicatedWorkerGlobalScope !== 'undefined') fix(DedicatedWorkerGlobalScope.prototype)
}

// Intl.Locale（Chrome 74）：foliate 用它看书的语言是不是中日韩、从右往左排
if (typeof Intl !== 'undefined' && typeof Intl.Locale !== 'function') {
  const RTL = ['ar', 'he', 'fa', 'ur', 'yi', 'ps', 'sd', 'ug', 'dv', 'ku']
  Intl.Locale = class Locale {
    constructor(tag) {
      const parts = String(tag).replace(/_/g, '-').split('-')
      this.language = (parts[0] || 'und').toLowerCase()
      this.script = parts.find((p, i) => i > 0 && /^[a-z]{4}$/i.test(p))
      this.region = parts.find((p, i) => i > 0 && /^([a-z]{2}|\d{3})$/i.test(p))
      this.baseName = [this.language, this.script, this.region].filter(Boolean).join('-')
      this.textInfo = { direction: RTL.includes(this.language) ? 'rtl' : 'ltr' }
    }
    getTextInfo() { return this.textInfo }
    maximize() { return this }
    minimize() { return this }
    toString() { return this.baseName }
  }
}

// DataView 读 64 位整数（Chrome 67）：没有 BigInt 时返回普通数字（zip.js 读 ZIP64 大小、NTFS 时间用，2^53 以内准确）
if (typeof DataView !== 'undefined' && !DataView.prototype.getBigUint64) {
  DataView.prototype.getBigUint64 = function (o, le) {
    const a = this.getUint32(o, le), b = this.getUint32(o + 4, le)
    return le ? b * 4294967296 + a : a * 4294967296 + b
  }
  DataView.prototype.getBigInt64 = function (o, le) {
    const hi = this.getInt32(le ? o + 4 : o, le), lo = this.getUint32(le ? o : o + 4, le)
    return hi * 4294967296 + lo
  }
}
