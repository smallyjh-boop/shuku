package main

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

// 最常用的汉字（简繁共有 + 常见繁体），用来判断 GBK 和 Big5 哪个解码更像中文
const commonHan = "的一是不了在人有我他这个们中来上大为和国地到以说时要就出会可也你对生能而子那得于着下自之年过发后作里用道行所然家种事成方多经么去法学如都同现当没动面起看定天分还进好小部其些主样理心她本前开但因只从想实日军者意无力它与长把机十民第公此已工使情明性知全三又关点正业外将两高间由问很最重并物手应战向头文体政美相见被利什二等产或新己制身果加西月话合回特代内信表化老给世位次度门任常先海通教儿原东声提立及比员解水名真论处走义各入几口认条平系气题活更别打女变四神总何电数安少报才结反受目太量再感建务做接必场件计管期市直资命山金指克许统区保至队形社便空决治展马科司五基眼书非则听白却界达光放强即像难且权思王象完设式色路记南品住告类求据程北边死张该交规万取拉格望觉术领共确传师观清今切院让识候带导争运笑飞风步改收根干造言联持组每济车亲极林服快办议往元英士证近失转夫令准布始怎呢存未远叫台单影具罗字爱击流备兵连调深商算质团集百需价花党华城石级整府离况请技际约示复病息究线似官火断精满支视消越器容照须九增研写称企八功吗包片史委乎查轻易早曾除农找装广显吧阿李标谈吃图念六引历首医局突专费号們個這來說時會對國過還進後裡為與當經發動麼樣種現開關點長問們實見頭體學話間從車門親東"

var commonSet = func() map[rune]bool {
	m := map[rune]bool{}
	for _, r := range commonHan {
		m[r] = true
	}
	return m
}()

func hanScore(s string) int {
	n := 0
	for _, r := range s {
		if commonSet[r] {
			n += 2
		} else if r == utf8.RuneError {
			n -= 4
		}
	}
	return n
}

func decodeGBK(b []byte) (string, bool) {
	out, _, err := transform.Bytes(simplifiedchinese.GB18030.NewDecoder(), b)
	if err != nil {
		return "", false
	}
	return string(out), true
}

// 自动识别 TXT 编码，返回 UTF-8 文本
func decodeText(b []byte) (string, string) {
	switch {
	case bytes.HasPrefix(b, []byte{0xEF, 0xBB, 0xBF}):
		return string(b[3:]), "UTF-8"
	case bytes.HasPrefix(b, []byte{0xFF, 0xFE}):
		out, _, _ := transform.Bytes(unicode.UTF16(unicode.LittleEndian, unicode.UseBOM).NewDecoder(), b)
		return string(out), "UTF-16LE"
	case bytes.HasPrefix(b, []byte{0xFE, 0xFF}):
		out, _, _ := transform.Bytes(unicode.UTF16(unicode.BigEndian, unicode.UseBOM).NewDecoder(), b)
		return string(out), "UTF-16BE"
	}
	sample := b
	if len(sample) > 256<<10 {
		sample = sample[:256<<10]
		// 截断处可能切断一个多字节字符
		for i := 0; i < 4 && !utf8.Valid(sample) && len(sample) > 0; i++ {
			sample = sample[:len(sample)-1]
		}
	}
	if utf8.Valid(sample) {
		return strings.ToValidUTF8(string(b), ""), "UTF-8"
	}
	gb, _, err1 := transform.Bytes(simplifiedchinese.GB18030.NewDecoder(), sample)
	big, _, err2 := transform.Bytes(traditionalchinese.Big5.NewDecoder(), sample)
	sg, sb := hanScore(string(gb)), hanScore(string(big))
	if err2 == nil && (err1 != nil || sb > sg) {
		out, _, _ := transform.Bytes(traditionalchinese.Big5.NewDecoder(), b)
		return string(out), "Big5"
	}
	out, _, _ := transform.Bytes(simplifiedchinese.GB18030.NewDecoder(), b)
	return string(out), "GB18030"
}

// ---------- TXT 智能分章 ----------

type Chapter struct {
	Title string
	Lines []string
}

var chapterRe = []*regexp.Regexp{
	regexp.MustCompile(`^\s*(第\s*[0-9０-９零〇一二两三四五六七八九十百千万壹贰叁肆伍陆柒捌玖拾佰仟]+\s*[章节回卷集部篇话幕])(\s*.{0,40})$`),
	regexp.MustCompile(`^\s*(序章|序言|序幕|楔子|引子|前言|后记|後記|尾声|尾聲|终章|終章|番外|完本感言|写在前面)(\s*.{0,30})$`),
	regexp.MustCompile(`^\s*(?i:chapter|part|book)\s+([0-9]+|[ivxlcdm]+)\b.{0,50}$`),
	regexp.MustCompile(`^\s*[0-9]{1,4}[、.．]\s*\S.{0,30}$`),
}

