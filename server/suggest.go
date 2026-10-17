package main

// 整理建议：合并文件夹、合并作者、系列归拢、同书多格式、重复文件、未按规范命名、空文件夹

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

type SugItem struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type Suggestion struct {
	Key    string    `json:"key"`
	Type   string    `json:"type"`
	Title  string    `json:"title"`
	Detail string    `json:"detail"`
	Items  []SugItem `json:"items"`
	Count  int       `json:"count"`
	apply  func(batch string) (int, error)
}

func sugKey(parts ...string) string {
	h := sha1.Sum([]byte(strings.Join(parts, "\x00")))
	return parts[0] + ":" + hex.EncodeToString(h[:])[:12]
}

var nationRe = regexp.MustCompile(`^[\[【(（〔][^\]】)）〕]{1,6}[\]】)）〕]\s*`)

func ignored() map[string]bool {
	m := map[string]bool{}
	rows, err := db.Query(`SELECT key FROM ignored_suggestions`)
	if err != nil {
		return m
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		rows.Scan(&k)
		m[k] = true
	}
	return m
}

// 名字归一：去空格标点、转小写、繁转简、汉字转拼音。"刘慈欣" "刘 慈欣" "Liu Cixin" 都得到 liucixin
func normName(s string) string {
	s = toSimplified(strings.ToLower(strings.TrimSpace(s)))
	s = nationRe.ReplaceAllString(s, "") // 去掉 "[美]" "（日）" 这样的国籍前缀
	for _, p := range []string{"（", "(", "[", "【"} {
		if i := strings.Index(s, p); i > 0 {
			s = s[:i] // 去掉末尾的括号说明
		}
	}
	s = strings.TrimSpace(strings.TrimRight(s, "著编译"))
	f, _ := pinyinOf(strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return -1
	}, s))
	return f
}

// 文件夹名归一：再去掉末尾的 " (2)"、"副本"、"-1"
func normDir(s string) string {
	s = strings.TrimSpace(s)
	for _, suf := range []string{" - 副本", "副本", " copy", " - copy"} {
		s = strings.TrimSuffix(strings.ToLower(s), suf)
	}
	if i := strings.LastIndexAny(s, "(（"); i > 0 && strings.IndexFunc(s[i:], unicode.IsDigit) >= 0 && len(s)-i <= 6 {
		s = s[:i]
	}
	return normName(s)
}

type bookLite struct {
	id                    int64
	title, author, series string
	kind, status          string
	files                 []File
}

func loadBooksLite() []bookLite {
	rows, err := db.Query(`SELECT id,title,author,series,kind,status FROM books`)
	if err != nil {
		return nil
	}
	var out []bookLite
	idx := map[int64]int{}
	for rows.Next() {
		var b bookLite
		rows.Scan(&b.id, &b.title, &b.author, &b.series, &b.kind, &b.status)
		idx[b.id] = len(out)
		out = append(out, b)
	}
	rows.Close()
	frows, err := db.Query(`SELECT id,book_id,library_id,path,format,size,hash FROM files`)
	if err != nil {
		return out
	}
	defer frows.Close()
	for frows.Next() {
		var f File
		frows.Scan(&f.ID, &f.BookID, &f.LibraryID, &f.Path, &f.Format, &f.Size, &f.Hash)
		if i, ok := idx[f.BookID]; ok {
			out[i].files = append(out[i].files, f)
		}
	}
	return out
}

