package main

import (
	"html"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

func TestResultMultipleClasses(t *testing.T) {
	a, _ := scopedFixture(t)
	_, e := a.db.Exec("INSERT INTO users(id,login,name,password,role,class_name) VALUES(5,'third','THIRD_PUPIL','unused','student','10м'),(6,'none','CLASSLESS_PUPIL','unused','student','')")
	if e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		classes []string
		uid     int64
		yes, no []string
	}{
		{[]string{"8м", "9м"}, 4, []string{"VISIBLE_PUPIL", "PRIVATE_PUPIL"}, []string{"THIRD_PUPIL", "CLASSLESS_PUPIL"}},
		{[]string{"8м", "9м"}, 2, []string{"VISIBLE_PUPIL"}, []string{"PRIVATE_PUPIL", "THIRD_PUPIL", "CLASSLESS_PUPIL"}},
		{[]string{"~", "9м"}, 4, []string{"PRIVATE_PUPIL", "CLASSLESS_PUPIL"}, []string{"VISIBLE_PUPIL", "THIRD_PUPIL"}},
		{[]string{"8м"}, 4, []string{"VISIBLE_PUPIL"}, []string{"PRIVATE_PUPIL", "THIRD_PUPIL"}},
		{nil, 4, []string{"VISIBLE_PUPIL", "PRIVATE_PUPIL", "THIRD_PUPIL", "CLASSLESS_PUPIL"}, nil},
		{[]string{"8м' OR 1=1 --"}, 4, nil, []string{"VISIBLE_PUPIL", "PRIVATE_PUPIL"}},
	} {
		w := requestAs(t, a, tc.uid, "GET", "/teacher/results?"+url.Values{"class": tc.classes}.Encode(), nil)
		if w.Code != 200 {
			t.Fatal(w.Code)
		}
		for _, name := range tc.yes {
			if !strings.Contains(w.Body.String(), name) {
				t.Fatal("missing", name, tc.classes)
			}
		}
		for _, name := range tc.no {
			if strings.Contains(w.Body.String(), name) {
				t.Fatal("leaked", name, tc.classes)
			}
		}
	}
	_, e = a.db.Exec(`WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<55)
 INSERT INTO users(login,name,password,role,class_name) SELECT 'page'||x,'Page pupil '||x,'unused','student','8м' FROM n`)
	if e != nil {
		t.Fatal(e)
	}
	path := "/teacher/results?" + url.Values{"class": {"8м", "9м", "8м"}, "contest": {"1"}}.Encode()
	w := requestAs(t, a, 4, "GET", path, nil)
	for _, cl := range []string{"8м", "9м"} {
		if !strings.Contains(w.Body.String(), `value="`+cl+`" checked`) {
			t.Fatal("checkbox not retained")
		}
	}
	match := regexp.MustCompile(`href="([^"]+)">Далее`).FindStringSubmatch(w.Body.String())
	if len(match) != 2 {
		t.Fatal("no next link")
	}
	u, e := url.Parse(html.UnescapeString(match[1]))
	if e != nil {
		t.Fatal(e)
	}
	if strings.Join(u.Query()["class"], ",") != "8м,9м" || u.Query().Get("contest") != "1" {
		t.Fatal("lost filters", u)
	}
	w = requestAs(t, a, 4, "GET", u.String(), nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), "THIRD_PUPIL") {
		t.Fatal("pagination filter")
	}
}
