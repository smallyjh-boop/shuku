package main

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// 一本没有换行的网文：整本书挤在一行里，段落之间只有全角空格。以前整本书只有一节，手机上排版要几十秒
func TestSplitChaptersWithoutLineBreaks(t *testing.T) {
	var b strings.Builder
	b.WriteString("当合欢宗老祖，真的太难了　　作者：测试")
	para := strings.Repeat("他看了看四周，没有说话。", 20) // 240 字一段
	for c := 1; c <= 50; c++ {
		fmt.Fprintf(&b, "　　第%d章 初入宗门", c)
		for k := 0; k < 12; k++ {
			b.WriteString("　　" + para)
		}
	}
	chs := splitChapters(b.String())
	if len(chs) < 50 {
		t.Fatalf("应该分出 50 章，实际 %d 章", len(chs))
	}
	if chs[len(chs)-1].Title != "第50章 初入宗门" {
		t.Errorf("最后一章的标题：%q", chs[len(chs)-1].Title)
	}
	for _, c := range chs {
		size := 0
		for _, l := range c.Lines {
			size += len(l)
		}
		if size > 70000 {
			t.Fatalf("%s 有 %d 字节，太大", c.Title, size)
		}
	}
	if !txtSplitChanged(b.String()) {
		t.Error("没有换行的书要重新分章")
	}
}

// 章节标题直接接在上一段句号后面、标题后面只有一个空格就是正文
func TestSplitChaptersInlineTitles(t *testing.T) {
	var b strings.Builder
	body := strings.Repeat("她笑了笑，转身走了。", 300) // 3000 字一章
	for c := 1; c <= 20; c++ {
		fmt.Fprintf(&b, "第%d章 风起 %s", c, body)
	}
	chs := splitChapters(b.String())
	if len(chs) != 20 || chs[0].Title != "第1章 风起" || chs[19].Title != "第20章 风起" {
		t.Fatalf("%d 章：%q … %q", len(chs), chs[0].Title, chs[len(chs)-1].Title)
	}
	if got := strings.Join(chs[3].Lines, ""); got != body {
		t.Fatalf("第 4 章正文不对：%d 字", utf8.RuneCountInString(got))
	}
}

// 连标点都没有的一大段：硬切，每段不超过 1000 字
func TestSplitLongLineWithoutPunctuation(t *testing.T) {
	line := strings.Repeat("字", 25000)
	parts := splitLongLine(line)
	total := 0
	for _, p := range parts {
		n := utf8.RuneCountInString(p)
		if n > 1000 {
			t.Fatalf("一段 %d 字", n)
		}
		total += n
	}
	if total != 25000 {
		t.Fatalf("切完少了字：%d", total)
	}
	chs := splitChapters(line)
	for _, c := range chs {
		size := 0
		for _, l := range c.Lines {
			size += len(l)
		}
		if size > 70000 {
			t.Fatalf("%s 有 %d 字节，太大", c.Title, size)
		}
	}
}

// 正常换行的书：分章结果和以前完全一样（已有的阅读位置、书签、全文索引都还对得上）
func TestSplitChaptersNormalBookUnchanged(t *testing.T) {
	var b strings.Builder
	b.WriteString("书名\r\n作者：某人\r\n\r\n")
	for c := 1; c <= 30; c++ {
		fmt.Fprintf(&b, "第%d章 标题%d\r\n", c, c)
		for k := 0; k < 20; k++ {
			b.WriteString("　　" + strings.Repeat("一句话，", 30) + "完了。\r\n")
		}
	}
	// 一段很长、但没超过 3000 字的段落也保持原样
	b.WriteString("　　" + strings.Repeat("长", 2900) + "\r\n")
	text := b.String()
	if txtSplitChanged(text) {
		t.Fatal("正常的书不该算作要重新分章")
	}
	chs := splitChapters(text)
	if len(chs) != 30 || chs[0].Title != "第1章 标题1" || len(chs[0].Lines) != 20 {
		t.Fatalf("分章：%d 章，第一章 %q %d 行", len(chs), chs[0].Title, len(chs[0].Lines))
	}
	last := chs[len(chs)-1].Lines
	if utf8.RuneCountInString(last[len(last)-1]) != 2900 {
		t.Error("没超过限制的长段落不该被切开")
	}
	// 只有 \r 换行的老式文件也照旧
	if txtSplitChanged(strings.ReplaceAll(text, "\r\n", "\r")) {
		t.Error("只用 \\r 换行的书不该算作要重新分章")
	}
}

// Unicode 段落分隔符当作换行
func TestSplitChaptersUnicodeSeparators(t *testing.T) {
	text := "第1章 开始 正文一 第2章 继续 正文二 还有"
	chs := splitChapters(text)
	if len(chs) != 2 || chs[1].Title != "第2章 继续" || len(chs[1].Lines) != 2 {
		t.Fatalf("%+v", chs)
	}
	if !txtSplitChanged(text) {
		t.Error("有段落分隔符的书要重新分章")
	}
}

func TestChunkSentencesKeepsQuotes(t *testing.T) {
	s := strings.Repeat("“你来了？”他问。", 100) // 900 字
	parts := chunkSentences(s+s, 400, 1000)
	for _, p := range parts {
		if strings.HasPrefix(p, "”") {
			t.Fatalf("引号被切到了下一段开头：%q", p[:12])
		}
		if n := utf8.RuneCountInString(p); n > 1000 {
			t.Fatalf("一段 %d 字", n)
		}
	}
	if strings.Join(parts, "") != s+s {
		t.Fatal("切完的文字和原文不一样")
	}
}
