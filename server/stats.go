package main

// 阅读统计：每天每本书读了多少秒；读完时间

import (
	"net/http"
	"time"
)

func today() string { return time.Now().Format("2006-01-02") }

// 记录阅读时长：客户端每次上报进度时带上这段时间实际阅读的秒数。
// 一次最多算 4 小时（手机离线读了很久，联网后一次补传）；一本书一天最多算 16 小时，防止挂机、重复上传
func addReadingTime(uid, bookID int64, seconds int) error {
	if seconds <= 0 {
		return nil
	}
	if seconds > 4*3600 {
		seconds = 4 * 3600
	}
	_, err := db.Exec(`INSERT INTO reading_log(user_id,day,book_id,seconds) VALUES(?,?,?,?) ON CONFLICT(user_id,day,book_id) DO UPDATE SET seconds=MIN(seconds+excluded.seconds, 57600)`, uid, today(), bookID, seconds)
	return err
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
	uid := meID(r)
	now := time.Now()
	sum := func(from string) int {
		var n int
		db.QueryRow(`SELECT COALESCE(SUM(seconds),0) FROM reading_log WHERE user_id=? AND day>=?`, uid, from).Scan(&n)
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
	rows, err := db.Query(`SELECT day,SUM(seconds) FROM reading_log WHERE user_id=? AND day>=? GROUP BY day`, uid, now.AddDate(0, 0, -89).Format("2006-01-02"))
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
			db.QueryRow(`SELECT COALESCE(SUM(seconds),0) FROM reading_log WHERE user_id=? AND day=?`, uid, d).Scan(&s)
		}
		if s >= 60 {
			streak++
		} else if i > 0 {
			break
		}
	}
	var total, finishedYear, booksRead int
	db.QueryRow(`SELECT COALESCE(SUM(seconds),0), COUNT(DISTINCT book_id) FROM reading_log WHERE user_id=?`, uid).Scan(&total, &booksRead)
	yearStartTS, _ := time.ParseInLocation("2006-01-02", yearStart, time.Local)
	db.QueryRow(`SELECT COUNT(*) FROM user_books ub JOIN books b ON b.id=ub.book_id WHERE ub.user_id=? AND ub.finished_at>=? AND b.deleted_at=0`, uid, yearStartTS.Unix()).Scan(&finishedYear)

	top := []bookStat{}
	rows, err = db.Query(`SELECT b.id,b.title,b.author,b.cover,b.kind,SUM(l.seconds) s,COALESCE(p.percent,0) FROM reading_log l JOIN books b ON b.id=l.book_id LEFT JOIN progress p ON p.book_id=b.id AND p.user_id=l.user_id WHERE l.user_id=? AND l.day>=? AND b.deleted_at=0 GROUP BY b.id ORDER BY s DESC LIMIT 8`, uid, now.AddDate(0, 0, -29).Format("2006-01-02"))
	if err == nil {
		for rows.Next() {
			var b bookStat
			rows.Scan(&b.ID, &b.Title, &b.Author, &b.Cover, &b.Kind, &b.Seconds, &b.Percent)
			top = append(top, b)
		}
		rows.Close()
	}
	finished := []bookStat{}
	rows, err = db.Query(`SELECT b.id,b.title,b.author,b.cover,b.kind,COALESCE((SELECT SUM(seconds) FROM reading_log WHERE book_id=b.id AND user_id=ub.user_id),0),ub.finished_at FROM user_books ub JOIN books b ON b.id=ub.book_id WHERE ub.user_id=? AND ub.finished_at>0 AND b.deleted_at=0 ORDER BY ub.finished_at DESC LIMIT 12`, uid)
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

// 今天读了多久 + 最近在读的一本（手表的 Tile、表盘用）
func handleStatsToday(w http.ResponseWriter, r *http.Request) {
	u := me(r)
	var secs int
	db.QueryRow(`SELECT COALESCE(SUM(seconds),0) FROM reading_log WHERE user_id=? AND day=?`, u.ID, today()).Scan(&secs)
	vis, vargs := visibleSQL(u)
	var book map[string]any
	var id int64
	var title, kind string
	var pct float64
	if db.QueryRow(`SELECT b.id,b.title,b.kind,p.percent FROM progress p JOIN books b ON b.id=p.book_id LEFT JOIN user_books ub ON ub.book_id=b.id AND ub.user_id=p.user_id
		WHERE p.user_id=? AND COALESCE(ub.read_state,'unread')<>'done' AND `+vis+` ORDER BY p.updated_at DESC LIMIT 1`, append([]any{u.ID}, vargs...)...).Scan(&id, &title, &kind, &pct) == nil {
		book = map[string]any{"id": id, "title": title, "kind": kind, "percent": pct}
	}
	writeJSON(w, 200, map[string]any{"seconds": secs, "book": book})
}
