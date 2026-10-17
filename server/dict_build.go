package main

// 本地字典：把开放的词典数据转成一个 SQLite 文件。发布时在 GitHub Actions 里做，打进运行环境镜像
// （shuku -build-dict 输出文件 -moe … -cedict … -ecdict …）。
//
//   中文释义：教育部《重编国语辞典修订本》（g0v/moedict-data 整理的 JSON，CC BY-ND 3.0 TW）。原文照存，
//             显示时按读者用的字形转成简体
//   大陆拼音、繁简对照、英文释义：CC-CEDICT（CC BY-SA 4.0）
//   英文单词：ECDICT（MIT），音标、中文释义、词形变化

import (
	"bufio"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// 字典文件的格式版本，查询代码按它判断能不能用
const dictSchema = 1

const dictDDL = `
CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE zh (word TEXT NOT NULL, simp TEXT NOT NULL, data TEXT NOT NULL);
CREATE TABLE cedict (trad TEXT NOT NULL, simp TEXT NOT NULL, py TEXT NOT NULL, en TEXT NOT NULL);
CREATE TABLE t2s (t TEXT PRIMARY KEY, s TEXT NOT NULL) WITHOUT ROWID;
CREATE TABLE en (word TEXT NOT NULL, ph TEXT NOT NULL, tr TEXT NOT NULL, def TEXT NOT NULL);
CREATE TABLE en_forms (form TEXT PRIMARY KEY, lemma TEXT NOT NULL, kind TEXT NOT NULL) WITHOUT ROWID;
`

const dictIndexes = `
CREATE INDEX zh_simp ON zh(simp);
CREATE INDEX zh_word ON zh(word);
CREATE INDEX cedict_simp ON cedict(simp);
CREATE INDEX cedict_trad ON cedict(trad);
CREATE INDEX en_word ON en(word COLLATE NOCASE);
`

// shuku -build-dict 输出文件 -moe 文件 -cedict 文件 -ecdict 文件（三个来源都可以缺）
func buildDictCLI(args []string) {
	fs := flag.NewFlagSet("build-dict", flag.ExitOnError)
	moe := fs.String("moe", "", "重编国语辞典修订本 dict-revised.json")
	cedict := fs.String("cedict", "", "CC-CEDICT cedict_ts.u8")
	ecdict := fs.String("ecdict", "", "ECDICT ecdict.csv")
	if len(args) < 1 {
		log.Fatal("用法：shuku -build-dict 输出文件 -moe dict-revised.json -cedict cedict.txt -ecdict ecdict.csv")
	}
	fs.Parse(args[1:])
	start := time.Now()
	n, err := buildDict(args[0], *moe, *cedict, *ecdict)
	if err != nil {
		log.Fatalf("生成字典失败: %v", err)
	}
	log.Printf("字典生成完毕（%s）：%v", time.Since(start).Round(time.Second), n)
}

type dictCounts map[string]int

func buildDict(out, moePath, cedictPath, ecdictPath string) (dictCounts, error) {
	n := dictCounts{}
	os.Remove(out)
	d, err := sql.Open("sqlite3", "file:"+out+"?_journal_mode=OFF&_synchronous=OFF")
	if err != nil {
		return n, err
	}
	defer d.Close()
	d.SetMaxOpenConns(1)
	if _, err := d.Exec(dictDDL); err != nil {
		return n, err
	}
	tx, err := d.Begin()
	if err != nil {
		return n, err
	}
	defer tx.Rollback()

	// 1. CC-CEDICT：大陆拼音、英文释义，顺便得到繁简对照
	words := map[string]string{} // 繁体词 → 简体词
	chars := map[rune]map[rune]int{}
	if cedictPath != "" {
		c, err := loadCEDICT(tx, cedictPath, words, chars)
		if err != nil {
			return n, fmt.Errorf("CC-CEDICT: %w", err)
		}
		n["cedict"] = c
	}
	t2sChars := map[rune]rune{}
	for t, m := range chars {
		best, bn := rune(0), 0
		for s, k := range m {
			if k > bn || (k == bn && s < best) {
				best, bn = s, k
			}
		}
		t2sChars[t] = best
	}
	for t, s := range t2sChars {
		if _, err := tx.Exec(`INSERT INTO t2s(t,s) VALUES(?,?)`, string(t), string(s)); err != nil {
			return n, err
		}
	}
	n["t2s"] = len(t2sChars)
	toSimp := func(s string) string {
		if w, ok := words[s]; ok {
			return w
		}
		rs := []rune(s)
		for i, r := range rs {
			if x, ok := t2sChars[r]; ok {
				rs[i] = x
			}
		}
		return toSimplified(string(rs))
	}

	// 2. 重编国语辞典修订本：中文释义
	if moePath != "" {
		c, err := loadMoE(tx, moePath, toSimp)
		if err != nil {
			return n, fmt.Errorf("国语辞典: %w", err)
		}
		n["zh"] = c
	}

	// 3. ECDICT：英文单词
	if ecdictPath != "" {
		c, forms, err := loadECDICT(tx, ecdictPath)
		if err != nil {
			return n, fmt.Errorf("ECDICT: %w", err)
		}
		n["en"], n["en_forms"] = c, forms
	}

	meta := map[string]string{"schema": strconv.Itoa(dictSchema), "built": time.Now().UTC().Format(time.RFC3339)}
	for k, v := range n {
		meta["count_"+k] = strconv.Itoa(v)
	}
	for k, v := range meta {
		if _, err := tx.Exec(`INSERT INTO meta(key,value) VALUES(?,?)`, k, v); err != nil {
			return n, err
		}
	}
	if err := tx.Commit(); err != nil {
		return n, err
	}
	if _, err := d.Exec(dictIndexes); err != nil {
		return n, err
	}
	if _, err := d.Exec(`VACUUM`); err != nil {
		return n, err
	}
	return n, nil
}

// ---------- CC-CEDICT ----------

// 傳統 传统 [chuan2 tong3] /tradition/traditional/
var cedictLine = regexp.MustCompile(`^(\S+) (\S+) \[([^\]]*)\] /(.*)/\s*$`)

func loadCEDICT(tx *sql.Tx, path string, words map[string]string, chars map[rune]map[rune]int) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	st, err := tx.Prepare(`INSERT INTO cedict(trad,simp,py,en) VALUES(?,?,?,?)`)
	if err != nil {
		return 0, err
	}
	defer st.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	n := 0
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#") {
			continue
		}
		m := cedictLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		trad, simp := m[1], m[2]
		en := strings.Join(strings.Split(m[4], "/"), "; ")
		if _, err := st.Exec(trad, simp, markPinyin(m[3]), en); err != nil {
			return n, err
		}
		n++
		if trad != simp {
			if _, ok := words[trad]; !ok {
				words[trad] = simp
			}
			tr, sr := []rune(trad), []rune(simp)
			if len(tr) == len(sr) {
				for i := range tr {
					if tr[i] != sr[i] {
						if chars[tr[i]] == nil {
							chars[tr[i]] = map[rune]int{}
						}
						chars[tr[i]][sr[i]]++
					}
				}
			}
		}
	}
	return n, sc.Err()
}

