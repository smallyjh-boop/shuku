package main

// 书内全文搜索：SQLite FTS5，中文按“两字一组”切词（黑暗森林 → 黑暗 暗森 森林），两个字以上的词都能搜到。
// 索引只存词，不存原文；正文另存一份压缩的分章文本，用来生成搜索结果里的上下文片段。

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

var ftAvailable bool

// 全文索引的表在单独的 fulltext.db 里（连接时挂成 fts，见 migrate.go）。
// 1.0.0.7 以前放在 shuku.db 里：删掉旧表、腾出空间，索引在后台重建。
// 在升级前备份之前做：索引能重建，不用备份，备份文件也就不会大得离谱
func dropMainFulltext() {
	var old int
	db.QueryRow(`SELECT COUNT(*) FROM main.sqlite_master WHERE name IN ('ft','ft_chunks','ft_files')`).Scan(&old)
	if old > 0 {
		for _, t := range []string{"ft", "ft_chunks", "ft_files"} {
			if _, err := db.Exec(`DROP TABLE IF EXISTS main.` + t); err != nil {
				log.Printf("删除旧的全文索引表 %s 失败：%v", t, err)
			}
		}
		// 旧索引占的空间很大：整理一次数据库文件（只在这次升级时做）
		var free, size int64
		db.QueryRow(`PRAGMA main.freelist_count`).Scan(&free)
		db.QueryRow(`PRAGMA main.page_size`).Scan(&size)
		if free*size > 32<<20 {
			log.Printf("全文索引已经搬到单独的 fulltext.db，正在整理数据库文件（释放 %d MB）…", free*size>>20)
			if _, err := db.Exec(`VACUUM main`); err != nil {
				log.Printf("整理数据库失败（不影响使用）：%v", err)
			}
		}
		logEvent("info", 0, "全文索引搬到了单独的文件，正在后台重建，书内搜索暂时可能不全")
	}
}

func migrateFulltext() {
	// 翻译过的段落（也能重建：再翻一次就有了）
	db.Exec(`CREATE TABLE IF NOT EXISTS fts.tr_cache (key TEXT PRIMARY KEY, text TEXT NOT NULL, created_at INTEGER NOT NULL)`)
	db.Exec(`CREATE TABLE IF NOT EXISTS fts.ft_chunks (id INTEGER PRIMARY KEY, book_id INTEGER NOT NULL, file_id INTEGER NOT NULL, sec INTEGER NOT NULL, part INTEGER NOT NULL DEFAULT 0, label TEXT NOT NULL DEFAULT '')`)
	db.Exec(`CREATE INDEX IF NOT EXISTS fts.idx_ft_chunks_file ON ft_chunks(file_id)`)
	db.Exec(`CREATE TABLE IF NOT EXISTS fts.ft_files (file_id INTEGER PRIMARY KEY, hash TEXT NOT NULL, chunks INTEGER NOT NULL DEFAULT 0, status TEXT NOT NULL DEFAULT 'ok', indexed_at INTEGER NOT NULL)`)
	_, err := db.Exec(`CREATE VIRTUAL TABLE IF NOT EXISTS fts.ft USING fts5(body, content='', contentless_delete=1, tokenize='unicode61 remove_diacritics 0')`)
	ftAvailable = err == nil
	if err != nil {
		logEvent("warn", 0, "当前程序没有编译全文搜索支持（需要 sqlite_fts5），书内搜索不可用："+err.Error())
	}
}

// ---------- 切词 ----------

func isCJK(r rune) bool {
	return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r)
}

// 中日韩文字两两成词，字母数字按单词
func tokenizeCJK(s string) []string {
	s = toSimplified(strings.ToLower(s))
	var out []string
	var run []rune
	var word strings.Builder
	flushRun := func() {
		if len(run) == 1 {
			out = append(out, string(run))
		}
		for i := 0; i+1 < len(run); i++ {
			out = append(out, string(run[i:i+2]))
		}
		run = run[:0]
	}
	flushWord := func() {
		if word.Len() > 0 {
			out = append(out, word.String())
			word.Reset()
		}
	}
	for _, r := range s {
		switch {
		case isCJK(r):
			flushWord()
			run = append(run, r)
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			flushRun()
			word.WriteRune(r)
		default:
			flushRun()
			flushWord()
		}
	}
	flushRun()
	flushWord()
	return out
}

