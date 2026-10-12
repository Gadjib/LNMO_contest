package main

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

type SummaryRow struct {
	UserID, ContestID                                int64
	Name, ClassName, ContestTitle                    string
	Score, Maximum, Solved, Tasks, Attempts, Pending int
	Published                                        bool
}
type ResultSummary struct {
	StudentID, ContestID int64
	Rows                 []SummaryRow
	Students             []User
	Contests             []Contest
	Next, Previous       string
}

func queryID(r *http.Request, key string) (int64, error) {
	s := r.URL.Query().Get(key)
	if s == "" {
		return 0, nil
	}
	id, e := strconv.ParseInt(s, 10, 64)
	if e != nil || id < 0 {
		return 0, fmt.Errorf("некорректный фильтр %s", key)
	}
	return id, nil
}

func (a *App) resultSummary(w http.ResponseWriter, r *http.Request) {
	summary := &ResultSummary{}
	var e error
	if summary.StudentID, e = queryID(r, "user"); e != nil {
		http.Error(w, e.Error(), 400)
		return
	}
	if summary.ContestID, e = queryID(r, "contest"); e != nil {
		http.Error(w, e.Error(), 400)
		return
	}
	page, e := queryID(r, "page")
	if e != nil || page > 100000 {
		http.Error(w, "Некорректная страница", 400)
		return
	}
	viewer := currentUser(r)
	if summary.StudentID != 0 {
		student, err := getAccount(a.db, summary.StudentID)
		if err != nil || student.Role != "student" || !viewer.AccessUser(student) {
			http.NotFound(w, r)
			return
		}
	}
	if summary.ContestID != 0 {
		if _, err := getContest(a.db, summary.ContestID); err != nil {
			http.NotFound(w, r)
			return
		}
	}
	scope, args := studentScope(viewer, "u.", false)
	students, e := a.db.Query("SELECT u.id,u.name,u.class_name FROM users u WHERE u.role='student'"+scope+" ORDER BY u.class_name,u.name", args...)
	if e != nil {
		a.fail(w, e)
		return
	}
	for students.Next() {
		var u User
		if e = students.Scan(&u.ID, &u.Name, &u.ClassName); e != nil {
			students.Close()
			a.fail(w, e)
			return
		}
		summary.Students = append(summary.Students, u)
	}
	e = students.Err()
	students.Close()
	if e != nil {
		a.fail(w, e)
		return
	}
	contests, e := a.db.Query("SELECT id,title FROM contests ORDER BY starts DESC,id DESC")
	if e != nil {
		a.fail(w, e)
		return
	}
	for contests.Next() {
		var c Contest
		if e = contests.Scan(&c.ID, &c.Title); e != nil {
			contests.Close()
			a.fail(w, e)
			return
		}
		summary.Contests = append(summary.Contests, c)
	}
	e = contests.Err()
	contests.Close()
	if e != nil {
		a.fail(w, e)
		return
	}
	cl, ca := classWhere(r.URL.Query().Get("class"), "u.")
	scope += cl
	args = append(args, ca...)
	if summary.StudentID > 0 {
		scope += " AND u.id=?"
		args = append(args, summary.StudentID)
	}
	if summary.ContestID > 0 {
		scope += " AND c.id=?"
		args = append(args, summary.ContestID)
	}
	q := `WITH best AS (
 SELECT user_id,problem_id,max(score) AS score,max(status='AC') AS solved FROM submissions WHERE status NOT IN ('QUEUED','RUNNING','JE') GROUP BY user_id,problem_id
 ), attempts AS (
 SELECT s.user_id,p.contest_id,count(*) AS attempts,sum(s.status IN ('QUEUED','RUNNING')) AS pending FROM submissions s JOIN problems p ON p.id=s.problem_id GROUP BY s.user_id,p.contest_id
 ) SELECT u.id,u.name,u.class_name,c.id,c.title,c.published,coalesce(sum(b.score),0),coalesce(sum(json_extract(v.spec,'$.Scoring.MaxScore')),0),coalesce(sum(b.solved),0),count(p.id),coalesce(a.attempts,0),coalesce(a.pending,0)
 FROM users u CROSS JOIN contests c
 LEFT JOIN problems p ON p.contest_id=c.id LEFT JOIN versions v ON v.id=p.current_version
 LEFT JOIN best b ON b.user_id=u.id AND b.problem_id=p.id
 LEFT JOIN attempts a ON a.user_id=u.id AND a.contest_id=c.id
 WHERE u.role='student' AND (NOT EXISTS(SELECT 1 FROM contest_classes cc WHERE cc.contest_id=c.id) OR EXISTS(SELECT 1 FROM contest_classes cc WHERE cc.contest_id=c.id AND cc.class_name=u.class_name) OR a.attempts>0)` + scope + `
 GROUP BY u.id,c.id ORDER BY c.starts DESC,c.id DESC,u.class_name,u.name,u.id LIMIT 51 OFFSET ?`
	args = append(args, page*50)
	rows, e := a.db.Query(q, args...)
	if e != nil {
		a.fail(w, e)
		return
	}
	for rows.Next() {
		var row SummaryRow
		if e = rows.Scan(&row.UserID, &row.Name, &row.ClassName, &row.ContestID, &row.ContestTitle, &row.Published, &row.Score, &row.Maximum, &row.Solved, &row.Tasks, &row.Attempts, &row.Pending); e != nil {
			rows.Close()
			a.fail(w, e)
			return
		}
		summary.Rows = append(summary.Rows, row)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		a.fail(w, e)
		return
	}
	link := func(n int64) string {
		values := url.Values{"class": {r.URL.Query().Get("class")}, "user": {strconv.FormatInt(summary.StudentID, 10)}, "contest": {strconv.FormatInt(summary.ContestID, 10)}, "page": {strconv.FormatInt(n, 10)}}
		return "/teacher/results?" + values.Encode()
	}
	if len(summary.Rows) > 50 {
		summary.Next = link(page + 1)
		summary.Rows = summary.Rows[:50]
	}
	if page > 0 {
		summary.Previous = link(page - 1)
	}
	a.render(w, r, "result-summary", Page{Title: "Итоговые результаты", Summary: summary})
}
