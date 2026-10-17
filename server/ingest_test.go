package main

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// 同一本书的另一种格式入库时会去查已有书目。以前在遍历查询结果的同时又查了一次数据库，
// 数据库只有一个连接时整个书库就卡死了（网页一直“加载中”、显示连不上服务器）。
func TestIngestSameBookNoDeadlock(t *testing.T) {
	dir := setupDB(t)
	lib := filepath.Join(dir, "books")
	os.MkdirAll(lib, 0o755)
	cfg.InitLibraries = "图书:" + lib + ":book"
	initLibraries()

	body := "第一章 开头\n" + strings.Repeat("方鸿渐到了上海。", 80) + "\n"
	write := func(name, extra string) string {
		p := filepath.Join(lib, name)
		if err := os.WriteFile(p, []byte(body+extra), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	ingest := func(p string) IngestResult {
		t.Helper()
		done := make(chan IngestResult, 1)
		go func() { done <- ingestFile(context.Background(), p, false) }()
		select {
		case r := <-done:
			return r
		case <-time.After(15 * time.Second):
			t.Fatalf("入库 %s 卡住了（数据库死锁）", filepath.Base(p))
			return IngestResult{}
		}
	}

	r1 := ingest(write("围城 - 钱锺书.txt", ""))
	if r1.Status != "new" {
		t.Fatalf("第一本：%+v", r1)
	}
	// 另一种格式：合并到同一个书目
	r2 := ingest(write("围城 - 钱锺书.md", "\n（另一种格式）\n"))
	if r2.Status != "attached" || r2.BookID != r1.BookID {
		t.Fatalf("另一种格式应该合并到同一本书：%+v / %+v", r1, r2)
	}
	// 同一种格式、内容不同：另建一个书目，也不能卡住
	r3 := ingest(write("围城（精校版） - 钱锺书.txt", "\n（另一个版本）\n"))
	if r3.Status == "error" {
		t.Fatalf("第三本：%+v", r3)
	}
	var files int
	db.QueryRow(`SELECT COUNT(*) FROM files WHERE book_id=?`, r1.BookID).Scan(&files)
	if files != 2 {
		t.Errorf("同一本书应有 2 个文件，实际 %d", files)
	}

	// 遍历查询结果时，别的请求（比如网页刷新书架）照样能查数据库
	rows, err := db.Query(`SELECT id FROM books`)
	if err != nil {
		t.Fatal(err)
	}
	ok := make(chan bool, 1)
	go func() {
		var n int
		ok <- db.QueryRow(`SELECT COUNT(*) FROM files`).Scan(&n) == nil
	}()
	select {
	case <-ok:
	case <-time.After(10 * time.Second):
		t.Error("一个查询没读完时，别的查询被卡住了")
	}
	rows.Close()
}

// 给一本很大的书建全文索引时，网页的查询不能被一直卡住
func TestFulltextIndexDoesNotBlockReads(t *testing.T) {
	dir := setupDB(t)
	if !ftAvailable {
		t.Skip("需要 -tags sqlite_fts5")
	}
	p := filepath.Join(dir, "big.txt")
	var sb strings.Builder
	for i := 1; i <= 400; i++ {
		sb.WriteString("第" + strconv.Itoa(i) + "章 修仙\n")
		sb.WriteString(strings.Repeat("少年踏上修仙之路，御剑飞行，斩妖除魔。", 150))
		sb.WriteString("\n")
	}
	os.WriteFile(p, []byte(sb.String()), 0o644)
	res, _ := db.Exec(`INSERT INTO books(title,author,kind,created_at,updated_at) VALUES('修仙','某某','book',1,1)`)
	bid, _ := res.LastInsertId()
	res, _ = db.Exec(`INSERT INTO files(book_id,path,format,size,hash,created_at) VALUES(?,?,?,?,?,1)`, bid, p, "txt", sb.Len(), "fedcba9876543210fedcba9876543210")
	fid, _ := res.LastInsertId()
	f, _ := getFile(fid)

	done := make(chan error, 1)
	go func() { _, err := indexFile(f); done <- err }()
	// 建索引期间每 50 毫秒查一次，最慢的一次不能超过 2 秒
	var worst time.Duration
	for running := true; running; {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			running = false
		default:
			t0 := time.Now()
			var n int
			db.QueryRow(`SELECT COUNT(*) FROM books`).Scan(&n)
			if d := time.Since(t0); d > worst {
				worst = d
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	if worst > 2*time.Second {
		t.Errorf("建全文索引时查询最慢等了 %v", worst)
	}
	var chunks int
	db.QueryRow(`SELECT chunks FROM ft_files WHERE file_id=?`, fid).Scan(&chunks)
	if chunks < 100 {
		t.Errorf("索引块数不对：%d", chunks)
	}
}
