package main

// 家人账号：每人一个名字和密码。管理员管书和设置；家人只能看书，
// 各自的进度、书签笔记、阅读统计、读完状态分开。管理员可以只给某个人开放部分书库（比如孩子只看童书）。

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type User struct {
	ID        int64   `json:"id"`
	Name      string  `json:"name"`
	Role      string  `json:"role"`      // admin | member
	Libraries []int64 `json:"libraries"` // 能看的书库，空表示全部
	HasPass   bool    `json:"has_password"`
	pass      string
}

func (u *User) Admin() bool { return u != nil && u.Role == "admin" }

// 这个人能看所有书库
func (u *User) AllLibraries() bool { return u == nil || u.Admin() || len(u.Libraries) == 0 }

type userKey struct{}

// 当前请求的人（requireAuth 之后一定有）
func me(r *http.Request) *User {
	if u, ok := r.Context().Value(userKey{}).(*User); ok {
		return u
	}
	return nil
}

func meID(r *http.Request) int64 {
	if u := me(r); u != nil {
		return u.ID
	}
	return 0
}

func withUser(r *http.Request, u *User) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), userKey{}, u))
}

func scanUser(sc interface{ Scan(...any) error }) (*User, error) {
	var u User
	var libs string
	if err := sc.Scan(&u.ID, &u.Name, &u.pass, &u.Role, &libs); err != nil {
		return nil, err
	}
	u.HasPass = u.pass != ""
	u.Libraries = []int64{}
	jsonUnmarshal(libs, &u.Libraries)
	return &u, nil
}

func getUser(id int64) (*User, error) {
	return scanUser(db.QueryRow(`SELECT id,name,pass,role,libraries FROM users WHERE id=?`, id))
}

func userByName(name string) (*User, error) {
	return scanUser(db.QueryRow(`SELECT id,name,pass,role,libraries FROM users WHERE name=?`, name))
}

func listUsers() []*User {
	rows, err := db.Query(`SELECT id,name,pass,role,libraries FROM users ORDER BY role='admin' DESC, id`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*User
	for rows.Next() {
		if u, err := scanUser(rows); err == nil {
			out = append(out, u)
		}
	}
	return out
}

// 第一个管理员（以前只有一个密码时，就是他）
func firstAdmin() *User {
	u, err := scanUser(db.QueryRow(`SELECT id,name,pass,role,libraries FROM users WHERE role='admin' ORDER BY id LIMIT 1`))
	if err != nil {
		return nil
	}
	return u
}

func checkPassword(u *User, pass string) bool {
	if u == nil || u.pass == "" || pass == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(u.pass), []byte(pass)) == nil
}

// ---------- 会话 ----------

const sessionMaxIdle = 180 * 24 * 3600 // 半年没用过的登录自动失效

var sessCache = struct {
	sync.Mutex
	m map[string]sessEntry
}{m: map[string]sessEntry{}}

type sessEntry struct {
	u  *User
	at time.Time
}

// 改了账号（密码、权限、删除）后清掉缓存，马上生效
func forgetSessions() {
	sessCache.Lock()
	sessCache.m = map[string]sessEntry{}
	sessCache.Unlock()
}

func sessionUser(t string) *User {
	if t == "" {
		return nil
	}
	sessCache.Lock()
	if e, ok := sessCache.m[t]; ok && time.Since(e.at) < 30*time.Second {
		sessCache.Unlock()
		return e.u
	}
	sessCache.Unlock()
	var uid, last int64
	if db.QueryRow(`SELECT user_id,last_seen FROM sessions WHERE token=?`, t).Scan(&uid, &last) != nil {
		return nil
	}
	if now()-last > sessionMaxIdle {
		db.Exec(`DELETE FROM sessions WHERE token=?`, t)
		return nil
	}
	u, err := getUser(uid)
	if err != nil {
		return nil
	}
	sessCache.Lock()
	if len(sessCache.m) > 500 {
		sessCache.m = map[string]sessEntry{}
	}
	sessCache.m[t] = sessEntry{u, time.Now()}
	sessCache.Unlock()
	return u
}

// 不登录也能访问的接口里，顺便看看是谁
func optionalUser(r *http.Request) *User { return sessionUser(tokenFrom(r)) }

