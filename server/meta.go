package main

// 从电子书文件里读出书名、作者、封面等内嵌信息

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/xml"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
	"unicode/utf8"
)

type Meta struct {
	Title       string   `json:"title"`
	Author      string   `json:"author"`
	Translator  string   `json:"translator"`
	Series      string   `json:"series"`
	SeriesIndex float64  `json:"series_index"`
	Year        int      `json:"year"`
	Description string   `json:"description"`
	ISBN        string   `json:"isbn"`
	Language    string   `json:"language"`
	Publisher   string   `json:"publisher"`
	Tags        []string `json:"tags"`
	Direction   string   `json:"direction"`
	IsComic     bool     `json:"is_comic"`
	Pages       int      `json:"pages"`
	Sample      string   `json:"-"` // 正文开头几段，给 AI 参考
	Cover       []byte   `json:"-"`
}

func extractMeta(p string, format string) Meta {
	var m Meta
	defer func() { recover() }() // 坏文件不能让入库流程崩掉
	switch format {
	case "epub":
		m = epubMeta(p)
	case "fb2":
		if b, err := readLimited(p, 64<<20); err == nil {
			m = fb2Meta(b)
		}
	case "fbz":
		if z, err := zip.OpenReader(p); err == nil {
			for _, f := range z.File {
				if strings.HasSuffix(strings.ToLower(f.Name), ".fb2") {
					if b, err := readZipFile(f, 64<<20); err == nil {
						m = fb2Meta(b)
					}
					break
				}
			}
			z.Close()
		}
	case "mobi", "azw", "azw3":
		m = mobiMeta(p)
	case "pdf":
		m = pdfMeta(p)
	case "txt":
		if b, err := readLimited(p, 96<<10); err == nil {
			text, _ := decodeText(b)
			m.Sample = sampleText(text)
		}
	case "docx":
		m = docxMeta(p)
	case "cbz", "cbr", "cb7", "cbt", "zip", "rar", "7z":
		m = comicMeta(p, format)
	}
	m.Title = cleanSpace(m.Title)
	m.Author = cleanSpace(m.Author)
	return m
}

func readLimited(p string, n int64) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, n))
}

func readZipFile(f *zip.File, limit int64) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(io.LimitReader(rc, limit))
}

var spaceRe = regexp.MustCompile(`\s+`)

func cleanSpace(s string) string { return strings.TrimSpace(spaceRe.ReplaceAllString(s, " ")) }

var tagRe = regexp.MustCompile(`(?s)<[^>]*>`)

func stripTags(s string) string {
	s = tagRe.ReplaceAllString(s, " ")
	r := strings.NewReplacer("&nbsp;", " ", "&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'")
	return cleanSpace(r.Replace(s))
}

func sampleText(s string) string {
	s = cleanSpace(s)
	if utf8.RuneCountInString(s) > 1200 {
		s = string([]rune(s)[:1200])
	}
	return s
}

func yearOf(s string) int {
	if m := regexp.MustCompile(`(1[5-9]|20)\d\d`).FindString(s); m != "" {
		y, _ := strconv.Atoi(m)
		return y
	}
	return 0
}

var isbnRe = regexp.MustCompile(`97[89][\d-]{10,14}|\b\d{9}[\dXx]\b`)

func isbnOf(s string) string {
	m := isbnRe.FindString(s)
	return strings.ReplaceAll(m, "-", "")
}

// ---------- EPUB ----------

type opfDoc struct {
	Metadata struct {
		Titles   []string `xml:"title"`
		Creators []struct {
			Name   string `xml:",chardata"`
			Role   string `xml:"role,attr"`
			ID     string `xml:"id,attr"`
			FileAs string `xml:"file-as,attr"`
		} `xml:"creator"`
		Contributors []struct {
			Name string `xml:",chardata"`
			Role string `xml:"role,attr"`
		} `xml:"contributor"`
		Dates       []string `xml:"date"`
		Publisher   string   `xml:"publisher"`
		Language    string   `xml:"language"`
		Description string   `xml:"description"`
		Subjects    []string `xml:"subject"`
		Identifiers []string `xml:"identifier"`
		Metas       []struct {
			Name     string `xml:"name,attr"`
			Content  string `xml:"content,attr"`
			Property string `xml:"property,attr"`
			Refines  string `xml:"refines,attr"`
			Value    string `xml:",chardata"`
		} `xml:"meta"`
	} `xml:"metadata"`
	Items []struct {
		ID         string `xml:"id,attr"`
		Href       string `xml:"href,attr"`
		MediaType  string `xml:"media-type,attr"`
		Properties string `xml:"properties,attr"`
	} `xml:"manifest>item"`
	Spine []struct {
		IDRef string `xml:"idref,attr"`
	} `xml:"spine>itemref"`
}

