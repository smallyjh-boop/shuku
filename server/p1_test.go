package main

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// 测试用数据库
func setupDB(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cfg = Config{DataDir: dir, Inbox: filepath.Join(dir, "inbox"), WebDir: dir}
	os.MkdirAll(filepath.Join(dir, "covers"), 0o755)
	if err := openDB(filepath.Join(dir, "t.db")); err != nil {
		t.Fatal(err)
	}
	return dir
}

// 测试用账号：role 是 admin 或 member，libs 是家人能看的书库（空 = 全部）
func addTestUser(t *testing.T, name, pass, role string, libs ...int64) *User {
	t.Helper()
	h, _ := hashPassword(pass)
	if libs == nil {
		libs = []int64{}
	}
	res, err := db.Exec(`INSERT INTO users(name,pass,role,libraries,created_at) VALUES(?,?,?,?,?)`, name, h, role, toJSON(libs), now())
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	u, err := getUser(id)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestTokenize(t *testing.T) {
	got := strings.Join(tokenizeCJK("黑暗森林 Dark Forest 2"), " ")
	if got != "黑暗 暗森 森林 dark forest 2" {
		t.Error(got)
	}
	if q := ftsQuery("黑暗森林"); q != `"黑暗 暗森 森林"` {
		t.Error(q)
	}
	if q := ftsQuery("面壁者 罗辑"); q != `"面壁 壁者" AND "罗辑"` {
		t.Error(q)
	}
	// 繁体也能搜到简体
	if strings.Join(tokenizeCJK("黑暗森林"), " ") != strings.Join(tokenizeCJK("黑闇森林"), " ") && !strings.Contains(strings.Join(tokenizeCJK("嚴肅"), " "), "严") {
		t.Log("繁简：", tokenizeCJK("嚴肅"))
	}
}

func TestSnippet(t *testing.T) {
	text := strings.Repeat("前文", 40) + "宇宙就是一座黑暗森林，每个文明都是带枪的猎人" + strings.Repeat("后文", 60)
	s, m, ok := makeSnippet(text, "黑暗森林")
	if !ok || m != "黑暗森林" || !strings.Contains(s, "宇宙就是一座黑暗森林") || !strings.HasPrefix(s, "…") {
		t.Error(s, m, ok)
	}
	// 繁体原文、简体搜索
	s, m, ok = makeSnippet("這是一座黑暗森林。", "这是")
	if !ok || m != "這是" {
		t.Error(s, m, ok)
	}
}

func TestPalmdoc(t *testing.T) {
	if string(palmdocDecompress([]byte("abc"))) != "abc" {
		t.Error("literal")
	}
	if string(palmdocDecompress([]byte{0xE1})) != " a" {
		t.Error("space+char")
	}
	// "abcabc"：abc + 回指 3 字节长度 3 → 0x80|... dist=3,len=3 → v = 3<<3 | 0 = 0x18, 前缀 0x80
	if got := string(palmdocDecompress([]byte{'a', 'b', 'c', 0x80, 0x18})); got != "abcabc" {
		t.Error(got)
	}
}

func TestFulltextEndToEnd(t *testing.T) {
	dir := setupDB(t)
	if !ftAvailable {
		t.Skip("需要 -tags sqlite_fts5")
	}
	p := filepath.Join(dir, "a.txt")
	body := "第一章 开始\n" + strings.Repeat("平平无奇的一段话。", 50) + "\n第二章 森林\n" + strings.Repeat("填充文字。", 30) + "宇宙就是一座黑暗森林，每个文明都是带枪的猎人。\n"
	os.WriteFile(p, []byte(body), 0o644)
	res, _ := db.Exec(`INSERT INTO books(title,author,kind,created_at,updated_at) VALUES('三体Ⅱ','刘慈欣','book',1,1)`)
	bid, _ := res.LastInsertId()
	res, _ = db.Exec(`INSERT INTO files(book_id,path,format,size,hash,created_at) VALUES(?,?,?,?,?,1)`, bid, p, "txt", len(body), "0123456789abcdef0123456789abcdef")
	fid, _ := res.LastInsertId()
	f, _ := getFile(fid)
	n, err := indexFile(f)
	if err != nil || n == 0 {
		t.Fatal(n, err)
	}
	rec := httptest.NewRecorder()
	handleFulltext(rec, httptest.NewRequest("GET", "/api/fulltext?q="+urlQ("黑暗森林"), nil))
	var out struct {
		Books []ftBook `json:"books"`
	}
	json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Books) != 1 || len(out.Books[0].Hits) == 0 || out.Books[0].Hits[0].Sec != 1 || !strings.Contains(out.Books[0].Hits[0].Snippet, "黑暗森林") {
		t.Fatalf("%s", rec.Body.String())
	}
	// 不连续的字不应命中
	rec = httptest.NewRecorder()
	handleFulltext(rec, httptest.NewRequest("GET", "/api/fulltext?q="+urlQ("森林黑暗"), nil))
	json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Books) != 0 {
		t.Error("不该命中", rec.Body.String())
	}
	// 删除后索引清空
	removeFileIndex(fid)
	var left int
	db.QueryRow(`SELECT COUNT(*) FROM ft WHERE ft MATCH ?`, ftsQuery("黑暗森林")).Scan(&left)
	if left != 0 {
		t.Error("删除后仍有索引", left)
	}
}