var toneTable = map[rune][4]rune{
	'a': {'ā', 'á', 'ǎ', 'à'}, 'e': {'ē', 'é', 'ě', 'è'}, 'i': {'ī', 'í', 'ǐ', 'ì'},
	'o': {'ō', 'ó', 'ǒ', 'ò'}, 'u': {'ū', 'ú', 'ǔ', 'ù'}, 'ü': {'ǖ', 'ǘ', 'ǚ', 'ǜ'},
}

// CC-CEDICT 的数字声调拼音（zhong1 guo2、lu:4）转成带声调符号的（zhōng guó、lǜ）
func markPinyin(s string) string {
	parts := strings.Fields(s)
	for i, p := range parts {
		parts[i] = markSyllable(p)
	}
	return strings.Join(parts, " ")
}

func markSyllable(p string) string {
	p = strings.NewReplacer("u:", "ü", "U:", "Ü").Replace(p)
	if p == "" {
		return p
	}
	tone := p[len(p)-1]
	if tone < '1' || tone > '5' {
		return p
	}
	body := p[:len(p)-1]
	if tone == '5' {
		return body
	}
	rs := []rune(body)
	lower := []rune(strings.ToLower(body))
	if len(lower) != len(rs) {
		return body
	}
	pos := -1
	for i, r := range lower { // a、e 优先
		if r == 'a' || r == 'e' {
			pos = i
			break
		}
	}
	if pos < 0 { // ou 标在 o 上
		for i := 0; i+1 < len(lower); i++ {
			if lower[i] == 'o' && lower[i+1] == 'u' {
				pos = i
				break
			}
		}
	}
	if pos < 0 { // 其余标在最后一个元音上
		for i := len(lower) - 1; i >= 0; i-- {
			if _, ok := toneTable[lower[i]]; ok {
				pos = i
				break
			}
		}
	}
	if pos < 0 { // m、n、ng、r 这类没有元音的
		return body
	}
	m := toneTable[lower[pos]][tone-'1']
	if unicode.IsUpper(rs[pos]) {
		m = unicode.ToUpper(m)
	}
	rs[pos] = m
	return string(rs)
}

