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
	ReadState   string   `json:"read_state"` // 下面这几项是当前这个人的：读完状态、进度、评分、想读
	CreatedAt   int64    `json:"created_at"`
	UpdatedAt   int64    `json:"updated_at"`
	Files       []File   `json:"files,omitempty"`
	Percent     float64  `json:"percent"`
	LastRead    int64    `json:"last_read"`
	Rating      int      `json:"rating"`
	Want        bool     `json:"want"`
	DeletedAt   int64    `json:"deleted_at,omitempty"`
	Missing     bool     `json:"missing,omitempty"` // 文件都找不到了（被移走、共享文件夹没挂上）
	Prefs       string   `json:"prefs,omitempty"`   // 这个人给这本书单独记的阅读设置（JSON）
	FinishedAt  int64    `json:"finished_at,omitempty"`
}

type File struct {
	ID           int64  `json:"id"`
	BookID       int64  `json:"book_id"`
	LibraryID    int64  `json:"library_id"`
	Path         string `json:"path"`
	Format       string `json:"format"`
	Size         int64  `json:"size"`
	Hash         string `json:"-"`
	Reader       string `json:"reader"`
	MissingSince int64  `json:"missing_since,omitempty"`
}

// 书目字段 + 当前这个人的读书状态。用法：SELECT bookCols + bookFrom + WHERE …，参数先放两次用户 id
const bookCols = `b.id,b.title,b.orig_title,b.author,b.translator,b.series,b.series_index,b.year,b.category,b.tags,b.description,b.isbn,b.language,b.publisher,b.kind,b.direction,b.status,b.confidence,b.source,b.orig_name,b.cover,COALESCE(ub.read_state,'unread'),b.created_at,b.updated_at,COALESCE(p.percent,0),COALESCE(p.updated_at,0),COALESCE(ub.rating,0),COALESCE(ub.want,0),b.deleted_at`

const bookFrom = ` FROM books b LEFT JOIN progress p ON p.book_id=b.id AND p.user_id=? LEFT JOIN user_books ub ON ub.book_id=b.id AND ub.user_id=?`

func scanBook(sc interface{ Scan(...any) error }) (*Book, error) {
	var b Book
	var tags string
	var want int
	err := sc.Scan(&b.ID, &b.Title, &b.OrigTitle, &b.Author, &b.Translator, &b.Series, &b.SeriesIndex, &b.Year, &b.Category, &tags, &b.Description, &b.ISBN, &b.Language, &b.Publisher, &b.Kind, &b.Direction, &b.Status, &b.Confidence, &b.Source, &b.OrigName, &b.Cover, &b.ReadState, &b.CreatedAt, &b.UpdatedAt, &b.Percent, &b.LastRead, &b.Rating, &want, &b.DeletedAt)
	if err != nil {
		return nil, err
	}
	b.Want = want == 1
	b.Tags = []string{}
	jsonUnmarshal(tags, &b.Tags)
	return &b, nil
}

// 不分人的书目信息（整理、入库等内部用）
func getBook(id int64) (*Book, error) { return getBookFor(id, 0) }

// 带上某个人的进度、读完状态
func getBookFor(id, uid int64) (*Book, error) {
	b, err := scanBook(db.QueryRow(`SELECT `+bookCols+bookFrom+` WHERE b.id=?`, uid, uid, id))
	if err != nil {
		return nil, err
	}
	b.Files = bookFiles(id)
	if len(b.Files) > 0 {
		b.Missing = true
		for _, f := range b.Files {
			if f.MissingSince == 0 {
				b.Missing = false
			}
		}
	}
	return b, nil
}

func bookFiles(id int64) []File {
	rows, err := db.Query(`SELECT id,book_id,library_id,path,format,size,hash,missing_since FROM files WHERE book_id=?`, id)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []File
	for rows.Next() {
		var f File
		rows.Scan(&f.ID, &f.BookID, &f.LibraryID, &f.Path, &f.Format, &f.Size, &f.Hash, &f.MissingSince)
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
	err := db.QueryRow(`SELECT id,book_id,library_id,path,format,size,hash,missing_since FROM files WHERE id=?`, id).Scan(&f.ID, &f.BookID, &f.LibraryID, &f.Path, &f.Format, &f.Size, &f.Hash, &f.MissingSince)
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
	// 太长就截短，但扩展名要留着：否则 .pdf 被截掉，入库时认不出是什么格式
	if r := []rune(s); len(r) > 80 {
		ext := filepath.Ext(s)
		// 两段的扩展名（.fb2.zip 靠整个后缀认格式）一起留着
		for _, two := range []string{".fb2.zip", ".kepub.epub"} {
			if strings.HasSuffix(strings.ToLower(s), two) {
				ext = s[len(s)-len(two):]
			}
		}
		if len([]rune(ext)) > 12 {
			ext = ""
		}
		stem := []rune(strings.TrimSuffix(s, ext))
		if keep := 80 - len([]rune(ext)); len(stem) > keep {
			stem = stem[:keep]
		}
		s = strings.TrimSpace(string(stem)) + ext
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
	if err == nil {
		err = dst.Sync() // 确认写到盘上了，再删原文件：中途断电也不会两边都没有
	}
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
	if d, err := os.Open(filepath.Dir(to)); err == nil {
		d.Sync()
		d.Close()
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
	// 只改了书目信息（没移动文件）的那一条是空路径，不算文件数
	rows, err := db.Query(`SELECT batch,MAX(label),SUM(from_path<>''),MIN(undone),MAX(created_at),MAX(to_path) FROM ops GROUP BY batch ORDER BY MAX(id) DESC LIMIT ?`, limit)
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

// 整理时改了书目信息（比如统一作者名）：记下原来的值，撤销时改回来。
// 同时记一条空的操作记录，这样即使没有移动文件，操作记录里也能看到、能撤销
func recordMetaChange(batch, label string, bookID int64, field, old string) {
	db.Exec(`INSERT INTO op_meta(batch,book_id,field,old) VALUES(?,?,?,?)`, batch, bookID, field, old)
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM ops WHERE batch=?`, batch).Scan(&n)
	if n == 0 {
		db.Exec(`INSERT INTO ops(batch,label,from_path,to_path,file_id,created_at) VALUES(?,?,'','',0,?)`, batch, label, now())
	}
}

func undoMeta(batch string) int {
	rows, err := db.Query(`SELECT id,book_id,field,old FROM op_meta WHERE batch=? ORDER BY id DESC`, batch)
	if err != nil {
		return 0
	}
	type m struct {
		id, book   int64
		field, old string
	}
	var list []m
	for rows.Next() {
		var x m
		rows.Scan(&x.id, &x.book, &x.field, &x.old)
		list = append(list, x)
	}
	rows.Close()
	for _, x := range list {
		switch x.field {
		case "author", "series", "title", "category":
			db.Exec(`UPDATE books SET `+x.field+`=?,updated_at=? WHERE id=?`, x.old, now(), x.book)
			updateSearchKey(x.book)
		}
		db.Exec(`DELETE FROM op_meta WHERE id=?`, x.id)
	}
	return len(list)
}

func undoBatch(batch string) (int, error) {
	moveMu.Lock()
	defer moveMu.Unlock()
	undoMeta(batch)
	defer restoreDeletedBooks(batch) // 撤销的是删除：书从回收站回来
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
		if o.from == "" && o.to == "" { // 只改了书目信息的记录
			db.Exec(`UPDATE ops SET undone=1 WHERE id=?`, o.id)
			continue
		}
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