func requireAdmin(h http.HandlerFunc) http.HandlerFunc {
	return requireAuth(func(w http.ResponseWriter, r *http.Request) {
		if !me(r).Admin() {
			errJSON(w, http.StatusForbidden, "只有管理员可以做这件事")
			return
		}
		h(w, r)
	})
}

// ---------- 能看哪些书 ----------

// 书目列表的过滤条件（b 是 books 的别名）：不在回收站，并且在这个人能看的书库里
func visibleSQL(u *User) (string, []any) {
	cond := `b.deleted_at=0`
	if u.AllLibraries() {
		return cond, nil
	}
	ph := make([]string, len(u.Libraries))
	args := make([]any, len(u.Libraries))
	for i, id := range u.Libraries {
		ph[i] = "?"
		args[i] = id
	}
	return cond + ` AND EXISTS(SELECT 1 FROM files vf WHERE vf.book_id=b.id AND vf.library_id IN (` + strings.Join(ph, ",") + `))`, args
}

func canSeeBook(u *User, bookID int64) bool {
	cond, args := visibleSQL(u)
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM books b WHERE b.id=? AND `+cond, append([]any{bookID}, args...)...).Scan(&n)
	return n > 0
}

func canSeeFile(u *User, f *File) bool {
	if f == nil {
		return false
	}
	return canSeeBook(u, f.BookID)
}

func canSeeLibrary(u *User, libID int64) bool {
	if u.AllLibraries() {
		return true
	}
	for _, id := range u.Libraries {
		if id == libID {
			return true
		}
	}
	return false
}

// ---------- 账号管理（管理员） ----------

type userView struct {
	ID        int64   `json:"id"`
	Name      string  `json:"name"`
	Role      string  `json:"role"`
	Libraries []int64 `json:"libraries"`
	HasPass   bool    `json:"has_password"`
	Devices   int     `json:"devices"`
	LastSeen  int64   `json:"last_seen"`
	Books     int     `json:"books"` // 读过的书
}

func handleUsers(w http.ResponseWriter, r *http.Request) {
	out := []userView{}
	for _, u := range listUsers() {
		v := userView{ID: u.ID, Name: u.Name, Role: u.Role, Libraries: u.Libraries, HasPass: u.HasPass}
		db.QueryRow(`SELECT COUNT(*),COALESCE(MAX(last_seen),0) FROM sessions WHERE user_id=?`, u.ID).Scan(&v.Devices, &v.LastSeen)
		db.QueryRow(`SELECT COUNT(*) FROM progress WHERE user_id=?`, u.ID).Scan(&v.Books)
		out = append(out, v)
	}
	writeJSON(w, 200, out)
}

type userReq struct {
	Name      *string  `json:"name"`
	Password  *string  `json:"password"`
	Role      *string  `json:"role"`
	Libraries *[]int64 `json:"libraries"`
}

func cleanUserName(s string) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > 20 {
		s = string(r[:20])
	}
	return s
}

func handleAddUser(w http.ResponseWriter, r *http.Request) {
	var req userReq
	if readJSON(r, &req) != nil || req.Name == nil || cleanUserName(*req.Name) == "" {
		errJSON(w, 400, "请填写名字")
		return
	}
	name := cleanUserName(*req.Name)
	role := "member"
	if req.Role != nil && *req.Role == "admin" {
		role = "admin"
	}
	if req.Password == nil || len(*req.Password) < 4 {
		errJSON(w, 400, "密码至少 4 位")
		return
	}
	hash, _ := hashPassword(*req.Password)
	libs := []int64{}
	if req.Libraries != nil {
		libs = *req.Libraries
	}
	res, err := db.Exec(`INSERT INTO users(name,pass,role,libraries,created_at) VALUES(?,?,?,?,?)`, name, hash, role, toJSON(libs), now())
	if err != nil {
		errJSON(w, 400, "已经有叫“"+name+"”的人了")
		return
	}
	id, _ := res.LastInsertId()
	logEvent("info", 0, "新增家人账号："+name)
	writeJSON(w, 200, map[string]any{"ok": true, "id": id})
}

func handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	u, err := getUser(id)
	if err != nil {
		errJSON(w, 404, "没有这个账号")
		return
	}
	var req userReq
	if readJSON(r, &req) != nil {
		errJSON(w, 400, "格式不正确")
		return
	}
	if req.Name != nil {
		n := cleanUserName(*req.Name)
		if n == "" {
			errJSON(w, 400, "名字不能为空")
			return
		}
		if _, err := db.Exec(`UPDATE users SET name=? WHERE id=?`, n, id); err != nil {
			errJSON(w, 400, "已经有叫“"+n+"”的人了")
			return
		}
	}
	if req.Role != nil && *req.Role != u.Role {
		role := "member"
		if *req.Role == "admin" {
			role = "admin"
		}
		if role == "member" && u.Admin() && adminCount() <= 1 {
			errJSON(w, 400, "至少要留一个管理员")
			return
		}
		db.Exec(`UPDATE users SET role=? WHERE id=?`, role, id)
	}
	if req.Password != nil && *req.Password != "" {
		if len(*req.Password) < 4 {
			errJSON(w, 400, "密码至少 4 位")
			return
		}
		h, _ := hashPassword(*req.Password)
		db.Exec(`UPDATE users SET pass=? WHERE id=?`, h, id)
		db.Exec(`DELETE FROM sessions WHERE user_id=? AND token<>?`, id, tokenFrom(r)) // 改了密码，别的设备要重新登录
	}
	if req.Libraries != nil {
		db.Exec(`UPDATE users SET libraries=? WHERE id=?`, toJSON(*req.Libraries), id)
	}
	forgetSessions()
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func adminCount() int {
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM users WHERE role='admin'`).Scan(&n)
	return n
}

