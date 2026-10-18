package main

import (
	"archive/zip"
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"strings"
	"testing"
)

// 造一个复杂一点的章节（和浏览器里测 split.js 的一样）：外面套一层 div、标题带 id、段落里有行内元素、
// 注释、实体、空元素、带 name 的锚点、嵌套块
func splitTestDoc(wrap bool, doctype string) []byte {
	var paras []string
	for i := 0; i < 400; i++ {
		if i%50 == 0 {
			paras = append(paras, fmt.Sprintf(`<h2 id="h%d">第%d节 标题</h2>`, i, i/50+1))
		}
		if i%37 == 0 {
			paras = append(paras, fmt.Sprintf(`<!-- 注释 %d -->`, i))
		}
		if i%41 == 0 {
			paras = append(paras, fmt.Sprintf(`<div class="box"><p id="inner%d">嵌套的段落 %d，<span>行内</span>文字</p><p>第二段 &amp; 实体 &#x4e00;</p></div>`, i, i))
		}
		if i%53 == 0 {
			paras = append(paras, fmt.Sprintf(`<p><a name="anc%d"></a>带锚点的段落 %d</p>`, i, i))
		}
		if i%61 == 0 {
			paras = append(paras, fmt.Sprintf(`<img src="x%d.jpg" alt=""/>`, i))
		}
		paras = append(paras, fmt.Sprintf("<p class=\"t%d\">这是第 %d 段，<em>强调</em>的文字和<b>加粗</b>，还有一些普通的句子用来凑够长度。%s</p>\n", i%3, i, strings.Repeat("很长的内容", i%7+3)))
	}
	inner := strings.Join(paras, "\n")
	body := inner
	if wrap {
		body = "<div class=\"wrap\" id=\"wrap\">\n" + inner + "\n</div>"
	}
	return []byte("<?xml version=\"1.0\" encoding=\"utf-8\"?>\n" + doctype + "\n<html xmlns=\"http://www.w3.org/1999/xhtml\" xmlns:epub=\"http://www.idpf.org/2007/ops\"><head><title>t</title><link rel=\"stylesheet\" href=\"s.css\"/></head>\n<body class=\"b\">\n" + body + "\n</body></html>")
}

// 按 CFI 的步找到元素
func nodeAtSteps(nodes []xnode, steps []int) int {
	el := 0
	for _, s := range steps {
		i := s/2 - 1
		if i < 0 || i >= len(nodes[el].children) {
			return -1
		}
		el = nodes[el].children[i]
	}
	return el
}

