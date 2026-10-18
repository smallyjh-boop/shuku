package main

// 1.0.0.7：数据库升级、家人账号、进度冲突、回收站、扫描、备份恢复

import (
	"archive/tar"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 发请求：token 为空时不带登录
func call(t *testing.T, srv *httptest.Server, token, method, path string, body any) (int, map[string]any, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, srv.URL+path, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	json.Unmarshal(raw, &m)
	return resp.StatusCode, m, raw
}

func tokenFor(u *User) string { return newSession(httptest.NewRecorder(), "测试", u.ID) }

func addLibrary(t *testing.T, name, path, kind string) int64 {
	t.Helper()
	os.MkdirAll(path, 0o755)
	res, err := db.Exec(`INSERT INTO libraries(name,path,kind,template,sort) VALUES(?,?,?,?,0)`, name, path, kind, defaultBookTemplate)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

// 直接登记一本书（不需要真文件的测试用）
func addBookRow(t *testing.T, title, author string, libID int64, path string) (int64, int64) {
	t.Helper()
	id := nextBookID()
	if _, err := db.Exec(`INSERT INTO books(id,title,author,kind,status,created_at,updated_at) VALUES(?,?,?,'book','ok',?,?)`, id, title, author, now(), now()); err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec(`INSERT INTO files(book_id,library_id,path,format,size,hash,created_at) VALUES(?,?,?,'epub',1,?,?)`, id, libID, path, "h-"+title, now())
	if err != nil {
		t.Fatal(err)
	}
	fid, _ := res.LastInsertId()
	updateSearchKey(id)
	return id, fid
}

func bookIDs(t *testing.T, srv *httptest.Server, token, query string) map[int64]bool {
	t.Helper()
	code, m, raw := call(t, srv, token, "GET", "/api/books?limit=200"+query, nil)
	if code != 200 {
		t.Fatalf("书架 %d %s", code, raw)
	}
	out := map[int64]bool{}
	items, _ := m["items"].([]any)
	for _, it := range items {
		out[int64(it.(map[string]any)["id"].(float64))] = true
	}
	return out
}

// ---------- 从 1.0.0.6 的数据库升级 ----------

func TestMigrateFrom1006(t *testing.T) {
	dir := t.TempDir()
	cfg = Config{DataDir: dir, Inbox: filepath.Join(dir, "inbox"), WebDir: dir}
	p := filepath.Join(dir, "shuku.db")
	old, err := sql.Open("sqlite3", "file:"+p+"?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	sch, err := os.ReadFile("testdata/schema-1.0.0.6.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(string(sch)); err != nil {
		t.Fatal("旧结构：", err)
	}
	h, _ := hashPassword("oldpass1")
	ts := time.Now().Unix()
	for _, q := range []string{
		`INSERT INTO settings(key,value) VALUES('password_hash','` + h + `'),('server_id','srv1')`,
		`INSERT INTO libraries(id,name,path,kind,template,sort) VALUES(1,'图书','` + filepath.Join(dir, "books") + `','book','',0)`,
		`INSERT INTO books(id,title,author,kind,read_state,finished_at,created_at,updated_at) VALUES(1,'三体','刘慈欣','book','done',1700000000,1,1),(2,'球状闪电','刘慈欣','book','reading',0,1,1),(3,'没读过','某人','book','unread',0,1,1)`,
		`INSERT INTO files(id,book_id,library_id,path,format,size,hash,created_at) VALUES(1,1,1,'/x/a.epub','epub',1,'h1',1),(2,2,1,'/x/b.epub','epub',1,'h2',1),(3,3,1,'/x/c.epub','epub',1,'h3',1)`,
		`INSERT INTO progress(book_id,file_id,location,percent,device,updated_at) VALUES(2,2,'epubcfi(/6/4)',0.3,'手机',1700000100)`,
		`INSERT INTO bookmarks(book_id,location,label,text,note,color,created_at) VALUES(2,'epubcfi(/6/2)','第一章','原文','我的笔记','yellow',1700000050)`,
		`INSERT INTO reading_log(day,book_id,seconds) VALUES('2026-10-01',2,600)`,
		fmt.Sprintf(`INSERT INTO sessions(token,device,created_at,last_seen) VALUES('tok-old','手机',1700000000,%d)`, ts),
		`INSERT INTO ft_chunks(book_id,file_id,sec,part,label) VALUES(1,1,0,0,'x')`,
		`INSERT INTO ft(rowid,body) VALUES(1,'黑暗 森林')`,
		`INSERT INTO ft_files(file_id,hash,chunks,status,indexed_at) VALUES(1,'h1',1,'ok',1)`,
	} {
		if _, err := old.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	old.Close()

	if err := openDB(p); err != nil {
		t.Fatal("升级失败：", err)
	}
	if v := schemaVersion(); v != latestSchema() {
		t.Fatal("版本", v)
	}
	// 原来的密码变成第一个管理员
	u, err := getUser(1)
	if err != nil || !u.Admin() || !checkPassword(u, "oldpass1") {
		t.Fatal("管理员账号", u, err)
	}
	// 原来登录的设备不用重新登录
	if su := sessionUser("tok-old"); su == nil || su.ID != 1 {
		t.Fatal("旧登录失效了")
	}
	// 进度、书签、统计、读完状态都归管理员
	if pr := getProgress(1, 2); pr.Location != "epubcfi(/6/4)" || pr.Device != "手机" {
		t.Error("进度", pr)
	}
	if bm := bookmarksOf(1, 2); len(bm) != 1 || bm[0].Note != "我的笔记" || bm[0].Kind != "bookmark" {
		t.Error("书签", bm)
	}
	var sec int
	db.QueryRow(`SELECT seconds FROM reading_log WHERE user_id=1 AND day='2026-10-01' AND book_id=2`).Scan(&sec)
	if sec != 600 {
		t.Error("统计", sec)
	}
	if ub := getUserBook(1, 1); ub.ReadState != "done" || ub.FinishedAt != 1700000000 {
		t.Error("读完", ub)
	}
	if ub := getUserBook(1, 2); ub.ReadState != "reading" {
		t.Error("在读", ub)
	}
	if ub := getUserBook(1, 3); ub.ReadState != "unread" {
		t.Error("未读", ub)
	}
	// 全文索引搬到 fulltext.db
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM main.sqlite_master WHERE name IN ('ft','ft_chunks','ft_files')`).Scan(&n)
	if n != 0 {
		t.Error("主库里还有旧的全文索引表")
	}
	db.QueryRow(`SELECT COUNT(*) FROM fts.sqlite_master WHERE name IN ('ft','ft_chunks','ft_files')`).Scan(&n)
	if n != 3 {
		t.Error("fulltext.db 里没有索引表", n)
	}
	// 升级前做了备份，备份里是旧结构
	var up []backupInfo
	for _, b := range listBackups() {
		if b.Kind == "upgrade" {
			up = append(up, b)
		}
	}
	if len(up) != 1 {
		t.Fatal("升级前备份", listBackups())
	}
	if err := checkBackupFile(filepath.Join(backupDir(), up[0].Name)); err != nil {
		t.Error("备份文件不完整：", err)
	}
	bk, _ := sql.Open("sqlite3", filepath.Join(backupDir(), up[0].Name))
	bk.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('progress') WHERE name='user_id'`).Scan(&n)
	bk.Close()
	if n != 0 {
		t.Error("备份应该是升级前的样子")
	}
	// 再启动一次：不再升级、不再备份
	db.Close()
	if err := openDB(p); err != nil {
		t.Fatal(err)
	}
	cnt := 0
	for _, b := range listBackups() {
		if b.Kind == "upgrade" {
			cnt++
		}
	}
	if cnt != 1 {
		t.Error("重复备份了", cnt)
	}
	if pr := getProgress(1, 2); pr.Location != "epubcfi(/6/4)" {
		t.Error("重启后进度", pr)
	}
}

// ---------- 家人账号 ----------

func TestFamilyAccounts(t *testing.T) {
	dir := setupDB(t)
	lib1 := addLibrary(t, "图书", filepath.Join(dir, "books"), "book")
	lib2 := addLibrary(t, "大人的书", filepath.Join(dir, "adult"), "book")
	b1, _ := addBookRow(t, "小王子", "圣埃克苏佩里", lib1, filepath.Join(dir, "books", "a.epub"))
	b2, _ := addBookRow(t, "资本论", "马克思", lib2, filepath.Join(dir, "adult", "b.epub"))
	admin := addTestUser(t, "爸爸", "secret1", "admin")
	kid := addTestUser(t, "小明", "1234", "member", lib1)
	mom := addTestUser(t, "妈妈", "5678", "member")
	srv := httptest.NewServer(routes())
	defer srv.Close()
	ta, tk, tm := tokenFor(admin), tokenFor(kid), tokenFor(mom)

	// 登录：选名字 + 密码
	if code, m, _ := call(t, srv, "", "GET", "/api/auth/state", nil); code != 200 || m["users"] == nil {
		t.Error("登录页应该列出名字", m)
	}
	if code, _, _ := call(t, srv, "", "POST", "/api/auth/login", map[string]any{"name": "小明", "password": "wrong"}); code != 401 {
		t.Error("错误密码", code)
	}
	if code, m, _ := call(t, srv, "", "POST", "/api/auth/login", map[string]any{"user_id": kid.ID, "password": "1234"}); code != 200 || m["token"] == "" {
		t.Error("家人登录", code, m)
	}

	// 只看得到允许的书库
	if ids := bookIDs(t, srv, tk, ""); !ids[b1] || ids[b2] {
		t.Error("小明的书架", ids)
	}
	if ids := bookIDs(t, srv, tm, ""); !ids[b1] || !ids[b2] {
		t.Error("妈妈能看全部", ids)
	}
	for _, p := range []string{fmt.Sprintf("/api/books/%d", b2), fmt.Sprintf("/api/books/%d/read", b2), fmt.Sprintf("/api/books/%d/progress", b2)} {
		if code, _, _ := call(t, srv, tk, "GET", p, nil); code != 404 {
			t.Error("看不到的书应该 404：", p, code)
		}
	}
	if code, _, raw := call(t, srv, tk, "GET", "/api/libraries", nil); code != 200 || strings.Contains(string(raw), "大人的书") {
		t.Error("书库列表", string(raw))
	}
	// 家人不能改书目、删书、管账号、看管理设置
	if code, _, _ := call(t, srv, tk, "PATCH", fmt.Sprintf("/api/books/%d", b1), map[string]any{"title": "改掉"}); code != 403 {
		t.Error("家人改书名", code)
	}
	for _, c := range []struct{ m, p string }{{"DELETE", fmt.Sprintf("/api/books/%d", b1)}, {"GET", "/api/users"}, {"GET", "/api/backups"},
		{"GET", "/api/trash"}, {"POST", "/api/scan"}, {"PUT", "/api/settings"}, {"GET", "/api/storage"}} {
		if code, _, _ := call(t, srv, tk, c.m, c.p, map[string]any{}); code != 403 {
			t.Error("家人不应该能", c.m, c.p, code)
		}
	}
	if _, m, _ := call(t, srv, tk, "GET", "/api/settings", nil); m["ai"] != nil || m["inbox"] != nil {
		t.Error("家人拿到了管理设置", m)
	}
	if _, m, _ := call(t, srv, ta, "GET", "/api/settings", nil); m["ai"] == nil {
		t.Error("管理员的设置", m)
	}

	// 每个人的进度、读完状态分开
	call(t, srv, tk, "PUT", fmt.Sprintf("/api/books/%d/progress", b1), map[string]any{"location": "kid-loc", "percent": 0.5})
	call(t, srv, ta, "PUT", fmt.Sprintf("/api/books/%d/progress", b1), map[string]any{"location": "dad-loc", "percent": 0.1})
	if _, m, _ := call(t, srv, tk, "GET", fmt.Sprintf("/api/books/%d/progress", b1), nil); m["location"] != "kid-loc" {
		t.Error("小明的进度", m)
	}
	if _, m, _ := call(t, srv, ta, "GET", fmt.Sprintf("/api/books/%d/progress", b1), nil); m["location"] != "dad-loc" {
		t.Error("爸爸的进度", m)
	}
	if code, _, _ := call(t, srv, tk, "PATCH", fmt.Sprintf("/api/books/%d", b1), map[string]any{"read_state": "done"}); code != 200 {
		t.Error("家人标记读完", code)
	}
	if getUserBook(kid.ID, b1).ReadState != "done" || getUserBook(admin.ID, b1).ReadState == "done" {
		t.Error("读完状态没有分开")
	}
	call(t, srv, tk, "POST", fmt.Sprintf("/api/books/%d/bookmarks", b1), map[string]any{"location": "x", "label": "小明的书签"})
	if bm := bookmarksOf(admin.ID, b1); len(bm) != 0 {
		t.Error("爸爸看到了小明的书签", bm)
	}

	// 管理员放开书库：马上生效
	if code, _, _ := call(t, srv, ta, "PATCH", fmt.Sprintf("/api/users/%d", kid.ID), map[string]any{"libraries": []int64{lib1, lib2}}); code != 200 {
		t.Fatal(code)
	}
	if ids := bookIDs(t, srv, tk, ""); !ids[b2] {
		t.Error("放开后应该能看到", ids)
	}
	// 改密码：他别的设备要重新登录
	call(t, srv, ta, "PATCH", fmt.Sprintf("/api/users/%d", kid.ID), map[string]any{"password": "4321"})
	if code, _, _ := call(t, srv, tk, "GET", "/api/books", nil); code != 401 {
		t.Error("改密码后旧登录应该失效", code)
	}
	// 至少留一个管理员
	if code, _, _ := call(t, srv, ta, "PATCH", fmt.Sprintf("/api/users/%d", admin.ID), map[string]any{"role": "member"}); code != 400 {
		t.Error("最后一个管理员不能降级", code)
	}
	if code, _, _ := call(t, srv, ta, "DELETE", fmt.Sprintf("/api/users/%d", admin.ID), nil); code != 400 {
		t.Error("不能删自己", code)
	}
	// 删账号：他的进度、笔记一起删
	if code, _, _ := call(t, srv, ta, "DELETE", fmt.Sprintf("/api/users/%d", kid.ID), nil); code != 200 {
		t.Error("删账号", code)
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM progress WHERE user_id=?`, kid.ID).Scan(&n)
	if n != 0 {
		t.Error("删账号后进度还在")
	}
}

// ---------- 进度冲突 ----------

func TestProgressConflict(t *testing.T) {
	dir := setupDB(t)
	lib := addLibrary(t, "图书", filepath.Join(dir, "books"), "book")
	b, _ := addBookRow(t, "三体", "刘慈欣", lib, filepath.Join(dir, "books", "a.epub"))
	admin := addTestUser(t, "管理员", "secret1", "admin")
	srv := httptest.NewServer(routes())
	defer srv.Close()
	tok := tokenFor(admin)
	path := fmt.Sprintf("/api/books/%d/progress", b)
	_, m, _ := call(t, srv, tok, "PUT", path, map[string]any{"location": "A", "base": 0, "device": "手机"})
	t1 := int64(m["updated_at"].(float64))
	_, m, _ = call(t, srv, tok, "PUT", path, map[string]any{"location": "B", "base": t1, "device": "手机"})
	t2 := int64(m["updated_at"].(float64))
	if t2 <= t1 || m["conflict"] != nil {
		t.Fatal("正常保存", m)
	}
	// 平板还以为服务器是 t1：不能盖掉手机刚存的 B
	_, m, _ = call(t, srv, tok, "PUT", path, map[string]any{"location": "C", "base": t1, "device": "平板"})
	if m["conflict"] != true || m["current"].(map[string]any)["location"] != "B" {
		t.Fatal("应该报冲突", m)
	}
	if getProgress(admin.ID, b).Location != "B" {
		t.Error("冲突时不应该保存")
	}
	// 同一台设备连着翻页，上一次的回复还没到（base 落后一步）：不算冲突
	if _, m, _ = call(t, srv, tok, "PUT", path, map[string]any{"location": "B2", "base": t1, "device": "手机"}); m["conflict"] != nil || getProgress(admin.ID, b).Location != "B2" {
		t.Error("同一台设备", m)
	}
	// 位置一样不算冲突；不带 base 的旧客户端照常保存
	if _, m, _ = call(t, srv, tok, "PUT", path, map[string]any{"location": "B2", "base": t1, "device": "平板"}); m["conflict"] != nil {
		t.Error("同一位置", m)
	}
	if _, m, _ = call(t, srv, tok, "PUT", path, map[string]any{"location": "D"}); m["conflict"] != nil || getProgress(admin.ID, b).Location != "D" {
		t.Error("旧客户端", m)
	}
	// 位置没变、只报阅读时长：时间不变（别的设备不会误报冲突）
	cur := getProgress(admin.ID, b)
	if _, m, _ = call(t, srv, tok, "PUT", path, map[string]any{"location": cur.Location, "seconds": 30, "device": "电脑"}); int64(m["updated_at"].(float64)) != cur.UpdatedAt {
		t.Error("位置没变时不应该改时间", m, cur.UpdatedAt)
	}
	// 阅读时长：一次最多 4 小时
	call(t, srv, tok, "PUT", path, map[string]any{"location": "D", "seconds": 99999})
	var sec int
	db.QueryRow(`SELECT SUM(seconds) FROM reading_log WHERE user_id=?`, admin.ID).Scan(&sec)
	if sec != 14400+30 {
		t.Error("阅读时长上限", sec)
	}
}

// ---------- 删书进回收站、恢复 ----------

func writeBook(t *testing.T, p, content string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestTrashAndRestore(t *testing.T) {
	dir := setupDB(t)
	root := filepath.Join(dir, "books")
	addLibrary(t, "图书", root, "book")
	admin := addTestUser(t, "管理员", "secret1", "admin")
	srv := httptest.NewServer(routes())
	defer srv.Close()
	tok := tokenFor(admin)
	p := filepath.Join(root, "小说", "刘慈欣", "三体 - 刘慈欣.txt")
	writeBook(t, p, "第一章 科学边界\n"+strings.Repeat("汪淼看着倒计时。", 200))
	r := ingestFile(context.Background(), p, false)
	if r.BookID == 0 {
		t.Fatal(r)
	}
	id := r.BookID
	call(t, srv, tok, "PUT", fmt.Sprintf("/api/books/%d/progress", id), map[string]any{"location": "sec:3", "percent": 0.4})
	call(t, srv, tok, "POST", fmt.Sprintf("/api/books/%d/bookmarks", id), map[string]any{"location": "sec:3", "label": "第三章", "kind": "highlight", "text": "倒计时", "note": "重要"})

	if code, _, raw := call(t, srv, tok, "DELETE", fmt.Sprintf("/api/books/%d", id), nil); code != 200 {
		t.Fatal(code, string(raw))
	}
	if _, err := os.Stat(p); err == nil {
		t.Error("文件应该挪走了")
	}
	if _, err := os.Stat(filepath.Join(root, trashDir, filepath.Base(p))); err != nil {
		t.Error("回收站里没有文件", err)
	}
	if ids := bookIDs(t, srv, tok, ""); ids[id] {
		t.Error("删掉的书还在书架上")
	}
	if code, _, _ := call(t, srv, tok, "GET", fmt.Sprintf("/api/books/%d", id), nil); code != 404 {
		t.Error("删掉的书打不开才对", code)
	}
	_, m, raw := call(t, srv, tok, "GET", "/api/trash", nil)
	if bs, _ := m["books"].([]any); len(bs) != 1 {
		t.Fatal("回收站列表", string(raw))
	}
	// 扫描不会把回收站里的文件当新书
	scanLibraries(context.Background())
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM books WHERE deleted_at=0`).Scan(&n)
	if n != 0 {
		t.Error("扫描把回收站里的书登记回来了", n)
	}

	if code, _, raw := call(t, srv, tok, "POST", fmt.Sprintf("/api/books/%d/restore", id), nil); code != 200 {
		t.Fatal(code, string(raw))
	}
	if _, err := os.Stat(p); err != nil {
		t.Error("文件没搬回原处", err)
	}
	if code, _, _ := call(t, srv, tok, "GET", fmt.Sprintf("/api/books/%d", id), nil); code != 200 {
		t.Error("恢复后应该能打开", code)
	}
	if pr := getProgress(admin.ID, id); pr.Location != "sec:3" {
		t.Error("进度没了", pr)
	}
	if bm := bookmarksOf(admin.ID, id); len(bm) != 1 || bm[0].Note != "重要" || bm[0].Kind != "highlight" {
		t.Error("笔记没了", bm)
	}

	// 再删一次，清空回收站：彻底删掉
	call(t, srv, tok, "DELETE", fmt.Sprintf("/api/books/%d", id), nil)
	if code, _, _ := call(t, srv, tok, "DELETE", "/api/trash", nil); code != 200 {
		t.Fatal(code)
	}
	db.QueryRow(`SELECT COUNT(*) FROM books WHERE id=?`, id).Scan(&n)
	if n != 0 {
		t.Error("清空回收站后书目还在")
	}
	db.QueryRow(`SELECT COUNT(*) FROM progress WHERE book_id=?`, id).Scan(&n)
	if n != 0 {
		t.Error("清空回收站后进度还在")
	}
	if entries, _ := os.ReadDir(filepath.Join(root, trashDir)); len(entries) != 0 {
		t.Error("回收站文件夹没清空", len(entries))
	}
	// 删掉的编号不给新书用
	p2 := filepath.Join(root, "新书.txt")
	writeBook(t, p2, "另一本书的内容"+strings.Repeat("。", 100))
	if r2 := ingestFile(context.Background(), p2, false); r2.BookID <= id {
		t.Error("书目编号复用了", r2.BookID, id)
	}
}

// ---------- 扫描：改名移动的文件接回原书，找不到的先标记，书库没挂上时不动 ----------

func TestScanMovedAndMissing(t *testing.T) {
	dir := setupDB(t)
	root := filepath.Join(dir, "books")
	lib := addLibrary(t, "图书", root, "book")
	admin := addTestUser(t, "管理员", "secret1", "admin")
	paths := map[string]string{}
	for i, name := range []string{"甲", "乙", "丙", "丁"} {
		paths[name] = filepath.Join(root, name+".txt")
		writeBook(t, paths[name], fmt.Sprintf("第%d本书：%s\n%s", i, name, strings.Repeat(name+"的内容。", 100)))
	}
	scanLibraries(context.Background())
	ids := map[string]int64{}
	for name, p := range paths {
		var id int64
		db.QueryRow(`SELECT book_id FROM files WHERE path=?`, p).Scan(&id)
		ids[name] = id
		if ids[name] == 0 {
			t.Fatal("没登记", name)
		}
	}
	db.Exec(`INSERT INTO progress(user_id,book_id,file_id,location,percent,device,updated_at) VALUES(?,?,0,'sec:9',0.5,'',?)`, admin.ID, ids["甲"], now())

	// 在 File Station 里把“甲”改名挪到别的文件夹
	moved := filepath.Join(root, "整理过", "甲（新名字）.txt")
	os.MkdirAll(filepath.Dir(moved), 0o755)
	if err := os.Rename(paths["甲"], moved); err != nil {
		t.Fatal(err)
	}
	// “乙”被删掉了
	os.Remove(paths["乙"])
	scanLibraries(context.Background())
	var bid int64
	db.QueryRow(`SELECT book_id FROM files WHERE path=?`, moved).Scan(&bid)
	if bid != ids["甲"] {
		t.Error("改名的文件应该接回原来的书", bid, ids["甲"])
	}
	if getProgress(admin.ID, ids["甲"]).Location != "sec:9" {
		t.Error("改名后进度丢了")
	}
	var missing, n int64
	db.QueryRow(`SELECT missing_since FROM files WHERE path=?`, paths["乙"]).Scan(&missing)
	if missing == 0 {
		t.Error("找不到的文件应该标记")
	}
	db.QueryRow(`SELECT COUNT(*) FROM books WHERE id=?`, ids["乙"]).Scan(&n)
	if n != 1 {
		t.Error("找不到文件的书不应该马上删")
	}
	// 文件又回来了
	writeBook(t, paths["乙"], "第1本书：乙\n"+strings.Repeat("乙的内容。", 100))
	scanLibraries(context.Background())
	db.QueryRow(`SELECT missing_since FROM files WHERE path=?`, paths["乙"]).Scan(&missing)
	if missing != 0 {
		t.Error("文件回来后应该恢复", missing)
	}

	// 共享文件夹没挂上：整个书库目录不见了，不能把书都标成找不到
	os.Rename(root, root+".bak")
	scanLibraries(context.Background())
	db.QueryRow(`SELECT COUNT(*) FROM files WHERE missing_since>0`).Scan(&n)
	if n != 0 {
		t.Error("书库目录不在时不应该标记", n)
	}
	// 目录在、但是空的（挂载点还没挂上）：也不动
	os.MkdirAll(root, 0o755)
	scanLibraries(context.Background())
	db.QueryRow(`SELECT COUNT(*) FROM files WHERE missing_since>0`).Scan(&n)
	if n != 0 {
		t.Error("空的书库目录不应该标记", n)
	}
	_ = lib
	// 30 天后还没找回来才删
	os.RemoveAll(root)
	os.Rename(root+".bak", root)
	os.Remove(paths["丙"])
	scanLibraries(context.Background())
	db.Exec(`UPDATE files SET missing_since=? WHERE missing_since>0`, now()-31*24*3600)
	purgeExpired()
	db.QueryRow(`SELECT COUNT(*) FROM books WHERE id=?`, ids["丙"]).Scan(&n)
	if n != 0 {
		t.Error("找不到超过 30 天应该删掉")
	}
	db.QueryRow(`SELECT COUNT(*) FROM books WHERE id=?`, ids["丁"]).Scan(&n)
	if n != 1 {
		t.Error("别的书不应该受影响")
	}
}

// ---------- 备份、恢复 ----------

func TestBackupRestore(t *testing.T) {
	dir := setupDB(t)
	lib := addLibrary(t, "图书", filepath.Join(dir, "books"), "book")
	addBookRow(t, "备份前", "某人", lib, "/x/1.epub")
	p, err := backupDB("manual")
	if err != nil {
		t.Fatal(err)
	}
	if bs := listBackups(); len(bs) != 1 || bs[0].Kind != "manual" {
		t.Fatal(bs)
	}
	if err := checkBackupFile(p); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(dir, "bad.db")
	os.WriteFile(bad, []byte("not a database"), 0o644)
	if checkBackupFile(bad) == nil {
		t.Error("坏文件应该检查出来")
	}
	addBookRow(t, "备份后", "某人", lib, "/x/2.epub")
	// 准备恢复，然后“重启”
	if err := copyFile(p, restorePath()); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err := openDB(filepath.Join(dir, "t.db")); err != nil {
		t.Fatal(err)
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM books WHERE title='备份后'`).Scan(&n)
	if n != 0 {
		t.Error("没有恢复")
	}
	db.QueryRow(`SELECT COUNT(*) FROM books WHERE title='备份前'`).Scan(&n)
	if n != 1 {
		t.Error("恢复后数据不对")
	}
	if _, err := os.Stat(restorePath()); err == nil {
		t.Error("restore.db 应该用掉了")
	}
	// 恢复前的数据库留了一份
	found := false
	for _, b := range listBackups() {
		if b.Kind == "restore" {
			found = true
		}
	}
	if !found {
		t.Error("恢复前的数据库没留", listBackups())
	}
	// 每种备份留的份数有上限
	for i := 0; i < 8; i++ {
		os.WriteFile(filepath.Join(backupDir(), fmt.Sprintf("shuku-20200101-0000%02d-daily.db", i)), []byte("x"), 0o644)
	}
	pruneBackups()
	daily := 0
	for _, b := range listBackups() {
		if b.Kind == "daily" {
			daily++
		}
	}
	if daily != 7 {
		t.Error("每日备份应该留 7 份", daily)
	}
}

// ---------- 整理建议：拼音一样的不同作者不合并 ----------

func TestAuthorVariantsNotMergedByPinyin(t *testing.T) {
	dir := setupDB(t)
	lib := addLibrary(t, "图书", filepath.Join(dir, "books"), "book")
	addBookRow(t, "书一", "王伟", lib, "/x/1.epub")
	addBookRow(t, "书二", "王薇", lib, "/x/2.epub")
	addBookRow(t, "书三", "刘慈欣", lib, "/x/3.epub")
	addBookRow(t, "书四", "劉慈欣", lib, "/x/4.epub")
	addBookRow(t, "书五", "刘慈欣", lib, "/x/5.epub")
	for _, s := range computeSuggestions() {
		if s.Type != "author" {
			continue
		}
		if strings.Contains(s.Title, "王伟") || strings.Contains(s.Title, "王薇") {
			t.Error("王伟、王薇是两个人：", s.Title)
		}
	}
	found := false
	for _, s := range computeSuggestions() {
		if s.Type == "author" && strings.Contains(s.Title, "劉慈欣") {
			found = true
		}
	}
	if !found {
		t.Error("繁简写法应该建议统一")
	}
}

// ---------- 不用解压整个包，读出压缩包里的文件名和指定文件 ----------

func TestPeekArchiveTar(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.cbt")
	f, _ := os.Create(p)
	tw := tar.NewWriter(f)
	img := image.NewRGBA(image.Rect(0, 0, 4, 6))
	img.Set(0, 0, color.White)
	var pb bytes.Buffer
	png.Encode(&pb, img)
	add := func(name string, b []byte) {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(b))})
		tw.Write(b)
	}
	add("002.png", pb.Bytes())
	add("001.png", pb.Bytes())
	add("ComicInfo.xml", []byte(`<ComicInfo><Series>测试</Series></ComicInfo>`))
	tw.Close()
	f.Close()
	names, data, err := peekArchive(p, "cbt", func(n []string) []string { return []string{"ComicInfo.xml"} })
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 3 || !strings.Contains(string(data["ComicInfo.xml"]), "测试") {
		t.Error(names, data)
	}
	if imgs := imageNames(names); len(imgs) != 2 || imgs[0] != "001.png" {
		t.Error("图片排序", imgs)
	}
}
