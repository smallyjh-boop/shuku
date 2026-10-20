package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func randToken(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func errJSON(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func readJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(http.MaxBytesReader(nil, r.Body, 4<<20)).Decode(v)
}

// 令牌从哪来：请求头、cookie、?t=（图片、文件链接没法带请求头）
func tokenFrom(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	if c, err := r.Cookie("shuku_token"); err == nil {
		return c.Value
	}
	return r.URL.Query().Get("t")
}

func tokenFromCookieOnly(r *http.Request) bool {
	if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") || r.URL.Query().Get("t") != "" {
		return false
	}
	_, err := r.Cookie("shuku_token")
	return err == nil
}

var lastSeenMu sync.Mutex
var lastSeen = map[string]int64{}

func authed(r *http.Request) bool { return optionalUser(r) != nil }

// 只靠 cookie 登录的写请求，要求来自书库自己的网页：防止同一台 NAS 上别的网页偷偷替你提交（CSRF）。
// 书库的网页跨地址（内网 ↔ Tailscale）请求时用的是 Authorization 请求头，不受影响
func sameOrigin(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	if !tokenFromCookieOnly(r) {
		return true
	}
	o := r.Header.Get("Origin")
	if o == "" {
		if ref := r.Header.Get("Referer"); ref != "" {
			o = ref
		} else {
			return true // 不是浏览器发的（浏览器的跨站写请求一定带 Origin）
		}
	}
	u, err := url.Parse(o)
	if err != nil {
		return false
	}
	host := r.Host
	if fh := r.Header.Get("X-Forwarded-Host"); fh != "" {
		host = fh
	}
	return strings.EqualFold(u.Host, host)
}

func requireAuth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		t := tokenFrom(r)
		u := sessionUser(t)
		if u == nil {
			errJSON(w, http.StatusUnauthorized, "请先登录")
			return
		}
		if !sameOrigin(r) {
			errJSON(w, http.StatusForbidden, "请求来源不对，请从书库的网页操作")
			return
		}
		lastSeenMu.Lock()
		if now()-lastSeen[t] > 300 {
			lastSeen[t] = now()
			go db.Exec(`UPDATE sessions SET last_seen=? WHERE token=?`, now(), t)
		}
		lastSeenMu.Unlock()
		h(w, withUser(r, u))
	}
}

// 新登录。w 为 nil 时不写 cookie：手机、手表、插件配对用请求头带令牌，
// 写了 cookie 反而会让浏览器网页也用上插件的登录（插件断开时网页跟着退出）
func newSession(w http.ResponseWriter, device string, uid int64) string {
	t := randToken(32)
	db.Exec(`INSERT INTO sessions(token,device,created_at,last_seen,user_id) VALUES(?,?,?,?,?)`, t, device, now(), now(), uid)
	if w != nil {
		http.SetCookie(w, &http.Cookie{Name: "shuku_token", Value: t, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 3600 * 24 * 365})
	}
	return t
}

func handleAuthState(w http.ResponseWriter, r *http.Request) {
	u := optionalUser(r)
	out := map[string]any{
		"setup":  firstAdmin() != nil,
		"authed": u != nil,
		"name":   getSetting("server_name", "我的书库"),
	}
	if u != nil {
		out["user"] = map[string]any{"id": u.ID, "name": u.Name, "role": u.Role, "libraries": u.Libraries, "has_password": u.HasPass}
	}
	// 登录页：多于一个人时显示名字让人选
	if users := loginUsers(); len(users) > 1 {
		out["users"] = users
	}
	writeJSON(w, 200, out)
}

func handleSetup(w http.ResponseWriter, r *http.Request) {
	if firstAdmin() != nil {
		errJSON(w, 400, "已经设置过密码")
		return
	}
	var req struct{ Password, Device, Name string }
	if readJSON(r, &req) != nil || len(req.Password) < 6 {
		errJSON(w, 400, "密码至少 6 位")
		return
	}
	if len(req.Password) > maxPassword {
		errJSON(w, 400, errPasswordLong)
		return
	}
	h, _ := hashPassword(req.Password)
	name := cleanUserName(req.Name)
	if name == "" {
		name = "管理员"
	}
	res, err := db.Exec(`INSERT INTO users(name,pass,role,libraries,created_at) VALUES(?,?,?,?,?)`, name, h, "admin", "[]", now())
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	uid, _ := res.LastInsertId()
	if getSetting("server_id", "") == "" {
		setSetting("server_id", randToken(8))
	}
	t := newSession(w, req.Device, uid)
	writeJSON(w, 200, map[string]string{"token": t})
}

// ---------- 登录失败限速（登录、配对码、OPDS 共用，按来源地址算） ----------

var failMu sync.Mutex
var fails = map[string][]time.Time{}

// 客户端地址：经过本机反向代理（tailscale serve 等）时取 X-Forwarded-For 里的第一个
func clientIP(r *http.Request) string {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}
	if p := net.ParseIP(ip); p != nil && p.IsLoopback() {
		if xf := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0]); xf != "" {
			return xf
		}
	}
	return ip
}

func tooManyFails(r *http.Request) bool {
	ip := clientIP(r)
	failMu.Lock()
	defer failMu.Unlock()
	recent := fails[ip][:0]
	for _, t := range fails[ip] {
		if time.Since(t) < 10*time.Minute {
			recent = append(recent, t)
		}
	}
	fails[ip] = recent
	return len(recent) >= 8
}

