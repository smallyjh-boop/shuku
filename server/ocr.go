package main

// 扫描版 PDF、DJVU（每页都是图片，没有文字层）识别出文字：阅读器翻到没有文字的一页时来要，
// 书库用 mutool 把这一页画成图片，再用 tesseract 识别（简体、繁体、英文、日文的快速模型），
// 返回每一行的文字和位置（按页面宽高的比例），阅读器据此铺一层透明文字：能选字、复制、查字典、划线、翻译。
// 识别结果存在缓存里（cache/ocr，算在缓存上限里），同一页不会再识别一次。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/png"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

var tesseractBin = "tesseract"

type ocrWord struct {
	T string  `json:"t"`
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

type ocrLine struct {
	Text  string    `json:"text"`
	X     float64   `json:"x"`
	Y     float64   `json:"y"`
	W     float64   `json:"w"`
	H     float64   `json:"h"`
	Words []ocrWord `json:"words"`
}

type ocrPage struct {
	Page  int       `json:"page"`
	Lang  string    `json:"lang"`
	W     int       `json:"w"` // 识别用的图片的像素大小
	H     int       `json:"h"`
	Lines []ocrLine `json:"lines"`
}

func ocrAvailable() bool {
	_, err := execLookPath(tesseractBin)
	return err == nil
}

var ocrLangCache struct {
	sync.Mutex
	have map[string]bool
	at   time.Time
}

// 装了哪些语言的识别模型
func ocrInstalledLangs() map[string]bool {
	ocrLangCache.Lock()
	defer ocrLangCache.Unlock()
	if ocrLangCache.have != nil && time.Since(ocrLangCache.at) < 10*time.Minute {
		return ocrLangCache.have
	}
	have := map[string]bool{}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, tesseractBin, "--list-langs").CombinedOutput()
	if err == nil {
		for _, l := range strings.Split(string(out), "\n") {
			l = strings.TrimSpace(l)
			if l != "" && !strings.Contains(l, " ") && !strings.HasSuffix(l, ":") && l != "osd" {
				have[l] = true
			}
		}
	}
	ocrLangCache.have, ocrLangCache.at = have, time.Now()
	return have
}

// 书的语言（ISO 代码，两个或三个字母，取前缀）→ tesseract 的识别模型
var ocrModels = []struct{ prefix, model string }{
	{"zh-hant", "chi_tra"}, {"zh-tw", "chi_tra"}, {"zh-hk", "chi_tra"}, {"cht", "chi_tra"},
	{"zh", "chi_sim"}, {"chi", "chi_sim"}, {"ja", "jpn"}, {"jpn", "jpn"}, {"ko", "kor"}, {"th", "tha"}, {"vi", "vie"},
	{"en", "eng"}, {"fr", "fra"}, {"de", "deu"}, {"ger", "deu"}, {"es", "spa"}, {"spa", "spa"}, {"it", "ita"},
	{"pt", "por"}, {"por", "por"}, {"ru", "rus"}, {"id", "ind"}, {"ms", "msa"}, {"my", "mya"}, {"bur", "mya"},
	{"km", "khm"}, {"lo", "lao"}, {"ar", "ara"}, {"hi", "hin"},
}

func ocrModelOf(lang string) string {
	l := strings.ToLower(strings.TrimSpace(lang))
	if l == "" {
		return ""
	}
	for _, m := range ocrModels {
		if strings.HasPrefix(l, m.prefix) {
			return m.model
		}
	}
	return ""
}

