package main

import (
	"database/sql"
	"encoding/json"
	"log"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

var db *sql.DB

const schema = `
CREATE TABLE IF NOT EXISTS libraries (
	id INTEGER PRIMARY KEY,
	name TEXT NOT NULL,
	path TEXT NOT NULL,
	kind TEXT NOT NULL DEFAULT 'book',      -- book | comic
	template TEXT NOT NULL DEFAULT '',
	sort INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS books (
	id INTEGER PRIMARY KEY,
	title TEXT NOT NULL DEFAULT '',
	orig_title TEXT NOT NULL DEFAULT '',
	author TEXT NOT NULL DEFAULT '',
	translator TEXT NOT NULL DEFAULT '',
	series TEXT NOT NULL DEFAULT '',
	series_index REAL NOT NULL DEFAULT 0,
	year INTEGER NOT NULL DEFAULT 0,
	category TEXT NOT NULL DEFAULT '',
	tags TEXT NOT NULL DEFAULT '[]',
	description TEXT NOT NULL DEFAULT '',
	isbn TEXT NOT NULL DEFAULT '',
	language TEXT NOT NULL DEFAULT '',
	publisher TEXT NOT NULL DEFAULT '',
	kind TEXT NOT NULL DEFAULT 'book',      -- book | comic
	direction TEXT NOT NULL DEFAULT '',     -- rtl 表示日漫从右到左
	status TEXT NOT NULL DEFAULT 'ok',      -- ok | review
	confidence REAL NOT NULL DEFAULT 0,
	source TEXT NOT NULL DEFAULT '',        -- 识别来源说明
	orig_name TEXT NOT NULL DEFAULT '',     -- 入库时的原始文件名
	cover TEXT NOT NULL DEFAULT '',
	search_key TEXT NOT NULL DEFAULT '',
	sort_key TEXT NOT NULL DEFAULT '',
	author_key TEXT NOT NULL DEFAULT '',
	read_state TEXT NOT NULL DEFAULT 'unread', -- unread | reading | done
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS files (
	id INTEGER PRIMARY KEY,
	book_id INTEGER NOT NULL REFERENCES books(id) ON DELETE CASCADE,
	library_id INTEGER NOT NULL DEFAULT 0,
	path TEXT NOT NULL UNIQUE,
	format TEXT NOT NULL,
	size INTEGER NOT NULL DEFAULT 0,
	hash TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_files_book ON files(book_id);
CREATE INDEX IF NOT EXISTS idx_files_hash ON files(hash);
CREATE INDEX IF NOT EXISTS idx_books_status ON books(status);
CREATE INDEX IF NOT EXISTS idx_books_created ON books(created_at);
CREATE TABLE IF NOT EXISTS progress (
	book_id INTEGER PRIMARY KEY REFERENCES books(id) ON DELETE CASCADE,
	file_id INTEGER NOT NULL DEFAULT 0,
	location TEXT NOT NULL DEFAULT '',
	percent REAL NOT NULL DEFAULT 0,
	device TEXT NOT NULL DEFAULT '',
	updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS bookmarks (
	id INTEGER PRIMARY KEY,
	book_id INTEGER NOT NULL REFERENCES books(id) ON DELETE CASCADE,
	location TEXT NOT NULL,
	label TEXT NOT NULL DEFAULT '',
	text TEXT NOT NULL DEFAULT '',
	note TEXT NOT NULL DEFAULT '',
	color TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS ops (
	id INTEGER PRIMARY KEY,
	batch TEXT NOT NULL,
	label TEXT NOT NULL DEFAULT '',
	from_path TEXT NOT NULL,
	to_path TEXT NOT NULL,
	file_id INTEGER NOT NULL DEFAULT 0,
	undone INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_ops_batch ON ops(batch);
CREATE TABLE IF NOT EXISTS events (
	id INTEGER PRIMARY KEY,
	level TEXT NOT NULL,      -- info | warn | error
	message TEXT NOT NULL,
	book_id INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS sessions (token TEXT PRIMARY KEY, device TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, last_seen INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS ignored_suggestions (key TEXT PRIMARY KEY, created_at INTEGER NOT NULL);
`

func openDB(path string) error {
	var err error
	// _txlock=immediate：事务一开始就拿写锁，不会读到一半升级写锁失败
	db, err = sql.Open("sqlite3", "file:"+path+"?_busy_timeout=30000&_journal_mode=WAL&_foreign_keys=on&_synchronous=NORMAL&_txlock=immediate")
	if err != nil {
		return err
	}
	// WAL 模式下读和写可以同时进行。以前只给 1 个连接，入库、建全文索引占着连接时，
	// 网页的查询全都排队，书多的时候整个书库像卡死一样
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	if _, err = db.Exec(schema); err != nil {
		return err
	}
	migrate()
	return nil
}

func now() int64 { return time.Now().Unix() }

func getSetting(key, def string) string {
	var v string
	if err := db.QueryRow(`SELECT value FROM settings WHERE key=?`, key).Scan(&v); err != nil {
		return def
	}
	return v
}

func setSetting(key, value string) {
	db.Exec(`INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
}

func logEvent(level string, bookID int64, msg string) {
	log.Printf("[%s] %s", level, msg)
	db.Exec(`INSERT INTO events(level,message,book_id,created_at) VALUES(?,?,?,?)`, level, msg, bookID, now())
	// 只保留最近 2000 条
	db.Exec(`DELETE FROM events WHERE id < (SELECT MAX(id) - 2000 FROM events)`)
}

type Library struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Path     string `json:"path"`
	Kind     string `json:"kind"`
	Template string `json:"template"`
}

const defaultBookTemplate = "{分类}/{作者}/{书名}{ (年份)}/{书名} - {作者}"
const defaultSeriesTemplate = "{分类}/{作者}/{系列}/{序号:02} {书名}"
const defaultComicTemplate = "{系列}/{系列} 第{序号:03}卷"

func initLibraries() {
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM libraries`).Scan(&n)
	if n > 0 {
		return
	}
	for i, part := range strings.Split(cfg.InitLibraries, ",") {
		f := strings.Split(strings.TrimSpace(part), ":")
		if len(f) < 2 {
			continue
		}
		kind := "book"
		if len(f) >= 3 && f[2] == "comic" {
			kind = "comic"
		}
		tpl := defaultBookTemplate
		if kind == "comic" {
			tpl = defaultComicTemplate
		}
		db.Exec(`INSERT INTO libraries(name,path,kind,template,sort) VALUES(?,?,?,?,?)`, f[0], f[1], kind, tpl, i)
	}
}

func listLibraries() []Library {
	rows, err := db.Query(`SELECT id,name,path,kind,template FROM libraries ORDER BY sort,id`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Library
	for rows.Next() {
		var l Library
		rows.Scan(&l.ID, &l.Name, &l.Path, &l.Kind, &l.Template)
		out = append(out, l)
	}
	return out
}

func libraryByID(id int64) *Library {
	for _, l := range listLibraries() {
		if l.ID == id {
			l := l
			return &l
		}
	}
	return nil
}

// 默认书库：按 kind 找第一个匹配的
func libraryFor(kind string) *Library {
	libs := listLibraries()
	for _, l := range libs {
		if l.Kind == kind {
			l := l
			return &l
		}
	}
	if len(libs) > 0 {
		return &libs[0]
	}
	return nil
}

func toJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
