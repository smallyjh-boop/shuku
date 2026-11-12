package main

// 在线升级
//
// NAS 服务器：镜像发在 ghcr.io，NAS 上已有的更新服务（Watchtower，泰好说装的 taihaoshuo-updater）
// 按容器标签每分钟检查一次，有新镜像就自动换上。网页上点“检查更新”时，通知它马上检查
// （POST {SHUKU_UPDATE_URL}/v1/update，带 SHUKU_UPDATE_TOKEN）。
//
// 手机 App、手表 App、浏览器插件：打包时和服务器放在同一个镜像里（/app/clients，带 clients.json），
// 各端从书库取最新版本号和安装包，不用登录 GitHub。服务器升级了，各端的新安装包也就跟着到了。

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

func clientsDir() string { return env("SHUKU_CLIENTS", "/app/clients") }

var clientFiles = map[string]string{ // 能下载的文件和类型
	"shuku-android.apk":   "application/vnd.android.package-archive",
	"shuku-wear.apk":      "application/vnd.android.package-archive",
	"shuku-extension.zip": "application/zip",
}

var clientsCache struct {
	sync.Mutex
	mod  time.Time
	data map[string]any
}

// 读 clients.json（文件很小，按修改时间缓存）。没有时返回 nil
func loadClients() map[string]any {
	p := filepath.Join(clientsDir(), "clients.json")
	st, err := os.Stat(p)
	if err != nil {
		return nil
	}
	clientsCache.Lock()
	defer clientsCache.Unlock()
	if clientsCache.data != nil && st.ModTime().Equal(clientsCache.mod) {
		return clientsCache.data
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	// 每一项补上下载地址；文件不在就去掉
	for _, k := range []string{"android", "wear", "extension"} {
		c, ok := m[k].(map[string]any)
		if !ok {
			continue
		}
		f, _ := c["file"].(string)
		if _, allowed := clientFiles[f]; !allowed {
			delete(m, k)
			continue
		}
		if _, err := os.Stat(filepath.Join(clientsDir(), f)); err != nil {
			delete(m, k)
			continue
		}
		c["url"] = "/dl/" + f
	}
	clientsCache.data, clientsCache.mod = m, st.ModTime()
	return m
}

// GET /api/clients：书库版本和各端最新安装包（不用登录：手机浏览器第一次装 App、插件检查更新都要用）
func handleClients(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "no-store")
	out := map[string]any{"app": "shuku", "server": version}
	for k, v := range loadClients() {
		if k == "android" || k == "wear" || k == "extension" {
			out[k] = v
		}
	}
	if d := desktopClientInfo(); d != nil {
		out["desktop"] = d
	}
	writeJSON(w, 200, out)
}

// GET /dl/{name}：下载安装包
func handleDownloadClient(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ct, ok := clientFiles[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	p := filepath.Join(clientsDir(), name)
	if _, err := os.Stat(p); err != nil {
		errJSON(w, 404, "这个书库里没有安装包。用 GitHub Actions 打包的镜像才带各端安装包")
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	http.ServeFile(w, r, p)
}

func updaterURL() string   { return strings.TrimRight(env("SHUKU_UPDATE_URL", ""), "/") }
func updaterToken() string { return env("SHUKU_UPDATE_TOKEN", "") }

// GET /api/update：版本信息，网页“设置 → 版本与升级”用
func handleUpdateInfo(w http.ResponseWriter, r *http.Request) {
	clients := map[string]any{}
	for k, v := range loadClients() {
		if k == "android" || k == "wear" || k == "extension" {
			clients[k] = v
		}
	}
	writeJSON(w, 200, map[string]any{
		"version": version,
		"updater": updaterURL() != "" && updaterToken() != "",
		"clients": clients,
	})
}

var updateClient = &http.Client{Timeout: 170 * time.Second}

// 通知更新服务马上检查（它会等检查、下载、换容器都做完才回复）。
// 有新版本时书库自己会被停掉换新，这次请求就等不到回复了，网页那边看到连接断开就按“正在重启”处理；
// 能收到回复，就说明书库没被换掉。
func triggerUpdate(ctx context.Context) map[string]any {
	u, tok := updaterURL(), updaterToken()
	if u == "" || tok == "" {
		return map[string]any{"ok": false, "code": "off", "reason": "还没开启一键升级：按说明用新的 compose（带更新服务的地址和令牌）重建一次书库"}
	}
	req, err := http.NewRequestWithContext(ctx, "POST", u+"/v1/update", nil)
	if err != nil {
		return map[string]any{"ok": false, "code": "failed", "reason": err.Error()}
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := updateClient.Do(req)
	if err != nil {
		var ne net.Error
		switch {
		case errors.Is(err, syscall.ECONNREFUSED):
			return map[string]any{"ok": false, "code": "down", "reason": "连不上更新服务（" + u + "）。请在 Container Station 里确认 taihaoshuo-updater 在运行，它在运行时每分钟也会自己检查一次新版本"}
		case errors.As(err, &ne) && ne.Timeout():
			return map[string]any{"ok": false, "code": "timeout", "reason": "更新服务很久没回复，可能正在下载新版本。过几分钟刷新页面看看"}
		}
		return map[string]any{"ok": false, "code": "failed", "reason": "更新服务没有正常回复（" + err.Error() + "）。它每分钟也会自己检查一次新版本"}
	}
	resp.Body.Close()
	switch {
	case resp.StatusCode == 401:
		return map[string]any{"ok": false, "code": "token", "reason": "更新服务的令牌不对，请检查 compose 里的 SHUKU_UPDATE_TOKEN"}
	case resp.StatusCode/100 != 2:
		return map[string]any{"ok": false, "code": "failed", "reason": "更新服务返回错误（" + http.StatusText(resp.StatusCode) + "）"}
	}
	// 更新服务回了“成功”只说明它检查过了：拉取失败（比如 ghcr.io 的令牌过期）它也会这样回。
	// 能走到这里说明书库没被换掉，所以只能说“没有换新版本”
	return map[string]any{"ok": true, "reason": "已检查，没有换新版本（当前 " + version + "）。如果确定发布了更新的版本，请在 Container Station 里看 taihaoshuo-updater 的日志，可能是拉取失败了"}
}

// 设置页“存储与状态”：更新服务有没有配好、连不连得上（只试着连一下端口，不触发更新）
func updateStatus(ctx context.Context) map[string]any {
	u, tok := updaterURL(), updaterToken()
	if u == "" || tok == "" {
		return map[string]any{"configured": false}
	}
	host := strings.TrimPrefix(strings.TrimPrefix(u, "http://"), "https://")
	if i := strings.Index(host, "/"); i >= 0 {
		host = host[:i]
	}
	if !strings.Contains(host, ":") {
		host += ":80"
	}
	d := net.Dialer{Timeout: 2 * time.Second}
	c, err := d.DialContext(ctx, "tcp", host)
	if err != nil {
		return map[string]any{"configured": true, "reachable": false, "url": u}
	}
	c.Close()
	return map[string]any{"configured": true, "reachable": true, "url": u}
}

// POST /api/update/check
func handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	// 书库收到停止信号（多半是更新服务来换新版本）就不再等回复，免得拖慢关闭
	defer context.AfterFunc(appCtx, cancel)()
	res := triggerUpdate(ctx)
	if appCtx.Err() != nil {
		panic(http.ErrAbortHandler) // 直接断开连接，网页按“正在重启”处理
	}
	writeJSON(w, 200, res)
}
