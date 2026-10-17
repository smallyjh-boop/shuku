package main

// 联网补全：豆瓣读书、Open Library、Google Books、Bangumi（漫画）
// 用途：1) 入库时校验 AI / 规则识别出的书名作者，对得上就提高把握；2) 补全封面、简介、年份、ISBN、出版社；
//      3) 详情页“联网查找”给出候选，手动挑一个。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 各数据源地址（测试时替换成本地模拟服务）
var (
	doubanBase   = "https://book.douban.com"
	olBase       = "https://openlibrary.org"
	olCoverBase  = "https://covers.openlibrary.org"
	googleBase   = "https://www.googleapis.com"
	bangumiBase  = "https://api.bgm.tv"
	metaClient   = &http.Client{Timeout: 12 * time.Second}
	browserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0 Safari/537.36"
)

type Candidate struct {
	Source      string   `json:"source"`
	Title       string   `json:"title"`
	OrigTitle   string   `json:"original_title"`
	Author      string   `json:"author"`
	Translator  string   `json:"translator"`
	Year        int      `json:"year"`
	Publisher   string   `json:"publisher"`
	ISBN        string   `json:"isbn"`
	Description string   `json:"description"`
	Cover       string   `json:"cover"`
	Tags        []string `json:"tags"`
	Language    string   `json:"language"`
	Link        string   `json:"link"`
	Score       float64  `json:"score"`
	detail      func(ctx context.Context, c *Candidate) // 需要时再取详情（简介、ISBN）
}

type MetaConfig struct {
	Enabled     bool `json:"enabled"`
	Douban      bool `json:"douban"`
	OpenLibrary bool `json:"openlibrary"`
	Google      bool `json:"google"`
	Bangumi     bool `json:"bangumi"`
	OnIngest    bool `json:"on_ingest"` // 入库时自动校验、补全
}

func metaConfig() MetaConfig {
	c := MetaConfig{Enabled: true, Douban: true, OpenLibrary: true, Google: true, Bangumi: true, OnIngest: true}
	json.Unmarshal([]byte(getSetting("online_meta", "{}")), &c)
	return c
}

// ---------- 相似度 ----------

func bigrams(s string) map[string]int {
	r := []rune(s)
	m := map[string]int{}
	if len(r) == 1 {
		m[s]++
	}
	for i := 0; i+1 < len(r); i++ {
		m[string(r[i:i+2])]++
	}
	return m
}

func titleSim(a, b string) float64 {
	a, b = normTitle(toSimplified(a)), normTitle(toSimplified(b))
	if a == "" || b == "" {
		return 0
	}
	if a == b {
		return 1
	}
	la, lb := len([]rune(a)), len([]rune(b))
	if strings.Contains(a, b) || strings.Contains(b, a) {
		short, long := la, lb
		if short > long {
			short, long = long, short
		}
		if float64(short)/float64(long) >= 0.4 {
			return 0.88
		}
		return 0.7
	}
	// 双字 Dice 系数
	ga, gb := bigrams(a), bigrams(b)
	inter, total := 0, 0
	for k, n := range ga {
		total += n
		if m, ok := gb[k]; ok {
			inter += min(n, m)
		}
	}
	for _, n := range gb {
		total += n
	}
	if total == 0 {
		return 0
	}
	return 2 * float64(inter) / float64(total)
}

func authorSim(ours, theirs string) float64 {
	if strings.TrimSpace(ours) == "" {
		return 0.6 // 我们不知道作者，不加分也不扣分
	}
	if strings.TrimSpace(theirs) == "" {
		return 0.5
	}
	a, b := normName(firstAuthor(ours)), normName(theirs)
	if a == "" || b == "" {
		return 0.5
	}
	if strings.Contains(b, strings.TrimSuffix(a, "deng")) || strings.Contains(a, b) {
		return 1
	}
	return 0
}

func scoreCandidate(c *Candidate, title, author string) {
	c.Score = 0.72*titleSim(title, c.Title) + 0.28*authorSim(author, c.Author)
	if c.OrigTitle != "" {
		if s := 0.72*titleSim(title, c.OrigTitle) + 0.28*authorSim(author, c.Author); s > c.Score {
			c.Score = s
		}
	}
}

// ---------- 请求 ----------

