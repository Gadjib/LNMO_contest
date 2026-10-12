package main

import (
	"net/url"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func scopedFixture(t *testing.T) (*App, int64) {
	t.Helper()
	a, pid := testApp(t)
	for _, q := range []string{
		"UPDATE users SET name='VISIBLE_PUPIL',class_name='8м' WHERE id=1",
		"UPDATE users SET name='PRIVATE_PUPIL',class_name='9м' WHERE id=3",
		"INSERT INTO account_settings(user_id,teacher_all) VALUES(2,0)",
		"INSERT INTO teacher_classes(user_id,class_name) VALUES(2,'8м')",
	} {
		if _, e := a.db.Exec(q); e != nil {
			t.Fatal(e)
		}
	}
	p, e := getProblem(a.db, pid)
	if e != nil {
		t.Fatal(e)
	}
	for _, uid := range []int{1, 3} {
		if _, e := a.db.Exec("INSERT INTO submissions(id,user_id,problem_id,version_id,language,source,status,score,created) VALUES(?,?,?,?,'python3','print(5)','WA',40,1)", uid, uid, pid, p.VersionID); e != nil {
			t.Fatal(e)
		}
	}
	return a, pid
}

func TestTeacherClassSecurity(t *testing.T) {
	a, _ := scopedFixture(t)
	for _, path := range []string{"/teacher/users", "/submissions", "/teacher/contests/1/results", "/teacher/results"} {
		w := requestAs(t, a, 2, "GET", path, nil)
		if w.Code != 200 || !strings.Contains(w.Body.String(), "VISIBLE_PUPIL") || strings.Contains(w.Body.String(), "PRIVATE_PUPIL") {
			t.Fatalf("bad scope %s: %d %s", path, w.Code, w.Body.String())
		}
	}
	for _, path := range []string{"/submissions/3", "/submissions/3/status", "/teacher/results?user=3", "/teacher/contests/1/results?user=3"} {
		if w := requestAs(t, a, 2, "GET", path, nil); w.Code != 404 {
			t.Fatal(path, w.Code)
		}
	}
	for _, path := range []string{"/teacher/submissions/3/rejudge", "/teacher/users/3/reset", "/teacher/users/3/class"} {
		if w := requestAs(t, a, 2, "POST", path, url.Values{"password": {"x"}, "class_name": {"8м"}}); w.Code != 404 {
			t.Fatal(path, w.Code)
		}
	}
	if w := requestAs(t, a, 2, "POST", "/teacher/users/1/class", url.Values{"class_name": {"9м"}}); w.Code != 403 {
		t.Fatal("transfer out of scope", w.Code)
	}
	if w := requestAs(t, a, 2, "POST", "/teacher/users", url.Values{"login": {"outside"}, "name": {"Outside"}, "role": {"student"}, "class_name": {"9м"}, "password": {"x"}}); w.Code != 403 {
		t.Fatal("created outside class")
	}
	if w := requestAs(t, a, 2, "POST", "/teacher/users", url.Values{"login": {"inside"}, "name": {"Inside"}, "role": {"student"}, "class_name": {"8м"}, "password": {"x"}}); w.Code != 303 {
		t.Fatal("failed short-password student", w.Body.String())
	}
	if w := requestAs(t, a, 2, "GET", "/teacher/users/1/edit", nil); w.Code != 403 {
		t.Fatal("teacher got admin editor")
	}
	if w := requestAs(t, a, 1, "GET", "/teacher/results", nil); w.Code != 403 {
		t.Fatal("student got reports")
	}
	a.db.Exec("INSERT INTO code_fingerprints VALUES(1,'{}'),(3,'{}')")
	a.db.Exec("INSERT INTO code_matches VALUES(1,3,100)")
	if w := requestAs(t, a, 2, "GET", "/submissions/1", nil); strings.Contains(w.Body.String(), "PRIVATE_PUPIL") || strings.Contains(w.Body.String(), "Высокое сходство") {
		t.Fatal("similarity leaked")
	}
	if w := requestAs(t, a, 4, "GET", "/submissions/1", nil); !strings.Contains(w.Body.String(), "PRIVATE_PUPIL") {
		t.Fatal("admin lost similarity")
	}
	a.db.Exec("DELETE FROM teacher_classes WHERE user_id=2")
	if w := requestAs(t, a, 2, "GET", "/teacher/users", nil); strings.Contains(w.Body.String(), "VISIBLE_PUPIL") {
		t.Fatal("empty assignment must deny access")
	}
}

func TestAccountEditorAndLastAdmin(t *testing.T) {
	a, _ := scopedFixture(t)
	form := url.Values{"login": {"renamed"}, "name": {"Новое имя"}, "role": {"teacher"}, "teacher_access": {"selected"}, "teacher_classes": {"8м"}, "extra_classes": {"10М, 8м"}, "password": {"x"}}
	w := requestAs(t, a, 4, "POST", "/teacher/users/1/edit", form)
	if w.Code != 303 {
		t.Fatal(w.Code, w.Body.String())
	}
	u, e := getAccount(a.db, 1)
	if e != nil || u.Login != "renamed" || u.Name != "Новое имя" || u.Role != "teacher" || u.AllClasses || len(u.TeachingClasses) != 2 {
		t.Fatal(u, e)
	}
	var count int
	a.db.QueryRow("SELECT count(*) FROM submissions WHERE user_id=1").Scan(&count)
	if count != 1 {
		t.Fatal("history lost")
	}
	var hash string
	a.db.QueryRow("SELECT password FROM users WHERE id=1").Scan(&hash)
	if !checkPassword(hash, "x") {
		t.Fatal("password mismatch")
	}
	form.Set("password", "")
	form.Set("blocked", "on")
	if w = requestAs(t, a, 4, "POST", "/teacher/users/1/edit", form); w.Code != 303 {
		t.Fatal(w.Code)
	}
	u, _ = getAccount(a.db, 1)
	if !u.Blocked {
		t.Fatal("not blocked")
	}
	if w = requestAs(t, a, 1, "GET", "/", nil); w.Code != 303 || w.Header().Get("Location") != "/login" {
		t.Fatal("blocked session allowed")
	}
	var after string
	a.db.QueryRow("SELECT password FROM users WHERE id=1").Scan(&after)
	if hash != after {
		t.Fatal("blank password changed hash")
	}
	form.Set("login", "admin")
	form.Set("role", "student")
	w = requestAs(t, a, 4, "POST", "/teacher/users/4/edit", form)
	if !strings.Contains(w.Body.String(), "последнего активного администратора") {
		t.Fatal("last admin demoted")
	}
	u, _ = getAccount(a.db, 4)
	if u.Role != "admin" || u.Blocked {
		t.Fatal("last admin changed")
	}
	// Admin can create another administrator, but a teacher cannot.
	form = url.Values{"login": {"admin2"}, "name": {"Admin Two"}, "role": {"admin"}, "password": {"1"}}
	if w = requestAs(t, a, 4, "POST", "/teacher/users", form); w.Code != 303 {
		t.Fatal(w.Code, w.Body.String())
	}
	form.Set("login", "admin3")
	if w = requestAs(t, a, 2, "POST", "/teacher/users", form); w.Code != 403 {
		t.Fatal("privilege escalation")
	}
}

func TestPasswordLengthsAndLegacy(t *testing.T) {
	for _, p := range []string{"x", " ", strings.Repeat("Я", 200)} {
		hash, e := hashPassword(p)
		if e != nil || !checkPassword(hash, p) || checkPassword(hash, p+"x") {
			t.Fatal("password roundtrip", e)
		}
	}
	if _, e := hashPassword(""); e == nil {
		t.Fatal("empty password")
	}
	legacy, e := bcrypt.GenerateFromPassword([]byte("old-pass"), bcrypt.MinCost)
	if e != nil {
		t.Fatal(e)
	}
	if !checkPassword(string(legacy), "old-pass") || checkPassword(string(legacy), "wrong") {
		t.Fatal("legacy hash broken")
	}
	a, _ := testApp(t)
	long := strings.Repeat("a", 300)
	if e = createUser(a.db, "longpass", "Long", "student", long); e != nil {
		t.Fatal(e)
	}
	if w := requestAs(t, a, 4, "POST", "/teacher/users/1/reset", url.Values{"password": {long}}); w.Code != 303 {
		t.Fatal(w.Code)
	}
	var hash string
	a.db.QueryRow("SELECT password FROM users WHERE id=1").Scan(&hash)
	if !checkPassword(hash, long) {
		t.Fatal("long reset failed")
	}
}

func TestResultSummaryBestScore(t *testing.T) {
	a, pid := scopedFixture(t)
	p, _ := getProblem(a.db, pid)
	for _, s := range []struct {
		status string
		score  int
	}{{"WA", 60}, {"WA", 50}, {"QUEUED", 99}, {"JE", 100}} {
		if _, e := a.db.Exec("INSERT INTO submissions(user_id,problem_id,version_id,language,source,status,score,created) VALUES(1,?,?,'python3','x',?,?,1)", pid, p.VersionID, s.status, s.score); e != nil {
			t.Fatal(e)
		}
	}
	w := requestAs(t, a, 2, "GET", "/teacher/results?class=8%D0%BC&user=1&contest=1", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "60 / 100") || !strings.Contains(w.Body.String(), "В очереди: 1") {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := requestAs(t, a, 2, "GET", "/teacher/results?user=-1", nil); w.Code != 400 {
		t.Fatal("invalid filter")
	}
	if w := requestAs(t, a, 2, "GET", "/teacher/results?class=9%D0%BC", nil); strings.Contains(w.Body.String(), "PRIVATE_PUPIL") {
		t.Fatal("report class bypass")
	}
	if w := requestAs(t, a, 4, "GET", "/teacher/results", nil); !strings.Contains(w.Body.String(), "PRIVATE_PUPIL") {
		t.Fatal("admin report scope")
	}
}
