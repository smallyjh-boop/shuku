package main

// 翻译：阅读器里“翻译本页”，原文和译文对照着看。
// 两种引擎（设置 → 翻译里选）：
//   microsoft：微软翻译（和 Edge 浏览器自带的“翻译此页”同一个接口），免费、快，不用配置
//   ai：设置里配的 AI（OpenAI 兼容接口），更通顺，按字数花钱
// 翻过的段落存在 fulltext.db 的 tr_cache 里（能重建，不进备份），同一段不会再花一次钱。

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type TranslateConfig struct {
	Engine string `json:"engine"` // microsoft | ai
	To     string `json:"to"`     // 默认译成哪种语言（各人可以在阅读器里另选）
}

// 能译成的语言：微软的语言代码 → 名字（给 AI 的提示里用名字）
var translateLangs = map[string]string{
	"zh-Hans": "简体中文", "zh-Hant": "繁體中文", "en": "English", "ja": "日本語", "ko": "한국어",
	"fr": "Français", "de": "Deutsch", "es": "Español", "ru": "Русский", "th": "ภาษาไทย",
}

func translateConfig() TranslateConfig {
	c := TranslateConfig{Engine: "microsoft", To: "zh-Hans"}
	jsonUnmarshal(getSetting("translate", "{}"), &c)
	if c.Engine != "ai" {
		c.Engine = "microsoft"
	}
	if translateLangs[c.To] == "" {
		c.To = "zh-Hans"
	}
	return c
}

// 测试时换成模拟服务
var msTranslateAuth = "https://edge.microsoft.com/translate/auth"
var msTranslateBase = "https://api-edge.cognitive.microsofttranslator.com"

var translateClient = &http.Client{Timeout: 60 * time.Second}

// POST /api/translate {texts: [...], to: "zh-Hans", engine: ""}
// → {translations: [...], engine, to}，和 texts 一一对应
func handleTranslate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Texts  []string `json:"texts"`
		To     string   `json:"to"`
		From   string   `json:"from"`
		Engine string   `json:"engine"`
	}
	if readJSON(r, &req) != nil || len(req.Texts) == 0 {
		errJSON(w, 400, "没有要翻译的内容")
		return
	}
	if len(req.Texts) > 200 {
		errJSON(w, 400, "一次最多翻译 200 段")
		return
	}
	total := 0
	for i, t := range req.Texts {
		if utf8.RuneCountInString(t) > 6000 {
			req.Texts[i] = string([]rune(t)[:6000])
		}
		total += len(req.Texts[i])
	}
	if total > 120000 {
		errJSON(w, 400, "一次要翻译的文字太多")
		return
	}
	cfg := translateConfig()
	engine := cfg.Engine
	if req.Engine == "microsoft" || req.Engine == "ai" {
		engine = req.Engine
	}
	to := cfg.To
	if translateLangs[req.To] != "" {
		to = req.To
	}
	if engine == "ai" && !aiReady() {
		engine = "microsoft" // AI 还没配置：先用微软翻译
	}
	out, used, err := translateTexts(r.Context(), req.Texts, to, req.From, engine)
	if err != nil {
		errJSON(w, 502, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"translations": out, "engine": used, "to": to})
}

// 先查缓存，没翻过的再交给引擎；微软翻译失败、AI 配好了时换 AI 再试（反过来也一样）
func translateTexts(ctx context.Context, texts []string, to, from, engine string) ([]string, string, error) {
	out := make([]string, len(texts))
	var miss []int
	for i, t := range texts {
		if strings.TrimSpace(t) == "" {
			out[i] = t
			continue
		}
		if c, ok := trCacheGet(engine, to, t); ok {
			out[i] = c
			continue
		}
		miss = append(miss, i)
	}
	if len(miss) == 0 {
		return out, engine, nil
	}
	todo := make([]string, len(miss))
	for k, i := range miss {
		todo[k] = texts[i]
	}
	res, err := translateWith(ctx, engine, todo, to, from)
	if err != nil {
		other := map[string]string{"microsoft": "ai", "ai": "microsoft"}[engine]
		if other == "ai" && !aiReady() {
			return nil, engine, err
		}
		res2, err2 := translateWith(ctx, other, todo, to, from)
		if err2 != nil {
			return nil, engine, err // 报第一个引擎的错
		}
		res, engine = res2, other
	}
	for k, i := range miss {
		out[i] = res[k]
		trCachePut(engine, to, texts[i], res[k])
	}
	return out, engine, nil
}

