package main

import (
	"archive/tar"
	"archive/zip"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/nwaples/rardecode/v2"
)

// 漫画压缩包统一接口：列出图片、读取任意文件
type Archive interface {
	Names() []string // 全部文件（排好序）
	Read(name string) ([]byte, error)
	Close() error
}

func images(a Archive) []string { return imageNames(a.Names()) }

func imageNames(names []string) []string {
	var out []string
	for _, n := range names {
		base := filepath.Base(n)
		if strings.HasPrefix(base, ".") || strings.Contains(n, "__MACOSX") {
			continue
		}
		if isImageName(n) {
			out = append(out, n)
		}
	}
	return out
}

// 自然排序：第2话 < 第10话
func naturalLess(a, b string) bool {
	ra, rb := []rune(strings.ToLower(a)), []rune(strings.ToLower(b))
	i, j := 0, 0
	for i < len(ra) && j < len(rb) {
		if unicode.IsDigit(ra[i]) && unicode.IsDigit(rb[j]) {
			si := i
			for i < len(ra) && unicode.IsDigit(ra[i]) {
				i++
			}
			sj := j
			for j < len(rb) && unicode.IsDigit(rb[j]) {
				j++
			}
			na := strings.TrimLeft(string(ra[si:i]), "0")
			nb := strings.TrimLeft(string(rb[sj:j]), "0")
			if len(na) != len(nb) {
				return len(na) < len(nb)
			}
			if na != nb {
				return na < nb
			}
			continue
		}
		if ra[i] != rb[j] {
			return ra[i] < rb[j]
		}
		i++
		j++
	}
	return len(ra)-i < len(rb)-j
}

func sortNatural(names []string) {
	sort.SliceStable(names, func(i, j int) bool { return naturalLess(names[i], names[j]) })
}

// ---------- zip ----------

type zipArchive struct {
	r     *zip.ReadCloser
	files map[string]*zip.File
	names []string
}

func (z *zipArchive) Names() []string { return z.names }
func (z *zipArchive) Close() error    { return z.r.Close() }
func (z *zipArchive) Read(name string) ([]byte, error) {
	f, ok := z.files[name]
	if !ok {
		return nil, os.ErrNotExist
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(io.LimitReader(rc, 200<<20))
}

func (z *zipArchive) ReadHead(name string, n int) ([]byte, error) {
	f, ok := z.files[name]
	if !ok {
		return nil, os.ErrNotExist
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(io.LimitReader(rc, int64(n)))
}

func openZip(path string) (*zipArchive, error) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	z := &zipArchive{r: r, files: map[string]*zip.File{}}
	for _, f := range r.File {
		if f.FileInfo().IsDir() {
			continue
		}
		name := f.Name
		// 老的中文 zip 文件名是 GBK 编码，没有 UTF-8 标志位
		if f.NonUTF8 {
			if s, ok := decodeGBK([]byte(name)); ok {
				name = s
			}
		}
		z.files[name] = f
		z.names = append(z.names, name)
	}
	sortNatural(z.names)
	return z, nil
}

// ---------- 目录（rar / 7z / tar 解压后的缓存） ----------

type dirArchive struct {
	root  string
	names []string
}

func (d *dirArchive) Names() []string { return d.names }
func (d *dirArchive) Close() error    { return nil }
func (d *dirArchive) ReadHead(name string, n int) ([]byte, error) {
	p := filepath.Join(d.root, filepath.FromSlash(name))
	if !strings.HasPrefix(p, d.root) {
		return nil, os.ErrNotExist
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, int64(n)))
}
func (d *dirArchive) Read(name string) ([]byte, error) {
	p := filepath.Join(d.root, filepath.FromSlash(name))
	if !strings.HasPrefix(p, d.root) {
		return nil, os.ErrNotExist
	}
	return os.ReadFile(p)
}

func openDir(root string) *dirArchive {
	d := &dirArchive{root: root}
	filepath.WalkDir(root, func(p string, e os.DirEntry, err error) error {
		if err == nil && !e.IsDir() && e.Name() != ".done" {
			rel, _ := filepath.Rel(root, p)
			d.names = append(d.names, filepath.ToSlash(rel))
		}
		return nil
	})
	sortNatural(d.names)
	return d
}

// 同一个压缩包同时只解压一次；不同的压缩包互不等待
var extractLocks keyedMutex

// 缓存键只看文件内容（大小、修改时间、开头 64KB），文件改名或移动后缓存仍然有效
func cacheKey(path string) string {
	h := sha1.New()
	if f, err := os.Open(path); err == nil {
		st, _ := f.Stat()
		buf := make([]byte, 64<<10)
		n, _ := f.ReadAt(buf, 0)
		fmt.Fprintf(h, "%d|%d|", st.Size(), st.ModTime().Unix())
		h.Write(buf[:n])
		f.Close()
	} else {
		h.Write([]byte(path))
	}
	return hex.EncodeToString(h.Sum(nil))[:20]
}

// rar、7z、tar 不适合随机读取，第一次打开时解压到缓存目录
func extractCached(path, format string) (*dirArchive, error) {
	key := cacheKey(path)
	unlock := extractLocks.Lock(key)
	defer unlock()
	dir := filepath.Join(cfg.DataDir, "cache", "arc", key)
	if _, err := os.Stat(filepath.Join(dir, ".done")); err == nil {
		touchCache(dir)
		return openDir(dir), nil
	}
	os.RemoveAll(dir)
	os.MkdirAll(dir, 0o755)
	var err error
	switch format {
	case "cbr", "rar":
		err = extractRar(path, dir)
	case "cbt":
		err = extractTar(path, dir)
	case "cb7", "7z":
		err = extract7z(path, dir)
	default:
		err = errors.New("不支持的压缩格式")
	}
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	os.WriteFile(filepath.Join(dir, ".done"), nil, 0o644)
	noteCacheWrite()
	return openDir(dir), nil
}