// 用哪些语言识别：指定了就用指定的（去掉没装的）；否则按书的语言，书的语言不知道（或没装这个模型）时
// 看书名是什么文字（泰文书名用泰文模型，拉丁字母用英文）；都看不出来用简体中文。
// 不是英文的都加上英文（书里常夹英文）
func ocrLangFor(bookLang, title, want string) string {
	have := ocrInstalledLangs()
	pick := func(langs ...string) string {
		var ok []string
		seen := map[string]bool{}
		for _, l := range langs {
			if have[l] && !seen[l] {
				seen[l] = true
				ok = append(ok, l)
			}
		}
		return strings.Join(ok, "+")
	}
	if want != "" {
		langs := strings.Split(want, "+")
		if len(langs) > 4 {
			langs = langs[:4] // 同时最多 4 种：每多一种识别慢一截，别把 NAS 拖住
		}
		if s := pick(langs...); s != "" {
			return s
		}
	}
	model := ocrModelOf(bookLang)
	if model == "" || !have[model] {
		model = ""
		switch sl := scriptLang(title); sl {
		case "latin":
			model = "eng"
		case "":
		default:
			model = ocrModelOf(sl)
		}
	}
	if model == "" || !have[model] {
		model = "chi_sim"
	}
	s := pick(model, "eng")
	if s == "" {
		s = "eng"
	}
	return s
}

var ocrSem = make(chan struct{}, 2) // 同时最多识别两页，别把 NAS 的 CPU 占满
var ocrLocks keyedMutex

// GET /api/files/{fid}/ocr/{page}?lang=chi_sim+eng   page 从 1 开始
func handleOCR(w http.ResponseWriter, r *http.Request) {
	f, ok := visibleFile(w, r)
	if !ok {
		return
	}
	page, _ := strconv.Atoi(r.PathValue("page"))
	if page < 1 || page > 100000 {
		errJSON(w, 404, "没有这一页")
		return
	}
	var src string
	switch f.Format {
	case "pdf":
		src = f.Path
	case "djvu":
		p, err := ensurePDF(f.Path)
		if err != nil {
			errJSON(w, 500, err.Error())
			return
		}
		src = p
	default:
		errJSON(w, 400, "只有 PDF、DJVU 能识别文字")
		return
	}
	if n := pdfPageCount(r, src); n > 0 && page > n {
		errJSON(w, 404, "没有这一页")
		return
	}
	var bookLang, title string
	db.QueryRow(`SELECT language,title FROM books WHERE id=?`, f.BookID).Scan(&bookLang, &title)
	lang := ocrLangFor(bookLang, title, r.URL.Query().Get("lang"))
	cp := filepath.Join(cacheRoot(), "ocr", cacheKey(src), fmt.Sprintf("%d-%s.json", page, strings.ReplaceAll(lang, "+", "_")))
	if b, err := os.ReadFile(cp); err == nil {
		touchCache(cp)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "private, max-age=604800")
		w.Write(b)
		return
	}
	if !ocrAvailable() {
		errJSON(w, 501, "服务器没有安装文字识别（tesseract），请更新书库")
		return
	}
	unlock := ocrLocks.Lock(cp)
	defer unlock()
	if b, err := os.ReadFile(cp); err == nil { // 等锁的时候别人已经识别好了
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Write(b)
		return
	}
	select {
	case ocrSem <- struct{}{}:
		defer func() { <-ocrSem }()
	case <-r.Context().Done():
		return
	case <-time.After(2 * time.Minute):
		errJSON(w, 503, "识别的人太多，请稍后再试")
		return
	}
	res, err := ocrPDFPage(r.Context(), src, page, lang)
	if err != nil {
		errJSON(w, 500, "识别失败："+err.Error())
		return
	}
	b, _ := json.Marshal(res)
	os.MkdirAll(filepath.Dir(cp), 0o755)
	if os.WriteFile(cp+".part", b, 0o644) == nil {
		os.Rename(cp+".part", cp)
		noteCacheWrite()
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "private, max-age=604800")
	w.Write(b)
}

