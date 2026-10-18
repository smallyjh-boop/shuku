package main

// 书里的大图：墨水屏、旧手机按需取书时，比屏幕大的图在 NAS 上缩小到屏幕大小再传（?max=宽x高，设备像素）。
// 几千像素的插图在设备上解码一张就要几十 MB 内存，内存小的阅读器翻到几张大图就会卡死；
// 缩小以后传得也快。只缩 JPEG、PNG、WebP（GIF 可能是动图，SVG 本来就不占内存，照原样传）。

import (
	"archive/zip"
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/image/draw"
)

// ?max=宽x高
func parseImageMax(s string) (int, int, bool) {
	a, b, ok := strings.Cut(s, "x")
	if !ok {
		return 0, 0, false
	}
	w, err1 := strconv.Atoi(a)
	h, err2 := strconv.Atoi(b)
	if err1 != nil || err2 != nil || w < 200 || h < 200 || w > 8000 || h > 8000 {
		return 0, 0, false
	}
	return w, h, true
}

func shrinkableImage(ctype string) bool {
	return ctype == "image/jpeg" || ctype == "image/png" || ctype == "image/webp"
}

// 缩到 maxW×maxH 以内。不用缩（本来就不大、解不开）时返回 false，照原样传
func shrinkImage(data []byte, maxW, maxH int) ([]byte, string, bool) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || format == "gif" || (cfg.Width <= maxW && cfg.Height <= maxH) {
		return nil, "", false
	}
	if cfg.Width*cfg.Height > 80_000_000 { // 太大的不解（解一张就要几百 MB）
		return nil, "", false
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", false
	}
	b := img.Bounds()
	scale := min(float64(maxW)/float64(b.Dx()), float64(maxH)/float64(b.Dy()))
	w, h := max(1, int(float64(b.Dx())*scale+0.5)), max(1, int(float64(b.Dy())*scale+0.5))
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.BiLinear.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
	var out bytes.Buffer
	// 带透明的（PNG 的透明背景）还用 PNG，其余用 JPEG
	if o, ok := img.(interface{ Opaque() bool }); ok && !o.Opaque() {
		if png.Encode(&out, dst) != nil {
			return nil, "", false
		}
		return out.Bytes(), "image/png", true
	}
	if jpeg.Encode(&out, dst, &jpeg.Options{Quality: 82}) != nil {
		return nil, "", false
	}
	if out.Len() >= len(data) { // 缩完反而更大（本来压得很狠）：用原图
		return nil, "", false
	}
	return out.Bytes(), "image/jpeg", true
}

// 缩好的图：最近的放在内存里（一共不超过 48 MB），翻回去不用再缩
type shrunkImage struct {
	data  []byte
	ctype string // 空：不用缩，用原图
}

var imageCache = struct {
	sync.Mutex
	m     map[string]shrunkImage
	order []string
	bytes int
}{m: map[string]shrunkImage{}}

var imageWork keyedMutex

func zipEntryShrunk(zf *zip.File, key string, maxW, maxH int) (shrunkImage, error) {
	ckey := fmt.Sprintf("%s|%dx%d", key, maxW, maxH)
	imageCache.Lock()
	if s, ok := imageCache.m[ckey]; ok {
		imageCache.Unlock()
		return s, nil
	}
	imageCache.Unlock()
	unlock := imageWork.Lock(ckey)
	defer unlock()
	imageCache.Lock()
	if s, ok := imageCache.m[ckey]; ok {
		imageCache.Unlock()
		return s, nil
	}
	imageCache.Unlock()
	var s shrunkImage
	if zf.UncompressedSize64 <= 40<<20 {
		rc, err := zf.Open()
		if err != nil {
			return s, err
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return s, err
		}
		if out, ctype, ok := shrinkImage(data, maxW, maxH); ok {
			s = shrunkImage{out, ctype}
		}
	}
	imageCache.Lock()
	if _, ok := imageCache.m[ckey]; !ok {
		imageCache.m[ckey] = s
		imageCache.order = append(imageCache.order, ckey)
		imageCache.bytes += len(s.data)
		for imageCache.bytes > 48<<20 && len(imageCache.order) > 1 {
			old := imageCache.order[0]
			imageCache.order = imageCache.order[1:]
			imageCache.bytes -= len(imageCache.m[old].data)
			delete(imageCache.m, old)
		}
	}
	imageCache.Unlock()
	return s, nil
}