func translateWith(ctx context.Context, engine string, texts []string, to, from string) ([]string, error) {
	// 分批：微软一次最多 50 段、1 万字；AI 一次 16 段、3 千字（太长容易漏段、超时）
	maxN, maxChars := 50, 10000
	if engine == "ai" {
		maxN, maxChars = 16, 3000
	}
	var out []string
	for start := 0; start < len(texts); {
		end, chars := start, 0
		for end < len(texts) && end-start < maxN && (end == start || chars+utf8.RuneCountInString(texts[end]) <= maxChars) {
			chars += utf8.RuneCountInString(texts[end])
			end++
		}
		var res []string
		var err error
		if engine == "ai" {
			res, err = aiTranslate(ctx, texts[start:end], to)
		} else {
			res, err = msTranslate(ctx, texts[start:end], to, from)
		}
		if err != nil {
			return nil, err
		}
		out = append(out, res...)
		start = end
	}
	return out, nil
}

// ---------- 缓存（fulltext.db 里的 tr_cache） ----------

func trKey(engine, to, text string) string {
	model := ""
	if engine == "ai" {
		model = aiConfig().Model
	}
	sum := sha1.Sum([]byte(engine + "|" + model + "|" + to + "|" + text))
	return hex.EncodeToString(sum[:])
}

func trCacheGet(engine, to, text string) (string, bool) {
	var s string
	if db.QueryRow(`SELECT text FROM tr_cache WHERE key=?`, trKey(engine, to, text)).Scan(&s) != nil {
		return "", false
	}
	return s, true
}

func trCachePut(engine, to, text, tr string) {
	db.Exec(`INSERT INTO tr_cache(key,text,created_at) VALUES(?,?,?) ON CONFLICT(key) DO UPDATE SET text=excluded.text,created_at=excluded.created_at`,
		trKey(engine, to, text), tr, now())
}

// 只留最近一年、最多 20 万段
func pruneTranslations() {
	db.Exec(`DELETE FROM tr_cache WHERE created_at<?`, now()-365*24*3600)
	db.Exec(`DELETE FROM tr_cache WHERE key IN (SELECT key FROM tr_cache ORDER BY created_at DESC LIMIT -1 OFFSET 200000)`)
}

// ---------- 微软翻译 ----------

var msToken struct {
	sync.Mutex
	tok string
	exp time.Time
}

func msAuthToken(ctx context.Context, fresh bool) (string, error) {
	msToken.Lock()
	defer msToken.Unlock()
	if !fresh && msToken.tok != "" && time.Until(msToken.exp) > time.Minute {
		return msToken.tok, nil
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", msTranslateAuth, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36 Edg/130.0.0.0")
	resp, err := translateClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("连不上微软翻译：%s", simplifyNetErr(err))
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	tok := strings.TrimSpace(string(b))
	if resp.StatusCode != 200 || tok == "" || strings.ContainsAny(tok, " <{") {
		return "", fmt.Errorf("微软翻译暂时不能用（%d）", resp.StatusCode)
	}
	msToken.tok, msToken.exp = tok, jwtExpiry(tok)
	return tok, nil
}

