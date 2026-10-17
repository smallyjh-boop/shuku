package main

// 按命名模板整理文件：移动、记录操作日志、撤销

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

type Book struct {
	ID          int64    `json:"id"`
	Title       string   `json:"title"`
	OrigTitle   string   `json:"orig_title"`
	Author      string   `json:"author"`
	Translator  string   `json:"translator"`
	Series      string   `json:"series"`
	SeriesIndex float64  `json:"series_index"`
	Year        int      `json:"year"`
	Category    string   `json:"category"`
	Tags        []string `json:"tags"`
	Description string   `json:"description"`
	ISBN        string   `json:"isbn"`
	Language    string   `json:"language"`
	Publisher   string   `json:"publisher"`
	Kind        string   `json:"kind"`
	Direction   string   `json:"direction"`
	Status      string   `json:"status"`
	Confidence  float64  `json:"confidence"`
	Source      string   `json:"source"`
	OrigName    string   `json:"orig_name"`
	Cover       string   `json:"cover"`
	ReadState   string   `json:"read_state"`
	CreatedAt   int64    `json:"created_at"`
	UpdatedAt   int64    `json:"updated_at"`
	Files       []File   `json:"files,omitempty"`
	Percent     float64  `json:"percent"`
	LastRead    int64    `json:"last_read"`
}

type File struct {
	ID        int64  `json:"id"`
	BookID    int64  `json:"book_id"`
	LibraryID int64  `json:"library_id"`
	Path      string `json:"path"`
	Format    string `json:"format"`
	Size      int64  `json:"size"`
	Hash      string `json:"-"`
	Reader    string `json:"reader"`
}

const bookCols = `b.id,b.title,b.orig_title,b.author,b.translator,b.series,b.series_index,b.year,b.category,b.tags,b.description,b.isbn,b.language,b.publisher,b.kind,b.direction,b.status,b.confidence,b.source,b.orig_name,b.cover,b.read_state,b.created_at,b.updated_at,COALESCE(p.percent,0),COALESCE(p.updated_at,0)`

func scanBook(sc interface{ Scan(...any) error }) (*Book, error) {
	var b Book
	var tags string
	err := sc.Scan(&b.ID, &b.Title, &b.OrigTitle, &b.Author, &b.Translator, &b.Series, &b.SeriesIndex, &b.Year, &b.Category, &tags, &b.Description, &b.ISBN, &b.Language, &b.Publisher, &b.Kind, &b.Direction, &b.Status, &b.Confidence, &b.Source, &b.OrigName, &b.Cover, &b.ReadState, &b.CreatedAt, &b.UpdatedAt, &b.Percent, &b.LastRead)
	if err != nil {
		return nil, err
	}
	b.Tags = []string{}
	jsonUnmarshal(tags, &b.Tags)
	return &b, nil
}

func getBook(id int64) (*Book, error) {
	b, err := scanBook(db.QueryRow(`SELECT `+bookCols+` FROM books b LEFT JOIN progress p ON p.book_id=b.id WHERE b.id=?`, id))
	if err != nil {
		return nil, err
	}
	b.Files = bookFiles(id)
	return b, nil
}

func bookFiles(id int64) []File {
	rows, err := db.Query(`SELECT id,book_id,library_id,path,format,size,hash FROM files WHERE book_id=?`, id)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []File
	for rows.Next() {
		var f File
		rows.Scan(&f.ID, &f.BookID, &f.LibraryID, &f.Path, &f.Format, &f.Size, &f.Hash)
		f.Reader = readerFor(f.Format)
		out = append(out, f)
	}
	sortFiles(out)
	return out
}

func sortFiles(fs []File) {
	for i := 1; i < len(fs); i++ {
		for j := i; j > 0 && rankFormat(fs[j].Format) < rankFormat(fs[j-1].Format); j-- {
			fs[j], fs[j-1] = fs[j-1], fs[j]
		}
	}
}

func getFile(id int64) (*File, error) {
	var f File
	err := db.QueryRow(`SELECT id,book_id,library_id,path,format,size,hash FROM files WHERE id=?`, id).Scan(&f.ID, &f.BookID, &f.LibraryID, &f.Path, &f.Format, &f.Size, &f.Hash)
	if err != nil {
		return nil, err
	}
	f.Reader = readerFor(f.Format)
	return &f, nil
}

// ---------- 模板 ----------

var tplRe = regexp.MustCompile(`\{([^{}]*)\}`)
var tplKeyRe = regexp.MustCompile(`(分类|作者|书名|原名|系列|序号|年份|译者|格式)(?::(0?\d))?`)

