package main

import (
	"testing"
	"time"
)

// 识别用哪些语言：书的语言 > 书名的文字 > 简体中文；不是英文的都加英文；没装的模型不用
func TestOcrLangFor(t *testing.T) {
	ocrLangCache.Lock()
	old, oldAt := ocrLangCache.have, ocrLangCache.at
	ocrLangCache.have = map[string]bool{"eng": true, "chi_sim": true, "chi_tra": true, "jpn": true, "tha": true, "kor": true, "fra": true}
	ocrLangCache.at = time.Now()
	ocrLangCache.Unlock()
	defer func() {
		ocrLangCache.Lock()
		ocrLangCache.have, ocrLangCache.at = old, oldAt
		ocrLangCache.Unlock()
	}()
	cases := []struct{ lang, title, want, out string }{
		{"th", "", "", "tha+eng"},
		{"tha", "", "", "tha+eng"},
		{"en", "", "", "eng"},
		{"zh-Hant", "", "", "chi_tra+eng"},
		{"fr", "", "", "fra+eng"},
		{"", "เจ้าชายน้อย", "", "tha+eng"},         // 不知道语言：看书名是泰文
		{"", "Thinking, Fast and Slow", "", "eng"}, // 拉丁字母
		{"", "三体", "", "chi_sim+eng"},
		{"", "", "", "chi_sim+eng"},
		{"de", "Der Prozess", "", "eng"}, // 没装德文：按书名的文字，拉丁字母用英文
		{"", "", "tha+eng", "tha+eng"},   // 指定的
		{"", "", "deu+eng", "eng"},       // 指定的没装：去掉
		{"th", "", "kor", "kor"},         // 指定的优先于书的语言
		{"ko", "", "", "kor+eng"},
	}
	for _, c := range cases {
		if got := ocrLangFor(c.lang, c.title, c.want); got != c.out {
			t.Errorf("ocrLangFor(%q, %q, %q) = %q，应该是 %q", c.lang, c.title, c.want, got, c.out)
		}
	}
}