// 删掉账号：他的登录、进度、书签笔记、统计都删掉
func handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	u, err := getUser(id)
	if err != nil {
		errJSON(w, 404, "没有这个账号")
		return
	}
	if u.ID == meID(r) {
		errJSON(w, 400, "不能删除自己")
		return
	}
	if u.Admin() && adminCount() <= 1 {
		errJSON(w, 400, "至少要留一个管理员")
		return
	}
	// 他的进度、笔记删了就没了：先备份一份，删错了能从备份恢复
	if _, err := backupDB("manual"); err != nil {
		errJSON(w, 500, "删除前备份数据库失败："+err.Error())
		return
	}
	for _, q := range []string{`DELETE FROM sessions WHERE user_id=?`, `DELETE FROM progress WHERE user_id=?`, `DELETE FROM bookmarks WHERE user_id=?`,
		`DELETE FROM reading_log WHERE user_id=?`, `DELETE FROM user_books WHERE user_id=?`, `DELETE FROM users WHERE id=?`} {
		db.Exec(q, id)
	}
	forgetSessions()
	logEvent("info", 0, "已删除账号："+u.Name)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// 登录页用：有哪几个人（只给名字）
func loginUsers() []map[string]any {
	out := []map[string]any{}
	for _, u := range listUsers() {
		out = append(out, map[string]any{"id": u.ID, "name": u.Name, "admin": u.Admin()})
	}
	return out
}

// ---------- 每个人自己的读书状态：在读 / 读完、评分、想读、阅读设置 ----------

type userBook struct {
	ReadState  string `json:"read_state"`
	FinishedAt int64  `json:"finished_at"`
	Rating     int    `json:"rating"`
	Want       bool   `json:"want"`
	Prefs      string `json:"prefs"`
}

func getUserBook(uid, bookID int64) userBook {
	ub := userBook{ReadState: "unread"}
	var want int
	db.QueryRow(`SELECT read_state,finished_at,rating,want,prefs FROM user_books WHERE user_id=? AND book_id=?`, uid, bookID).Scan(&ub.ReadState, &ub.FinishedAt, &ub.Rating, &want, &ub.Prefs)
	ub.Want = want == 1
	return ub
}

