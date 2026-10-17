package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 用几条样本数据生成一个小字典，各种来源的格式都和真的一样
func makeTestDict(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	moe := `[
 {"title":"覬覦","heteronyms":[{"bopomofo":"ㄐㄧˋ ㄩˊ","pinyin":"jì yú","definitions":[{"def":"希望得到不該擁有的東西。","quote":["《左傳》：「而下無覬覦。」"]}]}]},
 {"title":"垃圾","heteronyms":[{"pinyin":"lè sè","definitions":[{"def":"穢物、塵土及被棄的東西的統稱。"}]}]},
 {"title":"覬","heteronyms":[{"pinyin":"jì","definitions":[{"def":"希望得到。","type":"動"}]}]},
 {"title":"覦","heteronyms":[{"pinyin":"yú","definitions":[{"def":"企求。","type":"動"}]}]},
 {"title":"萌","heteronyms":[{"pinyin":"méng","definitions":[{"def":"草木初生的芽。","type":"名","example":["如：「萌芽」。"]},{"def":"發生。","type":"動","example":["如：「故態復萌」。"]}]}]},
 {"title":"{[8ff0]}族","heteronyms":[{"pinyin":"zhuàng zú","definitions":[{"def":"略"}]}]}
]`
	cedict := "# CC-CEDICT\n" +
		"覬覦 觊觎 [ji4 yu2] /to covet/to long for/\n" +
		"覬 觊 [ji4] /to covet/\n" +
		"覦 觎 [yu2] /to desire/\n" +
		"垃圾 垃圾 [la1 ji1] /trash/garbage/\n" +
		"綠 绿 [lu:4] /green/\n" +
		"北京 北京 [Bei3 jing1] /Beijing/\n" +
		"穢 秽 [hui4] /dirty/\n" +
		"塵 尘 [chen2] /dust/\n" +
		"這 这 [zhe4] /this/\n"
	ecdict := "word,phonetic,definition,translation,pos,collins,oxford,tag,bnc,frq,exchange,detail,audio\n" +
		"go,gou,v. move,\"vi. 去, 走\\nvt. 忍受\",,5,1,zk gk,35,35,i:going/p:went/d:gone/3:goes,,\n" +
		"went,went,,go的过去式,,,,,0,0,0:go/1:p,,\n" +
		"running,'rʌniŋ,n. the act of running,\"n. 赛跑\\na. 连续的\",,,,gk,0,3252,0:run/1:i,,\n" +
		"run,rʌn,v. move fast,\"vi. 跑\\nn. 跑步\",,5,1,zk,20,20,p:ran/d:run/i:running/3:runs,,\n" +
		"serendipity,\",serәn'dipiti\",n. good luck,n. 意外发现珍奇事物的本领,,,,gre,0,23199,,,\n" +
		"Hague,heig,,[法] 海牙,,,,,0,0,,,\n" +
		"big red apple,,,大红苹果,,,,,0,0,,,\n" +
		"look forward to,,,期待,,,,cet4,0,0,,,\n"
	files := map[string]string{"moe.json": moe, "cedict.txt": cedict, "ecdict.csv": ecdict}
	for n, c := range files {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(dir, "dict.db")
	n, err := buildDict(out, filepath.Join(dir, "moe.json"), filepath.Join(dir, "cedict.txt"), filepath.Join(dir, "ecdict.csv"))
	if err != nil {
		t.Fatal(err)
	}
	if n["zh"] != 5 || n["cedict"] != 9 || n["en"] < 6 {
		t.Fatalf("条数不对：%v", n)
	}
	return out
}

func TestMarkPinyin(t *testing.T) {
	for in, want := range map[string]string{
		"ji4 yu2": "jì yú", "lu:4": "lǜ", "Bei3 jing1": "Běi jīng", "hao3": "hǎo", "gou3": "gǒu", "liu2": "liú",
		"gui4": "guì", "er2": "ér", "r5": "r", "xx5": "xx", "m2": "m", "nu:3": "nǚ", "A1": "Ā", "de5": "de", "，": "，",
	} {
		if got := markPinyin(in); got != want {
			t.Errorf("%s → %s，应该是 %s", in, got, want)
		}
	}
}

func dictGet(t *testing.T, mux http.Handler, q, lang string) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/dict?q="+url.QueryEscape(q)+"&lang="+lang, nil))
	if rec.Code != 200 {
		t.Fatalf("%s：%d %s", q, rec.Code, rec.Body)
	}
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	return out
}