func urlQ(s string) string { return url.QueryEscape(s) }

// ---------- 微软语音协议：用本地假服务器验证握手、分帧、音频解析 ----------

func fakeEdgeServer(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		// 现在的服务端只认 v2 令牌（<16 位>-<64 位>）和 2- 开头的版本号
		if !regexp.MustCompile(`^[0-9A-F]{16}-[0-9A-F]{64}$`).MatchString(q.Get("Sec-MS-GEC")) || !strings.HasPrefix(q.Get("Sec-MS-GEC-Version"), "2-") ||
			q.Get("TrustedClientToken") == "" || q.Get("ConnectionId") == "" || r.Header.Get("Origin") == "" || !strings.HasPrefix(r.Header.Get("Cookie"), "muid=") {
			http.Error(w, "forbidden", 403)
			return
		}
		key := r.Header.Get("Sec-WebSocket-Key")
		sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
		hj, _ := w.(http.Hijacker)
		conn, brw, _ := hj.Hijack()
		defer conn.Close()
		brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + base64.StdEncoding.EncodeToString(sum[:]) + "\r\n\r\n")
		brw.Flush()
		readMasked := func(br *bufio.Reader) string {
			var h [2]byte
			io.ReadFull(br, h[:])
			n := int(h[1] & 0x7F)
			if n == 126 {
				var b [2]byte
				io.ReadFull(br, b[:])
				n = int(binary.BigEndian.Uint16(b[:]))
			}
			var m [4]byte
			io.ReadFull(br, m[:])
			d := make([]byte, n)
			io.ReadFull(br, d)
			for i := range d {
				d[i] ^= m[i%4]
			}
			return string(d)
		}
		c1 := readMasked(brw.Reader)
		c2 := readMasked(brw.Reader)
		if !strings.Contains(c1, "Path:speech.config") || !strings.Contains(c2, "zh-CN-XiaoxiaoNeural") || !strings.Contains(c2, "rate='+20%'") || !strings.Contains(c2, "你好&amp;世界") {
			t.Errorf("客户端消息不对：%q / %q", c1, c2)
		}
		send := func(op byte, d []byte) {
			hdr := []byte{0x80 | op}
			if len(d) < 126 {
				hdr = append(hdr, byte(len(d)))
			} else {
				hdr = append(hdr, 126, byte(len(d)>>8), byte(len(d)))
			}
			conn.Write(append(hdr, d...))
		}
		send(1, []byte("X-RequestId:x\r\nPath:turn.start\r\n\r\n{}"))
		head := "X-RequestId:x\r\nContent-Type:audio/mpeg\r\nPath:audio\r\n"
		for _, chunk := range []string{"ID3-part1-", "part2"} {
			fr := make([]byte, 2)
			binary.BigEndian.PutUint16(fr, uint16(len(head)))
			fr = append(fr, head...)
			fr = append(fr, chunk...)
			send(2, fr)
		}
		send(1, []byte("X-RequestId:x\r\nPath:turn.end\r\n\r\n{}"))
		time.Sleep(50 * time.Millisecond)
	}))
}

func TestEdgeProtocol(t *testing.T) {
	srv := fakeEdgeServer(t)
	defer srv.Close()
	old := edgeWSS
	edgeWSS = "ws" + strings.TrimPrefix(srv.URL, "http") + "/consumer/speech/synthesize/readaloud/edge/v1"
	defer func() { edgeWSS = old }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	b, err := edgeSynth(ctx, "你好&世界", "zh-CN-XiaoxiaoNeural", 1.2)
	if err != nil || string(b) != "ID3-part1-part2" {
		t.Fatalf("%q %v", b, err)
	}
	if g := edgeGEC(); len(g) != 81 || strings.ToUpper(g) != g || g[16] != '-' {
		t.Error(g)
	}
}

// ---------- 联网补全：四个数据源用本地模拟接口 ----------

