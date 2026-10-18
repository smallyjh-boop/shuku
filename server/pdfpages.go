package main

// 墨水屏、旧浏览器看 PDF：书库把每一页画成和屏幕一样宽的图（mutool draw），文字另外给（每行的位置和文字），
// 设备上不用下载整本、不用 pdf.js 解析和画字体（墨水屏上又慢又占内存，旧的网页组件还会丢字）。
//
//   GET /api/files/{fid}/pdf/info          页数、每页大小、目录
//   GET /api/files/{fid}/pdf/page/{n}?w=   第 n 页（从 1 开始）的图，w 是宽度（像素），g=1 画成 16 级灰度（墨水屏）
//   GET /api/files/{fid}/pdf/text/{n}      第 n 页的文字：每行一项，位置按页面大小折成 0～1（和扫描版识别出的一样）
//
// 没有 mutool 时用 poppler（pdfinfo / pdftoppm / pdftotext），都没有就 501，网页退回 pdf.js。

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type pdfOutlineItem struct {
	Title string `json:"title"`
	Page  int    `json:"page"`
	Level int    `json:"level"`
}

type pdfInfoResp struct {
	Pages   int              `json:"pages"`
	Sizes   [][2]float64     `json:"sizes"` // 每页的宽高（点），读不出的是 0
	Outline []pdfOutlineItem `json:"outline"`
}

type pdfTextLine struct {
	Text string  `json:"text"`
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	W    float64 `json:"w"`
	H    float64 `json:"h"`
}

type pdfTextResp struct {
	Page  int           `json:"page"`
	Lines []pdfTextLine `json:"lines"`
}

// 同时最多画两页（NAS 的 CPU 留给别的）；同一页同时只画一次
var (
	pdfDrawSlots = make(chan struct{}, 2)
	pdfWork      keyedMutex
	pdfKeys      sync.Map // 文件路径|大小|修改时间 → 缓存键（算缓存键要读文件开头，记下来）
)

func pdfToolsAvailable() bool {
	if _, err := execLookPath("mutool"); err == nil {
		return true
	}
	_, e1 := execLookPath("pdftoppm")
	_, e2 := execLookPath("pdfinfo")
	return e1 == nil && e2 == nil
}

// 这个文件给阅读器看的 PDF：PDF 本身，或者 DJVU 转成的 PDF
func pdfSourceOf(f *File) (string, error) {
	if f.Reader == "pdf-converted" {
		return ensurePDF(f.Path)
	}
	if f.Reader != "pdf" {
		return "", errors.New("不是 PDF")
	}
	return f.Path, nil
}

func pdfCacheDir(src string) string {
	st, err := os.Stat(src)
	id := src
	if err == nil {
		id = fmt.Sprintf("%s|%d|%d", src, st.Size(), st.ModTime().UnixNano())
	}
	if k, ok := pdfKeys.Load(id); ok {
		return k.(string)
	}
	dir := filepath.Join(cfg.DataDir, "cache", "pages", cacheKey(src))
	pdfKeys.Store(id, dir)
	return dir
}

// 先打开文件、找到 PDF；出错时已经回了错误
func pdfFileFor(w http.ResponseWriter, r *http.Request) (string, bool) {
	f, ok := visibleFile(w, r)
	if !ok {
		return "", false
	}
	if !pdfToolsAvailable() {
		errJSON(w, 501, "书库没有安装 mutool")
		return "", false
	}
	src, err := pdfSourceOf(f)
	if err != nil {
		errJSON(w, 400, err.Error())
		return "", false
	}
	if _, err := os.Stat(src); err != nil {
		errJSON(w, 404, "文件不存在，可能已被移动，请重新扫描书库")
		return "", false
	}
	return src, true
}