func isChapterLine(line string) bool {
	if utf8.RuneCountInString(line) > 50 {
		return false
	}
	for i, re := range chapterRe {
		// 最后一条（"1. xxx"）太宽泛，只有前面都找不到章节时才用
		if i == len(chapterRe)-1 {
			continue
		}
		if re.MatchString(line) {
			return true
		}
	}
	return false
}

// TXT 分章规则的版本：改了规则就加一，旧规则转出来的 EPUB 缓存、全文索引会重新生成
const txtSplitVersion = 2

// 不常见的换行符（Unicode 的行、段分隔符，NEL，换页）也当作换行
var lineBreaks = strings.NewReplacer("\r\n", "\n", "\r", "\n", " ", "\n", " ", "\n", "\u0085", "\n", "\f", "\n", "\v", "\n")

// 一行超过这么多字（整本书没有换行、一章挤在一行之类）就拆开：
// 否则整本书只有一节，手机上光排版就要几十秒
const longLineRunes = 3000

// 段首缩进：两个以上全角空格、四个以上半角空格或制表符
var paraIndentRe = regexp.MustCompile(`　{2,}|[ \t]{4,}|\t+`)

func splitLongLine(line string) []string {
	if len(line) <= longLineRunes || utf8.RuneCountInString(line) <= longLineRunes {
		return []string{line}
	}
	var out []string
	for _, p := range paraIndentRe.Split(line, -1) {
		for _, q := range splitInlineChapters(p) {
			if q = strings.TrimSpace(strings.Trim(q, "　")); q != "" {
				out = append(out, chunkSentences(q, 400, 1000)...)
			}
		}
	}
	return out
}

const chapterNum = `第\s*[0-9０-９零〇一二两三四五六七八九十百千万]+\s*[章回节卷]`

// 句末（或空格）后面紧跟着的“第X章”：没有换行的书里章节标题就是这样夹在正文中间的
var inlineChapterRe = regexp.MustCompile(`[。！？!?…”’」』）)》】 　](` + chapterNum + `)`)

// 段首的“第X章 标题”后面跟着空格和正文：标题单独成行
var chapterHeadRe = regexp.MustCompile(`^` + chapterNum + `[ 　]*[^ 　]{1,30}[ 　]+`)

func splitInlineChapters(p string) []string {
	var parts []string
	start := 0
	for _, m := range inlineChapterRe.FindAllStringSubmatchIndex(p, -1) {
		parts = append(parts, p[start:m[2]])
		start = m[2]
	}
	parts = append(parts, p[start:])
	var out []string
	for _, s := range parts {
		if loc := chapterHeadRe.FindStringIndex(s); loc != nil && loc[1] < len(s) {
			out = append(out, s[:loc[1]], s[loc[1]:])
		} else {
			out = append(out, s)
		}
	}
	return out
}

func isSentenceEnd(r rune) bool { return strings.ContainsRune("。！？!?…", r) }
func isCloser(r rune) bool      { return strings.ContainsRune("”’」』）)》】\"'", r) }

// 没有分段的长文字按句子切成段落：凑够 target 字后在句末断开，一直没有句末标点就在 max 字处硬断
func chunkSentences(s string, target, max int) []string {
	rs := []rune(s)
	if len(rs) <= max {
		return []string{s}
	}
	var out []string
	start := 0
	push := func(end int) {
		if t := strings.TrimSpace(string(rs[start:end])); t != "" {
			out = append(out, t)
		}
		start = end
	}
	for i := 0; i < len(rs); i++ {
		if isSentenceEnd(rs[i]) {
			j := i + 1
			for j < len(rs) && (isSentenceEnd(rs[j]) || isCloser(rs[j])) {
				j++
			}
			i = j - 1
			if j-start >= target {
				push(j)
				continue
			}
		}
		if i+1-start >= max {
			push(i + 1)
		}
	}
	push(len(rs))
	return out
}

// 这本 TXT 按新规则分章会不会和旧规则不一样（有很长的行或不常见的换行符）
func txtSplitChanged(text string) bool {
	if strings.ContainsAny(text, "  \u0085\f\v") {
		return true
	}
	// 旧规则也认 \r\n 和 \r
	for _, l := range strings.Split(strings.ReplaceAll(text, "\r", "\n"), "\n") {
		if len(splitLongLine(l)) > 1 {
			return true
		}
	}
	return false
}

