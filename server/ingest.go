package main

// 入库：投递目录监听、书库扫描、封面、搜索关键字

import (
	"bytes"
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	Status string // new | attached | moved | duplicate | review | unsupported | error
	Msg    string
}

var ingestMu sync.Mutex

// fromInbox=true：入库后按模板移动到书库；false：书库里已有的文件，原地登记
// 发现页加入书库时，书源上已知的书目信息（书名、作者、主题、简介）：下载到投递目录后按路径记下，入库时用来补全、自动分类
type ingestHint struct {
	Title, Author, Summary, Language, Source string
	Year                                     int
	Tags                                     []string
}

var ingestHints = struct {
	sync.Mutex
	m map[string]hintEntry
}{m: map[string]hintEntry{}}

type hintEntry struct {
	h  ingestHint
	at time.Time
}

func setIngestHint(p string, h ingestHint) {
	if h.Title == "" && h.Author == "" && h.Summary == "" && len(h.Tags) == 0 {
		return
	}
	ingestHints.Lock()
	defer ingestHints.Unlock()
	for k, e := range ingestHints.m {
		if time.Since(e.at) > 48*time.Hour {
			delete(ingestHints.m, k)
		}
	}
	ingestHints.m[filepath.Clean(p)] = hintEntry{h, time.Now()}
}

func takeIngestHint(p string) *ingestHint {
	ingestHints.Lock()
	defer ingestHints.Unlock()
	e, ok := ingestHints.m[filepath.Clean(p)]
	if !ok {
		return nil
	}
	delete(ingestHints.m, filepath.Clean(p))
	return &e.h
}