func noteFail(r *http.Request) {
	ip := clientIP(r)
	failMu.Lock()
	fails[ip] = append(fails[ip], time.Now())
	failMu.Unlock()
}

// 登录：{user_id 或 name（多人时）, password, device}。只有一个人时不用选人
func handleLogin(w http.ResponseWriter, r *http.Request) {
	if tooManyFails(r) {
		errJSON(w, 429, "尝试次数过多，请 10 分钟后再试")
		return
	}
	var req struct {
		UserID   int64  `json:"user_id"`
		Name     string `json:"name"`
		Password string `json:"password"`
		Device   string `json:"device"`
	}
	readJSON(r, &req)
	var u *User
	switch {
	case req.UserID > 0:
		u, _ = getUser(req.UserID)
	case strings.TrimSpace(req.Name) != "":
		u, _ = userByName(strings.TrimSpace(req.Name))
	default:
		u = firstAdmin()
	}
	if u == nil || !checkPassword(u, req.Password) {
		noteFail(r)
		errJSON(w, 401, "密码不正确")
		return
	}
	t := newSession(w, req.Device, u.ID)
	writeJSON(w, 200, map[string]any{"token": t, "user": map[string]any{"id": u.ID, "name": u.Name, "role": u.Role}})
}

func handleLogout(w http.ResponseWriter, r *http.Request) {
	db.Exec(`DELETE FROM sessions WHERE token=?`, tokenFrom(r))
	forgetSessions()
	http.SetCookie(w, &http.Cookie{Name: "shuku_token", Value: "", Path: "/", MaxAge: -1})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// 改自己的密码：别的设备上的登录都退出
func handleChangePassword(w http.ResponseWriter, r *http.Request) {
	u := me(r)
	var req struct{ Old, New string }
	readJSON(r, &req)
	if u.HasPass && bcrypt.CompareHashAndPassword([]byte(u.pass), []byte(req.Old)) != nil {
		errJSON(w, 400, "原密码不正确")
		return
	}
	min := 6
	if !u.Admin() {
		min = 4
	}
	if len(req.New) < min {
		errJSON(w, 400, "新密码太短")
		return
	}
	if len(req.New) > maxPassword {
		errJSON(w, 400, errPasswordLong)
		return
	}
	h, _ := hashPassword(req.New)
	db.Exec(`UPDATE users SET pass=? WHERE id=?`, h, u.ID)
	db.Exec(`DELETE FROM sessions WHERE user_id=? AND token<>?`, u.ID, tokenFrom(r))
	forgetSessions()
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// 忘记密码：compose 里加环境变量 SHUKU_RESET_PASSWORD=新密码，重启后第一个管理员的密码就是它（记得再删掉这一行）
func resetPasswordFromEnv() {
	p := env("SHUKU_RESET_PASSWORD", "")
	if p == "" {
		return
	}
	if len(p) < 6 {
		log.Printf("SHUKU_RESET_PASSWORD 至少 6 位，没有重置")
		return
	}
	h, _ := hashPassword(p)
	if a := firstAdmin(); a != nil {
		db.Exec(`UPDATE users SET pass=? WHERE id=?`, h, a.ID)
		log.Printf("已按 SHUKU_RESET_PASSWORD 重置管理员“%s”的密码。登录后请在 compose 里删掉这一行，再更新一次应用程序", a.Name)
	} else {
		db.Exec(`INSERT INTO users(name,pass,role,libraries,created_at) VALUES('管理员',?,'admin','[]',?)`, h, now())
		log.Printf("已按 SHUKU_RESET_PASSWORD 设置管理员密码")
	}
	logEvent("warn", 0, "管理员密码已按 SHUKU_RESET_PASSWORD 重置，请从 compose 里删掉这个环境变量")
}

// 内网 / Tailscale 地址切换时，用一次性交接码把登录状态带到另一个地址，免得重新输密码
var handoffMu sync.Mutex
var handoffs = map[string]handoff{}

type handoff struct {
	at  time.Time
	uid int64
}

func handleHandoff(w http.ResponseWriter, r *http.Request) {
	code := randToken(16)
	handoffMu.Lock()
	for c, h := range handoffs { // 顺手清掉过期的
		if time.Since(h.at) > 2*time.Minute {
			delete(handoffs, c)
		}
	}
	handoffs[code] = handoff{time.Now(), meID(r)}
	handoffMu.Unlock()
	writeJSON(w, 200, map[string]string{"code": code})
}

func handleExchange(w http.ResponseWriter, r *http.Request) {
	var req struct{ Code, Device string }
	readJSON(r, &req)
	handoffMu.Lock()
	h, ok := handoffs[req.Code]
	delete(handoffs, req.Code)
	handoffMu.Unlock()
	if !ok || time.Since(h.at) > 2*time.Minute {
		errJSON(w, 401, "交接码无效或已过期")
		return
	}
	tok := newSession(w, req.Device, h.uid)
	writeJSON(w, 200, map[string]string{"token": tok})
}

// /api/ping：任何来源都可以访问，用来探测哪个地址可用
func handlePing(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]string{"id": getSetting("server_id", ""), "name": getSetting("server_name", "我的书库"), "app": "shuku"})
}

// bcrypt 只认前 72 个字节，再长会出错（以前出错时存了空密码，账号就登不上了）
const maxPassword = 72
const errPasswordLong = "密码太长了（最多 72 个字母，约 24 个汉字）"

func hashPassword(p string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(p), bcrypt.DefaultCost)
	return string(h), err
}
