package main

// 从链接下载入库：网页里粘贴链接、“发现”页一键入库、浏览器插件让 NAS 直接下载，都走这里。
// 下载到投递目录（临时文件带 .shuku-part 后缀，投递监听会跳过），完成后改名，交给入库流程识别整理。

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type FetchJob struct {
	ID       string `json:"id"`
	URL      string `json:"url"`
	Name     string `json:"name"`
	Status   string `json:"status"` // queued | downloading | done | error
	Bytes    int64  `json:"bytes"`
	Total    int64  `json:"total"`
	Error    string `json:"error,omitempty"`
	Source   string `json:"source,omitempty"`
	File     string `json:"file,omitempty"` // 放进投递目录后的文件名
	Created  int64  `json:"created_at"`
	Finished int64  `json:"finished_at,omitempty"`

	catalog string
	hint    ingestHint
	referer string
	cancel  context.CancelFunc
}

var fetchJobs = struct {
	sync.Mutex
	list []*FetchJob
}{}

var fetchSem = make(chan struct{}, 3)

const maxFetchSize = 4 << 30

var fetchClient = &http.Client{
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 20 * time.Second}).DialContext,
		TLSHandshakeTimeout:   20 * time.Second,
		ResponseHeaderTimeout: 3 * time.Minute, // 维基文库现场生成 EPUB 比较慢
	},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("跳转次数太多")
		}
		return nil
	},
}

var mimeExt = map[string]string{
	"application/epub+zip": "epub", "application/pdf": "pdf", "application/x-mobipocket-ebook": "mobi",
	"application/vnd.amazon.ebook": "azw3", "application/x-mobi8-ebook": "azw3", "application/x-fictionbook+xml": "fb2",
	"application/vnd.comicbook+zip": "cbz", "application/x-cbz": "cbz", "application/vnd.comicbook-rar": "cbr", "application/x-cbr": "cbr",
	"application/x-rar-compressed": "rar", "application/vnd.rar": "rar", "application/x-7z-compressed": "7z", "image/vnd.djvu": "djvu",
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document": "docx", "application/rtf": "rtf", "text/plain": "txt",
}

func newFetchJob(rawURL, name, source, catalog, referer string, hint ingestHint) (*FetchJob, error) {
	rawURL = strings.TrimSpace(rawURL)
	if !strings.HasPrefix(rawURL, "ia:") {
		u, err := url.Parse(rawURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, errors.New("请填写 http:// 或 https:// 开头的下载链接")
		}
	}
	fetchJobs.Lock()
	defer fetchJobs.Unlock()
	for _, j := range fetchJobs.list {
		if j.URL == rawURL && (j.Status == "queued" || j.Status == "downloading") {
			return j, nil // 同一个链接正在下载
		}
	}
	j := &FetchJob{ID: randToken(5), URL: rawURL, Name: strings.TrimSpace(name), Status: "queued", Source: source, Created: now(), catalog: catalog, referer: referer, hint: hint}
	fetchJobs.list = append(fetchJobs.list, j)
	// 只保留最近 100 条
	if len(fetchJobs.list) > 100 {
		fetchJobs.list = fetchJobs.list[len(fetchJobs.list)-100:]
	}
	go runFetch(j)
	return j, nil
}

func setJob(j *FetchJob, f func(j *FetchJob)) {
	fetchJobs.Lock()
	f(j)
	fetchJobs.Unlock()
}

func runFetch(j *FetchJob) {
	ctx, cancel := context.WithTimeout(appCtx, 60*time.Minute)
	setJob(j, func(j *FetchJob) { j.cancel = cancel })
	defer cancel()
	select {
	case fetchSem <- struct{}{}:
		defer func() { <-fetchSem }()
	case <-ctx.Done():
		// 还在排队就取消了、超时了：也要标成结束，不然一直显示“排队中”
		setJob(j, func(j *FetchJob) {
			j.Finished = now()
			j.Status, j.Error = "error", "排队太久，没有开始下载"
			if errors.Is(ctx.Err(), context.Canceled) {
				j.Error = cancelReason()
			}
		})
		return
	}
	setJob(j, func(j *FetchJob) { j.Status = "downloading" })
	name, err := doFetch(ctx, j)
	setJob(j, func(j *FetchJob) {
		j.Finished = now()
		if err != nil {
			j.Status, j.Error = "error", err.Error()
			if errors.Is(ctx.Err(), context.Canceled) {
				j.Error = cancelReason()
			}
		} else {
			j.Status, j.File = "done", name
		}
	})
	if err != nil {
		logEvent("warn", 0, fmt.Sprintf("下载失败：%s（%s）", truncate(ifs(j.Name != "", j.Name, j.URL), 60), err.Error()))
	} else {
		logEvent("info", 0, "已下载 "+name+"，正在入库")
	}
}

// 取消的原因：书库在重启（自动升级）时不算用户取消，插件看到会重新提交
func cancelReason() string {
	if appCtx.Err() != nil {
		return "书库重启了，下载中断"
	}
	return "已取消"
}