func epubMeta(p string) Meta {
	var m Meta
	z, err := zip.OpenReader(p)
	if err != nil {
		return m
	}
	defer z.Close()
	files := map[string]*zip.File{}
	for _, f := range z.File {
		files[f.Name] = f
	}
	read := func(name string, limit int64) []byte {
		f, ok := files[name]
		if !ok {
			// 有些书大小写不一致
			for k, v := range files {
				if strings.EqualFold(k, name) {
					f, ok = v, true
					break
				}
			}
		}
		if !ok {
			return nil
		}
		b, _ := readZipFile(f, limit)
		return b
	}
	cont := read("META-INF/container.xml", 1<<20)
	opfPath := regexp.MustCompile(`full-path="([^"]+)"`).FindSubmatch(cont)
	if opfPath == nil {
		return m
	}
	op := string(opfPath[1])
	var opf opfDoc
	dec := xml.NewDecoder(bytes.NewReader(read(op, 8<<20)))
	dec.Strict = false
	dec.CharsetReader = func(label string, input io.Reader) (io.Reader, error) { return input, nil }
	if dec.Decode(&opf) != nil {
		return m
	}
	md := opf.Metadata
	if len(md.Titles) > 0 {
		m.Title = md.Titles[0]
	}
	roles := map[string]string{} // id -> role（EPUB3 用 meta refines 标角色）
	for _, mt := range md.Metas {
		if mt.Property == "role" && mt.Refines != "" {
			roles[strings.TrimPrefix(mt.Refines, "#")] = strings.TrimSpace(mt.Value)
		}
	}
	var authors, trans []string
	for _, c := range md.Creators {
		role := c.Role
		if role == "" {
			role = roles[c.ID]
		}
		name := cleanSpace(c.Name)
		if name == "" {
			continue
		}
		if role == "trl" {
			trans = append(trans, name)
		} else {
			authors = append(authors, name)
		}
	}
	for _, c := range md.Contributors {
		if c.Role == "trl" && cleanSpace(c.Name) != "" {
			trans = append(trans, cleanSpace(c.Name))
		}
	}
	m.Author = strings.Join(authors, " / ")
	m.Translator = strings.Join(trans, " / ")
	if len(md.Dates) > 0 {
		m.Year = yearOf(md.Dates[0])
	}
	m.Publisher = cleanSpace(md.Publisher)
	m.Language = cleanSpace(md.Language)
	m.Description = stripTags(md.Description)
	for _, s := range md.Subjects {
		if s = cleanSpace(s); s != "" {
			m.Tags = append(m.Tags, s)
		}
	}
	for _, id := range md.Identifiers {
		if i := isbnOf(id); i != "" && strings.Contains(strings.ToLower(id), "") {
			m.ISBN = i
			break
		}
	}
	coverID := ""
	for _, mt := range md.Metas {
		switch {
		case mt.Name == "calibre:series":
			m.Series = cleanSpace(mt.Content)
		case mt.Name == "calibre:series_index":
			m.SeriesIndex, _ = strconv.ParseFloat(mt.Content, 64)
		case mt.Property == "belongs-to-collection" && m.Series == "":
			m.Series = cleanSpace(mt.Value)
		case mt.Property == "group-position" && m.SeriesIndex == 0:
			m.SeriesIndex, _ = strconv.ParseFloat(strings.TrimSpace(mt.Value), 64)
		case mt.Name == "cover":
			coverID = mt.Content
		case mt.Property == "rendition:layout" && strings.TrimSpace(mt.Value) == "pre-paginated":
			m.IsComic = true // 固定版式 EPUB，多半是漫画或绘本
		}
	}
	base := path.Dir(op)
	resolve := func(href string) string {
		if base == "." {
			return href
		}
		return path.Join(base, href)
	}
	coverHref := ""
	for _, it := range opf.Items {
		if strings.Contains(it.Properties, "cover-image") || (coverID != "" && it.ID == coverID) {
			coverHref = it.Href
			break
		}
	}
	if coverHref == "" {
		for _, it := range opf.Items {
			if strings.HasPrefix(it.MediaType, "image/") && strings.Contains(strings.ToLower(it.Href+it.ID), "cover") {
				coverHref = it.Href
				break
			}
		}
	}
	if coverHref != "" {
		m.Cover = read(resolve(coverHref), 20<<20)
	}
	// 正文开头
	var sb strings.Builder
	byID := map[string]string{}
	for _, it := range opf.Items {
		byID[it.ID] = it.Href
	}
	for _, s := range opf.Spine {
		if sb.Len() > 6000 {
			break
		}
		if h, ok := byID[s.IDRef]; ok {
			sb.WriteString(stripTags(string(read(resolve(h), 2<<20))))
			sb.WriteString(" ")
		}
	}
	m.Sample = sampleText(sb.String())
	m.Pages = len(opf.Spine)
	return m
}

