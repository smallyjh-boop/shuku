package main

// 识别书名：规则清洗文件名 + 内嵌元数据 + AI

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

type Identity struct {
	Title       string   `json:"title"`
	OrigTitle   string   `json:"original_title"`
	Author      string   `json:"author"`
	Translator  string   `json:"translator"`
	Series      string   `json:"series"`
	SeriesIndex float64  `json:"series_index"`
	Year        int      `json:"year"`
	Category    string   `json:"category"`
	Tags        []string `json:"tags"`
	Language    string   `json:"language"`
	Description string   `json:"description"`
	Confidence  float64  `json:"confidence"`
	Source      string   `json:"source"`
}

var defaultCategories = []string{"小说", "文学", "历史", "社科", "哲学宗教", "经济管理", "科学技术", "计算机", "生活", "艺术", "少儿", "教育考试", "漫画", "其他"}

func categories() []string {
	var c []string
	if json.Unmarshal([]byte(getSetting("categories", "")), &c) == nil && len(c) > 0 {
		return c
	}
	return defaultCategories
}

// ---------- 文件名清洗 ----------

var junkPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)[\[【(（{]\s*(www\.|https?://)?[a-z0-9-]+\.(com|net|org|cn|cc|me|io|info|top|xyz|club|vip)(\.[a-z]{2})?\s*[\]】)）}]`),
	regexp.MustCompile(`(?i)(www\.)?[a-z0-9-]+\.(com|net|org|cn|cc|top|xyz|vip)\b`),
	regexp.MustCompile(`(?i)[\[【(（]\s*(漫画|汉化|漢化|中文|日漫|美漫|韩漫|港版|台版|东立|東立|单行本|單行本|连载|彩色|黑白|生肉|熟肉|z-?lib(rary)?(\.org)?|libgen|anna'?s archive|epub|mobi|azw3|pdf|txt|kindle|精[校排品]版?|完整版|完结|完本|全本|全集|校对版|高清|扫描版|文字版|修订版|珍藏版|典藏版|插图版|无删减|未删减|中文版|简体|繁体|中英双语|双语|电子书|下载|整理|免费|最新|合集|共\d+[册本部卷])[^\]】)）]*[\]】)）]`),
	regexp.MustCompile(`(?i)\s*[-_—]?\s*(精[校排品]版|完整版|校对版|全本|完结版|完本|高清版|扫描版|文字版|epub版|txt版|kindle版)\s*$`),
	regexp.MustCompile(`(?i)(\s*[-_]?\s*副本|\s*-\s*copy|^copy of\s+|\s*[(（]\d[)）]|_\d|[\s_-]+final|[\s_-]+v\d)\s*$`),
	regexp.MustCompile(`(?i)^\s*(microsoft (word|powerpoint)|wps)\s*-\s*`),
	regexp.MustCompile(`(?i)^\s*(\d{1,4}\s*[._、-]\s*)(?:\D)`), // 开头的序号 "01. "
}

// Anna's Archive / Z-Library 常见命名："书名 -- 作者 -- 出版社, 2019 -- ISBN -- md5 -- Anna’s Archive"
var annaRe = regexp.MustCompile(`\s--\s`)

type parsedName struct {
	Title, Author, Series string
	Index                 float64
	Year                  int
	Strong                bool // 文件名格式很明确（《书名》作者：xxx 之类），可以直接采信
}

func parseFileName(name string) parsedName {
	base := strings.TrimSuffix(filepath.Base(name), filepath.Ext(name))
	if strings.HasSuffix(strings.ToLower(base), ".fb2") {
		base = base[:len(base)-4]
	}
	var pn parsedName
	if parts := annaRe.Split(base, -1); len(parts) >= 3 {
		pn.Title = strings.TrimSpace(parts[0])
		pn.Author = strings.TrimSpace(strings.Split(parts[1], ";")[0])
		pn.Year = yearOf(parts[2])
		pn.Strong = true
		return pn
	}
	s := strings.NewReplacer("_", " ", "　", " ").Replace(base)
	for i := 0; i < 3; i++ {
		for j, re := range junkPatterns {
			if j == len(junkPatterns)-1 {
				if m := re.FindStringSubmatchIndex(s); m != nil {
					s = s[m[3]:]
				}
				continue
			}
			s = re.ReplaceAllString(s, " ")
		}
	}
	s = cleanSpace(s)
	if y := regexp.MustCompile(`[(（\[]((1[5-9]|20)\d\d)[)）\]]`).FindStringSubmatch(s); y != nil {
		pn.Year, _ = strconv.Atoi(y[1])
		s = cleanSpace(strings.Replace(s, y[0], " ", 1))
	}
	// 《书名》作者
	if m := regexp.MustCompile(`《([^》]+)》\s*(?:作者[:：]?\s*)?([^\s(（\[【].*)?`).FindStringSubmatch(s); m != nil {
		pn.Title = m[1]
		pn.Author = strings.Trim(strings.TrimSpace(m[2]), "-_ ")
		pn.Author = strings.TrimSuffix(strings.TrimSuffix(pn.Author, "著"), "作品")
		pn.Strong = pn.Author != "" && strings.Contains(s, "作者")
		return pn
	}
	// 书名 作者：xxx
	if m := regexp.MustCompile(`^(.+?)\s*作者[:：]\s*(.+)$`).FindStringSubmatch(s); m != nil {
		pn.Title, pn.Author = m[1], m[2]
		pn.Strong = true
		return pn
	}
	// [作者] 书名（"[美]海明威" 这种国籍前缀除外）
	if m := regexp.MustCompile(`^[\[【]([^\]】]{2,30})[\]】]\s*([^-–—]+)$`).FindStringSubmatch(s); m != nil {
		pn.Author, pn.Title = m[1], m[2]
		return pn
	}
	// 书名（作者）或 书名 (作者 著)
	if m := regexp.MustCompile(`^(.+?)\s*[(（]([^)）]{1,30}?)\s*(著|编著|主编|译)?[)）]$`).FindStringSubmatch(s); m != nil {
		pn.Title, pn.Author = m[1], m[2]
		return pn
	}
	// 书名 - 作者 / 作者 - 书名（中文名短的一般是作者）
	if parts := regexp.MustCompile(`\s+[-–—]\s+|\s*[-–—]{2}\s*`).Split(s, 3); len(parts) >= 2 {
		a, b := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		nation := regexp.MustCompile(`^[\[【(（〔][美英法德日俄意韩奥荷瑞丹挪芬波捷西葡希加澳印以][\]】)）〕]`)
		if nation.MatchString(a) || (looksLikePerson(a) && !looksLikePerson(b)) {
			pn.Author, pn.Title = a, b
		} else {
			pn.Title, pn.Author = a, b
		}
		return pn
	}
	pn.Title = s
	return pn
}

// 2~4 个汉字、可能带国籍前缀，或者两三个英文单词
func looksLikePerson(s string) bool {
	s = regexp.MustCompile(`^[\[【(（]?[美英法德日俄意韩中][\]】)）]?\s*`).ReplaceAllString(s, "")
	r := []rune(s)
	han := 0
	for _, c := range r {
		if unicode.Is(unicode.Han, c) {
			han++
		}
	}
	if han == len(r) && han >= 2 && han <= 4 {
		return true
	}
	if strings.Contains(s, "·") && han >= 3 && han <= 12 {
		return true
	}
	return false
}

// 漫画卷号："进击的巨人 第03卷"、"One Piece v05"、"海贼王 Vol.12"、"XXX 03"
var volRes = []*regexp.Regexp{
	regexp.MustCompile(`^(.+?)\s*第\s*([0-9０-９]+(?:\.\d)?)\s*[卷册部话集]`),
	regexp.MustCompile(`(?i)^(.+?)\s*(?:vol\.?|v\.?|volume|tome|#)\s*([0-9]+(?:\.\d)?)\b`),
	regexp.MustCompile(`^(.+?)\s+([0-9]{1,4}(?:\.\d)?)$`),
	regexp.MustCompile(`^(.+?)\s*[(（]([0-9]{1,3})[)）]$`),
}

func parseVolume(title string) (string, float64, bool) {
	t := strings.Map(func(r rune) rune {
		if r >= '０' && r <= '９' {
			return r - '０' + '0'
		}
		return r
	}, title)
	for _, re := range volRes {
		if m := re.FindStringSubmatch(t); m != nil {
			n, err := strconv.ParseFloat(m[2], 64)
			if err == nil && strings.TrimSpace(m[1]) != "" {
				return strings.TrimSpace(strings.Trim(m[1], "-_ ")), n, true
			}
		}
	}
	return title, 0, false
}

// ---------- 规则识别（没有 AI 时） ----------

func ruleIdentify(fileName string, m Meta, kind string) Identity {
	pn := parseFileName(fileName)
	id := Identity{Source: "文件名"}
	titleOK := m.Title != "" && !looksGarbage(m.Title)
	if titleOK {
		id.Title, id.Source = m.Title, "内嵌信息"
	} else {
		id.Title = pn.Title
	}
	id.Author = m.Author
	if id.Author == "" {
		id.Author = pn.Author
	}
	id.Translator = m.Translator
	id.Series, id.SeriesIndex = m.Series, m.SeriesIndex
	id.Year = m.Year
	if id.Year == 0 {
		id.Year = pn.Year
	}
	id.Tags = m.Tags
	id.Language = m.Language
	id.Description = m.Description
	if kind == "comic" {
		id.Category = "漫画"
		if id.Series == "" {
			if s, n, ok := parseVolume(id.Title); ok {
				id.Series, id.SeriesIndex = s, n
				id.Title = fmt.Sprintf("%s 第%s卷", s, strconv.FormatFloat(n, 'f', -1, 64))
			} else {
				id.Series = id.Title
			}
		}
	}
	switch {
	case titleOK && m.Author != "":
		id.Confidence = 0.9
	case kind == "comic" && id.Series != "":
		id.Confidence = 0.86
	case !titleOK && pn.Strong && id.Author != "":
		id.Confidence = 0.88
	case id.Title != "" && id.Author != "":
		id.Confidence = 0.7
	default:
		id.Confidence = 0.4
	}
	if id.Category == "" {
		id.Category = guessCategory(id.Tags)
	}
	id.Author = normalizeAuthor(id.Author)
	id.Translator = normalizeAuthor(id.Translator)
	return id
}

// 文件名式、全是数字或乱码的标题
func looksGarbage(s string) bool {
	l := strings.ToLower(s)
	if regexp.MustCompile(`^[0-9a-f_\-\s.]{6,}$`).MatchString(l) {
		return true
	}
	for _, ext := range []string{".txt", ".epub", ".mobi", ".pdf", ".doc", ".azw3"} {
		if strings.HasSuffix(l, ext) {
			return true
		}
	}
	return strings.Contains(l, "unknown") || strings.Contains(s, "�")
}

// 作者名规范：去掉汉字之间的空格（"刘 慈欣"→"刘慈欣"）、结尾的"著"
var hanSpaceRe = regexp.MustCompile(`(\p{Han})\s+(\p{Han})`)

func normalizeAuthor(a string) string {
	a = cleanSpace(a)
	a = regexp.MustCompile(`^[\[【(（〔][美英法德日俄意韩奥荷瑞丹挪芬波捷西葡希加澳印以][\]】)）〕]\s*`).ReplaceAllString(a, "")
	for i := 0; i < 3; i++ {
		a = hanSpaceRe.ReplaceAllString(a, "$1$2")
	}
	a = strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(a, "著"), "编著"))
	return a
}

func guessCategory(tags []string) string {
	t := strings.ToLower(strings.Join(tags, " "))
	t = strings.NewReplacer("nonfiction", " ", "non-fiction", " ").Replace(t)
	// 顺序有讲究：少儿、小说在前（Juvenile fiction 是少儿，Science fiction 是小说）
	rules := []struct{ cat, words string }{
		{"少儿", "儿童 少儿 童话 绘本 children juvenile fairy nursery"},
		{"小说", "小说 科幻 奇幻 武侠 仙侠 言情 悬疑 推理 玄幻 都市 演义 章回 话本 fiction novel novels romance mystery detective adventure fantasy"},
		{"计算机", "计算机 编程 程序 computer computers programming software python java 算法 algorithms"},
		{"历史", "历史 史部 正史 编年 纪传 history historical"},
		{"经济管理", "经济 管理 商业 金融 投资 business economics finance management"},
		{"哲学宗教", "哲学 宗教 佛教 佛经 道教 儒家 经部 子部 philosophy religion buddhism taoism christianity theology"},
		{"科学技术", "科学 科普 物理 化学 生物 数学 天文 医学 science sciences physics chemistry biology mathematics astronomy medicine technology"},
		{"社科", "社会 政治 心理 法律 social sociology politics psychology law"},
		{"文学", "文学 散文 诗歌 诗词 诗经 楚辞 戏曲 戏剧 杂剧 集部 literature poetry poems essays drama plays"},
		{"艺术", "艺术 美术 音乐 书法 绘画 摄影 art arts music painting photography"},
		{"生活", "生活 烹饪 美食 旅行 健康 cooking travel"},
		{"教育考试", "教育 考试 教材 education textbooks"},
	}
	have := map[string]bool{}
	for _, c := range categories() {
		have[c] = true
	}
	for _, r := range rules {
		if !have[r.cat] {
			continue // 自己改过分类列表、没有这一类
		}
		for _, w := range strings.Fields(r.words) {
			if w[0] < 0x80 {
				if regexp.MustCompile(`\b` + regexp.QuoteMeta(w) + `\b`).MatchString(t) {
					return r.cat
				}
			} else if strings.Contains(t, w) {
				return r.cat
			}
		}
	}
	return "其他"
}

// ---------- AI 识别 ----------

type AIConfig struct {
	Enabled bool   `json:"enabled"`
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
	Model   string `json:"model"`
}

func aiConfig() AIConfig {
	var c AIConfig
	json.Unmarshal([]byte(getSetting("ai", "{}")), &c)
	if c.BaseURL == "" {
		c.BaseURL = env("SHUKU_AI_BASE_URL", "")
	}
	if c.APIKey == "" {
		c.APIKey = env("SHUKU_AI_API_KEY", "")
	}
	if c.Model == "" {
		c.Model = env("SHUKU_AI_MODEL", "")
	}
	if getSetting("ai", "") == "" && c.BaseURL != "" && c.Model != "" {
		c.Enabled = true
	}
	return c
}

func aiReady() bool {
	c := aiConfig()
	return c.Enabled && c.BaseURL != "" && c.Model != ""
}

const aiPrompt = `你是图书管理员。根据电子书文件的信息，判断这本书的真实信息，恢复正式书名。
规则：
- title：正式中文书名（中文书或已有中文译本时用中文名），去掉网站名、"精校版"、格式、卷号以外的修饰；不要书名号。
- original_title：外文原著的原名，中文原创书留空。
- author：作者正式姓名。外国作者用通行中文译名；多人用 " / " 分隔；不要国籍前缀和"著"。
- translator：译者，没有留空。
- series / series_index：属于系列（含漫画、多卷本）时填写系列名和第几本，否则留空和 0。
- year：首次出版年份，不确定填 0。
- category：只能从这些里选一个：%s。
- tags：2~5 个中文题材标签。
- confidence：0~1，你对书名和作者都正确的把握。只看到乱码或信息太少时要低于 0.6。
只输出一个 JSON 对象，不要任何解释。字段：title, original_title, author, translator, series, series_index, year, category, tags, language, confidence。`

func aiIdentify(ctx context.Context, fileName string, m Meta, kind string, rule Identity) (Identity, error) {
	return aiIdentifyWith(ctx, aiConfig(), fileName, m, kind, rule)
}

// 用指定的配置识别（设置页“测试”用填着还没保存的配置，不改动正在用的设置）
func aiIdentifyWith(ctx context.Context, c AIConfig, fileName string, m Meta, kind string, rule Identity) (Identity, error) {
	if !c.Enabled || c.BaseURL == "" || c.Model == "" {
		return rule, errors.New("AI 未配置")
	}
	info := map[string]any{
		"文件名":  filepath.Base(fileName),
		"类型":   map[string]string{"book": "电子书", "comic": "漫画"}[kind],
		"内嵌书名": m.Title, "内嵌作者": m.Author, "内嵌译者": m.Translator,
		"内嵌系列": m.Series, "出版社": m.Publisher, "ISBN": m.ISBN, "标签": m.Tags,
		"规则解析": map[string]any{"书名": rule.Title, "作者": rule.Author, "系列": rule.Series, "序号": rule.SeriesIndex},
		"正文开头": m.Sample,
	}
	if d := []rune(m.Description); len(d) > 300 {
		info["简介"] = string(d[:300])
	} else {
		info["简介"] = m.Description
	}
	userMsg, _ := json.Marshal(info)
	body, _ := json.Marshal(map[string]any{
		"model":       c.Model,
		"temperature": 0.1,
		"messages": []map[string]string{
			{"role": "system", "content": fmt.Sprintf(aiPrompt, strings.Join(categories(), "、"))},
			{"role": "user", "content": string(userMsg)},
		},
	})
	url := strings.TrimRight(c.BaseURL, "/")
	if !strings.HasSuffix(url, "/chat/completions") {
		url += "/chat/completions"
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return rule, fmt.Errorf("AI 请求失败: %w", err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != 200 {
		return rule, fmt.Errorf("AI 返回 %d: %s", resp.StatusCode, truncate(string(rb), 200))
	}
	var cr struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(rb, &cr) != nil || len(cr.Choices) == 0 {
		return rule, errors.New("AI 返回格式不正确")
	}
	content := cr.Choices[0].Message.Content
	// 推理模型会带 <think>…</think>
	content = regexp.MustCompile(`(?s)<think>.*?</think>`).ReplaceAllString(content, "")
	i, j := strings.Index(content, "{"), strings.LastIndex(content, "}")
	if i < 0 || j <= i {
		return rule, errors.New("AI 没有返回 JSON")
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(content[i:j+1]), &raw); err != nil {
		return rule, errors.New("AI 返回的 JSON 无法解析")
	}
	id := rule
	str := func(k string) string {
		if v, ok := raw[k].(string); ok {
			return cleanSpace(v)
		}
		return ""
	}
	num := func(k string) float64 {
		switch v := raw[k].(type) {
		case float64:
			return v
		case string:
			f, _ := strconv.ParseFloat(v, 64)
			return f
		}
		return 0
	}
	if t := strings.Trim(str("title"), "《》"); t != "" {
		id.Title = t
	}
	id.OrigTitle = str("original_title")
	if a := normalizeAuthor(str("author")); a != "" {
		id.Author = a
	}
	id.Translator = str("translator")
	if s := str("series"); s != "" {
		id.Series, id.SeriesIndex = s, num("series_index")
	} else if kind != "comic" {
		id.Series, id.SeriesIndex = "", 0
	}
	if y := int(num("year")); y > 1000 && y < 2100 {
		id.Year = y
	}
	if cat := str("category"); cat != "" {
		for _, c := range categories() {
			if c == cat {
				id.Category = cat
			}
		}
	}
	if tags, ok := raw["tags"].([]any); ok {
		id.Tags = nil
		for _, t := range tags {
			if s, ok := t.(string); ok && strings.TrimSpace(s) != "" {
				id.Tags = append(id.Tags, strings.TrimSpace(s))
			}
		}
	}
	if l := str("language"); l != "" {
		id.Language = l
	}
	id.Confidence = num("confidence")
	if id.Confidence > 1 {
		id.Confidence /= 100
	}
	// 内嵌信息和 AI 一致时更可信
	if rule.Source == "内嵌信息" && normTitle(rule.Title) == normTitle(id.Title) && id.Confidence < 0.95 {
		id.Confidence = (id.Confidence + 1) / 2
	}
	id.Source = "AI · " + c.Model
	if kind == "comic" {
		id.Category = "漫画"
	}
	return id, nil
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// 用于比较的书名：去空格标点、转小写
func normTitle(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, s)
}

func autoThreshold() float64 {
	f, err := strconv.ParseFloat(getSetting("auto_threshold", "0.85"), 64)
	if err != nil || f <= 0 || f > 1 {
		return 0.85
	}
	return f
}
