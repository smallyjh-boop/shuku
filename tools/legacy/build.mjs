// 书库：给旧浏览器构建一套降级过的网页，输出到 web/legacy/（不进 git，打包时在 Actions 里构建）。
//
//   cd tools/legacy && npm ci && node build.mjs
//
// 新浏览器直接用 web/js 里的 ES 模块。旧浏览器（墨水屏阅读器、旧安卓自带的网页组件，大约 Chrome 55～116）
// 由 web/js/boot.js 判断后换成这里的：
//   polyfills.js   补上缺的功能（core-js、流、解压、ResizeObserver……），先加载
//   app.js         整个网页打成一个普通脚本，新语法降级到 Chrome 55 认识的写法
//   app.css        降级过的样式（inset、:is() 等展开）
//   pdf.js         pdf.js 降级后的普通脚本（第一次打开 PDF 时才加载）
//   pdf.worker.js  pdf.js 的 worker，普通脚本（旧浏览器的 worker 不支持 ES 模块）
// 构建完检查一遍：输出里不能有 ES2018 以后的语法、旧浏览器不认识的正则写法。

import * as esbuild from 'esbuild'
import { transform as transformCSS } from 'lightningcss'
import * as acorn from 'acorn'
import compat from 'core-js-compat'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const HERE = path.dirname(fileURLToPath(import.meta.url))
const ROOT = path.resolve(HERE, '../..')
const WEB = path.join(ROOT, 'web')
const OUT = path.join(WEB, 'legacy')
const TARGET = ['chrome55'] // 安卓 8 自带的网页组件大约是 Chrome 58～62，再留一点余地
const CSS_TARGETS = { chrome: 55 << 16 }
// core-js 只打进 Chrome 55 缺的那些（全部打进来 600 多 KB，墨水屏阅读器每次打开都要解析）。
// 不要的：这几个是把浏览器原生的（快）整个换成 JS 写的（慢很多），只为了规范里的边角情况——
// 数组长度超过 2³²、稳定排序、JSON 里落单的代理字符、正则的命名分组和 dotAll（书库的代码不用，build 时检查）、
// URL 的新方法（canParse 等，不用）。换掉以后 push、URL、正则这些每次调用都慢，打开大书时差出好几秒
const SLOW_REPLACEMENTS = new Set([
  'es.array.push', 'es.array.unshift', 'es.array.sort', 'es.typed-array.sort', 'es.typed-array.set',
  'es.json.stringify', 'es.json.parse', 'es.json.raw-json', 'es.json.is-raw-json',
  'es.regexp.constructor', 'es.regexp.dot-all', 'es.regexp.exec', 'es.regexp.flags', 'es.string.replace',
  'web.url', 'web.url.to-json', 'web.url.parse', 'web.url.can-parse',
  'web.url-search-params', 'web.url-search-params.delete', 'web.url-search-params.has', 'web.url-search-params.size',
])
const CORE_JS = compat({ targets: 'chrome 55', modules: ['core-js/stable'] }).list.filter(m => !SLOW_REPLACEMENTS.has(m))

const rel = p => path.relative(ROOT, p).split(path.sep).join('/')