// ---------- FB2 ----------

func fb2Meta(b []byte) Meta {
	var m Meta
	var doc struct {
		Desc struct {
			Title struct {
				Genres  []string `xml:"genre"`
				Authors []struct {
					First  string `xml:"first-name"`
					Middle string `xml:"middle-name"`
					Last   string `xml:"last-name"`
					Nick   string `xml:"nickname"`
				} `xml:"author"`
				Translators []struct {
					First string `xml:"first-name"`
					Last  string `xml:"last-name"`
				} `xml:"translator"`
				BookTitle  string `xml:"book-title"`
				Annotation struct {
					Inner string `xml:",innerxml"`
				} `xml:"annotation"`
				Date     string `xml:"date"`
				Lang     string `xml:"lang"`
				Sequence struct {
					Name   string `xml:"name,attr"`
					Number string `xml:"number,attr"`
				} `xml:"sequence"`
				Cover struct {
					Image struct {
						Href  string `xml:"http://www.w3.org/1999/xlink href,attr"`
						Href2 string `xml:"href,attr"`
					} `xml:"image"`
				} `xml:"coverpage"`
			} `xml:"title-info"`
			Publish struct {
				Publisher string `xml:"publisher"`
				Year      string `xml:"year"`
				ISBN      string `xml:"isbn"`
			} `xml:"publish-info"`
		} `xml:"description"`
		Binaries []struct {
			ID   string `xml:"id,attr"`
			Data string `xml:",chardata"`
		} `xml:"binary"`
		Body struct {
			Inner string `xml:",innerxml"`
		} `xml:"body"`
	}
	dec := xml.NewDecoder(bytes.NewReader(b))
	dec.Strict = false
	dec.CharsetReader = func(label string, input io.Reader) (io.Reader, error) {
		if strings.Contains(strings.ToLower(label), "1251") || strings.Contains(strings.ToLower(label), "gb") {
			// 少见编码不处理，按 UTF-8 读，最多书名乱码
		}
		return input, nil
	}
	if dec.Decode(&doc) != nil {
		return m
	}
	t := doc.Desc.Title
	m.Title = t.BookTitle
	var as []string
	for _, a := range t.Authors {
		n := cleanSpace(strings.Join([]string{a.First, a.Middle, a.Last}, " "))
		if n == "" {
			n = a.Nick
		}
		if n != "" {
			as = append(as, n)
		}
	}
	m.Author = strings.Join(as, " / ")
	var ts []string
	for _, a := range t.Translators {
		if n := cleanSpace(a.First + " " + a.Last); n != "" {
			ts = append(ts, n)
		}
	}
	m.Translator = strings.Join(ts, " / ")
	m.Description = stripTags(t.Annotation.Inner)
	m.Year = yearOf(t.Date + " " + doc.Desc.Publish.Year)
	m.Language = t.Lang
	m.Series = cleanSpace(t.Sequence.Name)
	m.SeriesIndex, _ = strconv.ParseFloat(t.Sequence.Number, 64)
	m.Publisher = doc.Desc.Publish.Publisher
	m.ISBN = isbnOf(doc.Desc.Publish.ISBN)
	m.Tags = t.Genres
	href := strings.TrimPrefix(t.Cover.Image.Href+t.Cover.Image.Href2, "#")
	for _, bin := range doc.Binaries {
		if href != "" && bin.ID == href {
			m.Cover, _ = base64.StdEncoding.DecodeString(strings.Join(strings.Fields(bin.Data), ""))
		}
	}
	m.Sample = sampleText(stripTags(doc.Body.Inner))
	return m
}

