package main

// 给手机、手表、浏览器插件用的接口：
//   扫码配对      POST /api/pair → 网页显示二维码；App 扫码后 POST /api/pair/claim 换登录令牌
//   设备管理      GET /api/sessions、DELETE /api/sessions/{id}
//   服务器信息    GET /api/info（内网、Tailscale 地址，客户端据此自动切换）
//   按章节取正文  GET /api/books/{id}/chapters、/chapters/{n}（手表阅读、手机后台听书）

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"shuku/qr"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// ---------- 扫码配对 ----------

type pairCode struct {
	created time.Time
	uid     int64 // 谁生成的配对码：连上的设备就是这个人的
}

var pairMu sync.Mutex
var pairCodes = map[string]pairCode{}

// 去掉容易看错的字符，方便手动输入
const pairAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

func newPairCode() string {
	b := randBytes(8)
	out := make([]byte, 8)
	for i := range out {
		out[i] = pairAlphabet[int(b[i])%len(pairAlphabet)]
	}
	return string(out)
}

func serverNetwork() Network {
	var n Network
	jsonUnmarshal(getSetting("network", "{}"), &n)
	if n.LanURL == "" {
		n.LanURL = env("SHUKU_LAN_URL", "")
	}
	if n.RemoteURL == "" {
		n.RemoteURL = env("SHUKU_REMOTE_URL", "")
	}
	return n
}

func serverInfo(r *http.Request) map[string]any {
	n := serverNetwork()
	return map[string]any{
		"name": getSetting("server_name", "我的书库"), "id": getSetting("server_id", ""), "version": version,
		"lan_url": n.LanURL, "remote_url": n.RemoteURL, "origin": baseURL(r),
	}
}

func handleInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, serverInfo(r))
}

func handlePairCreate(w http.ResponseWriter, r *http.Request) {
	code := newPairCode()
	pairMu.Lock()
	for c, p := range pairCodes {
		if time.Since(p.created) > 10*time.Minute {
			delete(pairCodes, c)
		}
	}
	pairCodes[code] = pairCode{created: time.Now(), uid: meID(r)}
	pairMu.Unlock()
	info := serverInfo(r)
	q := url.Values{}
	q.Set("c", code)
	for k, v := range map[string]string{"l": info["lan_url"].(string), "r": info["remote_url"].(string), "o": info["origin"].(string), "n": info["name"].(string), "i": info["id"].(string)} {
		if v != "" {
			q.Set(k, v)
		}
	}
	info["code"] = code
	info["expires_in"] = 600
	info["link"] = "shuku://pair?" + q.Encode()
	if svg, err := qr.SVG(info["link"].(string)); err == nil {
		info["qr"] = svg
	}
	writeJSON(w, 200, info)
}

func handlePairClaim(w http.ResponseWriter, r *http.Request) {
	if tooManyFails(r) {
		errJSON(w, 429, "尝试次数过多，请 10 分钟后再试")
		return
	}
	var req struct {
		Code   string `json:"code"`
		Device string `json:"device"`
	}
	readJSON(r, &req)
	code := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(req.Code), " ", ""))
	pairMu.Lock()
	p, ok := pairCodes[code]
	delete(pairCodes, code)
	pairMu.Unlock()
	if !ok || time.Since(p.created) > 10*time.Minute {
		noteFail(r)
		errJSON(w, 401, "配对码无效或已过期，请在网页上重新生成")
		return
	}
	owner, err := getUser(p.uid)
	if err != nil {
		errJSON(w, 401, "生成配对码的账号已经删除了")
		return
	}
	device := strings.TrimSpace(req.Device)
	if device == "" {
		device = "App"
	}
	info := serverInfo(r)
	info["token"] = newSession(w, device, owner.ID)
	info["user"] = map[string]any{"id": owner.ID, "name": owner.Name, "role": owner.Role}
	logEvent("info", 0, "新设备已连接："+device+"（"+owner.Name+"）")
	writeJSON(w, 200, info)
}

// ---------- 设备（登录会话） ----------

func sessionID(token string) string {
	s := sha256.Sum256([]byte(token))
	return hex.EncodeToString(s[:6])
}

