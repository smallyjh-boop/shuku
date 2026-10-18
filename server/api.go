package main

import (
	"context"
	"encoding/json"
	"fmt"
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
	ad := requireAdmin // 只有管理员能做：改书目、整理、设置、加书……
	mux.HandleFunc("GET /api/ping", handlePing)
	// 在线升级：各端安装包（不用登录）、通知更新服务
	mux.HandleFunc("GET /api/clients", handleClients)
	mux.HandleFunc("GET /dl/{name}", handleDownloadClient)
	mux.HandleFunc("GET /api/dict", a(handleDict))
	mux.HandleFunc("GET /api/dict/t2s", a(handleDictT2S))
	mux.HandleFunc("POST /api/dict/explain", a(handleDictExplain))
	mux.HandleFunc("POST /api/dict/speak", a(handleDictSpeak))
	mux.HandleFunc("GET /api/update", a(handleUpdateInfo))
	mux.HandleFunc("POST /api/update/check", ad(handleUpdateCheck))
	mux.HandleFunc("GET /api/auth/state", handleAuthState)
	mux.HandleFunc("POST /api/auth/setup", handleSetup)
	mux.HandleFunc("POST /api/auth/login", handleLogin)
	mux.HandleFunc("POST /api/auth/logout", a(handleLogout))
	mux.HandleFunc("POST /api/auth/password", a(handleChangePassword))
	mux.HandleFunc("POST /api/auth/handoff", a(handleHandoff))
	mux.HandleFunc("POST /api/auth/exchange", handleExchange)
	mux.HandleFunc("GET /api/me", a(handleMe))
	mux.HandleFunc("GET /api/users", ad(handleUsers))
	mux.HandleFunc("POST /api/users", ad(handleAddUser))
	mux.HandleFunc("PATCH /api/users/{id}", ad(handleUpdateUser))
	mux.HandleFunc("DELETE /api/users/{id}", ad(handleDeleteUser))

	mux.HandleFunc("GET /api/books", a(handleBooks))
	mux.HandleFunc("GET /api/books/states", a(handleBookStates))
	mux.HandleFunc("POST /api/books/batch", a(handleBatchBooks))
	mux.HandleFunc("GET /api/facets", a(handleFacets))
	mux.HandleFunc("GET /api/series", a(handleSeries))
	mux.HandleFunc("GET /api/books/{id}", a(handleBook))
	mux.HandleFunc("PATCH /api/books/{id}", a(handleUpdateBook))
	mux.HandleFunc("PATCH /api/books/{id}/me", a(handleUserBook))
	mux.HandleFunc("DELETE /api/books/{id}", ad(handleDeleteBook))
	mux.HandleFunc("POST /api/books/{id}/restore", ad(handleRestoreBook))
	mux.HandleFunc("GET /api/books/{id}/next", a(handleNextInSeries))
	mux.HandleFunc("POST /api/books/{id}/confirm", ad(handleConfirm))
	mux.HandleFunc("POST /api/books/{id}/identify", ad(handleIdentify))
	mux.HandleFunc("GET /api/books/{id}/cover", a(handleCover))
	mux.HandleFunc("PUT /api/books/{id}/cover", ad(handlePutCover))
	mux.HandleFunc("GET /api/books/{id}/read", a(handleRead))
	mux.HandleFunc("GET /api/books/{id}/progress", a(handleGetProgress))
	mux.HandleFunc("PUT /api/books/{id}/progress", a(handlePutProgress))
	mux.HandleFunc("GET /api/books/{id}/bookmarks", a(handleBookmarks))
	mux.HandleFunc("POST /api/books/{id}/bookmarks", a(handleAddBookmark))
	mux.HandleFunc("PATCH /api/bookmarks/{bid}", a(handleUpdateBookmark))
	mux.HandleFunc("DELETE /api/bookmarks/{bid}", a(handleDeleteBookmark))
	mux.HandleFunc("GET /api/notes", a(handleAllNotes))
	mux.HandleFunc("GET /api/books/{id}/notes.md", a(handleNotesMarkdown))
	mux.HandleFunc("GET /api/notes.md", a(handleNotesMarkdown))

	mux.HandleFunc("GET /api/files/{fid}/raw", a(handleRaw))
	mux.HandleFunc("GET /api/files/{fid}/epub", a(handleConvertedEPUB))
	// 阅读器按需取书里的单个文件（zipentry.go）
	mux.HandleFunc("GET /api/files/{fid}/zip", a(handleZipList(false)))
	mux.HandleFunc("GET /api/files/{fid}/zip/{name...}", a(handleZipEntry(false)))
	mux.HandleFunc("GET /api/files/{fid}/epub/zip", a(handleZipList(true)))
	mux.HandleFunc("GET /api/files/{fid}/epub/zip/{name...}", a(handleZipEntry(true)))
	mux.HandleFunc("GET /api/files/{fid}/search", a(handleFileSearch)) // 一本书里搜（用全文索引）
	mux.HandleFunc("GET /api/files/{fid}/pdf", a(handleConvertedPDF))
	mux.HandleFunc("GET /api/files/{fid}/pages", a(handlePages))
	mux.HandleFunc("GET /api/files/{fid}/pages/{n}", a(handlePage))
	mux.HandleFunc("GET /api/files/{fid}/pdf/info", a(handlePDFInfo))
	mux.HandleFunc("GET /api/files/{fid}/pdf/page/{n}", a(handlePDFPage))
	mux.HandleFunc("GET /api/files/{fid}/pdf/text/{n}", a(handlePDFText))
	mux.HandleFunc("GET /api/files/{fid}/ocr/{page}", a(handleOCR))
	mux.HandleFunc("POST /api/translate", a(handleTranslate))

	mux.HandleFunc("POST /api/upload", ad(handleUpload))
	mux.HandleFunc("GET /api/status", a(handleStatus))
	mux.HandleFunc("POST /api/scan", ad(handleScan))
	mux.HandleFunc("GET /api/events", ad(handleEvents))
	mux.HandleFunc("GET /api/suggestions", ad(handleSuggestions))
	mux.HandleFunc("POST /api/suggestions/apply", ad(handleApplySuggestion))
	mux.HandleFunc("POST /api/suggestions/ignore", ad(handleIgnoreSuggestion))
	mux.HandleFunc("GET /api/ops", ad(handleOps))
	mux.HandleFunc("POST /api/ops/{batch}/undo", ad(handleUndo))
	mux.HandleFunc("GET /api/libraries", a(handleLibraries))
	mux.HandleFunc("POST /api/libraries", ad(handleAddLibrary))
	mux.HandleFunc("PUT /api/libraries/{id}", ad(handleUpdateLibrary))
	mux.HandleFunc("DELETE /api/libraries/{id}", ad(handleDeleteLibrary))
	mux.HandleFunc("GET /api/settings", a(handleGetSettings))
	mux.HandleFunc("PUT /api/settings", ad(handlePutSettings))
	mux.HandleFunc("POST /api/ai/test", ad(handleAITest))
	mux.HandleFunc("GET /api/preview-template", ad(handlePreviewTemplate))
	mux.HandleFunc("GET /api/storage", ad(handleStorage))
	mux.HandleFunc("POST /api/storage/clean", ad(handleStorageClean))
	mux.HandleFunc("GET /api/backups", ad(handleBackups))
	mux.HandleFunc("POST /api/backups", ad(handleCreateBackup))
	mux.HandleFunc("POST /api/backups/restore", ad(handleRestoreBackup))
	mux.HandleFunc("GET /api/backups/{name}", ad(handleDownloadBackup))
	mux.HandleFunc("DELETE /api/backups/{name}", ad(handleDeleteBackup))

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
	Rating      int     `json:"rating"`
	Want        bool    `json:"want"`
	Missing     bool    `json:"missing,omitempty"`
}