// 旧浏览器不认识的写法，按文件改掉（改不到就报错：上游更新了，要重新看一下）
const PATCHES = {
  'web/vendor/foliate-js/paginator.js': [
    // 正则的后行断言（Chrome 62）
    [".replace(/(?<=[{\\s;])-epub-/gi, '')", ".replace(/([{\\s;])-epub-/gi, '$1')"],
    // 样式嵌套（Chrome 112）、容器查询（Chrome 105）：旧浏览器整段丢掉，竖屏也排成两栏（墨案 10 寸竖着拿，一页两栏字很挤）。
    // 展开成普通写法；“竖屏”改成下面排版前由脚本标在元素上（data-portrait）
    [`            &.vertical {
                --_max-column-count-spread: var(--_max-column-count-portrait);
                --_max-width: var(--_max-block-size);
                --_max-height: calc(var(--_max-inline-size) * var(--_max-column-count-spread));
            }
            @container (orientation: portrait) {
                & {
                    --_max-column-count-spread: var(--_max-column-count-portrait);
                }
                &.vertical {
                    --_max-column-count-spread: var(--_max-column-count);
                }
            }
        }`, `        }
        #top.vertical {
            --_max-column-count-spread: var(--_max-column-count-portrait);
            --_max-width: var(--_max-block-size);
            --_max-height: calc(var(--_max-inline-size) * var(--_max-column-count-spread));
        }
        :host([data-portrait]) #top {
            --_max-column-count-spread: var(--_max-column-count-portrait);
        }
        :host([data-portrait]) #top.vertical {
            --_max-column-count-spread: var(--_max-column-count);
        }`],
    // :is()（Chrome 88）
    [':is(#header, #footer) > * > * {', '#header > * > *, #footer > * > * {'],
    [':is(#header, #footer) > * {', '#header > *, #footer > * {'],
    // 量尺寸之前先标上横竖（栏数影响 #container 的宽度）
    [`        const { width, height } = this.#container.getBoundingClientRect()
        const size = vertical ? height : width`, `        { const r = this.getBoundingClientRect(); if (r.height >= r.width) this.setAttribute('data-portrait', ''); else this.removeAttribute('data-portrait') }
        const { width, height } = this.#container.getBoundingClientRect()
        const size = vertical ? height : width`],
  ],
  'web/vendor/foliate-js/search.js': [
    // \p{Format}（Chrome 64）：换成格式字符的范围
    ['/[^\\p{Format}]/u', '/[^\\u00AD\\u0600-\\u0605\\u061C\\u06DD\\u070F\\u180E\\u200B-\\u200F\\u202A-\\u202E\\u2060-\\u2064\\u2066-\\u206F\\uFEFF\\uFFF9-\\uFFFB]/'],
  ],
  'web/vendor/foliate-js/mobi.js': [
    // BigInt（Chrome 67）：最多 40 位，用普通数字算（调用的地方本来就转成 Number）
    [`    let bits = 0n
    for (let i = startByte; i <= endByte; i++)
        bits = bits << 8n | BigInt(byteArray[i] ?? 0)
    return (bits >> (8n - BigInt(end & 7))) & 0xffffffffn`,
    `    let bits = 0
    for (let i = startByte; i <= endByte; i++)
        bits = bits * 256 + (byteArray[i] ?? 0)
    return Math.floor(bits / Math.pow(2, 8 - (end & 7))) % 4294967296`],
  ],
  'web/vendor/foliate-js/vendor/zip.js': [
    // BigInt（Chrome 67）：没有就用普通数字（ZIP64 大小、NTFS 时间）
    ['BigInt(', '(typeof BigInt=="function"?BigInt:Number)(', 'all'],
  ],
  'web/vendor/pdfjs/pdf.mjs': [
    // worker 用普通脚本（/legacy/pdf.worker.js）
    [`const worker = new Worker(workerSrc, {
        type: "module"
      });`, 'const worker = new Worker(workerSrc);'],
    // 万一建不了 worker（pdf.js 退回在页面里解析）：用普通脚本把 worker 那一套加载进页面（它会挂在 globalThis.pdfjsWorker 上）
    [`      const worker = await import(/*webpackIgnore: true*/this.workerSrc);
      return worker.WorkerMessageHandler;`, `      await new Promise((resolve, reject) => {
        const s = document.createElement("script");
        s.src = this.workerSrc;
        s.onload = resolve;
        s.onerror = () => reject(new Error("PDF 阅读器没有加载上：" + this.workerSrc));
        document.head.appendChild(s);
      });
      return globalThis.pdfjsWorker.WorkerMessageHandler;`],
  ],
  'web/vendor/pdfjs/pdf.worker.mjs': [
    // \p{Mn}、\p{Cf}（Chrome 64）：模块一加载就要建这个正则，旧浏览器换成常见的范围
    ['const SpecialCharRegExp = new RegExp("^(\\\\s)|(\\\\p{Mn})|(\\\\p{Cf})$", "u");',
      'const SpecialCharRegExp = (() => { try { return new RegExp("^(\\\\s)|(\\\\p{Mn})|(\\\\p{Cf})$", "u"); } catch (e) { return /^(\\s)|([\\u0300-\\u036F\\u0483-\\u0489\\u0591-\\u05BD\\u05BF\\u05C1\\u05C2\\u05C4\\u05C5\\u05C7\\u0610-\\u061A\\u064B-\\u065F\\u0670\\u06D6-\\u06DC\\u06DF-\\u06E4\\u06E7\\u06E8\\u06EA-\\u06ED\\u0E31\\u0E34-\\u0E3A\\u0E47-\\u0E4E\\u1AB0-\\u1AFF\\u1DC0-\\u1DFF\\u20D0-\\u20FF\\uFE20-\\uFE2F])|([\\u00AD\\u0600-\\u0605\\u061C\\u06DD\\u070F\\u200B-\\u200F\\u202A-\\u202E\\u2060-\\u2064\\u2066-\\u206F\\uFEFF\\uFFF9-\\uFFFB])$/; } })();'],
  ],
}

