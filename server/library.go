package main

// 书架：系列列表、多选批量操作

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// 换一个表别名（系列封面的子查询里用）
func visibleSQLAs(u *User, alias string) (string, []any) {
	cond, args := visibleSQL(u)
	cond = strings.ReplaceAll(cond, "b.deleted_at", alias+".deleted_at")
	cond = strings.ReplaceAll(cond, "vf.book_id=b.id", "vf.book_id="+alias+".id")
	return cond, args
}

type seriesItem struct {
	Name     string  `json:"name"`
	Count    int     `json:"count"`
	Kind     string  `json:"kind"`
	CoverID  int64   `json:"cover_id"`
	Cover    string  `json:"cover"`
	Done     int     `json:"done"`
	LastRead int64   `json:"last_read"`
	Added    int64   `json:"added"`
	Author   string  `json:"author"`
	MaxIndex float64 `json:"max_index"`
}

// GET /api/series?kind=&q=&sort=recent|name|added
func handleSeries(w http.ResponseWriter, r *http.Request) {
	u := me(r)
	q := r.URL.Query()
	vis, vargs := visibleSQL(u)
	vis2, vargs2 := visibleSQLAs(u, "b2")
	where := []string{`b.series<>''`, vis}
	args := append([]any{}, vargs...)
	if k := q.Get("kind"); k != "" {
		where = append(where, `b.kind=?`)
		args = append(args, k)
	}
	if s := strings.TrimSpace(q.Get("q")); s != "" {
		where = append(where, `(b.series LIKE ? ESCAPE '\' OR b.search_key LIKE ? ESCAPE '\')`)
		args = append(args, "%"+likeEscape(s)+"%", "%"+likeEscape(strings.ToLower(toSimplified(s)))+"%")
	}
	order := `MAX(COALESCE(p.updated_at,0)) DESC, MAX(b.created_at) DESC`
	switch q.Get("sort") {
	case "name":
		order = `MIN(b.sort_key)`
	case "added":
		order = `MAX(b.created_at) DESC`
	}
	all := append(append(append([]any{}, vargs2...), u.ID, u.ID), args...)
	rows, err := db.Query(`SELECT b.series,COUNT(*),MIN(b.kind),
		COALESCE((SELECT b2.id FROM books b2 WHERE b2.series=b.series AND `+vis2+` ORDER BY b2.series_index,b2.sort_key LIMIT 1),0),
		SUM(CASE WHEN COALESCE(ub.read_state,'unread')='done' THEN 1 ELSE 0 END),MAX(COALESCE(p.updated_at,0)),MAX(b.created_at),MIN(b.author),MAX(b.series_index)
		FROM books b LEFT JOIN progress p ON p.book_id=b.id AND p.user_id=? LEFT JOIN user_books ub ON ub.book_id=b.id AND ub.user_id=?
		WHERE `+strings.Join(where, " AND ")+` GROUP BY b.series ORDER BY `+order+` LIMIT 1000`, all...)
	if err != nil {
		errJSON(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := []seriesItem{}
	for rows.Next() {
		var s seriesItem
		rows.Scan(&s.Name, &s.Count, &s.Kind, &s.CoverID, &s.Done, &s.LastRead, &s.Added, &s.Author, &s.MaxIndex)
		out = append(out, s)
	}
	rows.Close()
	for i := range out {
		db.QueryRow(`SELECT cover FROM books WHERE id=?`, out[i].CoverID).Scan(&out[i].Cover)
	}
	writeJSON(w, 200, out)
}

// POST /api/books/batch：多选以后一起改。
// 每个人自己的（谁都能做）：read_state、want、rating；书目信息（管理员）：series、category、tags_add、tags_remove、delete、confirm
func handleBatchBooks(w http.ResponseWriter, r *http.Request) {
	u := me(r)
	var req struct {
		IDs        []int64  `json:"ids"`
		ReadState  *string  `json:"read_state"`
		Want       *bool    `json:"want"`
		Rating     *int     `json:"rating"`
		Series     *string  `json:"series"`
		SeriesFrom *float64 `json:"series_from"` // 系列序号从几开始，按选中的顺序依次加 1；不填就不改序号
		Category   *string  `json:"category"`
		TagsAdd    []string `json:"tags_add"`
		TagsRemove []string `json:"tags_remove"`
		Delete     bool     `json:"delete"`
		Confirm    bool     `json:"confirm"`
	}
	if readJSON(r, &req) != nil || len(req.IDs) == 0 {
		errJSON(w, 400, "没有选中书")
		return
	}
	if len(req.IDs) > 1000 {
		errJSON(w, 400, "一次最多 1000 本")
		return
	}
	meta := req.Series != nil || req.Category != nil || len(req.TagsAdd) > 0 || len(req.TagsRemove) > 0 || req.Delete || req.Confirm
	if meta && !u.Admin() {
		errJSON(w, 403, "只有管理员可以改书目信息")
		return
	}
	var ids []int64
	for _, id := range req.IDs {
		if canSeeBook(u, id) {
			ids = append(ids, id)
		}
	}
	batch := "bt-" + newBatch()
	n := 0
	var errs []string
	for i, id := range ids {
		if req.ReadState != nil {
			setReadState(u.ID, id, *req.ReadState)
			if *req.ReadState == "unread" {
				db.Exec(`DELETE FROM progress WHERE user_id=? AND book_id=?`, u.ID, id)
			}
		}
		if req.Want != nil || req.Rating != nil {
			db.Exec(`INSERT OR IGNORE INTO user_books(user_id,book_id,updated_at) VALUES(?,?,?)`, u.ID, id, now())
			if req.Want != nil {
				db.Exec(`UPDATE user_books SET want=?,updated_at=? WHERE user_id=? AND book_id=?`, map[bool]int{true: 1, false: 0}[*req.Want], now(), u.ID, id)
			}
			if req.Rating != nil {
				v := *req.Rating
				if v < 0 || v > 5 {
					v = 0
				}
				db.Exec(`UPDATE user_books SET rating=?,updated_at=? WHERE user_id=? AND book_id=?`, v, now(), u.ID, id)
			}
		}
		if req.Delete {
			if b, err := getBook(id); err == nil && b.DeletedAt == 0 {
				if _, err := trashBook(b); err != nil {
					errs = append(errs, b.Title+"："+err.Error())
					continue
				}
			}
			n++
			continue
		}
		if meta {
			b, err := getBook(id)
			if err != nil {
				continue
			}
			changed := false
			if req.Series != nil {
				recordMetaChange(batch, "批量修改", id, "series", b.Series)
				b.Series = strings.TrimSpace(*req.Series)
				if req.SeriesFrom != nil {
					b.SeriesIndex = *req.SeriesFrom + float64(i)
				}
				changed = true
			}
			if req.Category != nil {
				recordMetaChange(batch, "批量修改", id, "category", b.Category)
				b.Category = strings.TrimSpace(*req.Category)
				changed = true
			}
			if len(req.TagsAdd) > 0 || len(req.TagsRemove) > 0 {
				set := map[string]bool{}
				var tags []string
				for _, t := range b.Tags {
					if !set[t] {
						set[t] = true
						tags = append(tags, t)
					}
				}
				for _, t := range req.TagsAdd {
					if t = strings.TrimSpace(t); t != "" && !set[t] {
						set[t] = true
						tags = append(tags, t)
					}
				}
				rm := map[string]bool{}
				for _, t := range req.TagsRemove {
					rm[strings.TrimSpace(t)] = true
				}
				var kept []string
				for _, t := range tags {
					if !rm[t] {
						kept = append(kept, t)
					}
				}
				b.Tags = kept
				changed = true
			}
			if req.Confirm {
				b.Status, b.Confidence = "ok", 1
				changed = true
			}
			if changed {
				tags, _ := json.Marshal(nonNil(b.Tags))
				if _, err := db.Exec(`UPDATE books SET series=?,series_index=?,category=?,tags=?,status=?,confidence=?,updated_at=? WHERE id=?`,
					b.Series, b.SeriesIndex, b.Category, string(tags), b.Status, b.Confidence, now(), id); err != nil {
					errs = append(errs, b.Title+"："+err.Error())
					continue
				}
				updateSearchKey(id)
				// 系列、分类决定文件放在哪：已确认的书跟着整理
				if b.Status == "ok" && (req.Series != nil || req.Category != nil || req.Confirm) {
					if err := organizeBook(id, batch, "批量修改"); err != nil {
						errs = append(errs, b.Title+"：移动文件失败："+err.Error())
					}
				}
			}
		}
		n++
	}
	if meta {
		invalidateSuggestions()
	}
	msg := fmt.Sprintf("已处理 %d 本", n)
	if len(errs) > 0 {
		msg += "；" + strconv.Itoa(len(errs)) + " 本有问题：" + truncate(strings.Join(errs, "；"), 300)
	}
	if meta && !req.Delete {
		logEvent("info", 0, "批量修改："+msg)
	}
	writeJSON(w, 200, map[string]any{"ok": len(errs) == 0, "count": n, "batch": batch, "message": msg})
}
