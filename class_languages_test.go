package main

import (
	"net/url"
	"strings"
	"testing"
)

func TestClassLanguages(t *testing.T) {
	a, pid := testApp(t)
	if _, e := a.db.Exec("UPDATE users SET class_name='8м-1' WHERE id=1"); e != nil {
		t.Fatal(e)
	}
	for _, uid := range []int64{1, 2} {
		for _, method := range []string{"GET", "POST"} {
			if w := requestAs(t, a, uid, method, "/admin/classes", url.Values{"class_name": {"8м-1"}, "mode": {"python3"}}); w.Code != 403 {
				t.Fatal("unauthorized settings", uid, method, w.Code)
			}
		}
	}
	for _, mode := range []string{"python3", "cpp17", "both"} {
		w := requestAs(t, a, 4, "POST", "/admin/classes", url.Values{"class_name": {"8м-1"}, "mode": {mode}})
		if w.Code != 303 {
			t.Fatal(w.Code, w.Body.String())
		}
		w = requestAs(t, a, 4, "GET", "/admin/classes", nil)
		if w.Code != 200 || !strings.Contains(w.Body.String(), `value="`+mode+`" selected`) {
			t.Fatal("settings not persisted")
		}
		for _, uid := range []int64{1, 2, 3} {
			w = requestAs(t, a, uid, "GET", "/problems/1", nil)
			for _, lang := range []string{"python3", "cpp17"} {
				allowed := uid != 1 || mode == "both" || lang == mode
				if strings.Contains(w.Body.String(), `<option value="`+lang+`">`) != allowed {
					t.Fatal("wrong language options", uid, mode, lang)
				}
				// Clear only fixture submissions so rate limits don't hide policy errors.
				a.db.Exec("DELETE FROM submissions")
				sent := requestAs(t, a, uid, "POST", "/problems/1/submit", url.Values{"language": {lang}, "source": {"print(1)"}})
				var count int
				a.db.QueryRow("SELECT count(*) FROM submissions").Scan(&count)
				if (sent.Code == 303) != allowed || (count == 1) != allowed {
					t.Fatal("submission policy", uid, mode, lang, sent.Code, count)
				}
			}
		}
	}
	if w := requestAs(t, a, 4, "POST", "/admin/classes", url.Values{"class_name": {"8м-1"}, "mode": {"ruby"}}); w.Code != 400 {
		t.Fatal("invalid language accepted")
	}
	requestAs(t, a, 4, "POST", "/admin/classes", url.Values{"class_name": {"8м-1"}, "mode": {"cpp17"}})
	p := defaultProblem()
	p.AllowedLanguages = []string{"python3"}
	if _, e := saveProblem(a.db, 1, pid, p); e != nil {
		t.Fatal(e)
	}
	w := requestAs(t, a, 1, "GET", "/problems/1", nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), `name="source"`) || !strings.Contains(w.Body.String(), "нет доступного языка") {
		t.Fatal("empty intersection", w.Code)
	}
	// Moving a student immediately adopts the destination class policy.
	a.db.Exec("UPDATE users SET class_name='9м' WHERE id=1")
	w = requestAs(t, a, 1, "GET", "/problems/1", nil)
	if !strings.Contains(w.Body.String(), `<option value="python3">`) {
		t.Fatal("stale class policy")
	}
	// Also support pupils without a class.
	requestAs(t, a, 4, "POST", "/admin/classes", url.Values{"class_name": {""}, "mode": {"cpp17"}})
	w = requestAs(t, a, 3, "GET", "/problems/1", nil)
	if strings.Contains(w.Body.String(), `name="source"`) {
		t.Fatal("classless policy ignored")
	}
}