func TestSearchOnline(t *testing.T) {
	setupDB(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/j/subject_suggest", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"title":"三体Ⅱ：黑暗森林","url":"https://book.douban.com/subject/3066477/","pic":"https://img3.doubanio.com/view/subject/s/public/s3078482.jpg","author_name":"刘慈欣","year":"2008","type":"b","id":"3066477"},{"title":"黑暗森林法则","url":"x","author_name":"某某","year":"2015","type":"b","id":"1"}]`))
	})
	mux.HandleFunc("/subject/3066477/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<div id="info" class=""><span class="pl"> 作者</span>: <a href="#">刘慈欣</a><br/><span class="pl">出版社:</span> <a href="#">重庆出版社</a><br><span class="pl">出版年:</span> 2008-5<br/><span class="pl">ISBN:</span> 9787536693968<br/></div><div class="intro"><p>三体人在利用科技锁死了地球人的科学之后……</p><p>面壁计划开始。</p></div>`))
	})
	mux.HandleFunc("/search.json", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"docs":[{"key":"/works/OL1","title":"The Dark Forest","author_name":["Cixin Liu"],"first_publish_year":2008,"cover_i":123,"isbn":["9780765386694"]}]}`))
	})
	mux.HandleFunc("/books/v1/volumes", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"items":[{"volumeInfo":{"title":"黑暗森林","authors":["刘慈欣"],"publishedDate":"2008","description":"<b>地球文明</b>的命运","industryIdentifiers":[{"type":"ISBN_13","identifier":"9787536693968"}],"imageLinks":{"thumbnail":"http://books.google.com/x?zoom=1&edge=curl"}}}]}`))
	})
	mux.HandleFunc("/v0/search/subjects", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.Header.Get("User-Agent") == "" {
			w.WriteHeader(400)
			return
		}
		w.Write([]byte(`{"data":[{"id":12,"name":"進撃の巨人","name_cn":"进击的巨人","summary":"巨人","date":"2010-03-17","images":{"large":"https://lain.bgm.tv/l.jpg"},"tags":[{"name":"热血"}],"infobox":[{"key":"作者","value":"諫山創"}]}]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	doubanBase, olBase, googleBase, bangumiBase = srv.URL, srv.URL, srv.URL, srv.URL

	c := bestMatch(context.Background(), lookupQuery{Title: "三体Ⅱ·黑暗森林", Author: "刘慈欣", Kind: "book"})
	if c == nil || c.Author != "刘慈欣" || c.Score < 0.84 {
		t.Fatalf("没有找到合适的候选：%+v", c)
	}
	if c.Source == "豆瓣读书" && (c.ISBN != "9787536693968" || !strings.Contains(c.Description, "面壁计划") || !strings.Contains(c.Cover, "/l/public/")) {
		t.Errorf("豆瓣详情没有补全：%+v", c)
	}
	cands, errs := searchOnline(context.Background(), lookupQuery{Title: "进击的巨人 第3卷", Kind: "comic"})
	if len(errs) > 0 || len(cands) == 0 || cands[0].Source != "Bangumi" || cands[0].OrigTitle != "進撃の巨人" {
		t.Errorf("漫画：%v %+v", errs, cands)
	}
	// 毫不相干的书名不应高分
	scored := &Candidate{Title: "黑暗森林法则", Author: "某某"}
	scoreCandidate(scored, "三体Ⅱ·黑暗森林", "刘慈欣")
	if scored.Score >= 0.84 {
		t.Error("误匹配", scored.Score)
	}
}

// ---------- OPDS ----------

func TestOPDS(t *testing.T) {
	setupDB(t)
	addTestUser(t, "管理员", "secret1", "admin")
	db.Exec(`INSERT INTO books(title,author,kind,category,cover,created_at,updated_at,sort_key,author_key,search_key) VALUES('三体','刘慈欣','book','小说','v1',1,1,'santi','liucixin','三体 | santi | st')`)
	db.Exec(`INSERT INTO files(book_id,path,format,size,hash,created_at) VALUES(1,'/x/三体.epub','epub',100,'h',1)`)
	srv := httptest.NewServer(routes())
	defer srv.Close()
	get := func(p, pass string) (*http.Response, string) {
		req, _ := http.NewRequest("GET", srv.URL+p, nil)
		if pass != "" {
			req.SetBasicAuth("me", pass)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, string(b)
	}
	if resp, _ := get("/opds", ""); resp.StatusCode != 401 || resp.Header.Get("WWW-Authenticate") == "" {
		t.Error("未登录应要求认证", resp.StatusCode)
	}
	if resp, _ := get("/opds", "wrong"); resp.StatusCode != 401 {
		t.Error("错误密码应拒绝")
	}
	for _, p := range []string{"/opds", "/opds/books/all", "/opds/books/new", "/opds/categories", "/opds/authors", "/opds/search?q=st", "/opds/books/author?name=" + "%E5%88%98%E6%85%88%E6%AC%A3"} {
		resp, body := get(p, "secret1")
		if resp.StatusCode != 200 {
			t.Errorf("%s → %d", p, resp.StatusCode)
			continue
		}
		var f struct {
			Entries []struct {
				Title string `xml:"title"`
				Links []struct {
					Rel  string `xml:"rel,attr"`
					Href string `xml:"href,attr"`
				} `xml:"link"`
			} `xml:"entry"`
		}
		if err := xml.Unmarshal([]byte(body), &f); err != nil || len(f.Entries) == 0 {
			t.Errorf("%s：XML 不正确或为空 %v\n%s", p, err, body)
		}
		if strings.Contains(p, "books") || strings.Contains(p, "search") {
			found := false
			for _, l := range f.Entries[0].Links {
				if l.Rel == "http://opds-spec.org/acquisition" && strings.HasPrefix(l.Href, "/opds/file/1/") {
					found = true
				}
			}
			if !found {
				t.Errorf("%s 没有下载链接", p)
			}
		}
	}
	if resp, body := get("/opds/opensearch.xml", "secret1"); resp.StatusCode != 200 || !strings.Contains(body, "{searchTerms}") {
		t.Error("opensearch", body)
	}
}
