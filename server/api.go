package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"io"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

var appCtx context.Context = context.Background()

// 删除的书移到书库里的这个文件夹，扫描时跳过
const trashDir = "_回收站"

func routes() http.Handler {
	mux := http.NewServeMux()
	a := requireAuth
	mux.HandleFunc("GET /api/ping", handlePing)
	// 在线升级：各端安装包（不用登录）、通知更新服务
	mux.HandleFunc("GET /api/clients", handleClients)
	mux.HandleFunc("GET /dl/{name}", handleDownloadClient)
	mux.HandleFunc("GET /api/dict", a(handleDict))
	mux.HandleFunc("POST /api/dict/explain", a(handleDictExplain))
	mux.HandleFunc("POST /api/dict/speak", a(handleDictSpeak))
	mux.HandleFunc("GET /api/update", a(handleUpdateInfo))
	mux.HandleFunc("POST /api/update/check", a(handleUpdateCheck))
	mux.HandleFunc("GET /api/auth/state", handleAuthState)
	mux.HandleFunc("POST /api/auth/setup", handleSetup)
	mux.HandleFunc("POST /api/auth/login", handleLogin)
	mux.HandleFunc("POST /api/auth/logout", a(handleLogout))
	mux.HandleFunc("POST /api/auth/password", a(handleChangePassword))
	mux.HandleFunc("POST /api/auth/handoff", a(handleHandoff))
	mux.HandleFunc("POST /api/auth/exchange", handleExchange)

	mux.HandleFunc("GET /api/books", a(handleBooks))
	mux.HandleFunc("GET /api/facets", a(handleFacets))
	mux.HandleFunc("GET /api/books/{id}", a(handleBook))
	mux.HandleFunc("PATCH /api/books/{id}", a(handleUpdateBook))
	mux.HandleFunc("DELETE /api/books/{id}", a(handleDeleteBook))
	mux.HandleFunc("POST /api/books/{id}/confirm", a(handleConfirm))
	mux.HandleFunc("POST /api/books/{id}/identify", a(handleIdentify))
	mux.HandleFunc("GET /api/books/{id}/cover", a(handleCover))
	mux.HandleFunc("PUT /api/books/{id}/cover", a(handlePutCover))
	mux.HandleFunc("GET /api/books/{id}/read", a(handleRead))
	mux.HandleFunc("GET /api/books/{id}/progress", a(handleGetProgress))
	mux.HandleFunc("PUT /api/books/{id}/progress", a(handlePutProgress))
	mux.HandleFunc("GET /api/books/{id}/bookmarks", a(handleBookmarks))
	mux.HandleFunc("POST /api/books/{id}/bookmarks", a(handleAddBookmark))
	mux.HandleFunc("DELETE /api/bookmarks/{bid}", a(handleDeleteBookmark))

	mux.HandleFunc("GET /api/files/{fid}/raw", a(handleRaw))
	mux.HandleFunc("GET /api/files/{fid}/epub", a(handleConvertedEPUB))
	mux.HandleFunc("GET /api/files/{fid}/pdf", a(handleConvertedPDF))
	mux.HandleFunc("GET /api/files/{fid}/pages", a(handlePages))
	mux.HandleFunc("GET /api/files/{fid}/pages/{n}", a(handlePage))

	mux.HandleFunc("POST /api/upload", a(handleUpload))
	mux.HandleFunc("GET /api/status", a(handleStatus))
	mux.HandleFunc("POST /api/scan", a(handleScan))
	mux.HandleFunc("GET /api/events", a(handleEvents))
	mux.HandleFunc("GET /api/suggestions", a(handleSuggestions))
	mux.HandleFunc("POST /api/suggestions/apply", a(handleApplySuggestion))
	mux.HandleFunc("POST /api/suggestions/ignore", a(handleIgnoreSuggestion))
	mux.HandleFunc("GET /api/ops", a(handleOps))
	mux.HandleFunc("POST /api/ops/{batch}/undo", a(handleUndo))
	mux.HandleFunc("GET /api/libraries", a(handleLibraries))
	mux.HandleFunc("POST /api/libraries", a(handleAddLibrary))
	mux.HandleFunc("PUT /api/libraries/{id}", a(handleUpdateLibrary))
	mux.HandleFunc("DELETE /api/libraries/{id}", a(handleDeleteLibrary))
	mux.HandleFunc("GET /api/settings", a(handleGetSettings))
	mux.HandleFunc("PUT /api/settings", a(handlePutSettings))
	mux.HandleFunc("POST /api/ai/test", a(handleAITest))
	mux.HandleFunc("GET /api/preview-template", a(handlePreviewTemplate))

	routesP1(mux)
	routesP2(mux)
	mux.HandleFunc("/", handleStatic)
	return withCORS(mux)
}

// 内网地址和 Tailscale 地址是两个“来源”，网页从一个地址打开、请求发往另一个地址时需要 CORS
func withCORS(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if o := r.Header.Get("Origin"); o != "" && strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Access-Control-Allow-Origin", o)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Range, X-Device")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Expose-Headers", "Content-Range, Content-Length, Accept-Ranges, X-Page-Width, X-Page-Height")
			w.Header().Set("Access-Control-Max-Age", "86400")
			if r.Method == http.MethodOptions {
				w.WriteHeader(204)
				return
			}
		}
		h.ServeHTTP(w, r)
	})
}

func pathID(r *http.Request, name string) int64 {
	id, _ := strconv.ParseInt(r.PathValue(name), 10, 64)
	return id
}

// ---------- 书目 ----------