// 登录过的设备：管理员看到所有人的，家人只看到自己的
func handleSessions(w http.ResponseWriter, r *http.Request) {
	u := me(r)
	cur := tokenFrom(r)
	q := `SELECT s.token,s.device,s.created_at,s.last_seen,s.user_id,COALESCE(u.name,'') FROM sessions s LEFT JOIN users u ON u.id=s.user_id`
	var args []any
	if !u.Admin() {
		q += ` WHERE s.user_id=?`
		args = append(args, u.ID)
	}
	rows, err := db.Query(q+` ORDER BY s.last_seen DESC`, args...)
	out := []map[string]any{}
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var t, d, name string
			var c, l, uid int64
			rows.Scan(&t, &d, &c, &l, &uid, &name)
			out = append(out, map[string]any{"id": sessionID(t), "device": ifs(d != "", d, "未知设备"), "created_at": c, "last_seen": l, "current": t == cur, "user_id": uid, "user": name})
		}
	}
	writeJSON(w, 200, out)
}

func handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	u := me(r)
	id := r.PathValue("id")
	rows, err := db.Query(`SELECT token,user_id FROM sessions`)
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	var del []string
	for rows.Next() {
		var t string
		var uid int64
		rows.Scan(&t, &uid)
		if sessionID(t) == id && (u.Admin() || uid == u.ID) {
			del = append(del, t)
		}
	}
	rows.Close()
	for _, t := range del {
		db.Exec(`DELETE FROM sessions WHERE token=?`, t)
	}
	forgetSessions()
	writeJSON(w, 200, map[string]any{"ok": true, "count": len(del)})
}

// ---------- 按章节取正文 ----------

type WatchChapter struct {
	Index int    `json:"i"`
	Title string `json:"title"`
	Chars int    `json:"chars"`
	Start int    `json:"start"` // 这一章之前的总字数
	Loc   string `json:"loc"`   // 阅读器能识别的位置：sec:章节:比例 / p:页码:0；空表示只能按百分比
	text  string
}

var chapterCache = struct {
	sync.Mutex
	key  string
	list []WatchChapter
}{}

var textFormatRank = map[string]int{"epub": 1, "txt": 2, "azw3": 3, "mobi": 4, "azw": 5, "fb2": 6, "fbz": 7, "pdf": 8, "docx": 9}

// 选一个能提取文字的文件：优先上次读的那个
func textFileOf(b *Book, prefer int64) *File {
	var best *File
	for i := range b.Files {
		f := &b.Files[i]
		r, ok := textFormatRank[f.Format]
		if !ok {
			continue
		}
		if f.ID == prefer {
			return f
		}
		if best == nil || r < textFormatRank[best.Format] {
			best = f
		}
	}
	return best
}

const watchChunk = 6000 // 手表一次读的字数上限

