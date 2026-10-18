package main

// 书里的单个文件（EPUB 就是一个 zip）：阅读器在线时按需取，不用先把整本书下载下来。
// 墨水屏、旧手机上，几 MB、几十 MB 的书下载、解压（浏览器里用 JS 解压）、整本放在内存里都很慢，内存小的还会卡死；
// 按需取只传正在看的那一章和它用到的图片，解压在 NAS 上做。断网时阅读器照旧下载整本（App 里用下载好的）。
//
//	GET /api/files/{fid}/zip               书里的文件列表：[名字, 解压后的大小]
//	    ?split=limit,part                  另外给出超过 limit 字节的章节怎么拆（zipsplit.go），阅读器按段取
//	    ?inline=1                          顺便带上打开书一定要读的 container.xml、OPF、目录
//	GET /api/files/{fid}/zip/{name...}     某个文件解压后的内容
//	    ?split=limit,part&part=j           拆开的大章节的第 j 段
//	    ?max=宽x高                          比这大的图缩小到这么大（zipimage.go）
//	GET /api/files/{fid}/epub/zip…         服务器转换出来的 EPUB（TXT 等）里的，用法一样（带上 ?v= 分章规则版本）
//
// 文字类的（章节、样式、目录）浏览器支持就压缩传输：墨水屏阅读器的无线网卡大多很慢。

import (
	"archive/zip"
	"compress/gzip"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
)

// 按扩展名的类型（mime 包不认识的几个补上）
var zipEntryTypes = map[string]string{
	".xhtml": "application/xhtml+xml", ".xht": "application/xhtml+xml", ".opf": "application/oebps-package+xml",
	".ncx": "application/x-dtbncx+xml", ".xml": "application/xml", ".css": "text/css", ".svg": "image/svg+xml",
	".otf": "font/otf", ".ttf": "font/ttf", ".woff": "font/woff", ".woff2": "font/woff2", ".webp": "image/webp",
	".smil": "application/smil+xml", ".js": "text/javascript",
}

func zipEntryType(name string) string {
	ext := strings.ToLower(path.Ext(name))
	if t, ok := zipEntryTypes[ext]; ok {
		return t
	}
	if t := mime.TypeByExtension(ext); t != "" {
		return t
	}
	return "application/octet-stream"
}

// 值得压缩传输的（文字类）
func compressibleType(t string) bool {
	return strings.HasPrefix(t, "text/") || strings.Contains(t, "xml") || strings.Contains(t, "json") || strings.Contains(t, "javascript")
}

// 浏览器支持就用 gzip 写（快速档：NAS 不费劲，文字照样压到三四分之一）
func gzipWriter(w http.ResponseWriter, r *http.Request, contentType string) (io.Writer, func()) {
	if !compressibleType(contentType) || !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		return w, func() {}
	}
	w.Header().Set("Content-Encoding", "gzip")
	w.Header().Add("Vary", "Accept-Encoding")
	w.Header().Del("Content-Length")
	gz, _ := gzip.NewWriterLevel(w, gzip.BestSpeed)
	return gz, func() { gz.Close() }
}

// 要读的 zip 文件在哪：原文件（只有 EPUB），或者转换出来的 EPUB
func zipPathOf(w http.ResponseWriter, r *http.Request, converted bool) (string, bool) {
	f, ok := visibleFile(w, r)
	if !ok {
		return "", false
	}
	if converted {
		out, err := convertedEPUBOf(f)
		if err != nil {
			errJSON(w, 500, err.Error())
			return "", false
		}
		return out, true
	}
	if f.Format != "epub" {
		errJSON(w, 404, "这本书不是 EPUB")
		return "", false
	}
	return f.Path, true
}

func handleZipList(converted bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := zipPathOf(w, r, converted)
		if !ok {
			return
		}
		zr, err := zip.OpenReader(p)
		if err != nil {
			errJSON(w, 404, "打不开这本书："+err.Error())
			return
		}
		defer zr.Close()
		st, err := os.Stat(p)
		if err != nil {
			errJSON(w, 404, "文件不存在")
			return
		}
		opts, split := parseSplitOpts(r.URL.Query().Get("split"))
		list := make([][2]any, 0, len(zr.File))
		splits := map[string]*splitPlan{}
		for _, zf := range zr.File {
			if zf.FileInfo().IsDir() {
				continue
			}
			list = append(list, [2]any{zf.Name, zf.UncompressedSize64})
			if split {
				if plan := zipEntryPlan(zf, zipEntryKey(p, st, zf.Name), opts); plan != nil {
					splits[zf.Name] = plan
				}
			}
		}
		res := map[string]any{"entries": list}
		if split {
			res["splits"] = splits
		}
		// 打开书一定要读的几个小文件（container.xml、OPF、目录）直接带上：少等几个来回（墨水屏的无线网大多很慢）
		if r.URL.Query().Get("inline") == "1" {
			res["inline"] = zipInlineFiles(zr)
		}
		w.Header().Set("Cache-Control", "private, no-cache")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		out, done := gzipWriter(w, r, "application/json")
		defer done()
		w.WriteHeader(200)
		writeJSONTo(out, res)
	}
}

