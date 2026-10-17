package main

// OPDS 1.2 书目：KOReader、静读天下、Panels、Librera 等阅读器可以直接浏览、搜索、下载书库里的书
// 地址：http(s)://NAS:27200/opds（SHUKU_PORT，威联通上用宿主机网络直接监听）   用户名随意，密码就是书库密码

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func routesP1(mux *http.ServeMux) {
	a := requireAuth
	mux.HandleFunc("GET /api/stats", a(handleStats))
	mux.HandleFunc("POST /api/books/{id}/enrich", a(handleEnrichBook))
	mux.HandleFunc("POST /api/books/{id}/cover-url", a(handleCoverFromURL))
	mux.HandleFunc("POST /api/enrich-all", a(handleEnrichAll))
	mux.HandleFunc("GET /api/fulltext", a(handleFulltext))
	mux.HandleFunc("POST /api/fulltext/rebuild", a(handleFulltextRebuild))
	mux.HandleFunc("GET /api/tts/voices", a(handleTTSVoices))
	mux.HandleFunc("POST /api/tts", a(handleTTS))

	o := requireOPDS
	mux.HandleFunc("GET /opds", o(opdsRoot))
	mux.HandleFunc("GET /opds/", o(opdsRoot))
	mux.HandleFunc("GET /opds/opensearch.xml", o(opdsOpenSearch))
	mux.HandleFunc("GET /opds/search", o(opdsSearch))
	mux.HandleFunc("GET /opds/books/{list}", o(opdsList))
	mux.HandleFunc("GET /opds/categories", o(opdsCategories))
	mux.HandleFunc("GET /opds/authors", o(opdsAuthors))
	mux.HandleFunc("GET /opds/series", o(opdsSeries))
	mux.HandleFunc("GET /opds/cover/{id}", o(handleCover))
	mux.HandleFunc("GET /opds/file/{fid}/{name}", o(handleRaw))
}

// ---------- Basic 认证（OPDS 客户端只会用这种） ----------

var basicCache = struct {
	sync.Mutex
	ok map[string]time.Time
}{ok: map[string]time.Time{}}

func basicAuthOK(r *http.Request) bool {
	_, pass, ok := r.BasicAuth()
	if !ok || pass == "" {
		return false
	}
	hash := getSetting("password_hash", "")
	sum := sha256.Sum256([]byte(hash + "|" + pass))
	key := hex.EncodeToString(sum[:])
	basicCache.Lock()
	t, hit := basicCache.ok[key]
	basicCache.Unlock()
	if hit && time.Since(t) < 30*time.Minute {
		return true
	}
	// bcrypt 比较慢，成功一次后缓存半小时，翻目录、加载封面时不会卡
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(pass)) != nil {
		return false
	}
	basicCache.Lock()
	if len(basicCache.ok) > 50 {
		basicCache.ok = map[string]time.Time{}
	}
	basicCache.ok[key] = time.Now()
	basicCache.Unlock()
	return true
}

func requireOPDS(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authed(r) && !basicAuthOK(r) {
			w.Header().Set("WWW-Authenticate", `Basic realm="shuku", charset="UTF-8"`)
			http.Error(w, "需要登录：用户名随意，密码是书库密码", http.StatusUnauthorized)
			return
		}
		h(w, r)
	}
}

// ---------- 输出 ----------

const (
	opdsNav = "application/atom+xml;profile=opds-catalog;kind=navigation"
	opdsAcq = "application/atom+xml;profile=opds-catalog;kind=acquisition"
)

var xe = html.EscapeString

type feed struct {
	b     strings.Builder
	title string
}

func newFeed(id, title, self, kind string) *feed {
	f := &feed{title: title}
	f.b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	f.b.WriteString(`<feed xmlns="http://www.w3.org/2005/Atom" xmlns:opds="http://opds-spec.org/2010/catalog" xmlns:dc="http://purl.org/dc/terms/" xmlns:opensearch="http://a9.com/-/spec/opensearch/1.1/">`)
	fmt.Fprintf(&f.b, `<id>urn:shuku:%s</id><title>%s</title><updated>%s</updated><author><name>%s</name></author>`,
		xe(id), xe(title), time.Now().UTC().Format(time.RFC3339), xe(getSetting("server_name", "我的书库")))
	fmt.Fprintf(&f.b, `<link rel="self" href="%s" type="%s"/>`, xe(self), kind)
	fmt.Fprintf(&f.b, `<link rel="start" href="/opds" type="%s"/>`, opdsNav)
	fmt.Fprintf(&f.b, `<link rel="search" href="/opds/opensearch.xml" type="application/opensearchdescription+xml"/>`)
	return f
}

