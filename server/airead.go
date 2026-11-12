package main

// AI 读书助手（1.0.0.28）：前情提要、问这本书、整理划线。
//
// 都用“设置 → 识别与整理”里配好的 AI（aiChat，OpenAI 兼容接口），只发送需要的那几段文字，不上传整本书。
// 不剧透：前情提要、问这本书默认只用读者已经读到的部分（读到哪按阅读器给的位置，没给就按存的进度）。
// 结果缓存在 /data/cache/ai（每章的概要大家共用，同一本书不重复花钱；前情提要、问答、笔记按人），能删、不进备份。

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const aiNotReady = "AI 还没配置：管理员在“设置 → 识别与整理”里填好 AI 接口就能用"

// ---------- 缓存 ----------

func aiCacheKey(parts ...string) string {
	sum := sha1.Sum([]byte(aiConfig().Model + "\x00" + strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

func aiCachePath(key string) string {
	return filepath.Join(cfg.DataDir, "cache", "ai", key[:2], key+".txt")
}

func aiCacheGet(key string) (string, bool) {
	b, err := os.ReadFile(aiCachePath(key))
	if err != nil {
		return "", false
	}
	return string(b), true
}

func aiCachePut(key, v string) {
	p := aiCachePath(key)
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte(v), 0o644)
}

// ---------- 文字工具 ----------

func runeSlice(s string, from, to int) string {
	r := []rune(s)
	if from < 0 {
		from = 0
	}
	if to > len(r) {
		to = len(r)
	}
	if from >= to {
		return ""
	}
	return string(r[from:to])
}

// 太长的一章取开头、中间、结尾（概要够用，省钱）
func aiSampleText(s string, max int) string {
	n := utf8.RuneCountInString(s)
	if n <= max {
		return s
	}
	part := max / 3
	mid := n/2 - part/2
	return runeSlice(s, 0, part) + "\n……\n" + runeSlice(s, mid, mid+part) + "\n……\n" + runeSlice(s, n-part, n)
}

// 读者读到哪：第几章、章里的比例。阅读器给了位置就用它（比存的进度新），没给按存的进度
func aiReadPoint(r *http.Request, b *Book, f *File, chs []WatchChapter) (int, float64) {
	loc := r.URL.Query().Get("loc")
	pct, _ := strconv.ParseFloat(r.URL.Query().Get("pct"), 64)
	if loc == "" && r.URL.Query().Get("pct") == "" {
		p := getProgress(meID(r), b.ID)
		loc, pct = p.Location, p.Percent
		if p.FileID != 0 && p.FileID != f.ID && !strings.HasPrefix(loc, "sec:") {
			loc = ""
		}
	}
	return resumePoint(chs, loc, pct)
}

// 某章第 off 个字在阅读器里的位置（点一下跳过去用）：sec:节:比例 / p:页:0，都不行就给全书百分比
func aiLocOf(chs []WatchChapter, i, off int) (string, float64) {
	c := chs[i]
	total := chs[len(chs)-1].Start + chs[len(chs)-1].Chars
	pct := 0.0
	if total > 0 {
		pct = float64(c.Start+off) / float64(total)
	}
	var sec int
	var f0 float64
	if n, _ := fmt.Sscanf(c.Loc, "sec:%d:%f", &sec, &f0); n >= 1 {
		f1 := 1.0
		if i+1 < len(chs) {
			var s2 int
			var f2 float64
			if m, _ := fmt.Sscanf(chs[i+1].Loc, "sec:%d:%f", &s2, &f2); m == 2 && s2 == sec {
				f1 = f2
			}
		}
		fr := f0
		if c.Chars > 0 {
			fr = f0 + float64(off)/float64(c.Chars)*(f1-f0)
		}
		return fmt.Sprintf("sec:%d:%.4f", sec, fr), pct
	}
	return c.Loc, pct
}

func aiBook(w http.ResponseWriter, r *http.Request) (*Book, *File, []WatchChapter, bool) {
	if !aiReady() {
		errJSON(w, 400, aiNotReady)
		return nil, nil, nil, false
	}
	return chapterBook(w, r)
}

// ---------- 每章概要（大家共用的缓存） ----------

func chapterSummary(ctx context.Context, b *Book, f *File, c WatchChapter) (string, error) {
	key := aiCacheKey("chapter", strconv.FormatInt(f.ID, 10), f.Hash, strconv.Itoa(c.Index))
	if v, ok := aiCacheGet(key); ok {
		return v, nil
	}
	user := fmt.Sprintf("书：《%s》%s\n章节：%s\n\n请用简体中文概括这一章的主要内容（人物、发生的事、讲的观点），120 字以内，不评价，不写“本章”之类的开头。\n\n正文：\n%s",
		b.Title, ifs(b.Author != "", "，"+b.Author, ""), c.Title, aiSampleText(c.text, 7000))
	out, err := aiChat(ctx, "你是读书助手，帮读者概括书的内容。", user, 0.3)
	if err != nil {
		return "", err
	}
	aiCachePut(key, out)
	return out, nil
}

// ---------- 前情提要 ----------
//
// GET /api/books/{id}/ai/recap?loc=&pct=[&force=1]
// 最近读过的几章各自的概要（缓存）+ 这一章读到的地方之前的原文，合成一段“前情提要”和“上次读到”。
func handleAIRecap(w http.ResponseWriter, r *http.Request) {
	b, f, chs, ok := aiBook(w, r)
	if !ok {
		return
	}
	i, frac := aiReadPoint(r, b, f, chs)
	cur := chs[i]
	pos := int(frac * float64(cur.Chars))
	if i == 0 && pos < 200 {
		writeJSON(w, 200, map[string]any{"recap": "刚开始读，还没有前情。", "chapter": cur.Title})
		return
	}
	// 同一个人在差不多的位置（这一章的每 10%）问过：直接给
	key := aiCacheKey("recap", strconv.FormatInt(meID(r), 10), strconv.FormatInt(f.ID, 10), f.Hash, strconv.Itoa(i), strconv.Itoa(int(frac*10)))
	if r.URL.Query().Get("force") == "" {
		if v, ok := aiCacheGet(key); ok {
			writeJSON(w, 200, map[string]any{"recap": v, "chapter": cur.Title, "cached": true})
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 150*time.Second)
	defer cancel()
	// 前面最多 6 章的概要（同时请求 3 个）
	from := i - 6
	if from < 0 {
		from = 0
	}
	sums := make([]string, i-from)
	var wg sync.WaitGroup
	var firstErr error
	var mu sync.Mutex
	sem := make(chan struct{}, 3)
	for k := from; k < i; k++ {
		wg.Add(1)
		go func(k int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			s, err := chapterSummary(ctx, b, f, chs[k])
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				return
			}
			sums[k-from] = fmt.Sprintf("【%s】%s", chs[k].Title, s)
		}(k)
	}
	wg.Wait()
	if firstErr != nil && i-from > 0 && strings.Join(sums, "") == "" {
		errJSON(w, 502, firstErr.Error())
		return
	}
	var u strings.Builder
	fmt.Fprintf(&u, "书：《%s》%s\n", b.Title, ifs(b.Author != "", "，"+b.Author, ""))
	if from > 0 {
		fmt.Fprintf(&u, "（更早的 %d 章略）\n", from)
	}
	for _, s := range sums {
		if s != "" {
			u.WriteString(s + "\n")
		}
	}
	fmt.Fprintf(&u, "\n现在这一章【%s】读到的地方之前的原文：\n%s\n\n", cur.Title, runeSlice(cur.text, pos-4500, pos))
	u.WriteString(`读者隔了一段时间回来接着读，请写“前情提要”帮他想起来：
先用 3～6 句话讲清楚之前的主要内容，越靠近读到的地方越详细；最后单独一行，以“上次读到：”开头，一句话说读到的地方正在发生什么。
只根据上面给的内容，不要提读者还没读到的情节，不要猜后面会怎样。用简体中文，300 字以内，不用 Markdown 标题。`)
	out, err := aiChat(ctx, "你是读书助手。", u.String(), 0.3)
	if err != nil {
		errJSON(w, 502, err.Error())
		return
	}
	aiCachePut(key, out)
	writeJSON(w, 200, map[string]any{"recap": out, "chapter": cur.Title})
}

// ---------- 问这本书 ----------
//
// POST /api/books/{id}/ai/ask?loc=&pct= {question, all}
// 先让 AI 从问题里挑几个检索词，在书里（默认只在读到的地方以前）找出相关的段落，再根据这些段落回答，
// 回答里的 [1][2] 对应返回的 sources（带位置，点一下跳过去）。

var aiNumRe = regexp.MustCompile(`^[\s\d.、:：\-*•]+`)

func handleAIAsk(w http.ResponseWriter, r *http.Request) {
	b, f, chs, ok := aiBook(w, r)
	if !ok {
		return
	}
	var req struct {
		Question string `json:"question"`
		All      bool   `json:"all"` // 也看读到的地方之后的（会剧透）
	}
	if readJSON(r, &req) != nil || strings.TrimSpace(req.Question) == "" {
		errJSON(w, 400, "请输入问题")
		return
	}
	q := truncate(strings.TrimSpace(req.Question), 300)
	i, frac := aiReadPoint(r, b, f, chs)
	limitCh, limitOff := i, int(frac*float64(chs[i].Chars))
	if req.All {
		limitCh, limitOff = len(chs)-1, chs[len(chs)-1].Chars
	}
	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()

	// 1. 检索词
	kw, err := aiChat(ctx, "你帮读者在书里找资料。",
		fmt.Sprintf("书：《%s》\n读者的问题：%s\n\n请给出 2～6 个最可能在书的原文里出现的检索词（人名、地名、名词、短语），用这本书原文的语言（中文书用中文），每行一个，不要编号，不要解释。", b.Title, q), 0.2)
	if err != nil {
		errJSON(w, 502, err.Error())
		return
	}
	var words []string
	for _, l := range strings.Split(kw, "\n") {
		l = strings.Trim(aiNumRe.ReplaceAllString(strings.TrimSpace(l), ""), "\"“”'‘’「」《》")
		if utf8.RuneCountInString(l) >= 1 && utf8.RuneCountInString(l) <= 20 {
			words = append(words, l)
		}
	}
	// 2. 找段落：每处命中取前后各 300 字，命中的检索词越多越靠前，重叠的合并
	type hit struct {
		ch, at, score int
	}
	var hits []hit
	for k := 0; k <= limitCh && k < len(chs); k++ {
		text := []rune(chs[k].text)
		end := len(text)
		if k == limitCh && limitOff < end {
			end = limitOff
		}
		low := strings.ToLower(string(text[:end]))
		lr := []rune(low)
		for _, wd := range words {
			wl := []rune(strings.ToLower(wd))
			for p := 0; p+len(wl) <= len(lr); p++ {
				if string(lr[p:p+len(wl)]) == string(wl) {
					hits = append(hits, hit{k, p, len(wl)})
					p += len(wl)
				}
			}
		}
	}
	// 按窗口合并打分
	type win struct {
		ch, from, to, score int
		words               map[string]bool
	}
	var wins []*win
	sort.Slice(hits, func(a, c int) bool {
		if hits[a].ch != hits[c].ch {
			return hits[a].ch < hits[c].ch
		}
		return hits[a].at < hits[c].at
	})
	for _, h := range hits {
		// 挨得近的并成一段（每段最长约 900 字，太长的另起一段）
		if n := len(wins); n > 0 && wins[n-1].ch == h.ch && h.at-wins[n-1].to < 200 && h.at+300-wins[n-1].from <= 900 {
			wins[n-1].to = h.at + 300
			wins[n-1].score += h.score
			continue
		}
		wins = append(wins, &win{ch: h.ch, from: h.at - 300, to: h.at + 300, score: h.score})
	}
	sort.SliceStable(wins, func(a, c int) bool { return wins[a].score > wins[c].score })
	if len(wins) > 8 {
		wins = wins[:8]
	}
	sort.SliceStable(wins, func(a, c int) bool {
		if wins[a].ch != wins[c].ch {
			return wins[a].ch < wins[c].ch
		}
		return wins[a].from < wins[c].from
	})
	type source struct {
		N       int     `json:"n"`
		Chapter string  `json:"chapter"`
		Text    string  `json:"text"`
		Loc     string  `json:"loc"`
		Pct     float64 `json:"pct"`
	}
	var srcs []source
	var ctxText strings.Builder
	for n, wn := range wins {
		c := chs[wn.ch]
		t := strings.TrimSpace(runeSlice(c.text, wn.from, wn.to))
		at := wn.from
		if at < 0 {
			at = 0
		}
		loc, pct := aiLocOf(chs, wn.ch, at)
		srcs = append(srcs, source{N: n + 1, Chapter: c.Title, Text: truncate(t, 160), Loc: loc, Pct: pct})
		fmt.Fprintf(&ctxText, "[%d]（%s）%s\n\n", n+1, c.Title, t)
	}
	// 一处都没找到：用读到的地方前面的一段，让 AI 至少能答最近发生的事
	if len(srcs) == 0 {
		c := chs[i]
		off := int(frac * float64(c.Chars))
		t := runeSlice(c.text, off-3000, off)
		loc, pct := aiLocOf(chs, i, max(0, off-3000))
		srcs = append(srcs, source{N: 1, Chapter: c.Title, Text: truncate(strings.TrimSpace(t), 160), Loc: loc, Pct: pct})
		fmt.Fprintf(&ctxText, "[1]（%s）%s\n\n", c.Title, t)
	}
	scope := "读者已经读到的部分"
	if req.All {
		scope = "全书"
	}
	ans, err := aiChat(ctx, "你是读书助手，只根据书里的原文回答读者的问题。",
		fmt.Sprintf("书：《%s》%s\n下面是从%s里找到的几段原文：\n\n%s读者的问题：%s\n\n请只根据这些原文回答；原文里没有就直接说“书里（目前读到的部分）没找到”。引用到哪段，就在那句话后面标上 [编号]。%s用简体中文，300 字以内，不用 Markdown 标题。",
			b.Title, ifs(b.Author != "", "，"+b.Author, ""), scope, ctxText.String(), q, ifs(req.All, "", "不要透露读者还没读到的内容。")), 0.3)
	if err != nil {
		errJSON(w, 502, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"answer": ans, "sources": srcs, "words": words})
}

// ---------- 整理划线和笔记 ----------
//
// POST /api/books/{id}/ai/notes：这个人在这本书里的划线、批注整理成一份 Markdown 读书笔记（同样的划线不重复花钱）
func handleAINotes(w http.ResponseWriter, r *http.Request) {
	if !aiReady() {
		errJSON(w, 400, aiNotReady)
		return
	}
	b, ok := visibleBook(w, r, pathID(r, "id"))
	if !ok {
		return
	}
	rows, err := db.Query(`SELECT label,text,note FROM bookmarks WHERE book_id=? AND user_id=? AND kind='highlight' ORDER BY id`, b.ID, meID(r))
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	var items strings.Builder
	n := 0
	for rows.Next() {
		var label, text, note string
		rows.Scan(&label, &text, &note)
		if strings.TrimSpace(text) == "" && strings.TrimSpace(note) == "" {
			continue
		}
		n++
		fmt.Fprintf(&items, "- %s%s", ifs(label != "", "（"+truncate(label, 40)+"）", ""), truncate(strings.TrimSpace(text), 400))
		if strings.TrimSpace(note) != "" {
			fmt.Fprintf(&items, "\n  我的批注：%s", truncate(strings.TrimSpace(note), 300))
		}
		items.WriteString("\n")
	}
	rows.Close()
	if n == 0 {
		errJSON(w, 400, "这本书还没有划线。阅读时选中文字，点颜色就能划线")
		return
	}
	body := items.String()
	if utf8.RuneCountInString(body) > 14000 {
		body = runeSlice(body, 0, 14000) + "\n……（后面还有，略）"
	}
	key := aiCacheKey("notes", strconv.FormatInt(meID(r), 10), strconv.FormatInt(b.ID, 10), body)
	if v, ok := aiCacheGet(key); ok && r.URL.Query().Get("force") == "" {
		writeJSON(w, 200, map[string]any{"markdown": v, "count": n, "cached": true})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()
	out, err := aiChat(ctx, "你是读书助手，帮读者把划线和批注整理成读书笔记。",
		fmt.Sprintf(`书：《%s》%s
下面是读者在这本书里的 %d 条划线（括号里是所在章节）和批注：

%s
请整理成一份读书笔记，用 Markdown，按这几部分：
## 核心要点（5～8 条，一句话一条）
## 按章节（每章几条要点，最有代表性的原句用 > 引用）
## 我的想法（根据读者写的批注归纳，没有批注就省略这一部分）
## 可以继续想的问题（2～3 个）
用简体中文，只根据划线和批注，不要编造书里没有的内容。`, b.Title, ifs(b.Author != "", "，"+b.Author, ""), n, body), 0.3)
	if err != nil {
		errJSON(w, 502, err.Error())
		return
	}
	aiCachePut(key, out)
	writeJSON(w, 200, map[string]any{"markdown": out, "count": n})
}

func routesAIRead(mux *http.ServeMux) {
	a := requireAuth
	mux.HandleFunc("GET /api/books/{id}/ai/recap", a(handleAIRecap))
	mux.HandleFunc("POST /api/books/{id}/ai/ask", a(handleAIAsk))
	mux.HandleFunc("POST /api/books/{id}/ai/notes", a(handleAINotes))
}
