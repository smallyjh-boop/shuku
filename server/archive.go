package main

import (
	"archive/tar"
	"archive/zip"
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
	"sync"
	"unicode"

	"github.com/nwaples/rardecode/v2"
)

// 漫画压缩包统一接口：列出图片、读取任意文件
type Archive interface {
	Names() []string // 全部文件（排好序）
	Read(name string) ([]byte, error)
	Close() error
}

func images(a Archive) []string {
	var out []string
	for _, n := range a.Names() {
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

var extractMu sync.Mutex

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
	extractMu.Lock()
	defer extractMu.Unlock()
	dir := filepath.Join(cfg.DataDir, "cache", "arc", cacheKey(path))
	if _, err := os.Stat(filepath.Join(dir, ".done")); err == nil {
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
	out, err := exec.Command(bin, "x", "-y", "-o"+dir, path).CombinedOutput()
	if err != nil {
		return fmt.Errorf("7z 解压失败: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

func openArchive(path, format string) (Archive, error) {
	switch format {
	case "cbz", "zip", "epub", "fbz":
		return openZip(path)
	default:
		return extractCached(path, format)
	}
}