// 把一页画成灰度图（200 dpi），交给 tesseract
func ocrPDFPage(ctx context.Context, src string, page int, lang string) (*ocrPage, error) {
	dir, err := os.MkdirTemp("", "shuku-ocr-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	img := filepath.Join(dir, "p.png")
	rctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if bin, err := execLookPath("mutool"); err == nil {
		out, err := exec.CommandContext(rctx, bin, "draw", "-q", "-r", "200", "-c", "gray", "-o", img, src, strconv.Itoa(page)).CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("画不出这一页：%s", truncate(strings.TrimSpace(string(out)), 200))
		}
	} else if bin, err := execLookPath("pdftoppm"); err == nil {
		base := filepath.Join(dir, "p")
		out, err := exec.CommandContext(rctx, bin, "-r", "200", "-gray", "-png", "-singlefile", "-f", strconv.Itoa(page), "-l", strconv.Itoa(page), src, base).CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("画不出这一页：%s", truncate(strings.TrimSpace(string(out)), 200))
		}
	} else {
		return nil, errors.New("服务器没有 mutool")
	}
	fh, err := os.Open(img)
	if err != nil {
		return nil, errors.New("没有这一页")
	}
	cfg, _, err := image.DecodeConfig(fh)
	fh.Close()
	if err != nil || cfg.Width == 0 || cfg.Height == 0 {
		return nil, errors.New("这一页的图片打不开")
	}
	tctx, cancel2 := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel2()
	cmd := exec.CommandContext(tctx, tesseractBin, img, "stdout", "-l", lang, "--psm", "3", "tsv")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.Env = append(os.Environ(), "OMP_THREAD_LIMIT=1") // 每页一个线程，两页并行刚好
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("tesseract：%s", truncate(strings.TrimSpace(stderr.String()), 200))
	}
	return &ocrPage{Page: page, Lang: lang, W: cfg.Width, H: cfg.Height, Lines: parseTesseractTSV(stdout.String(), cfg.Width, cfg.Height)}, nil
}

// tesseract 的 TSV：level page block par line word left top width height conf text。
// level 5 是词（中文多半一个字一个词），按 block/par/line 合成行；位置换成页面宽高的比例
func parseTesseractTSV(tsv string, w, h int) []ocrLine {
	type key struct{ b, p, l int }
	type acc struct {
		k     key
		words []ocrWord
		order int
	}
	lines := map[key]*acc{}
	n := 0
	for i, row := range strings.Split(tsv, "\n") {
		if i == 0 || strings.TrimSpace(row) == "" {
			continue
		}
		c := strings.Split(strings.TrimRight(row, "\r"), "\t")
		if len(c) < 12 || c[0] != "5" {
			continue
		}
		text := strings.TrimSpace(c[11])
		conf, _ := strconv.ParseFloat(c[10], 64)
		if text == "" || conf < 0 {
			continue
		}
		atoi := func(s string) int { v, _ := strconv.Atoi(s); return v }
		k := key{atoi(c[2]), atoi(c[3]), atoi(c[4])}
		a := lines[k]
		if a == nil {
			a = &acc{k: k, order: n}
			n++
			lines[k] = a
		}
		a.words = append(a.words, ocrWord{T: text,
			X: float64(atoi(c[6])) / float64(w), Y: float64(atoi(c[7])) / float64(h),
			W: float64(atoi(c[8])) / float64(w), H: float64(atoi(c[9])) / float64(h)})
	}
	list := make([]*acc, 0, len(lines))
	for _, a := range lines {
		list = append(list, a)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].order < list[j].order })
	out := make([]ocrLine, 0, len(list))
	for _, a := range list {
		x0, y0, x1, y1 := 1.0, 1.0, 0.0, 0.0
		var sb strings.Builder
		var prev rune
		for i, wd := range a.words {
			x0, y0 = min(x0, wd.X), min(y0, wd.Y)
			x1, y1 = max(x1, wd.X+wd.W), max(y1, wd.Y+wd.H)
			first := []rune(wd.T)[0]
			// 中文、日文之间不加空格，其他的词之间加
			if i > 0 && !(isCJKRune(prev) && isCJKRune(first)) {
				sb.WriteByte(' ')
			}
			sb.WriteString(wd.T)
			r := []rune(wd.T)
			prev = r[len(r)-1]
		}
		out = append(out, ocrLine{Text: sb.String(), X: x0, Y: y0, W: x1 - x0, H: y1 - y0, Words: a.words})
	}
	return out
}

func isCJKRune(r rune) bool {
	return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) ||
		unicode.Is(unicode.Hangul, r) || (r >= 0x3000 && r <= 0x303F) || (r >= 0xFF00 && r <= 0xFFEF)
}
