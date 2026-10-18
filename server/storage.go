package main

// 缓存和存储：
//   - 缓存（漫画解压、缩放过的页面、格式转换、封面小图、提取的正文）统一设上限，超过就删最久没用的
//   - 删书（清空回收站、30 天后）时一起删掉它的缓存
//   - 设置页显示各项占用、剩余空间、最近的错误，能一键清理缓存

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// 这几种缓存参与总量控制（听书的语音缓存另外限制在 400MB，见 tts.go）
var cacheKinds = []struct{ dir, name string }{
	{"arc", "漫画解压"}, {"pages", "漫画页面"}, {"conv", "格式转换"}, {"covers", "封面小图"}, {"text", "提取的正文"},
}

func cacheRoot() string { return filepath.Join(cfg.DataDir, "cache") }

func cacheLimitMB() int64 {
	n, _ := strconv.ParseInt(getSetting("cache_limit_mb", "5120"), 10, 64)
	if n < 256 {
		n = 256
	}
	return n
}

// 用到一个缓存时更新它的时间（NAS 上常常关掉了访问时间，只能自己记）
func touchCache(p string) {
	t := time.Now()
	if st, err := os.Stat(p); err == nil && t.Sub(st.ModTime()) > time.Hour {
		os.Chtimes(p, t, t)
	}
}

type cacheEntry struct {
	path string
	size int64
	used time.Time
}

// 一种缓存下的条目：漫画解压和页面按一本书一个文件夹算，其他按文件算
func cacheEntries(kind string) []cacheEntry {
	root := filepath.Join(cacheRoot(), kind)
	var out []cacheEntry
	if kind == "arc" || kind == "pages" {
		entries, _ := os.ReadDir(root)
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			p := filepath.Join(root, e.Name())
			var size int64
			filepath.Walk(p, func(_ string, info os.FileInfo, err error) error {
				if err == nil && !info.IsDir() {
					size += info.Size()
				}
				return nil
			})
			used := time.Time{}
			if st, err := os.Stat(p); err == nil {
				used = st.ModTime()
			}
			out = append(out, cacheEntry{p, size, used})
		}
		return out
	}
	filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			out = append(out, cacheEntry{p, info.Size(), info.ModTime()})
		}
		return nil
	})
	return out
}

func dirSize(p string) int64 {
	var n int64
	filepath.Walk(p, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			n += info.Size()
		}
		return nil
	})
	return n
}

var cacheTrimMu sync.Mutex

// 总量超过上限时，删最久没用的，删到上限的 80%
func trimCaches() (removed int, freed int64) {
	if !cacheTrimMu.TryLock() {
		return 0, 0
	}
	defer cacheTrimMu.Unlock()
	var all []cacheEntry
	var total int64
	for _, k := range cacheKinds {
		for _, e := range cacheEntries(k.dir) {
			all = append(all, e)
			total += e.size
		}
	}
	limit := cacheLimitMB() << 20
	if total <= limit {
		return 0, 0
	}
	sort.Slice(all, func(i, j int) bool { return all[i].used.Before(all[j].used) })
	for _, e := range all {
		if total <= limit*8/10 {
			break
		}
		// 正在用的漫画解压不能删（压缩包还开着）
		if strings.Contains(e.path, string(os.PathSeparator)+"arc"+string(os.PathSeparator)) && archiveInUse(e.path) {
			continue
		}
		if os.RemoveAll(e.path) == nil {
			total -= e.size
			freed += e.size
			removed++
		}
	}
	return removed, freed
}

var cacheWrites struct {
	sync.Mutex
	n int
}

// 写了新缓存：每写 50 次检查一次总量
func noteCacheWrite() {
	cacheWrites.Lock()
	cacheWrites.n++
	check := cacheWrites.n%50 == 0
	cacheWrites.Unlock()
	if check {
		go trimCaches()
	}
}