func renderTemplate(tpl string, b *Book, format string) string {
	vals := map[string]string{
		"分类": b.Category, "作者": firstAuthor(b.Author), "书名": b.Title, "原名": b.OrigTitle,
		"系列": b.Series, "译者": b.Translator, "格式": strings.ToUpper(format),
	}
	if b.Year > 0 {
		vals["年份"] = strconv.Itoa(b.Year)
	}
	defaults := map[string]string{"分类": "其他", "作者": "未知作者", "书名": "未命名", "系列": b.Title}
	out := tplRe.ReplaceAllStringFunc(tpl, func(tok string) string {
		inner := tok[1 : len(tok)-1]
		m := tplKeyRe.FindStringSubmatchIndex(inner)
		if m == nil {
			return tok
		}
		key := inner[m[2]:m[3]]
		val := vals[key]
		if key == "序号" && b.SeriesIndex > 0 {
			width := 0
			if m[4] >= 0 {
				width, _ = strconv.Atoi(inner[m[4]:m[5]])
			}
			if b.SeriesIndex == float64(int(b.SeriesIndex)) {
				val = fmt.Sprintf("%0*d", width, int(b.SeriesIndex))
			} else {
				val = strconv.FormatFloat(b.SeriesIndex, 'f', -1, 64)
			}
		}
		prefix, suffix := inner[:m[0]], inner[m[1]:]
		if val == "" {
			if prefix == "" && suffix == "" {
				return defaults[key]
			}
			return ""
		}
		return prefix + safeName(val) + suffix
	})
	var segs []string
	for _, s := range strings.Split(out, "/") {
		if s = safeName(s); s != "" {
			segs = append(segs, s)
		}
	}
	return filepath.Join(segs...)
}

func firstAuthor(a string) string {
	for _, sep := range []string{" / ", "/", "、", ";", "；", ",", "，", "&"} {
		if i := strings.Index(a, sep); i > 0 {
			return strings.TrimSpace(a[:i]) + "等"
		}
	}
	return a
}

var unsafeRe = regexp.MustCompile(`[\\/:*?"<>|\x00-\x1f]`)

func safeName(s string) string {
	s = unsafeRe.ReplaceAllStringFunc(s, func(c string) string {
		switch c {
		case ":":
			return "："
		case "?":
			return "？"
		case "\"":
			return "'"
		case "<":
			return "《"
		case ">":
			return "》"
		case "|", "/", "\\":
			return "-"
		}
		return ""
	})
	s = strings.Trim(cleanSpace(s), ". ")
	if r := []rune(s); len(r) > 80 {
		s = strings.TrimSpace(string(r[:80]))
	}
	return s
}

func templateFor(lib *Library, b *Book) string {
	if lib.Kind == "comic" {
		if lib.Template != "" {
			return lib.Template
		}
		return defaultComicTemplate
	}
	if b.Series != "" && b.SeriesIndex > 0 {
		return getSetting("series_template", defaultSeriesTemplate)
	}
	if lib.Template != "" {
		return lib.Template
	}
	return defaultBookTemplate
}

// 这本书的这个文件应该放在哪里
func targetPath(f *File, b *Book) (string, *Library) {
	lib := libraryByID(f.LibraryID)
	if lib == nil {
		lib = libraryFor(b.Kind)
	}
	if lib == nil {
		return "", nil
	}
	rel := renderTemplate(templateFor(lib, b), b, f.Format)
	ext := filepath.Ext(f.Path)
	if strings.HasSuffix(strings.ToLower(f.Path), ".fb2.zip") {
		ext = ".fb2.zip"
	}
	return filepath.Join(lib.Path, rel) + strings.ToLower(ext), lib
}

// ---------- 移动 ----------

var moveMu sync.Mutex

func moveFile(from, to string) error {
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(to); err == nil {
		return errors.New("目标文件已存在")
	}
	err := os.Rename(from, to)
	if err == nil {
		return nil
	}
	var le *os.LinkError
	if errors.As(err, &le) && errors.Is(le.Err, syscall.EXDEV) {
		return copyThenRemove(from, to)
	}
	return err
}

func copyThenRemove(from, to string) error {
	src, err := os.Open(from)
	if err != nil {
		return err
	}
	st, _ := src.Stat()
	tmp := to + ".shuku-part"
	dst, err := os.Create(tmp)
	if err != nil {
		src.Close()
		return err
	}
	_, err = io.Copy(dst, src)
	src.Close()
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	os.Chtimes(tmp, st.ModTime(), st.ModTime())
	if err := os.Rename(tmp, to); err != nil {
		return err
	}
	return os.Remove(from)
}

// 目标被占用时加序号
func freePath(p string) string {
	if _, err := os.Stat(p); err != nil {
		return p
	}
	ext := filepath.Ext(p)
	base := strings.TrimSuffix(p, ext)
	if strings.HasSuffix(strings.ToLower(p), ".fb2.zip") {
		ext = p[len(p)-8:]
		base = p[:len(p)-8]
	}
	for i := 2; ; i++ {
		q := fmt.Sprintf("%s (%d)%s", base, i, ext)
		if _, err := os.Stat(q); err != nil {
			return q
		}
	}
}

