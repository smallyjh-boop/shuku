package main

// 大章节拆开排版（服务器这边，阅读器按需取书时用）。
// 一节几百 KB 甚至几 MB 的章节（整本书写在一个文件里之类），墨水屏、旧手机上要先把整节下载下来、
// 在设备上解析一遍才能拆（web/js/reader/split.js），打开书就要等很久，内存小的还会卡死。
// 按需取书时改由书库来拆：文件列表里带上每个大章节的拆法，阅读器只取要看的那一段。
//
// 拆法和 split.js 一样：找到装正文的那一层（body，或者 body 里唯一的那个 div），按它的子元素分段；
// 每段是一个完整的文档，这一段前面的子元素换成隐藏的空元素（个数一样，带着原来的 id），后面的只留带 id 的。
// 正文里每个元素在文档里的位置都和原来一样：位置记录（CFI）、划线、书签和不拆的时候完全通用。
// 只拆能按 XML 正常解析的（浏览器也是按 XML 解析 XHTML 的；解析不了的浏览器会改按 HTML 解析，结构可能不一样）。

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
)

// 占位元素：和 split.js 一样（阅读器的样式里把带这个标记的藏起来）
const splitPH = `data-shuku-ph=""`

type splitPlan struct {
	Steps  []int          `json:"steps"`         // 正文那一层在文档里的位置（CFI 的步：第 n 个子元素是 2n）
	Ranges [][2]int       `json:"ranges"`        // 每段是正文那一层的哪几个子元素 [从, 到)
	Sizes  []int          `json:"sizes"`         // 每段多大（字节）
	Total  int            `json:"total"`         // 子元素加起来多大
	IDs    map[string]int `json:"ids,omitempty"` // id → 在第几段（目录、链接按 id 找）

	kids     [][2]int // 每个子元素在原文里的起止（字节）
	open     int      // 正文那一层的开始标签之后
	close    int      // 正文那一层的结束标签之前
	ownID    []string
	innerIDs [][]string
}

// 拆法参数：超过 limit 字节的章节拆成大约 part 字节一段（阅读器在 ?split=limit,part 里给）
type splitOpts struct{ limit, part int }

func parseSplitOpts(s string) (splitOpts, bool) {
	a, b, ok := strings.Cut(s, ",")
	if !ok {
		return splitOpts{}, false
	}
	l, err1 := strconv.Atoi(a)
	p, err2 := strconv.Atoi(b)
	if err1 != nil || err2 != nil || p < 4000 || l < p {
		return splitOpts{}, false
	}
	return splitOpts{l, p}, true
}

type xnode struct {
	local, space string
	id, name     string
	start        int // 开始标签的 <
	startEnd     int // 开始标签的 > 之后
	endStart     int // 结束标签的 <（自闭合的就是 startEnd）
	end          int // 结束标签的 > 之后
	children     []int
}

// 按 XML 解析出元素树（只要元素和它们在文字里的位置）。解析不了、浏览器多半也解析不了的，返回 false
func parseXMLTree(text []byte) ([]xnode, bool) {
	d := xml.NewDecoder(bytes.NewReader(text))
	d.Strict = true
	// 文档类型是 XHTML 的，浏览器认识 &nbsp; 这类 HTML 的字符实体；不是的话浏览器解析出错，这里也不认
	if i := bytes.Index(text[:min(len(text), 2048)], []byte("<!DOCTYPE")); i >= 0 && bytes.Contains(text[i:min(len(text), i+300)], []byte("//DTD XHTML")) {
		d.Entity = xml.HTMLEntity
	}
	var nodes []xnode
	var stack []int
	for {
		pos := int(d.InputOffset())
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, false
		}
		end := int(d.InputOffset())
		switch t := tok.(type) {
		case xml.StartElement:
			if undeclaredPrefix(t.Name) {
				return nil, false
			}
			n := xnode{local: t.Name.Local, space: t.Name.Space, start: pos, startEnd: end}
			seen := map[xml.Name]bool{}
			for _, a := range t.Attr {
				if seen[a.Name] || undeclaredPrefix(a.Name) { // 属性重名、前缀没声明：浏览器解析出错
					return nil, false
				}
				seen[a.Name] = true
				if a.Name.Space == "" && a.Name.Local == "id" {
					n.id = a.Value
				} else if a.Name.Space == "" && a.Name.Local == "name" {
					n.name = a.Value
				}
			}
			idx := len(nodes)
			if len(stack) > 0 {
				p := stack[len(stack)-1]
				nodes[p].children = append(nodes[p].children, idx)
			} else if idx > 0 {
				return nil, false // 不止一个根元素
			}
			nodes = append(nodes, n)
			stack = append(stack, idx)
		case xml.EndElement:
			if len(stack) == 0 {
				return nil, false
			}
			i := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			nodes[i].endStart, nodes[i].end = pos, end
		}
	}
	if len(nodes) == 0 || len(stack) != 0 {
		return nil, false
	}
	return nodes, true
}