// 搜索词 → FTS 查询：每个词是一个短语，词之间是“并且”
func ftsQuery(q string) string {
	var parts []string
	for _, term := range strings.Fields(q) {
		toks := tokenizeCJK(term)
		if len(toks) == 0 {
			continue
		}
		parts = append(parts, `"`+strings.Join(toks, " ")+`"`)
	}
	return strings.Join(parts, " AND ")
}

// ---------- 提取正文 ----------

type Section struct {
	Sec   int    `json:"s"` // 章节序号（EPUB 是第几个文档，PDF 是页码；-1 表示无法定位，打开后在书内搜索）
	Label string `json:"l"`
	Text  string `json:"t"`
}

var blockRe = regexp.MustCompile(`(?i)</(p|div|h[1-6]|li|tr|blockquote|section)>|<br\s*/?>`)
var headRe = regexp.MustCompile(`(?is)<h[1-3][^>]*>(.*?)</h[1-3]>`)
var titleTagRe = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
var scriptRe = regexp.MustCompile(`(?is)<(script|style|head)[^>]*>.*?</(script|style|head)>`)

func htmlToText(h string) string {
	h = scriptRe.ReplaceAllString(h, "")
	h = blockRe.ReplaceAllString(h, "\n")
	h = tagRe.ReplaceAllString(h, "")
	r := strings.NewReplacer("&nbsp;", " ", "&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'", "&#160;", " ")
	var lines []string
	for _, l := range strings.Split(r.Replace(h), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	return strings.Join(lines, "\n")
}

func extractSections(f *File) ([]Section, error) {
	switch f.Format {
	case "epub":
		return epubSections(f.Path)
	case "txt":
		b, err := os.ReadFile(f.Path)
		if err != nil {
			return nil, err
		}
		text, _ := decodeText(b)
		var out []Section
		for i, c := range splitChapters(text) {
			out = append(out, Section{Sec: i, Label: c.Title, Text: strings.Join(c.Lines, "\n")})
		}
		return out, nil
	case "fb2", "fbz":
		var b []byte
		if f.Format == "fbz" {
			z, err := zip.OpenReader(f.Path)
			if err != nil {
				return nil, err
			}
			for _, zf := range z.File {
				if strings.HasSuffix(strings.ToLower(zf.Name), ".fb2") {
					b, _ = readZipFile(zf, 64<<20)
				}
			}
			z.Close()
		} else {
			var err error
			if b, err = readLimited(f.Path, 64<<20); err != nil {
				return nil, err
			}
		}
		body := string(b)
		if i := strings.Index(body, "<body"); i >= 0 {
			body = body[i:]
		}
		return []Section{{Sec: -1, Text: htmlToText(regexp.MustCompile(`(?is)<binary.*?</binary>`).ReplaceAllString(body, ""))}}, nil
	case "mobi", "azw", "azw3":
		t, err := mobiText(f.Path)
		if err != nil {
			return nil, err
		}
		return []Section{{Sec: -1, Text: htmlToText(t)}}, nil
	case "pdf":
		return pdfSections(f.Path)
	case "docx", "doc", "rtf", "odt", "html", "md", "chm", "lit", "pdb", "lrf", "snb", "umd":
		// 这些格式先转成 EPUB（需要 Calibre），转不了就跳过
		b, _ := getBook(f.BookID)
		title := filepath.Base(f.Path)
		if b != nil {
			title = b.Title
		}
		p, err := ensureEPUB(f.Path, f.Format, title, "")
		if err != nil {
			return nil, err
		}
		return epubSections(p)
	}
	return nil, fmt.Errorf("不支持的格式")
}

func epubSections(p string) ([]Section, error) {
	z, err := zip.OpenReader(p)
	if err != nil {
		return nil, err
	}
	defer z.Close()
	files := map[string]*zip.File{}
	for _, f := range z.File {
		files[f.Name] = f
	}
	read := func(name string) []byte {
		f, ok := files[name]
		if !ok {
			for k, v := range files {
				if strings.EqualFold(k, name) {
					f, ok = v, true
				}
			}
		}
		if !ok {
			return nil
		}
		b, _ := readZipFile(f, 16<<20)
		return b
	}
	m := regexp.MustCompile(`full-path="([^"]+)"`).FindSubmatch(read("META-INF/container.xml"))
	if m == nil {
		return nil, fmt.Errorf("EPUB 结构不完整")
	}
	opfPath := string(m[1])
	var opf opfDoc
	if err := xmlDecodeLoose(read(opfPath), &opf); err != nil {
		return nil, err
	}
	base := path.Dir(opfPath)
	byID := map[string]string{}
	for _, it := range opf.Items {
		byID[it.ID] = it.Href
	}
	var out []Section
	for i, s := range opf.Spine {
		href, ok := byID[s.IDRef]
		if !ok {
			continue
		}
		if base != "." {
			href = path.Join(base, href)
		}
		if u, err := urlUnescape(href); err == nil {
			if _, ok := files[u]; ok {
				href = u
			}
		}
		raw := string(read(href))
		label := ""
		if h := headRe.FindStringSubmatch(raw); h != nil {
			label = cleanSpace(stripTags(h[1]))
		}
		if label == "" {
			if t := titleTagRe.FindStringSubmatch(raw); t != nil {
				label = cleanSpace(stripTags(t[1]))
			}
		}
		text := htmlToText(raw)
		if text != "" {
			out = append(out, Section{Sec: i, Label: truncate(label, 40), Text: text})
		}
	}
	return out, nil
}

func pdfSections(p string) ([]Section, error) {
	ctx, cancel := context.WithTimeout(appCtx, 3*time.Minute)
	defer cancel()
	var out []byte
	var err error
	// 两种工具输出的文字都用换页符分隔每一页
	if bin, e := exec.LookPath("mutool"); e == nil {
		out, err = exec.CommandContext(ctx, bin, "draw", "-q", "-F", "txt", "-o", "-", p).Output()
	} else if bin, e := exec.LookPath("pdftotext"); e == nil {
		out, err = exec.CommandContext(ctx, bin, "-q", "-enc", "UTF-8", p, "-").Output()
	} else {
		return nil, fmt.Errorf("服务器没有安装 mutool 或 pdftotext，PDF 跳过全文索引")
	}
	if err != nil && len(out) == 0 {
		return nil, fmt.Errorf("提取 PDF 文字失败")
	}
	var secs []Section
	for i, page := range strings.Split(string(out), "\f") {
		page = strings.TrimSpace(page)
		if page != "" {
			secs = append(secs, Section{Sec: i + 1, Label: fmt.Sprintf("第 %d 页", i+1), Text: page})
		}
	}
	return secs, nil
}

// MOBI / AZW3 正文：PalmDOC 压缩（最常见）；HUFF/CDIC 压缩的老书不支持
func mobiText(p string) (string, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	if len(b) < 86 {
		return "", fmt.Errorf("文件太小")
	}
	n := int(binary.BigEndian.Uint16(b[76:78]))
	rec := make([]int, n+1)
	for i := 0; i < n && 78+i*8+4 <= len(b); i++ {
		rec[i] = int(binary.BigEndian.Uint32(b[78+i*8:]))
	}
	rec[n] = len(b)
	r0 := rec[0]
	if r0+16 > len(b) {
		return "", fmt.Errorf("格式不正确")
	}
	comp := binary.BigEndian.Uint16(b[r0:])
	count := int(binary.BigEndian.Uint16(b[r0+8:]))
	extraFlags := uint16(0)
	if r0+16+0xF4 <= len(b) && string(b[r0+16:r0+20]) == "MOBI" {
		hl := int(binary.BigEndian.Uint32(b[r0+20:]))
		if hl >= 0xE4 {
			extraFlags = binary.BigEndian.Uint16(b[r0+16+0xF2:])
		}
	}
	if comp != 1 && comp != 2 {
		return "", fmt.Errorf("这种 MOBI 压缩方式暂不支持全文索引")
	}
	var sb strings.Builder
	for i := 1; i <= count && i < n; i++ {
		if rec[i] >= rec[i+1] || rec[i+1] > len(b) {
			continue
		}
		data := trimTrailing(b[rec[i]:rec[i+1]], extraFlags)
		if comp == 2 {
			data = palmdocDecompress(data)
		}
		sb.Write(data)
	}
	return strings.ToValidUTF8(sb.String(), ""), nil
}

// 去掉记录末尾的附加数据（多字节字符续接、索引等）
func trimTrailing(d []byte, flags uint16) []byte {
	for bit := 15; bit > 0; bit-- {
		if flags&(1<<uint(bit)) == 0 || len(d) == 0 {
			continue
		}
		// 读末尾的倒序变长整数
		size, shift := 0, 0
		for j := len(d) - 1; j >= 0 && j >= len(d)-4; j-- {
			c := d[j]
			size |= int(c&0x7F) << shift
			shift += 7
			if c&0x80 != 0 {
				break
			}
		}
		if size > 0 && size <= len(d) {
			d = d[:len(d)-size]
		}
	}
	if flags&1 != 0 && len(d) > 0 {
		k := int(d[len(d)-1]&3) + 1
		if k <= len(d) {
			d = d[:len(d)-k]
		}
	}
	return d
}

func palmdocDecompress(in []byte) []byte {
	out := make([]byte, 0, len(in)*2)
	for i := 0; i < len(in); {
		c := in[i]
		i++
		switch {
		case c >= 1 && c <= 8:
			if i+int(c) <= len(in) {
				out = append(out, in[i:i+int(c)]...)
			}
			i += int(c)
		case c < 0x80:
			out = append(out, c)
		case c >= 0xC0:
			out = append(out, ' ', c^0x80)
		default:
			if i >= len(in) {
				return out
			}
			v := int(c)<<8 | int(in[i])
			i++
			dist := (v >> 3) & 0x7FF
			l := (v & 7) + 3
			if dist == 0 || dist > len(out) {
				continue
			}
			for k := 0; k < l; k++ {
				out = append(out, out[len(out)-dist])
			}
		}
	}
	return out
}

func xmlDecodeLoose(b []byte, v any) error {
	dec := xml.NewDecoder(bytes.NewReader(b))
	dec.Strict = false
	dec.CharsetReader = func(label string, input io.Reader) (io.Reader, error) { return input, nil }
	return dec.Decode(v)
}

func urlUnescape(s string) (string, error) { return url.PathUnescape(s) }

// ---------- 正文缓存（生成搜索片段用） ----------

func textCachePath(f *File) string {
	h := f.Hash
	if len(h) > 24 {
		h = h[:24]
	}
	return filepath.Join(cfg.DataDir, "cache", "text", h[:2], h+".json.gz")
}

func saveSections(f *File, secs []Section) {
	p := textCachePath(f)
	os.MkdirAll(filepath.Dir(p), 0o755)
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	json.NewEncoder(zw).Encode(secs)
	zw.Close()
	os.WriteFile(p, buf.Bytes(), 0o644)
}

func loadSections(f *File) []Section {
	fh, err := os.Open(textCachePath(f))
	if err != nil {
		return nil
	}
	defer fh.Close()
	zr, err := gzip.NewReader(fh)
	if err != nil {
		return nil
	}
	var secs []Section
	json.NewDecoder(zr).Decode(&secs)
	return secs
}

const chunkSize = 3000

// 一章按字数切成若干块，每块一条索引
func chunksOf(text string) []string {
	r := []rune(text)
	var out []string
	for len(r) > chunkSize {
		cut := chunkSize
		// 尽量在换行处切
		for j := chunkSize; j > chunkSize-400; j-- {
			if r[j] == '\n' {
				cut = j
				break
			}
		}
		out = append(out, string(r[:cut]))
		r = r[cut:]
	}
	if len(r) > 0 {
		out = append(out, string(r))
	}
	return out
}

// ---------- 建索引 ----------

var ftIndexable = map[string]bool{"epub": true, "txt": true, "fb2": true, "fbz": true, "mobi": true, "azw": true, "azw3": true, "pdf": true,
	"docx": true, "doc": true, "rtf": true, "odt": true, "html": true, "md": true, "chm": true}

var ftState struct {
	sync.Mutex
	Running bool
	Current string
}

var ftKick = make(chan struct{}, 1)

func kickFulltext() {
	select {
	case ftKick <- struct{}{}:
	default:
	}
}

func fulltextEnabled() bool { return ftAvailable && getSetting("fulltext", "1") == "1" }

func removeFileIndex(fileID int64) {
	db.Exec(`DELETE FROM ft WHERE rowid IN (SELECT id FROM ft_chunks WHERE file_id=?)`, fileID)
	db.Exec(`DELETE FROM ft_chunks WHERE file_id=?`, fileID)
	db.Exec(`DELETE FROM ft_files WHERE file_id=?`, fileID)
}

func indexFile(f *File) (int, error) {
	secs, err := extractSections(f)
	if err != nil {
		return 0, err
	}
	removeFileIndex(f.ID)
	// 分批提交：每批几十块，写锁只占一小会儿，入库和网页上的修改不用等一整本书索引完。
	// 中途出错时已经写进去的块留着没关系：没写 ft_files，下次会先清掉再重建
	const batch = 64
	var tx *sql.Tx
	commit := func() error {
		if tx == nil {
			return nil
		}
		err := tx.Commit()
		tx = nil
		time.Sleep(10 * time.Millisecond) // 让别的写操作插进来
		return err
	}
	fail := func(err error) (int, error) {
		if tx != nil {
			tx.Rollback()
		}
		return 0, err
	}
	n := 0
	total := 0
	for _, s := range secs {
		for part, ch := range chunksOf(s.Text) {
			if tx == nil {
				if tx, err = db.Begin(); err != nil {
					return 0, err
				}
			}
			total += utf8.RuneCountInString(ch)
			res, err := tx.Exec(`INSERT INTO ft_chunks(book_id,file_id,sec,part,label) VALUES(?,?,?,?,?)`, f.BookID, f.ID, s.Sec, part, s.Label)
			if err != nil {
				return fail(err)
			}
			id, _ := res.LastInsertId()
			if _, err := tx.Exec(`INSERT INTO ft(rowid, body) VALUES(?,?)`, id, strings.Join(tokenizeCJK(ch), " ")); err != nil {
				return fail(err)
			}
			n++
			if n%batch == 0 {
				if err := commit(); err != nil {
					return fail(err)
				}
			}
		}
		if total > 20_000_000 { // 单本最多索引 2000 万字
			break
		}
	}
	if err := commit(); err != nil {
		return fail(err)
	}
	if _, err := db.Exec(`INSERT INTO ft_files(file_id,hash,chunks,status,indexed_at) VALUES(?,?,?,?,?) ON CONFLICT(file_id) DO UPDATE SET hash=excluded.hash,chunks=excluded.chunks,status=excluded.status,indexed_at=excluded.indexed_at`, f.ID, f.Hash, n, "ok", now()); err != nil {
		return 0, err
	}
	saveSections(f, secs)
	return n, nil
}

// TXT 的分章规则改过：章节分得不一样了的书，全文索引和章节缓存要重建，
// 搜索结果、手表才能对上新的章节。只在升级后做一次，只读 TXT 文件、不动书
func migrateTxtSplit(ctx context.Context) {
	want := strconv.Itoa(txtSplitVersion)
	if getSetting("txt_split_version", "1") == want {
		return
	}
	type txtFile struct {
		id         int64
		path, hash string
	}
	var list []txtFile
	rows, err := db.Query(`SELECT id, path, hash FROM files WHERE format='txt'`)
	if err != nil {
		return
	}
	for rows.Next() {
		var x txtFile
		if rows.Scan(&x.id, &x.path, &x.hash) == nil {
			list = append(list, x)
		}
	}
	rows.Close()
	changed := 0
	for _, x := range list {
		if ctx.Err() != nil {
			return // 下次启动接着做
		}
		b, err := os.ReadFile(x.path)
		if err != nil {
			continue
		}
		if text, _ := decodeText(b); txtSplitChanged(text) {
			db.Exec(`DELETE FROM ft_files WHERE file_id=?`, x.id)
			if len(x.hash) >= 2 {
				os.Remove(textCachePath(&File{Hash: x.hash}))
			}
			changed++
		}
		time.Sleep(20 * time.Millisecond)
	}
	setSetting("txt_split_version", want)
	if changed > 0 {
		log.Printf("TXT 分章规则更新：%d 本书重新分章，全文索引会重建", changed)
	}
}

func fulltextLoop(ctx context.Context) {
	time.Sleep(5 * time.Second)
	migrateTxtSplit(ctx)
	for {
		if fulltextEnabled() {
			fulltextPass(ctx)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Minute):
		case <-ftKick:
			time.Sleep(3 * time.Second)
		}
	}
}

