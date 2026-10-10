package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"golang.org/x/crypto/bcrypt"
)

type App struct {
	db        *sql.DB
	templates *template.Template
	limiter   rateLimiter
	dummyHash string
}
type BoardRow struct {
	User   User
	Scores []int
	Total  int
}
type Page struct {
	Title, Error, Notice, CSRF string
	User                       User
	Contests                   []Contest
	Contest                    Contest
	Problems                   []Problem
	Problem                    Problem
	Submissions                []Submission
	Submission                 Submission
	Users                      []User
	Board                      []BoardRow
	SelectedUser               int64
	WorkerOK                   bool
	NextPage                   int
	Classes                    []string
	ClassFilter                string
	RestrictedAudience         bool
}

func newApp(db *sql.DB) *App {
	policy := bluemonday.UGCPolicy()
	fm := template.FuncMap{"date": dateTime, "formtime": formTime, "status": statusLabel, "add": func(a, b int) int { return a + b }, "lang": func(s string) string {
		if s == "cpp17" {
			return "C++17"
		}
		return "Python 3"
	}, "haslang": func(p ProblemSpec, s string) bool {
		for _, l := range p.AllowedLanguages {
			if l == s {
				return true
			}
		}
		return false
	}, "markdown": func(s string) template.HTML {
		var b bytes.Buffer
		if e := goldmark.Convert([]byte(s), &b); e != nil {
			return ""
		}
		return template.HTML(policy.SanitizeBytes(b.Bytes()))
	}}
	fm["emptyExample"] = func() Example { return Example{} }
	fm["containsClass"] = func(classes []string, name string) bool { return slices.Contains(classes, name) }
	t := template.Must(template.New("").Funcs(fm).ParseFS(assets, "templates/*.html"))
	h, _ := bcrypt.GenerateFromPassword([]byte(randomToken()), 12)
	return &App{db: db, templates: t, dummyHash: string(h)}
}
func (a *App) routes() http.Handler {
	m := http.NewServeMux()
	static, _ := fs.Sub(assets, "static")
	m.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(static))))
	m.HandleFunc("GET /login", a.login)
	m.HandleFunc("POST /login", a.login)
	m.HandleFunc("POST /logout", a.protected(a.logout, false))
	m.HandleFunc("GET /account", a.protected(a.password, false))
	m.HandleFunc("POST /account", a.protected(a.password, false))
	m.HandleFunc("GET /{$}", a.protected(a.home, false))
	m.HandleFunc("GET /contests/{id}", a.protected(a.contest, false))
	m.HandleFunc("GET /problems/{id}", a.protected(a.problem, false))
	m.HandleFunc("GET /images/{id}", a.protected(a.serveImage, false))
	m.HandleFunc("POST /teacher/images", a.protected(a.uploadImage, true))
	m.HandleFunc("POST /problems/{id}/submit", a.protected(a.submit, false))
	m.HandleFunc("GET /submissions", a.protected(a.submissions, false))
	m.HandleFunc("GET /submissions/{id}", a.protected(a.submission, false))
	m.HandleFunc("GET /submissions/{id}/status", a.protected(a.submissionStatus, false))
	m.HandleFunc("GET /teacher/users", a.protected(a.users, true))
	m.HandleFunc("POST /teacher/users", a.protected(a.users, true))
	m.HandleFunc("POST /teacher/users/{id}/reset", a.protected(a.resetPassword, true))
	m.HandleFunc("POST /teacher/users/{id}/class", a.protected(a.changeClass, true))
	m.HandleFunc("GET /teacher/contests/new", a.protected(a.editContest, true))
	m.HandleFunc("GET /teacher/contests/import", a.protected(a.importContestPage, true))
	m.HandleFunc("POST /teacher/contests/import", a.protected(a.importContestPage, true))
	m.HandleFunc("GET /teacher/contests/{id}/export", a.protected(a.exportContestPage, true))
	m.HandleFunc("POST /teacher/contests/new", a.protected(a.editContest, true))
	m.HandleFunc("GET /teacher/contests/{id}/edit", a.protected(a.editContest, true))
	m.HandleFunc("POST /teacher/contests/{id}/edit", a.protected(a.editContest, true))
	m.HandleFunc("GET /teacher/contests/{id}/problems/new", a.protected(a.editProblem, true))
	m.HandleFunc("POST /teacher/contests/{id}/problems/new", a.protected(a.editProblem, true))
	m.HandleFunc("GET /teacher/problems/{id}/edit", a.protected(a.editProblem, true))
	m.HandleFunc("POST /teacher/problems/{id}/edit", a.protected(a.editProblem, true))
	m.HandleFunc("GET /teacher/problems/{id}/export", a.protected(a.export, true))
	m.HandleFunc("GET /teacher/contests/{id}/results", a.protected(a.results, true))
	m.HandleFunc("POST /teacher/submissions/{id}/rejudge", a.protected(a.rejudge, true))
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if e := a.db.Ping(); e != nil {
			http.Error(w, "database unavailable", 503)
			return
		}
		w.Write([]byte("ok\n"))
	})
	return a.middleware(m)
}
func (a *App) render(w http.ResponseWriter, r *http.Request, name string, d Page) {
	d.User = currentUser(r)
	if d.User.Teacher() {
		d.ClassFilter = r.URL.Query().Get("class")
		rows, e := a.db.Query("SELECT DISTINCT class_name FROM users WHERE role='student' AND class_name<>'' ORDER BY class_name")
		if e != nil {
			a.fail(w, e)
			return
		}
		for rows.Next() {
			var c string
			if e = rows.Scan(&c); e != nil {
				rows.Close()
				a.fail(w, e)
				return
			}
			d.Classes = append(d.Classes, c)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			a.fail(w, e)
			return
		}
		if d.ClassFilter != "" && d.ClassFilter != "~" {
			found := false
			for _, c := range d.Classes {
				if c == d.ClassFilter {
					found = true
				}
			}
			if !found {
				d.Classes = append(d.Classes, d.ClassFilter)
			}
		}
		for _, name := range d.Contest.Classes {
			if !slices.Contains(d.Classes, name) {
				d.Classes = append(d.Classes, name)
			}
		}
	}
	d.CSRF, _ = r.Context().Value(csrfKey).(string)
	var heartbeat int64
	a.db.QueryRow("SELECT updated FROM health WHERE name='worker'").Scan(&heartbeat)
	d.WorkerOK = time.Now().Unix()-heartbeat < 30
	var b bytes.Buffer
	if e := a.templates.ExecuteTemplate(&b, name, d); e != nil {
		a.fail(w, e)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(b.Bytes())
}
func (a *App) fail(w http.ResponseWriter, e error) {
	log.Print(e)
	http.Error(w, "Ошибка сервера. Попробуйте позже.", 500)
}
func idParam(r *http.Request) int64 { id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64); return id }
func (a *App) home(w http.ResponseWriter, r *http.Request) {
	q := "SELECT id,title,description,starts,ends,published FROM contests c"
	var args []any
	if !currentUser(r).Teacher() {
		q += " WHERE published=1 AND " + contestClassSQL("c")
		args = append(args, currentUser(r).ClassName)
	}
	q += " ORDER BY starts DESC,id DESC"
	rows, e := a.db.Query(q, args...)
	if e != nil {
		a.fail(w, e)
		return
	}
	defer rows.Close()
	d := Page{Title: "Контесты"}
	for rows.Next() {
		var c Contest
		if e = rows.Scan(&c.ID, &c.Title, &c.Description, &c.Starts, &c.Ends, &c.Published); e != nil {
			a.fail(w, e)
			return
		}
		d.Contests = append(d.Contests, c)
	}
	a.render(w, r, "home", d)
}
func (a *App) visibleContest(r *http.Request, c Contest) bool {
	return currentUser(r).Teacher() || (contestAllowed(c, currentUser(r)) && time.Now().Unix() >= c.Starts)
}
func (a *App) problemList(cid int64) ([]Problem, error) {
	rows, e := a.db.Query("SELECT p.id,p.contest_id,v.id,v.spec FROM problems p JOIN versions v ON v.id=p.current_version WHERE p.contest_id=? ORDER BY p.id", cid)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var list []Problem
	for rows.Next() {
		var p Problem
		var spec string
		if e = rows.Scan(&p.ID, &p.ContestID, &p.VersionID, &spec); e != nil {
			return nil, e
		}
		if e = json.Unmarshal([]byte(spec), &p.Spec); e != nil {
			return nil, e
		}
		list = append(list, p)
	}
	return list, rows.Err()
}
func (a *App) contest(w http.ResponseWriter, r *http.Request) {
	c, e := getContest(a.db, idParam(r))
	if e != nil || !contestAllowed(c, currentUser(r)) {
		http.NotFound(w, r)
		return
	}
	if !a.visibleContest(r, c) {
		a.render(w, r, "contest", Page{Title: c.Title, Contest: c})
		return
	}
	p, e := a.problemList(c.ID)
	if e != nil {
		a.fail(w, e)
		return
	}
	a.render(w, r, "contest", Page{Title: c.Title, Contest: c, Problems: p})
}
func (a *App) visibleProblem(w http.ResponseWriter, r *http.Request) (Problem, Contest, bool) {
	p, e := getProblem(a.db, idParam(r))
	if e != nil {
		http.NotFound(w, r)
		return p, Contest{}, false
	}
	c, e := getContest(a.db, p.ContestID)
	if e != nil || !a.visibleContest(r, c) {
		http.NotFound(w, r)
		return p, c, false
	}
	return p, c, true
}
func (a *App) problem(w http.ResponseWriter, r *http.Request) {
	p, c, ok := a.visibleProblem(w, r)
	if !ok {
		return
	}
	a.render(w, r, "problem", Page{Title: p.Spec.Title, Problem: p, Contest: c})
}
func (a *App) submit(w http.ResponseWriter, r *http.Request) {
	p, c, ok := a.visibleProblem(w, r)
	if !ok {
		return
	}
	u := currentUser(r)
	bad := func(s string) {
		a.render(w, r, "problem", Page{Title: p.Spec.Title, Problem: p, Contest: c, Error: s, Submission: Submission{Source: r.FormValue("source")}})
	}
	if !c.Open() && !u.Teacher() {
		http.Error(w, "Приём решений закрыт", 403)
		return
	}
	lang := r.FormValue("language")
	allowed := false
	for _, l := range p.Spec.AllowedLanguages {
		if lang == l {
			allowed = true
		}
	}
	if !allowed {
		bad("Выберите допустимый язык")
		return
	}
	source := r.FormValue("source")
	if f, _, e := r.FormFile("source_file"); e == nil {
		defer f.Close()
		b, e := io.ReadAll(io.LimitReader(f, 65537))
		if e != nil {
			bad("Не удалось прочитать файл")
			return
		}
		source = string(b)
	}
	if len(source) == 0 || len(source) > 65536 {
		bad("Размер решения: от 1 байта до 64 КБ")
		return
	}
	now := time.Now().Unix()
	res, e := a.db.Exec(`INSERT INTO submissions(user_id,problem_id,version_id,language,source,created)
	SELECT ?,?,?,?,?,? WHERE (SELECT count(*) FROM submissions WHERE user_id=? AND status IN ('QUEUED','RUNNING'))<3
	AND (SELECT coalesce(max(created),0) FROM submissions WHERE user_id=?)<=?`, u.ID, p.ID, p.VersionID, lang, source, now, u.ID, u.ID, now-5)
	if e != nil {
		a.fail(w, e)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		bad("Не больше 3 решений в очереди и одной отправки в 5 секунд")
		return
	}
	id, _ := res.LastInsertId()
	http.Redirect(w, r, fmt.Sprintf("/submissions/%d", id), 303)
}
func (a *App) submissions(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	filter := u.ID
	if u.Teacher() {
		filter, _ = strconv.ParseInt(r.URL.Query().Get("user"), 10, 64)
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 0 || page > 100000 {
		page = 0
	}
	q := "SELECT s.id FROM submissions s JOIN users u ON u.id=s.user_id WHERE 1=1"
	var args []any
	if filter > 0 {
		q += " AND s.user_id=?"
		args = append(args, filter)
	}
	if u.Teacher() {
		cl, ca := classWhere(r.URL.Query().Get("class"), "u.")
		q += cl
		args = append(args, ca...)
	}
	q += " ORDER BY s.id DESC LIMIT 51 OFFSET ?"
	args = append(args, page*50)
	rows, e := a.db.Query(q, args...)
	if e != nil {
		a.fail(w, e)
		return
	}
	var ids []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	d := Page{Title: "Отправки", SelectedUser: filter}
	if len(ids) > 50 {
		d.NextPage = page + 1
		ids = ids[:50]
	}
	for _, id := range ids {
		s, e := getSubmission(a.db, id)
		if e != nil {
			a.fail(w, e)
			return
		}
		s.Source = ""
		s.Results = nil
		if u.Teacher() {
			if e = a.loadSimilarity(&s, false); e != nil {
				a.fail(w, e)
				return
			}
		}
		d.Submissions = append(d.Submissions, s)
	}
	a.render(w, r, "submissions", d)
}
func (a *App) authorizedSubmission(w http.ResponseWriter, r *http.Request) (Submission, bool) {
	s, e := getSubmission(a.db, idParam(r))
	if e != nil || (!currentUser(r).Teacher() && s.UserID != currentUser(r).ID) {
		http.NotFound(w, r)
		return s, false
	}
	return s, true
}
func (a *App) submission(w http.ResponseWriter, r *http.Request) {
	s, ok := a.authorizedSubmission(w, r)
	if !ok {
		return
	}
	if currentUser(r).Teacher() {
		if e := a.loadTestDetails(&s); e != nil {
			a.fail(w, e)
			return
		}
		if e := a.loadSimilarity(&s, true); e != nil {
			a.fail(w, e)
			return
		}
	}
	a.render(w, r, "submission", Page{Title: fmt.Sprintf("Отправка #%d", s.ID), Submission: s})
}
func (a *App) submissionStatus(w http.ResponseWriter, r *http.Request) {
	s, ok := a.authorizedSubmission(w, r)
	if !ok {
		return
	}
	response := map[string]any{"status": s.Status, "pending": s.Pending()}
	if currentUser(r).Teacher() {
		if e := a.loadSimilarity(&s, false); e != nil {
			a.fail(w, e)
			return
		}
		response["similarity_checked"] = s.SimilarityChecked
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}
func (a *App) users(w http.ResponseWriter, r *http.Request) {
	d := Page{Title: "Ученики и учителя"}
	if r.Method == "POST" {
		role := r.FormValue("role")
		if role != "student" && currentUser(r).Role != "admin" {
			http.Error(w, "Только администратор создаёт учителей", 403)
			return
		}
		_, classError := normalizeClass(r.FormValue("class_name"))
		if role == "student" && classError != nil {
			d.Error = classError.Error()
		} else if e := createUser(a.db, r.FormValue("login"), r.FormValue("name"), role, r.FormValue("password"), r.FormValue("class_name")); e != nil {
			d.Error = "Не удалось создать аккаунт: проверьте уникальность логина, имя и длину пароля (10–72 байта)"
		} else {
			http.Redirect(w, r, "/teacher/users?created=1", 303)
			return
		}
	}
	if r.URL.Query().Get("created") == "1" {
		d.Notice = "Аккаунт создан"
	}
	if r.URL.Query().Get("reset") == "1" {
		d.Notice = "Пароль изменён, старые сессии закрыты"
	}
	if r.URL.Query().Get("class_saved") == "1" {
		d.Notice = "Класс изменён"
	}
	cl, ca := classWhere(r.URL.Query().Get("class"), "")
	rows, e := a.db.Query("SELECT id,login,name,role,class_name FROM users WHERE 1=1"+cl+" ORDER BY role,class_name,name", ca...)
	if e != nil {
		a.fail(w, e)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var u User
		rows.Scan(&u.ID, &u.Login, &u.Name, &u.Role, &u.ClassName)
		d.Users = append(d.Users, u)
	}
	a.render(w, r, "users", d)
}
func (a *App) resetPassword(w http.ResponseWriter, r *http.Request) {
	var role string
	if e := a.db.QueryRow("SELECT role FROM users WHERE id=?", idParam(r)).Scan(&role); e != nil {
		http.NotFound(w, r)
		return
	}
	if role != "student" && currentUser(r).Role != "admin" {
		http.Error(w, "Нет доступа", 403)
		return
	}
	p := r.FormValue("password")
	if len(p) < 10 || len(p) > 72 {
		http.Error(w, "Пароль: 10–72 байта", 400)
		return
	}
	h, e := bcrypt.GenerateFromPassword([]byte(p), 12)
	if e != nil {
		a.fail(w, e)
		return
	}
	tx, e := a.db.Begin()
	if e != nil {
		a.fail(w, e)
		return
	}
	defer tx.Rollback()
	if _, e = tx.Exec("UPDATE users SET password=? WHERE id=?", string(h), idParam(r)); e == nil {
		_, e = tx.Exec("DELETE FROM sessions WHERE user_id=?", idParam(r))
	}
	if e == nil {
		e = tx.Commit()
	}
	if e != nil {
		a.fail(w, e)
		return
	}
	http.Redirect(w, r, "/teacher/users?reset=1", 303)
}
func (a *App) editContest(w http.ResponseWriter, r *http.Request) {
	c := Contest{Starts: time.Now().Unix(), Ends: time.Now().Add(7 * 24 * time.Hour).Unix()}
	var e error
	if idParam(r) > 0 {
		c, e = getContest(a.db, idParam(r))
		if e != nil {
			http.NotFound(w, r)
			return
		}
	}
	d := Page{Title: "Редактор контеста"}
	d.RestrictedAudience = len(c.Classes) > 0
	if r.Method == "POST" {
		c.Title = strings.TrimSpace(r.FormValue("title"))
		c.Description = r.FormValue("description")
		c.Published = r.FormValue("published") == "on"
		var classErr error
		c.Classes, classErr = contestClassesFromForm(r)
		d.RestrictedAudience = r.FormValue("audience") == "classes"
		var e1, e2 error
		c.Starts, e1 = parseTime(r.FormValue("starts"))
		c.Ends, e2 = parseTime(r.FormValue("ends"))
		if c.Title == "" || len(c.Title) > 300 || len(c.Description) > 100000 || e1 != nil || e2 != nil || c.Ends <= c.Starts {
			d.Error = "Укажите название и корректный интервал времени (окончание позже начала)"
		} else if classErr != nil {
			d.Error = classErr.Error()
		} else {
			e = saveContest(a.db, &c)
			if e != nil {
				a.fail(w, e)
				return
			}
			http.Redirect(w, r, fmt.Sprintf("/contests/%d", c.ID), 303)
			return
		}
	}
	d.Contest = c
	a.render(w, r, "edit-contest", d)
}
func problemFromForm(r *http.Request) ProblemSpec {
	p := ProblemSpec{FormatVersion: 1, Code: r.FormValue("code"), Title: r.FormValue("title"), Statement: r.FormValue("statement"), InputFormat: r.FormValue("input_format"), OutputFormat: r.FormValue("output_format"), Notes: r.FormValue("notes"), AllowedLanguages: r.PostForm["languages"]}
	p.Limits.TimeMS, _ = strconv.Atoi(r.FormValue("time_ms"))
	p.Limits.MemoryMB, _ = strconv.Atoi(r.FormValue("memory_mb"))
	p.Limits.OutputKB, _ = strconv.Atoi(r.FormValue("output_kb"))
	p.Checker.Type = r.FormValue("checker")
	p.Scoring.Type = "sum"
	p.Scoring.MaxScore, _ = strconv.Atoi(r.FormValue("max_score"))
	get := func(k string, i int) string {
		a := r.PostForm[k]
		if i < len(a) {
			return a[i]
		}
		return ""
	}
	for i, id := range r.PostForm["test_id"] {
		points, _ := strconv.Atoi(get("test_points", i))
		p.Tests = append(p.Tests, TestCase{ID: id, Input: get("test_input", i), Answer: get("test_answer", i), Points: points})
	}
	for i, input := range r.PostForm["example_input"] {
		p.Examples = append(p.Examples, Example{Input: input, Output: get("example_output", i), Explanation: get("example_explanation", i)})
	}
	return p
}
func (a *App) editProblem(w http.ResponseWriter, r *http.Request) {
	p := Problem{Spec: defaultProblem()}
	var e error
	if strings.Contains(r.URL.Path, "/teacher/problems/") {
		p, e = getProblem(a.db, idParam(r))
		if e != nil {
			http.NotFound(w, r)
			return
		}
	} else {
		p.ContestID = idParam(r)
	}
	c, e := getContest(a.db, p.ContestID)
	if e != nil {
		http.NotFound(w, r)
		return
	}
	d := Page{Title: "Редактор задачи", Contest: c}
	if r.Method == "POST" {
		oldMax := p.Spec.Scoring.MaxScore
		if f, _, fe := r.FormFile("archive"); fe == nil {
			defer f.Close()
			var data []byte
			data, e = io.ReadAll(io.LimitReader(f, maxArchiveBytes+1))
			if e == nil {
				p.Spec, e = importProblem(data)
			}
		} else {
			p.Spec = problemFromForm(r)
			e = p.Spec.Validate()
		}
		if e == nil && p.ID != 0 && oldMax != p.Spec.Scoring.MaxScore {
			var n int
			a.db.QueryRow("SELECT count(*) FROM submissions WHERE problem_id=?", p.ID).Scan(&n)
			if n > 0 {
				e = fmt.Errorf("после первой отправки нельзя менять максимальный балл; создайте новую задачу")
			}
		}
		if e == nil {
			var savedID int64
			savedID, e = saveProblem(a.db, p.ContestID, p.ID, p.Spec)
			if e == nil {
				p.ID = savedID
			} else if strings.Contains(e.Error(), "UNIQUE constraint failed") {
				e = fmt.Errorf("не удалось сохранить: код задачи должен быть уникален внутри контеста")
			} else if strings.Contains(e.Error(), "SQL") {
				log.Print(e)
				e = fmt.Errorf("не удалось сохранить задачу")
			}
		}
		if e == nil {
			http.Redirect(w, r, fmt.Sprintf("/problems/%d", p.ID), 303)
			return
		}
		d.Error = e.Error()
	}
	d.Problem = p
	a.render(w, r, "edit-problem", d)
}
func (a *App) export(w http.ResponseWriter, r *http.Request) {
	p, e := getProblem(a.db, idParam(r))
	if e != nil {
		http.NotFound(w, r)
		return
	}
	if e = loadImages(a.db, &p.Spec); e != nil {
		a.fail(w, e)
		return
	}
	b, e := exportProblem(p.Spec)
	if e != nil {
		a.fail(w, e)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.zip"`, p.Spec.Code))
	w.Write(b)
}
func (a *App) results(w http.ResponseWriter, r *http.Request) {
	c, e := getContest(a.db, idParam(r))
	if e != nil {
		http.NotFound(w, r)
		return
	}
	p, e := a.problemList(c.ID)
	if e != nil {
		a.fail(w, e)
		return
	}
	cl, ca := classWhere(r.URL.Query().Get("class"), "")
	if len(c.Classes) > 0 {
		cl += " AND (EXISTS (SELECT 1 FROM contest_classes cc WHERE cc.contest_id=? AND cc.class_name=users.class_name) OR EXISTS (SELECT 1 FROM submissions s JOIN problems p ON p.id=s.problem_id WHERE s.user_id=users.id AND p.contest_id=?))"
		ca = append(ca, c.ID, c.ID)
	}
	rows, e := a.db.Query("SELECT id,login,name,role,class_name FROM users WHERE role='student'"+cl+" ORDER BY class_name,name", ca...)
	if e != nil {
		a.fail(w, e)
		return
	}
	var users []User
	for rows.Next() {
		var u User
		rows.Scan(&u.ID, &u.Login, &u.Name, &u.Role, &u.ClassName)
		users = append(users, u)
	}
	rows.Close()
	scores := map[[2]int64]int{}
	rows, e = a.db.Query("SELECT s.user_id,s.problem_id,max(s.score) FROM submissions s JOIN problems p ON p.id=s.problem_id WHERE p.contest_id=? AND s.status NOT IN ('QUEUED','RUNNING','JE') GROUP BY s.user_id,s.problem_id", c.ID)
	if e != nil {
		a.fail(w, e)
		return
	}
	for rows.Next() {
		var uid, pid int64
		var score int
		rows.Scan(&uid, &pid, &score)
		scores[[2]int64{uid, pid}] = score
	}
	rows.Close()
	d := Page{Title: "Результаты · " + c.Title, Contest: c, Problems: p}
	for _, u := range users {
		row := BoardRow{User: u}
		for _, pr := range p {
			s := scores[[2]int64{u.ID, pr.ID}]
			row.Scores = append(row.Scores, s)
			row.Total += s
		}
		d.Board = append(d.Board, row)
	}
	a.render(w, r, "results", d)
}
func (a *App) rejudge(w http.ResponseWriter, r *http.Request) {
	s, e := getSubmission(a.db, idParam(r))
	if e != nil {
		http.NotFound(w, r)
		return
	}
	if s.Pending() {
		http.Error(w, "Отправка уже проверяется", 409)
		return
	}
	// Keep the recorded version and replace its evaluation. A restart requeues RUNNING rows.
	if _, e = a.db.Exec("UPDATE submissions SET status='QUEUED',score=0,results='[]',diagnostic='',finished=NULL WHERE id=? AND status NOT IN ('QUEUED','RUNNING')", s.ID); e != nil {
		a.fail(w, e)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/submissions/%d", s.ID), 303)
}