func getJSON(ctx context.Context, u string, v any, headers map[string]string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", browserAgent)
	req.Header.Set("Accept", "application/json")
	for k, val := range headers {
		req.Header.Set(k, val)
	}
	resp, err := metaClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("%s 返回 %d", req.URL.Host, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(v)
}

func getText(ctx context.Context, u string, headers map[string]string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", browserAgent)
	for k, val := range headers {
		req.Header.Set(k, val)
	}
	resp, err := metaClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("%s 返回 %d", req.URL.Host, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return string(b), err
}

// ---------- 豆瓣读书 ----------

func searchDouban(ctx context.Context, q string) ([]*Candidate, error) {
	var res []struct {
		Title  string `json:"title"`
		URL    string `json:"url"`
		Pic    string `json:"pic"`
		Author string `json:"author_name"`
		Year   string `json:"year"`
		Type   string `json:"type"`
		ID     string `json:"id"`
	}
	err := getJSON(ctx, doubanBase+"/j/subject_suggest?q="+url.QueryEscape(q), &res, map[string]string{"Referer": doubanBase + "/"})
	if err != nil {
		return nil, err
	}
	var out []*Candidate
	for _, r := range res {
		if r.Type != "" && r.Type != "b" {
			continue
		}
		y, _ := strconv.Atoi(r.Year)
		cover := r.Pic
		// 小图换大图：/s/public/ → /l/public/
		cover = strings.Replace(strings.Replace(cover, "/s/public/", "/l/public/", 1), "/view/subject/s/", "/view/subject/l/", 1)
		c := &Candidate{Source: "豆瓣读书", Title: r.Title, Author: r.Author, Year: y, Cover: cover, Link: r.URL, Language: "zh"}
		c.detail = doubanDetail
		out = append(out, c)
	}
	return out, nil
}

var (
	dbIntroRe = regexp.MustCompile(`(?s)<div class="intro">(.*?)</div>`)
	dbInfoRe  = regexp.MustCompile(`(?s)<div id="info"[^>]*>(.*?)</div>`)
	dbFieldRe = func(name string) *regexp.Regexp {
		return regexp.MustCompile(`(?s)<span class="pl">\s*` + name + `:?\s*</span>:?\s*(?:<a[^>]*>)?([^<]+)`)
	}
	dbISBN      = dbFieldRe("ISBN")
	dbPublisher = dbFieldRe("出版社")
	dbOrig      = dbFieldRe("原作名")
	dbYear      = dbFieldRe("出版年")
	dbTrans     = regexp.MustCompile(`(?s)<span class="pl">\s*译者\s*</span>:?\s*<a[^>]*>([^<]+)</a>`)
)

// 豆瓣详情页：简介、ISBN、出版社、原作名、译者（豆瓣有时拦截机器访问，失败就算了）
func doubanDetail(ctx context.Context, c *Candidate) {
	if c.Link == "" {
		return
	}
	link := c.Link
	if strings.HasPrefix(link, "https://book.douban.com") && doubanBase != "https://book.douban.com" {
		link = doubanBase + strings.TrimPrefix(link, "https://book.douban.com")
	}
	page, err := getText(ctx, link, map[string]string{"Referer": doubanBase + "/"})
	if err != nil {
		return
	}
	if m := dbIntroRe.FindAllStringSubmatch(page, -1); len(m) > 0 {
		// 有“展开全部”时最后一个 intro 是完整简介
		c.Description = strings.TrimSpace(strings.ReplaceAll(stripTagsKeepLines(m[len(m)-1][1]), "(展开全部)", ""))
	}
	if info := dbInfoRe.FindStringSubmatch(page); info != nil {
		get := func(re *regexp.Regexp) string {
			if m := re.FindStringSubmatch(info[1]); m != nil {
				return strings.TrimSpace(m[1])
			}
			return ""
		}
		c.ISBN = isbnOf(get(dbISBN))
		c.Publisher = get(dbPublisher)
		c.OrigTitle = get(dbOrig)
		if y := yearOf(get(dbYear)); y > 0 {
			c.Year = y
		}
		if m := dbTrans.FindStringSubmatch(info[1]); m != nil {
			c.Translator = strings.TrimSpace(m[1])
		}
	}
}

func stripTagsKeepLines(s string) string {
	s = regexp.MustCompile(`(?i)</p>|<br\s*/?>`).ReplaceAllString(s, "\n")
	s = tagRe.ReplaceAllString(s, "")
	r := strings.NewReplacer("&nbsp;", " ", "&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'")
	var lines []string
	for _, l := range strings.Split(r.Replace(s), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	return strings.Join(lines, "\n")
}

// ---------- Open Library ----------

func searchOpenLibrary(ctx context.Context, title, author, isbn string) ([]*Candidate, error) {
	q := url.Values{}
	if isbn != "" {
		q.Set("isbn", isbn)
	} else {
		q.Set("title", title)
		if author != "" {
			q.Set("author", firstAuthor(author))
		}
	}
	q.Set("limit", "5")
	q.Set("fields", "key,title,subtitle,author_name,first_publish_year,isbn,cover_i,publisher,language,subject")
	var res struct {
		Docs []struct {
			Key       string   `json:"key"`
			Title     string   `json:"title"`
			Authors   []string `json:"author_name"`
			Year      int      `json:"first_publish_year"`
			ISBN      []string `json:"isbn"`
			CoverID   int      `json:"cover_i"`
			Publisher []string `json:"publisher"`
			Language  []string `json:"language"`
			Subject   []string `json:"subject"`
		} `json:"docs"`
	}
	if err := getJSON(ctx, olBase+"/search.json?"+q.Encode(), &res, nil); err != nil {
		return nil, err
	}
	var out []*Candidate
	for _, d := range res.Docs {
		c := &Candidate{Source: "Open Library", Title: d.Title, Year: d.Year, Link: olBase + d.Key}
		c.Author = strings.Join(first(d.Authors, 3), " / ")
		if d.CoverID > 0 {
			c.Cover = fmt.Sprintf("%s/b/id/%d-L.jpg", olCoverBase, d.CoverID)
		}
		for _, i := range d.ISBN {
			if len(i) == 13 {
				c.ISBN = i
				break
			}
		}
		if len(d.Publisher) > 0 {
			c.Publisher = d.Publisher[0]
		}
		if len(d.Language) > 0 {
			c.Language = d.Language[0]
		}
		c.Tags = first(d.Subject, 5)
		key := d.Key
		c.detail = func(ctx context.Context, c *Candidate) {
			var w struct {
				Description any `json:"description"`
			}
			if getJSON(ctx, olBase+key+".json", &w, nil) == nil {
				switch v := w.Description.(type) {
				case string:
					c.Description = v
				case map[string]any:
					c.Description, _ = v["value"].(string)
				}
			}
		}
		out = append(out, c)
	}
	return out, nil
}

func first(s []string, n int) []string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// ---------- Google Books ----------

func searchGoogle(ctx context.Context, title, author, isbn string) ([]*Candidate, error) {
	q := "intitle:" + title
	if author != "" {
		q += " inauthor:" + firstAuthor(author)
	}
	if isbn != "" {
		q = "isbn:" + isbn
	}
	var res struct {
		Items []struct {
			ID   string `json:"id"`
			Info struct {
				Title       string   `json:"title"`
				Subtitle    string   `json:"subtitle"`
				Authors     []string `json:"authors"`
				Publisher   string   `json:"publisher"`
				Published   string   `json:"publishedDate"`
				Description string   `json:"description"`
				Categories  []string `json:"categories"`
				Language    string   `json:"language"`
				InfoLink    string   `json:"infoLink"`
				IDs         []struct {
					Type string `json:"type"`
					ID   string `json:"identifier"`
				} `json:"industryIdentifiers"`
				Images struct {
					Thumbnail string `json:"thumbnail"`
				} `json:"imageLinks"`
			} `json:"volumeInfo"`
		} `json:"items"`
	}
	if err := getJSON(ctx, googleBase+"/books/v1/volumes?maxResults=5&q="+url.QueryEscape(q), &res, nil); err != nil {
		return nil, err
	}
	var out []*Candidate
	for _, it := range res.Items {
		v := it.Info
		c := &Candidate{Source: "Google Books", Title: v.Title, Author: strings.Join(first(v.Authors, 3), " / "), Publisher: v.Publisher,
			Year: yearOf(v.Published), Description: stripTags(v.Description), Tags: v.Categories, Language: v.Language, Link: v.InfoLink}
		for _, id := range v.IDs {
			if id.Type == "ISBN_13" {
				c.ISBN = id.ID
			}
		}
		if t := v.Images.Thumbnail; t != "" {
			t = strings.Replace(t, "http://", "https://", 1)
			t = strings.Replace(t, "&edge=curl", "", 1)
			c.Cover = strings.Replace(t, "zoom=1", "zoom=0", 1)
		}
		out = append(out, c)
	}
	return out, nil
}

// ---------- Bangumi（漫画） ----------

func searchBangumi(ctx context.Context, q string) ([]*Candidate, error) {
	body, _ := json.Marshal(map[string]any{"keyword": q, "filter": map[string]any{"type": []int{1}}})
	req, _ := http.NewRequestWithContext(ctx, "POST", bangumiBase+"/v0/search/subjects?limit=5", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "shuku/"+version+" (NAS ebook library)")
	resp, err := metaClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("Bangumi 返回 %d", resp.StatusCode)
	}
	var res struct {
		Data []struct {
			ID      int    `json:"id"`
			Name    string `json:"name"`
			NameCN  string `json:"name_cn"`
			Summary string `json:"summary"`
			Date    string `json:"date"`
			Image   string `json:"image"`
			Images  struct {
				Large string `json:"large"`
			} `json:"images"`
			Tags []struct {
				Name string `json:"name"`
			} `json:"tags"`
			Infobox []struct {
				Key   string `json:"key"`
				Value any    `json:"value"`
			} `json:"infobox"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&res); err != nil {
		return nil, err
	}
	var out []*Candidate
	for _, d := range res.Data {
		title := d.NameCN
		if title == "" {
			title = d.Name
		}
		c := &Candidate{Source: "Bangumi", Title: title, OrigTitle: d.Name, Description: d.Summary, Year: yearOf(d.Date),
			Link: fmt.Sprintf("https://bgm.tv/subject/%d", d.ID), Cover: d.Images.Large}
		if c.Cover == "" {
			c.Cover = d.Image
		}
		if c.OrigTitle == c.Title {
			c.OrigTitle = ""
		}
		for _, t := range d.Tags {
			if len(c.Tags) < 5 {
				c.Tags = append(c.Tags, t.Name)
			}
		}
		for _, ib := range d.Infobox {
			if s, ok := ib.Value.(string); ok && (ib.Key == "作者" || ib.Key == "作画" || ib.Key == "原作") && c.Author == "" {
				c.Author = s
			}
			if s, ok := ib.Value.(string); ok && ib.Key == "出版社" {
				c.Publisher = s
			}
		}
		out = append(out, c)
	}
	return out, nil
}

// ---------- 汇总 ----------

type lookupQuery struct {
	Title, Author, ISBN, Kind string
}

// 连续几次所有数据源都连不上（NAS 没有外网），暂停联网 10 分钟，免得每本书都等超时
var netBreaker struct {
	sync.Mutex
	fails int
	until time.Time
}

func onlinePaused() bool {
	netBreaker.Lock()
	defer netBreaker.Unlock()
	return time.Now().Before(netBreaker.until)
}

func recordOnline(ok bool) {
	netBreaker.Lock()
	defer netBreaker.Unlock()
	if ok {
		netBreaker.fails = 0
		return
	}
	netBreaker.fails++
	if netBreaker.fails >= 3 {
		netBreaker.until = time.Now().Add(10 * time.Minute)
		netBreaker.fails = 0
	}
}

// 同时查几个数据源，按相似度排序
func searchOnline(ctx context.Context, q lookupQuery) ([]*Candidate, []string) {
	if onlinePaused() {
		return nil, []string{"最近几次都连不上外网，联网查找暂停 10 分钟"}
	}
	cfg := metaConfig()
	title := strings.TrimSpace(q.Title)
	if q.Kind == "comic" {
		// 漫画按系列名查，去掉卷号
		if s, _, ok := parseVolume(title); ok {
			title = s
		}
	}
	if title == "" && q.ISBN == "" {
		return nil, []string{"没有书名，无法查找"}
	}
	type job struct {
		name string
		run  func() ([]*Candidate, error)
	}
	var jobs []job
	han := hasHan(title)
	if q.Kind == "comic" && cfg.Bangumi {
		jobs = append(jobs, job{"Bangumi", func() ([]*Candidate, error) { return searchBangumi(ctx, title) }})
	}
	if cfg.Douban && (han || q.Kind == "comic") {
		dq := title
		if q.ISBN != "" {
			dq = q.ISBN
		}
		jobs = append(jobs, job{"豆瓣读书", func() ([]*Candidate, error) { return searchDouban(ctx, dq) }})
	}
	if cfg.Google {
		jobs = append(jobs, job{"Google Books", func() ([]*Candidate, error) { return searchGoogle(ctx, title, q.Author, q.ISBN) }})
	}
	if cfg.OpenLibrary {
		jobs = append(jobs, job{"Open Library", func() ([]*Candidate, error) { return searchOpenLibrary(ctx, title, q.Author, q.ISBN) }})
	}
	if cfg.Douban && !han && q.Kind != "comic" {
		jobs = append(jobs, job{"豆瓣读书", func() ([]*Candidate, error) { return searchDouban(ctx, title) }})
	}
	var mu sync.Mutex
	var all []*Candidate
	var errs []string
	var wg sync.WaitGroup
	for _, j := range jobs {
		wg.Add(1)
		go func(j job) {
			defer wg.Done()
			cs, err := j.run()
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, j.name+"："+simplifyNetErr(err))
				return
			}
			all = append(all, cs...)
		}(j)
	}
	wg.Wait()
	recordOnline(len(jobs) == 0 || len(errs) < len(jobs))
	for _, c := range all {
		c.Title = cleanSpace(c.Title)
		c.Author = normalizeAuthor(c.Author)
		scoreCandidate(c, title, q.Author)
		if q.ISBN != "" && c.ISBN == q.ISBN {
			c.Score = 1
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].Score > all[j].Score })
	if len(all) > 8 {
		all = all[:8]
	}
	return all, errs
}

func simplifyNetErr(err error) string {
	s := err.Error()
	switch {
	case errors.Is(err, context.DeadlineExceeded) || strings.Contains(s, "Timeout") || strings.Contains(s, "deadline"):
		return "超时"
	case strings.Contains(s, "no such host"):
		return "无法解析域名（NAS 可能无法访问这个网站）"
	case strings.Contains(s, "connection refused"), strings.Contains(s, "connection reset"):
		return "连接被拒绝"
	}
	return truncate(s, 80)
}

// 最可信的候选（达到门槛才返回），并补全它的详情
func bestMatch(ctx context.Context, q lookupQuery) *Candidate {
	cands, _ := searchOnline(ctx, q)
	if len(cands) == 0 || cands[0].Score < 0.84 {
		return nil
	}
	c := cands[0]
	if c.detail != nil && (c.Description == "" || c.ISBN == "") {
		c.detail(ctx, c)
	}
	return c
}

// 下载封面
func fetchImage(ctx context.Context, u string) ([]byte, error) {
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		return nil, errors.New("封面地址不正确")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", browserAgent)
	if strings.Contains(u, "doubanio.com") {
		req.Header.Set("Referer", "https://book.douban.com/")
	}
	resp, err := metaClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("下载封面失败（%d）", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 15<<20))
	if err != nil {
		return nil, err
	}
	if len(b) < 2000 {
		return nil, errors.New("封面图片太小，可能是占位图")
	}
	return b, nil
}

// 用联网结果补全空着的字段（不覆盖已有信息）；返回补了哪些
func fillFromCandidate(ctx context.Context, bookID int64, c *Candidate) []string {
	b, err := getBook(bookID)
	if err != nil {
		return nil
	}
	var filled []string
	set := func(cond bool, col string, val any, label string) {
		if cond {
			db.Exec(`UPDATE books SET `+col+`=? WHERE id=?`, val, bookID)
			filled = append(filled, label)
		}
	}
	set(b.Description == "" && c.Description != "", "description", c.Description, "简介")
	set(b.Year == 0 && c.Year > 0, "year", c.Year, "年份")
	set(b.ISBN == "" && c.ISBN != "", "isbn", c.ISBN, "ISBN")
	set(b.Publisher == "" && c.Publisher != "", "publisher", c.Publisher, "出版社")
	set(b.OrigTitle == "" && c.OrigTitle != "" && normTitle(c.OrigTitle) != normTitle(b.Title), "orig_title", c.OrigTitle, "原名")
	set(b.Translator == "" && c.Translator != "", "translator", c.Translator, "译者")
	if len(b.Tags) == 0 && len(c.Tags) > 0 {
		set(true, "tags", toJSON(first(c.Tags, 5)), "标签")
	}
	if b.Cover == "" && c.Cover != "" {
		if img, err := fetchImage(ctx, c.Cover); err == nil && saveCover(bookID, img) {
			filled = append(filled, "封面")
		}
	}
	db.Exec(`UPDATE books SET enriched_at=?,updated_at=? WHERE id=?`, now(), now(), bookID)
	updateSearchKey(bookID)
	return filled
}

// ---------- 接口 ----------

func handleEnrichBook(w http.ResponseWriter, r *http.Request) {
	b, err := getBook(pathID(r, "id"))
	if err != nil {
		errJSON(w, 404, "找不到这本书")
		return
	}
	var req struct{ Title, Author string }
	readJSON(r, &req)
	if req.Title == "" {
		req.Title = b.Title
	}
	if req.Author == "" && req.Title == b.Title {
		req.Author = b.Author
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	cands, errs := searchOnline(ctx, lookupQuery{Title: req.Title, Author: req.Author, ISBN: b.ISBN, Kind: b.Kind})
	// 前三个候选补详情，方便对比
	var wg sync.WaitGroup
	for i, c := range cands {
		if i >= 3 || c.detail == nil {
			continue
		}
		wg.Add(1)
		go func(c *Candidate) { defer wg.Done(); c.detail(ctx, c) }(c)
	}
	wg.Wait()
	if cands == nil {
		cands = []*Candidate{}
	}
	writeJSON(w, 200, map[string]any{"candidates": cands, "errors": errs})
}

func handleCoverFromURL(w http.ResponseWriter, r *http.Request) {
	var req struct{ URL string }
	readJSON(r, &req)
	img, err := fetchImage(r.Context(), req.URL)
	if err != nil {
		errJSON(w, 502, err.Error())
		return
	}
	if !saveCover(pathID(r, "id"), img) {
		errJSON(w, 400, "图片无法识别")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---------- 后台批量补全 ----------

var enrichState struct {
	sync.Mutex
	Running bool   `json:"running"`
	Done    int    `json:"done"`
	Total   int    `json:"total"`
	Filled  int    `json:"filled"`
	Current string `json:"current"`
}

func enrichStatus() map[string]any {
	enrichState.Lock()
	defer enrichState.Unlock()
	return map[string]any{"running": enrichState.Running, "done": enrichState.Done, "total": enrichState.Total, "filled": enrichState.Filled, "current": enrichState.Current}
}

func handleEnrichAll(w http.ResponseWriter, r *http.Request) {
	if !metaConfig().Enabled {
		errJSON(w, 400, "联网补全已关闭，请先在设置里打开")
		return
	}
	enrichState.Lock()
	if enrichState.Running {
		enrichState.Unlock()
		writeJSON(w, 200, map[string]any{"ok": true, "message": "已经在补全了"})
		return
	}
	var ids []int64
	rows, err := db.Query(`SELECT id FROM books WHERE status='ok' AND enriched_at=0 AND (cover='' OR description='' OR year=0) ORDER BY id`)
	if err == nil {
		for rows.Next() {
			var id int64
			rows.Scan(&id)
			ids = append(ids, id)
		}
		rows.Close()
	}
	enrichState.Running, enrichState.Done, enrichState.Total, enrichState.Filled = true, 0, len(ids), 0
	enrichState.Unlock()
	go func() {
		defer func() {
			enrichState.Lock()
			enrichState.Running, enrichState.Current = false, ""
			filled, total := enrichState.Filled, enrichState.Total
			enrichState.Unlock()
			if total > 0 {
				logEvent("info", 0, fmt.Sprintf("联网补全完成：检查了 %d 本，补全了 %d 本", total, filled))
			}
		}()
		for _, id := range ids {
			if appCtx.Err() != nil {
				return
			}
			b, err := getBook(id)
			if err != nil {
				continue
			}
			enrichState.Lock()
			enrichState.Current = b.Title
			enrichState.Unlock()
			ctx, cancel := context.WithTimeout(appCtx, 30*time.Second)
			if c := bestMatch(ctx, lookupQuery{Title: b.Title, Author: b.Author, ISBN: b.ISBN, Kind: b.Kind}); c != nil {
				if f := fillFromCandidate(ctx, id, c); len(f) > 0 {
					enrichState.Lock()
					enrichState.Filled++
					enrichState.Unlock()
				}
			} else {
				db.Exec(`UPDATE books SET enriched_at=? WHERE id=?`, now(), id)
			}
			cancel()
			enrichState.Lock()
			enrichState.Done++
			enrichState.Unlock()
			time.Sleep(1500 * time.Millisecond) // 别把人家网站刷爆
		}
	}()
	writeJSON(w, 200, map[string]any{"ok": true, "total": len(ids), "message": fmt.Sprintf("开始联网补全 %d 本书", len(ids))})
}