// 前缀没声明（Go 解析时留着原来的前缀，不是命名空间地址）。xmlns: 声明本身不算
func undeclaredPrefix(n xml.Name) bool {
	return n.Space != "" && n.Space != "xmlns" && !strings.Contains(n.Space, ":")
}

// 一个文档怎么拆成 parts 段（和 split.js 的 planSplit 一样）。拆不了返回 nil
func planSplit(text []byte, parts int) *splitPlan {
	off := 0
	if bytes.HasPrefix(text, []byte("\xEF\xBB\xBF")) { // BOM
		off = 3
	}
	nodes, ok := parseXMLTree(text[off:])
	if !ok {
		return nil
	}
	root := 0
	if nodes[root].local != "html" || nodes[root].space == "" {
		return nil
	}
	body := -1
	for _, c := range nodes[root].children {
		if nodes[c].local == "body" {
			body = c
		}
	}
	if body < 0 {
		return nil
	}
	// 正文那一层：body 里只有一个子元素、它下面又有好几个的，往下找
	box := body
	for k := 0; k < 6 && len(nodes[box].children) == 1 && len(nodes[nodes[box].children[0]].children) >= 2; k++ {
		box = nodes[box].children[0]
	}
	kids := nodes[box].children
	if len(kids) < 2 {
		return nil
	}
	// 正文那一层在文档里的位置（从 html 往下，每一步是第几个子元素）
	parent := make([]int, len(nodes))
	parent[root] = -1
	for i := range nodes {
		for _, c := range nodes[i].children {
			parent[c] = i
		}
	}
	var steps []int
	for el := box; el != root; el = parent[el] {
		p := parent[el]
		idx := 0
		for i, c := range nodes[p].children {
			if c == el {
				idx = i
				break
			}
		}
		steps = append([]int{(idx + 1) * 2}, steps...)
	}

	p := &splitPlan{Steps: steps, open: off + nodes[box].startEnd, close: off + nodes[box].endStart}
	p.kids = make([][2]int, len(kids))
	p.ownID = make([]string, len(kids))
	p.innerIDs = make([][]string, len(kids))
	sizes := make([]int, len(kids))
	total := 0
	for n, k := range kids {
		p.kids[n] = [2]int{off + nodes[k].start, off + nodes[k].end}
		sizes[n] = nodes[k].end - nodes[k].start
		total += sizes[n]
		p.ownID[n] = nodes[k].id
		// 里面带 id 的元素（还有 <a name>）：按文档顺序
		var walk func(i int)
		walk = func(i int) {
			for _, c := range nodes[i].children {
				id := nodes[c].id
				if id == "" && nodes[c].local == "a" {
					id = nodes[c].name
				}
				if id != "" {
					p.innerIDs[n] = append(p.innerIDs[n], id)
				}
				walk(c)
			}
		}
		if nodes[k].id == "" && nodes[k].local == "a" && nodes[k].name != "" {
			p.innerIDs[n] = append(p.innerIDs[n], nodes[k].name)
		}
		walk(k)
	}

	// 分段：按大小均分，每段至少一个子元素
	count := min(len(kids), max(2, parts))
	acc, from := 0, 0
	for n := range kids {
		acc += sizes[n]
		left := count - len(p.Ranges) - 1
		if left > 0 && (float64(acc) >= float64(total)*float64(len(p.Ranges)+1)/float64(count) || len(kids)-n-1 == left) && n+1 > from {
			p.Ranges = append(p.Ranges, [2]int{from, n + 1})
			from = n + 1
		}
	}
	p.Ranges = append(p.Ranges, [2]int{from, len(kids)})
	if len(p.Ranges) < 2 {
		return nil
	}
	partOf := make([]int, len(kids))
	for j, r := range p.Ranges {
		s := 0
		for n := r[0]; n < r[1]; n++ {
			partOf[n] = j
			s += sizes[n]
		}
		p.Sizes = append(p.Sizes, s)
	}
	p.Total = total
	p.IDs = map[string]int{}
	for n := range kids {
		if p.ownID[n] != "" {
			p.IDs[p.ownID[n]] = partOf[n]
		}
		for _, id := range p.innerIDs[n] {
			if _, ok := p.IDs[id]; !ok {
				p.IDs[id] = partOf[n]
			}
		}
	}
	return p
}