func TestDictLookup(t *testing.T) {
	setupDB(t)
	t.Setenv("SHUKU_DICT", makeTestDict(t))
	mux := http.HandlerFunc(handleDict)

	// 简体书里选中“觊觎”：大陆拼音、释义转成简体，不带书证；两个字也分别列出
	r := dictGet(t, mux, "觊觎", "zh")
	if r["installed"] != true || r["lang"] != "zh" {
		t.Fatalf("%v", r)
	}
	b, _ := json.Marshal(r)
	s := string(b)
	for _, want := range []string{`"word":"觊觎"`, `"jì yú"`, "希望得到不该拥有的东西", `"ch":"觊"`, `"ch":"觎"`} {
		if !strings.Contains(s, want) {
			t.Errorf("觊觎：缺少 %s：%s", want, s)
		}
	}
	if strings.Contains(s, "左傳") || strings.Contains(s, "左传") {
		t.Error("书证不用放进字典")
	}
	// 选多了字：找出里面的词
	r = dictGet(t, mux, "了觊觎着。", "zh")
	if w := r["words"].([]any); len(w) != 1 || w[0].(map[string]any)["word"] != "觊觎" {
		t.Errorf("从选中的文字里找词：%v", r["words"])
	}
	// 繁体书显示繁体
	r = dictGet(t, mux, "覬覦", "zh-TW")
	if w := r["words"].([]any); len(w) != 1 || w[0].(map[string]any)["word"] != "覬覦" || !strings.Contains(toJSON(w), "不該擁有") {
		t.Errorf("繁体：%v", r["words"])
	}
	// 大陆读音和台湾读音不同：标题用大陆读音，辞典里的读音标出来
	r = dictGet(t, mux, "垃圾", "")
	w := r["words"].([]any)[0].(map[string]any)
	if toJSON(w["pinyin"]) != `["lā jī"]` || !strings.Contains(toJSON(w["readings"]), `"tw":true`) {
		t.Errorf("垃圾：%v", w)
	}
	// 只有 CC-CEDICT 的词：给英文释义
	r = dictGet(t, mux, "北京", "")
	w = r["words"].([]any)[0].(map[string]any)
	if toJSON(w["pinyin"]) != `["Běi jīng"]` || w["en"] != "Beijing" {
		t.Errorf("北京：%v", w)
	}
	// 英文：变形找到原形
	r = dictGet(t, mux, "went", "en")
	ws := r["words"].([]any)
	if len(ws) != 1 || ws[0].(map[string]any)["word"] != "go" || !strings.Contains(toJSON(ws[0]), "过去式") {
		t.Errorf("went：%v", ws)
	}
	r = dictGet(t, mux, "Running", "en")
	if ws := r["words"].([]any); len(ws) != 2 || ws[1].(map[string]any)["word"] != "run" {
		t.Errorf("running：%v", r["words"])
	}
	r = dictGet(t, mux, "serendipity,", "")
	if ws := r["words"].([]any); len(ws) != 1 || ws[0].(map[string]any)["ph"] != "ˌserənˈdipiti" {
		t.Errorf("serendipity：%v", r["words"])
	}
	if ws := dictGet(t, mux, "big red apple", "en")["words"].([]any); len(ws) != 0 {
		t.Errorf("不常用的词组不收：%v", ws)
	}
	if ws := dictGet(t, mux, "look forward to", "en")["words"].([]any); len(ws) != 1 {
		t.Errorf("常用词组：%v", ws)
	}
	// 其他语言：本地字典没有，交给 AI
	r = dictGet(t, mux, "こんにちは", "")
	if r["lang"] != "ja" || len(r["words"].([]any)) != 0 {
		t.Errorf("日文：%v", r)
	}
	// 没有字典文件
	t.Setenv("SHUKU_DICT", filepath.Join(t.TempDir(), "none.db"))
	if r := dictGet(t, mux, "觊觎", ""); r["installed"] != false {
		t.Errorf("没有字典：%v", r)
	}
}

func TestDetectLang(t *testing.T) {
	for _, c := range [][3]string{
		{"觊觎", "", "zh"}, {"觊觎", "zh-CN", "zh"}, {"覬覦", "zh-Hant", "zh-TW"}, {"学校", "ja", "ja"}, {"こんにちは", "", "ja"},
		{"사랑", "", "ko"}, {"สวัสดี", "", "th"}, {"привет", "", "ru"}, {"hello", "", "en"}, {"bonjour", "fr-FR", "fr"},
		{"hello", "zh", "en"}, {"Việt Nam", "", "vi"}, {"مرحبا", "", "ar"}, {"שלום", "", "he"}, {"नमस्ते", "", "hi"},
	} {
		if got := detectLang(c[0], c[1]); got != c[2] {
			t.Errorf("%s（%s）→ %s，应该是 %s", c[0], c[1], got, c[2])
		}
	}
	if dictVoice("th") != "th-TH-PremwadeeNeural" || dictVoice("xx") != "en-US-AvaMultilingualNeural" || dictVoice("fr") != "fr-FR-DeniseNeural" {
		t.Error("发音音色")
	}
}

func TestDictExplainPrompt(t *testing.T) {
	p := dictPrompt("觊觎", "他一直觊觎着那把剑", "某书", "某人", "zh")
	if !strings.Contains(p, "上下文：他一直觊觎着那把剑") || !strings.Contains(p, "拼音") || !strings.Contains(p, "《某书》") {
		t.Errorf("中文词：%s", p)
	}
	p = dictPrompt("It went beyond words.", "", "", "", "en")
	if !strings.Contains(p, "翻译") || strings.Contains(p, "上下文") {
		t.Errorf("外文句子：%s", p)
	}
}