func TestPlanSplitKeepsPositions(t *testing.T) {
	for _, wrap := range []bool{false, true} {
		for _, doctype := range []string{"<!DOCTYPE html>", `<!DOCTYPE html PUBLIC "-//W3C//DTD XHTML 1.1//EN" "http://www.w3.org/TR/xhtml11/DTD/xhtml11.dtd">`} {
			text := splitTestDoc(wrap, doctype)
			if strings.Contains(doctype, "XHTML") {
				text = bytes.Replace(text, []byte("第二段 &amp;"), []byte("第二段&nbsp;&amp;"), -1) // XHTML 文档类型里能用 HTML 的实体
			}
			plan := planSplit(text, (len(text)+5999)/6000)
			if plan == nil {
				t.Fatalf("wrap=%v：拆不了", wrap)
			}
			orig, _ := parseXMLTree(text)
			obox := nodeAtSteps(orig, plan.Steps)
			okids := orig[obox].children
			if len(okids) != len(plan.kids) || len(plan.Ranges) < 5 {
				t.Fatalf("wrap=%v：子元素 %d / %d，分了 %d 段", wrap, len(okids), len(plan.kids), len(plan.Ranges))
			}
			if wrap && (len(plan.Steps) != 2 || plan.Steps[1] != 2) {
				t.Fatalf("套了一层 div 的应该往下找一层：%v", plan.Steps)
			}
			seenContent := 0
			for j, r := range plan.Ranges {
				pt := plan.partText(text, j)
				nodes, ok := parseXMLTree(pt)
				if !ok {
					t.Fatalf("第 %d 段解析不了", j)
				}
				box := nodeAtSteps(nodes, plan.Steps)
				kids := nodes[box].children
				if len(kids) < r[1] {
					t.Fatalf("第 %d 段：子元素只有 %d 个，要到 %d", j, len(kids), r[1])
				}
				for n := 0; n < r[1]; n++ {
					k, o := nodes[kids[n]], orig[okids[n]]
					if n < r[0] {
						if !bytes.Contains(pt[k.start:k.startEnd], []byte(splitPH)) || k.id != o.id {
							t.Fatalf("第 %d 段第 %d 个应该是占位（id %q）：%s", j, n, o.id, pt[k.start:k.startEnd])
						}
						continue
					}
					if k.local != o.local || k.id != o.id || !bytes.Equal(pt[k.start:k.end], text[o.start:o.end]) {
						t.Fatalf("第 %d 段第 %d 个和原文不一样", j, n)
					}
					seenContent++
				}
				// 后面只留带 id 的占位
				for n := r[1]; n < len(kids); n++ {
					if !bytes.Contains(pt[nodes[kids[n]].start:nodes[kids[n]].startEnd], []byte(splitPH)) {
						t.Fatalf("第 %d 段后面的第 %d 个不是占位", j, n)
					}
				}
			}
			if seenContent != len(okids) {
				t.Fatalf("各段的正文加起来 %d 个子元素，原文 %d 个", seenContent, len(okids))
			}
			// 每个 id 都在它那一段里是真的元素
			for id, j := range plan.IDs {
				pt := plan.partText(text, j)
				nodes, _ := parseXMLTree(pt)
				box := nodeAtSteps(nodes, plan.Steps)
				found := false
				var walk func(i int, inPH bool)
				walk = func(i int, inPH bool) {
					n := nodes[i]
					ph := inPH || bytes.Contains(pt[n.start:n.startEnd], []byte(splitPH))
					if !ph && (n.id == id || (n.local == "a" && n.name == id)) {
						found = true
					}
					for _, c := range n.children {
						walk(c, ph)
					}
				}
				walk(box, false)
				if !found {
					t.Fatalf("id %q 在第 %d 段里找不到", id, j)
				}
			}
			if len(plan.IDs) < 20 {
				t.Fatalf("id 太少：%d", len(plan.IDs))
			}
		}
	}
}

func TestPlanSplitRefusesBrokenXML(t *testing.T) {
	good := splitTestDoc(false, "<!DOCTYPE html>")
	cases := map[string][]byte{
		"没声明的前缀":                 bytes.Replace(good, []byte("<p class=\"t1\">"), []byte("<p class=\"t1\"><foo:bar/>"), 1),
		"属性重名":                   bytes.Replace(good, []byte("<p class=\"t1\">"), []byte("<p class=\"t1\" class=\"x\">"), 1),
		"没有 XHTML 文档类型时用 &nbsp;": bytes.Replace(good, []byte("第二段 &amp;"), []byte("第二段&nbsp;"), 1),
		"标签没闭合":                  bytes.Replace(good, []byte("</em>"), []byte(""), 1),
		"不是 XHTML 命名空间":          bytes.Replace(good, []byte(` xmlns="http://www.w3.org/1999/xhtml"`), nil, 1),
	}
	for name, text := range cases {
		if planSplit(text, 10) != nil {
			t.Errorf("%s：不该拆", name)
		}
	}
	// BOM：照样能拆，各段也带着 BOM
	bom := append([]byte("\xEF\xBB\xBF"), good...)
	p := planSplit(bom, 10)
	if p == nil || !bytes.HasPrefix(p.partText(bom, 1), []byte("\xEF\xBB\xBF<?xml")) {
		t.Fatal("带 BOM 的拆不了")
	}
	// 只有一个子元素的：不拆
	one := []byte(`<?xml version="1.0"?><html xmlns="http://www.w3.org/1999/xhtml"><head><title>t</title></head><body><p>` + strings.Repeat("字", 100000) + `</p></body></html>`)
	if planSplit(one, 10) != nil {
		t.Fatal("只有一个段落的不该拆")
	}
}