func (f *feed) link(rel, href, typ string) {
	fmt.Fprintf(&f.b, `<link rel="%s" href="%s" type="%s"/>`, rel, xe(href), typ)
}

func (f *feed) nav(id, title, content, href, kind string) {
	fmt.Fprintf(&f.b, `<entry><title>%s</title><id>urn:shuku:%s</id><updated>%s</updated><content type="text">%s</content><link rel="subsection" href="%s" type="%s"/></entry>`,
		xe(title), xe(id), time.Now().UTC().Format(time.RFC3339), xe(content), xe(href), kind)
}

var opdsTypes = map[string]string{
	"epub": "application/epub+zip", "pdf": "application/pdf", "mobi": "application/x-mobipocket-ebook",
	"azw": "application/vnd.amazon.ebook", "azw3": "application/vnd.amazon.ebook", "fb2": "application/x-fictionbook+xml",
	"fbz": "application/x-zip-compressed-fb2", "cbz": "application/vnd.comicbook+zip", "cbr": "application/vnd.comicbook-rar",
	"cb7": "application/x-cb7", "cbt": "application/x-cbt", "txt": "text/plain", "djvu": "image/vnd.djvu",
	"zip": "application/zip", "rar": "application/vnd.rar", "7z": "application/x-7z-compressed", "docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
}

func (f *feed) book(b *Book) {
	upd := time.Unix(b.UpdatedAt, 0).UTC().Format(time.RFC3339)
	fmt.Fprintf(&f.b, `<entry><title>%s</title><id>urn:shuku:book:%d</id><updated>%s</updated>`, xe(b.Title), b.ID, upd)
	for _, a := range strings.Split(b.Author, " / ") {
		if a = strings.TrimSpace(a); a != "" {
			fmt.Fprintf(&f.b, `<author><name>%s</name></author>`, xe(a))
		}
	}
	if b.Language != "" {
		fmt.Fprintf(&f.b, `<dc:language>%s</dc:language>`, xe(b.Language))
	}
	if b.Year > 0 {
		fmt.Fprintf(&f.b, `<dc:issued>%d</dc:issued>`, b.Year)
	}
	if b.Publisher != "" {
		fmt.Fprintf(&f.b, `<dc:publisher>%s</dc:publisher>`, xe(b.Publisher))
	}
	if b.Category != "" {
		fmt.Fprintf(&f.b, `<category term="%s" label="%s"/>`, xe(b.Category), xe(b.Category))
	}
	desc := b.Description
	if b.Series != "" {
		s := b.Series
		if b.SeriesIndex > 0 {
			s += " #" + strconv.FormatFloat(b.SeriesIndex, 'f', -1, 64)
		}
		desc = "系列：" + s + "\n" + desc
	}
	if desc != "" {
		fmt.Fprintf(&f.b, `<summary type="text">%s</summary>`, xe(truncate(desc, 1200)))
	}
	if b.Cover != "" {
		fmt.Fprintf(&f.b, `<link rel="http://opds-spec.org/image" href="/opds/cover/%d?%s" type="image/jpeg"/>`, b.ID, b.Cover)
		fmt.Fprintf(&f.b, `<link rel="http://opds-spec.org/image/thumbnail" href="/opds/cover/%d?%s" type="image/jpeg"/>`, b.ID, b.Cover)
	}
	for _, fl := range b.Files {
		typ := opdsTypes[fl.Format]
		if typ == "" {
			typ = "application/octet-stream"
		}
		name := safeName(b.Title)
		if b.Author != "" {
			name += " - " + safeName(firstAuthor(b.Author))
		}
		ext := strings.ToLower(filepath.Ext(fl.Path))
		fmt.Fprintf(&f.b, `<link rel="http://opds-spec.org/acquisition" href="/opds/file/%d/%s" type="%s" title="%s" length="%d"/>`,
			fl.ID, url.PathEscape(name+ext), typ, strings.ToUpper(fl.Format), fl.Size)
	}
	f.b.WriteString(`</entry>`)
}

