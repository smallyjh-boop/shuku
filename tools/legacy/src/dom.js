// 旧浏览器（墨水屏阅读器、旧安卓的网页组件，大约 Chrome 55～80）缺的网页功能
import { ResizeObserver as ResizeObserverPolyfill } from '@juggle/resize-observer'
import { ReadableStream as PolyReadableStream, WritableStream as PolyWritableStream, TransformStream as PolyTransformStream } from 'web-streams-polyfill'
import { Inflate, Unzlib, Gunzip } from 'fflate'

const G = window
const def = (obj, name, value) => {
  if (obj && !(name in obj)) Object.defineProperty(obj, name, { value, configurable: true, writable: true })
}

// ---------- 事件 ----------

// new EventTarget()、class X extends EventTarget（Chrome 64）：foliate 的翻页历史、注释、朗读同步都要
let constructibleET = true
try { new EventTarget() } catch (e) { constructibleET = false }
if (!constructibleET) {
  const NativeEventTarget = G.EventTarget
  const listeners = new WeakMap()
  const isCapture = o => typeof o === 'boolean' ? o : !!(o && o.capture)
  class EventTarget {
    addEventListener(type, fn, opts) {
      if (!fn) return
      let m = listeners.get(this)
      if (!m) listeners.set(this, m = new Map())
      if (!m.has(type)) m.set(type, [])
      const list = m.get(type), capture = isCapture(opts)
      if (list.some(l => l.fn === fn && l.capture === capture)) return
      list.push({ fn, capture, once: !!(opts && typeof opts === 'object' && opts.once) })
      if (opts && typeof opts === 'object' && opts.signal) opts.signal.addEventListener('abort', () => this.removeEventListener(type, fn, opts))
    }
    removeEventListener(type, fn, opts) {
      const list = listeners.get(this) && listeners.get(this).get(type)
      if (!list) return
      const capture = isCapture(opts)
      const i = list.findIndex(l => l.fn === fn && l.capture === capture)
      if (i >= 0) list.splice(i, 1)
    }
    dispatchEvent(ev) {
      try {
        Object.defineProperty(ev, 'target', { value: this, configurable: true })
        Object.defineProperty(ev, 'currentTarget', { value: this, configurable: true })
      } catch (e) {}
      const list = listeners.get(this) && listeners.get(this).get(ev.type)
      for (const l of list ? list.slice() : []) {
        if (l.once) this.removeEventListener(ev.type, l.fn, l.capture)
        try { typeof l.fn === 'function' ? l.fn.call(this, ev) : l.fn.handleEvent(ev) } catch (e) { setTimeout(() => { throw e }) }
      }
      return !ev.defaultPrevented
    }
    static [Symbol.hasInstance](x) {
      return (!!NativeEventTarget && x instanceof NativeEventTarget) || (x != null && EventTarget.prototype.isPrototypeOf(x))
    }
  }
  G.EventTarget = EventTarget
}

// ---------- 元素 ----------

// ResizeObserver（Chrome 64）：阅读器排版、漫画缩放
if (!G.ResizeObserver) G.ResizeObserver = ResizeObserverPolyfill

// IntersectionObserverEntry.isIntersecting（Chrome 58）：书架封面按需加载
if (G.IntersectionObserverEntry && !('isIntersecting' in IntersectionObserverEntry.prototype)) {
  Object.defineProperty(IntersectionObserverEntry.prototype, 'isIntersecting', { get() { return this.intersectionRatio > 0 }, configurable: true })
}

const EP = Element.prototype
function replaceChildren(...nodes) {
  while (this.lastChild) this.removeChild(this.lastChild)
  if (nodes.length) this.append(...nodes)
}
def(EP, 'replaceChildren', replaceChildren)
def(DocumentFragment.prototype, 'replaceChildren', replaceChildren)
def(Document.prototype, 'replaceChildren', replaceChildren)
def(EP, 'toggleAttribute', function (name, force) {
  const has = this.hasAttribute(name)
  const on = force === undefined ? !has : !!force
  if (on && !has) this.setAttribute(name, '')
  else if (!on && has) this.removeAttribute(name)
  return on
})
def(EP, 'getAttributeNames', function () { return Array.from(this.attributes, a => a.name) })

