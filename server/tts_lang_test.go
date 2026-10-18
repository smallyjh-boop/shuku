package main

import "testing"

func TestScriptLangAndAdaptVoice(t *testing.T) {
	setupDB(t)
	cases := map[string]string{
		"สวัสดีครับ วันนี้อากาศดีมาก":  "th",
		"안녕하세요 반갑습니다":                  "ko",
		"こんにちは、世界":                     "ja",
		"今天天气很好":                       "zh",
		"Hello world, this is a test.": "latin",
		"Привет, как дела?":            "ru",
		"今天读了 The Little Prince":       "zh",
		"他说 OK":                        "zh",
		"他说：สวัสดี":                    "zh", // 中文为主的一句里夹个泰文词：不换
		"12345 —— …":                   "",
	}
	for text, want := range cases {
		if got := scriptLang(text); got != want {
			t.Errorf("scriptLang(%q) = %q，应该是 %q", text, got, want)
		}
	}
	if v := adaptVoice("สวัสดีครับ วันนี้อากาศดีมาก", "zh-CN-XiaoxiaoNeural", "zh"); v != "th-TH-PremwadeeNeural" {
		t.Errorf("中文音色读泰文应该换成泰语音色：%s", v)
	}
	if v := adaptVoice("สวัสดีครับ", "th-TH-NiwatNeural", "th"); v != "th-TH-NiwatNeural" {
		t.Errorf("泰语音色读泰文不用换：%s", v)
	}
	if v := adaptVoice("This is a whole English sentence.", "zh-CN-YunxiNeural", "zh"); v != "en-US-AriaNeural" {
		t.Errorf("中文音色读整句英文应该换成英文音色：%s", v)
	}
	if v := adaptVoice("C'est une phrase entière en français.", "zh-CN-YunxiNeural", "fr"); v != "fr-FR-DeniseNeural" {
		t.Errorf("法文书里的句子用法语音色：%s", v)
	}
	if v := adaptVoice("This is English.", "en-GB-SoniaNeural", "zh"); v != "en-GB-SoniaNeural" {
		t.Errorf("英文音色读英文不用换：%s", v)
	}
	if v := adaptVoice("他说 OK 就好。", "zh-CN-YunxiNeural", "zh"); v != "zh-CN-YunxiNeural" {
		t.Errorf("中文句子里夹个英文词不用换：%s", v)
	}
	if v := adaptVoice("今天天气很好", "en-US-AvaMultilingualNeural", "en"); v != "en-US-AvaMultilingualNeural" {
		t.Errorf("多语言音色读中文不用换：%s", v)
	}
	if v := adaptVoice("今天天气很好", "th-TH-PremwadeeNeural", "th"); v[:3] != "zh-" {
		t.Errorf("泰语音色读中文应该换成中文音色：%s", v)
	}
	if v := adaptVoice("今天天气很好", "zh-TW-HsiaoChenNeural", "zh"); v != "zh-TW-HsiaoChenNeural" {
		t.Errorf("台湾腔读中文不用换：%s", v)
	}
	if v := adaptVoice("Привет", "uk-UA-PolinaNeural", "uk"); v != "uk-UA-PolinaNeural" {
		t.Errorf("乌克兰语音色读俄文字母不用换：%s", v)
	}
	if edgeDefaults()["th"] != "th-TH-PremwadeeNeural" || edgeDefaults()["en"] == "" {
		t.Error("默认音色缺泰语或英语")
	}
}
