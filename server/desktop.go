package main

// 书库 Windows 版（1.0.0.33）：打开就是书库的窗口，听书加速器在同一个程序里（desktop/shuku_desktop.py）。
// 安装程序不放进每一版镜像：Windows 版的 Python 和库在 Dockerfile 的 desk 段（/opt/desktop/runtime，单独做成基础镜像，
// NAS 只下载一次），程序在 /app/desktop；书库用 makensis（desk 段装的）现场做安装程序，带上书库的地址（server.json），
// 按版本、运行环境、地址缓存在 /data/cache/desktop。启动两分钟后先把内网、Tailscale 两个地址的做好，下载时不用等。
//
// GET /dl/shuku-setup.exe               安装程序（双击装好，桌面上有“书库”）
// GET /dl/shuku-desktop.zip?part=app    只有程序（运行环境没变时自动升级用，几十 KB）
// GET /api/listen/worker/model          本地语音模型的文件列表（第一次加速时从书库下载模型）
// GET /api/listen/worker/model/file?name=…

import (
	"archive/zip"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

func desktopRuntimeDir() string { return env("SHUKU_DESKTOP_RUNTIME", "/opt/desktop/runtime") }
func desktopAppDir() string     { return env("SHUKU_DESKTOP_APP", "/app/desktop") }

func desktopAvailable() bool {
	if _, err := os.Stat(filepath.Join(desktopRuntimeDir(), "python", "pythonw.exe")); err != nil {
		return false
	}
	if _, err := os.Stat(filepath.Join(desktopAppDir(), "shuku_desktop.py")); err != nil {
		return false
	}
	_, err := exec.LookPath("makensis")
	return err == nil
}

// 运行环境的编号（make_runtime.py 写的）：变了桌面版就要重新安装，没变只换程序
func desktopRuntimeID() string {
	b, _ := os.ReadFile(filepath.Join(desktopRuntimeDir(), "runtime.txt"))
	return strings.TrimSpace(string(b))
}

// 写进安装程序的书库地址
func desktopServerJSON(origin string) []byte {
	n := serverNetwork()
	b, _ := json.MarshalIndent(map[string]any{
		"name": getSetting("server_name", "我的书库"), "id": getSetting("server_id", ""),
		"lan_url": n.LanURL, "remote_url": n.RemoteURL, "origin": origin,
	}, "", "  ")
	return b
}

// 程序目录里装进去的文件：程序、图标、版本号、运行环境编号、书库地址
func desktopAppFiles(sj []byte) (map[string][]byte, error) {
	out := map[string][]byte{}
	ents, err := os.ReadDir(desktopAppDir())
	if err != nil {
		return nil, err
	}
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || n == "make_runtime.py" || !(strings.HasSuffix(n, ".py") || n == "shuku.ico") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(desktopAppDir(), n))
		if err != nil {
			return nil, err
		}
		out[n] = b
	}
	out["version.txt"] = []byte(version)
	out["runtime.txt"] = []byte(desktopRuntimeID())
	out["server.json"] = sj
	return out, nil
}

var desktopBuildMu sync.Mutex

func desktopCacheDir() string { return filepath.Join(cfg.DataDir, "cache", "desktop") }

// 做（或者拿缓存的）安装程序
func desktopInstaller(origin string) (string, error) {
	sj := desktopServerJSON(origin)
	h := sha1.Sum(sj)
	name := deskSafe(version) + "-" + deskSafe(desktopRuntimeID()) + "-" + hex.EncodeToString(h[:4]) + ".exe"
	p := filepath.Join(desktopCacheDir(), name)
	desktopBuildMu.Lock()
	defer desktopBuildMu.Unlock()
	if _, err := os.Stat(p); err == nil {
		return p, nil
	}
	os.MkdirAll(desktopCacheDir(), 0o755)
	// 别的版本的删掉
	if old, _ := filepath.Glob(filepath.Join(desktopCacheDir(), "*")); old != nil {
		for _, o := range old {
			if !strings.HasPrefix(filepath.Base(o), deskSafe(version)+"-") {
				os.RemoveAll(o)
			}
		}
	}
	stage, err := os.MkdirTemp(desktopCacheDir(), "app-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)
	files, err := desktopAppFiles(sj)
	if err != nil {
		return "", err
	}
	for n, b := range files {
		if err := os.WriteFile(filepath.Join(stage, n), b, 0o644); err != nil {
			return "", err
		}
	}
	// makensis 在脚本所在的目录里跑：路径都用绝对路径
	if a, err := filepath.Abs(stage); err == nil {
		stage = a
	}
	tmp := p + ".part"
	if a, err := filepath.Abs(tmp); err == nil {
		tmp = a
	}
	rt, _ := filepath.Abs(desktopRuntimeDir())
	start := time.Now()
	cmd := exec.Command("makensis", "-INPUTCHARSET", "UTF8", "-V2",
		"-DRUNTIME="+rt, "-DAPPSRC="+stage, "-DOUTFILE="+tmp, "-DVERSION="+version,
		filepath.Join(desktopAppDir(), "installer.nsi"))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var outBuf strings.Builder
	cmd.Stdout, cmd.Stderr = &outBuf, &outBuf
	if err := cmd.Start(); err != nil {
		return "", err
	}
	syscall.Setpriority(syscall.PRIO_PROCESS, cmd.Process.Pid, 10) // 低优先级：不抢书库自己的 CPU
	if err := cmd.Wait(); err != nil {
		os.Remove(tmp)
		msg := outBuf.String()
		if len(msg) > 600 {
			msg = msg[len(msg)-600:]
		}
		return "", &deskErr{"做安装程序失败：" + err.Error() + "\n" + msg}
	}
	if err := os.Rename(tmp, p); err != nil {
		return "", err
	}
	log.Printf("Windows 版安装程序做好了（%s，%s）", origin, time.Since(start).Round(time.Second))
	return p, nil
}

type deskErr struct{ s string }

func (e *deskErr) Error() string { return e.s }

func deskSafe(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '.' || r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
			return r
		}
		return '_'
	}, s)
}