type BookListItem struct {
	ID          int64   `json:"id"`
	Title       string  `json:"title"`
	Author      string  `json:"author"`
	Series      string  `json:"series"`
	SeriesIndex float64 `json:"series_index"`
	Category    string  `json:"category"`
	Kind        string  `json:"kind"`
	Status      string  `json:"status"`
	Cover       string  `json:"cover"`
	ReadState   string  `json:"read_state"`
	Percent     float64 `json:"percent"`
	LastRead    int64   `json:"last_read"`
	CreatedAt   int64   `json:"created_at"`
	Formats     string  `json:"formats"`
	Confidence  float64 `json:"confidence"`
}

func handleBooks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var where []string
	var args []any
	if s := strings.TrimSpace(q.Get("q")); s != "" {
		for _, term := range strings.Fields(toSimplified(strings.ToLower(s))) {
			where = append(where, `b.search_key LIKE ?`)
			args = append(args, "%"+term+"%")
		}
	}
	eq := func(param, col string) {
		if v := q.Get(param); v != "" {
			where = append(where, col+`=?`)
			args = append(args, v)
		}
	}
	eq("kind", "b.kind")
	eq("category", "b.category")
	eq("state", "b.read_state")
	eq("status", "b.status")
	eq("author", "b.author")
	eq("series", "b.series")
	if v := q.Get("format"); v != "" {
		where = append(where, `EXISTS(SELECT 1 FROM files f WHERE f.book_id=b.id AND f.format=?)`)
		args = append(args, v)
	}
	if v := q.Get("library"); v != "" {
		where = append(where, `EXISTS(SELECT 1 FROM files f WHERE f.book_id=b.id AND f.library_id=?)`)
		args = append(args, v)
	}
	if q.Get("reading") == "1" {
		where = append(where, `COALESCE(p.updated_at,0)>0 AND b.read_state<>'done'`)
	}
	order := `COALESCE(p.updated_at,0) DESC, b.created_at DESC`
	switch q.Get("sort") {
	case "added":
		order = `b.created_at DESC, b.id DESC`
	case "title":
		order = `b.sort_key, b.series_index`
	case "author":
		order = `b.author_key, b.sort_key`
	case "series":
		order = `b.series='' , b.series, b.series_index, b.sort_key`
	}
	w2 := ""
	if len(where) > 0 {
		w2 = " WHERE " + strings.Join(where, " AND ")
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 120
	}
	offset, _ := strconv.Atoi(q.Get("offset"))
	var total int
	db.QueryRow(`SELECT COUNT(*) FROM books b LEFT JOIN progress p ON p.book_id=b.id`+w2, args...).Scan(&total)
	rows, err := db.Query(`SELECT b.id,b.title,b.author,b.series,b.series_index,b.category,b.kind,b.status,b.cover,b.read_state,COALESCE(p.percent,0),COALESCE(p.updated_at,0),b.created_at,b.confidence,
		COALESCE((SELECT GROUP_CONCAT(DISTINCT f.format) FROM files f WHERE f.book_id=b.id),'')
		FROM books b LEFT JOIN progress p ON p.book_id=b.id`+w2+` ORDER BY `+order+` LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	defer rows.Close()
	items := []BookListItem{}
	for rows.Next() {
		var b BookListItem
		rows.Scan(&b.ID, &b.Title, &b.Author, &b.Series, &b.SeriesIndex, &b.Category, &b.Kind, &b.Status, &b.Cover, &b.ReadState, &b.Percent, &b.LastRead, &b.CreatedAt, &b.Confidence, &b.Formats)
		items = append(items, b)
	}
	writeJSON(w, 200, map[string]any{"total": total, "items": items})
}

func countBy(query string) []map[string]any {
	rows, err := db.Query(query)
	out := []map[string]any{}
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var n int
		rows.Scan(&k, &n)
		out = append(out, map[string]any{"name": k, "count": n})
	}
	return out
}

func handleFacets(w http.ResponseWriter, r *http.Request) {
	var total, review, reading int
	db.QueryRow(`SELECT COUNT(*) FROM books`).Scan(&total)
	db.QueryRow(`SELECT COUNT(*) FROM books WHERE status='review'`).Scan(&review)
	db.QueryRow(`SELECT COUNT(*) FROM books b JOIN progress p ON p.book_id=b.id WHERE b.read_state<>'done'`).Scan(&reading)
	writeJSON(w, 200, map[string]any{
		"total":       total,
		"review":      review,
		"reading":     reading,
		"suggestions": len(cachedSuggestions(false)),
		"kinds":       countBy(`SELECT kind,COUNT(*) FROM books GROUP BY kind`),
		"categories":  countBy(`SELECT category,COUNT(*) FROM books WHERE category<>'' GROUP BY category ORDER BY COUNT(*) DESC`),
		"formats":     countBy(`SELECT format,COUNT(DISTINCT book_id) FROM files GROUP BY format ORDER BY COUNT(*) DESC`),
		"states":      countBy(`SELECT read_state,COUNT(*) FROM books GROUP BY read_state`),
		"libraries":   countBy(`SELECT CAST(library_id AS TEXT),COUNT(DISTINCT book_id) FROM files GROUP BY library_id`),
	})
}

func handleBook(w http.ResponseWriter, r *http.Request) {
	b, err := getBook(pathID(r, "id"))
	if err != nil {
		errJSON(w, 404, "找不到这本书")
		return
	}
	for i := range b.Files {
		b.Files[i].Path = relToLib(b.Files[i].Path)
	}
	writeJSON(w, 200, b)
}

func handleUpdateBook(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	b, err := getBook(id)
	if err != nil {
		errJSON(w, 404, "找不到这本书")
		return
	}
	var req map[string]any
	if readJSON(r, &req) != nil {
		errJSON(w, 400, "请求格式不正确")
		return
	}
	str := func(k string, dst *string) {
		if v, ok := req[k].(string); ok {
			*dst = strings.TrimSpace(v)
		}
	}
	str("title", &b.Title)
	str("orig_title", &b.OrigTitle)
	str("author", &b.Author)
	str("translator", &b.Translator)
	str("series", &b.Series)
	str("category", &b.Category)
	str("description", &b.Description)
	str("isbn", &b.ISBN)
	str("publisher", &b.Publisher)
	str("language", &b.Language)
	str("direction", &b.Direction)
	str("read_state", &b.ReadState)
	str("kind", &b.Kind)
	if v, ok := req["series_index"].(float64); ok {
		b.SeriesIndex = v
	}
	if v, ok := req["year"].(float64); ok {
		b.Year = int(v)
	}
	if v, ok := req["tags"].([]any); ok {
		b.Tags = []string{}
		for _, t := range v {
			if s, ok := t.(string); ok && strings.TrimSpace(s) != "" {
				b.Tags = append(b.Tags, strings.TrimSpace(s))
			}
		}
	}
	if b.Title == "" {
		errJSON(w, 400, "书名不能为空")
		return
	}
	confirm, _ := req["confirm"].(bool)
	if confirm {
		b.Status, b.Confidence, b.Source = "ok", 1, "人工确认"
	}
	tags, _ := json.Marshal(nonNil(b.Tags))
	db.Exec(`UPDATE books SET title=?,orig_title=?,author=?,translator=?,series=?,series_index=?,year=?,category=?,tags=?,description=?,isbn=?,publisher=?,language=?,direction=?,read_state=?,kind=?,status=?,confidence=?,source=?,updated_at=? WHERE id=?`,
		b.Title, b.OrigTitle, b.Author, b.Translator, b.Series, b.SeriesIndex, b.Year, b.Category, string(tags), b.Description, b.ISBN, b.Publisher, b.Language, b.Direction, b.ReadState, b.Kind, b.Status, b.Confidence, b.Source, now(), id)
	updateSearchKey(id)
	if b.ReadState == "done" {
		db.Exec(`UPDATE books SET finished_at=? WHERE id=? AND finished_at=0`, now(), id)
	} else {
		db.Exec(`UPDATE books SET finished_at=0 WHERE id=?`, id)
	}
	msg := ""
	// 已确认的书改了信息，同步改文件名和位置
	reorg, has := req["organize"].(bool)
	if (has && reorg) || (!has && b.Status == "ok") {
		if err := organizeBook(id, "ed-"+newBatch(), "修改信息后整理"); err != nil {
			msg = "信息已保存，但移动文件失败：" + err.Error()
		}
	}
	invalidateSuggestions()
	nb, _ := getBook(id)
	for i := range nb.Files {
		nb.Files[i].Path = relToLib(nb.Files[i].Path)
	}
	writeJSON(w, 200, map[string]any{"book": nb, "message": msg})
}

func handleConfirm(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	db.Exec(`UPDATE books SET status='ok',confidence=1,source=CASE WHEN source='' THEN '人工确认' ELSE source||' · 已确认' END,updated_at=? WHERE id=?`, now(), id)
	err := organizeBook(id, "cf-"+newBatch(), "确认后整理")
	invalidateSuggestions()
	if err != nil {
		errJSON(w, 500, "已确认，但移动文件失败："+err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func handleIdentify(w http.ResponseWriter, r *http.Request) {
	b, err := getBook(pathID(r, "id"))
	if err != nil || len(b.Files) == 0 {
		errJSON(w, 404, "找不到这本书")
		return
	}
	f := b.Files[0]
	m := extractMeta(f.Path, f.Format)
	name := b.OrigName
	if name == "" {
		name = filepath.Base(f.Path)
	}
	id := ruleIdentify(name, m, b.Kind)
	if aiReady() {
		aid, err := aiIdentify(r.Context(), name, m, b.Kind, id)
		if err != nil {
			errJSON(w, 502, err.Error())
			return
		}
		id = aid
	}
	writeJSON(w, 200, id)
}

func handleDeleteBook(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	b, err := getBook(id)
	if err != nil {
		errJSON(w, 404, "找不到这本书")
		return
	}
	// 不真正删除文件：移到书库的 _回收站，需要时可以从操作记录撤销
	batch := "rm-" + newBatch()
	moveMu.Lock()
	for i := range b.Files {
		f := &b.Files[i]
		lib, ok := insideLibrary(f.Path)
		if !ok {
			lib = libraryByID(f.LibraryID)
		}
		if lib != nil {
			relocate(f, filepath.Join(lib.Path, trashDir, filepath.Base(f.Path)), batch, "删除《"+b.Title+"》")
		}
	}
	moveMu.Unlock()
	db.Exec(`DELETE FROM books WHERE id=?`, id)
	os.Remove(coverPath(id))
	invalidateSuggestions()
	logEvent("info", 0, fmt.Sprintf("已删除《%s》，文件移到了 _回收站", b.Title))
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func handleCover(w http.ResponseWriter, r *http.Request) {
	if wd, _ := strconv.Atoi(r.URL.Query().Get("w")); wd > 0 && wd < 480 {
		handleCoverSized(w, r, pathID(r, "id"), wd)
		return
	}
	p := coverPath(pathID(r, "id"))
	if _, err := os.Stat(p); err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=2592000")
	http.ServeFile(w, r, p)
}

func handlePutCover(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(io.LimitReader(r.Body, 20<<20))
	if !saveCover(pathID(r, "id"), b) {
		errJSON(w, 400, "图片无法识别")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---------- 阅读 ----------

func handleRead(w http.ResponseWriter, r *http.Request) {
	b, err := getBook(pathID(r, "id"))
	if err != nil || len(b.Files) == 0 {
		errJSON(w, 404, "找不到这本书")
		return
	}
	f := b.Files[0]
	if fid, _ := strconv.ParseInt(r.URL.Query().Get("file"), 10, 64); fid > 0 {
		for _, x := range b.Files {
			if x.ID == fid {
				f = x
			}
		}
	} else {
		// 上次读的是哪个格式就继续用哪个
		var last int64
		if db.QueryRow(`SELECT file_id FROM progress WHERE book_id=?`, b.ID).Scan(&last) == nil {
			for _, x := range b.Files {
				if x.ID == last {
					f = x
				}
			}
		}
	}
	reader, url := f.Reader, fmt.Sprintf("/api/files/%d/raw", f.ID)
	switch f.Reader {
	case "epub-converted":
		reader, url = "foliate", fmt.Sprintf("/api/files/%d/epub", f.ID)
		if f.Format == "txt" {
			// 带上分章规则的版本：规则改了以后，浏览器缓存、手机上下载过的旧版本就不会再用
			url += fmt.Sprintf("?v=%d", txtSplitVersion)
		}
	case "pdf-converted":
		reader, url = "pdf", fmt.Sprintf("/api/files/%d/pdf", f.ID)
	}
	// 固定版式的 EPUB / PDF 漫画仍然用各自的阅读器
	name := safeName(b.Title)
	ext := f.Format
	if reader == "foliate" && f.Reader == "epub-converted" {
		ext = "epub"
	}
	formats := []map[string]any{}
	for _, x := range b.Files {
		formats = append(formats, map[string]any{"id": x.ID, "format": x.Format, "size": x.Size})
	}
	writeJSON(w, 200, map[string]any{
		"file_id": f.ID, "format": f.Format, "reader": reader, "url": url,
		"name": name + "." + ext, "title": b.Title, "author": b.Author,
		"kind": b.Kind, "direction": b.Direction, "has_cover": b.Cover != "",
		"files": formats,
	})
}

var contentTypes = map[string]string{
	"epub": "application/epub+zip", "pdf": "application/pdf", "mobi": "application/x-mobipocket-ebook",
	"azw": "application/vnd.amazon.ebook", "azw3": "application/vnd.amazon.ebook", "fb2": "application/x-fictionbook+xml",
	"fbz": "application/x-zip-compressed-fb2", "cbz": "application/vnd.comicbook+zip", "cbr": "application/vnd.comicbook-rar",
	"txt": "text/plain; charset=utf-8", "djvu": "image/vnd.djvu",
}

func serveFile(w http.ResponseWriter, r *http.Request, p, format, downloadName string) {
	f, err := os.Open(p)
	if err != nil {
		errJSON(w, 404, "文件不存在，可能已被移动，请重新扫描书库")
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	if ct, ok := contentTypes[format]; ok {
		w.Header().Set("Content-Type", ct)
	}
	if downloadName != "" {
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": downloadName}))
	}
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.ServeContent(w, r, "", st.ModTime(), f)
}

func handleRaw(w http.ResponseWriter, r *http.Request) {
	f, err := getFile(pathID(r, "fid"))
	if err != nil {
		errJSON(w, 404, "文件不存在")
		return
	}
	dl := ""
	if r.URL.Query().Get("download") == "1" {
		dl = filepath.Base(f.Path)
	}
	serveFile(w, r, f.Path, f.Format, dl)
}

func handleConvertedEPUB(w http.ResponseWriter, r *http.Request) {
	f, err := getFile(pathID(r, "fid"))
	if err != nil {
		errJSON(w, 404, "文件不存在")
		return
	}
	b, _ := getBook(f.BookID)
	title, author := filepath.Base(f.Path), ""
	if b != nil {
		title, author = b.Title, b.Author
	}
	out, err := ensureEPUB(f.Path, f.Format, title, author)
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	serveFile(w, r, out, "epub", "")
}

func handleConvertedPDF(w http.ResponseWriter, r *http.Request) {
	f, err := getFile(pathID(r, "fid"))
	if err != nil {
		errJSON(w, 404, "文件不存在")
		return
	}
	out, err := ensurePDF(f.Path)
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	serveFile(w, r, out, "pdf", "")
}

// 漫画页列表缓存（页名 + 每页宽高，前端据此判断跨页大图）
type comicInfo struct {
	names []string
	dims  [][2]int
}

var pageCache = struct {
	sync.Mutex
	m map[int64]*comicInfo
}{m: map[int64]*comicInfo{}}

func comicPagesInfo(f *File) (*comicInfo, error) {
	pageCache.Lock()
	if p, ok := pageCache.m[f.ID]; ok {
		pageCache.Unlock()
		return p, nil
	}
	pageCache.Unlock()
	a, err := pooledArchive(f)
	if err != nil {
		return nil, err
	}
	arcReadMu.Lock()
	imgs := images(a)
	info := &comicInfo{names: imgs, dims: make([][2]int, len(imgs))}
	for i, n := range imgs {
		if b, err := a.Read(n); err == nil {
			if c, _, err := image.DecodeConfig(bytes.NewReader(b)); err == nil {
				info.dims[i] = [2]int{c.Width, c.Height}
			}
		}
	}
	arcReadMu.Unlock()
	pageCache.Lock()
	if len(pageCache.m) > 200 {
		pageCache.m = map[int64]*comicInfo{}
	}
	pageCache.m[f.ID] = info
	pageCache.Unlock()
	return info, nil
}

func comicPages(f *File) ([]string, error) {
	info, err := comicPagesInfo(f)
	if err != nil {
		return nil, err
	}
	return info.names, nil
}

func handlePages(w http.ResponseWriter, r *http.Request) {
	f, err := getFile(pathID(r, "fid"))
	if err != nil {
		errJSON(w, 404, "文件不存在")
		return
	}
	info, err := comicPagesInfo(f)
	if err != nil {
		errJSON(w, 500, "打开压缩包失败："+err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"count": len(info.names), "dims": info.dims})
}

// 保持最近打开的几个压缩包，避免每页都重新打开
var arcPool = struct {
	sync.Mutex
	m     map[int64]Archive
	order []int64
}{m: map[int64]Archive{}}

func pooledArchive(f *File) (Archive, error) {
	arcPool.Lock()
	defer arcPool.Unlock()
	if a, ok := arcPool.m[f.ID]; ok {
		return a, nil
	}
	a, err := openArchive(f.Path, f.Format)
	if err != nil {
		return nil, err
	}
	arcPool.m[f.ID] = a
	arcPool.order = append(arcPool.order, f.ID)
	if len(arcPool.order) > 6 {
		old := arcPool.order[0]
		arcPool.order = arcPool.order[1:]
		if oa, ok := arcPool.m[old]; ok {
			oa.Close()
			delete(arcPool.m, old)
		}
	}
	return a, nil
}

var arcReadMu sync.Mutex

func handlePage(w http.ResponseWriter, r *http.Request) {
	f, err := getFile(pathID(r, "fid"))
	if err != nil {
		errJSON(w, 404, "文件不存在")
		return
	}
	n, _ := strconv.Atoi(r.PathValue("n"))
	pages, err := comicPages(f)
	if err != nil || n < 0 || n >= len(pages) {
		errJSON(w, 404, "没有这一页")
		return
	}
	width, _ := strconv.Atoi(r.URL.Query().Get("w"))
	w.Header().Set("Cache-Control", "private, max-age=604800")
	var cachePath string
	if width > 0 {
		width = (width + 199) / 200 * 200 // 取整，提高缓存命中
		cachePath = filepath.Join(cfg.DataDir, "cache", "pages", cacheKey(f.Path), fmt.Sprintf("%d_%d.jpg", n, width))
		if _, err := os.Stat(cachePath); err == nil {
			w.Header().Set("Content-Type", "image/jpeg")
			http.ServeFile(w, r, cachePath)
			return
		}
	}
	a, err := pooledArchive(f)
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	arcReadMu.Lock()
	data, err := a.Read(pages[n])
	arcReadMu.Unlock()
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	if width > 0 {
		if out, err := resizeImage(data, width, 86); err == nil {
			os.MkdirAll(filepath.Dir(cachePath), 0o755)
			os.WriteFile(cachePath, out, 0o644)
			w.Header().Set("Content-Type", "image/jpeg")
			w.Write(out)
			return
		}
	}
	ct := mime.TypeByExtension(strings.ToLower(filepath.Ext(pages[n])))
	if ct == "" {
		ct = http.DetectContentType(data)
	}
	w.Header().Set("Content-Type", ct)
	w.Write(data)
}

// ---------- 进度、书签 ----------

func handleGetProgress(w http.ResponseWriter, r *http.Request) {
	var p struct {
		FileID    int64   `json:"file_id"`
		Location  string  `json:"location"`
		Percent   float64 `json:"percent"`
		Device    string  `json:"device"`
		UpdatedAt int64   `json:"updated_at"`
	}
	db.QueryRow(`SELECT file_id,location,percent,device,updated_at FROM progress WHERE book_id=?`, pathID(r, "id")).Scan(&p.FileID, &p.Location, &p.Percent, &p.Device, &p.UpdatedAt)
	writeJSON(w, 200, p)
}

func handlePutProgress(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	var req struct {
		FileID   int64   `json:"file_id"`
		Location string  `json:"location"`
		Percent  float64 `json:"percent"`
		Device   string  `json:"device"`
		Seconds  int     `json:"seconds"` // 距上次上报实际阅读的秒数
	}
	if readJSON(r, &req) != nil {
		errJSON(w, 400, "格式不正确")
		return
	}
	ts := now()
	addReadingTime(id, req.Seconds)
	db.Exec(`INSERT INTO progress(book_id,file_id,location,percent,device,updated_at) VALUES(?,?,?,?,?,?)
		ON CONFLICT(book_id) DO UPDATE SET file_id=excluded.file_id,location=excluded.location,percent=excluded.percent,device=excluded.device,updated_at=excluded.updated_at`,
		id, req.FileID, req.Location, req.Percent, req.Device, ts)
	state := "reading"
	if req.Percent >= 0.995 {
		state = "done"
	}
	db.Exec(`UPDATE books SET read_state=? WHERE id=? AND (read_state<>'done' OR ?='done')`, state, id, state)
	if state == "done" {
		db.Exec(`UPDATE books SET finished_at=? WHERE id=? AND finished_at=0`, ts, id)
	}
	writeJSON(w, 200, map[string]int64{"updated_at": ts})
}

type Bookmark struct {
	ID        int64  `json:"id"`
	Location  string `json:"location"`
	Label     string `json:"label"`
	Text      string `json:"text"`
	Note      string `json:"note"`
	Color     string `json:"color"`
	CreatedAt int64  `json:"created_at"`
}

func handleBookmarks(w http.ResponseWriter, r *http.Request) {
	rows, err := db.Query(`SELECT id,location,label,text,note,color,created_at FROM bookmarks WHERE book_id=? ORDER BY id`, pathID(r, "id"))
	out := []Bookmark{}
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var b Bookmark
			rows.Scan(&b.ID, &b.Location, &b.Label, &b.Text, &b.Note, &b.Color, &b.CreatedAt)
			out = append(out, b)
		}
	}
	writeJSON(w, 200, out)
}

func handleAddBookmark(w http.ResponseWriter, r *http.Request) {
	var b Bookmark
	if readJSON(r, &b) != nil || b.Location == "" {
		errJSON(w, 400, "格式不正确")
		return
	}
	res, err := db.Exec(`INSERT INTO bookmarks(book_id,location,label,text,note,color,created_at) VALUES(?,?,?,?,?,?,?)`, pathID(r, "id"), b.Location, b.Label, truncate(b.Text, 2000), b.Note, b.Color, now())
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	b.ID, _ = res.LastInsertId()
	writeJSON(w, 200, b)
}

func handleDeleteBookmark(w http.ResponseWriter, r *http.Request) {
	db.Exec(`DELETE FROM bookmarks WHERE id=?`, pathID(r, "bid"))
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---------- 上传 ----------

// 每个文件一个请求：POST /api/upload?path=文件夹/子文件夹/书名.epub，请求体是文件内容
func handleUpload(w http.ResponseWriter, r *http.Request) {
	rel := filepath.ToSlash(strings.TrimSpace(r.URL.Query().Get("path")))
	var parts []string
	for _, s := range strings.Split(rel, "/") {
		s = strings.TrimSpace(s)
		if s == "" || s == "." || s == ".." {
			continue
		}
		parts = append(parts, safeName(s))
	}
	if len(parts) == 0 {
		errJSON(w, 400, "缺少文件名")
		return
	}
	name := parts[len(parts)-1]
	if _, ok := formatOf(name); !ok {
		errJSON(w, 415, "不支持的文件类型："+filepath.Ext(name))
		return
	}
	dir := filepath.Join(append([]string{cfg.Inbox}, parts[:len(parts)-1]...)...)
	os.MkdirAll(dir, 0o755)
	tmp := filepath.Join(dir, ".upload-"+randToken(6))
	out, err := os.Create(tmp)
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	n, err := io.Copy(out, r.Body)
	out.Close()
	if err != nil {
		os.Remove(tmp)
		errJSON(w, 500, "上传中断："+err.Error())
		return
	}
	final := freePath(filepath.Join(dir, name))
	if err := os.Rename(tmp, final); err != nil {
		os.Remove(tmp)
		errJSON(w, 500, err.Error())
		return
	}
	// 让文件“看起来”早已复制完，投递监听可以马上处理
	old := time.Now().Add(-10 * time.Second)
	os.Chtimes(final, old, old)
	kickInbox()
	writeJSON(w, 200, map[string]any{"ok": true, "size": n})
}

// ---------- 状态、事件、扫描 ----------

func handleStatus(w http.ResponseWriter, r *http.Request) {
	scanState.Lock()
	sc := map[string]any{"running": scanState.Running, "found": scanState.Found, "added": scanState.Added, "removed": scanState.Removed, "last": scanState.Last, "current": scanState.Current}
	scanState.Unlock()
	var lastEvent int64
	db.QueryRow(`SELECT COALESCE(MAX(id),0) FROM events`).Scan(&lastEvent)
	writeJSON(w, 200, map[string]any{"inbox": inboxStatus(), "scan": sc, "last_event": lastEvent, "ai": aiReady(), "enrich": enrichStatus(), "fulltext": fulltextStatus(), "version": version})
}

func handleScan(w http.ResponseWriter, r *http.Request) {
	kickScan()
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func handleEvents(w http.ResponseWriter, r *http.Request) {
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	rows, err := db.Query(`SELECT id,level,message,book_id,created_at FROM events WHERE id>? ORDER BY id DESC LIMIT 200`, since)
	out := []map[string]any{}
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var id, bid, ts int64
			var lv, msg string
			rows.Scan(&id, &lv, &msg, &bid, &ts)
			out = append(out, map[string]any{"id": id, "level": lv, "message": msg, "book_id": bid, "created_at": ts})
		}
	}
	writeJSON(w, 200, out)
}

// ---------- 整理建议 ----------

var sugCache = struct {
	sync.Mutex
	at   time.Time
	list []Suggestion
}{}

func invalidateSuggestions() {
	sugCache.Lock()
	sugCache.at = time.Time{}
	sugCache.Unlock()
}

func cachedSuggestions(fresh bool) []Suggestion {
	sugCache.Lock()
	defer sugCache.Unlock()
	if fresh || time.Since(sugCache.at) > 2*time.Minute {
		sugCache.list = computeSuggestions()
		sugCache.at = time.Now()
	}
	return sugCache.list
}

func handleSuggestions(w http.ResponseWriter, r *http.Request) {
	list := cachedSuggestions(r.URL.Query().Get("fresh") == "1")
	if list == nil {
		list = []Suggestion{}
	}
	writeJSON(w, 200, list)
}

func handleApplySuggestion(w http.ResponseWriter, r *http.Request) {
	var req struct{ Key string }
	readJSON(r, &req)
	for _, s := range cachedSuggestions(true) {
		if s.Key == req.Key {
			batch := "sg-" + newBatch()
			n, err := s.apply(batch)
			invalidateSuggestions()
			msg := fmt.Sprintf("已处理：%s（%d 项）", s.Title, n)
			if err != nil {
				msg += "，部分失败：" + err.Error()
			}
			logEvent("info", 0, msg)
			writeJSON(w, 200, map[string]any{"ok": err == nil, "count": n, "batch": batch, "message": msg})
			return
		}
	}
	errJSON(w, 404, "这条建议已经不存在了，可能已被处理")
}

func handleIgnoreSuggestion(w http.ResponseWriter, r *http.Request) {
	var req struct{ Key string }
	readJSON(r, &req)
	db.Exec(`INSERT OR IGNORE INTO ignored_suggestions(key,created_at) VALUES(?,?)`, req.Key, now())
	invalidateSuggestions()
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func handleOps(w http.ResponseWriter, r *http.Request) {
	list := listBatches(100)
	for i := range list {
		list[i].Sample = relToLib(list[i].Sample)
	}
	writeJSON(w, 200, list)
}

func handleUndo(w http.ResponseWriter, r *http.Request) {
	n, err := undoBatch(r.PathValue("batch"))
	invalidateSuggestions()
	msg := fmt.Sprintf("已撤销 %d 个文件的移动", n)
	if err != nil {
		msg += "；" + err.Error()
	}
	logEvent("info", 0, msg)
	writeJSON(w, 200, map[string]any{"count": n, "message": msg})
}

// ---------- 书库、设置 ----------

func handleLibraries(w http.ResponseWriter, r *http.Request) {
	out := []map[string]any{}
	for _, l := range listLibraries() {
		var n int
		db.QueryRow(`SELECT COUNT(DISTINCT book_id) FROM files WHERE library_id=?`, l.ID).Scan(&n)
		_, err := os.Stat(l.Path)
		out = append(out, map[string]any{"id": l.ID, "name": l.Name, "path": l.Path, "kind": l.Kind, "template": l.Template, "count": n, "exists": err == nil})
	}
	writeJSON(w, 200, out)
}

func validLibPath(p string) (string, error) {
	p = filepath.Clean(strings.TrimSpace(p))
	if p == "" || p == "." || p == "/" {
		return "", fmt.Errorf("请填写书库的完整路径")
	}
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("请填写以 / 开头的完整路径")
	}
	if err := os.MkdirAll(p, 0o755); err != nil {
		return "", fmt.Errorf("无法创建或访问这个目录：%v", err)
	}
	if strings.HasPrefix(p, filepath.Clean(cfg.Inbox)) || strings.HasPrefix(filepath.Clean(cfg.Inbox), p+string(os.PathSeparator)) {
		return "", fmt.Errorf("书库目录不能和投递目录互相包含")
	}
	return p, nil
}

func handleAddLibrary(w http.ResponseWriter, r *http.Request) {
	var l Library
	readJSON(r, &l)
	p, err := validLibPath(l.Path)
	if err != nil {
		errJSON(w, 400, err.Error())
		return
	}
	if l.Kind != "comic" {
		l.Kind = "book"
	}
	if strings.TrimSpace(l.Name) == "" {
		l.Name = filepath.Base(p)
	}
	if l.Template == "" {
		l.Template = map[string]string{"book": defaultBookTemplate, "comic": defaultComicTemplate}[l.Kind]
	}
	db.Exec(`INSERT INTO libraries(name,path,kind,template,sort) VALUES(?,?,?,?,(SELECT COALESCE(MAX(sort),0)+1 FROM libraries))`, l.Name, p, l.Kind, l.Template)
	kickScan()
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func handleUpdateLibrary(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	old := libraryByID(id)
	if old == nil {
		errJSON(w, 404, "书库不存在")
		return
	}
	var l Library
	readJSON(r, &l)
	p := old.Path
	if l.Path != "" && filepath.Clean(l.Path) != old.Path {
		var err error
		if p, err = validLibPath(l.Path); err != nil {
			errJSON(w, 400, err.Error())
			return
		}
	}
	if l.Name == "" {
		l.Name = old.Name
	}
	if l.Kind == "" {
		l.Kind = old.Kind
	}
	if l.Template == "" {
		l.Template = old.Template
	}
	db.Exec(`UPDATE libraries SET name=?,path=?,kind=?,template=? WHERE id=?`, l.Name, p, l.Kind, l.Template, id)
	invalidateSuggestions()
	if p != old.Path {
		kickScan()
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func handleDeleteLibrary(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM libraries`).Scan(&n)
	if n <= 1 {
		errJSON(w, 400, "至少要保留一个书库")
		return
	}
	// 只移除登记，不动文件
	db.Exec(`DELETE FROM files WHERE library_id=?`, id)
	db.Exec(`DELETE FROM books WHERE NOT EXISTS(SELECT 1 FROM files WHERE files.book_id=books.id)`)
	db.Exec(`DELETE FROM libraries WHERE id=?`, id)
	invalidateSuggestions()
	writeJSON(w, 200, map[string]bool{"ok": true})
}