func fulltextPass(ctx context.Context) {
	// 清理已经不存在的文件
	db.Exec(`DELETE FROM ft WHERE rowid IN (SELECT id FROM ft_chunks WHERE file_id NOT IN (SELECT id FROM files))`)
	db.Exec(`DELETE FROM ft_chunks WHERE file_id NOT IN (SELECT id FROM files)`)
	db.Exec(`DELETE FROM ft_files WHERE file_id NOT IN (SELECT id FROM files)`)
	for {
		if ctx.Err() != nil || !fulltextEnabled() {
			return
		}
		var f File
		err := db.QueryRow(`SELECT f.id,f.book_id,f.library_id,f.path,f.format,f.size,f.hash FROM files f JOIN books b ON b.id=f.book_id
			LEFT JOIN ft_files t ON t.file_id=f.id
			WHERE b.kind='book' AND b.deleted_at=0 AND f.missing_since=0 AND f.format IN ('epub','txt','fb2','fbz','mobi','azw','azw3','pdf','docx','doc','rtf','odt','html','md','chm')
			AND (t.file_id IS NULL OR t.hash<>f.hash) ORDER BY f.id LIMIT 1`).Scan(&f.ID, &f.BookID, &f.LibraryID, &f.Path, &f.Format, &f.Size, &f.Hash)
		if err != nil {
			return
		}
		ftState.Lock()
		ftState.Running, ftState.Current = true, filepath.Base(f.Path)
		ftState.Unlock()
		if _, err := indexFile(&f); err != nil {
			db.Exec(`INSERT INTO ft_files(file_id,hash,chunks,status,indexed_at) VALUES(?,?,0,?,?) ON CONFLICT(file_id) DO UPDATE SET hash=excluded.hash,status=excluded.status,indexed_at=excluded.indexed_at`, f.ID, f.Hash, "skip: "+truncate(err.Error(), 100), now())
		}
		ftState.Lock()
		ftState.Running, ftState.Current = false, ""
		ftState.Unlock()
		time.Sleep(150 * time.Millisecond) // 给 NAS 喘口气
	}
}

