package main

// 入库：投递目录监听、书库扫描、封面、搜索关键字

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mozillazg/go-pinyin"
	_ "golang.org/x/image/bmp"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

func jsonUnmarshal(s string, v any) { json.Unmarshal([]byte(s), v) }

// 快速指纹：大小 + 开头、中间、结尾各 1MB。几 GB 的漫画包也能秒算
func quickHash(p string) (string, int64, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", 0, err
	}
	h := sha1.New()
	fmt.Fprintf(h, "%d|", st.Size())
	const chunk = 1 << 20
	if st.Size() <= 3*chunk {
		io.Copy(h, f)
	} else {
		buf := make([]byte, chunk)
		for _, off := range []int64{0, st.Size()/2 - chunk/2, st.Size() - chunk} {
			n, _ := f.ReadAt(buf, off)
			h.Write(buf[:n])
		}
	}
	return hex.EncodeToString(h.Sum(nil)), st.Size(), nil
}

// ---------- 封面 ----------

func coverPath(bookID int64) string {
	return filepath.Join(cfg.DataDir, "covers", strconv.FormatInt(bookID, 10)+".jpg")
}

func resizeImage(data []byte, maxW int, quality int) ([]byte, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	b := img.Bounds()
	if b.Dx() > maxW {
		h := b.Dy() * maxW / b.Dx()
		dst := image.NewRGBA(image.Rect(0, 0, maxW, h))
		draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Over, nil)
		img = dst
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func saveCover(bookID int64, data []byte) bool {
	if len(data) == 0 {
		return false
	}
	out, err := resizeImage(data, 480, 82)
	if err != nil {
		return false
	}
	if os.WriteFile(coverPath(bookID), out, 0o644) != nil {
		return false
	}
	db.Exec(`UPDATE books SET cover=? WHERE id=?`, fmt.Sprintf("v%d", time.Now().UnixMilli()), bookID)
	return true
}

// ---------- 搜索关键字（含拼音、首字母） ----------

var pyArgs = func() pinyin.Args {
	a := pinyin.NewArgs()
	a.Fallback = func(r rune, a pinyin.Args) []string { return []string{string(r)} }
	return a
}()

func pinyinOf(s string) (full, initials string) {
	var f, in strings.Builder
	for _, w := range pinyin.LazyPinyin(s, pyArgs) {
		w = strings.ToLower(w)
		f.WriteString(w)
		if len(w) > 0 {
			r := []rune(w)
			if r[0] < 128 && (r[0] >= 'a' && r[0] <= 'z' || r[0] >= '0' && r[0] <= '9') {
				in.WriteRune(r[0])
			}
		}
	}
	return f.String(), in.String()
}

func searchKeyFor(b *Book) string {
	parts := []string{b.Title, b.OrigTitle, b.Author, b.Translator, b.Series, b.Publisher, b.ISBN, b.Category, strings.Join(b.Tags, " "), b.OrigName}
	key := strings.ToLower(strings.Join(parts, " | "))
	for _, s := range []string{b.Title, b.Author, b.Series} {
		if s == "" {
			continue
		}
		f, in := pinyinOf(strings.ReplaceAll(s, " ", ""))
		key += " | " + f + " | " + in
	}
	return toSimplified(key)
}

func updateSearchKey(id int64) {
	if b, err := getBook(id); err == nil {
		tp, _ := pinyinOf(toSimplified(b.Title))
		ap, _ := pinyinOf(toSimplified(b.Author))
		if b.Author == "" {
			ap = "~" // 没有作者的排在最后
		}
		db.Exec(`UPDATE books SET search_key=?,sort_key=?,author_key=? WHERE id=?`, searchKeyFor(b), tp, ap, id)
	}
}

// ---------- 入库 ----------

type IngestResult struct {
	BookID int64
	Status string // new | attached | duplicate | review | unsupported | error
	Msg    string
}

var ingestMu sync.Mutex

// fromInbox=true：入库后按模板移动到书库；false：书库里已有的文件，原地登记
func ingestFile(ctx context.Context, p string, fromInbox bool) IngestResult {
	ingestMu.Lock()
	defer ingestMu.Unlock()
	name := filepath.Base(p)
	fi, ok := formatOf(p)
	if !ok {
		return IngestResult{Status: "unsupported", Msg: "不支持的文件类型"}
	}
	hash, size, err := quickHash(p)
	if err != nil {
		return IngestResult{Status: "error", Msg: err.Error()}
	}
	var dupID, dupBook int64
	var dupPath string
	if db.QueryRow(`SELECT id,book_id,path FROM files WHERE hash=? LIMIT 1`, hash).Scan(&dupID, &dupBook, &dupPath) == nil {
		if _, err := os.Stat(dupPath); err == nil && filepath.Clean(dupPath) != filepath.Clean(p) {
			return IngestResult{BookID: dupBook, Status: "duplicate", Msg: "书库里已有完全相同的文件：" + dupPath}
		}
		if err != nil { // 旧记录的文件已经不在了，用新的顶替
			db.Exec(`DELETE FROM files WHERE id=?`, dupID)
		}
	}
	m := extractMeta(p, fi.Format)
	kind := fi.Kind
	if fi.Kind == "comic" && !m.IsComic {
		if fi.Format == "zip" || fi.Format == "rar" || fi.Format == "7z" {
			return IngestResult{Status: "unsupported", Msg: "压缩包里不是漫画图片"}
		}
	}
	if m.IsComic {
		kind = "comic"
	}
	// 书库里登记已有文件时，以所在书库的类型为准
	var lib *Library
	if !fromInbox {
		lib, _ = insideLibrary(p)
		if lib != nil && lib.Kind == "comic" && (fi.Format == "pdf" || fi.Format == "epub") {
			kind = "comic"
		}
	}
	id := ruleIdentify(name, m, kind)
	if fromInbox && aiReady() {
		if aid, err := aiIdentify(ctx, name, m, kind, id); err == nil {
			id = aid
		} else {
			logEvent("warn", 0, fmt.Sprintf("《%s》AI 识别失败，改用规则识别：%v", truncate(name, 40), err))
		}
	}
	if id.Title == "" {
		id.Title = strings.TrimSuffix(name, filepath.Ext(name))
	}
	// 联网校验：豆瓣 / Open Library / Google Books / Bangumi 能对上书名作者，就更有把握
	var online *Candidate
	if mc := metaConfig(); fromInbox && mc.Enabled && mc.OnIngest {
		octx, cancel := context.WithTimeout(ctx, 25*time.Second)
		online = bestMatch(octx, lookupQuery{Title: id.Title, Author: id.Author, ISBN: m.ISBN, Kind: kind})
		cancel()
		if online != nil {
			if id.Author == "" && online.Author != "" && online.Score >= 0.9 {
				id.Author = online.Author
			}
			if online.Score >= 0.9 && id.Confidence < 0.92 {
				id.Confidence = 0.92
			}
			id.Source += " · " + online.Source + "已核对"
		}
	}
	if lib == nil {
		lib = libraryFor(kind)
	}
	if lib == nil {
		return IngestResult{Status: "error", Msg: "还没有设置书库目录"}
	}
	confident := id.Confidence >= autoThreshold()
	status := "ok"
	if !confident {
		status = "review"
	}
	// 同一本书的其他格式：书名和作者都一致就挂到同一个书目下
	var bookID int64
	attached := false
	if id.Title != "" {
		rows, _ := db.Query(`SELECT id,title,author FROM books WHERE kind=?`, kind)
		for rows != nil && rows.Next() {
			var bid int64
			var t, a string
			rows.Scan(&bid, &t, &a)
			if normTitle(t) == normTitle(id.Title) && normTitle(a) == normTitle(id.Author) && (kind != "comic") {
				var hasFmt int
				db.QueryRow(`SELECT COUNT(*) FROM files WHERE book_id=? AND format=?`, bid, fi.Format).Scan(&hasFmt)
				if hasFmt == 0 {
					bookID, attached = bid, true
					break
				}
			}
		}
		if rows != nil {
			rows.Close()
		}
	}
	ts := now()
	if !attached {
		tags, _ := json.Marshal(nonNil(id.Tags))
		res, err := db.Exec(`INSERT INTO books(title,orig_title,author,translator,series,series_index,year,category,tags,description,isbn,language,publisher,kind,direction,status,confidence,source,orig_name,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			id.Title, id.OrigTitle, id.Author, id.Translator, id.Series, id.SeriesIndex, id.Year, id.Category, string(tags), id.Description, m.ISBN, id.Language, m.Publisher, kind, m.Direction, status, id.Confidence, id.Source, name, ts, ts)
		if err != nil {
			return IngestResult{Status: "error", Msg: err.Error()}
		}
		bookID, _ = res.LastInsertId()
	}
	res, err := db.Exec(`INSERT INTO files(book_id,library_id,path,format,size,hash,created_at) VALUES(?,?,?,?,?,?,?)`, bookID, lib.ID, p, fi.Format, size, hash, ts)
	if err != nil {
		if !attached {
			db.Exec(`DELETE FROM books WHERE id=?`, bookID)
		}
		return IngestResult{Status: "error", Msg: err.Error()}
	}
	fileID, _ := res.LastInsertId()
	if _, err := os.Stat(coverPath(bookID)); err != nil {
		saveCover(bookID, m.Cover)
	}
	if online != nil && !attached {
		octx, cancel := context.WithTimeout(ctx, 20*time.Second)
		fillFromCandidate(octx, bookID, online)
		cancel()
	}
	updateSearchKey(bookID)
	kickFulltext()

	r := IngestResult{BookID: bookID, Status: "new"}
	if attached {
		r.Status = "attached"
	}
	if fromInbox {
		batch := "in-" + newBatch()
		f, _ := getFile(fileID)
		if confident {
			if err := organizeBook(bookID, batch, "入库整理"); err != nil {
				r.Msg = "整理失败：" + err.Error()
			}
		} else {
			// 把握不大：先放到书库的“_待确认”文件夹，能读，等你确认后再按规范整理
			to := filepath.Join(lib.Path, "_待确认", name)
			moveMu.Lock()
			if err := relocate(f, to, batch, "待确认"); err != nil {
				r.Msg = "移动失败：" + err.Error()
			}
			moveMu.Unlock()
			r.Status = "review"
		}
	}
	return r
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// ---------- 投递目录 ----------

var inboxKick = make(chan struct{}, 1)

func kickInbox() {
	select {
	case inboxKick <- struct{}{}:
	default:
	}
}

type inboxState struct {
	mu      sync.Mutex
	pending int
	current string
	seen    map[string][2]int64 // 路径 -> 大小, 修改时间（判断文件是否已复制完）
}

var inbox = &inboxState{seen: map[string][2]int64{}}

func inboxStatus() map[string]any {
	inbox.mu.Lock()
	defer inbox.mu.Unlock()
	return map[string]any{"pending": inbox.pending, "current": inbox.current}
}

func inboxLoop(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		processInbox(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-inboxKick:
			time.Sleep(500 * time.Millisecond)
		}
	}
}

func skipName(n string) bool {
	return strings.HasPrefix(n, ".") || strings.HasPrefix(n, "@") || strings.HasPrefix(n, "#") || strings.HasSuffix(n, ".shuku-part") || n == "Thumbs.db" || n == "desktop.ini"
}

func processInbox(ctx context.Context) {
	var ready []string
	stable := map[string]bool{}
	total := 0
	filepath.WalkDir(cfg.Inbox, func(p string, e os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if e.IsDir() {
			if p != cfg.Inbox && (skipName(e.Name()) || strings.HasPrefix(e.Name(), "_")) {
				return filepath.SkipDir // _无法识别、_重复 等处理结果目录
			}
			return nil
		}
		if skipName(e.Name()) {
			return nil
		}
		total++
		info, err := e.Info()
		if err != nil {
			return nil
		}
		sig := [2]int64{info.Size(), info.ModTime().UnixNano()}
		inbox.mu.Lock()
		prev, ok := inbox.seen[p]
		inbox.seen[p] = sig
		inbox.mu.Unlock()
		// 连续两次看到的大小和时间都没变，并且 3 秒内没改过，才认为复制完成
		if ok && prev == sig && time.Since(info.ModTime()) > 3*time.Second {
			ready = append(ready, p)
			stable[p] = true
		}
		return nil
	})
	inbox.mu.Lock()
	inbox.pending = total
	inbox.mu.Unlock()
	sortNatural(ready)
	// 同一个文件有多份时，先处理不带“副本”“(1)”的那份，重复的副本移到 _重复
	sort.SliceStable(ready, func(i, j int) bool { return !isCopyName(ready[i]) && isCopyName(ready[j]) })
	for _, p := range ready {
		if ctx.Err() != nil {
			return
		}
		inbox.mu.Lock()
		inbox.current = filepath.Base(p)
		inbox.mu.Unlock()
		r := ingestFile(ctx, p, true)
		name := filepath.Base(p)
		switch r.Status {
		case "new", "attached":
			msg := "已入库：" + name
			if b, err := getBook(r.BookID); err == nil {
				msg = fmt.Sprintf("已入库《%s》%s", b.Title, ifs(b.Author != "", " · "+b.Author, ""))
				if r.Status == "attached" {
					msg += "（合并为同一本书的另一格式）"
				}
			}
			if r.Msg != "" {
				msg += "，" + r.Msg
			}
			logEvent("info", r.BookID, msg)
		case "review":
			b, _ := getBook(r.BookID)
			t := name
			if b != nil {
				t = b.Title
			}
			logEvent("warn", r.BookID, fmt.Sprintf("《%s》识别把握不大，已放进待确认", t))
		case "duplicate":
			moveAside(p, "_重复")
			logEvent("warn", r.BookID, name+" 已存在，已移到投递目录的 _重复 文件夹")
		case "unsupported":
			moveAside(p, "_无法识别")
			logEvent("warn", 0, name+"："+r.Msg+"，已移到投递目录的 _无法识别 文件夹")
		case "error":
			moveAside(p, "_出错")
			logEvent("error", 0, name+" 入库失败："+r.Msg)
		}
		inbox.mu.Lock()
		delete(inbox.seen, p)
		inbox.current = ""
		inbox.mu.Unlock()
		removeEmptyParents(filepath.Dir(p))
	}
}

var copyNameRe = regexp.MustCompile(`(?i)(副本|拷贝|复制|[ _-]copy\b|\(\d\)|（\d）|\[\d\])[^/\\]*$`)

func isCopyName(p string) bool { return copyNameRe.MatchString(filepath.Base(p)) }

func ifs(c bool, a, b string) string {
	if c {
		return a
	}
	return b
}

func moveAside(p, dir string) {
	if _, err := os.Stat(p); err != nil {
		return
	}
	to := freePath(filepath.Join(cfg.Inbox, dir, filepath.Base(p)))
	moveFile(p, to)
}

// ---------- 书库扫描 ----------

var scanKick = make(chan struct{}, 1)
var scanning sync.Mutex
var scanState struct {
	sync.Mutex
	Running bool   `json:"running"`
	Found   int    `json:"found"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
	Last    int64  `json:"last"`
	Current string `json:"current"`
}

func kickScan() {
	select {
	case scanKick <- struct{}{}:
	default:
	}
}

func scanLoop(ctx context.Context) {
	time.Sleep(2 * time.Second)
	for {
		scanLibraries(ctx)
		mins, _ := strconv.Atoi(getSetting("scan_minutes", "60"))
		if mins < 5 {
			mins = 5
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(mins) * time.Minute):
		case <-scanKick:
		}
	}
}

func scanLibraries(ctx context.Context) {
	if !scanning.TryLock() {
		return
	}
	defer scanning.Unlock()
	scanState.Lock()
	scanState.Running, scanState.Found, scanState.Added, scanState.Removed = true, 0, 0, 0
	scanState.Unlock()
	defer func() {
		scanState.Lock()
		scanState.Running, scanState.Last, scanState.Current = false, now(), ""
		scanState.Unlock()
	}()
	known := map[string]bool{}
	rows, err := db.Query(`SELECT path FROM files`)
	if err == nil {
		for rows.Next() {
			var p string
			rows.Scan(&p)
			known[p] = true
		}
		rows.Close()
	}
	present := map[string]bool{}
	for _, lib := range listLibraries() {
		os.MkdirAll(lib.Path, 0o755)
		filepath.WalkDir(lib.Path, func(p string, e os.DirEntry, err error) error {
			if err != nil || ctx.Err() != nil {
				return nil
			}
			if e.IsDir() {
				// 回收站里是删掉的书，不再登记
				if p != lib.Path && (skipName(e.Name()) || e.Name() == trashDir) {
					return filepath.SkipDir
				}
				return nil
			}
			if skipName(e.Name()) {
				return nil
			}
			if _, ok := formatOf(p); !ok {
				return nil
			}
			present[p] = true
			scanState.Lock()
			scanState.Found++
			scanState.Unlock()
			if known[p] {
				return nil
			}
			scanState.Lock()
			scanState.Current = e.Name()
			scanState.Unlock()
			r := ingestFile(ctx, p, false)
			if r.Status == "new" || r.Status == "attached" || r.Status == "review" {
				scanState.Lock()
				scanState.Added++
				scanState.Unlock()
			}
			return nil
		})
	}
	if ctx.Err() != nil {
		return
	}
	// 文件已经不在了：删掉记录（只删数据库，不碰文件）
	removed := 0
	for p := range known {
		if present[p] {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			continue // 不在书库目录内但仍存在的文件（比如书库路径改过），保留
		}
		var fid, bid int64
		if db.QueryRow(`SELECT id,book_id FROM files WHERE path=?`, p).Scan(&fid, &bid) == nil {
			db.Exec(`DELETE FROM files WHERE id=?`, fid)
			var left int
			db.QueryRow(`SELECT COUNT(*) FROM files WHERE book_id=?`, bid).Scan(&left)
			if left == 0 {
				db.Exec(`DELETE FROM books WHERE id=?`, bid)
				os.Remove(coverPath(bid))
			}
			removed++
		}
	}
	scanState.Lock()
	scanState.Removed = removed
	added := scanState.Added
	scanState.Unlock()
	if added > 0 || removed > 0 {
		logEvent("info", 0, fmt.Sprintf("书库扫描完成：新增 %d 个文件，移除 %d 条失效记录", added, removed))
	}
}