// 读完时记下读完的时间（已经记过就不改）；改回在读、未读时清掉
func setReadState(uid, bookID int64, state string) {
	if state != "reading" && state != "done" {
		state = "unread"
	}
	ts := now()
	fin := int64(0)
	if state == "done" {
		fin = ts
	}
	db.Exec(`INSERT INTO user_books(user_id,book_id,read_state,finished_at,updated_at) VALUES(?,?,?,?,?)
		ON CONFLICT(user_id,book_id) DO UPDATE SET read_state=excluded.read_state,
		finished_at=CASE WHEN excluded.read_state='done' THEN (CASE WHEN user_books.finished_at>0 THEN user_books.finished_at ELSE excluded.finished_at END) ELSE 0 END,
		updated_at=excluded.updated_at`, uid, bookID, state, fin, ts)
}

// PATCH /api/books/{id}/me：{read_state, rating, want, prefs, restart}
func handleUserBook(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	u := me(r)
	if !canSeeBook(u, id) {
		errJSON(w, 404, "找不到这本书")
		return
	}
	var req struct {
		ReadState *string          `json:"read_state"`
		Rating    *int             `json:"rating"`
		Want      *bool            `json:"want"`
		Prefs     *json.RawMessage `json:"prefs"`
		Restart   bool             `json:"restart"` // 从头读：进度清零、改回在读
	}
	if readJSON(r, &req) != nil {
		errJSON(w, 400, "格式不正确")
		return
	}
	db.Exec(`INSERT OR IGNORE INTO user_books(user_id,book_id,updated_at) VALUES(?,?,?)`, u.ID, id, now())
	if req.Restart {
		db.Exec(`DELETE FROM progress WHERE user_id=? AND book_id=?`, u.ID, id)
		setReadState(u.ID, id, "reading")
	} else if req.ReadState != nil {
		setReadState(u.ID, id, *req.ReadState)
		if *req.ReadState == "unread" {
			db.Exec(`DELETE FROM progress WHERE user_id=? AND book_id=?`, u.ID, id)
		}
	}
	if req.Rating != nil {
		v := *req.Rating
		if v < 0 {
			v = 0
		}
		if v > 5 {
			v = 5
		}
		db.Exec(`UPDATE user_books SET rating=?,updated_at=? WHERE user_id=? AND book_id=?`, v, now(), u.ID, id)
	}
	if req.Want != nil {
		db.Exec(`UPDATE user_books SET want=?,updated_at=? WHERE user_id=? AND book_id=?`, map[bool]int{true: 1, false: 0}[*req.Want], now(), u.ID, id)
	}
	if req.Prefs != nil {
		p := string(*req.Prefs)
		if p == "null" {
			p = ""
		}
		if len(p) > 4000 {
			errJSON(w, 400, "阅读设置太长")
			return
		}
		db.Exec(`UPDATE user_books SET prefs=?,updated_at=? WHERE user_id=? AND book_id=?`, p, now(), u.ID, id)
	}
	writeJSON(w, 200, getUserBook(u.ID, id))
}

// GET /api/books/states?ids=1,2,3 → 每本书的读完状态（手机清理读完的书用）
func handleBookStates(w http.ResponseWriter, r *http.Request) {
	u := me(r)
	out := map[string]any{}
	for _, s := range strings.Split(r.URL.Query().Get("ids"), ",") {
		id, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		if err != nil || id <= 0 || len(out) >= 500 {
			continue
		}
		var deleted int64
		if db.QueryRow(`SELECT deleted_at FROM books WHERE id=?`, id).Scan(&deleted) != nil {
			out[strconv.FormatInt(id, 10)] = map[string]any{"exists": false}
			continue
		}
		ub := getUserBook(u.ID, id)
		var pct float64
		db.QueryRow(`SELECT percent FROM progress WHERE user_id=? AND book_id=?`, u.ID, id).Scan(&pct)
		out[strconv.FormatInt(id, 10)] = map[string]any{"exists": deleted == 0 && canSeeBook(u, id), "read_state": ub.ReadState, "finished_at": ub.FinishedAt, "percent": pct}
	}
	writeJSON(w, 200, out)
}

// ---------- 自己的账号 ----------

func handleMe(w http.ResponseWriter, r *http.Request) {
	u := me(r)
	writeJSON(w, 200, map[string]any{"id": u.ID, "name": u.Name, "role": u.Role, "libraries": u.Libraries, "has_password": u.HasPass})
}
