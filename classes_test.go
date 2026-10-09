package main

import (
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

func TestClassMigrationPreservesAccounts(t *testing.T) {
	file := filepath.Join(t.TempDir(), "old.db")
	db, e := sql.Open("sqlite", file)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(schema + `PRAGMA user_version=1; INSERT INTO users(id,login,name,password,role) VALUES(1,'old','Old pupil','saved-hash','student');`); e != nil {
		t.Fatal(e)
	}
	db.Close()
	for i := 0; i < 2; i++ {
		db, e = openDB(file)
		if e != nil {
			t.Fatal(e)
		}
		var hash, class string
		var version int
		if e = db.QueryRow("SELECT password,class_name FROM users WHERE id=1").Scan(&hash, &class); e != nil {
			t.Fatal(e)
		}
		db.QueryRow("PRAGMA user_version").Scan(&version)
		if hash != "saved-hash" || class != "" || version != 2 {
			t.Fatalf("bad migration: %q %q %d", hash, class, version)
		}
		db.Close()
	}
}

func TestClassAssignmentAndAccess(t *testing.T) {
	a, _ := testApp(t)
	if e := createUser(a.db, "grouped", "Grouped", "student", "1234567890", " 8а "); e != nil {
		t.Fatal(e)
	}
	var c string
	a.db.QueryRow("SELECT class_name FROM users WHERE login='grouped'").Scan(&c)
	if c != "8а" {
		t.Fatalf("class normalization: %q", c)
	}
	if e := createUser(a.db, "badclass", "Bad", "student", "1234567890", "~"); e == nil {
		t.Fatal("reserved class accepted")
	}
	for _, v := range []struct {
		uid    int64
		target string
		class  string
		want   int
	}{{1, "1", "9б", 403}, {2, "2", "9б", 404}, {2, "1", "<bad>", 400}, {2, "1", " 9б ", 303}} {
		w := requestAs(t, a, v.uid, "POST", "/teacher/users/"+v.target+"/class", url.Values{"class_name": {v.class}, "filter_class": {"8а"}})
		if w.Code != v.want {
			t.Fatalf("assignment %+v status=%d", v, w.Code)
		}
	}
	a.db.QueryRow("SELECT class_name FROM users WHERE id=1").Scan(&c)
	if c != "9б" {
		t.Fatal("class not changed")
	}
	if w := requestAs(t, a, 2, "POST", "/teacher/users/1/class", url.Values{"class_name": {""}}); w.Code != 303 {
		t.Fatal("cannot clear class")
	}
}

func TestClassFilters(t *testing.T) {
	a, pid := testApp(t)
	p, _ := getProblem(a.db, pid)
	a.db.Exec("UPDATE users SET name='PUPIL_EIGHT',class_name='8а' WHERE id=1")
	a.db.Exec("UPDATE users SET name='PUPIL_NINE',class_name='9б' WHERE id=3")
	a.db.Exec("INSERT INTO users(id,login,name,password,role) VALUES(5,'unassigned','PUPIL_NONE','unused','student')")
	for _, uid := range []int{1, 3, 5} {
		_, e := a.db.Exec("INSERT INTO submissions(user_id,problem_id,version_id,language,source,status,score,created) VALUES(?,?,?,'python3','print(0)','AC',100,1)", uid, pid, p.VersionID)
		if e != nil {
			t.Fatal(e)
		}
	}
	for _, route := range []string{"/teacher/users", "/submissions", "/teacher/contests/1/results"} {
		for _, filter := range []struct{ class, want string }{{"8а", "PUPIL_EIGHT"}, {"9б", "PUPIL_NINE"}, {"~", "PUPIL_NONE"}} {
			t.Run(route+filter.class, func(t *testing.T) {
				w := requestAs(t, a, 2, "GET", route+"?"+url.Values{"class": {filter.class}}.Encode(), nil)
				if w.Code != 200 {
					t.Fatalf("status %d: %s", w.Code, w.Body.String())
				}
				body := w.Body.String()
				for _, name := range []string{"PUPIL_EIGHT", "PUPIL_NINE", "PUPIL_NONE"} {
					if strings.Contains(body, name) != (name == filter.want) {
						t.Fatalf("bad class filtering of %s", name)
					}
				}
			})
		}
	}
	w := requestAs(t, a, 2, "GET", "/submissions?user=3&"+url.Values{"class": {"8а"}}.Encode(), nil)
	if strings.Contains(w.Body.String(), "PUPIL_NINE") {
		t.Fatal("user filter bypassed class")
	}
	w = requestAs(t, a, 1, "GET", "/submissions?user=3&"+url.Values{"class": {"9б"}}.Encode(), nil)
	if strings.Contains(w.Body.String(), "/submissions/2\"") {
		t.Fatal("student sees other student")
	}
	for i := 0; i < 51; i++ {
		a.db.Exec("INSERT INTO submissions(user_id,problem_id,version_id,language,source,created) VALUES(1,?,?,'python3','x',1)", pid, p.VersionID)
	}
	w = requestAs(t, a, 2, "GET", "/submissions?"+url.Values{"class": {"8а"}}.Encode(), nil)
	if !strings.Contains(strings.ToLower(w.Body.String()), "page=1&amp;user=0&amp;class="+strings.ToLower(url.QueryEscape("8а"))) {
		t.Fatal("pagination does not preserve class")
	}
	w = requestAs(t, a, 2, "GET", "/submissions?class="+url.QueryEscape("' OR 1=1 --"), nil)
	if strings.Contains(w.Body.String(), "PUPIL_") {
		t.Fatal("SQL filter injection")
	}
	if w.Code != 200 {
		t.Fatal(fmt.Sprint(w.Code))
	}
}
