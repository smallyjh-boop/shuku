package main

import "testing"

func TestParseFileName(t *testing.T) {
	cases := []struct{ in, title, author string }{
		{"[漫画]海贼王 第05卷【汉化】.cbz", "海贼王 第05卷", ""},
		{"《活着》作者：余华【完结】.txt", "活着", "余华"},
		{"[www.zxcs.me]三体2：黑暗森林(精校版).epub", "三体2：黑暗森林", ""},
		{"Microsoft Word - 深度学习入门_final(1).pdf", "深度学习入门", ""},
		{"红楼梦 - 曹雪芹.epub", "红楼梦", "曹雪芹"},
		{"[美]海明威 - 老人与海.mobi", "老人与海", "[美]海明威"},
		{"The Pragmatic Programmer -- David Thomas; Andrew Hunt -- Addison-Wesley, 2019 -- 9780135957059 -- abc -- Anna’s Archive.epub", "The Pragmatic Programmer", "David Thomas"},
		{"sanTi1 - 副本.epub", "sanTi1", ""},
	}
	for _, c := range cases {
		p := parseFileName(c.in)
		if p.Title != c.title || p.Author != c.author {
			t.Errorf("%s => %q / %q, want %q / %q", c.in, p.Title, p.Author, c.title, c.author)
		}
	}
	if s, n, ok := parseVolume("海贼王 第05卷"); !ok || s != "海贼王" || n != 5 {
		t.Errorf("parseVolume: %s %v %v", s, n, ok)
	}
	if normalizeAuthor("[美]海明威") != "海明威" {
		t.Error(normalizeAuthor("[美]海明威"))
	}
	if normalizeAuthor("刘 慈欣 著") != "刘慈欣" {
		t.Error(normalizeAuthor("刘 慈欣 著"))
	}
	if normName("刘慈欣") != normName("Liu Cixin") || normName("[美]海明威") != normName("海明威") {
		t.Error("normName", normName("刘慈欣"), normName("Liu Cixin"), normName("[美]海明威"))
	}
}

func TestRenderTemplate(t *testing.T) {
	b := &Book{Title: "三体Ⅱ·黑暗森林", Author: "刘慈欣", Category: "小说", Series: "三体", SeriesIndex: 2, Year: 2008}
	if got := renderTemplate(defaultSeriesTemplate, b, "epub"); got != "小说/刘慈欣/三体/02 三体Ⅱ·黑暗森林" {
		t.Error(got)
	}
	b2 := &Book{Title: "活着", Author: "余华 / 某某", Category: ""}
	if got := renderTemplate(defaultBookTemplate, b2, "txt"); got != "其他/余华等/活着/活着 - 余华等" {
		t.Error(got)
	}
	if got := renderTemplate("{分类}/{作者}/{书名}", &Book{Title: "a/b:c?"}, "epub"); got != "其他/未知作者/a-b：c？" {
		t.Error(got)
	}
}

func TestSplitChapters(t *testing.T) {
	text := "《活着》作者：余华\n\n第一章 开始\n内容一\n第二章 福贵\n内容二\n"
	chs := splitChapters(text)
	if len(chs) != 2 || chs[0].Title != "第一章 开始" {
		t.Errorf("%+v", chs)
	}
}

func TestCopyName(t *testing.T) {
	for _, n := range []string{"sanTi1 - 副本.epub", "a (1).epub", "b（2）.txt", "x - Copy.pdf", "y_copy.mobi", "三体 - 拷贝.epub"} {
		if !isCopyName("/inbox/" + n) {
			t.Error("应识别为副本：", n)
		}
	}
	for _, n := range []string{"sanTi1.epub", "copyright.epub", "三体(2008).epub", "第1卷.cbz"} {
		if isCopyName("/inbox/" + n) {
			t.Error("不是副本：", n)
		}
	}
}
