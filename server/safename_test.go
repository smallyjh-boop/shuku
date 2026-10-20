package main

import (
	"strings"
	"testing"
)

// 长文件名截短后扩展名要还在，否则上传会被当成“不支持的文件类型”
func TestSafeNameKeepsExtension(t *testing.T) {
	long := strings.Repeat("看人的艺术书名", 12) + " - 作者.pdf"
	got := safeName(long)
	if !strings.HasSuffix(got, ".pdf") || len([]rune(got)) > 80 {
		t.Fatalf("截短后扩展名丢了或太长：%q", got)
	}
	if got := safeName(strings.Repeat("书", 100) + ".fb2.zip"); !strings.HasSuffix(got, "书.fb2.zip") || len([]rune(got)) > 80 {
		t.Fatalf("两段的扩展名要整个留着：%q", got)
	}
	if got := safeName("短名字.epub"); got != "短名字.epub" {
		t.Fatalf("短名字不该变：%q", got)
	}
}