func doFetch(ctx context.Context, j *FetchJob) (string, error) {
	target, title := j.URL, j.Name
	if strings.HasPrefix(target, "ia:") {
		u, t, err := iaResolve(ctx, strings.TrimPrefix(target, "ia:"))
		if err != nil {
			return "", err
		}
		target = u
		if title == "" {
			title = t
		}
	}
	req, err := http.NewRequestWithContext(ctx, "GET", target, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", browserAgent)
	req.Header.Set("Accept", "application/epub+zip, application/pdf, application/octet-stream;q=0.9, */*;q=0.5")
	if j.referer != "" {
		req.Header.Set("Referer", j.referer)
	}
	if c := catalogByID(j.catalog); c != nil && (c.Username != "" || c.Password != "") && sameHost(target, c.URL) {
		req.SetBasicAuth(c.Username, c.Password)
	}
	resp, err := fetchClient.Do(req)
	if err != nil {
		return "", errors.New(simplifyNetErr(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return "", fmt.Errorf("网站拒绝下载（%d），可能需要登录，可以用浏览器插件添加", resp.StatusCode)
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("网站返回 %d", resp.StatusCode)
	}
	ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	fileName := ""
	if _, params, err := mime.ParseMediaType(resp.Header.Get("Content-Disposition")); err == nil {
		fileName = params["filename"]
	}
	if fileName == "" {
		if p, err := url.PathUnescape(path.Base(resp.Request.URL.Path)); err == nil && p != "/" && p != "." {
			fileName = p
		}
	}
	ext := extOf(fileName)
	if _, ok := formats[ext]; !ok {
		ext = mimeExt[ct]
	}
	// 网页（包括 .html 结尾的链接）一律当成网页，不当成书
	if ext == "" || ct == "text/html" || ct == "application/xhtml+xml" {
		return "", errors.New("这个链接打开的是网页，不是电子书文件。请复制文件本身的下载链接，或者用浏览器插件添加")
	}
	if _, ok := formats[ext]; !ok {
		return "", fmt.Errorf("不支持的文件类型：%s", ext)
	}
	// 文件名：书源给了书名就用“书名 - 作者”，否则用网站给的文件名
	base := strings.TrimSuffix(fileName, filepath.Ext(fileName))
	if title != "" {
		base = title
	}
	if base == "" {
		base = "下载的书"
	}
	final := safeName(truncate(base, 120)) + "." + ext
	if resp.ContentLength > maxFetchSize {
		return "", errors.New("文件超过 4GB")
	}
	setJob(j, func(j *FetchJob) {
		j.Total = resp.ContentLength
		if j.Name == "" {
			j.Name = final
		}
	})
	os.MkdirAll(cfg.Inbox, 0o755)
	tmp := filepath.Join(cfg.Inbox, ".fetch-"+j.ID+".shuku-part")
	out, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	buf := make([]byte, 256<<10)
	var n int64
	for {
		k, rerr := resp.Body.Read(buf)
		if k > 0 {
			if _, werr := out.Write(buf[:k]); werr != nil {
				out.Close()
				os.Remove(tmp)
				return "", werr
			}
			n += int64(k)
			if n > maxFetchSize {
				out.Close()
				os.Remove(tmp)
				return "", errors.New("文件超过 4GB")
			}
			setJob(j, func(j *FetchJob) { j.Bytes = n })
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			out.Close()
			os.Remove(tmp)
			return "", fmt.Errorf("下载中断：%s", simplifyNetErr(rerr))
		}
	}
	out.Close()
	if n == 0 {
		os.Remove(tmp)
		return "", errors.New("下载到的文件是空的")
	}
	dest := freePath(filepath.Join(cfg.Inbox, final))
	if err := os.Rename(tmp, dest); err != nil {
		os.Remove(tmp)
		return "", err
	}
	old := time.Now().Add(-10 * time.Second)
	os.Chtimes(dest, old, old)
	setIngestHint(dest, j.hint)
	kickInbox()
	return filepath.Base(dest), nil
}

// ---------- 接口 ----------

func handleFetch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL     string `json:"url"`
		Name    string `json:"name"`
		Source  string `json:"source"`
		Catalog string `json:"catalog"`
		Referer string `json:"referer"`
		// 书源上的书目信息（发现页加入时带上）：入库时用来补书名作者、自动分类
		Title    string   `json:"title"`
		Author   string   `json:"author"`
		Summary  string   `json:"summary"`
		Language string   `json:"language"`
		Year     int      `json:"year"`
		Tags     []string `json:"tags"`
	}
	if readJSON(r, &req) != nil {
		errJSON(w, 400, "格式不正确")
		return
	}
	hint := ingestHint{Title: strings.TrimSpace(req.Title), Author: strings.TrimSpace(req.Author), Summary: truncate(strings.TrimSpace(req.Summary), 600),
		Language: strings.TrimSpace(req.Language), Year: req.Year, Source: strings.TrimSpace(req.Source)}
	for _, t := range req.Tags {
		if t = strings.TrimSpace(t); t != "" && len(hint.Tags) < 10 {
			hint.Tags = append(hint.Tags, truncate(t, 60))
		}
	}
	j, err := newFetchJob(req.URL, req.Name, req.Source, req.Catalog, req.Referer, hint)
	if err != nil {
		errJSON(w, 400, err.Error())
		return
	}
	fetchJobs.Lock()
	defer fetchJobs.Unlock()
	writeJSON(w, 200, j)
}

func handleFetchList(w http.ResponseWriter, r *http.Request) {
	fetchJobs.Lock()
	out := make([]FetchJob, 0, len(fetchJobs.list))
	for _, j := range fetchJobs.list {
		out = append(out, *j)
	}
	fetchJobs.Unlock()
	sort.SliceStable(out, func(a, b int) bool { return out[a].Created > out[b].Created })
	writeJSON(w, 200, out)
}

func handleFetchCancel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	fetchJobs.Lock()
	defer fetchJobs.Unlock()
	keep := fetchJobs.list[:0]
	for _, j := range fetchJobs.list {
		if j.ID == id {
			if j.cancel != nil && (j.Status == "queued" || j.Status == "downloading") {
				j.cancel()
			}
			continue
		}
		keep = append(keep, j)
	}
	fetchJobs.list = keep
	writeJSON(w, 200, map[string]bool{"ok": true})
}
