package main

// 1.0.0.9：翻译、扫描版 PDF 识别文字

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// 模拟微软翻译：/auth 发令牌，/translate 把每段变成“译:原文”
func fakeMicrosoft(t *testing.T, calls *int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/auth":
			io.WriteString(w, "aaa.eyJleHAiOjQxMDI0NDQ4MDB9.ccc") // exp: 2100 年
		case r.URL.Path == "/translate":
			if r.Header.Get("Authorization") != "Bearer aaa.eyJleHAiOjQxMDI0NDQ4MDB9.ccc" || r.URL.Query().Get("to") == "" {
				w.WriteHeader(401)
				return
			}
			atomic.AddInt32(calls, 1)
			var in []map[string]string
			json.NewDecoder(r.Body).Decode(&in)
			var out []map[string]any
			for _, x := range in {
				out = append(out, map[string]any{"translations": []map[string]string{{"text": "译:" + x["Text"], "to": r.URL.Query().Get("to")}}})
			}
			json.NewEncoder(w).Encode(out)
		default:
			w.WriteHeader(404)
		}
	}))
}

func TestTranslateMicrosoftAndCache(t *testing.T) {
	setupDB(t)
	var calls int32
	ms := fakeMicrosoft(t, &calls)
	defer ms.Close()
	msTranslateAuth, msTranslateBase = ms.URL+"/auth", ms.URL
	admin := addTestUser(t, "管理员", "secret1", "admin")
	srv := httptest.NewServer(routes())
	defer srv.Close()
	tok := tokenFor(admin)
	code, m, raw := call(t, srv, tok, "POST", "/api/translate", map[string]any{"texts": []string{"Hello", "", "World"}})
	if code != 200 || m["engine"] != "microsoft" {
		t.Fatal(code, string(raw))
	}
	tr := m["translations"].([]any)
	if tr[0] != "译:Hello" || tr[1] != "" || tr[2] != "译:World" {
		t.Error(tr)
	}
	// 再翻一次：全部来自缓存，不再请求微软
	call(t, srv, tok, "POST", "/api/translate", map[string]any{"texts": []string{"World", "Hello"}})
	if calls != 1 {
		t.Error("应该用缓存", calls)
	}
	// 译成别的语言是另一份缓存
	_, m, _ = call(t, srv, tok, "POST", "/api/translate", map[string]any{"texts": []string{"Hello"}, "to": "ja"})
	if calls != 2 || m["to"] != "ja" {
		t.Error("换语言", calls, m)
	}
	// 家人也能用
	kid := addTestUser(t, "小明", "1234", "member")
	if code, _, _ := call(t, srv, tokenFor(kid), "POST", "/api/translate", map[string]any{"texts": []string{"Hi"}}); code != 200 {
		t.Error("家人翻译", code)
	}
	if code, _, _ := call(t, srv, tok, "POST", "/api/translate", map[string]any{"texts": []string{}}); code != 400 {
		t.Error("空的应该 400", code)
	}
}

func TestTranslateAI(t *testing.T) {
	setupDB(t)
	var calls int32
	// 模拟 AI：把收到的数组每项变成“AI:原文”；第一次故意少返回一段，看能不能拆开重试
	ai := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		var req struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		var in []string
		json.Unmarshal([]byte(req.Messages[1].Content), &in)
		var out []string
		for _, x := range in {
			out = append(out, "AI:"+x)
		}
		if n == 1 && len(out) > 1 {
			out = out[:len(out)-1]
		}
		b, _ := json.Marshal(out)
		json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": "<think>想一想</think>```json\n" + string(b) + "\n```"}}}})
	}))
	defer ai.Close()
	setSetting("ai", toJSON(AIConfig{Enabled: true, BaseURL: ai.URL + "/v1", Model: "mock"}))
	setSetting("translate", toJSON(TranslateConfig{Engine: "ai", To: "zh-Hans"}))
	admin := addTestUser(t, "管理员", "secret1", "admin")
	srv := httptest.NewServer(routes())
	defer srv.Close()
	code, m, raw := call(t, srv, tokenFor(admin), "POST", "/api/translate", map[string]any{"texts": []string{"one", "two", "three"}})
	if code != 200 || m["engine"] != "ai" {
		t.Fatal(code, string(raw))
	}
	tr := m["translations"].([]any)
	if fmt.Sprint(tr) != "[AI:one AI:two AI:three]" {
		t.Error(tr)
	}
	// AI 没配置时改用微软翻译
	var mcalls int32
	ms := fakeMicrosoft(t, &mcalls)
	defer ms.Close()
	msTranslateAuth, msTranslateBase = ms.URL+"/auth", ms.URL
	setSetting("ai", toJSON(AIConfig{Enabled: false}))
	_, m, _ = call(t, srv, tokenFor(admin), "POST", "/api/translate", map[string]any{"texts": []string{"four"}})
	if m["engine"] != "microsoft" || m["translations"].([]any)[0] != "译:four" {
		t.Error("没有 AI 时", m)
	}
}

