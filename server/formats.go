package main

import (
	"path/filepath"
	"strings"
)

// 每种扩展名对应：格式名、书的类型、前端用哪种阅读方式
type FormatInfo struct {
	Format string // 规范化格式名
	Kind   string // book | comic
	Reader string // foliate | pdf | comic | convert
}

var formats = map[string]FormatInfo{
	"epub":  {"epub", "book", "foliate"},
	"kepub": {"epub", "book", "foliate"},
	"mobi":  {"mobi", "book", "foliate"},
	"azw":   {"azw", "book", "foliate"},
	"azw3":  {"azw3", "book", "foliate"},
	"prc":   {"mobi", "book", "foliate"},
	"fb2":   {"fb2", "book", "foliate"},
	"fbz":   {"fbz", "book", "foliate"},
	"txt":   {"txt", "book", "convert"},
	"pdf":   {"pdf", "book", "pdf"},
	"djvu":  {"djvu", "book", "convert"},
	"djv":   {"djvu", "book", "convert"},
	"cbz":   {"cbz", "comic", "comic"},
	"cbr":   {"cbr", "comic", "comic"},
	"cb7":   {"cb7", "comic", "comic"},
	"cbt":   {"cbt", "comic", "comic"},
	"zip":   {"zip", "comic", "comic"}, // 入库时检查：只有图片才算漫画
	"rar":   {"rar", "comic", "comic"},
	"7z":    {"7z", "comic", "comic"},
	"docx":  {"docx", "book", "convert"},
	"doc":   {"doc", "book", "convert"},
	"rtf":   {"rtf", "book", "convert"},
	"odt":   {"odt", "book", "convert"},
	"html":  {"html", "book", "convert"},
	"htm":   {"html", "book", "convert"},
	"md":    {"md", "book", "convert"},
	"chm":   {"chm", "book", "convert"},
	"lit":   {"lit", "book", "convert"},
	"pdb":   {"pdb", "book", "convert"},
	"lrf":   {"lrf", "book", "convert"},
	"snb":   {"snb", "book", "convert"},
	"umd":   {"umd", "book", "convert"},
}

func extOf(path string) string {
	name := strings.ToLower(filepath.Base(path))
	if strings.HasSuffix(name, ".fb2.zip") {
		return "fbz"
	}
	return strings.TrimPrefix(filepath.Ext(name), ".")
}

func formatOf(path string) (FormatInfo, bool) {
	f, ok := formats[extOf(path)]
	return f, ok
}

var imageExts = map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".webp": true, ".gif": true, ".bmp": true, ".avif": true, ".jxl": true}

func isImageName(name string) bool {
	return imageExts[strings.ToLower(filepath.Ext(name))]
}

// 阅读方式（返回给前端）
func readerFor(format string) string {
	switch format {
	case "epub", "mobi", "azw", "azw3", "fb2", "fbz":
		return "foliate"
	case "txt", "docx", "doc", "rtf", "odt", "html", "md", "chm", "lit", "pdb", "lrf", "snb", "umd":
		return "epub-converted"
	case "pdf":
		return "pdf"
	case "djvu":
		return "pdf-converted"
	case "cbz", "cbr", "cb7", "cbt", "zip", "rar", "7z":
		return "comic"
	}
	return "download"
}

// 多格式时优先用哪个读
var formatRank = map[string]int{"epub": 1, "azw3": 2, "mobi": 3, "azw": 4, "fb2": 5, "fbz": 6, "cbz": 7, "cbr": 8, "cb7": 9, "cbt": 10, "zip": 11, "rar": 12, "7z": 13, "pdf": 14, "txt": 15, "djvu": 16}

func rankFormat(f string) int {
	if r, ok := formatRank[f]; ok {
		return r
	}
	return 50
}