// 元素的 scrollTo / scrollBy（Chrome 61），以及 window.scrollTo({ top }) 这种写法
def(EP, 'scrollTo', function (a, b) {
  if (a && typeof a === 'object') {
    if (a.left != null) this.scrollLeft = a.left
    if (a.top != null) this.scrollTop = a.top
  } else { this.scrollLeft = a || 0; this.scrollTop = b || 0 }
})
def(EP, 'scrollBy', function (a, b) {
  if (a && typeof a === 'object') { this.scrollLeft += a.left || 0; this.scrollTop += a.top || 0 }
  else { this.scrollLeft += a || 0; this.scrollTop += b || 0 }
})
if (!('scrollBehavior' in document.documentElement.style)) {
  for (const name of ['scrollTo', 'scroll', 'scrollBy']) {
    const orig = G[name]
    if (typeof orig !== 'function') continue
    G[name] = function (a, b) {
      if (a && typeof a === 'object') {
        if (name === 'scrollBy') return orig.call(G, a.left || 0, a.top || 0)
        return orig.call(G, a.left != null ? a.left : G.pageXOffset, a.top != null ? a.top : G.pageYOffset)
      }
      return orig.call(G, a || 0, b || 0)
    }
  }
}

// ---------- Blob ----------

const readBlob = (blob, how) => new Promise((resolve, reject) => {
  const r = new FileReader()
  r.onload = () => resolve(r.result)
  r.onerror = () => reject(r.error)
  r[how](blob)
})
def(Blob.prototype, 'arrayBuffer', function () { return readBlob(this, 'readAsArrayBuffer') })
def(Blob.prototype, 'text', function () { return readBlob(this, 'readAsText') })

// ---------- 流（EPUB 解压） ----------

// TransformStream（Chrome 67）：zip.js 解压 EPUB 全靠流。没有的话三种流整套换成 polyfill（原生的和 polyfill 的不能互相 pipe）
if (typeof G.TransformStream === 'undefined') {
  G.ReadableStream = PolyReadableStream
  G.WritableStream = PolyWritableStream
  G.TransformStream = PolyTransformStream
  // zip.js 用 new Response(流).blob() 把解压出来的内容变成 Blob；原生的 Response 不认 polyfill 的流，先读完再交给它
  const NativeResponse = G.Response
  const collect = stream => {
    const reader = stream.getReader()
    const chunks = []
    const pump = () => reader.read().then(r => r.done ? chunks : (chunks.push(r.value), pump()))
    return pump()
  }
  const Response = function (body, init) {
    if (!(body instanceof PolyReadableStream)) return new NativeResponse(body, init)
    const done = collect(body).then(chunks => new NativeResponse(new Blob(chunks), init))
    return {
      ok: true, status: 200, statusText: '', headers: new Headers(init && init.headers), bodyUsed: false, body: null,
      blob: () => done.then(r => r.blob()),
      arrayBuffer: () => done.then(r => r.arrayBuffer()),
      text: () => done.then(r => r.text()),
      json: () => done.then(r => r.json()),
    }
  }
  Response.prototype = NativeResponse.prototype
  for (const k of ['error', 'redirect']) if (NativeResponse[k]) Response[k] = NativeResponse[k].bind(NativeResponse)
  G.Response = Response
}

