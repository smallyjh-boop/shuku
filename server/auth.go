package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
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

func tokenFrom(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	if c, err := r.Cookie("shuku_token"); err == nil {
		return c.Value
	}
	// 图片、文件链接无法带请求头时，用 ?t= 传 token
	return r.URL.Query().Get("t")
}

var lastSeenMu sync.Mutex
var lastSeen = map[string]int64{}

func authed(r *http.Request) bool {
	t := tokenFrom(r)
	if t == "" {
		return false
	}
	var n int
	if db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE token=?`, t).Scan(&n); n == 0 {
		return false
	}
	lastSeenMu.Lock()
	if now()-lastSeen[t] > 300 {
		lastSeen[t] = now()
		go db.Exec(`UPDATE sessions SET last_seen=? WHERE token=?`, now(), t)
	}
	lastSeenMu.Unlock()
	return true
}

func requireAuth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authed(r) {
			errJSON(w, http.StatusUnauthorized, "请先登录")
			return
		}
		h(w, r)
	}
}

func newSession(w http.ResponseWriter, device string) string {
	t := randToken(32)
	db.Exec(`INSERT INTO sessions(token,device,created_at,last_seen) VALUES(?,?,?,?)`, t, device, now(), now())
	http.SetCookie(w, &http.Cookie{Name: "shuku_token", Value: t, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 3600 * 24 * 365})
	return t
}

func handleAuthState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{
		"setup":  getSetting("password_hash", "") != "",
		"authed": authed(r),
		"name":   getSetting("server_name", "我的书库"),
	})
}

func handleSetup(w http.ResponseWriter, r *http.Request) {
	if getSetting("password_hash", "") != "" {
		errJSON(w, 400, "已经设置过密码")
		return
	}
	var req struct{ Password, Device string }
	if readJSON(r, &req) != nil || len(req.Password) < 6 {
		errJSON(w, 400, "密码至少 6 位")
		return
	}
	h, _ := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	setSetting("password_hash", string(h))
	if getSetting("server_id", "") == "" {
		setSetting("server_id", randToken(8))
	}
	t := newSession(w, req.Device)
	writeJSON(w, 200, map[string]string{"token": t})
}

// 简单的登录失败限速
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

func handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	failMu.Lock()
	recent := fails[ip][:0]
	for _, t := range fails[ip] {
		if time.Since(t) < 10*time.Minute {
			recent = append(recent, t)
		}
	}
	fails[ip] = recent
	tooMany := len(recent) >= 8
	failMu.Unlock()
	if tooMany {
		errJSON(w, 429, "尝试次数过多，请 10 分钟后再试")
		return
	}
	var req struct{ Password, Device string }
	readJSON(r, &req)
	if bcrypt.CompareHashAndPassword([]byte(getSetting("password_hash", "")), []byte(req.Password)) != nil {
		failMu.Lock()
		fails[ip] = append(fails[ip], time.Now())
		failMu.Unlock()
		errJSON(w, 401, "密码不正确")
		return
	}
	t := newSession(w, req.Device)
	writeJSON(w, 200, map[string]string{"token": t})
}

func handleLogout(w http.ResponseWriter, r *http.Request) {
	db.Exec(`DELETE FROM sessions WHERE token=?`, tokenFrom(r))
	http.SetCookie(w, &http.Cookie{Name: "shuku_token", Value: "", Path: "/", MaxAge: -1})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func handleChangePassword(w http.ResponseWriter, r *http.Request) {
	var req struct{ Old, New string }
	readJSON(r, &req)
	if bcrypt.CompareHashAndPassword([]byte(getSetting("password_hash", "")), []byte(req.Old)) != nil {
		errJSON(w, 400, "原密码不正确")
		return
	}
	if len(req.New) < 6 {
		errJSON(w, 400, "新密码至少 6 位")
		return
	}
	h, _ := bcrypt.GenerateFromPassword([]byte(req.New), bcrypt.DefaultCost)
	setSetting("password_hash", string(h))
	db.Exec(`DELETE FROM sessions WHERE token<>?`, tokenFrom(r))
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// 内网 / Tailscale 地址切换时，用一次性交接码把登录状态带到另一个地址，免得重新输密码
var handoffMu sync.Mutex
var handoffs = map[string]time.Time{}

func handleHandoff(w http.ResponseWriter, r *http.Request) {
	code := randToken(16)
	handoffMu.Lock()
	for c, t := range handoffs { // 顺手清掉过期的
		if time.Since(t) > 2*time.Minute {
			delete(handoffs, c)
		}
	}
	handoffs[code] = time.Now()
	handoffMu.Unlock()
	writeJSON(w, 200, map[string]string{"code": code})
}

func handleExchange(w http.ResponseWriter, r *http.Request) {
	var req struct{ Code, Device string }
	readJSON(r, &req)
	handoffMu.Lock()
	t, ok := handoffs[req.Code]
	delete(handoffs, req.Code)
	handoffMu.Unlock()
	if !ok || time.Since(t) > 2*time.Minute {
		errJSON(w, 401, "交接码无效或已过期")
		return
	}
	tok := newSession(w, req.Device)
	writeJSON(w, 200, map[string]string{"token": tok})
}

// /api/ping：任何来源都可以访问，用来探测哪个地址可用
func handlePing(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]string{"id": getSetting("server_id", ""), "name": getSetting("server_name", "我的书库"), "app": "shuku"})
}

func hashPassword(p string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(p), bcrypt.DefaultCost)
	return string(h), err
}