// 书里自带的信息优先，书源的只用来补空的；主题合在一起（自动分类、AI 识别都会参考）
func applyHint(m *Meta, h *ingestHint) {
	if m.Title == "" {
		m.Title = h.Title
	}
	if m.Author == "" {
		m.Author = h.Author
	}
	if m.Description == "" {
		m.Description = h.Summary
	}
	if m.Language == "" {
		m.Language = h.Language
	}
	if m.Year == 0 {
		m.Year = h.Year
	}
	seen := map[string]bool{}
	for _, t := range m.Tags {
		seen[strings.ToLower(t)] = true
	}
	for _, t := range h.Tags {
		if !seen[strings.ToLower(t)] {
			seen[strings.ToLower(t)] = true
			m.Tags = append(m.Tags, t)
		}
	}
}

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
	var dupID, dupBook, dupDeleted int64
	var dupPath string
	if db.QueryRow(`SELECT f.id,f.book_id,f.path,b.deleted_at FROM files f JOIN books b ON b.id=f.book_id WHERE f.hash=? ORDER BY b.deleted_at, f.id LIMIT 1`, hash).Scan(&dupID, &dupBook, &dupPath, &dupDeleted) == nil {
		_, statErr := os.Stat(dupPath)
		switch {
		case filepath.Clean(dupPath) == filepath.Clean(p):
			// 就是这个文件自己（登记过了），不用再处理
			db.Exec(`UPDATE files SET missing_since=0 WHERE id=?`, dupID)
			return IngestResult{BookID: dupBook, Status: "duplicate", Msg: "已经登记过"}
		case statErr == nil && dupDeleted == 0:
			return IngestResult{BookID: dupBook, Status: "duplicate", Msg: "书库里已有完全相同的文件：" + dupPath}
		case statErr != nil && dupDeleted == 0:
			// 原来的文件不在了，同样内容的文件出现在别处：是在 File Station 里改名、移动过。
			// 只改登记的路径，书目、每个人的进度、书签笔记都留着（以前这里会当成新书，进度全丢）
			return adoptMovedFile(dupID, dupBook, p, fromInbox)
		}
	}
	m := extractMeta(p, fi.Format)
	if fromInbox {
		if h := takeIngestHint(p); h != nil {
			applyHint(&m, h)
		}
	}
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
	var onlineCover []byte
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
			// 封面在登记之前就下载好：书出现在书库里时已经是整理完的样子，
			// 不会有一小段时间文件还在投递目录里
			if len(m.Cover) == 0 && online.Cover != "" {
				cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
				onlineCover, _ = fetchImage(cctx, online.Cover)
				cancel()
				online.Cover = "" // 下载失败也不再重试
			}
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
	if id.Title != "" && kind != "comic" {
		// 先把书名作者对得上的书目都取出来、关掉查询，再逐个看缺不缺这种格式。
		// 不能边遍历边查：以前这样写，数据库连接被占着时就互相等待，整个书库卡死
		var same []int64
		rows, err := db.Query(`SELECT id,title,author FROM books WHERE kind=? AND deleted_at=0`, kind)
		if err == nil {
			for rows.Next() {
				var bid int64
				var t, a string
				rows.Scan(&bid, &t, &a)
				if normTitle(t) == normTitle(id.Title) && normTitle(a) == normTitle(id.Author) {
					same = append(same, bid)
				}
			}
			rows.Close()
		}
		for _, bid := range same {
			var hasFmt int
			db.QueryRow(`SELECT COUNT(*) FROM files WHERE book_id=? AND format=?`, bid, fi.Format).Scan(&hasFmt)
			if hasFmt == 0 {
				bookID, attached = bid, true
				break
			}
		}
	}
	ts := now()
	if !attached {
		tags, _ := json.Marshal(nonNil(id.Tags))
		// 书目编号不复用：删掉的书的编号不会给新书（手机、浏览器里按编号记的东西不会串到别的书上）
		bookID = nextBookID()
		_, err := db.Exec(`INSERT INTO books(id,title,orig_title,author,translator,series,series_index,year,category,tags,description,isbn,language,publisher,kind,direction,status,confidence,source,orig_name,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			bookID, id.Title, id.OrigTitle, id.Author, id.Translator, id.Series, id.SeriesIndex, id.Year, id.Category, string(tags), id.Description, m.ISBN, id.Language, m.Publisher, kind, m.Direction, status, id.Confidence, id.Source, name, ts, ts)
		if err != nil {
			return IngestResult{Status: "error", Msg: err.Error()}
		}
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
		if !saveCover(bookID, m.Cover) {
			saveCover(bookID, onlineCover)
		}
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
		toReview := !confident
		if confident {
			if err := organizeBook(bookID, batch, "入库整理"); errors.Is(err, sql.ErrNoRows) {
				r.Msg = "入库过程中这本书被删除了"
			} else if err != nil {
				// 整理失败（建不了文件夹、磁盘满了……）：先放进“_待确认”，别让文件留在投递目录里
				r.Msg = "按规范整理失败（" + err.Error() + "），先放进了待确认"
				f, _ = getFile(fileID)
				toReview = f != nil && insideInbox(f.Path)
			}
		}
		if toReview && f != nil { // 入库过程中被删除时 f 是 nil，文件已经进了回收站
			// 把握不大：先放到书库的“_待确认”文件夹，能读，等你确认后再按规范整理
			to := filepath.Join(lib.Path, "_待确认", name)
			moveMu.Lock()
			err := relocate(f, to, batch, "待确认")
			moveMu.Unlock()
			if err != nil {
				// 连待确认都放不进去：撤销这次登记，文件留在投递目录，由调用方挪到 _出错
				db.Exec(`DELETE FROM files WHERE id=?`, fileID)
				if !attached {
					db.Exec(`DELETE FROM books WHERE id=?`, bookID)
					os.Remove(coverPath(bookID))
				}
				return IngestResult{Status: "error", Msg: "放不进书库：" + err.Error()}
			}
			if !confident {
				r.Status = "review"
			}
		}
	}
	return r
}

func insideInbox(p string) bool {
	return strings.HasPrefix(filepath.Clean(p), filepath.Clean(cfg.Inbox)+string(os.PathSeparator))
}

// 书目编号：一直往上加，不复用
func nextBookID() int64 {
	var maxID, seq int64
	db.QueryRow(`SELECT COALESCE(MAX(id),0) FROM books`).Scan(&maxID)
	seq, _ = strconv.ParseInt(getSetting("book_id_seq", "0"), 10, 64)
	if maxID > seq {
		seq = maxID
	}
	seq++
	setSetting("book_id_seq", strconv.FormatInt(seq, 10))
	return seq
}

// 改了名、换了位置的文件：接着用原来的书目
func adoptMovedFile(fileID, bookID int64, p string, fromInbox bool) IngestResult {
	lib, _ := insideLibrary(p)
	libID := int64(0)
	if lib != nil {
		libID = lib.ID
	}
	if _, err := db.Exec(`UPDATE files SET path=?,library_id=CASE WHEN ?>0 THEN ? ELSE library_id END,missing_since=0 WHERE id=?`, p, libID, libID, fileID); err != nil {
		return IngestResult{Status: "error", Msg: err.Error()}
	}
	r := IngestResult{BookID: bookID, Status: "moved"}
	if fromInbox {
		// 又放回投递目录的：按规范整理回书库
		if err := organizeBook(bookID, "in-"+newBatch(), "入库整理"); err != nil {
			r.Msg = "整理失败：" + err.Error()
		}
	}
	if b, _ := getBook(bookID); b != nil {
		logEvent("info", bookID, fmt.Sprintf("《%s》的文件换了位置，已更新记录（进度、书签都保留）", b.Title))
	}
	kickFulltext()
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
		case "moved":
			// 已经在 ingestFile 里记过日志
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
			if r.Msg == "已经登记过" {
				// 登记过、但还留在投递目录里（以前整理失败留下的）：再整理一次
				if err := organizeBook(r.BookID, "in-"+newBatch(), "入库整理"); err != nil {
					to := freePath(filepath.Join(cfg.Inbox, "_出错", name))
					if moveFile(p, to) == nil {
						db.Exec(`UPDATE files SET path=? WHERE path=?`, to, p)
					}
					logEvent("error", r.BookID, name+" 整理失败："+err.Error()+"，已移到投递目录的 _出错 文件夹")
				}
				break
			}
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
	type knownFile struct {
		id, lib int64
		missing int64
	}
	known := map[string]knownFile{}
	rows, err := db.Query(`SELECT f.id,f.path,f.library_id,f.missing_since FROM files f JOIN books b ON b.id=f.book_id WHERE b.deleted_at=0`)
	if err == nil {
		for rows.Next() {
			var k knownFile
			var p string
			rows.Scan(&k.id, &p, &k.lib, &k.missing)
			known[p] = k
		}
		rows.Close()
	}
	present := map[string]bool{}
	// 这次扫描不可信的书库：根目录不在、读不了，或者一本书都没找到而库里登记了不少
	unreliable := map[int64]string{}
	for _, lib := range listLibraries() {
		if st, err := os.Stat(lib.Path); err != nil || !st.IsDir() {
			unreliable[lib.ID] = "找不到书库目录 " + lib.Path
			continue
		}
		found := 0
		walkErr := filepath.WalkDir(lib.Path, func(p string, e os.DirEntry, err error) error {
			if err != nil {
				if p == lib.Path {
					return err // 根目录都读不了
				}
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
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
			found++
			present[p] = true
			scanState.Lock()
			scanState.Found++
			scanState.Unlock()
			if k, ok := known[p]; ok {
				if k.missing > 0 { // 找不到的文件又出现了
					db.Exec(`UPDATE files SET missing_since=0 WHERE id=?`, k.id)
				}
				return nil
			}
			scanState.Lock()
			scanState.Current = e.Name()
			scanState.Unlock()
			r := ingestFile(ctx, p, false)
			if r.Status == "new" || r.Status == "attached" || r.Status == "review" || r.Status == "moved" {
				scanState.Lock()
				scanState.Added++
				scanState.Unlock()
			}
			return nil
		})
		if ctx.Err() != nil {
			return
		}
		if walkErr != nil {
			unreliable[lib.ID] = "读不了书库目录 " + lib.Path + "：" + walkErr.Error()
			continue
		}
		registered := 0
		for _, k := range known {
			if k.lib == lib.ID && k.missing == 0 {
				registered++
			}
		}
		if found == 0 && registered >= 3 {
			unreliable[lib.ID] = fmt.Sprintf("书库目录 %s 里一本书都没找到（登记了 %d 个文件），可能是共享文件夹没挂上", lib.Path, registered)
		}
	}
	for _, why := range unreliable {
		logEvent("warn", 0, why+"。这次不清理找不到的书，等目录恢复后再扫描")
	}
	// 文件不在了：先标记“找不到”（书还在书架上，标着找不到文件），30 天后还没找回来才真正删掉。
	// 改名、移动过的文件在上面登记新路径时已经接回原来的书目了
	marked := 0
	for p, k := range known {
		if present[p] || k.missing > 0 {
			continue
		}
		if _, bad := unreliable[k.lib]; bad {
			continue
		}
		if lib, ok := insideLibrary(p); ok {
			if _, bad := unreliable[lib.ID]; bad {
				continue
			}
		}
		if _, err := os.Stat(p); err == nil {
			continue // 不在书库目录内但仍存在的文件（比如书库路径改过），保留
		}
		var cur string
		if db.QueryRow(`SELECT path FROM files WHERE id=?`, k.id).Scan(&cur) == nil && cur != p {
			continue // 扫描过程中已经接到新路径上了
		}
		db.Exec(`UPDATE files SET missing_since=? WHERE id=? AND missing_since=0`, now(), k.id)
		marked++
	}
	scanState.Lock()
	scanState.Removed = marked
	added := scanState.Added
	scanState.Unlock()
	if added > 0 || marked > 0 {
		msg := fmt.Sprintf("书库扫描完成：新增 %d 个文件", added)
		if marked > 0 {
			msg += fmt.Sprintf("，%d 个文件找不到了（30 天内找回来会自动恢复）", marked)
		}
		logEvent("info", 0, msg)
		invalidateSuggestions()
	}
}
