package main

import "testing"

// mutool 1.21 的 stext（墨水屏上 PDF 的文字层靠它）
func TestParseStext(t *testing.T) {
	x := `<?xml version="1.0"?>
<document name="a.pdf">
<page id="page2" width="595.2756" height="841.8898">
<block bbox="72 36.5 183.8 68.1">
<line bbox="72 36.567773 183.826 68.18177" wmode="0" dir="1 0">
<font name="STSong-Light" size="22">
<char quad="72 36.5 94 36.5 72 68.1 94 68.1" x="72" y="61.8" color="#000000" c="&#x7b2c;"/>
<char quad="94 36.5 99.6 36.5 94 68.1 99.6 68.1" x="94" y="61.8" color="#000000" c=" "/>
<char quad="98.5 36.5 110.4 36.5 98.5 68.1 110.4 68.1" x="98.5" y="61.8" color="#000000" c="2"/>
<char quad="0 0 0 0 0 0 0 0" x="0" y="0" color="#000000" c="&amp;"/>
</font>
</line>
<line bbox="72 80 90 92" wmode="0" dir="1 0"><font name="F" size="12"><char quad="" x="0" y="0" color="#000000" c=" "/></font></line>
</block>
</page>
</document>`
	lines := parseStext([]byte(x))
	if len(lines) != 1 || lines[0].Text != "第 2&" {
		t.Fatalf("%+v", lines)
	}
	l := lines[0]
	if l.X < 0.12 || l.X > 0.122 || l.Y < 0.043 || l.Y > 0.044 || l.W < 0.18 || l.H < 0.037 {
		t.Fatalf("位置不对：%+v", l)
	}
}

func TestParseBboxLayout(t *testing.T) {
	x := `<!DOCTYPE html><html xmlns="http://www.w3.org/1999/xhtml"><head><title></title></head><body>
<doc><page width="595.000000" height="842.000000"><flow><block xMin="72" yMin="60" xMax="300" yMax="80">
<line xMin="72.000000" yMin="60.000000" xMax="300.000000" yMax="80.000000">
<word xMin="72" yMin="60" xMax="130" yMax="80">Neural</word><word xMin="135" yMin="60" xMax="200" yMax="80">nets&amp;</word>
</line>
<line xMin="72" yMin="90" xMax="200" yMax="110"><word xMin="72" yMin="90" xMax="120" yMax="110">中文</word><word xMin="120" yMin="90" xMax="160" yMax="110">词语</word></line>
</block></flow></page></doc></body></html>`
	lines := parseBboxLayout([]byte(x))
	if len(lines) != 2 || lines[0].Text != "Neural nets&" || lines[1].Text != "中文词语" {
		t.Fatalf("%+v", lines)
	}
	if lines[0].X < 0.12 || lines[0].Y < 0.07 || lines[0].W < 0.38 {
		t.Fatalf("位置不对：%+v", lines[0])
	}
}

func TestParseMutoolOutline(t *testing.T) {
	s := "+\t\"第一章 \\\"开始\\\"\"\t#page=3&view=XYZ,0,800\n|\t\t\"第一节\"\t#page=4\n|\t\"附录\"\thttps://example.com\n"
	got := parseMutoolOutline(s)
	if len(got) != 3 || got[0].Title != `第一章 "开始"` || got[0].Page != 3 || got[0].Level != 0 ||
		got[1].Level != 1 || got[1].Page != 4 || got[2].Page != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestParsePdfinfo(t *testing.T) {
	s := "Title: x\nPages:          3\nPage    1 size: 595.276 x 841.89 pts (A4)\nPage    1 rot:  0\nPage    2 size: 595 x 842 pts\nPage    2 rot:  90\nPage    3 size: 300 x 400 pts\n"
	info := parsePdfinfo(s)
	if info.Pages != 3 || info.Sizes[0][0] != 595.276 || info.Sizes[1] != [2]float64{842, 595} || info.Sizes[2] != [2]float64{300, 400} {
		t.Fatalf("%+v", info)
	}
}
