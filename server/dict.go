package main

// 选词查字典：阅读时选中字词，先查本地字典（中文、英文），再按需要让 AI 结合上下文解释；
// 读音用微软自然语音（按语言挑音色），什么语言都能读。本地字典的数据见 dict_build.go。

import (
	"bytes"
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

func dictPath() string { return env("SHUKU_DICT", "/app/dict/dict.db") }

var dictState struct {
	sync.Mutex
	db   *sql.DB
	path string
	mod  time.Time
	t2s  map[rune]rune
	meta map[string]string
}

// 打开本地字典（只读，镜像里的文件不会变）。没有字典或格式对不上时返回 nil
func openDict() *sql.DB {
	p := dictPath()
	st, err := os.Stat(p)
	dictState.Lock()
	defer dictState.Unlock()
	if err == nil && dictState.db != nil && dictState.path == p && st.ModTime().Equal(dictState.mod) {
		return dictState.db
	}
	if dictState.db != nil {
		dictState.db.Close()
		dictState.db = nil
	}
	if err != nil || st.Size() == 0 {
		return nil
	}
	d, err := sql.Open("sqlite3", "file:"+p+"?mode=ro&immutable=1")
	if err != nil {
		return nil
	}
	meta := map[string]string{}
	rows, err := d.Query(`SELECT key, value FROM meta`)
	if err != nil {
		d.Close()
		return nil
	}
	for rows.Next() {
		var k, v string
		rows.Scan(&k, &v)
		meta[k] = v
	}
	rows.Close()
	if meta["schema"] != strconv.Itoa(dictSchema) {
		d.Close()
		return nil
	}
	t2s := map[rune]rune{}
	if rows, err := d.Query(`SELECT t, s FROM t2s`); err == nil {
		for rows.Next() {
			var t, s string
			rows.Scan(&t, &s)
			if tr, sr := []rune(t), []rune(s); len(tr) == 1 && len(sr) == 1 {
				t2s[tr[0]] = sr[0]
			}
		}
		rows.Close()
	}
	dictState.db, dictState.path, dictState.mod, dictState.t2s, dictState.meta = d, p, st.ModTime(), t2s, meta
	return d
}

func dictInstalled() bool { return openDict() != nil }

// 字典里的繁体转简体（字典自带的繁简对照 + 常用字表）
func dictToSimp(s string) string {
	dictState.Lock()
	m := dictState.t2s
	dictState.Unlock()
	rs := []rune(s)
	for i, r := range rs {
		if x, ok := m[r]; ok {
			rs[i] = x
		}
	}
	return toSimplified(string(rs))
}

// 繁体转简体的字表给阅读器用（繁体书转成简体读）：两个等长的字符串，第 i 个繁体字对应第 i 个简体字。
// 有本地字典时用字典里 CC-CEDICT 的繁简对照（约三千字），没有时用内置的常用字表
func handleDictT2S(w http.ResponseWriter, r *http.Request) {
	var t, s []rune
	if openDict() != nil {
		dictState.Lock()
		m := dictState.t2s
		dictState.Unlock()
		keys := make([]rune, 0, len(m))
		for k, v := range m {
			if k != v {
				keys = append(keys, k)
			}
		}
		sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
		for _, k := range keys {
			t = append(t, k)
			s = append(s, m[k])
		}
	}
	// 内置表补上字典里没有的
	have := map[rune]bool{}
	for _, k := range t {
		have[k] = true
	}
	bt, bs := []rune(tradChars), []rune(simpChars)
	for i := range bt {
		if !have[bt[i]] && bt[i] != bs[i] {
			t = append(t, bt[i])
			s = append(s, bs[i])
			have[bt[i]] = true
		}
	}
	w.Header().Set("Cache-Control", "private, max-age=86400")
	writeJSON(w, 200, map[string]any{"t": string(t), "s": string(s), "source": map[bool]string{true: "dict", false: "builtin"}[dictInstalled()]})
}

// ---------- 语言 ----------

// 按文字判断语言；书的语言（EPUB 里写的）只用来区分同一种文字的语言，比如拉丁字母是英文还是法文
func detectLang(text, hint string) string {
	hint = strings.ToLower(strings.TrimSpace(hint))
	var han, kana, hangul, thai, cyr, arab, heb, deva, greek, latin int
	for _, r := range text {
		switch {
		case unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r):
			kana++
		case unicode.Is(unicode.Han, r):
			han++
		case unicode.Is(unicode.Hangul, r):
			hangul++
		case unicode.Is(unicode.Thai, r):
			thai++
		case unicode.Is(unicode.Cyrillic, r):
			cyr++
		case unicode.Is(unicode.Arabic, r):
			arab++
		case unicode.Is(unicode.Hebrew, r):
			heb++
		case unicode.Is(unicode.Devanagari, r):
			deva++
		case unicode.Is(unicode.Greek, r):
			greek++
		case unicode.Is(unicode.Latin, r):
			latin++
		}
	}
	base := strings.SplitN(strings.ReplaceAll(hint, "_", "-"), "-", 2)[0]
	switch {
	case kana > 0:
		return "ja"
	case hangul > 0:
		return "ko"
	case thai > 0:
		return "th"
	case han > 0:
		switch {
		case base == "ja" || base == "ko":
			return base
		case strings.Contains(hint, "tw") || strings.Contains(hint, "hk") || strings.Contains(hint, "hant"):
			return "zh-TW"
		}
		return "zh"
	case cyr > 0:
		if base == "uk" || base == "bg" || base == "sr" {
			return base
		}
		return "ru"
	case arab > 0:
		if base == "fa" || base == "ur" {
			return base
		}
		return "ar"
	case heb > 0:
		return "he"
	case deva > 0:
		return "hi"
	case greek > 0:
		return "el"
	case latin > 0:
		if strings.ContainsAny(strings.ToLower(text), "ăđơưạảấầẩẫậắằẳẵặẹẻẽếềểễệỉịọỏốồổỗộớờởỡợụủứừửữựỳỵỷỹ") {
			return "vi"
		}
		if base != "" && base != "zh" && base != "ja" && base != "ko" && base != "und" {
			return base
		}
		return "en"
	}
	if base != "" {
		return base
	}
	return "en"
}