// 删一个文件相关的缓存（文件还在的时候调用，缓存键要读文件开头）
func removeFileCaches(f *File) {
	key := cacheKey(f.Path)
	os.RemoveAll(filepath.Join(cacheRoot(), "arc", key))
	os.RemoveAll(filepath.Join(cacheRoot(), "pages", key))
	matches, _ := filepath.Glob(filepath.Join(cacheRoot(), "conv", key+".*"))
	for _, m := range matches {
		os.Remove(m)
	}
	os.Remove(textCachePath(f))
	pageCache.Lock()
	delete(pageCache.m, f.ID)
	pageCache.Unlock()
	closePooledArchive(f.ID)
}

// 真正删掉一本书：数据库记录（连同每个人的进度、笔记、统计）、封面、缓存、全文索引
func purgeBook(id int64) {
	for _, f := range bookFiles(id) {
		removeFileCaches(&f)
		removeFileIndex(f.ID)
	}
	db.Exec(`DELETE FROM reading_log WHERE book_id=?`, id)
	db.Exec(`DELETE FROM files WHERE book_id=?`, id)
	db.Exec(`DELETE FROM books WHERE id=?`, id) // progress、bookmarks、user_books 跟着删（外键级联）
	os.Remove(coverPath(id))
	matches, _ := filepath.Glob(filepath.Join(cacheRoot(), "covers", strconv.FormatInt(id, 10)+"_*.jpg"))
	for _, m := range matches {
		os.Remove(m)
	}
}

// 每天一次：回收站里超过 30 天的书、文件找不到超过 30 天的记录，真正删掉；缓存超过上限的清一清
func purgeLoop(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(3 * time.Minute):
	}
	for {
		purgeExpired()
		trimCaches()
		select {
		case <-ctx.Done():
			return
		case <-time.After(12 * time.Hour):
		}
	}
}

const keepDays = 30

func purgeExpired() {
	cut := now() - keepDays*24*3600
	var ids []int64
	rows, err := db.Query(`SELECT id FROM books WHERE deleted_at>0 AND deleted_at<?`, cut)
	if err == nil {
		for rows.Next() {
			var id int64
			rows.Scan(&id)
			ids = append(ids, id)
		}
		rows.Close()
	}
	for _, id := range ids {
		// 回收站里的文件也删掉
		for _, f := range bookFiles(id) {
			if strings.Contains(f.Path, string(os.PathSeparator)+trashDir+string(os.PathSeparator)) {
				removeFileCaches(&f)
				os.Remove(f.Path)
			}
		}
		purgeBook(id)
	}
	// 找不到超过 30 天的文件：去掉这条记录；一本书的文件都没了就删掉这本书
	var missing []File
	rows, err = db.Query(`SELECT id,book_id,path FROM files WHERE missing_since>0 AND missing_since<?`, cut)
	if err == nil {
		for rows.Next() {
			var f File
			rows.Scan(&f.ID, &f.BookID, &f.Path)
			missing = append(missing, f)
		}
		rows.Close()
	}
	gone := 0
	for _, f := range missing {
		removeFileIndex(f.ID)
		db.Exec(`DELETE FROM files WHERE id=?`, f.ID)
		var left int
		db.QueryRow(`SELECT COUNT(*) FROM files WHERE book_id=?`, f.BookID).Scan(&left)
		if left == 0 {
			purgeBook(f.BookID)
			gone++
		}
	}
	if len(ids) > 0 || gone > 0 {
		logEvent("info", 0, "清理：回收站里超过 30 天的 "+strconv.Itoa(len(ids))+" 本书、文件找不到超过 30 天的 "+strconv.Itoa(gone)+" 本书已彻底删除")
		invalidateSuggestions()
	}
}

// ---------- 存储与状态（设置页） ----------

func diskFree(p string) (free, total int64) {
	var st syscall.Statfs_t
	if syscall.Statfs(p, &st) != nil {
		return 0, 0
	}
	return int64(st.Bavail) * int64(st.Bsize), int64(st.Blocks) * int64(st.Bsize)
}

