package main

// 打开着的漫画压缩包：保留最近用过的几个，翻页时不用每页重新打开。
// 每个压缩包单独加锁：打开（rar、7z 要先解压）一个大压缩包时，别的漫画照样能翻页。

import (
	"bytes"
	"image"
	"path/filepath"
	"sync"
)

// 漫画页列表缓存（页名 + 每页宽高，前端据此判断跨页大图）
type comicInfo struct {
	names []string
	dims  [][2]int
}

var pageCache = struct {
	sync.Mutex
	m map[int64]*comicInfo
}{m: map[int64]*comicInfo{}}

type pooledArc struct {
	a   Archive
	dir string // rar、7z 解压到的缓存文件夹（清理缓存时不能删正在用的）
	mu  sync.Mutex
}

var arcPool = struct {
	sync.Mutex
	m     map[int64]*pooledArc
	order []int64
}{m: map[int64]*pooledArc{}}

// 同一个文件同时只打开一次（第二个请求等第一个打开好）
var arcOpening keyedMutex

func pooledArchive(f *File) (*pooledArc, error) {
	arcPool.Lock()
	if p, ok := arcPool.m[f.ID]; ok {
		arcPool.Unlock()
		return p, nil
	}
	arcPool.Unlock()
	unlock := arcOpening.Lock(f.ID)
	defer unlock()
	arcPool.Lock()
	if p, ok := arcPool.m[f.ID]; ok { // 等锁的时候别人已经打开了
		arcPool.Unlock()
		return p, nil
	}
	arcPool.Unlock()
	a, err := openArchive(f.Path, f.Format)
	if err != nil {
		return nil, err
	}
	p := &pooledArc{a: a}
	if d, ok := a.(*dirArchive); ok {
		p.dir = d.root
		touchCache(d.root)
	}
	arcPool.Lock()
	arcPool.m[f.ID] = p
	arcPool.order = append(arcPool.order, f.ID)
	var old *pooledArc
	if len(arcPool.order) > 6 {
		id := arcPool.order[0]
		arcPool.order = arcPool.order[1:]
		old = arcPool.m[id]
		delete(arcPool.m, id)
	}
	arcPool.Unlock()
	if old != nil {
		old.mu.Lock()
		old.a.Close()
		old.mu.Unlock()
	}
	return p, nil
}

func closePooledArchive(fileID int64) {
	arcPool.Lock()
	p, ok := arcPool.m[fileID]
	delete(arcPool.m, fileID)
	for i, id := range arcPool.order {
		if id == fileID {
			arcPool.order = append(arcPool.order[:i], arcPool.order[i+1:]...)
			break
		}
	}
	arcPool.Unlock()
	if ok {
		p.mu.Lock()
		p.a.Close()
		p.mu.Unlock()
	}
}

// 这个解压出来的文件夹是不是正在用
func archiveInUse(dir string) bool {
	dir = filepath.Clean(dir)
	arcPool.Lock()
	defer arcPool.Unlock()
	for _, p := range arcPool.m {
		if p.dir != "" && filepath.Clean(p.dir) == dir {
			return true
		}
	}
	return false
}

func readArchivePage(f *File, name string) ([]byte, error) {
	p, err := pooledArchive(f)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.a.Read(name)
}

// 只读开头一段：看图片的宽高用不着读完整张图
type headReader interface {
	ReadHead(name string, n int) ([]byte, error)
}

func comicPagesInfo(f *File) (*comicInfo, error) {
	pageCache.Lock()
	if p, ok := pageCache.m[f.ID]; ok {
		pageCache.Unlock()
		return p, nil
	}
	pageCache.Unlock()
	p, err := pooledArchive(f)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	imgs := images(p.a)
	info := &comicInfo{names: imgs, dims: make([][2]int, len(imgs))}
	hr, _ := p.a.(headReader)
	for i, n := range imgs {
		var b []byte
		var err error
		if hr != nil {
			b, err = hr.ReadHead(n, 256<<10)
		} else {
			b, err = p.a.Read(n)
		}
		if err != nil {
			continue
		}
		if c, _, err := image.DecodeConfig(bytes.NewReader(b)); err == nil {
			info.dims[i] = [2]int{c.Width, c.Height}
		} else if hr != nil {
			// 开头不够判断（少数格式把尺寸放在后面）：读整张
			if full, err := p.a.Read(n); err == nil {
				if c, _, err := image.DecodeConfig(bytes.NewReader(full)); err == nil {
					info.dims[i] = [2]int{c.Width, c.Height}
				}
			}
		}
	}
	p.mu.Unlock()
	pageCache.Lock()
	if len(pageCache.m) > 200 {
		pageCache.m = map[int64]*comicInfo{}
	}
	pageCache.m[f.ID] = info
	pageCache.Unlock()
	return info, nil
}

func comicPages(f *File) ([]string, error) {
	info, err := comicPagesInfo(f)
	if err != nil {
		return nil, err
	}
	return info.names, nil
}

// ---------- 按键加锁：同一个键同时只有一个在做，不同键互不影响 ----------

type keyedMutex struct {
	mu sync.Mutex
	m  map[any]*keyedEntry
}

type keyedEntry struct {
	mu   sync.Mutex
	refs int
}

func (k *keyedMutex) Lock(key any) func() {
	k.mu.Lock()
	if k.m == nil {
		k.m = map[any]*keyedEntry{}
	}
	e := k.m[key]
	if e == nil {
		e = &keyedEntry{}
		k.m[key] = e
	}
	e.refs++
	k.mu.Unlock()
	e.mu.Lock()
	return func() {
		e.mu.Unlock()
		k.mu.Lock()
		e.refs--
		if e.refs == 0 {
			delete(k.m, key)
		}
		k.mu.Unlock()
	}
}