// 发音用的微软自然语音；没有列出的语言用多语言音色
var dictVoices = map[string]string{
	"zh": "zh-CN-XiaoxiaoNeural", "zh-TW": "zh-TW-HsiaoChenNeural", "en": "en-US-AriaNeural", "ja": "ja-JP-NanamiNeural",
	"ko": "ko-KR-SunHiNeural", "th": "th-TH-PremwadeeNeural", "vi": "vi-VN-HoaiMyNeural", "fr": "fr-FR-DeniseNeural",
	"de": "de-DE-KatjaNeural", "es": "es-ES-ElviraNeural", "it": "it-IT-ElsaNeural", "pt": "pt-BR-FranciscaNeural",
	"ru": "ru-RU-SvetlanaNeural", "uk": "uk-UA-PolinaNeural", "ar": "ar-SA-ZariyahNeural", "fa": "fa-IR-DilaraNeural",
	"he": "he-IL-HilaNeural", "hi": "hi-IN-SwaraNeural", "el": "el-GR-AthinaNeural", "id": "id-ID-GadisNeural",
	"ms": "ms-MY-YasminNeural", "tr": "tr-TR-EmelNeural", "pl": "pl-PL-ZofiaNeural", "nl": "nl-NL-ColetteNeural",
	"sv": "sv-SE-SofieNeural", "da": "da-DK-ChristelNeural", "nb": "nb-NO-PernilleNeural", "no": "nb-NO-PernilleNeural",
	"fi": "fi-FI-NooraNeural", "cs": "cs-CZ-VlastaNeural", "hu": "hu-HU-NoemiNeural", "ro": "ro-RO-AlinaNeural",
	"fil": "fil-PH-BlessicaNeural", "tl": "fil-PH-BlessicaNeural", "bg": "bg-BG-KalinaNeural", "sr": "sr-RS-SophieNeural",
	"ur": "ur-PK-UzmaNeural", "km": "km-KH-SreymomNeural", "lo": "lo-LA-KeomanyNeural", "my": "my-MM-NilarNeural",
}