func (f *feed) write(w http.ResponseWriter, kind string) {
	f.b.WriteString(`</feed>`)
	w.Header().Set("Content-Type", kind+";charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write([]byte(f.b.String()))
}

// ---------- 书目查询 ----------

const opdsPage = 50

func queryBooks(where string, args []any, order string, page int) ([]*Book, int) {
	if where != "" {
		where = " WHERE " + where
	}
	var total int
	db.QueryRow(`SELECT COUNT(*) FROM books b LEFT JOIN progress p ON p.book_id=b.id`+where, args...).Scan(&total)
	rows, err := db.Query(`SELECT `+bookCols+` FROM books b LEFT JOIN progress p ON p.book_id=b.id`+where+` ORDER BY `+order+` LIMIT ? OFFSET ?`, append(args, opdsPage, page*opdsPage)...)
	if err != nil {
		return nil, 0
	}
	var out []*Book
	for rows.Next() {
		if b, err := scanBook(rows); err == nil {
			out = append(out, b)
		}
	}
	rows.Close()
	for _, b := range out {
		b.Files = bookFiles(b.ID)
	}
	return out, total
}

func pageOf(r *http.Request) int {
	p, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if p < 0 {
		p = 0
	}
	return p
}

func withPage(u *url.URL, page int) string {
	q := u.Query()
	q.Set("page", strconv.Itoa(page))
	return u.Path + "?" + q.Encode()
}

func acquisitionFeed(w http.ResponseWriter, r *http.Request, id, title, where string, args []any, order string) {
	page := pageOf(r)
	books, total := queryBooks(where, args, order, page)
	f := newFeed(id, title, r.URL.RequestURI(), opdsAcq)
	fmt.Fprintf(&f.b, `<opensearch:totalResults>%d</opensearch:totalResults><opensearch:itemsPerPage>%d</opensearch:itemsPerPage><opensearch:startIndex>%d</opensearch:startIndex>`, total, opdsPage, page*opdsPage+1)
	if (page+1)*opdsPage < total {
		f.link("next", withPage(r.URL, page+1), opdsAcq)
	}
	if page > 0 {
		f.link("previous", withPage(r.URL, page-1), opdsAcq)
	}
	for _, b := range books {
		f.book(b)
	}
	f.write(w, opdsAcq)
}

// ---------- 页面 ----------

func opdsRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/opds" && r.URL.Path != "/opds/" {
		http.NotFound(w, r)
		return
	}
	name := getSetting("server_name", "我的书库")
	f := newFeed("root", name, "/opds", opdsNav)
	var reading, total, comics int
	db.QueryRow(`SELECT COUNT(*) FROM books b JOIN progress p ON p.book_id=b.id WHERE b.read_state<>'done'`).Scan(&reading)
	db.QueryRow(`SELECT COUNT(*) FROM books`).Scan(&total)
	db.QueryRow(`SELECT COUNT(*) FROM books WHERE kind='comic'`).Scan(&comics)
	f.nav("reading", "在读", fmt.Sprintf("%d 本，按最近阅读排序", reading), "/opds/books/reading", opdsAcq)
	f.nav("new", "最近添加", "新入库的书", "/opds/books/new", opdsAcq)
	f.nav("all", "全部图书", fmt.Sprintf("共 %d 本，按书名排序", total-comics), "/opds/books/all", opdsAcq)
	if comics > 0 {
		f.nav("comics", "漫画", fmt.Sprintf("%d 本", comics), "/opds/books/comics", opdsAcq)
	}
	f.nav("categories", "分类", "按分类浏览", "/opds/categories", opdsNav)
	f.nav("authors", "作者", "按作者浏览", "/opds/authors", opdsNav)
	f.nav("series", "系列", "按系列浏览", "/opds/series", opdsNav)
	f.write(w, opdsNav)
}

