package main

// 发现：浏览、搜索公开书库，一键下载入库
//   opds        —— 任何 OPDS 1.x 书源（古腾堡计划、Standard Ebooks、自己的 Calibre / talebook / Komga 等）
//   wikisource  —— 维基文库（公版古籍），用 WS Export 生成 EPUB
//   ia          —— 互联网档案馆里可以直接下载的公版书
// 浏览结果统一整理成 DiscoverPage 交给前端；下载走 fetch.go 的后台任务。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Catalog struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"` // opds | wikisource | ia
	URL      string `json:"url"`
	Lang     string `json:"lang,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	Note     string `json:"note,omitempty"`
	Builtin  bool   `json:"builtin"`
}

var builtinCatalogs = []Catalog{
	{ID: "wikisource", Name: "维基文库", Kind: "wikisource", Lang: "zh", Note: "公版中文古籍、近代作品，生成 EPUB 下载", Builtin: true},
	{ID: "gutenberg", Name: "古腾堡计划", Kind: "opds", URL: "https://www.gutenberg.org/ebooks/search.opds/", Note: "七万多本公版书，以英文为主", Builtin: true},
	{ID: "ia", Name: "互联网档案馆", Kind: "ia", URL: "https://archive.org", Note: "可以直接下载的公版书和扫描本", Builtin: true},
	{ID: "standardebooks", Name: "Standard Ebooks", Kind: "opds", URL: "https://standardebooks.org/feeds/atom/new-releases", Note: "精校的英文公版书（新书栏目公开；完整书目需要赞助会员，用户名填邮箱）", Builtin: true},
}

// 可以用环境变量把内置书源换成镜像或本地模拟服务
var catalogEndpoints = map[string]string{
	"wikisource_api":    "https://%s.wikisource.org/w/api.php",
	"wikisource_export": "https://ws-export.wmcloud.org/",
	"ia":                "https://archive.org",
}

func listCatalogs() []Catalog {
	var user []Catalog
	json.Unmarshal([]byte(getSetting("catalogs", "[]")), &user)
	hidden := map[string]bool{}
	json.Unmarshal([]byte(getSetting("catalogs_hidden", "{}")), &hidden)
	var out []Catalog
	for _, c := range builtinCatalogs {
		if hidden[c.ID] {
			continue
		}
		if v := env("SHUKU_CATALOG_"+strings.ToUpper(c.ID), ""); v != "" {
			c.URL = v
		}
		out = append(out, c)
	}
	return append(out, user...)
}

func catalogByID(id string) *Catalog {
	for _, c := range listCatalogs() {
		if c.ID == id {
			c := c
			return &c
		}
	}
	return nil
}

// ---------- 统一的浏览结果 ----------

type DiscoverLink struct {
	Type  string `json:"type"`
	Label string `json:"label"`
	URL   string `json:"url"`
	Size  int64  `json:"size,omitempty"`
}

type DiscoverEntry struct {
	ID       string         `json:"id"`
	Title    string         `json:"title"`
	Author   string         `json:"author,omitempty"`
	Summary  string         `json:"summary,omitempty"`
	Cover    string         `json:"cover,omitempty"`
	Language string         `json:"language,omitempty"`
	Year     int            `json:"year,omitempty"`
	Kind     string         `json:"kind"` // book | nav
	Href     string         `json:"href,omitempty"`
	Formats  []DiscoverLink `json:"formats,omitempty"`
	Tags     []string       `json:"tags,omitempty"` // 书源给的主题、分类（加入书库时用来自动分类）
}

type DiscoverPage struct {
	Title     string          `json:"title"`
	Entries   []DiscoverEntry `json:"entries"`
	Next      string          `json:"next,omitempty"`
	Prev      string          `json:"prev,omitempty"`
	CanSearch bool            `json:"can_search"`
	Total     int             `json:"total,omitempty"`
}

// ---------- HTTP ----------

var discoverClient = &http.Client{Timeout: 30 * time.Second}

func catalogGet(ctx context.Context, c *Catalog, u string, accept string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "Shuku/"+version+" (+OPDS client)")
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if c != nil && (c.Username != "" || c.Password != "") && sameHost(u, c.URL) {
		req.SetBasicAuth(c.Username, c.Password)
	}
	resp, err := discoverClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return nil, "", fmt.Errorf("书源要求登录（%d），请在书源设置里填用户名和密码", resp.StatusCode)
	}
	if resp.StatusCode != 200 {
		return nil, "", fmt.Errorf("书源返回 %d", resp.StatusCode)
	}
	return b, resp.Request.URL.String(), err
}

func sameHost(a, b string) bool {
	ua, e1 := url.Parse(a)
	ub, e2 := url.Parse(b)
	return e1 == nil && e2 == nil && strings.EqualFold(ua.Host, ub.Host)
}

func absURL(base, ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	b, err := url.Parse(base)
	if err != nil {
		return ref
	}
	r, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	return b.ResolveReference(r).String()
}

// ---------- OPDS 1.x / Atom ----------

type atomLink struct {
	Rel    string `xml:"rel,attr"`
	Href   string `xml:"href,attr"`
	Type   string `xml:"type,attr"`
	Title  string `xml:"title,attr"`
	Length string `xml:"length,attr"`
}

type atomText struct {
	Type  string `xml:"type,attr"`
	Inner string `xml:",innerxml"`
}

type atomEntry struct {
	ID      string `xml:"id"`
	Title   string `xml:"title"`
	Authors []struct {
		Name string `xml:"name"`
	} `xml:"author"`
	Summary  atomText   `xml:"summary"`
	Content  atomText   `xml:"content"`
	Language []string   `xml:"language"`
	Issued   string     `xml:"issued"`
	Date     string     `xml:"date"`
	Links    []atomLink `xml:"link"`
	// 主题、分类：<category term="…" label="…"/>
	Categories []struct {
		Term  string `xml:"term,attr"`
		Label string `xml:"label,attr"`
	} `xml:"category"`
}

type atomFeed struct {
	Title   string      `xml:"title"`
	Links   []atomLink  `xml:"link"`
	Entries []atomEntry `xml:"entry"`
	Total   int         `xml:"totalResults"`
}

var opdsFormatNames = map[string]string{
	"application/epub+zip": "EPUB", "application/pdf": "PDF", "application/x-mobipocket-ebook": "MOBI",
	"application/vnd.amazon.ebook": "AZW3", "application/x-mobi8-ebook": "AZW3", "application/x-fictionbook+xml": "FB2",
	"text/plain": "TXT", "application/vnd.comicbook+zip": "CBZ", "application/x-cbz": "CBZ", "application/vnd.comicbook-rar": "CBR",
	"application/x-cbr": "CBR", "application/zip": "ZIP", "image/vnd.djvu": "DJVU", "application/rtf": "RTF",
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document": "DOCX", "application/x-zip-compressed-fb2": "FBZ",
}

// 下载格式的优先顺序：EPUB 最适合在线读
var opdsFormatRank = map[string]int{"EPUB": 1, "AZW3": 2, "MOBI": 3, "FB2": 4, "CBZ": 5, "PDF": 6, "TXT": 7, "CBR": 8}

func cleanAtomText(t atomText) string {
	s := t.Inner
	if t.Type == "html" || t.Type == "text/html" {
		s = htmlUnescape(s)
	}
	return strings.TrimSpace(htmlToText(strings.ReplaceAll(s, "<![CDATA[", "")))
}

func htmlUnescape(s string) string {
	r := strings.NewReplacer("&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'", "&amp;", "&")
	return r.Replace(s)
}

func isNavType(t string) bool {
	t = strings.ToLower(t)
	return strings.Contains(t, "profile=opds-catalog") || strings.HasPrefix(t, "application/atom+xml") || t == "application/opds+json"
}

func parseOPDS(body []byte, base string) (*DiscoverPage, string, error) {
	var f atomFeed
	if err := xmlDecodeLoose(body, &f); err != nil {
		return nil, "", fmt.Errorf("不是有效的 OPDS 书目：%v", err)
	}
	if f.Title == "" && len(f.Entries) == 0 {
		return nil, "", errors.New("不是有效的 OPDS 书目")
	}
	p := &DiscoverPage{Title: cleanSpace(f.Title), Total: f.Total, Entries: []DiscoverEntry{}}
	searchHref := ""
	for _, l := range f.Links {
		switch l.Rel {
		case "next":
			p.Next = absURL(base, l.Href)
		case "previous", "prev":
			p.Prev = absURL(base, l.Href)
		case "search":
			searchHref = absURL(base, l.Href)
		}
	}
	for _, e := range f.Entries {
		de := DiscoverEntry{ID: strings.TrimSpace(e.ID), Title: cleanSpace(e.Title), Kind: "nav"}
		var names []string
		for _, a := range e.Authors {
			if n := cleanSpace(a.Name); n != "" {
				names = append(names, n)
			}
		}
		de.Author = strings.Join(first(names, 3), " / ")
		de.Summary = truncate(cleanAtomText(e.Summary), 600)
		if de.Summary == "" {
			de.Summary = truncate(cleanAtomText(e.Content), 600)
		}
		// 古腾堡的搜索结果把作者放在 content 里
		if de.Author == "" && de.Summary != "" && len([]rune(de.Summary)) < 60 && !strings.Contains(de.Summary, "\n") {
			de.Author, de.Summary = de.Summary, ""
		}
		if len(e.Language) > 0 {
			de.Language = e.Language[0]
		}
		for _, c := range e.Categories {
			t := cleanSpace(c.Label)
			if t == "" && !strings.Contains(c.Term, "/") {
				t = cleanSpace(c.Term)
			}
			if t != "" && len(de.Tags) < 8 {
				de.Tags = append(de.Tags, truncate(t, 60))
			}
		}
		de.Year = yearOf(e.Issued + " " + e.Date)
		for _, l := range e.Links {
			href := absURL(base, l.Href)
			rel := strings.ToLower(l.Rel)
			switch {
			case strings.HasPrefix(rel, "http://opds-spec.org/acquisition") || rel == "enclosure":
				if strings.Contains(rel, "borrow") || strings.Contains(rel, "buy") || strings.Contains(rel, "subscribe") {
					continue
				}
				mt := strings.ToLower(strings.TrimSpace(strings.Split(l.Type, ";")[0]))
				name := opdsFormatNames[mt]
				if name == "" {
					if ext := strings.TrimPrefix(strings.ToLower(path.Ext(strings.Split(href, "?")[0])), "."); ext != "" && formats[ext].Format != "" {
						name = strings.ToUpper(ext)
					} else if strings.Contains(mt, "epub") {
						name = "EPUB"
					} else {
						continue
					}
				}
				size, _ := strconv.ParseInt(l.Length, 10, 64)
				label := name
				if t := cleanSpace(l.Title); t != "" && !strings.EqualFold(t, name) {
					label = name + "（" + truncate(t, 24) + "）"
				}
				de.Formats = append(de.Formats, DiscoverLink{Type: name, Label: label, URL: href, Size: size})
			case rel == "http://opds-spec.org/image" || rel == "x-stanza-cover-image":
				de.Cover = href
			case rel == "http://opds-spec.org/image/thumbnail" || rel == "http://opds-spec.org/thumbnail" || rel == "x-stanza-cover-image-thumbnail":
				if de.Cover == "" {
					de.Cover = href
				}
			case isNavType(l.Type) && rel != "self" && rel != "up" && rel != "start" && !strings.Contains(rel, "related") && !strings.Contains(rel, "alternate"):
				if de.Href == "" {
					de.Href = href
				}
			case isNavType(l.Type) && de.Href == "" && rel == "alternate":
				de.Href = href
			}
		}
		if len(de.Formats) > 0 {
			de.Kind = "book"
			sortFormats(de.Formats)
		} else if de.Href == "" {
			continue // 既不能下载也不能打开的条目
		}
		p.Entries = append(p.Entries, de)
	}
	return p, searchHref, nil
}

func sortFormats(fs []DiscoverLink) {
	rank := func(t string) int {
		if r, ok := opdsFormatRank[t]; ok {
			return r
		}
		return 50
	}
	for i := 1; i < len(fs); i++ {
		for j := i; j > 0 && rank(fs[j].Type) < rank(fs[j-1].Type); j-- {
			fs[j], fs[j-1] = fs[j-1], fs[j]
		}
	}
}

// OpenSearch：搜索链接可能直接是模板，也可能是描述文件
var searchTplCache sync.Map // catalogID -> template

func opdsSearchTemplate(ctx context.Context, c *Catalog, searchHref string) (string, error) {
	if strings.Contains(searchHref, "{searchTerms}") {
		return searchHref, nil
	}
	b, final, err := catalogGet(ctx, c, searchHref, "application/opensearchdescription+xml, */*")
	if err != nil {
		return "", err
	}
	var d struct {
		URLs []struct {
			Type     string `xml:"type,attr"`
			Template string `xml:"template,attr"`
		} `xml:"Url"`
	}
	if err := xmlDecodeLoose(b, &d); err != nil {
		return "", err
	}
	tpl := ""
	for _, u := range d.URLs {
		if strings.Contains(u.Type, "atom") || strings.Contains(u.Type, "opds") {
			tpl = u.Template
			break
		}
	}
	if tpl == "" && len(d.URLs) > 0 {
		tpl = d.URLs[0].Template
	}
	if tpl == "" {
		return "", errors.New("书源没有提供搜索")
	}
	return absURL(final, tpl), nil
}

func fillTemplate(tpl, q string) string {
	r := regexp.MustCompile(`\{([a-zA-Z:]+)\??\}`)
	return r.ReplaceAllStringFunc(tpl, func(m string) string {
		switch strings.Trim(m, "{}?") {
		case "searchTerms":
			return url.QueryEscape(q)
		case "startPage":
			return "1"
		case "startIndex":
			return "1"
		case "count":
			return "30"
		case "language":
			return "*"
		}
		return ""
	})
}

func opdsBrowse(ctx context.Context, c *Catalog, u string) (*DiscoverPage, error) {
	if u == "" {
		u = c.URL
	}
	b, final, err := catalogGet(ctx, c, u, "application/atom+xml;profile=opds-catalog, application/atom+xml, */*")
	if err != nil {
		return nil, err
	}
	p, searchHref, err := parseOPDS(b, final)
	if err != nil {
		return nil, err
	}
	if searchHref != "" {
		searchTplCache.LoadOrStore(c.ID, searchHref)
	}
	_, ok := searchTplCache.Load(c.ID)
	p.CanSearch = ok
	return p, nil
}

func catalogOPDSSearch(ctx context.Context, c *Catalog, q string) (*DiscoverPage, error) {
	v, ok := searchTplCache.Load(c.ID)
	if !ok {
		if _, err := opdsBrowse(ctx, c, ""); err != nil {
			return nil, err
		}
		if v, ok = searchTplCache.Load(c.ID); !ok {
			return nil, errors.New("这个书源不支持搜索，可以在目录里浏览")
		}
	}
	tpl, err := opdsSearchTemplate(ctx, c, v.(string))
	if err != nil {
		return nil, err
	}
	searchTplCache.Store(c.ID, tpl)
	return opdsBrowse(ctx, c, fillTemplate(tpl, q))
}

// ---------- 维基文库 ----------

var s2t = func() *strings.Replacer {
	tr, sc := []rune(tradChars), []rune(simpChars)
	seen := map[rune]bool{}
	var pairs []string
	for i := range sc {
		if i < len(tr) && !seen[sc[i]] {
			seen[sc[i]] = true
			pairs = append(pairs, string(sc[i]), string(tr[i]))
		}
	}
	return strings.NewReplacer(pairs...)
}()

func wikisourceSearch(ctx context.Context, c *Catalog, q string, offset int) (*DiscoverPage, error) {
	lang := c.Lang
	if lang == "" {
		lang = "zh"
	}
	api := fmt.Sprintf(env("SHUKU_WIKISOURCE_API", catalogEndpoints["wikisource_api"]), lang)
	queries := []string{q}
	if lang == "zh" {
		if t := s2t.Replace(q); t != q {
			queries = append(queries, t) // 维基文库多是繁体书名
		}
	}
	type hit struct {
		Title   string `json:"title"`
		Snippet string `json:"snippet"`
		Size    int    `json:"size"`
	}
	seen := map[string]bool{}
	var works []DiscoverEntry
	for _, qq := range queries {
		u := api + "?action=query&list=search&format=json&srnamespace=0&srlimit=40&sroffset=" + strconv.Itoa(offset) + "&srsearch=" + url.QueryEscape(qq)
		b, _, err := catalogGet(ctx, nil, u, "application/json")
		if err != nil {
			return nil, err
		}
		var r struct {
			Query struct {
				Search []hit `json:"search"`
			} `json:"query"`
		}
		if json.Unmarshal(b, &r) != nil {
			return nil, errors.New("维基文库返回格式不正确")
		}
		for _, h := range r.Query.Search {
			// 只要整部作品，“红楼梦/第001回”这种章节页归到作品名下
			root := strings.Split(h.Title, "/")[0]
			if seen[root] {
				continue
			}
			seen[root] = true
			export := env("SHUKU_WIKISOURCE_EXPORT", catalogEndpoints["wikisource_export"])
			dl := export + "?format=epub&lang=" + url.QueryEscape(lang) + "&page=" + url.QueryEscape(root)
			works = append(works, DiscoverEntry{
				ID: "ws:" + root, Title: root, Kind: "book", Language: lang,
				Summary: truncate(cleanSpace(stripTags(htmlUnescape(h.Snippet))), 160),
				Formats: []DiscoverLink{{Type: "EPUB", Label: "EPUB（维基文库生成）", URL: dl}},
			})
		}
	}
	p := &DiscoverPage{Title: "维基文库：" + q, Entries: works, CanSearch: true}
	if p.Entries == nil {
		p.Entries = []DiscoverEntry{}
	}
	if len(works) >= 30 {
		p.Next = "offset:" + strconv.Itoa(offset+40)
	}
	return p, nil
}

// ---------- 互联网档案馆 ----------

func iaBase(c *Catalog) string {
	if c != nil && c.URL != "" {
		return strings.TrimRight(c.URL, "/")
	}
	return catalogEndpoints["ia"]
}

func iaSearch(ctx context.Context, c *Catalog, q string, page int) (*DiscoverPage, error) {
	if page < 1 {
		page = 1
	}
	query := fmt.Sprintf(`(title:(%s) OR creator:(%s)) AND mediatype:texts AND format:(EPUB OR "Text PDF") AND -collection:(inlibrary OR printdisabled OR lendinglibrary)`, q, q)
	u := iaBase(c) + "/advancedsearch.php?output=json&rows=30&page=" + strconv.Itoa(page) +
		"&fl[]=identifier&fl[]=title&fl[]=creator&fl[]=year&fl[]=language&fl[]=description&q=" + url.QueryEscape(query)
	b, _, err := catalogGet(ctx, c, u, "application/json")
	if err != nil {
		return nil, err
	}
	var r struct {
		Response struct {
			NumFound int               `json:"numFound"`
			Docs     []json.RawMessage `json:"docs"`
		} `json:"response"`
	}
	if json.Unmarshal(b, &r) != nil {
		return nil, errors.New("互联网档案馆返回格式不正确")
	}
	p := &DiscoverPage{Title: "互联网档案馆：" + q, Entries: []DiscoverEntry{}, CanSearch: true, Total: r.Response.NumFound}
	for _, raw := range r.Response.Docs {
		var d map[string]any
		json.Unmarshal(raw, &d)
		id := anyString(d["identifier"])
		if id == "" {
			continue
		}
		e := DiscoverEntry{ID: "ia:" + id, Title: anyString(d["title"]), Author: anyString(d["creator"]), Kind: "book",
			Language: anyString(d["language"]), Summary: truncate(cleanSpace(stripTags(anyString(d["description"]))), 300),
			Cover:   iaBase(c) + "/services/img/" + url.PathEscape(id),
			Formats: []DiscoverLink{{Type: "EPUB/PDF", Label: "下载（自动选 EPUB 或 PDF）", URL: "ia:" + id}}}
		e.Year, _ = strconv.Atoi(anyString(d["year"]))
		switch s := d["subject"].(type) {
		case string:
			e.Tags = []string{truncate(s, 60)}
		case []any:
			for _, x := range s {
				if t, ok := x.(string); ok && len(e.Tags) < 8 {
					e.Tags = append(e.Tags, truncate(t, 60))
				}
			}
		}
		p.Entries = append(p.Entries, e)
	}
	if page*30 < r.Response.NumFound {
		p.Next = "page:" + strconv.Itoa(page+1)
	}
	return p, nil
}

// 字段可能是字符串，也可能是字符串数组
func anyString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []any:
		var s []string
		for _, i := range x {
			if t, ok := i.(string); ok {
				s = append(s, t)
			}
		}
		return strings.Join(first(s, 3), " / ")
	case float64:
		return strconv.Itoa(int(x))
	}
	return ""
}

// ia:标识 → 实际下载地址（优先 EPUB，其次有文字层的 PDF）
func iaResolve(ctx context.Context, id string) (string, string, error) {
	c := catalogByID("ia")
	b, _, err := catalogGet(ctx, c, iaBase(c)+"/metadata/"+url.PathEscape(id), "application/json")
	if err != nil {
		return "", "", err
	}
	var m struct {
		Metadata map[string]any `json:"metadata"`
		Files    []struct {
			Name   string `json:"name"`
			Format string `json:"format"`
		} `json:"files"`
	}
	if json.Unmarshal(b, &m) != nil {
		return "", "", errors.New("互联网档案馆返回格式不正确")
	}
	if anyString(m.Metadata["access-restricted-item"]) == "true" {
		return "", "", errors.New("这本书只能在互联网档案馆在线借阅，不能下载")
	}
	best, rank := "", 99
	for _, f := range m.Files {
		r := map[string]int{"EPUB": 1, "Text PDF": 2, "PDF": 3, "DjVu": 4}[f.Format]
		if r > 0 && r < rank {
			best, rank = f.Name, r
		}
	}
	if best == "" {
		return "", "", errors.New("这一项没有可以下载的电子书文件")
	}
	title := anyString(m.Metadata["title"])
	return iaBase(c) + "/download/" + url.PathEscape(id) + "/" + url.PathEscape(best), title, nil
}

// ---------- 接口 ----------

func handleCatalogs(w http.ResponseWriter, r *http.Request) {
	out := []Catalog{}
	for _, c := range listCatalogs() {
		if c.Password != "" {
			c.Password = "••••"
		}
		out = append(out, c)
	}
	writeJSON(w, 200, out)
}

func handleAddCatalog(w http.ResponseWriter, r *http.Request) {
	var c Catalog
	if readJSON(r, &c) != nil {
		errJSON(w, 400, "格式不正确")
		return
	}
	c.URL = strings.TrimSpace(c.URL)
	if !strings.HasPrefix(c.URL, "http://") && !strings.HasPrefix(c.URL, "https://") {
		errJSON(w, 400, "请填写 http:// 或 https:// 开头的 OPDS 地址")
		return
	}
	c.Kind, c.Builtin, c.ID = "opds", false, "u"+randToken(4)
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	p, err := opdsBrowse(ctx, &c, "")
	if err != nil {
		errJSON(w, 400, "无法打开这个书源："+err.Error())
		return
	}
	if strings.TrimSpace(c.Name) == "" {
		c.Name = truncate(p.Title, 20)
	}
	var user []Catalog
	json.Unmarshal([]byte(getSetting("catalogs", "[]")), &user)
	user = append(user, c)
	setSetting("catalogs", toJSON(user))
	c.Password = ""
	writeJSON(w, 200, c)
}

func handleDeleteCatalog(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	for _, b := range builtinCatalogs {
		if b.ID == id {
			hidden := map[string]bool{}
			json.Unmarshal([]byte(getSetting("catalogs_hidden", "{}")), &hidden)
			hidden[id] = true
			setSetting("catalogs_hidden", toJSON(hidden))
			writeJSON(w, 200, map[string]bool{"ok": true})
			return
		}
	}
	var user, keep []Catalog
	json.Unmarshal([]byte(getSetting("catalogs", "[]")), &user)
	for _, c := range user {
		if c.ID != id {
			keep = append(keep, c)
		}
	}
	if keep == nil {
		keep = []Catalog{}
	}
	setSetting("catalogs", toJSON(keep))
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func handleRestoreCatalogs(w http.ResponseWriter, r *http.Request) {
	setSetting("catalogs_hidden", "{}")
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// GET /api/catalogs/{id}/browse?url=  或  ?q=搜索词&next=
func handleCatalogBrowse(w http.ResponseWriter, r *http.Request) {
	c := catalogByID(r.PathValue("id"))
	if c == nil {
		errJSON(w, 404, "书源不存在")
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	next := r.URL.Query().Get("next")
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	var p *DiscoverPage
	var err error
	switch c.Kind {
	case "wikisource":
		if q == "" {
			writeJSON(w, 200, DiscoverPage{Title: c.Name, Entries: []DiscoverEntry{}, CanSearch: true})
			return
		}
		off, _ := strconv.Atoi(strings.TrimPrefix(next, "offset:"))
		p, err = wikisourceSearch(ctx, c, q, off)
	case "ia":
		if q == "" {
			writeJSON(w, 200, DiscoverPage{Title: c.Name, Entries: []DiscoverEntry{}, CanSearch: true})
			return
		}
		pg, _ := strconv.Atoi(strings.TrimPrefix(next, "page:"))
		p, err = iaSearch(ctx, c, q, pg)
	default:
		switch {
		case next != "":
			p, err = opdsBrowse(ctx, c, next)
		case q != "":
			p, err = catalogOPDSSearch(ctx, c, q)
		default:
			p, err = opdsBrowse(ctx, c, r.URL.Query().Get("url"))
		}
	}
	if err != nil {
		errJSON(w, 502, simplifyNetErr(err))
		return
	}
	// 书库里已经有的书做个标记
	for i := range p.Entries {
		e := &p.Entries[i]
		if e.Kind == "book" && e.Title != "" {
			var n int
			db.QueryRow(`SELECT COUNT(*) FROM books WHERE (title=? OR title=?) AND deleted_at=0`, e.Title, toSimplified(e.Title)).Scan(&n)
			if n > 0 {
				e.ID = "have:" + e.ID
			}
		}
	}
	writeJSON(w, 200, p)
}

// 书源里的封面经服务器转一下：需要登录的书源也能显示，客户端不用直接访问外网
func handleCatalogImage(w http.ResponseWriter, r *http.Request) {
	u := r.URL.Query().Get("url")
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		http.NotFound(w, r)
		return
	}
	c := catalogByID(r.URL.Query().Get("c"))
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	b, _, err := catalogGet(ctx, c, u, "image/*")
	if err != nil || len(b) == 0 {
		http.NotFound(w, r)
		return
	}
	ct := http.DetectContentType(b)
	if !strings.HasPrefix(ct, "image/") {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "private, max-age=604800")
	w.Write(b)
}