// DecompressionStream（Chrome 80）：zip.js 解压 EPUB 用，旧浏览器用 fflate 解（同一套格式名）
if (typeof G.DecompressionStream === 'undefined') {
  G.DecompressionStream = class DecompressionStream {
    constructor(format) {
      const Codec = format === 'deflate-raw' ? Inflate : format === 'deflate' ? Unzlib : format === 'gzip' ? Gunzip : null
      if (!Codec) throw new TypeError('不支持的压缩格式：' + format)
      let out
      const codec = new Codec(chunk => out.enqueue(chunk))
      const ts = new G.TransformStream({
        start(c) { out = c },
        transform(chunk) {
          codec.push(chunk instanceof Uint8Array ? chunk : new Uint8Array(chunk.buffer || chunk, chunk.byteOffset || 0, chunk.byteLength), false)
        },
        flush() { codec.push(new Uint8Array(0), true) },
      })
      this.readable = ts.readable
      this.writable = ts.writable
    }
  }
}

// ---------- 样式表 ----------

// new CSSStyleSheet() + adoptedStyleSheets（Chrome 73）：foliate 的固定版式（漫画、绘本 EPUB）给 shadow root 加样式用；
// 旧浏览器换成往 shadow root 里放 <style>
let constructable = true
try { new CSSStyleSheet() } catch (e) { constructable = false }
if (!constructable && G.ShadowRoot) {
  class Sheet {
    constructor() { this._text = ''; this._nodes = [] }
    replaceSync(text) { this._text = String(text); for (const n of this._nodes) n.textContent = this._text }
    replace(text) { this.replaceSync(text); return Promise.resolve(this) }
  }
  G.CSSStyleSheet = Sheet
  Object.defineProperty(ShadowRoot.prototype, 'adoptedStyleSheets', {
    configurable: true,
    get() { return this._adopted || [] },
    set(list) {
      for (const n of this._adoptedNodes || []) n.remove()
      this._adopted = Array.from(list)
      this._adoptedNodes = this._adopted.map(sheet => {
        const st = document.createElement('style')
        st.textContent = sheet._text
        sheet._nodes.push(st)
        this.appendChild(st)
        return st
      })
    },
  })
}

// ---------- 画布（pdf.js） ----------

// DOMMatrix（Chrome 61）：只要二维的
if (typeof G.DOMMatrix === 'undefined') {
  class DOMMatrix {
    constructor(init) {
      const m = Array.isArray(init) ? init : [1, 0, 0, 1, 0, 0]
      ;[this.a, this.b, this.c, this.d, this.e, this.f] = m
      this.is2D = true
    }
    get isIdentity() { return this.a === 1 && this.b === 0 && this.c === 0 && this.d === 1 && this.e === 0 && this.f === 0 }
    get m11() { return this.a } get m12() { return this.b } get m21() { return this.c } get m22() { return this.d } get m41() { return this.e } get m42() { return this.f }
    multiply(o) { return new DOMMatrix(mul([this.a, this.b, this.c, this.d, this.e, this.f], [o.a, o.b, o.c, o.d, o.e, o.f])) }
    multiplySelf(o) { const r = this.multiply(o); Object.assign(this, { a: r.a, b: r.b, c: r.c, d: r.d, e: r.e, f: r.f }); return this }
    preMultiplySelf(o) { const r = new DOMMatrix([o.a, o.b, o.c, o.d, o.e, o.f]).multiply(this); Object.assign(this, { a: r.a, b: r.b, c: r.c, d: r.d, e: r.e, f: r.f }); return this }
    translate(x = 0, y = 0) { return this.multiply({ a: 1, b: 0, c: 0, d: 1, e: x, f: y }) }
    scale(x = 1, y = x) { return this.multiply({ a: x, b: 0, c: 0, d: y, e: 0, f: 0 }) }
    inverse() {
      const { a, b, c, d, e, f } = this
      const det = a * d - b * c
      if (!det) return new DOMMatrix([NaN, NaN, NaN, NaN, NaN, NaN])
      return new DOMMatrix([d / det, -b / det, -c / det, a / det, (c * f - d * e) / det, (b * e - a * f) / det])
    }
    invertSelf() { const r = this.inverse(); Object.assign(this, { a: r.a, b: r.b, c: r.c, d: r.d, e: r.e, f: r.f }); return this }
    transformPoint(p = {}) { const x = p.x || 0, y = p.y || 0; return { x: this.a * x + this.c * y + this.e, y: this.b * x + this.d * y + this.f, z: 0, w: 1 } }
  }
  G.DOMMatrix = DOMMatrix
}