func splitChapters(text string) []Chapter {
	lines := strings.Split(lineBreaks.Replace(text), "\n")
	var chs []Chapter
	cur := Chapter{Title: "开始"}
	found := 0
	for _, whole := range lines {
		for _, raw := range splitLongLine(whole) {
			line := strings.TrimRight(raw, " \t　")
			trim := strings.TrimSpace(strings.Trim(line, "　"))
			if trim == "" {
				continue
			}
			if isChapterLine(trim) {
				found++
				if len(cur.Lines) > 0 || cur.Title != "开始" {
					chs = append(chs, cur)
				}
				cur = Chapter{Title: trim}
				continue
			}
			cur.Lines = append(cur.Lines, trim)
		}
	}
	chs = append(chs, cur)
	if len(chs) > 0 && chs[0].Title == "开始" && len(chs[0].Lines) == 0 {
		chs = chs[1:]
	}
	// 开头只有“书名 作者”之类几行信息的，不单独成章
	if found >= 2 && len(chs) > 1 && chs[0].Title == "开始" {
		n := 0
		for _, l := range chs[0].Lines {
			n += utf8.RuneCountInString(l)
		}
		if n < 120 && len(chs[0].Lines) <= 4 {
			chs = chs[1:]
		}
	}
	// 没找到章节，或者章节太大：按字数切
	if found < 2 {
		return splitBySize(chs)
	}
	var out []Chapter
	for _, c := range chs {
		if c.Title == "开始" {
			c.Title = "前言"
		}
		out = append(out, splitBySize([]Chapter{c})...)
	}
	return out
}

func splitBySize(chs []Chapter) []Chapter {
	const limit = 30000
	var out []Chapter
	for _, c := range chs {
		size := 0
		for _, l := range c.Lines {
			size += len(l)
		}
		if size <= limit*2 {
			out = append(out, c)
			continue
		}
		part := Chapter{Title: c.Title}
		n, idx := 0, 1
		for _, l := range c.Lines {
			part.Lines = append(part.Lines, l)
			n += len(l)
			if n >= limit {
				out = append(out, part)
				idx++
				part = Chapter{Title: fmt.Sprintf("%s（%d）", c.Title, idx)}
				if c.Title == "开始" {
					part.Title = fmt.Sprintf("第 %d 部分", idx)
				}
				n = 0
			}
		}
		if len(part.Lines) > 0 {
			out = append(out, part)
		}
		if out[len(out)-1].Title == "开始" {
			out[len(out)-1].Title = "第 1 部分"
		}
	}
	for i := range out {
		if out[i].Title == "开始" {
			out[i].Title = "第 1 部分"
		}
	}
	return out
}

// ---------- 生成 EPUB ----------

func buildEPUB(w io.Writer, title, author string, chs []Chapter) error {
	zw := zip.NewWriter(w)
	// mimetype 必须是第一个文件且不压缩
	mw, _ := zw.CreateHeader(&zip.FileHeader{Name: "mimetype", Method: zip.Store})
	mw.Write([]byte("application/epub+zip"))
	add := func(name, content string) {
		f, _ := zw.Create(name)
		f.Write([]byte(content))
	}
	add("META-INF/container.xml", `<?xml version="1.0" encoding="UTF-8"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`)
	esc := html.EscapeString
	var manifest, spine, nav strings.Builder
	for i, c := range chs {
		id := fmt.Sprintf("c%05d", i+1)
		var body strings.Builder
		body.WriteString(`<?xml version="1.0" encoding="UTF-8"?><!DOCTYPE html><html xmlns="http://www.w3.org/1999/xhtml" xml:lang="zh"><head><meta charset="utf-8"/><title>`)
		body.WriteString(esc(c.Title))
		body.WriteString(`</title><link rel="stylesheet" href="style.css"/></head><body><h2>`)
		body.WriteString(esc(c.Title))
		body.WriteString("</h2>\n")
		for _, l := range c.Lines {
			body.WriteString("<p>")
			body.WriteString(esc(l))
			body.WriteString("</p>\n")
		}
		body.WriteString("</body></html>")
		add("OEBPS/"+id+".xhtml", body.String())
		fmt.Fprintf(&manifest, `<item id="%s" href="%s.xhtml" media-type="application/xhtml+xml"/>`, id, id)
		fmt.Fprintf(&spine, `<itemref idref="%s"/>`, id)
		fmt.Fprintf(&nav, `<li><a href="%s.xhtml">%s</a></li>`, id, esc(c.Title))
	}
	add("OEBPS/style.css", `h2{text-align:center;margin:1.5em 0 1.2em;font-size:1.3em;font-weight:600}p{text-indent:2em;margin:0 0 .6em}`)
	add("OEBPS/nav.xhtml", `<?xml version="1.0" encoding="UTF-8"?><!DOCTYPE html><html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops"><head><meta charset="utf-8"/><title>目录</title></head><body><nav epub:type="toc"><h1>目录</h1><ol>`+nav.String()+`</ol></nav></body></html>`)
	add("OEBPS/content.opf", `<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="uid"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:identifier id="uid">shuku-`+randToken(6)+`</dc:identifier><dc:title>`+esc(title)+`</dc:title><dc:creator>`+esc(author)+`</dc:creator><dc:language>zh</dc:language><meta property="dcterms:modified">2026-01-01T00:00:00Z</meta></metadata><manifest><item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/><item id="css" href="style.css" media-type="text/css"/>`+manifest.String()+`</manifest><spine>`+spine.String()+`</spine></package>`)
	return zw.Close()
}

