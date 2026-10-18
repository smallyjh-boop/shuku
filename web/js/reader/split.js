// 超大的章节拆开排版。整本书写在一个文件里之类的 EPUB，一节有几百 KB 甚至几 MB：
// 翻页器每次都要把整节排一遍，墨水屏、旧手机上要几十秒，内存不够时直接卡死。
//
// 拆法：找到装正文的那一层（body，或者 body 里唯一的那个 div），按它的子元素分成几段，每段做成一个完整的文档——
// 这一段前面的子元素换成隐藏的空元素（个数一样，带着原来的 id），后面的只留带 id 的。
// 这样正文里每个元素在文档里的位置都和原来一样：位置记录（CFI）、划线、书签和不拆的时候完全通用，
// 别的设备不拆也对得上；目录、链接按 id 找到在哪一段。
// 拆出来的每段在书里是单独的一节（book.sections 变长）。按节序号算的地方（手表、服务器用的 sec:节:比例，
// 服务器全文搜索给的节序号）用 toOrig / toPart 换算。只拆能按 XHTML 正常解析的文档，拆不了的照原样。
// 在线按需取书时由书库来拆（server/zipsplit.go，同一套拆法）：不用先把整节下载下来、在设备上解析，只取要看的那一段。

import * as CFI from '/vendor/foliate-js/epubcfi.js'

const XHTML = 'application/xhtml+xml'
const MARK = '#shuku-part-' // 拆出来的每段的地址：原来的地址 + 这个 + 段号（# 后面的部分不影响图片等相对地址）
// 占位的空元素：只带这个标记（每段可能有几千个，越短越好），阅读器的样式里把它们藏起来（flow.js 的 css）
const PH = 'data-shuku-ph=""'

const esc = s => String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;')

// 没拆（或者没有要拆的）时的换算：节序号不变
export const NO_SPLIT = {
  split: false,
  toPart: (index, frac) => ({ index, frac }),
  toOrig: (index, frac) => ({ index, frac }),
  partsOf: index => [index],
  lastPartOf: index => index,
}

// 一个文档怎么拆：找到正文那一层和它的子元素在文字里的起止位置，按字数均分成 parts 段。拆不了返回 null
export function planSplit(text, parts) {
  // 先用浏览器自己的 XML 解析器确认能正常解析（foliate 也是这样解析的；解析不了的会改用 HTML 解析，结构可能不一样，不拆）
  let doc
  try { doc = new DOMParser().parseFromString(text, XHTML) } catch { return null }
  if (!doc || doc.getElementsByTagName('parsererror').length || !doc.documentElement?.namespaceURI) return null
  const html = doc.documentElement
  let body = null
  for (const el of html.children) if (el.localName === 'body') body = el
  if (!body) return null
  // 正文那一层：body 里只有一个子元素、它下面又有好几个的，往下找
  let box = body
  for (let k = 0; k < 6 && box.children.length === 1 && box.children[0].children.length >= 2; k++) box = box.children[0]
  const kids = Array.from(box.children)
  if (kids.length < 2) return null
  // 正文那一层在文档里的位置（CFI 的步：第 n 个子元素是 2n+2）
  const steps = []
  for (let el = box; el !== html; el = el.parentElement) steps.unshift((Array.prototype.indexOf.call(el.parentElement.children, el) + 1) * 2)

  // 每个子元素自己的 id 和里面的 id（目录、链接、foliate 判断“当前章节”要用；占位的空元素也带上）
  const ownId = kids.map(el => el.getAttribute('id') || '')
  const innerIds = kids.map(() => [])
  const kidIndex = new Map(kids.map((el, n) => [el, n]))
  for (const el of box.querySelectorAll('[id], a[name]')) {
    let top = el
    while (top.parentElement && top.parentElement !== box) top = top.parentElement
    const n = kidIndex.get(top)
    if (n == null || top === el && el.getAttribute('id')) continue
    const id = el.getAttribute('id') || el.getAttribute('name')
    if (id) innerIds[n].push(id)
  }

  // 在文字里找到这些子元素的起止位置：按标签扫一遍（文档能按 XML 解析，标签一定是配对的）
  const pos = scanChildren(text, steps.length, steps)
  if (!pos || pos.kids.length !== kids.length) return null
  for (let n = 0; n < kids.length; n++) if (pos.kids[n].name.replace(/^.*:/, '') !== kids[n].localName) return null

  // 分段：按字数均分，每段至少一个子元素
  const sizes = pos.kids.map(c => c.e - c.s)
  const total = sizes.reduce((a, b) => a + b, 0)
  const count = Math.min(kids.length, Math.max(2, parts | 0))
  const ranges = []
  let acc = 0, from = 0
  for (let n = 0; n < kids.length; n++) {
    acc += sizes[n]
    const left = count - ranges.length - 1 // 还要分几段
    if (left > 0 && (acc >= total * (ranges.length + 1) / count || kids.length - n - 1 === left) && n + 1 > from) {
      ranges.push([from, n + 1]); from = n + 1
    }
  }
  ranges.push([from, kids.length])
  if (ranges.length < 2) return null
  const partOfChild = new Int32Array(kids.length)
  ranges.forEach(([a, b], j) => { for (let n = a; n < b; n++) partOfChild[n] = j })
  const ids = new Map()
  for (let n = 0; n < kids.length; n++) {
    if (ownId[n]) ids.set(ownId[n], partOfChild[n])
    for (const id of innerIds[n]) if (!ids.has(id)) ids.set(id, partOfChild[n])
  }
  const partSizes = ranges.map(([a, b]) => { let s = 0; for (let n = a; n < b; n++) s += sizes[n]; return s })
  return { steps, kids: pos.kids, open: pos.open, close: pos.close, ownId, innerIds, ranges, partSizes, total, ids, partOfChild }
}

