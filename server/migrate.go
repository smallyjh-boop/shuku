package main

// 数据库升级：版本号存在 settings 的 schema_version。每个版本一组改动，放在一个事务里执行，
// 要么全部成功、要么一点不改；升级前先备份一份（backup/shuku-日期-upgrade-v版本.db）。
// “列已存在”“表已存在”这类错误说明以前已经改过，忽略；其他错误停下不启动，免得带着半截数据跑。

import (
	"database/sql"
	"database/sql/driver"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"

	"github.com/mattn/go-sqlite3"
)

type migration struct {
	v     int
	name  string
	stmts []string
}

var migrations = []migration{
	{1, "阅读统计", []string{
		`ALTER TABLE books ADD COLUMN finished_at INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE books ADD COLUMN enriched_at INTEGER NOT NULL DEFAULT 0`,
		`CREATE TABLE IF NOT EXISTS reading_log (day TEXT NOT NULL, book_id INTEGER NOT NULL, seconds INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(day, book_id))`,
		`CREATE INDEX IF NOT EXISTS idx_reading_log_book ON reading_log(book_id)`,
	}},
	// 家人账号：进度、书签笔记、阅读统计、读完状态都按人分开。以前的数据归管理员（id=1）
	{2, "家人账号", []string{
		`CREATE TABLE IF NOT EXISTS users (id INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE, pass TEXT NOT NULL DEFAULT '', role TEXT NOT NULL DEFAULT 'member', libraries TEXT NOT NULL DEFAULT '[]', created_at INTEGER NOT NULL)`,
		`INSERT OR IGNORE INTO users(id,name,pass,role,libraries,created_at) SELECT 1,'管理员',value,'admin','[]',CAST(strftime('%s','now') AS INTEGER) FROM settings WHERE key='password_hash' AND value<>''`,
		`ALTER TABLE sessions ADD COLUMN user_id INTEGER NOT NULL DEFAULT 1`,
		`CREATE TABLE progress_new (user_id INTEGER NOT NULL, book_id INTEGER NOT NULL REFERENCES books(id) ON DELETE CASCADE, file_id INTEGER NOT NULL DEFAULT 0, location TEXT NOT NULL DEFAULT '', percent REAL NOT NULL DEFAULT 0, device TEXT NOT NULL DEFAULT '', updated_at INTEGER NOT NULL, PRIMARY KEY(user_id, book_id))`,
		`INSERT INTO progress_new(user_id,book_id,file_id,location,percent,device,updated_at) SELECT 1,book_id,file_id,location,percent,device,updated_at FROM progress`,
		`DROP TABLE progress`,
		`ALTER TABLE progress_new RENAME TO progress`,
		`CREATE INDEX IF NOT EXISTS idx_progress_book ON progress(book_id)`,
		`ALTER TABLE bookmarks ADD COLUMN user_id INTEGER NOT NULL DEFAULT 1`,
		`ALTER TABLE bookmarks ADD COLUMN kind TEXT NOT NULL DEFAULT 'bookmark'`,
		`ALTER TABLE bookmarks ADD COLUMN updated_at INTEGER NOT NULL DEFAULT 0`,
		`CREATE INDEX IF NOT EXISTS idx_bookmarks_book ON bookmarks(book_id, user_id)`,
		`CREATE TABLE reading_log_new (user_id INTEGER NOT NULL, day TEXT NOT NULL, book_id INTEGER NOT NULL, seconds INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(user_id, day, book_id))`,
		`INSERT INTO reading_log_new(user_id,day,book_id,seconds) SELECT 1,day,book_id,seconds FROM reading_log`,
		`DROP TABLE reading_log`,
		`ALTER TABLE reading_log_new RENAME TO reading_log`,
		`CREATE INDEX IF NOT EXISTS idx_reading_log_book ON reading_log(book_id)`,
		`CREATE INDEX IF NOT EXISTS idx_reading_log_user_day ON reading_log(user_id, day)`,
		`CREATE TABLE IF NOT EXISTS user_books (user_id INTEGER NOT NULL, book_id INTEGER NOT NULL REFERENCES books(id) ON DELETE CASCADE, read_state TEXT NOT NULL DEFAULT 'unread', finished_at INTEGER NOT NULL DEFAULT 0, rating INTEGER NOT NULL DEFAULT 0, want INTEGER NOT NULL DEFAULT 0, prefs TEXT NOT NULL DEFAULT '', updated_at INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(user_id, book_id))`,
		`INSERT OR IGNORE INTO user_books(user_id,book_id,read_state,finished_at,updated_at) SELECT 1,id,read_state,finished_at,updated_at FROM books WHERE read_state<>'unread' OR finished_at>0`,
		`CREATE INDEX IF NOT EXISTS idx_user_books_book ON user_books(book_id)`,
	}},
	// 删书先放回收站（连进度、笔记一起能恢复）；扫描时找不到的文件先标记，不马上删
	{3, "回收站和找不到的文件", []string{
		`ALTER TABLE books ADD COLUMN deleted_at INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE books ADD COLUMN deleted_batch TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE files ADD COLUMN missing_since INTEGER NOT NULL DEFAULT 0`,
		`CREATE INDEX IF NOT EXISTS idx_books_deleted ON books(deleted_at)`,
		`CREATE INDEX IF NOT EXISTS idx_books_series ON books(series)`,
		`CREATE TABLE IF NOT EXISTS op_meta (id INTEGER PRIMARY KEY, batch TEXT NOT NULL, book_id INTEGER NOT NULL, field TEXT NOT NULL, old TEXT NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS idx_op_meta_batch ON op_meta(batch)`,
	}},
}