func splitText(text string, max int) []string {
	r := []rune(text)
	var out []string
	for len(r) > max {
		cut := max
		for j := max; j > max-800 && j > 0; j-- {
			if r[j] == '\n' {
				cut = j + 1
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

func bookChapters(f *File) ([]WatchChapter, error) {
	key := fmt.Sprintf("%d:%s", f.ID, f.Hash)
	chapterCache.Lock()
	if chapterCache.key == key {
		l := chapterCache.list
		chapterCache.Unlock()
		return l, nil
	}
	chapterCache.Unlock()
	secs := loadSections(f)
	if secs == nil {
		var err error
		if secs, err = extractSections(f); err != nil {
			return nil, err
		}
		if f.Hash != "" {
			saveSections(f, secs)
		}
	}
	var out []WatchChapter
	add := func(title, text, loc string) {
		text = strings.TrimSpace(text)
		if text == "" {
			return
		}
		out = append(out, WatchChapter{Index: len(out), Title: title, Chars: utf8.RuneCountInString(text), Loc: loc, text: text})
	}
	switch {
	case f.Format == "pdf":
		// PDF 一页一节太碎：几页合成一段
		var buf strings.Builder
		first := 0
		flush := func(last int) {
			if buf.Len() > 0 {
				title := fmt.Sprintf("第 %d 页", first)
				if last > first {
					title = fmt.Sprintf("第 %d～%d 页", first, last)
				}
				add(title, buf.String(), fmt.Sprintf("p:%d:0", first))
				buf.Reset()
			}
		}
		last := 0
		for _, s := range secs {
			if buf.Len() == 0 {
				first = s.Sec
			}
			buf.WriteString(s.Text)
			buf.WriteString("\n")
			last = s.Sec
			if utf8.RuneCountInString(buf.String()) >= watchChunk {
				flush(last)
			}
		}
		flush(last)
	case len(secs) == 1 && secs[0].Sec < 0:
		// MOBI、FB2：整本是一段文字，按“第X章”切
		for _, c := range splitChapters(secs[0].Text) {
			for k, part := range splitText(strings.Join(c.Lines, "\n"), watchChunk*2) {
				title := c.Title
				if k > 0 {
					title = fmt.Sprintf("%s（%d）", c.Title, k+1)
				}
				add(title, part, "")
			}
		}
	default:
		for n, s := range secs {
			title := s.Label
			if title == "" {
				title = fmt.Sprintf("第 %d 节", n+1)
			}
			parts := splitText(s.Text, watchChunk*2)
			total := utf8.RuneCountInString(s.Text)
			done := 0
			for k, part := range parts {
				t := title
				if k > 0 {
					t = fmt.Sprintf("%s（%d）", title, k+1)
				}
				frac := 0.0
				if total > 0 {
					frac = float64(done) / float64(total)
				}
				add(t, part, fmt.Sprintf("sec:%d:%.4f", s.Sec, frac))
				done += utf8.RuneCountInString(part)
			}
		}
	}
	start := 0
	for i := range out {
		out[i].Start = start
		start += out[i].Chars
	}
	chapterCache.Lock()
	chapterCache.key, chapterCache.list = key, out
	chapterCache.Unlock()
	return out, nil
}

var cfiSpineRe = regexp.MustCompile(`^epubcfi\(/6/(\d+)`)

// 把阅读进度换算成“第几段、段内比例”
func resumePoint(chs []WatchChapter, loc string, percent float64) (int, float64) {
	if len(chs) == 0 {
		return 0, 0
	}
	total := chs[len(chs)-1].Start + chs[len(chs)-1].Chars
	byChar := func(pos int) (int, float64) {
		for _, c := range chs {
			if pos < c.Start+c.Chars {
				return c.Index, float64(pos-c.Start) / float64(max(1, c.Chars))
			}
		}
		return len(chs) - 1, 1
	}
	target := int(percent * float64(total))
	sec, inSec := -1, -1.0
	switch {
	case strings.HasPrefix(loc, "sec:"):
		p := strings.Split(loc, ":")
		if len(p) >= 2 {
			sec, _ = strconv.Atoi(p[1])
		}
		if len(p) >= 3 {
			inSec, _ = strconv.ParseFloat(p[2], 64)
		}
	case strings.HasPrefix(loc, "epubcfi("):
		if m := cfiSpineRe.FindStringSubmatch(loc); m != nil {
			n, _ := strconv.Atoi(m[1])
			sec = n/2 - 1
		}
	case strings.HasPrefix(loc, "p:"):
		p := strings.Split(loc, ":")
		page, _ := strconv.Atoi(p[1])
		best := 0
		for _, c := range chs {
			var first int
			fmt.Sscanf(c.Loc, "p:%d", &first)
			if first <= page {
				best = c.Index
			}
		}
		return best, 0
	}
	if sec >= 0 {
		// 同一节可能拆成了几段
		var group []WatchChapter
		for _, c := range chs {
			var s int
			var f float64
			if n, _ := fmt.Sscanf(c.Loc, "sec:%d:%f", &s, &f); n >= 1 && s == sec {
				group = append(group, c)
			}
		}
		if len(group) > 0 {
			gStart, gEnd := group[0].Start, group[len(group)-1].Start+group[len(group)-1].Chars
			pos := -1
			if inSec >= 0 {
				pos = gStart + int(inSec*float64(gEnd-gStart))
			} else if target >= gStart && target < gEnd {
				pos = target // CFI 没有段内位置，用百分比估一下
			} else {
				pos = gStart
			}
			return byChar(pos)
		}
	}
	return byChar(target)
}

func chapterBook(w http.ResponseWriter, r *http.Request) (*Book, *File, []WatchChapter, bool) {
	b, ok := visibleBook(w, r, pathID(r, "id"))
	if !ok {
		return nil, nil, nil, false
	}
	var last int64
	db.QueryRow(`SELECT file_id FROM progress WHERE user_id=? AND book_id=?`, meID(r), b.ID).Scan(&last)
	if fid, _ := strconv.ParseInt(r.URL.Query().Get("file"), 10, 64); fid > 0 {
		last = fid
	}
	f := textFileOf(b, last)
	if f == nil || b.Kind == "comic" {
		errJSON(w, 400, "这本书没有可以提取的文字（漫画、扫描版）")
		return nil, nil, nil, false
	}
	chs, err := bookChapters(f)
	if err != nil || len(chs) == 0 {
		msg := "提取文字失败"
		if err != nil {
			msg += "：" + err.Error()
		}
		errJSON(w, 400, msg)
		return nil, nil, nil, false
	}
	return b, f, chs, true
}

func handleChapters(w http.ResponseWriter, r *http.Request) {
	b, f, chs, ok := chapterBook(w, r)
	if !ok {
		return
	}
	p := getProgress(meID(r), b.ID)
	loc := p.Location
	if p.FileID != 0 && p.FileID != f.ID && !strings.HasPrefix(loc, "sec:") {
		loc = "" // 换了格式，只能按百分比
	}
	i, frac := resumePoint(chs, loc, p.Percent)
	total := chs[len(chs)-1].Start + chs[len(chs)-1].Chars
	writeJSON(w, 200, map[string]any{
		"book_id": b.ID, "file_id": f.ID, "format": f.Format, "title": b.Title, "author": b.Author,
		"total": total, "chapters": chs, "resume": map[string]any{"i": i, "frac": frac, "percent": p.Percent, "updated_at": p.UpdatedAt},
	})
}

func handleChapter(w http.ResponseWriter, r *http.Request) {
	b, f, chs, ok := chapterBook(w, r)
	if !ok {
		return
	}
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || n < 0 || n >= len(chs) {
		errJSON(w, 404, "没有这一章")
		return
	}
	c := chs[n]
	total := chs[len(chs)-1].Start + chs[len(chs)-1].Chars
	writeJSON(w, 200, map[string]any{
		"book_id": b.ID, "file_id": f.ID, "i": c.Index, "title": c.Title, "text": c.text, "chars": c.Chars,
		"start": c.Start, "total": total, "loc": c.Loc, "count": len(chs),
	})
}

// ---------- 小尺寸封面（手表、通知栏） ----------

func handleCoverSized(w http.ResponseWriter, r *http.Request, id int64, width int) {
	src := coverPath(id)
	st, err := os.Stat(src)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	width = (width + 39) / 40 * 40
	if width > 480 {
		width = 480
	}
	p := filepath.Join(cfg.DataDir, "cache", "covers", fmt.Sprintf("%d_%d_%d.jpg", id, width, st.ModTime().Unix()))
	if _, err := os.Stat(p); err != nil {
		b, err := os.ReadFile(src)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		out, err := resizeImage(b, width, 80)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, out, 0o644)
	}
	w.Header().Set("Cache-Control", "private, max-age=2592000")
	w.Header().Set("Content-Type", "image/jpeg")
	http.ServeFile(w, r, p)
}

// ---------- 回收站 ----------

func trashDirs() []string {
	var out []string
	for _, l := range listLibraries() {
		out = append(out, filepath.Join(l.Path, trashDir))
	}
	return out
}

// 回收站：删掉的书（能连同进度、笔记一起恢复）+ 文件夹里的占用
func handleTrash(w http.ResponseWriter, r *http.Request) {
	var files int
	var size int64
	for _, d := range trashDirs() {
		filepath.Walk(d, func(p string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() {
				files++
				size += info.Size()
			}
			return nil
		})
	}
	books := []map[string]any{}
	rows, err := db.Query(`SELECT id,title,author,kind,cover,deleted_at FROM books WHERE deleted_at>0 ORDER BY deleted_at DESC LIMIT 500`)
	if err == nil {
		for rows.Next() {
			var id, at int64
			var title, author, kind, cover string
			rows.Scan(&id, &title, &author, &kind, &cover, &at)
			books = append(books, map[string]any{"id": id, "title": title, "author": author, "kind": kind, "cover": cover, "deleted_at": at,
				"expires_at": at + keepDays*24*3600})
		}
		rows.Close()
	}
	writeJSON(w, 200, map[string]any{"files": files, "size": size, "books": books, "keep_days": keepDays})
}

// 清空回收站：删掉的书彻底删除（文件、书目、每个人的进度笔记、缓存），回收站文件夹清空
func handleEmptyTrash(w http.ResponseWriter, r *http.Request) {
	var ids []int64
	rows, err := db.Query(`SELECT id FROM books WHERE deleted_at>0`)
	if err == nil {
		for rows.Next() {
			var id int64
			rows.Scan(&id)
			ids = append(ids, id)
		}
		rows.Close()
	}
	for _, id := range ids {
		for _, f := range bookFiles(id) {
			removeFileCaches(&f)
		}
		purgeBook(id)
	}
	n := 0
	for _, d := range trashDirs() {
		entries, _ := os.ReadDir(d)
		for _, e := range entries {
			if os.RemoveAll(filepath.Join(d, e.Name())) == nil {
				n++
			}
		}
	}
	logEvent("info", 0, fmt.Sprintf("已清空回收站（%d 本书，%d 个文件）", len(ids), n))
	writeJSON(w, 200, map[string]any{"ok": true, "count": n, "books": len(ids)})
}

func routesP2(mux *http.ServeMux) {
	a := requireAuth
	mux.HandleFunc("GET /api/info", a(handleInfo))
	mux.HandleFunc("GET /api/assets", handleAssets)
	mux.HandleFunc("POST /api/pair", a(handlePairCreate))
	mux.HandleFunc("POST /api/pair/claim", handlePairClaim)
	mux.HandleFunc("GET /api/sessions", a(handleSessions))
	mux.HandleFunc("DELETE /api/sessions/{id}", a(handleDeleteSession))
	mux.HandleFunc("GET /api/books/{id}/chapters", a(handleChapters))
	mux.HandleFunc("GET /api/books/{id}/chapters/{n}", a(handleChapter))
	ad := requireAdmin
	mux.HandleFunc("GET /api/trash", ad(handleTrash))
	mux.HandleFunc("DELETE /api/trash", ad(handleEmptyTrash))

	// 发现、从链接添加：往书库里加书，只有管理员能做
	mux.HandleFunc("POST /api/fetch", ad(handleFetch))
	mux.HandleFunc("GET /api/fetch", ad(handleFetchList))
	mux.HandleFunc("DELETE /api/fetch/{id}", ad(handleFetchCancel))

	mux.HandleFunc("GET /api/catalogs", ad(handleCatalogs))
	mux.HandleFunc("POST /api/catalogs", ad(handleAddCatalog))
	mux.HandleFunc("POST /api/catalogs/restore", ad(handleRestoreCatalogs))
	mux.HandleFunc("DELETE /api/catalogs/{id}", ad(handleDeleteCatalog))
	mux.HandleFunc("GET /api/catalogs/{id}/browse", ad(handleCatalogBrowse))
	mux.HandleFunc("GET /api/catalog-image", ad(handleCatalogImage))
}

// ---------- 网页文件清单（App 预先缓存，离线也能打开阅读器） ----------

var assetsCache struct {
	sync.Mutex
	at   time.Time
	body []byte
}

func handleAssets(w http.ResponseWriter, r *http.Request) {
	assetsCache.Lock()
	defer assetsCache.Unlock()
	if time.Since(assetsCache.at) > time.Minute || assetsCache.body == nil {
		type item struct {
			P string `json:"p"`
			S int64  `json:"s"`
			M int64  `json:"m"`
		}
		var files []item
		h := sha256.New()
		filepath.Walk(cfg.WebDir, func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(cfg.WebDir, p)
			rel = "/" + filepath.ToSlash(rel)
			if strings.HasPrefix(filepath.Base(p), ".") || rel == "/sw.js" {
				return nil
			}
			files = append(files, item{rel, info.Size(), info.ModTime().Unix()})
			fmt.Fprintf(h, "%s|%d|%d\n", rel, info.Size(), info.ModTime().Unix())
			return nil
		})
		assetsCache.body = []byte(toJSON(map[string]any{"version": hex.EncodeToString(h.Sum(nil))[:16], "files": files}))
		assetsCache.at = time.Now()
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(assetsCache.body)
}
