package main

// Windows 桌面版（1.0.0.32）：托盘里的“听书加速器”，用电脑的 CPU 帮 NAS 合成听书包（微软语音不行时），
// 也能一点打开书库看书。安装包不放进每一版镜像：Windows 版的 Python 和 sherpa-onnx 在 Dockerfile 的 desk 段
// （/opt/desktop/runtime，单独做成基础镜像，NAS 只下载一次），程序本身在 /app/desktop；
// 下载时书库现场拼成一个 zip（运行环境那部分先压好存在 /data/cache/desktop，之后直接拷贝压缩好的数据），
// 里面带着书库的地址（server.json），电脑上只要填配对码。
//
// GET /dl/shuku-desktop.zip           整个安装包（解压后双击“启动书库.cmd”）
// GET /dl/shuku-desktop.zip?part=app  只有程序（桌面版自己升级用，几十 KB）
// GET /api/listen/worker/model        本地语音模型的文件列表（桌面版第一次运行时从书库下载模型）
// GET /api/listen/worker/model/file?name=…

import (
	"archive/zip"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

func desktopRuntimeDir() string { return env("SHUKU_DESKTOP_RUNTIME", "/opt/desktop/runtime") }
func desktopAppDir() string     { return env("SHUKU_DESKTOP_APP", "/app/desktop") }

func desktopAvailable() bool {
	if _, err := os.Stat(filepath.Join(desktopRuntimeDir(), "python", "pythonw.exe")); err != nil {
		return false
	}
	_, err := os.Stat(filepath.Join(desktopAppDir(), "shuku_desktop.py"))
	return err == nil
}

// 桌面版的启动文件：控制台窗口一闪就关，程序在托盘里
const desktopLauncher = "@echo off\r\ncd /d \"%~dp0\"\r\nstart \"\" \"%~dp0python\\pythonw.exe\" \"%~dp0app\\shuku_desktop.py\"\r\n"

const desktopReadme = "书库桌面版（Windows）\r\n\r\n" +
	"1. 把这个文件夹放到一个固定的地方（比如 文档\\书库桌面版），不要放在压缩包里直接运行。\r\n" +
	"2. 双击“启动书库.cmd”。右下角托盘里出现书库的图标，浏览器会打开桌面版的设置页。\r\n" +
	"3. 在书库网页“设置 → 手机、手表、浏览器插件”里点“生成配对码”，把配对码填到设置页里。\r\n\r\n" +
	"连上以后：电脑开着、空闲时帮 NAS 合成听书包（微软语音不行的时候才用），电脑在用时自动放慢，用电池时不干活。\r\n" +
	"托盘菜单里可以打开书库、暂停加速、设开机自动启动。第一次加速时会从书库下载语音模型（约 170MB）。\r\n"

var desktopZipMu sync.Mutex

// 运行环境 + 程序，压好存起来（每个版本一份）；旧版本的删掉
func desktopBaseZip() (string, error) {
	desktopZipMu.Lock()
	defer desktopZipMu.Unlock()
	dir := filepath.Join(cfg.DataDir, "cache", "desktop")
	p := filepath.Join(dir, "base-"+deskSafe(version)+".zip")
	if _, err := os.Stat(p); err == nil {
		return p, nil
	}
	os.MkdirAll(dir, 0o755)
	if old, _ := filepath.Glob(filepath.Join(dir, "base-*.zip")); old != nil {
		for _, o := range old {
			os.Remove(o)
		}
	}
	tmp := p + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	zw := zip.NewWriter(f)
	err = addDirToZip(zw, desktopRuntimeDir(), "")
	if err == nil {
		err = addDesktopApp(zw)
	}
	if err == nil {
		err = zw.Close()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return "", err
	}
	return p, os.Rename(tmp, p)
}

func deskSafe(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '.' || r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
			return r
		}
		return '_'
	}, s)
}

func addDirToZip(zw *zip.Writer, root, prefix string) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		return addFileToZip(zw, p, prefix+filepath.ToSlash(rel))
	})
}

func addFileToZip(zw *zip.Writer, src, name string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, _ := in.Stat()
	h := &zip.FileHeader{Name: name, Method: zip.Deflate}
	if st != nil {
		h.Modified = st.ModTime()
	}
	w, err := zw.CreateHeader(h)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, in)
	return err
}

func addBytesToZip(zw *zip.Writer, name string, b []byte) error {
	w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: time.Now()})
	if err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

// 程序（app/ 下的 .py）、启动文件、说明、版本号
func addDesktopApp(zw *zip.Writer) error {
	ents, err := os.ReadDir(desktopAppDir())
	if err != nil {
		return err
	}
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".py") {
			continue
		}
		if err := addFileToZip(zw, filepath.Join(desktopAppDir(), e.Name()), "app/"+e.Name()); err != nil {
			return err
		}
	}
	if err := addBytesToZip(zw, "app/version.txt", []byte(version)); err != nil {
		return err
	}
	if err := addBytesToZip(zw, "启动书库.cmd", []byte(desktopLauncher)); err != nil {
		return err
	}
	return addBytesToZip(zw, "说明.txt", []byte(desktopReadme))
}

func handleDesktopZip(w http.ResponseWriter, r *http.Request) {
	if !desktopAvailable() {
		errJSON(w, 404, "这个书库里没有桌面版。用 GitHub Actions 打包的镜像才带桌面版")
		return
	}
	info := serverInfo(r)
	sj, _ := json.MarshalIndent(map[string]any{"name": info["name"], "id": info["id"], "lan_url": info["lan_url"], "remote_url": info["remote_url"], "origin": info["origin"]}, "", "  ")
	appOnly := r.URL.Query().Get("part") == "app"
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Cache-Control", "no-store")
	name := "shuku-desktop.zip"
	if appOnly {
		name = "shuku-desktop-app.zip"
	}
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	zw := zip.NewWriter(w)
	defer zw.Close()
	if appOnly {
		if err := addDesktopApp(zw); err != nil {
			return
		}
		addBytesToZip(zw, "app/server.json", sj)
		return
	}
	p, err := desktopBaseZip()
	if err != nil {
		errJSON(w, 500, "打包桌面版失败："+err.Error())
		return
	}
	zr, err := zip.OpenReader(p)
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	defer zr.Close()
	// 压好的数据原样拷过去，不用重新压缩
	for _, f := range zr.File {
		if err := zw.Copy(f); err != nil {
			return
		}
	}
	addBytesToZip(zw, "app/server.json", sj)
}

// 桌面版的版本信息（/api/clients 里的 desktop）
func desktopClientInfo() map[string]any {
	if !desktopAvailable() {
		return nil
	}
	return map[string]any{"version": version, "url": "/dl/shuku-desktop.zip", "app_url": "/dl/shuku-desktop.zip?part=app", "platform": "windows-x64"}
}

// ---------- 本地语音模型：桌面版第一次运行时从书库下载 ----------

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
	mux.HandleFunc("GET /dl/shuku-desktop.zip", handleDesktopZip)
	mux.HandleFunc("GET /api/listen/worker/model", requireAuth(handleModelList))
	mux.HandleFunc("GET /api/listen/worker/model/file", requireAuth(handleModelFile))
}