func handleStorage(w http.ResponseWriter, r *http.Request) {
	caches := []map[string]any{}
	var cacheTotal int64
	for _, k := range cacheKinds {
		n := dirSize(filepath.Join(cacheRoot(), k.dir))
		cacheTotal += n
		caches = append(caches, map[string]any{"key": k.dir, "name": k.name, "size": n})
	}
	tts := dirSize(filepath.Join(cacheRoot(), "tts"))
	fileSize := func(p string) int64 {
		var n int64
		for _, suf := range []string{"", "-wal"} {
			if st, err := os.Stat(p + suf); err == nil {
				n += st.Size()
			}
		}
		return n
	}
	free, total := diskFree(cfg.DataDir)
	libs := []map[string]any{}
	for _, l := range listLibraries() {
		lf, lt := diskFree(l.Path)
		_, err := os.Stat(l.Path)
		libs = append(libs, map[string]any{"name": l.Name, "path": l.Path, "free": lf, "total": lt, "exists": err == nil})
	}
	var trashBooks int
	db.QueryRow(`SELECT COUNT(*) FROM books WHERE deleted_at>0`).Scan(&trashBooks)
	var missing int
	db.QueryRow(`SELECT COUNT(*) FROM books b WHERE b.deleted_at=0 AND NOT EXISTS(SELECT 1 FROM files f WHERE f.book_id=b.id AND f.missing_since=0)`).Scan(&missing)
	errors := []map[string]any{}
	rows, err := db.Query(`SELECT id,level,message,created_at FROM events WHERE level IN ('error','warn') ORDER BY id DESC LIMIT 12`)
	if err == nil {
		for rows.Next() {
			var id, at int64
			var lv, msg string
			rows.Scan(&id, &lv, &msg, &at)
			errors = append(errors, map[string]any{"id": id, "level": lv, "message": msg, "created_at": at})
		}
		rows.Close()
	}
	var backups int64
	for _, b := range listBackups() {
		backups += b.Size
	}
	writeJSON(w, 200, map[string]any{
		"caches": caches, "cache_total": cacheTotal, "cache_limit_mb": cacheLimitMB(), "tts": tts,
		"db": fileSize(dbPath), "fulltext": fileSize(ftDBPath), "covers": dirSize(filepath.Join(cfg.DataDir, "covers")), "backups": backups,
		"data_free": free, "data_total": total, "data_dir": cfg.DataDir, "libraries": libs,
		"trash_books": trashBooks, "missing_books": missing, "recent_problems": errors,
		"update": updateStatus(r.Context()),
	})
}

// 清理缓存：{kind: "arc" | "pages" | ... | "all"}，或 {limit_mb: 新上限}
func handleStorageClean(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Kind    string `json:"kind"`
		LimitMB int64  `json:"limit_mb"`
	}
	readJSON(r, &req)
	if req.LimitMB > 0 {
		if req.LimitMB < 256 {
			req.LimitMB = 256
		}
		setSetting("cache_limit_mb", strconv.FormatInt(req.LimitMB, 10))
	}
	var freed int64
	switch req.Kind {
	case "":
		_, freed = trimCaches()
	case "all", "tts", "arc", "pages", "conv", "covers", "text":
		kinds := []string{req.Kind}
		if req.Kind == "all" {
			kinds = []string{"arc", "pages", "conv", "covers", "text", "tts"}
		}
		for _, k := range kinds {
			for _, e := range cacheEntries(k) {
				if k == "arc" && archiveInUse(e.path) {
					continue
				}
				if os.RemoveAll(e.path) == nil {
					freed += e.size
				}
			}
		}
		pageCache.Lock()
		pageCache.m = map[int64]*comicInfo{}
		pageCache.Unlock()
	default:
		errJSON(w, 400, "不认识的缓存类型")
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "freed": freed})
}