func opdsList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	switch r.PathValue("list") {
	case "reading":
		acquisitionFeed(w, r, "reading", "在读", `COALESCE(p.updated_at,0)>0 AND b.read_state<>'done'`, nil, `p.updated_at DESC`)
	case "new":
		acquisitionFeed(w, r, "new", "最近添加", "", nil, `b.created_at DESC, b.id DESC`)
	case "all":
		acquisitionFeed(w, r, "all", "全部图书", `b.kind='book'`, nil, `b.sort_key`)
	case "comics":
		acquisitionFeed(w, r, "comics", "漫画", `b.kind='comic'`, nil, `b.series, b.series_index, b.sort_key`)
	case "category":
		c := q.Get("name")
		acquisitionFeed(w, r, "cat:"+c, c, `b.category=?`, []any{c}, `b.sort_key`)
	case "author":
		a := q.Get("name")
		acquisitionFeed(w, r, "author:"+a, a, `b.author=?`, []any{a}, `b.series, b.series_index, b.sort_key`)
	case "series":
		s := q.Get("name")
		acquisitionFeed(w, r, "series:"+s, s, `b.series=?`, []any{s}, `b.series_index, b.sort_key`)
	default:
		http.NotFound(w, r)
	}
}

func groupNav(w http.ResponseWriter, r *http.Request, id, title, query, list string) {
	page := pageOf(r)
	f := newFeed(id, title, r.URL.RequestURI(), opdsNav)
	rows, err := db.Query(query+` LIMIT ? OFFSET ?`, 101, page*100)
	if err != nil {
		f.write(w, opdsNav)
		return
	}
	n := 0
	for rows.Next() {
		var name string
		var cnt int
		rows.Scan(&name, &cnt)
		n++
		if n > 100 {
			break
		}
		f.nav(list+":"+name, name, fmt.Sprintf("%d 本", cnt), "/opds/books/"+list+"?name="+url.QueryEscape(name), opdsAcq)
	}
	rows.Close()
	if n > 100 {
		f.link("next", withPage(r.URL, page+1), opdsNav)
	}
	f.write(w, opdsNav)
}

func opdsCategories(w http.ResponseWriter, r *http.Request) {
	groupNav(w, r, "categories", "分类", `SELECT category,COUNT(*) FROM books WHERE category<>'' GROUP BY category ORDER BY COUNT(*) DESC`, "category")
}

func opdsAuthors(w http.ResponseWriter, r *http.Request) {
	groupNav(w, r, "authors", "作者", `SELECT author,COUNT(*) FROM books WHERE author<>'' GROUP BY author ORDER BY MIN(author_key)`, "author")
}

func opdsSeries(w http.ResponseWriter, r *http.Request) {
	groupNav(w, r, "series", "系列", `SELECT series,COUNT(*) FROM books WHERE series<>'' GROUP BY series ORDER BY series`, "series")
}

func opdsSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	var where []string
	var args []any
	for _, t := range strings.Fields(toSimplified(strings.ToLower(q))) {
		where = append(where, `b.search_key LIKE ?`)
		args = append(args, "%"+t+"%")
	}
	if len(where) == 0 {
		where = []string{"1=0"}
	}
	acquisitionFeed(w, r, "search:"+q, "搜索：“"+q+"”", strings.Join(where, " AND "), args, `b.sort_key`)
}

func opdsOpenSearch(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/opensearchdescription+xml;charset=utf-8")
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<OpenSearchDescription xmlns="http://a9.com/-/spec/opensearch/1.1/">
<ShortName>%s</ShortName><Description>搜索书名、作者、系列，支持拼音</Description><InputEncoding>UTF-8</InputEncoding><OutputEncoding>UTF-8</OutputEncoding>
<Url type="%s" template="%s/opds/search?q={searchTerms}"/>
</OpenSearchDescription>`, xe(getSetting("server_name", "我的书库")), opdsAcq, xe(baseURL(r)))
}

// 客户端访问用的地址（反向代理、tailscale serve 后面也能拼对）
func baseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	return scheme + "://" + host
}