func fulltextStatus() map[string]any {
	var indexed, skipped, total int
	db.QueryRow(`SELECT COUNT(*) FROM ft_files WHERE status='ok'`).Scan(&indexed)
	db.QueryRow(`SELECT COUNT(*) FROM ft_files WHERE status<>'ok'`).Scan(&skipped)
	db.QueryRow(`SELECT COUNT(*) FROM files f JOIN books b ON b.id=f.book_id WHERE b.kind='book' AND b.deleted_at=0 AND f.missing_since=0 AND f.format IN ('epub','txt','fb2','fbz','mobi','azw','azw3','pdf','docx','doc','rtf','odt','html','md','chm')`).Scan(&total)
	ftState.Lock()
	defer ftState.Unlock()
	return map[string]any{"available": ftAvailable, "enabled": fulltextEnabled(), "indexed": indexed, "skipped": skipped, "total": total, "running": ftState.Running, "current": ftState.Current}
}

// ---------- 搜索 ----------

type ftHit struct {
	Sec     int    `json:"sec"`
	Label   string `json:"label"`
	Snippet string `json:"snippet"`
	Match   string `json:"match"`
	FileID  int64  `json:"file_id"`
}

type ftBook struct {
	ID     int64   `json:"id"`
	Title  string  `json:"title"`
	Author string  `json:"author"`
	Cover  string  `json:"cover"`
	Kind   string  `json:"kind"`
	Hits   []ftHit `json:"hits"`
	Total  int     `json:"total"`
}