func schemaVersion() int {
	v, _ := strconv.Atoi(getSetting("schema_version", "0"))
	return v
}

func latestSchema() int { return migrations[len(migrations)-1].v }

// 以前升级过的痕迹：重复执行时会报这些错，可以忽略
func ignorableMigrationErr(err error) bool {
	s := err.Error()
	return strings.Contains(s, "duplicate column name") || strings.Contains(s, "already exists")
}

func migrate() error {
	cur := schemaVersion()
	if cur >= latestSchema() {
		return nil
	}
	var books int
	db.QueryRow(`SELECT COUNT(*) FROM books`).Scan(&books)
	if books > 0 {
		if p, err := backupDB(fmt.Sprintf("upgrade-v%d", latestSchema())); err != nil {
			return fmt.Errorf("升级前备份数据库失败（数据库没有改动）：%v", err)
		} else {
			log.Printf("数据库升级前已备份：%s", p)
		}
	}
	for _, m := range migrations {
		if m.v <= cur {
			continue
		}
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		for i, s := range m.stmts {
			if _, err := tx.Exec(s); err != nil && !ignorableMigrationErr(err) {
				tx.Rollback()
				return fmt.Errorf("数据库升级到第 %d 版（%s）的第 %d 步失败，数据库没有改动：%v", m.v, m.name, i+1, err)
			}
		}
		if _, err := tx.Exec(`INSERT INTO settings(key,value) VALUES('schema_version',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, strconv.Itoa(m.v)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("数据库升级到第 %d 版（%s）失败：%v", m.v, m.name, err)
		}
		log.Printf("数据库已升级到第 %d 版：%s", m.v, m.name)
	}
	return nil
}

// ---------- 全文索引放在单独的 fulltext.db ----------
// 全文索引很大（每个字约 6 字节，网文多了能到十几 GB），而且随时能重建：
// 和进度、设置分开放，备份只备 shuku.db，索引坏了删掉 fulltext.db 就行。
// 每个数据库连接打开时把它挂成 fts，表名不带前缀时 SQLite 会自动找到。

var ftDBPath string

var registerDriver sync.Once

func shukuDriver() string {
	registerDriver.Do(func() {
		sql.Register("sqlite3_shuku", &sqlite3.SQLiteDriver{
			ConnectHook: func(c *sqlite3.SQLiteConn) error {
				if ftDBPath == "" {
					return nil
				}
				if _, err := c.Exec(`ATTACH DATABASE ? AS fts`, []driver.Value{ftDBPath}); err != nil {
					return err
				}
				c.Exec(`PRAGMA fts.journal_mode=WAL`, nil)
				c.Exec(`PRAGMA fts.synchronous=NORMAL`, nil)
				return nil
			},
		})
	})
	return "sqlite3_shuku"
}