func handleZipEntry(converted bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := zipPathOf(w, r, converted)
		if !ok {
			return
		}
		name := r.PathValue("name")
		zr, err := zip.OpenReader(p)
		if err != nil {
			errJSON(w, 404, "打不开这本书："+err.Error())
			return
		}
		defer zr.Close()
		var zf *zip.File
		for _, x := range zr.File {
			if x.Name == name {
				zf = x
				break
			}
		}
		if zf == nil {
			errJSON(w, 404, "书里没有这个文件")
			return
		}
		st, err := os.Stat(p)
		if err != nil {
			errJSON(w, 404, "文件不存在")
			return
		}
		q := r.URL.Query()
		// 拆开的大章节的某一段
		part, isPart := -1, q.Has("part")
		var opts splitOpts
		if isPart {
			var ok bool
			opts, ok = parseSplitOpts(q.Get("split"))
			part, _ = strconv.Atoi(q.Get("part"))
			if !ok || part < 0 {
				errJSON(w, 400, "参数不对")
				return
			}
		}
		ctype := zipEntryType(name)
		// 大图缩到屏幕大小（zipimage.go）
		maxW, maxH, shrink := parseImageMax(q.Get("max"))
		shrink = shrink && !isPart && shrinkableImage(ctype)
		// 书文件没变、这个文件的校验值没变，就是同一个内容：浏览器缓存里有就不用再传
		etag := fmt.Sprintf(`"%x-%x-%08x`, st.ModTime().UnixNano(), st.Size(), zf.CRC32)
		if isPart {
			etag += fmt.Sprintf("-p%d-%d-%d", part, opts.limit, opts.part)
		}
		if shrink {
			etag += fmt.Sprintf("-m%dx%d", maxW, maxH)
		}
		if compressibleType(ctype) && strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			etag += "-gz"
		}
		etag += `"`
		w.Header().Set("ETag", etag)
		w.Header().Set("Cache-Control", "private, max-age=86400")
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		if isPart {
			key := zipEntryKey(p, st, name)
			plan := zipEntryPlan(zf, key, opts)
			if plan == nil || part >= len(plan.Ranges) {
				errJSON(w, 404, "这一章没有拆开")
				return
			}
			text, err := zipEntryText(zf, key)
			if err != nil {
				errJSON(w, 500, "解压失败："+err.Error())
				return
			}
			w.Header().Set("Content-Type", ctype)
			out, done := gzipWriter(w, r, ctype)
			defer done()
			out.Write(plan.partText(text, part))
			return
		}
		if shrink {
			if img, err := zipEntryShrunk(zf, zipEntryKey(p, st, name), maxW, maxH); err == nil && img.ctype != "" {
				w.Header().Set("Content-Type", img.ctype)
				w.Header().Set("Content-Length", strconv.Itoa(len(img.data)))
				w.Write(img.data)
				return
			}
		}
		rc, err := zf.Open()
		if err != nil {
			errJSON(w, 500, "解压失败："+err.Error())
			return
		}
		defer rc.Close()
		w.Header().Set("Content-Type", ctype)
		w.Header().Set("Content-Length", strconv.FormatUint(zf.UncompressedSize64, 10))
		out, done := gzipWriter(w, r, ctype)
		defer done()
		io.Copy(out, rc)
	}
}

func writeJSONTo(w io.Writer, v any) {
	json.NewEncoder(w).Encode(v)
}

// 打开书一定要读的几个文件的内容（一共不超过 2 MB）
func zipInlineFiles(zr *zip.ReadCloser) map[string]string {
	byName := map[string]*zip.File{}
	for _, zf := range zr.File {
		byName[zf.Name] = zf
	}
	out := map[string]string{}
	budget := 2 << 20
	read := func(name string) []byte {
		zf := byName[name]
		if zf == nil || int(zf.UncompressedSize64) > budget {
			return nil
		}
		rc, err := zf.Open()
		if err != nil {
			return nil
		}
		defer rc.Close()
		b, err := io.ReadAll(io.LimitReader(rc, int64(budget)))
		if err != nil {
			return nil
		}
		budget -= len(b)
		out[name] = string(b)
		return b
	}
	container := read("META-INF/container.xml")
	var c struct {
		Rootfiles []struct {
			Path string `xml:"full-path,attr"`
			Type string `xml:"media-type,attr"`
		} `xml:"rootfiles>rootfile"`
	}
	if container == nil || xml.Unmarshal(container, &c) != nil {
		return out
	}
	opfPath := ""
	for _, rf := range c.Rootfiles {
		if rf.Type == "application/oebps-package+xml" {
			opfPath = rf.Path
			break
		}
	}
	opf := read(opfPath)
	var pkg struct {
		Items []struct {
			ID    string `xml:"id,attr"`
			Href  string `xml:"href,attr"`
			Type  string `xml:"media-type,attr"`
			Props string `xml:"properties,attr"`
		} `xml:"manifest>item"`
		Spine struct {
			Toc string `xml:"toc,attr"`
		} `xml:"spine"`
	}
	if opf == nil || xml.Unmarshal(opf, &pkg) != nil {
		return out
	}
	// 目录：EPUB 3 的 nav，没有就用 NCX
	nav := ""
	for _, it := range pkg.Items {
		if strings.Contains(" "+it.Props+" ", " nav ") {
			nav = it.Href
		}
	}
	if nav == "" {
		for _, it := range pkg.Items {
			if it.ID == pkg.Spine.Toc || it.Type == "application/x-dtbncx+xml" {
				nav = it.Href
				break
			}
		}
	}
	if nav != "" {
		if u, err := url.PathUnescape(strings.SplitN(nav, "#", 2)[0]); err == nil {
			read(path.Join(path.Dir(opfPath), u))
		}
	}
	return out
}
