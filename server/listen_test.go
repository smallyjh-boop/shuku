package main

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// 书库切的句子要和手机、手表（android/common BookText）一模一样：第几章第几句就是一句语音的名字。
// testdata/listen/kotlin.out 是 BookText.format + sentences 对 texts.txt 的输出（改了切法两边一起改、重新生成）
func TestListenSentencesMatchKotlin(t *testing.T) {
	in, err := os.ReadFile("testdata/listen/texts.txt")
	if err != nil {
		t.Fatal(err)
	}
	want, _ := os.ReadFile("testdata/listen/kotlin.out")
	var b strings.Builder
	for _, text := range strings.Split(string(in), "\x01") {
		for _, s := range listenSentences(listenFormat(text)) {
			fmt.Fprintf(&b, "%d\t%d\t%s\n", s.Start, s.End, s.Text)
		}
		b.WriteString("====\n")
	}
	got := b.String()
	if got != string(want) {
		gl, wl := strings.Split(got, "\n"), strings.Split(string(want), "\n")
		for k := 0; k < len(gl) || k < len(wl); k++ {
			var g, w string
			if k < len(gl) {
				g = gl[k]
			}
			if k < len(wl) {
				w = wl[k]
			}
			if g != w {
				t.Fatalf("第 %d 行不一样：\n书库  %q\nKotlin %q", k+1, g, w)
			}
		}
	}
}

func TestMP3Duration(t *testing.T) {
	// 48kbps 的一帧头（MPEG1 Layer3, 48k, 24kHz 不对……用 MPEG2 48k）
	dir := t.TempDir()
	p := dir + "/a.mp3"
	frame := []byte{0xFF, 0xF3, 0x64, 0xC4} // MPEG2 Layer3 bitrate index 6 = 48kbps
	data := append(frame, make([]byte, 6000-4)...)
	os.WriteFile(p, data, 0o644)
	if d := mp3Duration(p); d < 0.99 || d > 1.01 {
		t.Fatalf("48kbps 6000 字节应该是 1 秒，得到 %.3f", d)
	}
}