func handleBooks(w http.ResponseWriter, r *http.Request) {
	u := me(r)
	q := r.URL.Query()
	vis, vargs := visibleSQL(u)
	where := []string{vis}
	args := append([]any{}, vargs...)
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
	eq("state", "COALESCE(ub.read_state,'unread')")
	eq("status", "b.status")
	eq("author", "b.author")
	eq("series", "b.series")
	if v := q.Get("tag"); v != "" {
		where = append(where, `EXISTS(SELECT 1 FROM json_each(b.tags) t WHERE t.value=?)`)
		args = append(args, v)
	}
	if v := q.Get("format"); v != "" {
		where = append(where, `EXISTS(SELECT 1 FROM files f WHERE f.book_id=b.id AND f.format=?)`)
		args = append(args, v)
	}
	if v := q.Get("library"); v != "" {
		where = append(where, `EXISTS(SELECT 1 FROM files f WHERE f.book_id=b.id AND f.library_id=?)`)
		args = append(args, v)
	}
	if q.Get("reading") == "1" {
		where = append(where, `COALESCE(p.updated_at,0)>0 AND COALESCE(ub.read_state,'unread')<>'done'`)
	}
	if q.Get("want") == "1" {
		where = append(where, `COALESCE(ub.want,0)=1`)
	}
	if q.Get("missing") == "1" {
		where = append(where, `NOT EXISTS(SELECT 1 FROM files f WHERE f.book_id=b.id AND f.missing_since=0)`)
	}
	if v := q.Get("ids"); v != "" {
		var ph []string
		for _, s := range strings.Split(v, ",") {
			if id, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64); err == nil && len(ph) < 500 {
				ph = append(ph, "?")
				args = append(args, id)
			}
		}
		if len(ph) > 0 {
			where = append(where, `b.id IN (`+strings.Join(ph, ",")+`)`)
		}
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
	case "rating":
		order = `COALESCE(ub.rating,0) DESC, COALESCE(p.updated_at,0) DESC, b.sort_key`
	}
	w2 := " WHERE " + strings.Join(where, " AND ")
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 120
	}
	offset, _ := strconv.Atoi(q.Get("offset"))
	from := ` FROM books b LEFT JOIN progress p ON p.book_id=b.id AND p.user_id=? LEFT JOIN user_books ub ON ub.book_id=b.id AND ub.user_id=?`
	all := append([]any{u.ID, u.ID}, args...)
	var total int
	db.QueryRow(`SELECT COUNT(*)`+from+w2, all...).Scan(&total)
	rows, err := db.Query(`SELECT b.id,b.title,b.author,b.series,b.series_index,b.category,b.kind,b.status,b.cover,COALESCE(ub.read_state,'unread'),COALESCE(p.percent,0),COALESCE(p.updated_at,0),b.created_at,b.confidence,
		COALESCE((SELECT GROUP_CONCAT(DISTINCT f.format) FROM files f WHERE f.book_id=b.id),''),COALESCE(ub.rating,0),COALESCE(ub.want,0),
		NOT EXISTS(SELECT 1 FROM files f WHERE f.book_id=b.id AND f.missing_since=0)`+from+w2+` ORDER BY `+order+` LIMIT ? OFFSET ?`, append(all, limit, offset)...)
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	defer rows.Close()
	items := []BookListItem{}
	for rows.Next() {
		var b BookListItem
		var want int
		rows.Scan(&b.ID, &b.Title, &b.Author, &b.Series, &b.SeriesIndex, &b.Category, &b.Kind, &b.Status, &b.Cover, &b.ReadState, &b.Percent, &b.LastRead, &b.CreatedAt, &b.Confidence, &b.Formats, &b.Rating, &want, &b.Missing)
		b.Want = want == 1
		items = append(items, b)
	}
	writeJSON(w, 200, map[string]any{"total": total, "items": items})
}