// ---------- 格式转换（带缓存） ----------

var convMu sync.Map // 同一文件同时只转换一次

func convertedPath(srcPath, ext string) string {
	return filepath.Join(cfg.DataDir, "cache", "conv", cacheKey(srcPath)+"."+ext)
}

// TXT 转出来的 EPUB 缓存文件名带分章规则的版本，改了规则就重新转
func txtEPUBExt(v int) string {
	if v <= 1 {
		return "epub"
	}
	return fmt.Sprintf("t%d.epub", v)
}

// 把文件转换成 EPUB（TXT 自己转，其他格式交给 Calibre）
func ensureEPUB(srcPath, format, title, author string) (string, error) {
	ext := "epub"
	if format == "txt" {
		ext = txtEPUBExt(txtSplitVersion)
		defer func() {
			for v := 1; v < txtSplitVersion; v++ { // 旧规则转出来的缓存用不到了
				os.Remove(convertedPath(srcPath, txtEPUBExt(v)))
			}
		}()
	}
	out := convertedPath(srcPath, ext)
	if _, err := os.Stat(out); err == nil {
		touchCache(out)
		return out, nil
	}
	m, _ := convMu.LoadOrStore(out, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()
	if _, err := os.Stat(out); err == nil {
		return out, nil
	}
	os.MkdirAll(filepath.Dir(out), 0o755)
	tmp := out + ".tmp.epub"
	defer os.Remove(tmp)
	if format == "txt" {
		b, err := os.ReadFile(srcPath)
		if err != nil {
			return "", err
		}
		text, _ := decodeText(b)
		f, err := os.Create(tmp)
		if err != nil {
			return "", err
		}
		err = buildEPUB(f, title, author, splitChapters(text))
		f.Close()
		if err != nil {
			return "", err
		}
	} else {
		bin, err := exec.LookPath("ebook-convert")
		if err != nil {
			return "", errors.New("这个格式需要 Calibre 转换，当前镜像没有安装 Calibre")
		}
		// 不生成默认封面（要用 Qt 画图，NAS 上没有显示器）；大文件也不能一直转下去
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, srcPath, tmp, "--no-default-epub-cover")
		if o, err := cmd.CombinedOutput(); err != nil {
			lines := strings.Split(strings.TrimSpace(string(o)), "\n")
			return "", fmt.Errorf("Calibre 转换失败: %s", lines[len(lines)-1])
		}
	}
	if err := os.Rename(tmp, out); err != nil {
		return "", err
	}
	noteCacheWrite()
	return out, nil
}

// DJVU 转 PDF
func ensurePDF(srcPath string) (string, error) {
	out := convertedPath(srcPath, "pdf")
	if _, err := os.Stat(out); err == nil {
		touchCache(out)
		return out, nil
	}
	m, _ := convMu.LoadOrStore(out, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()
	if _, err := os.Stat(out); err == nil {
		return out, nil
	}
	os.MkdirAll(filepath.Dir(out), 0o755)
	bin, err := exec.LookPath("ddjvu")
	if err != nil {
		return "", errors.New("服务器没有安装 djvulibre，无法打开 DJVU")
	}
	tmp := out + ".tmp.pdf"
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	if o, err := exec.CommandContext(ctx, bin, "-format=pdf", "-quality=85", srcPath, tmp).CombinedOutput(); err != nil {
		os.Remove(tmp)
		if ctx.Err() != nil {
			return "", errors.New("DJVU 转换超时（15 分钟）")
		}
		return "", fmt.Errorf("DJVU 转换失败: %s", strings.TrimSpace(string(o)))
	}
	noteCacheWrite()
	return out, os.Rename(tmp, out)
}