// ---------- MOBI / AZW3 ----------

func mobiMeta(p string) Meta {
	var m Meta
	b, err := os.ReadFile(p)
	if err != nil || len(b) < 78+8 {
		return m
	}
	m.Title = strings.TrimRight(string(b[:32]), "\x00")
	n := int(binary.BigEndian.Uint16(b[76:78]))
	if len(b) < 78+n*8 {
		return m
	}
	rec := make([]int, n+1)
	for i := 0; i < n; i++ {
		rec[i] = int(binary.BigEndian.Uint32(b[78+i*8:]))
	}
	rec[n] = len(b)
	r0 := rec[0]
	if r0+16+0x80 > len(b) || string(b[r0+16:r0+20]) != "MOBI" {
		return m
	}
	mh := r0 + 16
	hlen := int(binary.BigEndian.Uint32(b[mh+4:]))
	encoding := binary.BigEndian.Uint32(b[mh+12:])
	dec := func(x []byte) string {
		if encoding == 1252 {
			r := make([]rune, len(x))
			for i, c := range x {
				r[i] = rune(c)
			}
			return string(r)
		}
		return strings.ToValidUTF8(string(x), "")
	}
	nameOff := int(binary.BigEndian.Uint32(b[mh+0x44:]))
	nameLen := int(binary.BigEndian.Uint32(b[mh+0x48:]))
	if r0+nameOff+nameLen <= len(b) && nameLen > 0 {
		m.Title = dec(b[r0+nameOff : r0+nameOff+nameLen])
	}
	firstImage := int(binary.BigEndian.Uint32(b[mh+0x5c:]))
	exthFlags := binary.BigEndian.Uint32(b[mh+0x70:])
	coverOff := -1
	if exthFlags&0x40 != 0 {
		e := mh + hlen
		if e+12 <= len(b) && string(b[e:e+4]) == "EXTH" {
			cnt := int(binary.BigEndian.Uint32(b[e+8:]))
			q := e + 12
			var authors []string
			for i := 0; i < cnt && q+8 <= len(b); i++ {
				typ := binary.BigEndian.Uint32(b[q:])
				ln := int(binary.BigEndian.Uint32(b[q+4:]))
				if ln < 8 || q+ln > len(b) {
					break
				}
				data := b[q+8 : q+ln]
				switch typ {
				case 100:
					authors = append(authors, dec(data))
				case 101:
					m.Publisher = dec(data)
				case 103:
					m.Description = stripTags(dec(data))
				case 104:
					m.ISBN = isbnOf(dec(data))
				case 105:
					m.Tags = append(m.Tags, dec(data))
				case 106:
					m.Year = yearOf(dec(data))
				case 108:
					// contributor，常常是转换工具名，忽略
				case 201:
					if len(data) >= 4 {
						coverOff = int(binary.BigEndian.Uint32(data))
					}
				case 503:
					m.Title = dec(data)
				case 524:
					m.Language = dec(data)
				}
				q += ln
			}
			m.Author = strings.Join(authors, " / ")
		}
	}
	if coverOff >= 0 && firstImage > 0 && firstImage+coverOff < n {
		i := firstImage + coverOff
		if rec[i] < rec[i+1] && rec[i+1] <= len(b) {
			m.Cover = append([]byte(nil), b[rec[i]:rec[i+1]]...)
		}
	}
	return m
}

// ---------- PDF ----------