func TestShrinkImage(t *testing.T) {
	// 3000×2000 的 JPEG：缩到 1404×1872 以内
	big := image.NewRGBA(image.Rect(0, 0, 3000, 2000))
	for y := 0; y < 2000; y++ {
		for x := 0; x < 3000; x++ {
			big.Set(x, y, color.RGBA{uint8(x), uint8(y), uint8(x ^ y), 255})
		}
	}
	var jb bytes.Buffer
	jpeg.Encode(&jb, big, &jpeg.Options{Quality: 95})
	out, ctype, ok := shrinkImage(jb.Bytes(), 1404, 1872)
	if !ok || ctype != "image/jpeg" {
		t.Fatalf("大 JPEG 没缩：%v %s", ok, ctype)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(out))
	if err != nil || cfg.Width != 1404 || cfg.Height != 936 {
		t.Fatalf("缩出来的大小不对：%v %dx%d", err, cfg.Width, cfg.Height)
	}
	// 带透明的 PNG：还是 PNG
	tp := image.NewNRGBA(image.Rect(0, 0, 2000, 4000))
	tp.Set(10, 10, color.NRGBA{255, 0, 0, 128})
	var pb bytes.Buffer
	png.Encode(&pb, tp)
	out, ctype, ok = shrinkImage(pb.Bytes(), 1404, 1872)
	if !ok || ctype != "image/png" {
		t.Fatalf("透明 PNG：%v %s", ok, ctype)
	}
	cfg, _, _ = image.DecodeConfig(bytes.NewReader(out))
	if cfg.Height != 1872 || cfg.Width != 936 {
		t.Fatalf("透明 PNG 缩出来 %dx%d", cfg.Width, cfg.Height)
	}
	// 本来就不大的：不缩
	small := image.NewRGBA(image.Rect(0, 0, 800, 600))
	var sb bytes.Buffer
	jpeg.Encode(&sb, small, nil)
	if _, _, ok := shrinkImage(sb.Bytes(), 1404, 1872); ok {
		t.Fatal("小图不该缩")
	}
	if _, _, ok := shrinkImage([]byte("不是图"), 1404, 1872); ok {
		t.Fatal("不是图的不该缩")
	}
}

func TestZipInlineFiles(t *testing.T) {
	dir := t.TempDir()
	p := dir + "/t.epub"
	f, _ := os.Create(p)
	zw := zip.NewWriter(f)
	add := func(name, s string) { w, _ := zw.Create(name); w.Write([]byte(s)) }
	add("mimetype", "application/epub+zip")
	add("META-INF/container.xml", `<?xml version="1.0"?><container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`)
	add("OEBPS/content.opf", `<?xml version="1.0"?><package xmlns="http://www.idpf.org/2007/opf" version="3.0"><manifest><item id="nav" href="Text/nav%20x.xhtml" media-type="application/xhtml+xml" properties="nav"/><item id="c1" href="Text/c1.xhtml" media-type="application/xhtml+xml"/></manifest><spine><itemref idref="c1"/></spine></package>`)
	add("OEBPS/Text/nav x.xhtml", `<html/>`)
	add("OEBPS/Text/c1.xhtml", `<html/>`)
	zw.Close()
	f.Close()
	zr, _ := zip.OpenReader(p)
	defer zr.Close()
	got := zipInlineFiles(zr)
	for _, n := range []string{"META-INF/container.xml", "OEBPS/content.opf", "OEBPS/Text/nav x.xhtml"} {
		if _, ok := got[n]; !ok {
			t.Errorf("没带上 %s：%v", n, keysOf(got))
		}
	}
	if _, ok := got["OEBPS/Text/c1.xhtml"]; ok {
		t.Error("正文不该带上")
	}
}

func keysOf(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
