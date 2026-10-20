package main

// 数据库备份：每天自动一份（留 7 份），升级前一份，也可以在设置页手动备份、下载、上传恢复。
// 备份用 VACUUM INTO：在线做一份完整、一致的副本（包括 WAL 里还没合并的改动），不影响正常使用。
// 只备份 shuku.db（进度、书签笔记、统计、书目信息、设置、账号）；全文索引 fulltext.db 能重建，不备份。

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

func backupDir() string { return filepath.Join(filepath.Dir(dbPath), "backup") }

var backupMu sync.Mutex

// 备份文件名：shuku-20261018-031500-daily.db；类型：daily 每天、upgrade 升级前、manual 手动、delete 删账号删书库前、restore 恢复前
var backupNameRe = regexp.MustCompile(`^shuku-(\d{8}-\d{6})-([a-z0-9-]+)\.db$`)

func backupDB(kind string) (string, error) {
	backupMu.Lock()
	defer backupMu.Unlock()
	if err := os.MkdirAll(backupDir(), 0o755); err != nil {
		return "", err
	}
	kind = strings.ToLower(regexp.MustCompile(`[^a-zA-Z0-9-]`).ReplaceAllString(kind, ""))
	if kind == "" {
		kind = "manual"
	}
	p := filepath.Join(backupDir(), fmt.Sprintf("shuku-%s-%s.db", time.Now().Format("20060102-150405"), kind))
	tmp := p + ".part"
	os.Remove(tmp)
	if _, err := db.Exec(`VACUUM main INTO ?`, tmp); err != nil {
		os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, p); err != nil {
		os.Remove(tmp)
		return "", err
	}
	pruneBackups()
	return p, nil
}

type backupInfo struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Size    int64  `json:"size"`
	Created int64  `json:"created_at"`
}