type Network struct {
	LanURL    string `json:"lan_url"`
	RemoteURL string `json:"remote_url"`
}

func handleGetSettings(w http.ResponseWriter, r *http.Request) {
	ai := aiConfig()
	if len(ai.APIKey) > 8 {
		ai.APIKey = ai.APIKey[:3] + strings.Repeat("•", 8) + ai.APIKey[len(ai.APIKey)-4:]
	} else if ai.APIKey != "" {
		ai.APIKey = "••••"
	}
	var net Network
	jsonUnmarshal(getSetting("network", "{}"), &net)
	if net.LanURL == "" {
		net.LanURL = env("SHUKU_LAN_URL", "")
	}
	if net.RemoteURL == "" {
		net.RemoteURL = env("SHUKU_REMOTE_URL", "")
	}
	tts := ttsConfig()
	if k := tts.OpenAI.APIKey; len(k) > 8 {
		tts.OpenAI.APIKey = k[:3] + strings.Repeat("•", 8) + k[len(k)-4:]
	} else if k != "" {
		tts.OpenAI.APIKey = "••••"
	}
	writeJSON(w, 200, map[string]any{
		"tts":             tts,
		"online_meta":     metaConfig(),
		"fulltext":        fulltextStatus(),
		"opds_path":       "/opds",
		"server_name":     getSetting("server_name", "我的书库"),
		"ai":              ai,
		"auto_threshold":  autoThreshold(),
		"categories":      categories(),
		"series_template": getSetting("series_template", defaultSeriesTemplate),
		"scan_minutes":    getSetting("scan_minutes", "60"),
		"network":         net,
		"inbox":           cfg.Inbox,
		"version":         version,
		"tools":           toolStatus(),
	})
}

func handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var req map[string]json.RawMessage
	if readJSON(r, &req) != nil {
		errJSON(w, 400, "格式不正确")
		return
	}
	for k, v := range req {
		switch k {
		case "server_name", "series_template", "scan_minutes":
			var s string
			if json.Unmarshal(v, &s) == nil {
				setSetting(k, strings.TrimSpace(s))
			}
		case "auto_threshold":
			var f float64
			if json.Unmarshal(v, &f) == nil && f > 0 && f <= 1 {
				setSetting(k, strconv.FormatFloat(f, 'f', 2, 64))
			}
		case "categories":
			var c []string
			if json.Unmarshal(v, &c) == nil && len(c) > 0 {
				setSetting(k, toJSON(c))
			}
		case "network":
			var n Network
			if json.Unmarshal(v, &n) == nil {
				n.LanURL = strings.TrimRight(strings.TrimSpace(n.LanURL), "/")
				n.RemoteURL = strings.TrimRight(strings.TrimSpace(n.RemoteURL), "/")
				setSetting(k, toJSON(n))
			}
		case "tts":
			var t TTSConfig
			if json.Unmarshal(v, &t) == nil {
				if strings.Contains(t.OpenAI.APIKey, "•") {
					t.OpenAI.APIKey = ttsConfig().OpenAI.APIKey
				}
				setSetting(k, toJSON(t))
			}
		case "online_meta":
			var m MetaConfig
			if json.Unmarshal(v, &m) == nil {
				setSetting(k, toJSON(m))
			}
		case "fulltext":
			var on bool
			if json.Unmarshal(v, &on) == nil {
				setSetting(k, map[bool]string{true: "1", false: "0"}[on])
				if on {
					kickFulltext()
				}
			}
		case "ai":
			var a AIConfig
			if json.Unmarshal(v, &a) == nil {
				old := aiConfig()
				if strings.Contains(a.APIKey, "•") {
					a.APIKey = old.APIKey // 前端回传的是打码后的 key，保留原值
				}
				setSetting(k, toJSON(a))
			}
		}
	}
	handleGetSettings(w, r)
}