func pdfString(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var bs []byte
	if raw[0] == '<' { // 十六进制字符串
		h := strings.Map(func(r rune) rune {
			if strings.ContainsRune("0123456789abcdefABCDEF", r) {
				return r
			}
			return -1
		}, string(raw))
		if len(h)%2 == 1 {
			h += "0"
		}
		for i := 0; i+1 < len(h); i += 2 {
			v, _ := strconv.ParseUint(h[i:i+2], 16, 8)
			bs = append(bs, byte(v))
		}
	} else { // 字面字符串，去括号并处理转义
		s := raw[1 : len(raw)-1]
		for i := 0; i < len(s); i++ {
			c := s[i]
			if c != '\\' || i+1 >= len(s) {
				bs = append(bs, c)
				continue
			}
			i++
			switch s[i] {
			case 'n':
				bs = append(bs, '\n')
			case 'r':
				bs = append(bs, '\r')
			case 't':
				bs = append(bs, '\t')
			case 'b', 'f':
			case '\n', '\r':
			default:
				if s[i] >= '0' && s[i] <= '7' {
					v := 0
					j := 0
					for j < 3 && i < len(s) && s[i] >= '0' && s[i] <= '7' {
						v = v*8 + int(s[i]-'0')
						i++
						j++
					}
					i--
					bs = append(bs, byte(v))
				} else {
					bs = append(bs, s[i])
				}
			}
		}
	}
	if len(bs) >= 2 && bs[0] == 0xFE && bs[1] == 0xFF {
		u := make([]uint16, 0, len(bs)/2)
		for i := 2; i+1 < len(bs); i += 2 {
			u = append(u, uint16(bs[i])<<8|uint16(bs[i+1]))
		}
		return string(utf16.Decode(u))
	}
	if utf8.Valid(bs) {
		return string(bs)
	}
	if s, ok := decodeGBK(bs); ok {
		return s
	}
	return ""
}

var pdfFieldRe = map[string]*regexp.Regexp{}
var pdfFieldMu sync.Mutex // 入库和“重新识别”可能同时在读 PDF 信息

func pdfField(b []byte, key string) string {
	pdfFieldMu.Lock()
	re, ok := pdfFieldRe[key]
	if !ok {
		re = regexp.MustCompile(`/` + key + `\s*(\((?:\\.|[^\\)])*\)|<[0-9A-Fa-f\s]*>)`)
		pdfFieldRe[key] = re
	}
	pdfFieldMu.Unlock()
	mm := re.FindAllSubmatch(b, -1)
	for i := len(mm) - 1; i >= 0; i-- { // 增量更新的 PDF，后面的信息更新
		if s := cleanSpace(pdfString(mm[i][1])); s != "" {
			return s
		}
	}
	return ""
}

func pdfMeta(p string) Meta {
	var m Meta
	f, err := os.Open(p)
	if err != nil {
		return m
	}
	defer f.Close()
	st, _ := f.Stat()
	head := make([]byte, 256<<10)
	nh, _ := f.ReadAt(head, 0)
	head = head[:nh]
	var tail []byte
	if st.Size() > int64(nh) {
		tail = make([]byte, 256<<10)
		off := st.Size() - int64(len(tail))
		if off < 0 {
			off = 0
		}
		nt, _ := f.ReadAt(tail, off)
		tail = tail[:nt]
	}
	all := append(head, tail...)
	m.Title = pdfField(all, "Title")
	m.Author = pdfField(all, "Author")
	m.Pages = len(regexp.MustCompile(`/Type\s*/Page[^s]`).FindAll(all, -1))
	// 很多 PDF 的标题是 "Microsoft Word - xxx.doc" 之类，没用
	low := strings.ToLower(m.Title)
	if strings.HasPrefix(low, "microsoft word") || strings.HasSuffix(low, ".doc") || strings.HasSuffix(low, ".docx") || low == "untitled" || strings.HasPrefix(low, "untitled") {
		m.Title = ""
	}
	if a := strings.ToLower(m.Author); a == "administrator" || a == "user" || a == "admin" || a == "owner" {
		m.Author = ""
	}
	m.Cover = renderPDFCover(p)
	return m
}