const used = new Set()
const plugin = {
  name: 'shuku-legacy',
  setup(b) {
    // 网页里的绝对路径（/js/…、/vendor/…）指向 web 目录
    b.onResolve({ filter: /^\/(js|vendor)\// }, a => ({ path: path.join(WEB, a.path) }))
    // pdf.js 的加载方式换成普通脚本
    b.onResolve({ filter: /^\.\/pdfjs\.js$/ }, a => rel(a.importer) === 'web/js/reader/pdf.js' ? { path: path.join(HERE, 'src/pdfjs.js') } : undefined)
    // foliate 自己的 PDF 支持用不到（PDF 用书库自己的阅读器），它有顶层 await，不打进来
    b.onResolve({ filter: /^\.\/pdf\.js$/ }, a => rel(a.importer) === 'web/vendor/foliate-js/view.js' ? { path: 'foliate-pdf', namespace: 'stub' } : undefined)
    b.onLoad({ filter: /^foliate-pdf$/, namespace: 'stub' }, () => ({ contents: "export const makePDF = () => { throw new Error('PDF 用书库自己的阅读器打开') }", loader: 'js' }))
    // core-js：只要 Chrome 55 缺的
    b.onResolve({ filter: /^core-js-needed$/ }, () => ({ path: 'core-js-needed', namespace: 'stub' }))
    b.onLoad({ filter: /^core-js-needed$/, namespace: 'stub' }, () => ({ contents: CORE_JS.map(m => `import 'core-js/modules/${m}.js'`).join('\n'), loader: 'js', resolveDir: HERE }))
    b.onLoad({ filter: /\.m?js$/ }, a => {
      const key = rel(a.path)
      const list = PATCHES[key]
      if (!list) return undefined
      let s = fs.readFileSync(a.path, 'utf8')
      for (const [from, to, all] of list) {
        const n = s.split(from).length - 1
        if (n === 0 || (n > 1 && !all)) throw new Error(`${key}：要改的地方找到 ${n} 处（${from.slice(0, 60)}…），上游可能更新了，看一下 tools/legacy/build.mjs 的 PATCHES`)
        s = s.split(from).join(to)
      }
      used.add(key)
      return { contents: s, loader: 'js' }
    })
  },
}

const common = {
  bundle: true,
  format: 'iife',
  target: TARGET,
  charset: 'utf8',
  // 不改变量名：出错时报的位置还看得懂
  minifyWhitespace: true,
  minifySyntax: true,
  legalComments: 'none',
  logLevel: 'warning',
  plugins: [plugin],
  write: false,
}

fs.rmSync(OUT, { recursive: true, force: true })
fs.mkdirSync(OUT, { recursive: true })

const builds = [
  { out: 'polyfills.js', entry: path.join(HERE, 'src/polyfills.js'), minify: true },
  { out: 'app.js', entry: path.join(WEB, 'js/main.js') },
  { out: 'pdf.js', entry: path.join(HERE, 'src/pdf-lib.js'), legalComments: 'eof', minify: true },
  { out: 'pdf.worker.js', entry: path.join(HERE, 'src/pdf-worker.js'), legalComments: 'eof', minify: true },
]

let failed = false
for (const b of builds) {
  const r = await esbuild.build({ ...common, entryPoints: [b.entry], legalComments: b.legalComments || common.legalComments, minifyIdentifiers: !!b.minify })
  const code = r.outputFiles[0].text
  fs.writeFileSync(path.join(OUT, b.out), code)
  // 检查：ES2017（Chrome 58 认识的）以后的语法不能有
  try {
    acorn.parse(code, { ecmaVersion: 2017, sourceType: 'script' })
  } catch (e) {
    failed = true
    console.error(`✗ ${b.out}：有旧浏览器不认识的语法：${e.message}\n  ${code.slice(Math.max(0, e.pos - 120), e.pos + 80)}`)
  }
  // 检查：降级时改写成 new RegExp(...) 的正则里，不能有旧浏览器不认识的写法（后行断言、命名分组、\p{…}、dotAll 的 s 标志；
  // core-js 里补这些的模块上面去掉了）
  const bad = [...code.matchAll(/new RegExp\("((?:[^"\\]|\\.)*)"(?:\s*,\s*"([a-z]*)")?/g)]
    .filter(m => /\(\?<[=!a-zA-Z]|\\\\[pP]\{/.test(m[1]) || /s/.test(m[2] || '')).map(m => m[1] + (m[2] ? ' /' + m[2] : ''))
  const allowed = b.out === 'pdf.worker.js' ? 2 : 0 // pdf.js：XFA 表单里一个（用不到），SpecialCharRegExp 外面包了 try
  if (bad.length > allowed) {
    failed = true
    console.error(`✗ ${b.out}：有旧浏览器不认识的正则：\n  ` + bad.join('\n  '))
  }
  console.log(`${b.out}  ${(code.length / 1024).toFixed(0)} KB`)
}

for (const k of Object.keys(PATCHES)) {
  if (!used.has(k)) { failed = true; console.error(`✗ 没有用上的改动：${k}（文件没有打进来？）`) }
}

// 样式：inset、:is()、颜色写法等降级；env(safe-area-inset-*)（Chrome 69）旧浏览器不认识，用 0
{
  const src = fs.readFileSync(path.join(WEB, 'css/app.css'))
  const r = transformCSS({ filename: 'app.css', code: src, targets: CSS_TARGETS, minify: true })
  let css = Buffer.from(r.code).toString()
  css = css.replace(/env\(\s*safe-area-inset-[a-z]+\s*(?:,\s*([^()]*?))?\)/g, (_, fallback) => (fallback || '0px').trim())
  css = legacyCSS(css)
  fs.writeFileSync(path.join(OUT, 'app.css'), css)
  for (const w of r.warnings || []) console.warn('app.css：', w.message)
  console.log(`app.css  ${(css.length / 1024).toFixed(0)} KB`)
}

if (failed) process.exit(1)
console.log('兼容旧系统的网页已生成：' + rel(OUT))

// ---------- 样式的进一步降级（lightningcss 不管的） ----------
// 1. 选择器列表里有旧浏览器不认识的（:focus-visible、:has() 等），整条规则都会被丢掉：拆开，认识的那些照样生效
// 2. gap：Chrome 84 以前 flex 不支持，Chrome 66 以前 grid 只认 grid-gap。grid 补 grid-gap；flex 换成子元素之间的外边距
function legacyCSS(css) {
  const tree = parse(css)
  const info = new Map() // 选择器 → { display, dir, wrap }（各条规则合起来）
  walk(tree, r => {
    const d = decls(r.body)
    for (const sel of splitTop(r.sel)) {
      const k = sel.trim()
      const o = info.get(k) || {}
      if (d.display) o.display = d.display
      if (d['flex-direction']) o.dir = d['flex-direction']
      if (d['flex-flow']) { o.dir = /column/.test(d['flex-flow']) ? 'column' : o.dir; if (/wrap/.test(d['flex-flow'])) o.wrap = 'wrap' }
      if (d['flex-wrap']) o.wrap = d['flex-wrap']
      info.set(k, o)
    }
  })
  return serialize(transformList(tree, info))
}

function transformList(list, info) {
  const UNSUPPORTED = /:focus-visible|:focus-within|:has\(|:is\(|:where\(|::marker|:placeholder-shown|::backdrop/
  const out = []
  for (const n of list) {
    if (n.type === 'group') { out.push({ ...n, children: transformList(n.children, info) }); continue }
    if (n.type !== 'rule') { out.push(n); continue }
    const sels = splitTop(n.sel)
    const bad = sels.filter(x => UNSUPPORTED.test(x))
    const good = sels.filter(x => !UNSUPPORTED.test(x))
    if (bad.length && good.length) {
      out.push({ type: 'rule', sel: good.join(','), body: n.body }, { type: 'rule', sel: bad.join(','), body: n.body })
    } else out.push(n)
    const d = decls(n.body)
    let row = null, col = null
    if (d.gap) { const v = splitTop(d.gap, ' ').filter(Boolean); row = v[0]; col = v[1] || v[0] }
    if (d['row-gap']) row = d['row-gap']
    if (d['column-gap']) col = d['column-gap']
    if (row == null && col == null) continue
    const zero = v => v == null || v === 'normal' || /^0(px|em|rem)?$/.test(v)
    let gridDone = false
    for (const sel of good) {
      const k = sel.trim()
      const o = { ...(info.get(k) || {}), ...(d.display ? { display: d.display } : {}), ...(d['flex-direction'] ? { dir: d['flex-direction'] } : {}), ...(d['flex-wrap'] ? { wrap: d['flex-wrap'] } : {}) }
      if (/grid/.test(o.display || '')) {
        if (!gridDone) { gridDone = true; out.push({ type: 'rule', sel: good.join(','), body: `grid-row-gap:${zero(row) ? 0 : row};grid-column-gap:${zero(col) ? 0 : col}` }) }
      } else if (/flex/.test(o.display || '')) {
        const column = /column/.test(o.dir || '')
        if (/^wrap/.test(o.wrap || '')) {
          const m = [!zero(col) && `margin-right:${col}`, !zero(row) && `margin-bottom:${row}`].filter(Boolean).join(';')
          if (m) out.push({ type: 'rule', sel: `${k}>*`, body: m })
        } else if (column ? !zero(row) : !zero(col)) {
          out.push({ type: 'rule', sel: `${k}>*+*`, body: column ? `margin-top:${row}` : `margin-left:${col}` })
        }
      }
    }
  }
  return out
}

function walk(list, fn) { for (const n of list) n.type === 'group' ? walk(n.children, fn) : n.type === 'rule' && fn(n) }
function serialize(list) { return list.map(n => n.type === 'group' ? `${n.prelude}{${serialize(n.children)}}` : n.type === 'rule' ? `${n.sel}{${n.body}}` : n.text).join('') }
function decls(body) {
  const o = {}
  for (const part of splitTop(body, ';')) {
    const i = part.indexOf(':')
    if (i > 0) o[part.slice(0, i).trim()] = part.slice(i + 1).trim()
  }
  return o
}
// 按分隔符拆开（括号、引号里的不算）
function splitTop(s, sep = ',') {
  const out = []
  let depth = 0, q = null, start = 0
  for (let i = 0; i < s.length; i++) {
    const c = s[i]
    if (q) { if (c === '\\') i++; else if (c === q) q = null; continue }
    if (c === '"' || c === "'") q = c
    else if (c === '(' || c === '[') depth++
    else if (c === ')' || c === ']') depth--
    else if (c === sep && depth === 0) { out.push(s.slice(start, i)); start = i + 1 }
  }
  out.push(s.slice(start))
  return out.filter(x => x.trim() !== '')
}
function parse(css) {
  const out = []
  let i = 0
  while (i < css.length) {
    const start = i
    let depth = 0, q = null
    while (i < css.length) {
      const c = css[i]
      if (q) { if (c === '\\') i++; else if (c === q) q = null }
      else if (c === '"' || c === "'") q = c
      else if (c === '(') depth++
      else if (c === ')') depth--
      else if ((c === '{' || c === ';') && depth === 0) break
      i++
    }
    const prelude = css.slice(start, i).trim()
    if (i >= css.length) { if (prelude) out.push({ type: 'raw', text: prelude }); break }
    if (css[i] === ';') { out.push({ type: 'raw', text: prelude + ';' }); i++; continue }
    i++
    const bodyStart = i
    let d = 1
    q = null
    while (i < css.length && d > 0) {
      const c = css[i]
      if (q) { if (c === '\\') i++; else if (c === q) q = null }
      else if (c === '"' || c === "'") q = c
      else if (c === '{') d++
      else if (c === '}') d--
      i++
    }
    const body = css.slice(bodyStart, i - 1)
    if (/^@(media|supports|layer|container)\b/.test(prelude)) out.push({ type: 'group', prelude, children: parse(body) })
    else if (prelude.startsWith('@')) out.push({ type: 'raw', text: `${prelude}{${body}}` })
    else out.push({ type: 'rule', sel: prelude, body })
  }
  return out
}