func TestParseTesseractTSV(t *testing.T) {
	tsv := "level\tpage_num\tblock_num\tpar_num\tline_num\tword_num\tleft\ttop\twidth\theight\tconf\ttext\n" +
		"4\t1\t1\t1\t1\t0\t100\t100\t300\t50\t-1\t\n" +
		"5\t1\t1\t1\t1\t1\t100\t100\t50\t50\t91\t你\n" +
		"5\t1\t1\t1\t1\t2\t150\t100\t50\t50\t90\t好\n" +
		"5\t1\t1\t1\t1\t3\t220\t100\t120\t50\t88\tworld\n" +
		"5\t1\t1\t1\t2\t1\t100\t200\t100\t40\t95\tHello\n" +
		"5\t1\t1\t1\t2\t2\t210\t200\t100\t40\t95\tthere\n"
	lines := parseTesseractTSV(tsv, 1000, 1000)
	if len(lines) != 2 || lines[0].Text != "你好 world" || lines[1].Text != "Hello there" {
		t.Fatal(lines)
	}
	if l := lines[0]; l.X != 0.1 || l.Y != 0.1 || l.W < 0.239 || l.W > 0.241 || len(l.Words) != 3 {
		t.Error(l)
	}
}

// 真的识别一页扫描版 PDF（本机装了 tesseract 和 mutool/pdftoppm 时才跑）
func TestOCRScannedPDF(t *testing.T) {
	if _, err := exec.LookPath("tesseract"); err != nil {
		t.Skip("没有 tesseract")
	}
	_, e1 := exec.LookPath("mutool")
	_, e2 := exec.LookPath("pdftoppm")
	if e1 != nil && e2 != nil {
		t.Skip("没有 mutool / pdftoppm")
	}
	dir := setupDB(t)
	tesseractBin = "tesseract"
	lib := addLibrary(t, "图书", filepath.Join(dir, "books"), "book")
	p := filepath.Join(dir, "books", "scan.pdf")
	b, _ := os.ReadFile("testdata/scan-en.pdf")
	os.WriteFile(p, b, 0o644)
	bid, fid := addBookRow(t, "扫描版", "某人", lib, p)
	db.Exec(`UPDATE files SET format='pdf' WHERE id=?`, fid)
	db.Exec(`UPDATE books SET language='en' WHERE id=?`, bid)
	admin := addTestUser(t, "管理员", "secret1", "admin")
	srv := httptest.NewServer(routes())
	defer srv.Close()
	tok := tokenFor(admin)
	code, m, raw := call(t, srv, tok, "GET", fmt.Sprintf("/api/files/%d/ocr/1", fid), nil)
	if code != 200 {
		t.Fatal(code, string(raw))
	}
	var page ocrPage
	json.Unmarshal(raw, &page)
	all := ""
	for _, l := range page.Lines {
		all += l.Text + "\n"
		if l.X <= 0 || l.X >= 1 || l.W <= 0 || l.Y <= 0 {
			t.Error("位置不对", l)
		}
	}
	if !strings.Contains(all, "12345") || !strings.Contains(strings.ToLower(all), "quick brown fox") {
		t.Error("识别结果：", all, m)
	}
	// 第二次直接用缓存（把 tesseract 换成不存在的程序也能拿到）
	tesseractBin = "/nonexistent/tesseract"
	defer func() { tesseractBin = "tesseract" }()
	if code, _, _ := call(t, srv, tok, "GET", fmt.Sprintf("/api/files/%d/ocr/1", fid), nil); code != 200 {
		t.Error("缓存", code)
	}
	// 这本 PDF 只有一页：第 2 页没有，回 404（以前把 poppler 的出错原样回成 500）
	if code, _, _ := call(t, srv, tok, "GET", fmt.Sprintf("/api/files/%d/ocr/2", fid), nil); code != 404 {
		t.Error("超出页数应该是 404", code)
	}
}