// 有就用缓存的文件（JSON），没有就做出来存上
func pdfCachedJSON(w http.ResponseWriter, r *http.Request, path string, build func() (any, error)) {
	w.Header().Set("Cache-Control", "private, max-age=86400")
	serve := func(b []byte) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		etag := fmt.Sprintf(`"%x-%x"`, len(b), crc(b))
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(304)
			return
		}
		out, done := gzipWriter(w, r, "application/json")
		out.Write(b)
		done()
	}
	if b, err := os.ReadFile(path); err == nil {
		touchCache(filepath.Dir(path))
		serve(b)
		return
	}
	unlock := pdfWork.Lock(path)
	defer unlock()
	if b, err := os.ReadFile(path); err == nil {
		serve(b)
		return
	}
	v, err := build()
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	b, _ := json.Marshal(v)
	os.MkdirAll(filepath.Dir(path), 0o755)
	if os.WriteFile(path+".tmp", b, 0o644) == nil && os.Rename(path+".tmp", path) == nil {
		touchCache(filepath.Dir(path))
		noteCacheWrite()
	}
	serve(b)
}

func crc(b []byte) uint32 {
	var h uint32 = 2166136261
	for _, c := range b {
		h = (h ^ uint32(c)) * 16777619
	}
	return h
}

func handlePDFInfo(w http.ResponseWriter, r *http.Request) {
	src, ok := pdfFileFor(w, r)
	if !ok {
		return
	}
	pdfCachedJSON(w, r, filepath.Join(pdfCacheDir(src), "pdfinfo.json"), func() (any, error) { return pdfInfo(r.Context(), src) })
}

// 这本 PDF 有几页（用缓存的 pdfinfo.json，没有就读一遍存上）。页码超出范围时 mutool 会画最后一页，要先挡住
func pdfPageCount(r *http.Request, src string) int {
	path := filepath.Join(pdfCacheDir(src), "pdfinfo.json")
	var info pdfInfoResp
	if b, err := os.ReadFile(path); err == nil && json.Unmarshal(b, &info) == nil && info.Pages > 0 {
		return info.Pages
	}
	unlock := pdfWork.Lock(path)
	defer unlock()
	if b, err := os.ReadFile(path); err == nil && json.Unmarshal(b, &info) == nil && info.Pages > 0 {
		return info.Pages
	}
	got, err := pdfInfo(r.Context(), src)
	if err != nil {
		return 0
	}
	b, _ := json.Marshal(got)
	os.MkdirAll(filepath.Dir(path), 0o755)
	if os.WriteFile(path+".tmp", b, 0o644) == nil && os.Rename(path+".tmp", path) == nil {
		noteCacheWrite()
	}
	return got.Pages
}

// 页码：从 1 开始，不超过页数；不对时已经回了 404
func pdfPageArg(w http.ResponseWriter, r *http.Request, src string) (int, bool) {
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || n < 1 || n > pdfPageCount(r, src) {
		errJSON(w, 404, "没有这一页")
		return 0, false
	}
	return n, true
}

func handlePDFText(w http.ResponseWriter, r *http.Request) {
	src, ok := pdfFileFor(w, r)
	if !ok {
		return
	}
	n, ok := pdfPageArg(w, r, src)
	if !ok {
		return
	}
	pdfCachedJSON(w, r, filepath.Join(pdfCacheDir(src), fmt.Sprintf("pdftext_%d.json", n)), func() (any, error) {
		lines, err := pdfPageText(r.Context(), src, n)
		if err != nil {
			return nil, err
		}
		return pdfTextResp{Page: n, Lines: lines}, nil
	})
}

func handlePDFPage(w http.ResponseWriter, r *http.Request) {
	src, ok := pdfFileFor(w, r)
	if !ok {
		return
	}
	n, ok := pdfPageArg(w, r, src)
	if !ok {
		return
	}
	width, _ := strconv.Atoi(r.URL.Query().Get("w"))
	if width < 200 || width > 3000 {
		width = 1200
	}
	width = (width + 7) / 8 * 8
	gray := r.URL.Query().Get("g") == "1"
	mode, ext, ctype := "c", ".jpg", "image/jpeg"
	if gray {
		mode, ext, ctype = "g", ".png", "image/png"
	}
	path := filepath.Join(pdfCacheDir(src), fmt.Sprintf("pdf_%d_%d_%s%s", n, width, mode, ext))
	w.Header().Set("Cache-Control", "private, max-age=604800")
	serve := func() {
		touchCache(filepath.Dir(path))
		w.Header().Set("Content-Type", ctype)
		http.ServeFile(w, r, path)
	}
	if _, err := os.Stat(path); err == nil {
		serve()
		return
	}
	unlock := pdfWork.Lock(path)
	defer unlock()
	if _, err := os.Stat(path); err == nil {
		serve()
		return
	}
	data, err := pdfDrawPage(r.Context(), src, n, width, gray)
	if err != nil {
		if errors.Is(err, errNoSuchPage) {
			errJSON(w, 404, "没有这一页")
		} else {
			errJSON(w, 500, err.Error())
		}
		return
	}
	os.MkdirAll(filepath.Dir(path), 0o755)
	if os.WriteFile(path+".tmp", data, 0o644) == nil && os.Rename(path+".tmp", path) == nil {
		noteCacheWrite()
		serve()
		return
	}
	w.Header().Set("Content-Type", ctype)
	w.Write(data)
}