func handleAITest(w http.ResponseWriter, r *http.Request) {
	var a AIConfig
	readJSON(r, &a)
	old := aiConfig()
	if strings.Contains(a.APIKey, "•") || a.APIKey == "" {
		a.APIKey = old.APIKey
	}
	a.Enabled = true
	// 临时用这份配置测试
	prev := getSetting("ai", "")
	setSetting("ai", toJSON(a))
	defer setSetting("ai", prev)
	name := "[www.xxbook.com]三体2黑暗森林(精校版)_1.txt"
	m := Meta{Sample: "序章 褐蚁已经忘记这里曾是它的家园。这段漫长的时光对于暮色中的大地和刚刚出现的星星来说短得可以忽略不计"}
	start := time.Now()
	id, err := aiIdentify(r.Context(), name, m, "book", ruleIdentify(name, m, "book"))
	if err != nil {
		errJSON(w, 502, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"input": name, "result": id, "ms": time.Since(start).Milliseconds()})
}

func handlePreviewTemplate(w http.ResponseWriter, r *http.Request) {
	tpl := r.URL.Query().Get("t")
	b := &Book{Title: "三体Ⅱ·黑暗森林", Author: "刘慈欣", Category: "小说", Series: "三体", SeriesIndex: 2, Year: 2008}
	ext := "epub"
	if r.URL.Query().Get("kind") == "comic" {
		b = &Book{Title: "进击的巨人 第3卷", Series: "进击的巨人", SeriesIndex: 3, Author: "谏山创", Category: "漫画", Year: 2011}
		ext = "cbz"
	}
	writeJSON(w, 200, map[string]string{"path": filepath.ToSlash(renderTemplate(tpl, b, ext)) + "." + ext})
}