func computeSuggestions() []Suggestion {
	ign := ignored()
	books := loadBooksLite()
	var out []Suggestion
	add := func(s Suggestion) {
		if !ign[s.Key] && s.Count > 0 {
			out = append(out, s)
		}
	}

	// 1. 相似文件夹（同一层级下名字归一后相同）
	for _, lib := range listLibraries() {
		filepath.WalkDir(lib.Path, func(p string, e os.DirEntry, err error) error {
			if err != nil || !e.IsDir() {
				return nil
			}
			if p != lib.Path && skipName(e.Name()) {
				return filepath.SkipDir
			}
			ents, err := os.ReadDir(p)
			if err != nil {
				return nil
			}
			groups := map[string][]string{}
			for _, c := range ents {
				if c.IsDir() && !skipName(c.Name()) && !strings.HasPrefix(c.Name(), "_") {
					if k := normDir(c.Name()); k != "" {
						groups[k] = append(groups[k], c.Name())
					}
				}
			}
			for _, names := range groups {
				if len(names) < 2 {
					continue
				}
				// 保留内容最多的那个
				sort.Slice(names, func(i, j int) bool {
					a, b := countFiles(filepath.Join(p, names[i])), countFiles(filepath.Join(p, names[j]))
					if a != b {
						return a > b
					}
					return len(names[i]) < len(names[j]) // 一样多时选更简洁的名字
				})
				keep := filepath.Join(p, names[0])
				var items, shown []SugItem
				for _, n := range names[1:] {
					items = append(items, SugItem{From: filepath.Join(p, n), To: keep})
					shown = append(shown, SugItem{From: relToLib(filepath.Join(p, n)), To: relToLib(keep)})
				}
				parent := p
				add(Suggestion{
					Key: sugKey("folder", keep), Type: "folder",
					Title:  fmt.Sprintf("合并相似文件夹：%s", strings.Join(names, "、")),
					Detail: fmt.Sprintf("位于 %s，建议把内容合并到「%s」", relToLib(parent), names[0]),
					Items:  shown, Count: len(items),
					apply: func(batch string) (int, error) { return mergeFolders(items, batch) },
				})
			}
			return nil
		})
	}

	// 2. 同一作者多种写法
	byAuthor := map[string]map[string][]int64{}
	for _, b := range books {
		if b.author == "" {
			continue
		}
		k := normName(b.author)
		if k == "" {
			continue
		}
		if byAuthor[k] == nil {
			byAuthor[k] = map[string][]int64{}
		}
		byAuthor[k][b.author] = append(byAuthor[k][b.author], b.id)
	}
	for k, variants := range byAuthor {
		if len(variants) < 2 {
			continue
		}
		var names []string
		for n := range variants {
			names = append(names, n)
		}
		// 选书最多的写法；一样多时选中文写法
		sort.Slice(names, func(i, j int) bool {
			a, b := len(variants[names[i]]), len(variants[names[j]])
			if a != b {
				return a > b
			}
			return hasHan(names[i]) && !hasHan(names[j])
		})
		keep := names[0]
		var ids []int64
		var items []SugItem
		for _, n := range names[1:] {
			ids = append(ids, variants[n]...)
			items = append(items, SugItem{From: fmt.Sprintf("%s（%d 本）", n, len(variants[n])), To: keep})
		}
		add(Suggestion{
			Key: sugKey("author", k), Type: "author",
			Title:  fmt.Sprintf("同一作者有 %d 种写法：%s", len(names), strings.Join(names, "、")),
			Detail: fmt.Sprintf("建议统一为「%s」，并把这些书移到同一个作者文件夹", keep),
			Items:  items, Count: len(ids),
			apply: func(batch string) (int, error) {
				for _, id := range ids {
					db.Exec(`UPDATE books SET author=?,updated_at=? WHERE id=?`, keep, now(), id)
					updateSearchKey(id)
				}
				return organizeMany(ids, batch, "统一作者名")
			},
		})
	}

	// 3. 同一系列散落在多个文件夹
	bySeries := map[string][]bookLite{}
	for _, b := range books {
		if b.series != "" && b.status == "ok" {
			bySeries[b.kind+"|"+normName(b.series)] = append(bySeries[b.kind+"|"+normName(b.series)], b)
		}
	}
	for k, bs := range bySeries {
		dirs := map[string]bool{}
		var ids []int64
		for _, b := range bs {
			ids = append(ids, b.id)
			for _, f := range b.files {
				dirs[filepath.Dir(f.Path)] = true
			}
		}
		if len(dirs) < 2 || len(bs) < 2 {
			continue
		}
		var items []SugItem
		for d := range dirs {
			items = append(items, SugItem{From: relToLib(d)})
		}
		add(Suggestion{
			Key: sugKey("series", k), Type: "series",
			Title:  fmt.Sprintf("系列《%s》的 %d 本书分散在 %d 个文件夹", bs[0].series, len(bs), len(dirs)),
			Detail: "建议按命名规则归到同一个系列文件夹",
			Items:  items, Count: len(bs),
			apply: func(batch string) (int, error) { return organizeMany(ids, batch, "归拢系列") },
		})
	}

	// 4. 同一本书被登记成多本（书名作者相同，格式不同）
	byTA := map[string][]bookLite{}
	for _, b := range books {
		if b.kind == "comic" {
			continue
		}
		k := normTitle(toSimplified(b.title)) + "|" + normName(b.author)
		byTA[k] = append(byTA[k], b)
	}
	for k, bs := range byTA {
		if len(bs) < 2 {
			continue
		}
		var items []SugItem
		var fmts []string
		for _, b := range bs {
			for _, f := range b.files {
				fmts = append(fmts, strings.ToUpper(f.Format))
				items = append(items, SugItem{From: relToLib(f.Path)})
			}
		}
		target := bs[0].id
		var others []int64
		for _, b := range bs[1:] {
			others = append(others, b.id)
		}
		add(Suggestion{
			Key: sugKey("samebook", k), Type: "samebook",
			Title:  fmt.Sprintf("《%s》登记了 %d 次（%s）", bs[0].title, len(bs), strings.Join(fmts, "、")),
			Detail: "建议合并成一本书，保留全部格式，阅读进度取最新的",
			Items:  items, Count: len(bs),
			apply: func(batch string) (int, error) { return mergeBooks(target, others, batch) },
		})
	}

	// 5. 重复文件（指纹相同）
	byHash := map[string][]File{}
	for _, b := range books {
		for _, f := range b.files {
			if f.Hash != "" {
				byHash[f.Hash] = append(byHash[f.Hash], f)
			}
		}
	}
	for h, fs := range byHash {
		if len(fs) < 2 {
			continue
		}
		sort.Slice(fs, func(i, j int) bool { return fs[i].ID < fs[j].ID })
		var items []SugItem
		dups := fs[1:]
		for _, f := range dups {
			items = append(items, SugItem{From: relToLib(f.Path), To: "_重复文件"})
		}
		add(Suggestion{
			Key: sugKey("dup", h), Type: "duplicate",
			Title:  fmt.Sprintf("重复文件：%s 有 %d 份", filepath.Base(fs[0].Path), len(fs)),
			Detail: "保留 " + relToLib(fs[0].Path) + "，其余移到书库的 _重复文件 文件夹（不会删除）",
			Items:  items, Count: len(dups),
			apply: func(batch string) (int, error) {
				n := 0
				moveMu.Lock()
				defer moveMu.Unlock()
				for i := range dups {
					f := dups[i]
					lib, ok := insideLibrary(f.Path)
					if !ok {
						continue
					}
					if err := relocate(&f, filepath.Join(lib.Path, "_重复文件", filepath.Base(f.Path)), batch, "移走重复文件"); err == nil {
						db.Exec(`DELETE FROM files WHERE id=?`, f.ID)
						db.Exec(`DELETE FROM books WHERE id=? AND NOT EXISTS(SELECT 1 FROM files WHERE book_id=?)`, f.BookID, f.BookID)
						n++
					}
				}
				return n, nil
			},
		})
	}

	// 6. 没按命名规则存放的书
	var ns []int64
	var nsItems []SugItem
	for _, b := range books {
		if b.status != "ok" {
			continue
		}
		bk, err := getBookLiteFull(b.id)
		if err != nil {
			continue
		}
		for i := range bk.Files {
			to, _ := targetPath(&bk.Files[i], bk)
			if to != "" && filepath.Clean(to) != filepath.Clean(bk.Files[i].Path) && !samePathIgnoringSuffix(bk.Files[i].Path, to) {
				ns = append(ns, b.id)
				if len(nsItems) < 30 {
					nsItems = append(nsItems, SugItem{From: relToLib(bk.Files[i].Path), To: relToLib(to)})
				}
				break
			}
		}
	}
	if len(ns) > 0 {
		add(Suggestion{
			Key: sugKey("standard", fmt.Sprint(len(ns))), Type: "standard",
			Title:  fmt.Sprintf("%d 本书没有按命名规则存放", len(ns)),
			Detail: "建议按「设置 → 书库」里的命名模板统一整理。可在操作记录里一键撤销",
			Items:  nsItems, Count: len(ns),
			apply: func(batch string) (int, error) { return organizeMany(ns, batch, "按规范整理") },
		})
	}

	// 7. 空文件夹
	var empties []string
	for _, lib := range listLibraries() {
		filepath.WalkDir(lib.Path, func(p string, e os.DirEntry, err error) error {
			if err != nil || !e.IsDir() || p == lib.Path {
				return nil
			}
			if skipName(e.Name()) {
				return filepath.SkipDir
			}
			if countFiles(p) == 0 {
				empties = append(empties, p)
				return filepath.SkipDir
			}
			return nil
		})
	}
	if len(empties) > 0 {
		var items []SugItem
		for _, e := range empties {
			items = append(items, SugItem{From: relToLib(e)})
		}
		add(Suggestion{
			Key: sugKey("empty", strings.Join(empties, "|")), Type: "empty",
			Title:  fmt.Sprintf("%d 个空文件夹", len(empties)),
			Detail: "里面没有任何文件，建议清理",
			Items:  items, Count: len(empties),
			apply: func(batch string) (int, error) {
				n := 0
				for _, e := range empties {
					if countFiles(e) == 0 && !isProtectedDir(e) && os.RemoveAll(e) == nil {
						n++
					}
				}
				return n, nil
			},
		})
	}
	order := map[string]int{"folder": 1, "author": 2, "series": 3, "samebook": 4, "duplicate": 5, "standard": 6, "empty": 7}
	sort.SliceStable(out, func(i, j int) bool { return order[out[i].Type] < order[out[j].Type] })
	return out
}

