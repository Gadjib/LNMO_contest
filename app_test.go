package main

import (
	"archive/zip"
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testApp(t *testing.T) (*App, int64) {
	t.Helper()
	db, e := openDB(filepath.Join(t.TempDir(), "test.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	for _, q := range []string{
		`INSERT INTO users(id,login,name,password,role) VALUES(1,'student','Ученик','unused','student'),(2,'teacher','Учитель','unused','teacher'),(3,'other','Другой ученик','unused','student'),(4,'admin','Администратор','unused','admin')`,
		`INSERT INTO contests(id,title,description,starts,ends,published) VALUES(1,'Контест','Описание',0,4102444800,1)`,
	} {
		if _, e = db.Exec(q); e != nil {
			t.Fatal(e)
		}
	}
	pid, e := saveProblem(db, 1, 0, defaultProblem())
	if e != nil {
		t.Fatal(e)
	}
	return newApp(db), pid
}
func requestAs(t *testing.T, a *App, uid int64, method, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	token := randomToken()
	_, e := a.db.Exec("INSERT INTO sessions(token,user_id,expires) VALUES(?,?,?)", tokenHash(token), uid, time.Now().Add(time.Hour).Unix())
	if e != nil {
		t.Fatal(e)
	}
	csrf := strings.Repeat("c", 64)
	if form == nil {
		form = url.Values{}
	}
	if method == "POST" && form.Get("csrf") == "" {
		form.Set("csrf", csrf)
	}
	r := httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(&http.Cookie{Name: "session", Value: token})
	r.AddCookie(&http.Cookie{Name: "csrf", Value: csrf})
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, r)
	return w
}
func TestAllPagesAndAccess(t *testing.T) {
	a, pid := testApp(t)
	p, _ := getProblem(a.db, pid)
	_, e := a.db.Exec("INSERT INTO submissions(id,user_id,problem_id,version_id,language,source,status,results,created) VALUES(1,1,?,?,'python3','print(5)','WA',?,?)", pid, p.VersionID, encode([]Result{{TestID: "01", Status: "WA", Output: "SECRET-OUTPUT", Error: "SECRET-STDERR"}}), time.Now().Unix())
	if e != nil {
		t.Fatal(e)
	}
	paths := []string{"/", "/contests/1", "/problems/1", "/submissions", "/submissions/1", "/submissions/1/status", "/account", "/teacher/users", "/teacher/contests/new", "/teacher/contests/1/edit"}
	paths = append(paths, "/teacher/contests/1/problems/new", "/teacher/problems/1/edit", "/teacher/problems/1/export", "/teacher/contests/1/results")
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			w := requestAs(t, a, 2, "GET", path, nil)
			if w.Code != 200 {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
		})
	}
	for _, path := range []string{"/teacher/users", "/teacher/problems/1/export", "/teacher/contests/1/results"} {
		if w := requestAs(t, a, 1, "GET", path, nil); w.Code != 403 {
			t.Fatalf("student accessed %s: %d", path, w.Code)
		}
	}
	for _, path := range []string{"/submissions/1", "/submissions/1/status"} {
		if w := requestAs(t, a, 3, "GET", path, nil); w.Code != 404 {
			t.Fatalf("other student accessed %s: %d", path, w.Code)
		}
	}
	w := requestAs(t, a, 1, "GET", "/submissions/1", nil)
	if strings.Contains(w.Body.String(), "SECRET") {
		t.Fatal("hidden stdout leaked")
	}
	w = requestAs(t, a, 1, "GET", "/problems/1", nil)
	if strings.Contains(w.Body.String(), "-17 -23") {
		t.Fatal("test input leaked")
	}
	w = requestAs(t, a, 1, "GET", "/submissions?user=2", nil)
	if !strings.Contains(w.Body.String(), "#1") {
		t.Fatal("student query changed owner filter")
	}
	a.db.Exec("UPDATE contests SET starts=? WHERE id=1", time.Now().Add(time.Hour).Unix())
	if w = requestAs(t, a, 1, "GET", "/contests/1", nil); w.Code != 200 || strings.Contains(w.Body.String(), "Сумма двух чисел") {
		t.Fatal("future contest leaks tasks")
	}
	if w = requestAs(t, a, 1, "GET", "/problems/1", nil); w.Code != 404 {
		t.Fatal("future problem visible")
	}
	a.db.Exec("UPDATE contests SET published=0 WHERE id=1")
	if w = requestAs(t, a, 1, "GET", "/contests/1", nil); w.Code != 404 {
		t.Fatal("draft visible")
	}
}
func TestSubmitVersionsAndBounds(t *testing.T) {
	a, pid := testApp(t)
	p, _ := getProblem(a.db, pid)
	f := url.Values{"source": {"print(sum(map(int,input().split())))"}, "language": {"python3"}}
	w := requestAs(t, a, 1, "POST", "/problems/1/submit", f)
	if w.Code != 303 {
		t.Fatalf("submit: %d %s", w.Code, w.Body.String())
	}
	s, e := getSubmission(a.db, 1)
	if e != nil {
		t.Fatal(e)
	}
	if s.VersionID != p.VersionID || s.Status != "QUEUED" {
		t.Fatal("bad snapshot")
	}
	changed := p.Spec
	changed.Title = "Изменённая задача"
	changed.Tests[0].Answer = "7\n"
	if _, e = saveProblem(a.db, 1, pid, changed); e != nil {
		t.Fatal(e)
	}
	s, _ = getSubmission(a.db, 1)
	if s.ProblemTitle == changed.Title {
		t.Fatal("old submission changed version")
	}
	requestAs(t, a, 1, "POST", "/problems/1/submit", f)
	var n int
	a.db.QueryRow("SELECT count(*) FROM submissions").Scan(&n)
	if n != 1 {
		t.Fatal("rate limit bypassed")
	}
	a.db.Exec("UPDATE contests SET ends=1 WHERE id=1")
	w = requestAs(t, a, 3, "POST", "/problems/1/submit", f)
	if w.Code != 403 {
		t.Fatal("closed contest accepted submission")
	}
	w = requestAs(t, a, 1, "POST", "/logout", url.Values{"csrf": {"invalid"}})
	if w.Code != 403 {
		t.Fatal("CSRF accepted")
	}
	w = requestAs(t, a, 2, "POST", "/teacher/users", url.Values{"login": {"evil"}, "name": {"Evil"}, "password": {"1234567890"}, "role": {"admin"}})
	if w.Code != 403 {
		t.Fatal("teacher escalated privileges")
	}
}
func TestArchiveAndValidation(t *testing.T) {
	p := defaultProblem()
	data, e := exportProblem(p)
	if e != nil {
		t.Fatal(e)
	}
	q, e := importProblem(data)
	if e != nil {
		t.Fatal(e)
	}
	if q.Tests[3].Input != p.Tests[3].Input || q.Tests[0].Answer != p.Tests[0].Answer {
		t.Fatal("round trip changed tests")
	}
	makeZip := func(name string, mode os.FileMode) []byte {
		var b bytes.Buffer
		z := zip.NewWriter(&b)
		h := &zip.FileHeader{Name: name}
		h.SetMode(mode)
		w, _ := z.CreateHeader(h)
		io.WriteString(w, "x")
		z.Close()
		return b.Bytes()
	}
	for _, data := range [][]byte{makeZip("../problem.yaml", 0644), makeZip("problem.yaml", os.ModeSymlink|0777), makeZip("/problem.yaml", 0644)} {
		if _, e = importProblem(data); e == nil {
			t.Fatal("unsafe archive accepted")
		}
	}
	p.Tests[0].Points++
	if p.Validate() == nil {
		t.Fatal("incorrect points accepted")
	}
	p = defaultProblem()
	p.Tests[1].ID = p.Tests[0].ID
	if p.Validate() == nil {
		t.Fatal("duplicate ID accepted")
	}
	p = defaultProblem()
	p.Limits.MemoryMB = 2048
	if p.Validate() == nil {
		t.Fatal("oversized memory accepted")
	}
}
func TestComparators(t *testing.T) {
	for _, c := range []struct {
		k, a, b string
		want    bool
	}{{"tokens", "1  2\n", "1\n2", true}, {"tokens", "01", "1", false}, {"exact", "a\r\n", "a\n", true}, {"exact", "a\n", "a", false}, {"tokens", "", " \n", true}} {
		if compareOutput(c.k, c.a, c.b) != c.want {
			t.Fatalf("comparison %+v", c)
		}
	}
}
func TestPasswordLogin(t *testing.T) {
	a, _ := testApp(t)
	if e := createUser(a.db, "real", "Real", "student", "correct-password"); e != nil {
		t.Fatal(e)
	}
	csrf := strings.Repeat("c", 64)
	r := httptest.NewRequest("POST", "/login", strings.NewReader(url.Values{"csrf": {csrf}, "login": {"real"}, "password": {"correct-password"}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(&http.Cookie{Name: "csrf", Value: csrf})
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, r)
	if w.Code != 303 {
		t.Fatalf("login failed %s", w.Body.String())
	}
	found := false
	for _, c := range w.Result().Cookies() {
		if c.Name == "session" {
			found = true
			if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode {
				t.Fatal("insecure session cookie")
			}
		}
	}
	if !found {
		t.Fatal("no session")
	}
}
func TestSandboxIntegration(t *testing.T) {
	if os.Getenv("RUN_ISOLATE_TESTS") != "1" {
		t.Skip("requires installed Isolate and dedicated idle box 0")
	}
	if e := sandboxCheck(); e != nil {
		t.Fatal(e)
	}
	cases := []struct{ name, lang, source, want string }{
		{"python", "python3", "print(sum(map(int,input().split())))", "AC"},
		{"cpp", "cpp17", "#include <iostream>\nint main(){long long a,b;std::cin>>a>>b;std::cout<<a+b;}", "AC"},
		{"wrong", "python3", "print(99)", "WA"},
		{"runtime", "python3", "raise ValueError('oops')", "RE"},
		{"compile", "cpp17", "int main( {", "CE"},
		{"cpu", "python3", "while True: pass", "TLE"},
		{"sleep", "python3", "import time;time.sleep(100)", "TLE"},
		{"memory", "python3", "x = bytearray(300*1024*1024)", "MLE"},
		{"output", "python3", "print('x'*20000)", "OLE"},
		{"isolation", "python3", "import os,socket\nassert not os.path.exists('/var/lib/contests/contests.db')\ns=socket.socket();s.settimeout(.1)\nassert s.connect_ex(('192.168.10.5',80)) != 0\nprint(5)", "AC"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := defaultProblem()
			p.Tests = p.Tests[:1]
			p.Tests[0].Points = 100
			p.Limits = Limits{200, 32, 8}
			status, score, _, _, e := judge(Submission{Language: c.lang, Source: c.source}, p)
			if e != nil {
				t.Fatal(e)
			}
			if status != c.want {
				t.Fatalf("want %s got %s", c.want, status)
			}
			if status == "AC" && score != 100 {
				t.Fatal("bad score")
			}
		})
	}
}