// 服务器上有没有安装各种转换工具
func toolStatus() map[string]bool {
	has := func(names ...string) bool {
		for _, n := range names {
			if _, err := execLookPath(n); err == nil {
				return true
			}
		}
		return false
	}
	return map[string]bool{"calibre": has("ebook-convert"), "djvu": has("ddjvu"), "7z": has("7z", "7za", "7zz"), "pdf_cover": has("mutool", "pdftoppm"), "dict": dictInstalled()}
}

// ---------- 网页 ----------

func handleStatic(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		errJSON(w, 404, "接口不存在")
		return
	}
	p := filepath.Join(cfg.WebDir, filepath.FromSlash(filepath.Clean("/"+r.URL.Path)))
	st, err := os.Stat(p)
	if err != nil || st.IsDir() {
		p = filepath.Join(cfg.WebDir, "index.html")
	}
	base := filepath.Base(p)
	switch {
	case base == "index.html" || base == "sw.js" || base == "manifest.webmanifest":
		w.Header().Set("Cache-Control", "no-cache")
	case strings.Contains(p, string(os.PathSeparator)+"vendor"+string(os.PathSeparator)):
		w.Header().Set("Cache-Control", "public, max-age=604800")
	default:
		w.Header().Set("Cache-Control", "no-cache")
	}
	if strings.HasSuffix(p, ".mjs") {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	}
	http.ServeFile(w, r, p)
}

var execLookPath = exec.LookPath