func listBackups() []backupInfo {
	entries, _ := os.ReadDir(backupDir())
	out := []backupInfo{}
	for _, e := range entries {
		m := backupNameRe.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		t, _ := time.ParseInLocation("20060102-150405", m[1], time.Local)
		kind := m[2]
		if strings.HasPrefix(kind, "upgrade") {
			kind = "upgrade"
		}
		out = append(out, backupInfo{Name: e.Name(), Kind: kind, Size: info.Size(), Created: t.Unix()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created > out[j].Created })
	return out
}

// 每天的留 7 份，升级前、手动、删除前的各留 5 份，恢复前的留 3 份
// （删账号、删书库前自动做的备份单独算 delete，不挤掉用户自己点的手动备份）
func pruneBackups() {
	keep := map[string]int{"daily": 7, "upgrade": 5, "manual": 5, "delete": 5, "restore": 3}
	seen := map[string]int{}
	for _, b := range listBackups() {
		seen[b.Kind]++
		max, ok := keep[b.Kind]
		if !ok {
			max = 5
		}
		if seen[b.Kind] > max {
			os.Remove(filepath.Join(backupDir(), b.Name))
		}
	}
}

// 每小时看一次：最新的每日备份超过 20 小时就再做一份（库里有书才做）
func backupLoop(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(time.Minute):
	}
	for {
		var books int
		db.QueryRow(`SELECT COUNT(*) FROM books`).Scan(&books)
		if books > 0 {
			var last int64
			for _, b := range listBackups() {
				if b.Kind == "daily" && b.Created > last {
					last = b.Created
				}
			}
			if time.Since(time.Unix(last, 0)) > 20*time.Hour {
				if _, err := backupDB("daily"); err != nil {
					logEvent("error", 0, "每日备份失败："+err.Error())
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Hour):
		}
	}
}

// ---------- 恢复：先放到 restore.db，重启时换上（当前的数据库先挪进备份文件夹） ----------

func restorePath() string { return filepath.Join(filepath.Dir(dbPath), "restore.db") }

// 启动时（打开数据库之前）调用
func restorePending(path string) {
	rp := filepath.Join(filepath.Dir(path), "restore.db")
	if _, err := os.Stat(rp); err != nil {
		return
	}
	dir := filepath.Join(filepath.Dir(path), "backup")
	os.MkdirAll(dir, 0o755)
	if _, err := os.Stat(path); err == nil {
		// 数据库三个文件一起挪走（-wal 里可能有还没合并的改动），以后还能打开
		base := filepath.Join(dir, fmt.Sprintf("shuku-%s-restore.db", time.Now().Format("20060102-150405")))
		for _, suf := range []string{"", "-wal", "-shm"} {
			if _, err := os.Stat(path + suf); err == nil {
				if err := os.Rename(path+suf, base+suf); err != nil {
					log.Printf("恢复备份：挪走当前数据库失败，这次不恢复：%v", err)
					return
				}
			}
		}
	}
	if err := os.Rename(rp, path); err != nil {
		log.Printf("恢复备份失败：%v", err)
		return
	}
	log.Printf("已恢复备份的数据库，原来的数据库挪到了 %s", dir)
}

// 检查一个文件是不是书库的数据库
func checkBackupFile(p string) error {
	tdb, err := sql.Open("sqlite3", "file:"+p+"?mode=ro")
	if err != nil {
		return err
	}
	defer tdb.Close()
	var res string
	if err := tdb.QueryRow(`PRAGMA quick_check`).Scan(&res); err != nil {
		return fmt.Errorf("不是数据库文件：%v", err)
	}
	if res != "ok" {
		return fmt.Errorf("数据库文件已损坏：%s", res)
	}
	var n int
	tdb.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN ('books','files','settings')`).Scan(&n)
	if n < 3 {
		return fmt.Errorf("这不是书库的数据库")
	}
	return nil
}

// ---------- 接口（管理员） ----------

func handleBackups(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"backups": listBackups(), "dir": backupDir()})
}

func handleCreateBackup(w http.ResponseWriter, r *http.Request) {
	p, err := backupDB("manual")
	if err != nil {
		errJSON(w, 500, "备份失败："+err.Error())
		return
	}
	logEvent("info", 0, "已手动备份数据库："+filepath.Base(p))
	writeJSON(w, 200, map[string]any{"ok": true, "name": filepath.Base(p)})
}

func backupByName(name string) (string, bool) {
	if !backupNameRe.MatchString(name) {
		return "", false
	}
	p := filepath.Join(backupDir(), name)
	if _, err := os.Stat(p); err != nil {
		return "", false
	}
	return p, true
}

func handleDownloadBackup(w http.ResponseWriter, r *http.Request) {
	p, ok := backupByName(r.PathValue("name"))
	if !ok {
		errJSON(w, 404, "没有这个备份")
		return
	}
	w.Header().Set("Content-Type", "application/vnd.sqlite3")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filepath.Base(p)+`"`)
	http.ServeFile(w, r, p)
}

func handleDeleteBackup(w http.ResponseWriter, r *http.Request) {
	p, ok := backupByName(r.PathValue("name"))
	if !ok {
		errJSON(w, 404, "没有这个备份")
		return
	}
	os.Remove(p)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// 恢复：?name= 用已有的备份，否则请求体是上传的数据库文件。检查没问题后放到 restore.db，然后重启
func handleRestoreBackup(w http.ResponseWriter, r *http.Request) {
	tmp := restorePath() + ".part"
	os.Remove(tmp)
	if name := r.URL.Query().Get("name"); name != "" {
		p, ok := backupByName(name)
		if !ok {
			errJSON(w, 404, "没有这个备份")
			return
		}
		if err := copyFile(p, tmp); err != nil {
			errJSON(w, 500, err.Error())
			return
		}
	} else {
		out, err := os.Create(tmp)
		if err != nil {
			errJSON(w, 500, err.Error())
			return
		}
		_, err = io.Copy(out, io.LimitReader(r.Body, 8<<30))
		out.Close()
		if err != nil {
			os.Remove(tmp)
			errJSON(w, 400, "上传中断："+err.Error())
			return
		}
	}
	if err := checkBackupFile(tmp); err != nil {
		os.Remove(tmp)
		errJSON(w, 400, err.Error())
		return
	}
	if err := os.Rename(tmp, restorePath()); err != nil {
		os.Remove(tmp)
		errJSON(w, 500, err.Error())
		return
	}
	logEvent("warn", 0, "已准备恢复备份，书库马上重启")
	writeJSON(w, 200, map[string]any{"ok": true, "restart": restartAllowed()})
	if restartAllowed() {
		go func() {
			time.Sleep(time.Second)
			requestRestart()
		}()
	}
}

// 容器里退出后 Docker 会自动重启（compose 里是 restart: unless-stopped）。测试时设 SHUKU_NO_RESTART=1
func restartAllowed() bool { return env("SHUKU_NO_RESTART", "") == "" }

var restartHook func()

func requestRestart() {
	log.Printf("书库重启中…")
	if restartHook != nil {
		restartHook()
		return
	}
	os.Exit(0)
}

func copyFile(from, to string) error {
	src, err := os.Open(from)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.Create(to)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		return err
	}
	if err := dst.Sync(); err != nil {
		dst.Close()
		return err
	}
	return dst.Close()
}
