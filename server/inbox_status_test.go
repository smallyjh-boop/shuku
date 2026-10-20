package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

// 插件传上来的书：上传返回投递目录里的路径，凭它查入库结果；# 开头的文件名去掉 #（不然投递目录永远不处理）
func TestUploadInboxStatus(t *testing.T) {
	dir := setupDB(t)
	addLibrary(t, "图书", filepath.Join(dir, "books"), "book")
	admin := addTestUser(t, "管理员", "secret1", "admin")
	srv := httptest.NewServer(routes())
	defer srv.Close()
	tok := tokenFor(admin)
	text := "第一章 开始\n" + strings.Repeat("这是一本测试用的书。", 300)
	upload := func(name string) string {
		req, _ := http.NewRequest("POST", srv.URL+"/api/upload?path="+url.QueryEscape(name), strings.NewReader(text))
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var m map[string]any
		json.NewDecoder(resp.Body).Decode(&m)
		if resp.StatusCode != 200 {
			t.Fatalf("上传失败 %d %v", resp.StatusCode, m)
		}
		f, _ := m["file"].(string)
		return f
	}
	status := func(f string) map[string]any {
		_, m, _ := call(t, srv, tok, "GET", "/api/inbox/status?file="+url.QueryEscape(f), nil)
		return m
	}
	f := upload("#1 测试之书 - 某作者.txt")
	if f != "1 测试之书 - 某作者.txt" {
		t.Fatalf("开头的 # 要去掉：%q", f)
	}
	if s := status(f)["status"]; s != "pending" {
		t.Fatalf("还没处理时应该是 pending：%v", s)
	}
	processInbox(context.Background()) // 第一次只记下大小
	processInbox(context.Background())
	if m := status(f); (m["status"] != "done" && m["status"] != "review") || m["title"] == nil {
		t.Fatalf("应该入库了：%v", m)
	}
	f2 := upload("测试之书 - 某作者.txt")
	processInbox(context.Background())
	processInbox(context.Background())
	if m := status(f2); m["status"] != "duplicate" {
		t.Fatalf("同样的文件应该报已存在：%v", m)
	}
	if m := status("../../etc/passwd"); m["status"] != "unknown" {
		t.Fatalf("投递目录外的路径：%v", m)
	}
}