// 在原文里找到搜索词的位置，截一段上下文（繁简通吃：t2s 是逐字替换，位置一一对应）
func makeSnippet(text, q string) (string, string, bool) {
	terms := strings.Fields(q)
	if len(terms) == 0 {
		return "", "", false
	}
	rt := []rune(text)
	norm := []rune(toSimplified(strings.ToLower(text)))
	if len(norm) != len(rt) {
		norm = []rune(strings.ToLower(text))
	}
	needle := []rune(toSimplified(strings.ToLower(terms[0])))
	idx := runeIndex(norm, needle)
	if idx < 0 {
		return "", "", false
	}
	start, end := idx-36, idx+len(needle)+60
	pre, post := "…", "…"
	if start <= 0 {
		start, pre = 0, ""
	}
	if end >= len(rt) {
		end, post = len(rt), ""
	}
	s := strings.ReplaceAll(string(rt[start:end]), "\n", " ")
	return pre + s + post, string(rt[idx : idx+len(needle)]), true
}

func runeIndex(h, n []rune) int {
outer:
	for i := 0; i+len(n) <= len(h); i++ {
		for j := range n {
			if h[i+j] != n[j] {
				continue outer
			}
		}
		return i
	}
	return -1
}

func handleFulltext(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if !ftAvailable {
		errJSON(w, 400, "这个版本没有全文搜索支持")
		return
	}
	fq := ftsQuery(q)
	if fq == "" {
		writeJSON(w, 200, map[string]any{"books": []ftBook{}, "status": fulltextStatus()})
		return
	}
	// 单个汉字没法用两字索引查
	if utf8.RuneCountInString(q) < 2 {
		writeJSON(w, 200, map[string]any{"books": []ftBook{}, "status": fulltextStatus(), "hint": "至少输入两个字"})
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	rows, err := db.Query(`SELECT c.book_id,c.file_id,c.sec,c.part,c.label FROM ft JOIN ft_chunks c ON c.id=ft.rowid WHERE ft MATCH ? ORDER BY rank LIMIT 400`, fq)
	if err != nil {
		errJSON(w, 400, "搜索词格式不正确")
		return
	}
	type row struct {
		book, file int64
		sec, part  int
		label      string
	}
	order := []int64{}
	byBook := map[int64][]row{}
	for rows.Next() {
		var x row
		rows.Scan(&x.book, &x.file, &x.sec, &x.part, &x.label)
		if _, ok := byBook[x.book]; !ok {
			order = append(order, x.book)
		}
		byBook[x.book] = append(byBook[x.book], x)
	}
	rows.Close()
	out := []ftBook{}
	secCache := map[int64][]Section{}
	u := me(r)
	for _, bid := range order {
		if len(out) >= limit {
			break
		}
		// 回收站里的书、家人看不到的书库里的书不出现在结果里
		if !canSeeBook(u, bid) {
			continue
		}
		b, err := getBook(bid)
		if err != nil {
			continue
		}
		fb := ftBook{ID: b.ID, Title: b.Title, Author: b.Author, Cover: b.Cover, Kind: b.Kind, Total: len(byBook[bid])}
		for _, x := range byBook[bid] {
			if len(fb.Hits) >= 3 {
				break
			}
			secs, ok := secCache[x.file]
			if !ok {
				if f, err := getFile(x.file); err == nil && f.MissingSince == 0 {
					secs = loadSections(f)
				}
				secCache[x.file] = secs
			}
			hit := ftHit{Sec: x.sec, Label: x.label, FileID: x.file}
			for _, s := range secs {
				if s.Sec == x.sec {
					chunks := chunksOf(s.Text)
					if x.part < len(chunks) {
						if sn, m, ok := makeSnippet(chunks[x.part], q); ok {
							hit.Snippet, hit.Match = sn, m
						}
					}
					break
				}
			}
			if hit.Snippet == "" {
				continue // 多个词分散在一块里时找不到连续片段，跳过
			}
			fb.Hits = append(fb.Hits, hit)
		}
		if len(fb.Hits) > 0 {
			out = append(out, fb)
		}
	}
	writeJSON(w, 200, map[string]any{"books": out, "status": fulltextStatus()})
}

func handleFulltextRebuild(w http.ResponseWriter, r *http.Request) {
	db.Exec(`INSERT INTO ft(ft) VALUES('delete-all')`)
	db.Exec(`DELETE FROM ft_chunks`)
	db.Exec(`DELETE FROM ft_files`)
	kickFulltext()
	writeJSON(w, 200, map[string]bool{"ok": true})
}