func safeJoin(root, name string) (string, bool) {
	name = strings.ReplaceAll(name, "\\", "/")
	p := filepath.Join(root, filepath.FromSlash(name))
	if !strings.HasPrefix(p, filepath.Clean(root)+string(os.PathSeparator)) {
		return "", false
	}
	return p, true
}

func writeEntry(root, name string, r io.Reader) error {
	p, ok := safeJoin(root, name)
	if !ok {
		return nil
	}
	os.MkdirAll(filepath.Dir(p), 0o755)
	f, err := os.Create(p)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, io.LimitReader(r, 500<<20))
	return err
}

func extractRar(path, dir string) error {
	r, err := rardecode.OpenReader(path)
	if err != nil {
		return err
	}
	defer r.Close()
	for {
		h, err := r.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if h.IsDir {
			continue
		}
		if err := writeEntry(dir, h.Name, r); err != nil {
			return err
		}
	}
}

func extractTar(path, dir string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	tr := tar.NewReader(f)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		if err := writeEntry(dir, h.Name, tr); err != nil {
			return err
		}
	}
}

func extract7z(path, dir string) error {
	bin := ""
	for _, b := range []string{"7z", "7za", "7zz"} {
		if p, err := exec.LookPath(b); err == nil {
			bin = p
			break
		}
	}
	if bin == "" {
		return errors.New("服务器没有安装 7z，无法打开 7z 压缩包")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "x", "-y", "-o"+dir, path).CombinedOutput()
	if ctx.Err() != nil {
		return errors.New("7z 解压超时（10 分钟）")
	}
	if err != nil {
		return fmt.Errorf("7z 解压失败: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

func find7z() string {
	for _, b := range []string{"7z", "7za", "7zz"} {
		if p, err := exec.LookPath(b); err == nil {
			return p
		}
	}
	return ""
}

// ---------- 不解压整包，只看文件列表、读几个文件（入库时取封面和 ComicInfo 用） ----------
// rar、tar 边读边跳过不要的文件，不写硬盘；7z 用 7z l 列目录、7z e -so 取单个文件

func peekArchive(path, format string, pick func(names []string) []string) (names []string, data map[string][]byte, err error) {
	data = map[string][]byte{}
	switch format {
	case "cbr", "rar":
		names, err = rarNames(path)
		if err != nil {
			return
		}
		want := setOf(pick(names))
		if len(want) == 0 {
			return
		}
		r, err2 := rardecode.OpenReader(path)
		if err2 != nil {
			return names, data, err2
		}
		defer r.Close()
		for len(data) < len(want) {
			h, e := r.Next()
			if e != nil {
				break
			}
			if want[h.Name] {
				b, _ := io.ReadAll(io.LimitReader(r, 64<<20))
				data[h.Name] = b
			}
		}
	case "cbt":
		names, err = tarNames(path)
		if err != nil {
			return
		}
		want := setOf(pick(names))
		f, e := os.Open(path)
		if e != nil {
			return names, data, e
		}
		defer f.Close()
		tr := tar.NewReader(f)
		for len(data) < len(want) {
			h, e := tr.Next()
			if e != nil {
				break
			}
			if want[h.Name] {
				b, _ := io.ReadAll(io.LimitReader(tr, 64<<20))
				data[h.Name] = b
			}
		}
	case "cb7", "7z":
		bin := find7z()
		if bin == "" {
			return nil, data, errors.New("服务器没有安装 7z")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		out, e := exec.CommandContext(ctx, bin, "l", "-slt", path).Output()
		if e != nil {
			return nil, data, fmt.Errorf("7z 列目录失败：%v", e)
		}
		var cur string
		isDir := false
		flush := func() {
			if cur != "" && !isDir {
				names = append(names, filepath.ToSlash(cur))
			}
			cur, isDir = "", false
		}
		// 输出开头是压缩包本身的信息，“----------”之后才是里面的文件
		started := false
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimRight(line, "\r")
			if !started {
				started = strings.HasPrefix(line, "----------")
				continue
			}
			switch {
			case strings.HasPrefix(line, "Path = "):
				flush()
				cur = strings.TrimPrefix(line, "Path = ")
			case line == "Folder = +", strings.HasPrefix(line, "Attributes = D"):
				isDir = true
			}
		}
		flush()
		sortNatural(names)
		for _, n := range pick(names) {
			b, e := exec.CommandContext(ctx, bin, "e", "-so", path, filepath.FromSlash(n)).Output()
			if e == nil && len(b) > 0 {
				data[n] = b
			}
		}
	default:
		err = errors.New("不支持的压缩格式")
	}
	sortNatural(names)
	return
}

func setOf(list []string) map[string]bool {
	m := map[string]bool{}
	for _, s := range list {
		m[s] = true
	}
	return m
}

func rarNames(path string) ([]string, error) {
	r, err := rardecode.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	var out []string
	for {
		h, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			if len(out) > 0 {
				break
			}
			return nil, err
		}
		if !h.IsDir {
			out = append(out, h.Name)
		}
	}
	sortNatural(out)
	return out, nil
}

func tarNames(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	tr := tar.NewReader(f)
	var out []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return out, err
		}
		if h.Typeflag == tar.TypeReg {
			out = append(out, h.Name)
		}
	}
	sortNatural(out)
	return out, nil
}

func openArchive(path, format string) (Archive, error) {
	switch format {
	case "cbz", "zip", "epub", "fbz":
		return openZip(path)
	default:
		return extractCached(path, format)
	}
}