var errNoSuchPage = errors.New("没有这一页")

// 画一页：墨水屏要 16 级灰度的 PNG（字边清楚，文件也小）；彩色的旧手机要 JPEG
func pdfDrawPage(ctx context.Context, src string, n, width int, gray bool) ([]byte, error) {
	select {
	case pdfDrawSlots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-pdfDrawSlots }()
	dir, err := os.MkdirTemp("", "shuku-pdf-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	out := filepath.Join(dir, "p.png")
	cctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	cs := "rgb"
	if gray {
		cs = "gray"
	}
	var msg []byte
	if bin, err := execLookPath("mutool"); err == nil {
		msg, err = exec.CommandContext(cctx, bin, "draw", "-q", "-w", strconv.Itoa(width), "-c", cs, "-o", out, src, strconv.Itoa(n)).CombinedOutput()
	} else if bin, err := execLookPath("pdftoppm"); err == nil {
		args := []string{"-png", "-singlefile", "-f", strconv.Itoa(n), "-l", strconv.Itoa(n), "-scale-to-x", strconv.Itoa(width), "-scale-to-y", "-1"}
		if gray {
			args = append(args, "-gray")
		}
		msg, err = exec.CommandContext(cctx, bin, append(args, src, filepath.Join(dir, "p"))...).CombinedOutput()
	}
	b, err := os.ReadFile(out)
	if err != nil || len(b) == 0 {
		m := strings.TrimSpace(string(msg))
		if strings.Contains(m, "page") && (strings.Contains(m, "range") || strings.Contains(m, "out of") || strings.Contains(m, "Wrong page")) {
			return nil, errNoSuchPage
		}
		if m == "" {
			return nil, errNoSuchPage // 页码超出范围时 mutool 什么都不画
		}
		return nil, fmt.Errorf("画不出这一页：%s", truncate(m, 200))
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("画出来的图打不开：%v", err)
	}
	var buf bytes.Buffer
	if gray {
		// 墨水屏只有 16 级灰：压成 16 色的调色板 PNG，比 256 级灰度的小一半多
		bd := img.Bounds()
		pal := make(color.Palette, 16)
		for i := range pal {
			v := uint8(i * 17)
			pal[i] = color.Gray{v}
		}
		pm := image.NewPaletted(bd, pal)
		gi, isGray := img.(*image.Gray)
		for y := bd.Min.Y; y < bd.Max.Y; y++ {
			row := pm.Pix[(y-bd.Min.Y)*pm.Stride:]
			for x := bd.Min.X; x < bd.Max.X; x++ {
				var v uint8
				if isGray {
					v = gi.Pix[(y-bd.Min.Y)*gi.Stride+(x-bd.Min.X)]
				} else {
					v = color.GrayModel.Convert(img.At(x, y)).(color.Gray).Y
				}
				row[x-bd.Min.X] = uint8((int(v) + 8) / 17)
			}
		}
		enc := png.Encoder{CompressionLevel: png.BestCompression}
		if err := enc.Encode(&buf, pm); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	}
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ---------- 页数、每页大小、目录 ----------

// mutool run 的脚本（mutool 自带的 JavaScript）：一次读出页数、每页大小（已经算上裁切和旋转）和目录
const pdfInfoScript = `var doc = new Document(scriptArgs[0]);
var n = doc.countPages();
var sizes = [];
for (var i = 0; i < n; i++) {
  var b = null;
  try { b = doc.loadPage(i).bound(); } catch (e) {}
  sizes.push(b ? [Math.round((b[2] - b[0]) * 100) / 100, Math.round((b[3] - b[1]) * 100) / 100] : [0, 0]);
}
var out = [];
function pageOf(uri) {
  var m = /^#(?:page=)?(\d+)/.exec(uri || '');
  if (m) return +m[1];
  try {
    var d = doc.resolveLink(uri);
    if (typeof d === 'number') return d + 1;
    if (d && typeof d.page === 'number' && d.page >= 0) return d.page + 1;
  } catch (e) {}
  return 0;
}
function walk(items, level) {
  for (var i = 0; i < items.length && out.length < 5000; i++) {
    var it = items[i];
    out.push({ title: it.title || '', page: pageOf(it.uri), level: level });
    if (it.down) walk(it.down, level + 1);
  }
}
try { walk(doc.loadOutline() || [], 0); } catch (e) {}
print(JSON.stringify({ pages: n, sizes: sizes, outline: out }));
`

func pdfInfo(ctx context.Context, src string) (*pdfInfoResp, error) {
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if bin, err := execLookPath("mutool"); err == nil {
		dir, err := os.MkdirTemp("", "shuku-pdf-")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(dir)
		script := filepath.Join(dir, "info.js")
		os.WriteFile(script, []byte(pdfInfoScript), 0o644)
		out, err := exec.CommandContext(cctx, bin, "run", script, src).Output()
		var info pdfInfoResp
		if err == nil && json.Unmarshal(bytes.TrimSpace(out), &info) == nil && info.Pages > 0 {
			return cleanPDFInfo(&info), nil
		}
		// 老的 mutool 没有 run：页数、大小从 pages 里读，目录从 show outline 里读
		if info, err := pdfInfoFromPages(cctx, bin, src); err == nil {
			return cleanPDFInfo(info), nil
		}
	}
	if bin, err := execLookPath("pdfinfo"); err == nil {
		out, err := exec.CommandContext(cctx, bin, "-f", "1", "-l", "100000", src).Output()
		if err != nil && len(out) == 0 {
			return nil, errors.New("读不出这个 PDF")
		}
		return cleanPDFInfo(parsePdfinfo(string(out))), nil
	}
	return nil, errors.New("书库没有安装 mutool")
}

func cleanPDFInfo(info *pdfInfoResp) *pdfInfoResp {
	if len(info.Sizes) > info.Pages {
		info.Sizes = info.Sizes[:info.Pages]
	}
	out := info.Outline[:0]
	for _, o := range info.Outline {
		o.Title = strings.TrimSpace(o.Title)
		if o.Page > info.Pages {
			o.Page = 0
		}
		if o.Title != "" {
			out = append(out, o)
		}
	}
	info.Outline = out
	if info.Outline == nil {
		info.Outline = []pdfOutlineItem{}
	}
	return info
}

var (
	reMuPage    = regexp.MustCompile(`<page pagenum="(\d+)">`)
	reMuBox     = regexp.MustCompile(`<(MediaBox|CropBox) l="([-\d.e]+)" b="([-\d.e]+)" r="([-\d.e]+)" t="([-\d.e]+)"`)
	reMuRotate  = regexp.MustCompile(`<Rotate v="([-\d.]+)"`)
	reOutline   = regexp.MustCompile(`^([|+-])(\t+)"((?:[^"\\]|\\.)*)"\t(.*)$`)
	rePageInURI = regexp.MustCompile(`^#(?:page=)?(\d+)`)
	rePdfinfoSz = regexp.MustCompile(`(?m)^Page\s+(\d+) size:\s+([\d.]+) x ([\d.]+)`)
	rePdfinfoRt = regexp.MustCompile(`(?m)^Page\s+(\d+) rot:\s+(\d+)`)
	rePdfinfoN  = regexp.MustCompile(`(?m)^Pages:\s+(\d+)`)
)

func pdfInfoFromPages(ctx context.Context, bin, src string) (*pdfInfoResp, error) {
	out, err := exec.CommandContext(ctx, bin, "pages", src).Output()
	if err != nil && len(out) == 0 {
		return nil, errors.New("读不出这个 PDF")
	}
	info := &pdfInfoResp{}
	var last [2]float64
	for _, chunk := range strings.Split(string(out), "</page>") {
		m := reMuPage.FindStringSubmatch(chunk)
		if m == nil {
			continue
		}
		size := last // 大小写在上一级（继承的）时读不到，用上一页的
		boxes := map[string][4]float64{}
		for _, b := range reMuBox.FindAllStringSubmatch(chunk, -1) {
			var v [4]float64
			for i := 0; i < 4; i++ {
				v[i], _ = strconv.ParseFloat(b[i+2], 64)
			}
			boxes[b[1]] = v
		}
		box, ok := boxes["CropBox"]
		if !ok {
			box, ok = boxes["MediaBox"]
		}
		if ok {
			size = [2]float64{absf(box[2] - box[0]), absf(box[3] - box[1])}
		}
		if rm := reMuRotate.FindStringSubmatch(chunk); rm != nil {
			if r, _ := strconv.Atoi(strings.Split(rm[1], ".")[0]); (r/90)%2 != 0 {
				size = [2]float64{size[1], size[0]}
			}
		}
		last = size
		info.Sizes = append(info.Sizes, size)
	}
	info.Pages = len(info.Sizes)
	if info.Pages == 0 {
		return nil, errors.New("读不出页数")
	}
	if o, err := exec.CommandContext(ctx, bin, "show", src, "outline").Output(); err == nil {
		info.Outline = parseMutoolOutline(string(o))
	}
	return info, nil
}

// mutool show outline：每行“|”（没有下级）或“+ / -”（有下级），然后按层级几个 Tab、带引号的标题、Tab、链接
func parseMutoolOutline(s string) []pdfOutlineItem {
	var out []pdfOutlineItem
	for _, line := range strings.Split(s, "\n") {
		m := reOutline.FindStringSubmatch(strings.TrimRight(line, "\r"))
		if m == nil {
			continue
		}
		title, err := strconv.Unquote(`"` + m[3] + `"`)
		if err != nil {
			title = m[3]
		}
		page := 0
		if pm := rePageInURI.FindStringSubmatch(m[4]); pm != nil {
			page, _ = strconv.Atoi(pm[1])
		}
		out = append(out, pdfOutlineItem{Title: title, Page: page, Level: len(m[2]) - 1})
	}
	return out
}

func parsePdfinfo(s string) *pdfInfoResp {
	info := &pdfInfoResp{Outline: []pdfOutlineItem{}}
	if m := rePdfinfoN.FindStringSubmatch(s); m != nil {
		info.Pages, _ = strconv.Atoi(m[1])
	}
	info.Sizes = make([][2]float64, info.Pages)
	rot := map[int]int{}
	for _, m := range rePdfinfoRt.FindAllStringSubmatch(s, -1) {
		p, _ := strconv.Atoi(m[1])
		rot[p], _ = strconv.Atoi(m[2])
	}
	for _, m := range rePdfinfoSz.FindAllStringSubmatch(s, -1) {
		p, _ := strconv.Atoi(m[1])
		if p < 1 || p > info.Pages {
			continue
		}
		w, _ := strconv.ParseFloat(m[2], 64)
		h, _ := strconv.ParseFloat(m[3], 64)
		if (rot[p]/90)%2 != 0 {
			w, h = h, w
		}
		info.Sizes[p-1] = [2]float64{w, h}
	}
	return info
}

func absf(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

// ---------- 一页的文字 ----------

func pdfPageText(ctx context.Context, src string, n int) ([]pdfTextLine, error) {
	cctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if bin, err := execLookPath("mutool"); err == nil {
		out, err := exec.CommandContext(cctx, bin, "draw", "-q", "-F", "stext", "-o", "-", src, strconv.Itoa(n)).Output()
		if err != nil && len(out) == 0 {
			return nil, errors.New("读不出这一页的文字")
		}
		return parseStext(out), nil
	}
	if bin, err := execLookPath("pdftotext"); err == nil {
		out, err := exec.CommandContext(cctx, bin, "-q", "-enc", "UTF-8", "-bbox-layout", "-f", strconv.Itoa(n), "-l", strconv.Itoa(n), src, "-").Output()
		if err != nil && len(out) == 0 {
			return nil, errors.New("读不出这一页的文字")
		}
		return parseBboxLayout(out), nil
	}
	return nil, errors.New("书库没有安装 mutool")
}

func parseBox(s string) (x0, y0, x1, y1 float64, ok bool) {
	f := strings.Fields(s)
	if len(f) != 4 {
		return
	}
	var v [4]float64
	for i := range f {
		var err error
		if v[i], err = strconv.ParseFloat(f[i], 64); err != nil {
			return
		}
	}
	return v[0], v[1], v[2], v[3], true
}

func attr(se xml.StartElement, name string) string {
	for _, a := range se.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

// mutool 的 stext：<page width height> <block> <line bbox> <font> <char c=""/>…
func parseStext(b []byte) []pdfTextLine {
	dec := xml.NewDecoder(bytes.NewReader(b))
	dec.Strict = false
	var pw, ph float64
	lines := []pdfTextLine{}
	var cur *pdfTextLine
	var sb strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "page":
				pw, _ = strconv.ParseFloat(attr(t, "width"), 64)
				ph, _ = strconv.ParseFloat(attr(t, "height"), 64)
			case "line":
				x0, y0, x1, y1, ok := parseBox(attr(t, "bbox"))
				if ok && pw > 0 && ph > 0 {
					cur = &pdfTextLine{X: x0 / pw, Y: y0 / ph, W: (x1 - x0) / pw, H: (y1 - y0) / ph}
				} else {
					cur = &pdfTextLine{}
				}
				sb.Reset()
			case "char":
				if cur != nil {
					sb.WriteString(attr(t, "c"))
				}
			}
		case xml.EndElement:
			if t.Name.Local == "line" && cur != nil {
				cur.Text = sb.String()
				if strings.TrimSpace(cur.Text) != "" && cur.W > 0 && cur.H > 0 {
					lines = append(lines, *cur)
				}
				cur = nil
			}
		}
	}
	return roundLines(lines)
}

// pdftotext -bbox-layout：<page width height> <line xMin yMin xMax yMax> <word …>字</word>
func parseBboxLayout(b []byte) []pdfTextLine {
	dec := xml.NewDecoder(bytes.NewReader(b))
	dec.Strict = false
	dec.AutoClose = xml.HTMLAutoClose
	dec.Entity = xml.HTMLEntity
	var pw, ph float64
	lines := []pdfTextLine{}
	var cur *pdfTextLine
	var words []string
	inWord := false
	var word strings.Builder
	num := func(se xml.StartElement, n string) float64 { v, _ := strconv.ParseFloat(attr(se, n), 64); return v }
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "page":
				pw, ph = num(t, "width"), num(t, "height")
			case "line":
				if pw > 0 && ph > 0 {
					x0, y0, x1, y1 := num(t, "xMin"), num(t, "yMin"), num(t, "xMax"), num(t, "yMax")
					cur = &pdfTextLine{X: x0 / pw, Y: y0 / ph, W: (x1 - x0) / pw, H: (y1 - y0) / ph}
				}
				words = nil
			case "word":
				inWord = true
				word.Reset()
			}
		case xml.CharData:
			if inWord {
				word.Write(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "word":
				inWord = false
				words = append(words, word.String())
			case "line":
				if cur != nil {
					cur.Text = joinWords(words)
					if strings.TrimSpace(cur.Text) != "" && cur.W > 0 && cur.H > 0 {
						lines = append(lines, *cur)
					}
				}
				cur = nil
			}
		}
	}
	return roundLines(lines)
}

// 中文词之间不加空格，英文加
func joinWords(ws []string) string {
	var sb strings.Builder
	for i, w := range ws {
		if i > 0 {
			prev := []rune(ws[i-1])
			next := []rune(w)
			if len(prev) > 0 && len(next) > 0 && !(isCJKRune(prev[len(prev)-1]) && isCJKRune(next[0])) {
				sb.WriteByte(' ')
			}
		}
		sb.WriteString(w)
	}
	return sb.String()
}

func roundLines(lines []pdfTextLine) []pdfTextLine {
	r := func(f float64) float64 { return float64(int(f*100000+0.5)) / 100000 }
	for i := range lines {
		l := &lines[i]
		l.X, l.Y, l.W, l.H = r(l.X), r(l.Y), r(l.W), r(l.H)
	}
	return lines
}