func countBy(query string, args ...any) []map[string]any {
	rows, err := db.Query(query, args...)
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
	u := me(r)
	vis, vargs := visibleSQL(u)
	var total, review, reading, want, series, missing int
	db.QueryRow(`SELECT COUNT(*) FROM books b WHERE `+vis, vargs...).Scan(&total)
	db.QueryRow(`SELECT COUNT(*) FROM books b WHERE b.status='review' AND `+vis, vargs...).Scan(&review)
	db.QueryRow(`SELECT COUNT(*) FROM books b JOIN progress p ON p.book_id=b.id AND p.user_id=? LEFT JOIN user_books ub ON ub.book_id=b.id AND ub.user_id=? WHERE COALESCE(ub.read_state,'unread')<>'done' AND `+vis,
		append([]any{u.ID, u.ID}, vargs...)...).Scan(&reading)
	db.QueryRow(`SELECT COUNT(*) FROM books b JOIN user_books ub ON ub.book_id=b.id AND ub.user_id=? WHERE ub.want=1 AND `+vis, append([]any{u.ID}, vargs...)...).Scan(&want)
	db.QueryRow(`SELECT COUNT(DISTINCT b.series) FROM books b WHERE b.series<>'' AND `+vis, vargs...).Scan(&series)
	db.QueryRow(`SELECT COUNT(*) FROM books b WHERE NOT EXISTS(SELECT 1 FROM files f WHERE f.book_id=b.id AND f.missing_since=0) AND `+vis, vargs...).Scan(&missing)
	out := map[string]any{
		"total":      total,
		"review":     review,
		"reading":    reading,
		"want":       want,
		"series":     series,
		"missing":    missing,
		"kinds":      countBy(`SELECT b.kind,COUNT(*) FROM books b WHERE `+vis+` GROUP BY b.kind`, vargs...),
		"categories": countBy(`SELECT b.category,COUNT(*) FROM books b WHERE b.category<>'' AND `+vis+` GROUP BY b.category ORDER BY COUNT(*) DESC`, vargs...),
		"formats":    countBy(`SELECT f.format,COUNT(DISTINCT f.book_id) FROM files f JOIN books b ON b.id=f.book_id WHERE `+vis+` GROUP BY f.format ORDER BY COUNT(*) DESC`, vargs...),
		"states": countBy(`SELECT COALESCE(ub.read_state,'unread'),COUNT(*) FROM books b LEFT JOIN user_books ub ON ub.book_id=b.id AND ub.user_id=? WHERE `+vis+` GROUP BY 1`,
			append([]any{u.ID}, vargs...)...),
		"libraries": countBy(`SELECT CAST(f.library_id AS TEXT),COUNT(DISTINCT f.book_id) FROM files f JOIN books b ON b.id=f.book_id WHERE `+vis+` GROUP BY f.library_id`, vargs...),
		"tags":      countBy(`SELECT t.value,COUNT(*) FROM books b, json_each(b.tags) t WHERE `+vis+` GROUP BY t.value ORDER BY COUNT(*) DESC LIMIT 60`, vargs...),
	}
	// 整理建议只给管理员看；后台算好的结果，不在这里现算（书多时要好几秒）
	if u.Admin() {
		out["suggestions"] = suggestionCount()
	}
	writeJSON(w, 200, out)
}

// 当前这个人能不能看这本书；不能看（或在回收站）就回 404
func visibleBook(w http.ResponseWriter, r *http.Request, id int64) (*Book, bool) {
	u := me(r)
	b, err := getBookFor(id, u.ID)
	if err != nil || b.DeletedAt > 0 || !canSeeBook(u, id) {
		errJSON(w, 404, "找不到这本书")
		return nil, false
	}
	return b, true
}

func handleBook(w http.ResponseWriter, r *http.Request) {
	b, ok := visibleBook(w, r, pathID(r, "id"))
	if !ok {
		return
	}
	for i := range b.Files {
		b.Files[i].Path = relToLib(b.Files[i].Path)
	}
	ub := getUserBook(meID(r), b.ID)
	b.Prefs, b.FinishedAt = ub.Prefs, ub.FinishedAt
	writeJSON(w, 200, b)
}