// 令牌是 JWT，过期时间在中间那段的 exp 里；看不懂就按 8 分钟算
func jwtExpiry(tok string) time.Time {
	parts := strings.Split(tok, ".")
	if len(parts) == 3 {
		if b, err := base64.RawURLEncoding.DecodeString(parts[1]); err == nil {
			var c struct {
				Exp int64 `json:"exp"`
			}
			if json.Unmarshal(b, &c) == nil && c.Exp > 0 {
				return time.Unix(c.Exp, 0)
			}
		}
	}
	return time.Now().Add(8 * time.Minute)
}

func msTranslate(ctx context.Context, texts []string, to, from string) ([]string, error) {
	body := make([]map[string]string, len(texts))
	for i, t := range texts {
		body[i] = map[string]string{"Text": t}
	}
	data, _ := json.Marshal(body)
	q := url.Values{"api-version": {"3.0"}, "to": {to}}
	if from != "" {
		q.Set("from", from)
	}
	for attempt := 0; attempt < 2; attempt++ {
		tok, err := msAuthToken(ctx, attempt > 0)
		if err != nil {
			return nil, err
		}
		req, _ := http.NewRequestWithContext(ctx, "POST", msTranslateBase+"/translate?"+q.Encode(), bytes.NewReader(data))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		resp, err := translateClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("连不上微软翻译：%s", simplifyNetErr(err))
		}
		rb, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		resp.Body.Close()
		if resp.StatusCode == 401 && attempt == 0 {
			continue // 令牌过期了，换一个再试
		}
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("微软翻译出错（%d）：%s", resp.StatusCode, truncate(string(rb), 120))
		}
		var res []struct {
			Translations []struct {
				Text string `json:"text"`
			} `json:"translations"`
		}
		if json.Unmarshal(rb, &res) != nil || len(res) != len(texts) {
			return nil, errors.New("微软翻译返回的格式不对")
		}
		out := make([]string, len(texts))
		for i, x := range res {
			if len(x.Translations) > 0 {
				out[i] = x.Translations[0].Text
			}
		}
		return out, nil
	}
	return nil, errors.New("微软翻译暂时不能用")
}

// ---------- AI 翻译 ----------

const aiTranslatePrompt = `你是专业的书籍翻译。用户发来一个 JSON 字符串数组，每一项是书里的一段话。把每一项翻译成%s：
- 数组长度和顺序不变，一项对应一项，不合并、不拆分、不省略；
- 译文自然通顺，保留原文的语气和段落里的换行；人名、地名用通行译名；
- 本来就是%s的项原样返回。
只输出一个 JSON 字符串数组，不要任何解释。`

func aiTranslate(ctx context.Context, texts []string, to string) ([]string, error) {
	c := aiConfig()
	if !c.Enabled || c.BaseURL == "" || c.Model == "" {
		return nil, errors.New("AI 还没配置（设置 → 识别与整理）")
	}
	name := translateLangs[to]
	in, _ := json.Marshal(texts)
	actx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	content, err := aiChat(actx, fmt.Sprintf(aiTranslatePrompt, name, name), string(in), 0.2)
	cancel()
	if err != nil {
		return nil, err
	}
	i, j := strings.Index(content, "["), strings.LastIndex(content, "]")
	var out []string
	if i >= 0 && j > i {
		json.Unmarshal([]byte(content[i:j+1]), &out)
	}
	if len(out) == len(texts) {
		return out, nil
	}
	// 对不上（漏了段、合并了段）：拆成两半再试，一段一段地兜底
	if len(texts) > 1 {
		half := len(texts) / 2
		a, err := aiTranslate(ctx, texts[:half], to)
		if err != nil {
			return nil, err
		}
		b, err := aiTranslate(ctx, texts[half:], to)
		if err != nil {
			return nil, err
		}
		return append(a, b...), nil
	}
	if len(out) == 0 && strings.TrimSpace(content) != "" && i < 0 {
		return []string{strings.TrimSpace(content)}, nil // 只有一段时，模型可能直接回了译文
	}
	if len(out) > 0 {
		return []string{strings.Join(out, "\n")}, nil
	}
	return nil, errors.New("AI 返回的译文格式不对")
}