func samePathIgnoringSuffix(cur, want string) bool {
	// "书名 (2).epub" 视为已按规范存放
	ext := filepath.Ext(want)
	base := strings.TrimSuffix(want, ext)
	if !strings.HasPrefix(cur, base+" (") || !strings.HasSuffix(cur, ext) {
		return false
	}
	mid := strings.TrimSuffix(strings.TrimPrefix(cur, base+" ("), ")"+ext)
	return mid != "" && strings.Trim(mid, "0123456789") == ""
}

func getBookLiteFull(id int64) (*Book, error) { return getBook(id) }

func hasHan(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

func countFiles(dir string) int {
	n := 0
	filepath.WalkDir(dir, func(p string, e os.DirEntry, err error) error {
		if err == nil && !e.IsDir() {
			name := e.Name()
			if name != ".DS_Store" && name != "Thumbs.db" && name != "desktop.ini" {
				n++
			}
		}
		return nil
	})
	return n
}

func relToLib(p string) string {
	for _, l := range listLibraries() {
		root := filepath.Clean(l.Path)
		if r, err := filepath.Rel(root, p); err == nil && !strings.HasPrefix(r, "..") {
			if r == "." {
				return l.Name
			}
			return l.Name + "/" + filepath.ToSlash(r)
		}
	}
	return p
}

func organizeMany(ids []int64, batch, label string) (int, error) {
	n := 0
	var errs []string
	for _, id := range ids {
		if err := organizeBook(id, batch, label); err != nil {
			errs = append(errs, err.Error())
		} else {
			n++
		}
	}
	if len(errs) > 0 {
		return n, fmt.Errorf("%d 本失败：%s", len(errs), truncate(strings.Join(errs, "；"), 200))
	}
	return n, nil
}

// 把几个文件夹的内容并到一个文件夹
func mergeFolders(items []SugItem, batch string) (int, error) {
	moveMu.Lock()
	defer moveMu.Unlock()
	n := 0
	for _, it := range items {
		filepath.WalkDir(it.From, func(p string, e os.DirEntry, err error) error {
			if err != nil || e.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(it.From, p)
			to := freePath(filepath.Join(it.To, rel))
			var f File
			if db.QueryRow(`SELECT id,book_id,library_id,path,format,size,hash FROM files WHERE path=?`, p).Scan(&f.ID, &f.BookID, &f.LibraryID, &f.Path, &f.Format, &f.Size, &f.Hash) == nil {
				if relocate(&f, to, batch, "合并文件夹") == nil {
					n++
				}
				return nil
			}
			if moveFile(p, to) == nil {
				db.Exec(`INSERT INTO ops(batch,label,from_path,to_path,file_id,created_at) VALUES(?,?,?,?,0,?)`, batch, "合并文件夹", p, to, now())
				n++
			}
			return nil
		})
		removeEmptyParents(it.From)
		if countFiles(it.From) == 0 {
			os.RemoveAll(it.From)
		}
	}
	return n, nil
}

// 把几本书合并成一本
func mergeBooks(target int64, others []int64, batch string) (int, error) {
	var bestPct float64
	var bestAt int64
	db.QueryRow(`SELECT percent,updated_at FROM progress WHERE book_id=?`, target).Scan(&bestPct, &bestAt)
	for _, o := range others {
		db.Exec(`UPDATE files SET book_id=? WHERE book_id=?`, target, o)
		db.Exec(`UPDATE bookmarks SET book_id=? WHERE book_id=?`, target, o)
		var loc string
		var pct float64
		var at, fid int64
		if db.QueryRow(`SELECT file_id,location,percent,updated_at FROM progress WHERE book_id=?`, o).Scan(&fid, &loc, &pct, &at) == nil && at > bestAt {
			db.Exec(`INSERT INTO progress(book_id,file_id,location,percent,updated_at) VALUES(?,?,?,?,?) ON CONFLICT(book_id) DO UPDATE SET file_id=excluded.file_id,location=excluded.location,percent=excluded.percent,updated_at=excluded.updated_at`, target, fid, loc, pct, at)
			bestAt = at
		}
		if _, err := os.Stat(coverPath(target)); err != nil {
			os.Rename(coverPath(o), coverPath(target))
		}
		db.Exec(`DELETE FROM books WHERE id=?`, o)
		os.Remove(coverPath(o))
	}
	updateSearchKey(target)
	organizeBook(target, batch, "合并同一本书")
	return len(others), nil
}