func dictVoice(lang string) string {
	if strings.HasPrefix(lang, "zh") && lang != "zh-TW" {
		// 中文用设置里选的普通话音色
		if v := ttsConfig().Voice; strings.HasPrefix(v, "zh-CN") {
			return v
		}
	}
	if v, ok := dictVoices[lang]; ok {
		return v
	}
	if v, ok := dictVoices[strings.SplitN(lang, "-", 2)[0]]; ok {
		return v
	}
	return "en-US-AvaMultilingualNeural"
}

// ---------- 查字典 ----------

// 选中文字两头的标点、空格去掉
func trimSelection(s string) string {
	return strings.TrimFunc(strings.TrimSpace(s), func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r)
	})
}

func normPinyin(p string) string {
	p = strings.ToLower(p)
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) {
			return r
		}
		return -1
	}, p)
}

type cedictRow struct{ trad, simp, py, en string }

func cedictRows(d *sql.DB, w string) []cedictRow {
	rows, err := d.Query(`SELECT trad, simp, py, en FROM cedict WHERE simp=? OR trad=? LIMIT 12`, w, w)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []cedictRow
	for rows.Next() {
		var c cedictRow
		rows.Scan(&c.trad, &c.simp, &c.py, &c.en)
		out = append(out, c)
	}
	return out
}

// 一个中文词条：w 是要查的词（简体或繁体都行），show 把字典原文转成读者用的字形
func zhEntry(d *sql.DB, w string, show func(string) string) map[string]any {
	ced := cedictRows(d, w)
	type moeRow struct{ word, data string }
	var moe []moeRow
	if rows, err := d.Query(`SELECT word, data FROM zh WHERE simp=? OR word=? LIMIT 4`, w, w); err == nil {
		for rows.Next() {
			var m moeRow
			rows.Scan(&m.word, &m.data)
			moe = append(moe, m)
		}
		rows.Close()
	}
	if len(ced) == 0 && len(moe) == 0 {
		return nil
	}
	// 大陆读音（CC-CEDICT）：小写的优先（大写多是人名地名）
	var pys []string
	seen := map[string]bool{}
	mainland := map[string]bool{}
	for _, lower := range []bool{true, false} {
		for _, c := range ced {
			isLower := c.py == strings.ToLower(c.py)
			if isLower != lower || seen[c.py] {
				continue
			}
			seen[c.py] = true
			pys = append(pys, c.py)
			mainland[normPinyin(c.py)] = true
		}
	}
	e := map[string]any{"word": show(w)}
	var readings []map[string]any
	for _, m := range moe {
		var rs []zhReading
		if json.Unmarshal([]byte(m.data), &rs) != nil {
			continue
		}
		for _, r := range rs {
			var senses []map[string]any
			for _, s := range r.S {
				x := map[string]any{"def": show(s.D)}
				if s.T != "" {
					x["pos"] = show(s.T)
				}
				if len(s.E) > 0 {
					var ex []string
					for _, v := range s.E {
						ex = append(ex, show(v))
					}
					x["ex"] = ex
				}
				if len(s.L) > 0 {
					var see []string
					for _, v := range s.L {
						see = append(see, show(v))
					}
					x["see"] = see
				}
				if s.Y != "" {
					x["syn"] = show(strings.ReplaceAll(s.Y, ",", "、"))
				}
				if s.A != "" {
					x["ant"] = show(strings.ReplaceAll(s.A, ",", "、"))
				}
				senses = append(senses, x)
			}
			// 和大陆读音对不上的标出来（台湾的读法，比如“垃圾 lè sè”）
			tw := len(mainland) > 0 && !mainland[normPinyin(r.P)]
			readings = append(readings, map[string]any{"py": r.P, "tw": tw, "senses": senses})
		}
	}
	if len(pys) == 0 {
		for _, r := range readings {
			if p, _ := r["py"].(string); p != "" && !seen[p] {
				seen[p] = true
				pys = append(pys, p)
			}
		}
	}
	e["pinyin"] = pys
	if len(readings) > 0 {
		e["readings"] = readings
	}
	// 国语辞典里没有的（新词、外来词）给出 CC-CEDICT 的英文释义
	var en []string
	for _, c := range ced {
		en = append(en, c.en)
		if len(en) >= 3 {
			break
		}
	}
	if len(en) > 0 {
		e["en"] = strings.Join(en, " | ")
	}
	return e
}

