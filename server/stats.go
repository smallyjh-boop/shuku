package main

// 阅读统计：每天每本书读了多少秒；读完时间

import (
	"net/http"
	"time"
)

// 数据库升级：给旧库补列、补表（重复执行没有副作用）
var migrations = []string{
	`ALTER TABLE books ADD COLUMN finished_at INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE books ADD COLUMN enriched_at INTEGER NOT NULL DEFAULT 0`,
	`CREATE TABLE IF NOT EXISTS reading_log (day TEXT NOT NULL, book_id INTEGER NOT NULL, seconds INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(day, book_id))`,
	`CREATE INDEX IF NOT EXISTS idx_reading_log_book ON reading_log(book_id)`,
}

func migrate() {
	for _, m := range migrations {
		db.Exec(m) // 列已存在会报错，忽略
	}
	migrateFulltext()
}

func today() string { return time.Now().Format("2006-01-02") }

// 记录阅读时长：客户端每次上报进度时带上这段时间实际阅读的秒数
func addReadingTime(bookID int64, seconds int) {
	if seconds <= 0 {
		return
	}
	if seconds > 600 {
		seconds = 600 // 两次上报之间最多算 10 分钟，防止挂机
	}
	db.Exec(`INSERT INTO reading_log(day,book_id,seconds) VALUES(?,?,?) ON CONFLICT(day,book_id) DO UPDATE SET seconds=seconds+excluded.seconds`, today(), bookID, seconds)
}

type dayStat struct {
	Day     string `json:"day"`
	Seconds int    `json:"seconds"`
}

type bookStat struct {
	ID      int64   `json:"id"`
	Title   string  `json:"title"`
	Author  string  `json:"author"`
	Cover   string  `json:"cover"`
	Kind    string  `json:"kind"`
	Seconds int     `json:"seconds"`
	Percent float64 `json:"percent"`
	At      int64   `json:"at"`
}

func handleStats(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	sum := func(from string) int {
		var n int
		db.QueryRow(`SELECT COALESCE(SUM(seconds),0) FROM reading_log WHERE day>=?`, from).Scan(&n)
		return n
	}
	weekday := int(now.Weekday())
	if weekday == 0 {
		weekday = 7
	}
	weekStart := now.AddDate(0, 0, 1-weekday).Format("2006-01-02")
	monthStart := now.Format("2006-01") + "-01"
	yearStart := now.Format("2006") + "-01-01"

	// 最近 90 天每天的时长
	byDay := map[string]int{}
	rows, err := db.Query(`SELECT day,SUM(seconds) FROM reading_log WHERE day>=? GROUP BY day`, now.AddDate(0, 0, -89).Format("2006-01-02"))
	if err == nil {
		for rows.Next() {
			var d string
			var s int
			rows.Scan(&d, &s)
			byDay[d] = s
		}
		rows.Close()
	}
	days := make([]dayStat, 0, 90)
	for i := 89; i >= 0; i-- {
		d := now.AddDate(0, 0, -i).Format("2006-01-02")
		days = append(days, dayStat{d, byDay[d]})
	}
	// 连续阅读天数（今天还没读不打断，从昨天往前数）
	streak := 0
	for i := 0; i < 3650; i++ {
		d := now.AddDate(0, 0, -i).Format("2006-01-02")
		s, ok := byDay[d]
		if !ok && i >= 90 {
			db.QueryRow(`SELECT COALESCE(SUM(seconds),0) FROM reading_log WHERE day=?`, d).Scan(&s)
		}
		if s >= 60 {
			streak++
		} else if i > 0 {
			break
		}
	}
	var total, finishedYear, booksRead int
	db.QueryRow(`SELECT COALESCE(SUM(seconds),0), COUNT(DISTINCT book_id) FROM reading_log`).Scan(&total, &booksRead)
	yearStartTS, _ := time.ParseInLocation("2006-01-02", yearStart, time.Local)
	db.QueryRow(`SELECT COUNT(*) FROM books WHERE finished_at>=?`, yearStartTS.Unix()).Scan(&finishedYear)

	top := []bookStat{}
	rows, err = db.Query(`SELECT b.id,b.title,b.author,b.cover,b.kind,SUM(l.seconds) s,COALESCE(p.percent,0) FROM reading_log l JOIN books b ON b.id=l.book_id LEFT JOIN progress p ON p.book_id=b.id WHERE l.day>=? GROUP BY b.id ORDER BY s DESC LIMIT 8`, now.AddDate(0, 0, -29).Format("2006-01-02"))
	if err == nil {
		for rows.Next() {
			var b bookStat
			rows.Scan(&b.ID, &b.Title, &b.Author, &b.Cover, &b.Kind, &b.Seconds, &b.Percent)
			top = append(top, b)
		}
		rows.Close()
	}
	finished := []bookStat{}
	rows, err = db.Query(`SELECT b.id,b.title,b.author,b.cover,b.kind,COALESCE((SELECT SUM(seconds) FROM reading_log WHERE book_id=b.id),0),b.finished_at FROM books b WHERE b.finished_at>0 ORDER BY b.finished_at DESC LIMIT 12`)
	if err == nil {
		for rows.Next() {
			var b bookStat
			rows.Scan(&b.ID, &b.Title, &b.Author, &b.Cover, &b.Kind, &b.Seconds, &b.At)
			b.Percent = 1
			finished = append(finished, b)
		}
		rows.Close()
	}
	writeJSON(w, 200, map[string]any{
		"today": byDay[today()], "week": sum(weekStart), "month": sum(monthStart), "year": sum(yearStart),
		"total": total, "books_read": booksRead, "finished_year": finishedYear, "streak": streak,
		"days": days, "top": top, "finished": finished,
	})
}
