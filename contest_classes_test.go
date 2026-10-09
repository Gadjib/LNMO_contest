package main

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestContestClassAccess(t *testing.T) {
	a, pid := testApp(t)
	if _, err := a.db.Exec("UPDATE users SET class_name='9м' WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	c, err := getContest(a.db, 1)
	if err != nil {
		t.Fatal(err)
	}
	c.Classes = []string{"9м", "10м"}
	if err = saveContest(a.db, &c); err != nil {
		t.Fatal(err)
	}
	p := defaultProblem()
	id := randomToken()
	imageURL := "/images/" + id
	p.Statement += "\n![diagram](" + imageURL + ")"
	p.Images = map[string]ProblemImage{id: {Data: imageFixture(t), MIME: "image/png"}}
	if _, err = saveProblem(a.db, 1, pid, p); err != nil {
		t.Fatal(err)
	}
	for _, uid := range []int64{1, 2, 3, 4} {
		allowed := uid != 3
		w := requestAs(t, a, uid, "GET", "/", nil)
		if strings.Contains(w.Body.String(), `href="/contests/1"`) != allowed {
			t.Fatalf("home uid=%d", uid)
		}
		for _, path := range []string{"/contests/1", "/problems/1", imageURL} {
			w = requestAs(t, a, uid, "GET", path, nil)
			want := 200
			if !allowed {
				want = 404
			}
			if w.Code != want {
				t.Fatalf("uid=%d path=%s status=%d", uid, path, w.Code)
			}
		}
	}
	if w := requestAs(t, a, 3, "POST", "/problems/1/submit", url.Values{"source": {"print(5)"}, "language": {"python3"}}); w.Code != 404 {
		t.Fatal("unauthorized submission accepted")
	}
	if w := requestAs(t, a, 1, "POST", "/problems/1/submit", url.Values{"source": {"print(5)"}, "language": {"python3"}}); w.Code != 303 {
		t.Fatal("authorized submission failed", w.Body.String())
	}
	// Current class membership takes effect immediately; history is retained.
	a.db.Exec("UPDATE users SET class_name='8м' WHERE id=1")
	if w := requestAs(t, a, 1, "GET", "/problems/1", nil); w.Code != 404 {
		t.Fatal("old class still allowed")
	}
	if w := requestAs(t, a, 1, "GET", "/submissions/1", nil); w.Code != 200 {
		t.Fatal("own history lost")
	}
	w := requestAs(t, a, 2, "GET", "/teacher/contests/1/results", nil)
	if !strings.Contains(w.Body.String(), "Ученик") || strings.Contains(w.Body.String(), "Другой ученик") {
		t.Fatal("results roster incorrect")
	}
	a.db.Exec("UPDATE users SET class_name='10м' WHERE id=3")
	if w := requestAs(t, a, 3, "GET", "/problems/1", nil); w.Code != 200 {
		t.Fatal("second class denied")
	}
	c.Starts = time.Now().Add(time.Hour).Unix()
	if err = saveContest(a.db, &c); err != nil {
		t.Fatal(err)
	}
	if w := requestAs(t, a, 3, "GET", "/contests/1", nil); w.Code != 200 {
		t.Fatal("future contest hidden")
	}
	if w := requestAs(t, a, 3, "GET", imageURL, nil); w.Code != 404 {
		t.Fatal("future image exposed")
	}
	c.Classes = nil
	c.Starts = 0
	if err = saveContest(a.db, &c); err != nil {
		t.Fatal(err)
	}
	if w := requestAs(t, a, 1, "GET", "/problems/1", nil); w.Code != 200 {
		t.Fatal("all students not restored")
	}
}

func TestContestClassEditor(t *testing.T) {
	a, _ := testApp(t)
	a.db.Exec("UPDATE users SET class_name='9м' WHERE id=1")
	form := url.Values{"title": {"Restricted"}, "description": {""}, "starts": {"2026-01-01T10:00"}, "ends": {"2027-01-01T10:00"}, "published": {"on"}, "audience": {"classes"}, "classes": {" 9М ", "10м", "9м"}}
	w := requestAs(t, a, 2, "POST", "/teacher/contests/new", form)
	if w.Code != 303 {
		t.Fatal(w.Code, w.Body.String())
	}
	c, err := getContest(a.db, 2)
	if err != nil || len(c.Classes) != 2 {
		t.Fatal(c, err)
	}
	w = requestAs(t, a, 2, "GET", "/teacher/contests/2/edit", nil)
	if !strings.Contains(w.Body.String(), `value="10м" checked`) {
		t.Fatal("class without pupils disappeared")
	}
	form.Del("classes")
	w = requestAs(t, a, 2, "POST", "/teacher/contests/2/edit", form)
	if !strings.Contains(w.Body.String(), "выберите от 1 до 100 классов") {
		t.Fatal("empty restriction accepted")
	}
	c, _ = getContest(a.db, 2)
	if len(c.Classes) != 2 {
		t.Fatal("invalid form changed restrictions")
	}
	form.Set("audience", "all")
	if w = requestAs(t, a, 2, "POST", "/teacher/contests/2/edit", form); w.Code != 303 {
		t.Fatal(w.Code)
	}
	c, _ = getContest(a.db, 2)
	if len(c.Classes) != 0 {
		t.Fatal("restrictions not cleared")
	}
}