// ---------- 重编国语辞典修订本 ----------

type moeEntry struct {
	Title      string `json:"title"`
	Heteronyms []struct {
		Pinyin      string `json:"pinyin"`
		Definitions []struct {
			Type     string   `json:"type"`
			Def      string   `json:"def"`
			Example  []string `json:"example"`
			Link     []string `json:"link"`
			Synonyms string   `json:"synonyms"`
			Antonyms string   `json:"antonyms"`
		} `json:"definitions"`
	} `json:"heteronyms"`
}

// 存进字典的一个读音：p 拼音，s 义项
type zhReading struct {
	P string    `json:"p"`
	S []zhSense `json:"s"`
}

// 一个义项：t 词性，d 释义，e 例句，l 参见，y 近义词，a 反义词
type zhSense struct {
	T string   `json:"t,omitempty"`
	D string   `json:"d"`
	E []string `json:"e,omitempty"`
	L []string `json:"l,omitempty"`
	Y string   `json:"y,omitempty"`
	A string   `json:"a,omitempty"`
}

// 辞典里 Unicode 没有的字写成 {[8ff0]} 这样的图片编号，换成方框
var moeImage = regexp.MustCompile(`\{\[[0-9a-f]{4,5}\]\}`)

func loadMoE(tx *sql.Tx, path string, toSimp func(string) string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	dec := json.NewDecoder(bufio.NewReaderSize(f, 1<<20))
	if t, err := dec.Token(); err != nil || t != json.Delim('[') {
		return 0, errors.New("不是 JSON 数组")
	}
	st, err := tx.Prepare(`INSERT INTO zh(word,simp,data) VALUES(?,?,?)`)
	if err != nil {
		return 0, err
	}
	defer st.Close()
	clean := func(s string) string { return strings.TrimSpace(moeImage.ReplaceAllString(s, "□")) }
	n := 0
	for dec.More() {
		var e moeEntry
		if err := dec.Decode(&e); err != nil {
			return n, err
		}
		if e.Title == "" || strings.Contains(e.Title, "{[") {
			continue
		}
		var rs []zhReading
		for _, h := range e.Heteronyms {
			r := zhReading{P: strings.TrimSpace(h.Pinyin)}
			for _, d := range h.Definitions {
				if strings.TrimSpace(d.Def) == "" {
					continue
				}
				s := zhSense{T: d.Type, D: clean(d.Def), Y: clean(d.Synonyms), A: clean(d.Antonyms)}
				for _, x := range d.Example {
					s.E = append(s.E, clean(x))
				}
				for _, x := range d.Link {
					s.L = append(s.L, clean(x))
				}
				r.S = append(r.S, s)
			}
			if len(r.S) > 0 {
				rs = append(rs, r)
			}
		}
		if len(rs) == 0 {
			continue
		}
		b, _ := json.Marshal(rs)
		if _, err := st.Exec(e.Title, toSimp(e.Title), string(b)); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// ---------- ECDICT ----------

var enWordRe = regexp.MustCompile(`^[A-Za-z][a-z'\-]*$`)

// ECDICT 的音标：' 是重音，ә 换成国际音标的 ə
var phoneticFix = strings.NewReplacer("'", "ˈ", ",", "ˌ", "ә", "ə", ":", "ː")

func loadECDICT(tx *sql.Tx, path string) (int, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	r := csv.NewReader(bufio.NewReaderSize(f, 1<<20))
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	r.ReuseRecord = true
	head, err := r.Read()
	if err != nil {
		return 0, 0, err
	}
	col := map[string]int{}
	for i, h := range head {
		col[strings.TrimSpace(h)] = i
	}
	for _, k := range []string{"word", "phonetic", "definition", "translation", "tag", "bnc", "frq", "exchange", "collins", "oxford"} {
		if _, ok := col[k]; !ok {
			return 0, 0, fmt.Errorf("缺少 %s 列", k)
		}
	}
	get := func(rec []string, k string) string {
		if i := col[k]; i < len(rec) {
			return strings.TrimSpace(rec[i])
		}
		return ""
	}
	st, err := tx.Prepare(`INSERT INTO en(word,ph,tr,def) VALUES(?,?,?,?)`)
	if err != nil {
		return 0, 0, err
	}
	defer st.Close()
	forms := map[string][2]string{} // 词形（小写）→ 原形、变化类型
	addForm := func(form, lemma, kind string) {
		form = strings.ToLower(strings.TrimSpace(form))
		if form == "" || strings.EqualFold(form, lemma) {
			return
		}
		if _, ok := forms[form]; !ok {
			forms[form] = [2]string{lemma, kind}
		}
	}
	lines := func(s string, max int) string {
		s = strings.ReplaceAll(s, `\n`, "\n")
		var out []string
		for _, l := range strings.Split(s, "\n") {
			if l = strings.TrimSpace(l); l != "" {
				out = append(out, l)
			}
			if max > 0 && len(out) >= max {
				break
			}
		}
		return strings.Join(out, "\n")
	}
	n := 0
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			continue
		}
		word, tr, ex := get(rec, "word"), get(rec, "translation"), get(rec, "exchange")
		if word == "" || tr == "" {
			continue
		}
		bnc, _ := strconv.Atoi(get(rec, "bnc"))
		frq, _ := strconv.Atoi(get(rec, "frq"))
		common := bnc > 0 || frq > 0 || get(rec, "tag") != "" || get(rec, "collins") != "" || get(rec, "oxford") != ""
		// 只是别的词的变形（went、apples）且不常用：记成词形，不单独收
		lemma, kind := "", ""
		for _, p := range strings.Split(ex, "/") {
			if k, v, ok := strings.Cut(p, ":"); ok {
				switch k {
				case "0":
					lemma = v
				case "1":
					kind = v
				}
			}
		}
		if lemma != "" && !common {
			addForm(word, lemma, kind)
			continue
		}
		single := enWordRe.MatchString(word) && utf8.RuneCountInString(word) <= 18
		if !common && !single {
			continue
		}
		if !common && strings.ContainsAny(word, "'-") && strings.Count(word, "-") > 1 {
			continue
		}
		if _, err := st.Exec(word, phoneticFix.Replace(get(rec, "phonetic")), lines(tr, 0), lines(get(rec, "definition"), 2)); err != nil {
			return n, 0, err
		}
		n++
		if lemma != "" {
			addForm(word, lemma, kind)
		}
		for _, p := range strings.Split(ex, "/") {
			if k, v, ok := strings.Cut(p, ":"); ok && strings.Contains("pdi3rts", k) && len(k) == 1 {
				addForm(v, word, k)
			}
		}
	}
	fst, err := tx.Prepare(`INSERT INTO en_forms(form,lemma,kind) VALUES(?,?,?)`)
	if err != nil {
		return n, 0, err
	}
	defer fst.Close()
	for form, v := range forms {
		if _, err := fst.Exec(form, v[0], v[1]); err != nil {
			return n, 0, err
		}
	}
	return n, len(forms), nil
}
