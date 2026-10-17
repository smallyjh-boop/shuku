// 找出网页上的电子书链接。弹窗打开时用 chrome.scripting.executeScript 注入当前页面（包括里面的框架）运行。
// scanPage 会被序列化后放进网页执行，所以不能引用函数外面的任何东西。
// 只读取页面上的链接，不发任何请求。

export function scanPage() {
  const STRONG = ['epub', 'kepub', 'mobi', 'azw', 'azw3', 'prc', 'fb2', 'fbz', 'djvu', 'cbz', 'cbr', 'cb7', 'cbt', 'pdf', 'lit', 'pdb', 'umd', 'chm', 'lrf', 'snb']
  const WEAK = ['txt', 'zip', 'rar', '7z', 'docx', 'doc', 'rtf', 'odt']
  const ALL = STRONG.concat(WEAK)
  const RANK = { epub: 1, kepub: 2.5, azw3: 3, mobi: 4, azw: 4.5, prc: 4.6, fb2: 5, fbz: 5, pdf: 6, djvu: 7, cbz: 8, cbr: 8.5, cb7: 9, cbt: 9, txt: 10, docx: 11 }
  const NAMES = { fbz: 'FB2.ZIP', kepub: 'KEPUB' }
  const MAX = 300

  const clean = s => String(s || '').replace(/\s+/g, ' ').trim()
  const dec = s => { try { return decodeURIComponent(s) } catch { return s } }
  const extOf = name => {
    const n = String(name || '').toLowerCase()
    if (n.endsWith('.fb2.zip')) return 'fbz'
    if (n.endsWith('.kepub.epub')) return 'kepub'
    const m = n.match(/\.([a-z0-9]{1,5})$/)
    if (!m) return ''
    const e = m[1] === 'djv' ? 'djvu' : m[1]
    return ALL.includes(e) ? e : ''
  }
  // 链接文字只是“下载”“EPUB”“2.3 MB”之类时不能当书名
  const FMT_TOKEN = /(^|[^a-z0-9])(epub3?|kepub|mobi|azw3?|kindle|kf8|pdf|fb2|djvu|cbz|cbr|cb7|txt|zip|rar|7z|docx?|rtf)([^a-z0-9]|$)/i
  const DL_WORDS = /^(?:下载|download|点击下载|立即下载|本地下载|直接下载|下载地址|点此下载|点这里|get|save|link|链接|here|click here|this)\s*\d*$/i
  const titleOk = t => t.length >= 2 && t.length <= 120 && !DL_WORDS.test(t) && !(t.length <= 40 && FMT_TOKEN.test(t)) && !/^[\d.,\s]+(b|kb|mb|gb|k|m)?$/i.test(t)

  function pageTitle() {
    const t = clean(document.querySelector('h1')?.textContent) || clean(document.querySelector('meta[property="og:title"]')?.content) || clean(document.title)
    // 去掉“ | 网站名”之类的后缀
    return t.replace(/\s+[|｜–—-]\s+[^|｜–—-]{2,40}$/, '').slice(0, 120)
  }

  // 链接也可能在 Web Components 的 shadow DOM 里
  const anchors = []
  const visit = root => {
    root.querySelectorAll('a[href], area[href]').forEach(a => anchors.push(a))
    root.querySelectorAll('*').forEach(el => { if (el.shadowRoot) visit(el.shadowRoot) })
  }
  try { visit(document) } catch {}

  const here = pageTitle()
  const items = []
  const seen = new Set()

  for (const a of anchors) {
    if (items.length >= MAX) break
    let u
    try { u = new URL(a.getAttribute('href'), location.href) } catch { continue }
    if (u.protocol !== 'http:' && u.protocol !== 'https:') continue
    u.hash = ''
    if (u.href === location.href.split('#')[0] || seen.has(u.href)) continue
    const path = dec(u.pathname)
    const base = path.split('/').pop() || ''
    const dl = a.getAttribute('download') || ''
    let fmt = extOf(base) || extOf(dl)
    let label = ''
    if (!fmt) {
      // 古腾堡计划：/ebooks/1342.epub3.images、.kf8.images、.kindle.images、.txt.utf-8
      const m = path.match(/\.(epub3?|kf8|kindle|txt)\.(images|noimages|utf-8)$/i)
      if (m) {
        const k = m[1].toLowerCase()
        fmt = { epub: 'epub', epub3: 'epub', kf8: 'azw3', kindle: 'mobi', txt: 'txt' }[k]
        if (k === 'epub3') label = 'EPUB3'
        if (m[2].toLowerCase() === 'noimages') label = (label || fmt.toUpperCase()) + ' 无图'
      }
    }
    if (!fmt) {
      // 下载接口：?format=epub、?type=mobi、?file=xxx.pdf
      for (const [k, v] of u.searchParams) {
        const val = v.toLowerCase().replace(/^epub-?3$/, 'epub')
        if (/^(format|type|ext|fmt|filetype|file_type|dl_format|output)$/i.test(k) && STRONG.includes(val)) { fmt = val; break }
        if (/^(file|filename|name|f|path|download|dl)$/i.test(k) && extOf(dec(v))) { fmt = extOf(dec(v)); break }
      }
    }
    if (!fmt) continue
    seen.add(u.href)

    const text = clean(a.textContent || a.getAttribute('title') || a.getAttribute('aria-label') || a.querySelector('img')?.alt)
    const file = extOf(base) ? base : extOf(dl) ? dl : ''
    const host = u.hostname
    let key = '', title = '', m
    if (/(^|\.)gutenberg\.org$/.test(host) && (m = path.match(/\/(?:ebooks|cache\/epub|files)\/(\d+)/))) {
      key = 'pg:' + m[1]
      if (location.pathname.includes('/ebooks/' + m[1])) title = here
    } else if (/(^|\.)archive\.org$/.test(host) && (m = path.match(/^\/download\/([^/]+)\//))) {
      key = 'ia:' + m[1]
      if (location.pathname.startsWith('/details/' + m[1])) title = here
    } else if (/(^|\.)standardebooks\.org$/.test(host) && (m = path.match(/^\/ebooks\/(.+?)\/downloads\//))) {
      key = 'se:' + m[1]
      if (location.pathname.startsWith('/ebooks/' + m[1])) title = here
      if (/_advanced\.epub$/i.test(base)) label = 'EPUB 新版'
    } else if (file) {
      // 同一目录下同名的几种格式算一本：book.epub / book.mobi
      key = host + path.slice(0, path.lastIndexOf('/') + 1) + file.toLowerCase().replace(/(\.kepub)?\.[a-z0-9]+$/, '').replace(/\.fb2$/, '')
    } else {
      key = u.href
    }
    if (!title && titleOk(text)) title = text
    if (!title && file && !/^(pg)?\d+([-_.].*)?$/i.test(file)) title = dec(file).replace(/(\.kepub)?\.[a-z0-9]+$/i, '').replace(/\.fb2$/i, '').replace(/[_+]+/g, ' ').trim()

    let rank = RANK[fmt] || 12
    if (label.includes('无图')) rank += 0.5
    if (label.includes('新版')) rank += 0.2
    if (label.startsWith('EPUB3')) rank -= 0.1
    items.push({ url: u.href, fmt, label: label || NAMES[fmt] || fmt.toUpperCase(), file: file ? dec(file) : '', text: text.slice(0, 120), key, title, rank, weak: WEAK.includes(fmt) })
  }

  // 互联网档案馆的条目页：整本加入，让书库自己挑最好的格式
  let m
  if (/(^|\.)archive\.org$/.test(location.hostname) && (m = location.pathname.match(/^\/details\/([^/?#]+)/))) {
    const id = dec(m[1])
    items.push({ url: 'ia:' + id, fmt: 'auto', label: '整本（自动选格式）', file: '', text: '', key: 'ia:' + id, title: here, rank: 1.5, weak: false })
  }
  // 维基文库的作品页：用维基文库的导出服务生成整部作品的 EPUB
  if (/\.wikisource\.org$/.test(location.hostname) && document.body?.classList.contains('ns-0') && (m = location.pathname.match(/^\/wiki\/(.+)$/))) {
    const lang = location.hostname.split('.')[0]
    const root = dec(m[1]).replace(/_/g, ' ').split('/')[0]
    const url = 'https://ws-export.wmcloud.org/?format=epub&lang=' + encodeURIComponent(lang) + '&page=' + encodeURIComponent(root)
    if (!seen.has(url)) items.push({ url, fmt: 'epub', label: '整部作品 EPUB', file: '', text: '', key: 'ws:' + root, title: root, rank: 0.9, weak: false })
  }

  return { url: location.href, title: here, host: location.hostname, items }
}

/** 只看链接地址判断格式（右键菜单添加时用） */
export function formatOfURL(url) {
  try {
    const u = new URL(url)
    const p = decodeURIComponent(u.pathname).toLowerCase()
    if (p.endsWith('.fb2.zip')) return 'fbz'
    let m = p.match(/\.(epub3?|kf8|kindle|txt)\.(images|noimages|utf-8)$/)
    if (m) return { epub: 'epub', epub3: 'epub', kf8: 'azw3', kindle: 'mobi', txt: 'txt' }[m[1]]
    m = p.match(/\.([a-z0-9]{1,5})$/)
    if (m) return m[1]
    for (const [k, v] of u.searchParams) if (/^(format|type|ext|fmt)$/i.test(k)) return v.toLowerCase()
  } catch {}
  return ''
}
