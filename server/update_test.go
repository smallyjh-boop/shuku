package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestClientsAndDownload(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SHUKU_CLIENTS", dir)
	os.WriteFile(filepath.Join(dir, "shuku-android.apk"), []byte("PK-apk"), 0o644)
	os.WriteFile(filepath.Join(dir, "shuku-extension.zip"), []byte("PK-zip"), 0o644)
	os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("no"), 0o644)
	os.WriteFile(filepath.Join(dir, "clients.json"), []byte(`{"version":"1.2.3",
		"android":{"file":"shuku-android.apk","versionCode":7,"versionName":"1.2.3"},
		"wear":{"file":"shuku-wear.apk","versionCode":100007,"versionName":"1.2.3"},
		"extension":{"file":"shuku-extension.zip","version":"1.2.3"}}`), 0o644)
	mux := routes()

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/clients", nil))
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != 200 || out["server"] != version {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	a, _ := out["android"].(map[string]any)
	if a == nil || a["url"] != "/dl/shuku-android.apk" || a["versionCode"].(float64) != 7 {
		t.Errorf("手机 App 信息不对：%v", out["android"])
	}
	if _, ok := out["wear"]; ok {
		t.Error("手表安装包不存在时不该列出来")
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("插件要能跨域读取版本")
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/dl/shuku-android.apk", nil))
	if rec.Code != 200 || rec.Body.String() != "PK-apk" || !strings.Contains(rec.Header().Get("Content-Type"), "android.package-archive") {
		t.Errorf("下载 APK：%d %q %s", rec.Code, rec.Body.String(), rec.Header().Get("Content-Type"))
	}
	for _, bad := range []string{"/dl/secret.txt", "/dl/clients.json", "/dl/..%2Fsecret.txt"} {
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", bad, nil))
		if rec.Code == 200 {
			t.Errorf("%s 不该能下载", bad)
		}
	}
}

func TestTriggerUpdate(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Method + " " + r.URL.Path + " " + r.Header.Get("Authorization")
		if r.Header.Get("Authorization") != "Bearer right" {
			w.WriteHeader(401)
			return
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	t.Setenv("SHUKU_UPDATE_URL", "")
	if r := triggerUpdate(context.Background()); r["code"] != "off" {
		t.Errorf("没配置时：%v", r)
	}
	t.Setenv("SHUKU_UPDATE_URL", srv.URL+"/")
	t.Setenv("SHUKU_UPDATE_TOKEN", "right")
	if r := triggerUpdate(context.Background()); r["ok"] != true || got != "POST /v1/update Bearer right" {
		t.Errorf("正常检查：%v（%s）", r, got)
	}
	t.Setenv("SHUKU_UPDATE_TOKEN", "wrong")
	if r := triggerUpdate(context.Background()); r["code"] != "token" {
		t.Errorf("令牌错：%v", r)
	}
	srv.Close()
	if r := triggerUpdate(context.Background()); r["code"] != "down" {
		t.Errorf("更新服务没开：%v", r)
	}

	// 接上了却断开（比如端口转发不通）：不能当成“正在重启”
	drop := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _, _ := w.(http.Hijacker).Hijack()
		c.Close()
	}))
	defer drop.Close()
	t.Setenv("SHUKU_UPDATE_URL", drop.URL)
	if r := triggerUpdate(context.Background()); r["code"] != "failed" || r["ok"] != false {
		t.Errorf("连接断开：%v", r)
	}

	// 一直不回复：超时
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer slow.Close()
	old := updateClient
	updateClient = &http.Client{Timeout: 100 * time.Millisecond}
	defer func() { updateClient = old }()
	t.Setenv("SHUKU_UPDATE_URL", slow.URL)
	if r := triggerUpdate(context.Background()); r["code"] != "timeout" {
		t.Errorf("超时：%v", r)
	}
}