// 改书目信息（管理员）。read_state 是每个人自己的，谁都能改（旧版网页、App 也走这里）
func handleUpdateBook(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	u := me(r)
	b, ok := visibleBook(w, r, id)
	if !ok {
		return
	}
	var req map[string]any
	if readJSON(r, &req) != nil {
		errJSON(w, 400, "请求格式不正确")
		return
	}
	if v, ok := req["read_state"].(string); ok {
		setReadState(u.ID, id, v)
		delete(req, "read_state")
	}
	if len(req) == 0 {
		nb, _ := getBookFor(id, u.ID)
		writeJSON(w, 200, map[string]any{"book": nb, "message": ""})
		return
	}
	if !u.Admin() {
		errJSON(w, 403, "只有管理员可以改书目信息")
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
	if _, err := db.Exec(`UPDATE books SET title=?,orig_title=?,author=?,translator=?,series=?,series_index=?,year=?,category=?,tags=?,description=?,isbn=?,publisher=?,language=?,direction=?,kind=?,status=?,confidence=?,source=?,updated_at=? WHERE id=?`,
		b.Title, b.OrigTitle, b.Author, b.Translator, b.Series, b.SeriesIndex, b.Year, b.Category, string(tags), b.Description, b.ISBN, b.Publisher, b.Language, b.Direction, b.Kind, b.Status, b.Confidence, b.Source, now(), id); err != nil {
		errJSON(w, 500, "保存失败："+err.Error())
		return
	}
	updateSearchKey(id)
	msg := ""
	// 已确认的书改了信息，同步改文件名和位置
	reorg, has := req["organize"].(bool)
	if (has && reorg) || (!has && b.Status == "ok") {
		if err := organizeBook(id, "ed-"+newBatch(), "修改信息后整理"); err != nil {
			msg = "信息已保存，但移动文件失败：" + err.Error()
		}
	}
	invalidateSuggestions()
	nb, _ := getBookFor(id, u.ID)
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

// 删除：文件移到书库的 _回收站，书目标记为已删除（进度、书签笔记、统计都留着），
// 在回收站里能连同这些一起恢复；清空回收站或 30 天后才真正删掉
func handleDeleteBook(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	b, err := getBook(id)
	if err != nil || b.DeletedAt > 0 {
		errJSON(w, 404, "找不到这本书")
		return
	}
	n, err := trashBook(b)
	if err != nil && n == 0 {
		errJSON(w, 500, "删除失败："+err.Error())
		return
	}
	logEvent("info", 0, fmt.Sprintf("已删除《%s》，文件移到了 _回收站，30 天内可以在回收站里恢复", b.Title))
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func trashBook(b *Book) (int, error) {
	batch := "rm-" + newBatch()
	moveMu.Lock()
	moved := 0
	var errs []string
	for i := range b.Files {
		f := &b.Files[i]
		if f.MissingSince > 0 {
			continue
		}
		lib, ok := insideLibrary(f.Path)
		if !ok {
			lib = libraryByID(f.LibraryID)
		}
		if lib == nil {
			continue
		}
		if err := relocate(f, filepath.Join(lib.Path, trashDir, filepath.Base(f.Path)), batch, "删除《"+b.Title+"》"); err != nil {
			errs = append(errs, err.Error())
		} else {
			moved++
		}
	}
	moveMu.Unlock()
	if len(errs) > 0 && moved == 0 && len(b.Files) > 0 {
		return 0, fmt.Errorf("%s", strings.Join(errs, "；"))
	}
	db.Exec(`UPDATE books SET deleted_at=?,deleted_batch=? WHERE id=?`, now(), batch, b.ID)
	for _, f := range b.Files {
		removeFileIndex(f.ID)
	}
	invalidateSuggestions()
	if len(errs) > 0 {
		return moved + 1, fmt.Errorf("%s", strings.Join(errs, "；"))
	}
	return moved + 1, nil
}

// 从回收站恢复：文件搬回原处，书目、进度、笔记都回来
func handleRestoreBook(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	var batch string
	var deleted int64
	if db.QueryRow(`SELECT deleted_at,deleted_batch FROM books WHERE id=?`, id).Scan(&deleted, &batch) != nil || deleted == 0 {
		errJSON(w, 404, "回收站里没有这本书")
		return
	}
	n, err := undoBatch(batch)
	restoreDeletedBooks(batch)
	msg := fmt.Sprintf("已恢复，%d 个文件搬回了原处", n)
	if err != nil {
		msg += "；" + err.Error()
	}
	kickFulltext()
	logEvent("info", id, msg)
	writeJSON(w, 200, map[string]any{"ok": true, "message": msg})
}

// 撤销了删除（回收站里点恢复，或者操作记录里撤销）：书目标回正常
func restoreDeletedBooks(batch string) {
	if batch == "" {
		return
	}
	db.Exec(`UPDATE books SET deleted_at=0,deleted_batch='' WHERE deleted_batch=?`, batch)
	invalidateSuggestions()
}

// 同一系列的下一本（漫画读到最后一页时提示“下一卷”）
func handleNextInSeries(w http.ResponseWriter, r *http.Request) {
	u := me(r)
	b, ok := visibleBook(w, r, pathID(r, "id"))
	if !ok {
		return
	}
	if b.Series == "" {
		writeJSON(w, 200, map[string]any{"next": nil})
		return
	}
	vis, vargs := visibleSQL(u)
	var sk string
	db.QueryRow(`SELECT sort_key FROM books WHERE id=?`, b.ID).Scan(&sk)
	args := append([]any{u.ID, u.ID, b.Series, b.ID, b.SeriesIndex, b.SeriesIndex, sk}, vargs...)
	nb, err := scanBook(db.QueryRow(`SELECT `+bookCols+bookFrom+` WHERE b.series=? AND b.id<>? AND (b.series_index>? OR (b.series_index=? AND b.sort_key>?)) AND `+vis+`
		ORDER BY b.series_index, b.sort_key LIMIT 1`, args...))
	if err != nil {
		writeJSON(w, 200, map[string]any{"next": nil})
		return
	}
	writeJSON(w, 200, map[string]any{"next": nb})
}

func handleCover(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	if !canSeeBook(me(r), id) {
		// 回收站里的书也要能显示封面（回收站列表里用），只挡住看不到这个书库的家人
		var deleted int64
		if db.QueryRow(`SELECT deleted_at FROM books WHERE id=?`, id).Scan(&deleted) != nil || deleted == 0 || !me(r).Admin() {
			http.NotFound(w, r)
			return
		}
	}
	if wd, _ := strconv.Atoi(r.URL.Query().Get("w")); wd > 0 && wd < 480 {
		handleCoverSized(w, r, id, wd)
		return
	}
	p := coverPath(id)
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
	b, ok := visibleBook(w, r, pathID(r, "id"))
	if !ok {
		return
	}
	if len(b.Files) == 0 {
		errJSON(w, 404, "找不到这本书的文件")
		return
	}
	f := b.Files[0]
	// 找不到的文件（被移走了）排到后面
	for _, x := range b.Files {
		if x.MissingSince == 0 {
			f = x
			break
		}
	}
	if fid, _ := strconv.ParseInt(r.URL.Query().Get("file"), 10, 64); fid > 0 {
		for _, x := range b.Files {
			if x.ID == fid {
				f = x
			}
		}
	} else {
		// 上次读的是哪个格式就继续用哪个
		var last int64
		if db.QueryRow(`SELECT file_id FROM progress WHERE user_id=? AND book_id=?`, meID(r), b.ID).Scan(&last) == nil {
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
	ub := getUserBook(meID(r), b.ID)
	writeJSON(w, 200, map[string]any{
		"file_id": f.ID, "format": f.Format, "reader": reader, "url": url,
		"name": name + "." + ext, "title": b.Title, "author": b.Author,
		"kind": b.Kind, "direction": b.Direction, "has_cover": b.Cover != "",
		"files": formats, "language": b.Language, "series": b.Series, "series_index": b.SeriesIndex,
		"prefs": ub.Prefs, "read_state": ub.ReadState, "missing": f.MissingSince > 0,
	})
}

// 文件接口：先看这个人能不能看这本书
func visibleFile(w http.ResponseWriter, r *http.Request) (*File, bool) {
	f, err := getFile(pathID(r, "fid"))
	if err != nil || !canSeeFile(me(r), f) {
		errJSON(w, 404, "文件不存在")
		return nil, false
	}
	return f, true
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
	f, ok := visibleFile(w, r)
	if !ok {
		return
	}
	dl := ""
	if r.URL.Query().Get("download") == "1" {
		dl = filepath.Base(f.Path)
	}
	serveFile(w, r, f.Path, f.Format, dl)
}

func handleConvertedEPUB(w http.ResponseWriter, r *http.Request) {
	f, ok := visibleFile(w, r)
	if !ok {
		return
	}
	out, err := convertedEPUBOf(f)
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	serveFile(w, r, out, "epub", "")
}

// 这个文件转换成的 EPUB（缓存里有就直接用）
func convertedEPUBOf(f *File) (string, error) {
	b, _ := getBook(f.BookID)
	title, author := filepath.Base(f.Path), ""
	if b != nil {
		title, author = b.Title, b.Author
	}
	return ensureEPUB(f.Path, f.Format, title, author)
}

func handleConvertedPDF(w http.ResponseWriter, r *http.Request) {
	f, ok := visibleFile(w, r)
	if !ok {
		return
	}
	out, err := ensurePDF(f.Path)
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	serveFile(w, r, out, "pdf", "")
}

func handlePages(w http.ResponseWriter, r *http.Request) {
	f, ok := visibleFile(w, r)
	if !ok {
		return
	}
	info, err := comicPagesInfo(f)
	if err != nil {
		errJSON(w, 500, "打开压缩包失败："+err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"count": len(info.names), "dims": info.dims})
}

func handlePage(w http.ResponseWriter, r *http.Request) {
	f, ok := visibleFile(w, r)
	if !ok {
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
			touchCache(filepath.Dir(cachePath))
			w.Header().Set("Content-Type", "image/jpeg")
			http.ServeFile(w, r, cachePath)
			return
		}
	}
	data, err := readArchivePage(f, pages[n])
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	if width > 0 {
		if out, err := resizeImage(data, width, 86); err == nil {
			os.MkdirAll(filepath.Dir(cachePath), 0o755)
			if os.WriteFile(cachePath, out, 0o644) == nil {
				touchCache(filepath.Dir(cachePath))
				noteCacheWrite()
			}
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

// ---------- 进度、书签笔记（每个人自己的） ----------

type progressRow struct {
	FileID    int64   `json:"file_id"`
	Location  string  `json:"location"`
	Percent   float64 `json:"percent"`
	Device    string  `json:"device"`
	UpdatedAt int64   `json:"updated_at"`
}

func getProgress(uid, bookID int64) progressRow {
	var p progressRow
	db.QueryRow(`SELECT file_id,location,percent,device,updated_at FROM progress WHERE user_id=? AND book_id=?`, uid, bookID).Scan(&p.FileID, &p.Location, &p.Percent, &p.Device, &p.UpdatedAt)
	return p
}

func handleGetProgress(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	if !canSeeBook(me(r), id) {
		errJSON(w, 404, "找不到这本书")
		return
	}
	writeJSON(w, 200, getProgress(meID(r), id))
}

// 存进度。冲突时以别的设备上更新的为准：客户端带上打开这本书时拿到的 updated_at（base），
// 服务器上的进度比它新（期间在别处读过）、位置又不一样，就不覆盖，只记阅读时长，回 conflict 和服务器上的进度。
// 写库失败回 500，客户端留着下次再传。
func handlePutProgress(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	u := me(r)
	if !canSeeBook(u, id) {
		errJSON(w, 404, "找不到这本书")
		return
	}
	var req struct {
		FileID   int64    `json:"file_id"`
		Location string   `json:"location"`
		Percent  float64  `json:"percent"`
		Device   string   `json:"device"`
		Seconds  int      `json:"seconds"` // 距上次上报实际阅读的秒数
		Base     *int64   `json:"base"`    // 客户端知道的服务器进度时间
		At       *float64 `json:"at"`      // 客户端记下这个位置的时间（毫秒），只用来判断离线排队的先后
	}
	if readJSON(r, &req) != nil {
		errJSON(w, 400, "格式不正确")
		return
	}
	if err := addReadingTime(u.ID, id, req.Seconds); err != nil {
		errJSON(w, 500, "记录阅读时长失败："+err.Error())
		return
	}
	cur := getProgress(u.ID, id)
	// 冲突：客户端打开以后，别的设备又存过不一样的位置。同一台设备自己连着翻页时，
	// 上一次保存的回复可能还没到（App 里是原生排队上传），base 落后一步不算冲突
	sameDevice := req.Device != "" && cur.Device == req.Device
	if req.Base != nil && cur.UpdatedAt > *req.Base && (cur.Location != req.Location || cur.FileID != req.FileID) && cur.UpdatedAt > 0 && !sameDevice {
		writeJSON(w, 200, map[string]any{"updated_at": cur.UpdatedAt, "conflict": true, "current": cur})
		return
	}
	// 位置没变（只是报一下阅读时长）：不改时间。不然别的设备会以为这里又往后读了，误报冲突
	if cur.UpdatedAt > 0 && cur.Location == req.Location && cur.FileID == req.FileID {
		writeJSON(w, 200, map[string]int64{"updated_at": cur.UpdatedAt})
		return
	}
	ts := now()
	if ts <= cur.UpdatedAt {
		ts = cur.UpdatedAt + 1 // 同一秒里存两次，也要让 updated_at 变，客户端靠它判断新旧
	}
	if _, err := db.Exec(`INSERT INTO progress(user_id,book_id,file_id,location,percent,device,updated_at) VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(user_id,book_id) DO UPDATE SET file_id=excluded.file_id,location=excluded.location,percent=excluded.percent,device=excluded.device,updated_at=excluded.updated_at`,
		u.ID, id, req.FileID, req.Location, req.Percent, req.Device, ts); err != nil {
		errJSON(w, 500, "保存进度失败："+err.Error())
		return
	}
	ub := getUserBook(u.ID, id)
	switch {
	case req.Percent >= 0.995:
		if ub.ReadState != "done" {
			setReadState(u.ID, id, "done")
		}
	case ub.ReadState == "unread":
		setReadState(u.ID, id, "reading")
	}
	writeJSON(w, 200, map[string]int64{"updated_at": ts})
}

// 书签和笔记在同一张表：kind = bookmark（书签）| highlight（划线，可以带笔记）
type Bookmark struct {
	ID        int64  `json:"id"`
	BookID    int64  `json:"book_id,omitempty"`
	Kind      string `json:"kind"`
	Location  string `json:"location"`
	Label     string `json:"label"`
	Text      string `json:"text"`
	Note      string `json:"note"`
	Color     string `json:"color"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

func handleBookmarks(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	if !canSeeBook(me(r), id) {
		errJSON(w, 404, "找不到这本书")
		return
	}
	writeJSON(w, 200, bookmarksOf(meID(r), id))
}

func bookmarksOf(uid, bookID int64) []Bookmark {
	rows, err := db.Query(`SELECT id,book_id,kind,location,label,text,note,color,created_at,updated_at FROM bookmarks WHERE book_id=? AND user_id=? ORDER BY id`, bookID, uid)
	out := []Bookmark{}
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var b Bookmark
			rows.Scan(&b.ID, &b.BookID, &b.Kind, &b.Location, &b.Label, &b.Text, &b.Note, &b.Color, &b.CreatedAt, &b.UpdatedAt)
			out = append(out, b)
		}
	}
	return out
}

func handleAddBookmark(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	if !canSeeBook(me(r), id) {
		errJSON(w, 404, "找不到这本书")
		return
	}
	var b Bookmark
	if readJSON(r, &b) != nil || b.Location == "" {
		errJSON(w, 400, "格式不正确")
		return
	}
	if b.Kind != "highlight" {
		b.Kind = "bookmark"
	}
	b.CreatedAt, b.UpdatedAt, b.BookID = now(), now(), id
	b.Text = truncate(b.Text, 4000)
	b.Note = truncate(b.Note, 8000)
	res, err := db.Exec(`INSERT INTO bookmarks(book_id,user_id,kind,location,label,text,note,color,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		id, meID(r), b.Kind, b.Location, b.Label, b.Text, b.Note, b.Color, b.CreatedAt, b.UpdatedAt)
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	b.ID, _ = res.LastInsertId()
	writeJSON(w, 200, b)
}

// 改笔记内容、颜色
func handleUpdateBookmark(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Note  *string `json:"note"`
		Color *string `json:"color"`
		Label *string `json:"label"`
	}
	if readJSON(r, &req) != nil {
		errJSON(w, 400, "格式不正确")
		return
	}
	bid, uid := pathID(r, "bid"), meID(r)
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM bookmarks WHERE id=? AND user_id=?`, bid, uid).Scan(&n)
	if n == 0 {
		errJSON(w, 404, "没有这条笔记")
		return
	}
	if req.Note != nil {
		db.Exec(`UPDATE bookmarks SET note=?,updated_at=? WHERE id=?`, truncate(*req.Note, 8000), now(), bid)
	}
	if req.Color != nil {
		db.Exec(`UPDATE bookmarks SET color=?,updated_at=? WHERE id=?`, *req.Color, now(), bid)
	}
	if req.Label != nil {
		db.Exec(`UPDATE bookmarks SET label=?,updated_at=? WHERE id=?`, *req.Label, now(), bid)
	}
	var b Bookmark
	db.QueryRow(`SELECT id,book_id,kind,location,label,text,note,color,created_at,updated_at FROM bookmarks WHERE id=?`, bid).Scan(&b.ID, &b.BookID, &b.Kind, &b.Location, &b.Label, &b.Text, &b.Note, &b.Color, &b.CreatedAt, &b.UpdatedAt)
	writeJSON(w, 200, b)
}

func handleDeleteBookmark(w http.ResponseWriter, r *http.Request) {
	db.Exec(`DELETE FROM bookmarks WHERE id=? AND user_id=?`, pathID(r, "bid"), meID(r))
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// 全部笔记（划线和书签），按书分组，最近的在前
func handleAllNotes(w http.ResponseWriter, r *http.Request) {
	u := me(r)
	vis, vargs := visibleSQL(u)
	kind := r.URL.Query().Get("kind")
	q := `SELECT m.id,m.book_id,m.kind,m.location,m.label,m.text,m.note,m.color,m.created_at,m.updated_at,b.title,b.author FROM bookmarks m JOIN books b ON b.id=m.book_id WHERE m.user_id=? AND ` + vis
	args := append([]any{u.ID}, vargs...)
	if kind != "" {
		q += ` AND m.kind=?`
		args = append(args, kind)
	}
	rows, err := db.Query(q+` ORDER BY m.created_at DESC LIMIT 2000`, args...)
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	defer rows.Close()
	type item struct {
		Bookmark
		Title  string `json:"title"`
		Author string `json:"author"`
	}
	out := []item{}
	for rows.Next() {
		var it item
		rows.Scan(&it.ID, &it.BookID, &it.Kind, &it.Location, &it.Label, &it.Text, &it.Note, &it.Color, &it.CreatedAt, &it.UpdatedAt, &it.Title, &it.Author)
		out = append(out, it)
	}
	writeJSON(w, 200, out)
}

// 导出笔记为 Markdown：一本书（/api/books/{id}/notes.md）或全部（/api/notes.md）
func handleNotesMarkdown(w http.ResponseWriter, r *http.Request) {
	u := me(r)
	var ids []int64
	if id := pathID(r, "id"); id > 0 {
		if !canSeeBook(u, id) {
			errJSON(w, 404, "找不到这本书")
			return
		}
		ids = []int64{id}
	} else {
		vis, vargs := visibleSQL(u)
		rows, err := db.Query(`SELECT DISTINCT m.book_id FROM bookmarks m JOIN books b ON b.id=m.book_id WHERE m.user_id=? AND m.kind='highlight' AND `+vis+` ORDER BY b.sort_key`, append([]any{u.ID}, vargs...)...)
		if err == nil {
			for rows.Next() {
				var id int64
				rows.Scan(&id)
				ids = append(ids, id)
			}
			rows.Close()
		}
	}
	var sb strings.Builder
	name := "书库笔记"
	if len(ids) == 1 && pathID(r, "id") > 0 {
		if b, err := getBook(ids[0]); err == nil {
			name = b.Title + " 笔记"
		}
	}
	sb.WriteString("# " + name + "\n\n")
	for _, id := range ids {
		b, err := getBook(id)
		if err != nil {
			continue
		}
		marks := bookmarksOf(u.ID, id)
		var hl []Bookmark
		for _, m := range marks {
			if m.Kind == "highlight" {
				hl = append(hl, m)
			}
		}
		if len(hl) == 0 {
			continue
		}
		if len(ids) > 1 {
			sb.WriteString("## 《" + b.Title + "》")
			if b.Author != "" {
				sb.WriteString(" " + b.Author)
			}
			sb.WriteString("\n\n")
		} else if b.Author != "" {
			sb.WriteString(b.Author + "\n\n")
		}
		chapter := ""
		for _, m := range hl {
			if m.Label != "" && m.Label != chapter {
				chapter = m.Label
				sb.WriteString(strings.Repeat("#", 2+map[bool]int{true: 1, false: 0}[len(ids) > 1]) + " " + chapter + "\n\n")
			}
			for _, line := range strings.Split(strings.TrimSpace(m.Text), "\n") {
				sb.WriteString("> " + line + "\n")
			}
			if m.Note != "" {
				sb.WriteString("\n" + strings.TrimSpace(m.Note) + "\n")
			}
			sb.WriteString("\n")
		}
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": safeName(name) + ".md"}))
	w.Write([]byte(sb.String()))
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
	if !me(r).Admin() {
		// 家人账号不看入库、整理的动态（/api/events 只给管理员），只要知道版本有没有变
		writeJSON(w, 200, map[string]any{"inbox": map[string]any{"pending": 0, "current": ""}, "scan": map[string]any{"running": false, "found": 0}, "last_event": 0, "version": version})
		return
	}
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

// 整理建议要遍历整个书库目录，书多时要好几秒：放在后台算，页面上直接用算好的结果。
// 书目有变化（invalidateSuggestions）时 5 秒后在后台重算，同时最多一个在算
var sugCache = struct {
	sync.Mutex
	at       time.Time
	list     []Suggestion
	computed bool
	stale    bool
}{}

var sugComputing sync.Mutex
var sugKick = make(chan struct{}, 1)

func invalidateSuggestions() {
	sugCache.Lock()
	sugCache.stale = true
	sugCache.Unlock()
	select {
	case sugKick <- struct{}{}:
	default:
	}
}

func recomputeSuggestions() []Suggestion {
	sugComputing.Lock()
	defer sugComputing.Unlock()
	list := computeSuggestions()
	sugCache.Lock()
	sugCache.list, sugCache.at, sugCache.computed, sugCache.stale = list, time.Now(), true, false
	sugCache.Unlock()
	return list
}

func suggestionsLoop(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(20 * time.Second):
	}
	recomputeSuggestions()
	for {
		select {
		case <-ctx.Done():
			return
		case <-sugKick:
			time.Sleep(5 * time.Second) // 连着改好几本书时只算一次
			select {
			case <-sugKick:
			default:
			}
			recomputeSuggestions()
		case <-time.After(30 * time.Minute):
			recomputeSuggestions()
		}
	}
}

// fresh：用户点了“重新检查”，现算；否则用后台算好的（还没算过就现算一次）
func cachedSuggestions(fresh bool) []Suggestion {
	sugCache.Lock()
	computed, list := sugCache.computed, sugCache.list
	sugCache.Unlock()
	if fresh || !computed {
		return recomputeSuggestions()
	}
	return list
}

func suggestionCount() int {
	sugCache.Lock()
	defer sugCache.Unlock()
	return len(sugCache.list)
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
	msg := "已撤销"
	if n > 0 {
		msg = fmt.Sprintf("已撤销 %d 个文件的移动", n)
	}
	if err != nil {
		msg += "；" + err.Error()
	}
	logEvent("info", 0, msg)
	writeJSON(w, 200, map[string]any{"count": n, "message": msg})
}

// ---------- 书库、设置 ----------

func handleLibraries(w http.ResponseWriter, r *http.Request) {
	u := me(r)
	out := []map[string]any{}
	for _, l := range listLibraries() {
		if !canSeeLibrary(u, l.ID) {
			continue
		}
		var n int
		db.QueryRow(`SELECT COUNT(DISTINCT f.book_id) FROM files f JOIN books b ON b.id=f.book_id WHERE f.library_id=? AND b.deleted_at=0`, l.ID).Scan(&n)
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
	// 只移除登记，不动文件。登记连同每个人的进度、笔记一起删，先备份一份，删错了能从备份恢复
	if _, err := backupDB("manual"); err != nil {
		errJSON(w, 500, "移除前备份数据库失败："+err.Error())
		return
	}
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
	// 家人账号只拿到阅读、听书要用的部分（AI、整理规则、路径这些是管理员的事）
	if !me(r).Admin() {
		if tts.OpenAI.APIKey != "" {
			tts.OpenAI.APIKey = "••••"
		}
		writeJSON(w, 200, map[string]any{
			"tts":         tts,
			"fulltext":    fulltextStatus(),
			"opds_path":   "/opds",
			"server_name": getSetting("server_name", "我的书库"),
			"categories":  categories(),
			"network":     net,
			"version":     version,
			"translate":   translateConfig(),
		})
		return
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
		"translate":       translateConfig(),
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
		case "translate":
			var t TranslateConfig
			if json.Unmarshal(v, &t) == nil {
				if t.Engine != "ai" {
					t.Engine = "microsoft"
				}
				if translateLangs[t.To] == "" {
					t.To = "zh-Hans"
				}
				setSetting(k, toJSON(t))
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
	// 用这份配置测试（不写进设置：测试的时候别的书照常用原来的配置识别）
	name := "[www.xxbook.com]三体2黑暗森林(精校版)_1.txt"
	m := Meta{Sample: "序章 褐蚁已经忘记这里曾是它的家园。这段漫长的时光对于暮色中的大地和刚刚出现的星星来说短得可以忽略不计"}
	start := time.Now()
	id, err := aiIdentifyWith(r.Context(), a, name, m, "book", ruleIdentify(name, m, "book"))
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
	return map[string]bool{"calibre": has("ebook-convert"), "djvu": has("ddjvu"), "7z": has("7z", "7za", "7zz"), "pdf_cover": has("mutool", "pdftoppm"), "dict": dictInstalled(), "ocr": has(tesseractBin)}
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
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if base == "index.html" {
		w.Header().Set("Content-Security-Policy", pageCSP)
	}
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

// 网页的内容安全策略：只运行本站的脚本文件。
// 书的每一章在 iframe 里（blob: 地址，继承这个策略），书里夹带的脚本不会运行，也就碰不到登录状态；
// 书里的样式、图片、字体（blob:、data:）照常显示
const pageCSP = "default-src 'self'; script-src 'self' 'wasm-unsafe-eval'; style-src 'self' 'unsafe-inline' blob: data:; " +
	"img-src 'self' blob: data: http: https:; font-src 'self' blob: data:; media-src 'self' blob: data:; " +
	"connect-src 'self' blob: data: http: https:; frame-src 'self' blob: data: about:; worker-src 'self' blob:; " +
	"object-src 'none'; base-uri 'self'; form-action 'self'"

var execLookPath = exec.LookPath