func lookupZh(d *sql.DB, q string) (words, chars []map[string]any) {
	rs := []rune(q)
	// 读者看的是繁体书就显示繁体，否则转成简体
	trad := dictToSimp(q) != q
	show := func(s string) string {
		if trad {
			return s
		}
		return dictToSimp(s)
	}
	// 先查整个，查不到就找里面最长的词
	if len(rs) <= 12 {
		if e := zhEntry(d, q, show); e != nil {
			words = append(words, e)
		}
	}
	if len(words) == 0 {
	outer:
		for l := min(len(rs)-1, 8); l >= 2; l-- {
			for i := 0; i+l <= len(rs); i++ {
				if e := zhEntry(d, string(rs[i:i+l]), show); e != nil {
					words = append(words, e)
					break outer
				}
			}
		}
	}
	// 再列出每个字（选的是一个字时，上面就是这个字）
	if len(rs) > 1 {
		seen := map[rune]bool{}
		for _, r := range rs {
			if !unicode.Is(unicode.Han, r) || seen[r] || len(chars) >= 8 {
				continue
			}
			seen[r] = true
			e := zhEntry(d, string(r), show)
			if e == nil {
				continue
			}
			c := map[string]any{"ch": show(string(r)), "pinyin": e["pinyin"]}
			if rd, ok := e["readings"].([]map[string]any); ok && len(rd) > 0 {
				if ss, ok := rd[0]["senses"].([]map[string]any); ok && len(ss) > 0 {
					c["def"] = ss[0]["def"]
				}
			} else if en, ok := e["en"].(string); ok {
				c["def"] = en
			}
			chars = append(chars, c)
		}
	}
	return
}

var formNames = map[string]string{"p": "过去式", "d": "过去分词", "i": "现在分词", "3": "第三人称单数", "r": "比较级", "t": "最高级", "s": "复数"}

func enEntry(d *sql.DB, w string) map[string]any {
	rows, err := d.Query(`SELECT word, ph, tr, def FROM en WHERE word=? COLLATE NOCASE LIMIT 4`, w)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var best map[string]any
	for rows.Next() {
		var word, ph, tr, def string
		rows.Scan(&word, &ph, &tr, &def)
		e := map[string]any{"word": word, "ph": ph, "tr": strings.Split(tr, "\n")}
		if def != "" {
			e["def"] = strings.Split(def, "\n")
		}
		// 大小写完全一样的优先（Hague 和 hague）
		if best == nil || word == w {
			best = e
		}
	}
	return best
}

func lookupEn(d *sql.DB, q string) (words []map[string]any) {
	w := strings.Trim(q, "'’-")
	e := enEntry(d, w)
	var lemma, kind string
	d.QueryRow(`SELECT lemma, kind FROM en_forms WHERE form=?`, strings.ToLower(w)).Scan(&lemma, &kind)
	if e == nil && lemma == "" && strings.Contains(w, " ") {
		return nil
	}
	if e == nil && lemma == "" {
		// 简单去掉常见词尾再试：'s、s、es、ed、ing、ly
		for _, suf := range []string{"'s", "’s", "ies", "es", "s", "ed", "ing", "ly"} {
			if base := strings.TrimSuffix(strings.ToLower(w), suf); base != strings.ToLower(w) && len(base) > 2 {
				if suf == "ies" {
					base += "y"
				}
				if x := enEntry(d, base); x != nil {
					return []map[string]any{x}
				}
			}
		}
		return nil
	}
	if e != nil {
		words = append(words, e)
	}
	if lemma != "" && !strings.EqualFold(lemma, w) {
		if le := enEntry(d, lemma); le != nil {
			var names []string
			for _, k := range strings.Split(kind, "") {
				if n := formNames[k]; n != "" {
					names = append(names, n)
				}
			}
			if len(names) > 0 {
				le["note"] = fmt.Sprintf("%s 是 %s 的%s", w, lemma, strings.Join(names, "、"))
			} else {
				le["note"] = fmt.Sprintf("%s 是 %s 的变形", w, lemma)
			}
			words = append(words, le)
		}
	}
	return words
}