function mul(m, n) {
  return [m[0] * n[0] + m[2] * n[1], m[1] * n[0] + m[3] * n[1], m[0] * n[2] + m[2] * n[3], m[1] * n[2] + m[3] * n[3],
    m[0] * n[4] + m[2] * n[5] + m[4], m[1] * n[4] + m[3] * n[5] + m[5]]
}

// ctx.getTransform()、ctx.setTransform(矩阵)、ctx.resetTransform()（Chrome 68）：pdf.js 到处在用。自己记下当前的变换
const C2D = G.CanvasRenderingContext2D && CanvasRenderingContext2D.prototype
if (C2D && !C2D.getTransform) {
  const state = new WeakMap()
  const cur = ctx => {
    let s = state.get(ctx)
    if (!s) { s = { m: [1, 0, 0, 1, 0, 0], stack: [] }; state.set(ctx, s) }
    return s
  }
  const wrap = (name, fn) => {
    const orig = C2D[name]
    C2D[name] = function (...args) { fn(cur(this), args); return orig.apply(this, args) }
  }
  wrap('save', s => s.stack.push(s.m.slice()))
  wrap('restore', s => { if (s.stack.length) s.m = s.stack.pop() })
  wrap('translate', (s, [x, y]) => { s.m = mul(s.m, [1, 0, 0, 1, +x || 0, +y || 0]) })
  wrap('scale', (s, [x, y]) => { s.m = mul(s.m, [+x, 0, 0, +y, 0, 0]) })
  wrap('rotate', (s, [r]) => { const c = Math.cos(r), n = Math.sin(r); s.m = mul(s.m, [c, n, -n, c, 0, 0]) })
  wrap('transform', (s, a) => { s.m = mul(s.m, a.map(Number)) })
  const setT = C2D.setTransform
  C2D.setTransform = function (a, b, c, d, e, f) {
    if (a && typeof a === 'object') {
      const o = a
      a = o.a != null ? o.a : 1; b = o.b || 0; c = o.c || 0; d = o.d != null ? o.d : 1; e = o.e || 0; f = o.f || 0
    } else if (a === undefined) { a = 1; b = 0; c = 0; d = 1; e = 0; f = 0 }
    cur(this).m = [+a, +b, +c, +d, +e, +f]
    return setT.call(this, a, b, c, d, e, f)
  }
  C2D.resetTransform = function () { this.setTransform(1, 0, 0, 1, 0, 0) }
  C2D.getTransform = function () { return new DOMMatrix(cur(this).m.slice()) }
  // 画布改了宽高，状态（包括变换）会被浏览器清空：跟着清
  const ctxOf = new WeakMap()
  const getCtx = HTMLCanvasElement.prototype.getContext
  HTMLCanvasElement.prototype.getContext = function (type, ...rest) {
    const ctx = getCtx.call(this, type, ...rest)
    if (ctx && type === '2d') ctxOf.set(this, ctx)
    return ctx
  }
  for (const prop of ['width', 'height']) {
    const d = Object.getOwnPropertyDescriptor(HTMLCanvasElement.prototype, prop)
    if (!d || !d.set) continue
    Object.defineProperty(HTMLCanvasElement.prototype, prop, {
      configurable: true, enumerable: d.enumerable, get: d.get,
      set(v) { d.set.call(this, v); const ctx = ctxOf.get(this); if (ctx) state.delete(ctx) },
    })
  }
}

// AbortSignal.timeout（Chrome 103）
if (G.AbortSignal && !AbortSignal.timeout) {
  AbortSignal.timeout = ms => { const c = new AbortController(); setTimeout(() => c.abort(), ms); return c.signal }
}
