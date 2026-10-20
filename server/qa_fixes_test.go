package main

import (
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 测试员找出的问题：书库、文件编号不复用；密码太长报错；进度百分比；搜索里的 % _；不存在的书传封面
func TestQAFixes(t *testing.T) {
	dir := setupDB(t)
	addLibrary(t, "图书", filepath.Join(dir, "books"), "book")
	admin := addTestUser(t, "管理员", "secret1", "admin")
	kid := addTestUser(t, "小红", "1234", "member")
	srv := httptest.NewServer(routes())
	defer srv.Close()
	tok := tokenFor(admin)

	// 删掉的书库编号不能给新书库：只能看那个书库的人不能因此看到新书库
	for _, d := range []string{"kids", "adult"} {
		os.MkdirAll(filepath.Join(dir, d), 0o755)
	}
	call(t, srv, tok, "POST", "/api/libraries", map[string]any{"name": "童书", "path": filepath.Join(dir, "kids")})
	var kidsID int64
	db.QueryRow(`SELECT id FROM libraries WHERE name='童书'`).Scan(&kidsID)
	call(t, srv, tok, "PATCH", fmt.Sprintf("/api/users/%d", kid.ID), map[string]any{"libraries": []int64{kidsID}})
	call(t, srv, tok, "DELETE", fmt.Sprintf("/api/libraries/%d", kidsID), nil)
	call(t, srv, tok, "POST", "/api/libraries", map[string]any{"name": "成人", "path": filepath.Join(dir, "adult")})
	var adultID int64
	db.QueryRow(`SELECT id FROM libraries WHERE name='成人'`).Scan(&adultID)
	if adultID == 0 || adultID == kidsID {
		t.Fatalf("书库编号复用了：%d %d", kidsID, adultID)
	}

	// 密码太长：报错，不存空密码
	if code, m, _ := call(t, srv, tok, "POST", "/api/users", map[string]any{"name": "长密码", "password": strings.Repeat("我的密码", 7), "role": "member"}); code != 400 {
		t.Errorf("密码太长应该 400：%d %v", code, m)
	}
	if code, _, _ := call(t, srv, tok, "PATCH", fmt.Sprintf("/api/users/%d", kid.ID), map[string]any{"name": "改了名", "password": "12"}); code != 400 {
		t.Error("密码太短应该 400")
	}
	if u, _ := getUser(kid.ID); u.Name != "小红" {
		t.Error("报错时不应该先改了名字", u.Name)
	}

	// 进度：百分比夹在 0～1；位置不变时百分比到头也标读完
	b, _ := addBookRow(t, "测试书", "某人", 1, filepath.Join(dir, "books", "a.epub"))
	path := fmt.Sprintf("/api/books/%d/progress", b)
	call(t, srv, tok, "PUT", path, map[string]any{"location": "L1", "percent": -3})
	if p := getProgress(admin.ID, b); p.Percent != 0 {
		t.Error("负的百分比", p.Percent)
	}
	call(t, srv, tok, "PUT", path, map[string]any{"location": "L3", "percent": 0.3})
	call(t, srv, tok, "PUT", path, map[string]any{"location": "L3", "percent": 0.999})
	if getUserBook(admin.ID, b).ReadState != "done" || getProgress(admin.ID, b).Percent != 0.999 {
		t.Error("位置不变、百分比到头也要标读完", getUserBook(admin.ID, b).ReadState, getProgress(admin.ID, b).Percent)
	}

	// 搜索里的 % _ 是普通字符
	_, m, _ := call(t, srv, tok, "GET", "/api/books?q=%25", nil)
	if items, _ := m["items"].([]any); len(items) != 0 {
		t.Error("搜 % 不应该搜出所有书", len(items))
	}

	// 不存在的书不能传封面
	if code, _, _ := call(t, srv, tok, "PUT", "/api/books/99999/cover", map[string]any{}); code != 404 {
		t.Error("不存在的书传封面", code)
	}

	// 文件名：emoji 太多时按字节截短
	if got := safeName(strings.Repeat("😀", 70) + ".epub"); len(got) > 240 || !strings.HasSuffix(got, ".epub") {
		t.Error("按字节截短", len(got))
	}
}
