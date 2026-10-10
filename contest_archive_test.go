package main

import (
	"archive/zip"
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestContestExample(t *testing.T) {
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	err := filepath.WalkDir("examples/contest", func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		name, err := filepath.Rel("examples/contest", path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		w, err := z.Create(filepath.ToSlash(name))
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = z.Close(); err != nil {
		t.Fatal(err)
	}
	c, ps, err := importContest(b.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if c.Title != "Арифметика" || len(ps) != 2 || ps[1].Tests[0].Answer != "6\n" {
		t.Fatal("invalid example")
	}
}

func contestFixture(t *testing.T) (Contest, []ProblemSpec, []byte) {
	t.Helper()
	c := Contest{Title: "Архив контеста", Description: "Описание", Starts: 1700000000, Ends: 1900000000, Published: true, Classes: []string{"8м", "9м"}}
	first, second := defaultProblem(), defaultProblem()
	second.Code = "B"
	second.Title = "Вторая задача"
	id := randomToken()
	second.Statement += "\n![Схема](/images/" + id + ")"
	second.Images = map[string]ProblemImage{id: {Data: imageFixture(t), MIME: "image/png"}}
	specs := []ProblemSpec{first, second}
	data, e := exportContest(c, specs)
	if e != nil {
		t.Fatal(e)
	}
	return c, specs, data
}

func TestContestArchiveRoundTrip(t *testing.T) {
	_, _, data := contestFixture(t)
	c, ps, e := importContest(data)
	if e != nil {
		t.Fatal(e)
	}
	if c.Title != "Архив контеста" || c.Published || len(c.Classes) != 2 || len(ps) != 2 || ps[0].Code != "sum" || ps[1].Code != "B" || len(ps[1].Images) != 1 {
		t.Fatal("roundtrip failed", c)
	}
	a, _ := testApp(t)
	id, e := saveImportedContest(a.db, c, ps)
	if e != nil {
		t.Fatal(e)
	}
	saved, e := getContest(a.db, id)
	if e != nil || saved.Published || len(saved.Classes) != 2 {
		t.Fatal("draft not saved", e)
	}
	var n int
	a.db.QueryRow("SELECT count(*) FROM problems WHERE contest_id=?", id).Scan(&n)
	if n != 2 {
		t.Fatal(n)
	}
	w := requestAs(t, a, 2, "GET", "/teacher/contests/2/export", nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, ps, e = importContest(w.Body.Bytes()); e != nil || len(ps) != 2 {
		t.Fatal(e)
	}
	if w = requestAs(t, a, 1, "GET", "/teacher/contests/2/export", nil); w.Code != 403 {
		t.Fatal("student exported hidden tests")
	}
	if w = requestAs(t, a, 1, "GET", "/contests/2", nil); w.Code != 404 {
		t.Fatal("draft exposed")
	}
}

func TestContestArchiveAtomic(t *testing.T) {
	c, ps, _ := contestFixture(t)
	a, _ := testApp(t)
	// A storage-stage failure in the second task must roll back the first task,
	// the contest, class bindings and all images.
	ps[1].Images = nil
	if _, e := saveImportedContest(a.db, c, ps); e == nil {
		t.Fatal("missing image accepted")
	}
	for table, want := range map[string]int{"contests": 1, "problems": 1, "versions": 1, "contest_classes": 0, "problem_images": 0} {
		var n int
		a.db.QueryRow("SELECT count(*) FROM " + table).Scan(&n)
		if n != want {
			t.Fatalf("%s: %d", table, n)
		}
	}
}

func TestContestArchiveInvalid(t *testing.T) {
	_, _, data := contestFixture(t)
	tests := []struct {
		name    string
		rewrite func(string, []byte) (string, []byte)
		extra   bool
	}{
		{"unknown field", func(n string, b []byte) (string, []byte) {
			if n == "contest.yaml" {
				b = append(b, []byte("published: true\n")...)
			}
			return n, b
		}, false},
		{"missing task", func(n string, b []byte) (string, []byte) {
			if n == "contest.yaml" {
				b = bytes.ReplaceAll(b, []byte("problems/B"), []byte("problems/missing"))
			}
			return n, b
		}, false},
		{"duplicate code", func(n string, b []byte) (string, []byte) {
			if n == "problems/B/problem.yaml" {
				b = bytes.ReplaceAll(b, []byte("code: B"), []byte("code: sum"))
			}
			return n, b
		}, false},
		{"traversal", func(n string, b []byte) (string, []byte) {
			if strings.HasSuffix(n, "001.in") {
				n = "../" + n
			}
			return n, b
		}, false},
		{"extra file", func(n string, b []byte) (string, []byte) { return n, b }, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := zip.NewReader(bytes.NewReader(data), int64(len(data)))
			var b bytes.Buffer
			z := zip.NewWriter(&b)
			for _, f := range r.File {
				in, _ := f.Open()
				raw, _ := io.ReadAll(in)
				in.Close()
				n, raw := tc.rewrite(f.Name, raw)
				out, _ := z.Create(n)
				out.Write(raw)
			}
			if tc.extra {
				out, _ := z.Create("unlisted.txt")
				out.Write([]byte("unexpected"))
			}
			z.Close()
			if _, _, e := importContest(b.Bytes()); e == nil {
				t.Fatal("invalid archive accepted")
			}
		})
	}
	if _, _, e := importContest(make([]byte, maxArchiveBytes+1)); e == nil {
		t.Fatal("oversize accepted")
	}
}

func TestContestArchiveHTTP(t *testing.T) {
	a, _ := testApp(t)
	_, _, data := contestFixture(t)
	for _, tc := range []struct {
		uid    int64
		csrf   bool
		status int
	}{{1, true, 403}, {2, false, 403}, {2, true, 303}} {
		token := randomToken()
		a.db.Exec("INSERT INTO sessions(token,user_id,expires) VALUES(?,?,?)", tokenHash(token), tc.uid, time.Now().Add(time.Hour).Unix())
		var b bytes.Buffer
		m := multipart.NewWriter(&b)
		csrf := strings.Repeat("c", 64)
		if tc.csrf {
			m.WriteField("csrf", csrf)
		}
		f, _ := m.CreateFormFile("archive", "contest.zip")
		f.Write(data)
		m.Close()
		r := httptest.NewRequest("POST", "/teacher/contests/import", &b)
		r.Header.Set("Content-Type", m.FormDataContentType())
		r.AddCookie(&http.Cookie{Name: "csrf", Value: csrf})
		r.AddCookie(&http.Cookie{Name: "session", Value: token})
		w := httptest.NewRecorder()
		a.routes().ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	for _, path := range []string{"/", "/contests/1", "/teacher/contests/import"} {
		if w := requestAs(t, a, 2, "GET", path, nil); w.Code != 200 {
			t.Fatal(path, w.Code)
		}
	}
}