// 扫标签：找到 steps 指的那个元素（从 html 往下，每一步是第几个子元素：第 k 个是 2k），记下它的子元素的起止位置
function scanChildren(text, depth, steps) {
  const re = /<!--[\s\S]*?-->|<!\[CDATA\[[\s\S]*?\]\]>|<\?[\s\S]*?\?>|<![^>]*>|<\/\s*([^\s>]+)\s*>|<([^\s/>!?]+)((?:[^>"']|"[^"]*"|'[^']*')*?)(\/?)>/g
  // 当前路径上的元素：每个记着已经有几个子元素、是不是在去目标的路上（html 在第 0 层，目标在第 depth 层）
  const stack = [] // { name, kids, onPath }
  let target = null, open = -1, close = -1
  const kidsPos = []
  let m
  while ((m = re.exec(text))) {
    if (m[2] != null) { // 开始标签
      const name = m[2]
      const parent = stack[stack.length - 1]
      let onPath = !parent // 最外层（html）
      if (parent) {
        parent.kids++
        // 新元素在第 stack.length 层，对应 steps[stack.length - 1]
        onPath = parent.onPath && parent !== target && stack.length <= depth && steps[stack.length - 1] === parent.kids * 2
      }
      if (parent && parent === target) kidsPos.push({ s: m.index, e: -1, name })
      const selfClose = m[4] === '/'
      if (!selfClose) {
        const node = { name, kids: 0, onPath }
        stack.push(node)
        if (onPath && stack.length - 1 === depth) { target = node; open = re.lastIndex }
      } else if (parent && parent === target) kidsPos[kidsPos.length - 1].e = re.lastIndex
    } else if (m[1] != null) { // 结束标签
      const node = stack.pop()
      if (!node || node.name !== m[1]) return null
      const parent = stack[stack.length - 1]
      if (parent && parent === target) kidsPos[kidsPos.length - 1].e = re.lastIndex
      if (node === target) { close = m.index; break }
    }
  }
  if (!target || close < 0 || kidsPos.some(k => k.e < 0)) return null
  return { kids: kidsPos, open, close }
}

// 第 j 段的文档
export function partText(text, plan, j) {
  const { kids, open, close, ranges, ownId, innerIds } = plan
  const [a, b] = ranges[j]
  const ph = n => {
    const inner = innerIds[n].map(id => `<i id="${esc(id)}"/>`).join('')
    const own = ownId[n] ? ` id="${esc(ownId[n])}"` : ''
    return inner ? `<i ${PH}${own}>${inner}</i>` : `<i ${PH}${own}/>`
  }
  const out = [text.slice(0, open)]
  if (j === 0) out.push(text.slice(open, kids[0].s))
  for (let n = 0; n < a; n++) out.push(ph(n)) // 前面的：一个不少（后面元素的位置靠它们）
  for (let n = a; n < b; n++) out.push(text.slice(kids[n].s, n + 1 < kids.length ? kids[n + 1].s : close))
  for (let n = b; n < kids.length; n++) if (ownId[n] || innerIds[n].length) out.push(ph(n)) // 后面的：只留带 id 的
  out.push(text.slice(close))
  return out.join('')
}

// 位置（CFI）在拆出来的哪一段：看位置的路径在正文那一层是第几个子元素
export function partOfCFI(plan, cfi) {
  let parts
  try { parts = CFI.collapse(CFI.parse(cfi)) } catch { return 0 }
  const local = parts?.[1]
  if (!local) return 0
  for (let k = 0; k < plan.steps.length; k++) if (local[k]?.index !== plan.steps[k]) return 0
  const step = local[plan.steps.length]
  if (!step) return 0
  const n = step.index % 2 === 0 ? step.index / 2 - 1 : (step.index - 1) / 2 - 1 // 奇数：两个元素之间，算前面那个
  return n < 0 ? 0 : plan.partOfChild[Math.min(n, plan.partOfChild.length - 1)]
}

// 书库给的拆法（server/zipsplit.go 的 splitPlan）换成这里的格式
function fromServer(p) {
  const kids = p.ranges[p.ranges.length - 1][1]
  const partOfChild = new Int32Array(kids)
  p.ranges.forEach(([a, b], j) => { for (let n = a; n < b; n++) partOfChild[n] = j })
  return { steps: p.steps, ranges: p.ranges, partSizes: p.sizes, total: p.total, ids: new Map(Object.entries(p.ids || {})), partOfChild }
}

// 把书里超过 limit 的章节拆成大约 partSize 一段。返回节序号的换算（见上面 NO_SPLIT）。
// remote：在线按需取书时书库给的拆法 { plans: { 文件名: 拆法 }, loadPart(文件名, 段号) }，有就直接用（书库没拆的不拆）
export async function splitBigSections(book, { limit, partSize }, remote) {
  const sections = book?.sections
  // 只拆可重排的 EPUB（固定版式的每节就是一页）
  if (!sections?.length || typeof book.loadItem !== 'function' || !book.resources?.getItemByHref
    || book.rendition?.layout === 'pre-paginated') return NO_SPLIT
  const plans = new Map() // 原来的节序号 → 拆法
  const texts = new Map() // 拆开的章节原文（最近用的两个，其余用到时重新读）
  const loadText = book.loadText
  for (let i = 0; i < sections.length; i++) {
    const s = sections[i]
    if (!(s?.size > limit)) continue
    const item = book.resources.getItemByHref(s.id)
    if (!item || item.mediaType !== XHTML) continue
    if (remote) {
      const p = remote.plans?.[item.href]
      if (p?.ranges?.length > 1) plans.set(i, { ...fromServer(p), item, section: s, remote: true })
      continue
    }
    let text = null
    try { text = await loadText(item.href) } catch {}
    const plan = text && planSplit(text, Math.ceil(s.size / partSize)) // size、partSize 都是字节数
    if (!plan) continue
    plans.set(i, { ...plan, item, section: s })
    remember(i, text)
  }
  if (!plans.size) return NO_SPLIT
  function remember(i, text) {
    texts.delete(i); texts.set(i, text)
    while (texts.size > 2) texts.delete(texts.keys().next().value)
  }

  // 新的节列表；记下新旧序号的对应
  const out = [], firstOf = [], origOf = []
  sections.forEach((s, i) => {
    firstOf[i] = out.length
    const plan = plans.get(i)
    if (!plan) { origOf.push({ orig: i, j: 0 }); out.push(s); return }
    plan.first = out.length
    plan.ranges.forEach((_, j) => {
      const item = { ...plan.item, href: plan.item.href + MARK + j, id: plan.item.id + MARK + j }
      origOf.push({ orig: i, j })
      out.push({
        ...s,
        id: j === 0 ? s.id : s.id + MARK + j,
        // 按字数比例分原来的大小（别的章节的大小是字节数，进度百分比照旧连贯）
        size: Math.max(1, Math.round(s.size * plan.partSizes[j] / plan.total)),
        load: () => book.loadItem(item),
        unload: () => book.unloadItem(item),
        textHref: item.href, // 这一段的文字从哪取（flow.js 预先取下一段用）
        createDocument: () => book.loadDocument(item),
        mediaOverlay: j === 0 ? s.mediaOverlay : null,
      })
    })
  })
  book.sections = out

  // 读拆出来的那一段：按需生成（foliate 载入章节、搜索、听书都从 loadText 取文字）
  book.loadText = async href => {
    const k = typeof href === 'string' ? href.indexOf(MARK) : -1
    if (k < 0) return loadText(href)
    const path = href.slice(0, k), j = +href.slice(k + MARK.length)
    for (const [i, plan] of plans) {
      if (plan.item.href !== path) continue
      if (plan.remote) return remote.loadPart(path, j) // 书库拆好的那一段
      let text = texts.get(i)
      if (text == null) text = await loadText(path)
      remember(i, text)
      return partText(text, plan, j)
    }
    return null
  }

  const resolveCFI = book.resolveCFI.bind(book)
  book.resolveCFI = cfi => {
    const r = resolveCFI(cfi)
    if (!r || !(r.index >= 0)) return r
    const plan = plans.get(r.index)
    return { ...r, index: plan ? plan.first + partOfCFI(plan, cfi) : firstOf[r.index] }
  }
  const resolveHref = book.resolveHref.bind(book)
  book.resolveHref = href => {
    const r = resolveHref(href)
    if (!r || !(r.index >= 0)) return r
    const plan = plans.get(r.index)
    if (!plan) return { ...r, index: firstOf[r.index] }
    let frag = String(href).split('#')[1]
    try { frag = frag && decodeURIComponent(frag) } catch {}
    return { ...r, index: plan.first + (frag && plan.ids.has(frag) ? plan.ids.get(frag) : 0) }
  }

  const fracStart = (plan, j) => { let s = 0; for (let x = 0; x < j; x++) s += plan.partSizes[x]; return s / plan.total }
  return {
    split: true,
    parts: [...plans.values()].map(p => p.ranges.length),
    // 原来的第 index 节、节内比例 frac → 拆开以后的哪一节、节内比例
    toPart(index, frac = 0) {
      const plan = plans.get(index)
      if (!plan) return { index: firstOf[index] ?? index, frac }
      let j = 0
      while (j < plan.ranges.length - 1 && frac >= fracStart(plan, j + 1)) j++
      const a = fracStart(plan, j), w = plan.partSizes[j] / plan.total
      return { index: plan.first + j, frac: w > 0 ? Math.min(1, Math.max(0, (frac - a) / w)) : 0 }
    },
    // 反过来：拆开以后的第 index 节 → 原来的第几节、节内比例
    toOrig(index, frac = 0) {
      const o = origOf[index]
      if (!o) return { index, frac }
      const plan = plans.get(o.orig)
      if (!plan) return { index: o.orig, frac }
      return { index: o.orig, frac: fracStart(plan, o.j) + frac * plan.partSizes[o.j] / plan.total }
    },
    // 原来的第 index 节拆成了哪几节
    partsOf(index) {
      const plan = plans.get(index)
      return plan ? plan.ranges.map((_, j) => plan.first + j) : [firstOf[index] ?? index]
    },
    // 和第 index 节同属一章的最后一段
    lastPartOf(index) {
      const o = origOf[index]
      const plan = o && plans.get(o.orig)
      return plan ? plan.first + plan.ranges.length - 1 : index
    },
  }
}