// 有 mutool 或 pdftoppm 时在服务端生成 PDF 封面；没有的话前端打开书时用 pdf.js 生成后上传
func renderPDFCover(p string) []byte {
	tmp, err := os.MkdirTemp("", "cover")
	if err != nil {
		return nil
	}
	defer os.RemoveAll(tmp)
	out := filepath.Join(tmp, "c.png")
	// 坏文件可能让转换程序一直卡着：最多等 1 分钟（入库是一本一本做的，卡住会挡住后面所有的书）
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if bin, err := exec.LookPath("mutool"); err == nil {
		exec.CommandContext(ctx, bin, "draw", "-q", "-w", "480", "-o", out, p, "1").Run()
	} else if bin, err := exec.LookPath("pdftoppm"); err == nil {
		exec.CommandContext(ctx, bin, "-png", "-f", "1", "-l", "1", "-scale-to", "480", "-singlefile", p, filepath.Join(tmp, "c")).Run()
	}
	b, _ := os.ReadFile(out)
	return b
}

// ---------- DOCX ----------

func docxMeta(p string) Meta {
	var m Meta
	z, err := zip.OpenReader(p)
	if err != nil {
		return m
	}
	defer z.Close()
	for _, f := range z.File {
		switch f.Name {
		case "docProps/core.xml":
			b, _ := readZipFile(f, 1<<20)
			if t := regexp.MustCompile(`<dc:title>([^<]*)</dc:title>`).FindSubmatch(b); t != nil {
				m.Title = string(t[1])
			}
			if t := regexp.MustCompile(`<dc:creator>([^<]*)</dc:creator>`).FindSubmatch(b); t != nil {
				m.Author = string(t[1])
			}
		case "word/document.xml":
			b, _ := readZipFile(f, 2<<20)
			m.Sample = sampleText(stripTags(string(b)))
		}
	}
	return m
}

// ---------- 漫画 ----------

func comicMeta(p, format string) Meta {
	var m Meta
	var names []string
	var read func(string) ([]byte, error)
	switch format {
	case "cbz", "zip", "epub", "fbz":
		a, err := openArchive(p, format)
		if err != nil {
			return m
		}
		defer a.Close()
		names, read = a.Names(), a.Read
	default:
		// rar、7z、tar：只读封面和 ComicInfo，不把整包解压到缓存（以前入库时整包解压，占的空间和漫画本身一样大）
		ns, data, err := peekArchive(p, format, func(all []string) []string {
			var want []string
			if imgs := imageNames(all); len(imgs) > 0 {
				want = append(want, imgs[0])
			}
			for _, n := range all {
				if strings.EqualFold(filepath.Base(n), "ComicInfo.xml") {
					want = append(want, n)
					break
				}
			}
			return want
		})
		if err != nil {
			return m
		}
		names = ns
		read = func(n string) ([]byte, error) {
			if b, ok := data[n]; ok {
				return b, nil
			}
			return nil, os.ErrNotExist
		}
	}
	imgs := imageNames(names)
	// 压缩包里只要有电子书或大量非图片文件，就不当作漫画
	if len(imgs) == 0 || len(imgs)*2 < len(names)-2 {
		return m
	}
	m.IsComic = true
	m.Pages = len(imgs)
	m.Cover, _ = read(imgs[0])
	for _, n := range names {
		if strings.EqualFold(filepath.Base(n), "ComicInfo.xml") {
			b, _ := read(n)
			var ci struct {
				Title, Series, Number, Volume, Writer, Summary, Genre, Tags, Manga, LanguageISO, Publisher string
				Year                                                                                       int
			}
			if xml.Unmarshal(b, &ci) == nil {
				m.Title = ci.Title
				m.Series = ci.Series
				num := ci.Volume
				if num == "" {
					num = ci.Number
				}
				m.SeriesIndex, _ = strconv.ParseFloat(num, 64)
				m.Author = ci.Writer
				m.Description = ci.Summary
				m.Year = ci.Year
				m.Language = ci.LanguageISO
				m.Publisher = ci.Publisher
				for _, t := range strings.Split(ci.Genre+","+ci.Tags, ",") {
					if t = strings.TrimSpace(t); t != "" {
						m.Tags = append(m.Tags, t)
					}
				}
				if strings.Contains(ci.Manga, "RightToLeft") {
					m.Direction = "rtl"
				}
			}
			break
		}
	}
	return m
}