// 移动一个文件并写日志
func relocate(f *File, to, batch, label string) error {
	if filepath.Clean(f.Path) == filepath.Clean(to) {
		return nil
	}
	to = freePath(to)
	if err := moveFile(f.Path, to); err != nil {
		return err
	}
	db.Exec(`UPDATE files SET path=? WHERE id=?`, to, f.ID)
	db.Exec(`INSERT INTO ops(batch,label,from_path,to_path,file_id,created_at) VALUES(?,?,?,?,?,?)`, batch, label, f.Path, to, f.ID, now())
	removeEmptyParents(filepath.Dir(f.Path))
	f.Path = to
	return nil
}

// 按模板整理一本书的全部文件
func organizeBook(bookID int64, batch, label string) error {
	moveMu.Lock()
	defer moveMu.Unlock()
	b, err := getBook(bookID)
	if err != nil {
		return err
	}
	var errs []string
	for i := range b.Files {
		f := &b.Files[i]
		to, _ := targetPath(f, b)
		if to == "" {
			continue
		}
		if err := relocate(f, to, batch, label); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "；"))
	}
	return nil
}

// 是否在书库根目录之内
func insideLibrary(p string) (*Library, bool) {
	for _, l := range listLibraries() {
		root := filepath.Clean(l.Path)
		if strings.HasPrefix(filepath.Clean(p), root+string(os.PathSeparator)) {
			l := l
			return &l, true
		}
	}
	return nil, false
}

func isProtectedDir(p string) bool {
	c := filepath.Clean(p)
	for _, l := range listLibraries() {
		if filepath.Clean(l.Path) == c {
			return true
		}
	}
	return c == filepath.Clean(cfg.Inbox)
}

// 移走文件后，清理留下的空文件夹（不会删书库根目录和投递目录）
func removeEmptyParents(dir string) {
	for i := 0; i < 8; i++ {
		if isProtectedDir(dir) {
			return
		}
		_, inLib := insideLibrary(dir)
		inInbox := strings.HasPrefix(filepath.Clean(dir), filepath.Clean(cfg.Inbox)+string(os.PathSeparator))
		if !inLib && !inInbox {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			// 系统生成的小文件不算内容
			n := e.Name()
			if n == ".DS_Store" || n == "Thumbs.db" || n == "desktop.ini" || n == "@__thumb" || n == ".@__thumb" {
				continue
			}
			return
		}
		for _, e := range entries {
			os.RemoveAll(filepath.Join(dir, e.Name()))
		}
		if os.Remove(dir) != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}

// ---------- 撤销 ----------

type OpBatch struct {
	Batch   string `json:"batch"`
	Label   string `json:"label"`
	Count   int    `json:"count"`
	Undone  bool   `json:"undone"`
	Created int64  `json:"created_at"`
	Sample  string `json:"sample"`
}

func listBatches(limit int) []OpBatch {
	rows, err := db.Query(`SELECT batch,MAX(label),COUNT(*),MIN(undone),MAX(created_at),MAX(to_path) FROM ops GROUP BY batch ORDER BY MAX(id) DESC LIMIT ?`, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []OpBatch{}
	for rows.Next() {
		var o OpBatch
		var u int
		rows.Scan(&o.Batch, &o.Label, &o.Count, &u, &o.Created, &o.Sample)
		o.Undone = u == 1
		out = append(out, o)
	}
	return out
}

func undoBatch(batch string) (int, error) {
	moveMu.Lock()
	defer moveMu.Unlock()
	rows, err := db.Query(`SELECT id,from_path,to_path,file_id FROM ops WHERE batch=? AND undone=0 ORDER BY id DESC`, batch)
	if err != nil {
		return 0, err
	}
	type op struct {
		id, fileID int64
		from, to   string
	}
	var ops []op
	for rows.Next() {
		var o op
		rows.Scan(&o.id, &o.from, &o.to, &o.fileID)
		ops = append(ops, o)
	}
	rows.Close()
	n := 0
	var errs []string
	for _, o := range ops {
		if _, err := os.Stat(o.to); err != nil {
			errs = append(errs, filepath.Base(o.to)+" 已不在原处")
			continue
		}
		back := o.from
		if _, err := os.Stat(back); err == nil {
			back = freePath(back)
		}
		if err := moveFile(o.to, back); err != nil {
			errs = append(errs, err.Error())
			continue
		}
		if o.fileID > 0 {
			db.Exec(`UPDATE files SET path=? WHERE id=?`, back, o.fileID)
		}
		db.Exec(`UPDATE ops SET undone=1 WHERE id=?`, o.id)
		removeEmptyParents(filepath.Dir(o.to))
		n++
	}
	if len(errs) > 0 {
		return n, errors.New(strings.Join(errs, "；"))
	}
	return n, nil
}

func newBatch() string { return randToken(6) }

var _ = sql.ErrNoRows