// 第 j 段的文档（和 split.js 的 partText 一样）
func (p *splitPlan) partText(text []byte, j int) []byte {
	if j < 0 || j >= len(p.Ranges) {
		return nil
	}
	esc := html.EscapeString
	ph := func(b *bytes.Buffer, n int) {
		b.WriteString("<i " + splitPH)
		if p.ownID[n] != "" {
			b.WriteString(` id="` + esc(p.ownID[n]) + `"`)
		}
		if len(p.innerIDs[n]) == 0 {
			b.WriteString("/>")
			return
		}
		b.WriteString(">")
		for _, id := range p.innerIDs[n] {
			b.WriteString(`<i id="` + esc(id) + `"/>`)
		}
		b.WriteString("</i>")
	}
	a, z := p.Ranges[j][0], p.Ranges[j][1]
	var b bytes.Buffer
	b.Grow(len(text)/len(p.Ranges) + 64*a + 1024)
	b.Write(text[:p.open])
	if j == 0 {
		b.Write(text[p.open:p.kids[0][0]])
	}
	for n := 0; n < a; n++ { // 前面的：一个不少（后面元素的位置靠它们）
		ph(&b, n)
	}
	for n := a; n < z; n++ {
		end := p.close
		if n+1 < len(p.kids) {
			end = p.kids[n+1][0]
		}
		b.Write(text[p.kids[n][0]:end])
	}
	for n := z; n < len(p.kids); n++ { // 后面的：只留带 id 的
		if p.ownID[n] != "" || len(p.innerIDs[n]) > 0 {
			ph(&b, n)
		}
	}
	b.Write(text[p.close:])
	return b.Bytes()
}

// ---------- 缓存：拆法（最近的几百个）、解压出来的章节原文（最近的几个，同一章的几段接着取不用再解压） ----------

var splitCache = struct {
	sync.Mutex
	plans  map[string]*splitPlan // nil 也记：拆不了
	porder []string
	texts  map[string][]byte
	torder []string
}{plans: map[string]*splitPlan{}, texts: map[string][]byte{}}

var splitWork keyedMutex

func zipEntryKey(zipPath string, st os.FileInfo, name string) string {
	return fmt.Sprintf("%s|%d|%d|%s", zipPath, st.ModTime().UnixNano(), st.Size(), name)
}

// 章节原文（解压后的）
func zipEntryText(zf *zip.File, key string) ([]byte, error) {
	splitCache.Lock()
	if t, ok := splitCache.texts[key]; ok {
		splitCache.Unlock()
		return t, nil
	}
	splitCache.Unlock()
	if zf.UncompressedSize64 > 64<<20 {
		return nil, fmt.Errorf("章节太大")
	}
	rc, err := zf.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	t, err := io.ReadAll(rc)
	if err != nil {
		return nil, err
	}
	splitCache.Lock()
	splitCache.texts[key] = t
	splitCache.torder = append(splitCache.torder, key)
	for len(splitCache.torder) > 4 {
		delete(splitCache.texts, splitCache.torder[0])
		splitCache.torder = splitCache.torder[1:]
	}
	splitCache.Unlock()
	return t, nil
}

// 这个章节的拆法（不用拆、拆不了返回 nil）
func zipEntryPlan(zf *zip.File, key string, o splitOpts) *splitPlan {
	if int(zf.UncompressedSize64) <= o.limit || !splittable(zf.Name) {
		return nil
	}
	pkey := fmt.Sprintf("%s|%d,%d", key, o.limit, o.part)
	splitCache.Lock()
	if p, ok := splitCache.plans[pkey]; ok {
		splitCache.Unlock()
		return p
	}
	splitCache.Unlock()
	unlock := splitWork.Lock(pkey) // 同一章同时只算一次
	defer unlock()
	splitCache.Lock()
	if p, ok := splitCache.plans[pkey]; ok {
		splitCache.Unlock()
		return p
	}
	splitCache.Unlock()
	var p *splitPlan
	if text, err := zipEntryText(zf, key); err == nil {
		p = planSplit(text, (int(zf.UncompressedSize64)+o.part-1)/o.part)
	}
	splitCache.Lock()
	splitCache.plans[pkey] = p
	splitCache.porder = append(splitCache.porder, pkey)
	for len(splitCache.porder) > 400 {
		delete(splitCache.plans, splitCache.porder[0])
		splitCache.porder = splitCache.porder[1:]
	}
	splitCache.Unlock()
	return p
}

func splittable(name string) bool {
	n := strings.ToLower(name)
	return strings.HasSuffix(n, ".xhtml") || strings.HasSuffix(n, ".xht") || strings.HasSuffix(n, ".html") || strings.HasSuffix(n, ".htm")
}