// GET /api/dict?q=选中的文字&lang=书的语言
func handleDict(w http.ResponseWriter, r *http.Request) {
	q := trimSelection(r.URL.Query().Get("q"))
	if utf8.RuneCountInString(q) > 300 {
		q = string([]rune(q)[:300])
	}
	lang := detectLang(q, r.URL.Query().Get("lang"))
	out := map[string]any{"q": q, "lang": lang, "ai": aiReady(), "installed": false, "words": []any{}}
	d := openDict()
	if d != nil {
		out["installed"] = true
	}
	if d == nil || q == "" {
		writeJSON(w, 200, out)
		return
	}
	switch {
	case strings.HasPrefix(lang, "zh") && utf8.RuneCountInString(q) <= 20:
		words, chars := lookupZh(d, q)
		if len(words) > 0 {
			out["words"] = words
		}
		if len(chars) > 0 {
			out["chars"] = chars
		}
		out["source"] = "释义：教育部《重编国语辞典修订本》（CC BY-ND 3.0 TW）；拼音：CC-CEDICT"
	case lang == "en" && utf8.RuneCountInString(q) <= 40 && strings.Count(q, " ") <= 3:
		if words := lookupEn(d, q); len(words) > 0 {
			out["words"] = words
		}
		out["source"] = "ECDICT"
	}
	writeJSON(w, 200, out)
}

// ---------- AI 解释 ----------

// 调用设置里的 AI（OpenAI 兼容接口），返回去掉思考过程的回答
func aiChat(ctx context.Context, system, user string, temperature float64) (string, error) {
	c := aiConfig()
	if !c.Enabled || c.BaseURL == "" || c.Model == "" {
		return "", errors.New("AI 未配置")
	}
	body, _ := json.Marshal(map[string]any{
		"model":       c.Model,
		"temperature": temperature,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
	})
	url := strings.TrimRight(c.BaseURL, "/")
	if !strings.HasSuffix(url, "/chat/completions") {
		url += "/chat/completions"
	}
	req, _ := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("AI 请求失败：%s", simplifyNetErr(err))
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("AI 返回 %d：%s", resp.StatusCode, truncate(string(rb), 200))
	}
	var cr struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(rb, &cr) != nil || len(cr.Choices) == 0 {
		return "", errors.New("AI 返回格式不正确")
	}
	content := thinkRe.ReplaceAllString(cr.Choices[0].Message.Content, "")
	return strings.TrimSpace(content), nil
}

var thinkRe = regexp.MustCompile(`(?s)<think>.*?</think>`)

const dictSystemPrompt = `你是读书助手。读者在看书时选中了一段不懂的文字，请结合上下文帮他弄懂。
用简体中文回答，直接说内容，不要客套，不要重复问题，不用 Markdown 标题。`

var langNames = map[string]string{"zh": "中文", "zh-TW": "中文（繁体）", "en": "英文", "ja": "日文", "ko": "韩文", "th": "泰文", "vi": "越南文",
	"fr": "法文", "de": "德文", "es": "西班牙文", "it": "意大利文", "pt": "葡萄牙文", "ru": "俄文", "ar": "阿拉伯文"}