func handleDesktopSetup(w http.ResponseWriter, r *http.Request) {
	if !desktopAvailable() {
		errJSON(w, 404, "这个书库里没有 Windows 版。用 GitHub Actions 打包的镜像才带")
		return
	}
	p, err := desktopInstaller(baseURL(r))
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/vnd.microsoft.portable-executable")
	w.Header().Set("Content-Disposition", `attachment; filename="shuku-setup.exe"; filename*=UTF-8''`+url.PathEscape("书库安装程序.exe"))
	w.Header().Set("Cache-Control", "no-store")
	http.ServeFile(w, r, p)
}

// 只有程序（自动升级）；以前的整包下载改成安装程序
func handleDesktopZip(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("part") != "app" {
		http.Redirect(w, r, "/dl/shuku-setup.exe", http.StatusFound)
		return
	}
	if !desktopAvailable() {
		errJSON(w, 404, "这个书库里没有 Windows 版")
		return
	}
	files, err := desktopAppFiles(desktopServerJSON(baseURL(r)))
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="shuku-desktop-app.zip"`)
	w.Header().Set("Cache-Control", "no-store")
	zw := zip.NewWriter(w)
	defer zw.Close()
	for n, b := range files {
		f, err := zw.CreateHeader(&zip.FileHeader{Name: "app/" + n, Method: zip.Deflate, Modified: time.Now()})
		if err != nil {
			return
		}
		f.Write(b)
	}
}

// Windows 版的版本信息（/api/clients 里的 desktop）
func desktopClientInfo() map[string]any {
	if !desktopAvailable() {
		return nil
	}
	return map[string]any{"version": version, "url": "/dl/shuku-setup.exe", "app_url": "/dl/shuku-desktop.zip?part=app",
		"runtime": desktopRuntimeID(), "platform": "windows-x64"}
}

// 启动两分钟后先把内网、Tailscale 地址的安装程序做好
func desktopLoop(ctx context.Context) {
	if !desktopAvailable() {
		return
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(2 * time.Minute):
	}
	n := serverNetwork()
	for _, o := range []string{n.LanURL, n.RemoteURL} {
		o = strings.TrimRight(o, "/")
		if o == "" || ctx.Err() != nil {
			continue
		}
		if _, err := desktopInstaller(o); err != nil {
			log.Printf("Windows 版安装程序：%v", err)
		}
	}
}

// ---------- 本地语音模型：第一次加速时从书库下载 ----------

func modelFiles() []map[string]any {
	var out []map[string]any
	filepath.WalkDir(embedModel, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(embedModel, p)
		if rel == "model.int8.onnx" {
			return nil
		}
		st, err := d.Info()
		if err != nil {
			return nil
		}
		out = append(out, map[string]any{"name": filepath.ToSlash(rel), "size": st.Size()})
		return nil
	})
	return out
}

func handleModelList(w http.ResponseWriter, r *http.Request) {
	fs := modelFiles()
	if len(fs) == 0 {
		errJSON(w, 404, "书库里没有本地语音模型")
		return
	}
	writeJSON(w, 200, map[string]any{"model": filepath.Base(embedModel), "files": fs})
}

func handleModelFile(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	for _, f := range modelFiles() {
		if f["name"] == name {
			w.Header().Set("Content-Type", "application/octet-stream")
			http.ServeFile(w, r, filepath.Join(embedModel, filepath.FromSlash(name)))
			return
		}
	}
	http.NotFound(w, r)
}

func routesDesktop(mux *http.ServeMux) {
	mux.HandleFunc("GET /dl/shuku-setup.exe", handleDesktopSetup)
	mux.HandleFunc("GET /dl/shuku-desktop.zip", handleDesktopZip)
	mux.HandleFunc("GET /api/listen/worker/model", requireAuth(handleModelList))
	mux.HandleFunc("GET /api/listen/worker/model/file", requireAuth(handleModelFile))
}