func dictPrompt(text, context, title, author, lang string) string {
	var b strings.Builder
	if title != "" {
		fmt.Fprintf(&b, "书：《%s》", title)
		if author != "" {
			fmt.Fprintf(&b, "，%s", author)
		}
		b.WriteString("\n")
	}
	if context != "" && context != text {
		fmt.Fprintf(&b, "上下文：%s\n", context)
	}
	name := langNames[lang]
	if name == "" {
		name = lang
	}
	fmt.Fprintf(&b, "选中的文字（%s）：%s\n\n", name, text)
	short := utf8.RuneCountInString(text) <= 12 && strings.Count(text, " ") <= 3
	switch {
	case short && strings.HasPrefix(lang, "zh"):
		b.WriteString("请说明：1. 读音（拼音）；2. 在这段话里是什么意思；3. 如果是成语、典故、方言、网络用语或专有名词，简单说明来历。总共不超过 120 字。")
	case short:
		b.WriteString("请说明：1. 读音（英文给音标，日文给假名和罗马音，其他语言给罗马字拼读）；2. 原形和词性；3. 在这段话里是什么意思（中文）；4. 常见搭配或例子。总共不超过 150 字。")
	case strings.HasPrefix(lang, "zh"):
		b.WriteString("请用现代白话解释这段话的意思，点出其中难懂的字词。不超过 200 字。")
	default:
		b.WriteString("请先把这段话翻译成通顺的简体中文，再简单解释其中难懂的词或语法。不超过 250 字。")
	}
	return b.String()
}

// POST /api/dict/explain {text, context, title, author, lang}
func handleDictExplain(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text    string `json:"text"`
		Context string `json:"context"`
		Title   string `json:"title"`
		Author  string `json:"author"`
		Lang    string `json:"lang"`
	}
	if readJSON(r, &req) != nil {
		errJSON(w, 400, "格式不正确")
		return
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		errJSON(w, 400, "没有选中文字")
		return
	}
	cut := func(s string, n int) string {
		s = strings.Join(strings.Fields(s), " ")
		if rs := []rune(s); len(rs) > n {
			return string(rs[:n]) + "…"
		}
		return s
	}
	text, ctxText := cut(text, 300), cut(req.Context, 400)
	if !aiReady() {
		writeJSON(w, 200, map[string]any{"ok": false, "reason": "还没配置 AI：在“设置 → 识别与整理”里填好 AI 服务就能用"})
		return
	}
	lang := detectLang(text, req.Lang)
	sum := sha1.Sum([]byte(aiConfig().Model + "|" + lang + "|" + text + "|" + ctxText))
	key := hex.EncodeToString(sum[:])
	var answer string
	if db.QueryRow(`SELECT answer FROM dict_ai WHERE key=?`, key).Scan(&answer) == nil {
		writeJSON(w, 200, map[string]any{"ok": true, "answer": answer, "cached": true})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	answer, err := aiChat(ctx, dictSystemPrompt, dictPrompt(text, ctxText, req.Title, req.Author, lang), 0.3)
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "reason": err.Error()})
		return
	}
	if answer == "" {
		writeJSON(w, 200, map[string]any{"ok": false, "reason": "AI 没有回答"})
		return
	}
	db.Exec(`INSERT OR REPLACE INTO dict_ai(key, answer, created_at) VALUES(?,?,?)`, key, answer, now())
	writeJSON(w, 200, map[string]any{"ok": true, "answer": answer})
}

// ---------- 发音 ----------

// POST /api/dict/speak {text, lang}：按语言挑音色读出来；设置里用的是自定义语音接口时用它
func handleDictSpeak(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text string `json:"text"`
		Lang string `json:"lang"`
	}
	if readJSON(r, &req) != nil {
		errJSON(w, 400, "格式不正确")
		return
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		errJSON(w, 400, "没有要读的文字")
		return
	}
	if rs := []rune(text); len(rs) > 300 {
		text = string(rs[:300])
	}
	lang := detectLang(text, req.Lang)
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	var b []byte
	var err error
	if ttsConfig().Engine == "openai" {
		b, err = synthesize(ctx, text, "", "", 0.9)
	} else {
		b, err = synthesizeWith(ctx, "edge", text, dictVoice(lang), 0.9)
	}
	if err != nil {
		errJSON(w, 502, "发音失败："+simplifyNetErr(err))
		return
	}
	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Write(b)
}
