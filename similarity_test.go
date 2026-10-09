package main

import (
	"strings"
	"testing"
)

const similaritySample = `n = int(input())
values = list(map(int, input().split()))
total = 0
for value in values:
    if value > 0:
        total += value
print(total)
`

func TestCodeSimilarity(t *testing.T) {
	renamed := strings.NewReplacer("values", "items", "total", "answer", "value", "element").Replace(similaritySample)
	for _, tc := range []struct {
		name, lang, a, b string
		min, max         int
	}{
		{"exact short", "python3", "print(5)", "print(5)", 100, 100},
		{"python comments", "python3", "print(5) # hello", "  print ( 5 )\n", 100, 100},
		{"cpp comments", "cpp17", "int main(){/*hi*/return 0;}", "int main() { // hi\nreturn 0; }", 100, 100},
		{"renamed", "python3", similaritySample, renamed, 100, 100},
		{"small edit", "python3", similaritySample, strings.Replace(similaritySample, "> 0", ">= 0", 1), 85, 99},
		{"literal spaces", "python3", `print("a b")`, `print("a  b")`, 0, 0},
		{"literal comments", "cpp17", `cout << "// hello";`, `cout << "// world";`, 0, 0},
		{"empty", "python3", "# hello", "# hello", 0, 0},
		{"short renamed", "python3", "a=int(input());print(a)", "b=int(input());print(b)", 0, 0},
		{"unrelated", "python3", similaritySample, `def factorial(x):
 if x <= 1: return 1
 return x * factorial(x-1)
print(factorial(int(input())))`, 0, 60},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b := fingerprint(tc.a, tc.lang), fingerprint(tc.b, tc.lang)
			got := similarity(a, b)
			if got < tc.min || got > tc.max {
				t.Fatalf("score %d outside %d..%d", got, tc.min, tc.max)
			}
			if got != similarity(b, a) {
				t.Fatal("asymmetric score")
			}
		})
	}
}

func TestSimilarityBackfillAndPermissions(t *testing.T) {
	a, pid := testApp(t)
	p, _ := getProblem(a.db, pid)
	add := func(uid, problem, version int64, lang, source string) int64 {
		t.Helper()
		r, e := a.db.Exec("INSERT INTO submissions(user_id,problem_id,version_id,language,source,status,score,created) VALUES(?,?,?,?,?,'AC',100,1)", uid, problem, version, lang, source)
		if e != nil {
			t.Fatal(e)
		}
		id, _ := r.LastInsertId()
		return id
	}
	first := add(1, pid, p.VersionID, "python3", similaritySample)
	retry := add(1, pid, p.VersionID, "python3", similaritySample)
	renamed := strings.NewReplacer("values", "items", "total", "answer", "value", "element").Replace(similaritySample)
	other := add(3, pid, p.VersionID, "python3", renamed)
	teacher := add(2, pid, p.VersionID, "python3", similaritySample)
	foreignLang := add(3, pid, p.VersionID, "cpp17", similaritySample)
	spec := defaultProblem()
	spec.Code = "different"
	pid2, e := saveProblem(a.db, 1, 0, spec)
	if e != nil {
		t.Fatal(e)
	}
	p2, _ := getProblem(a.db, pid2)
	foreignProblem := add(3, pid2, p2.VersionID, "python3", similaritySample)
	for i := 0; i < 6; i++ {
		indexed, e := indexNextSimilarity(a.db)
		if e != nil || !indexed {
			t.Fatalf("backfill: %v %v", indexed, e)
		}
	}
	if indexed, e := indexNextSimilarity(a.db); e != nil || indexed {
		t.Fatal("indexing not idempotent")
	}
	for _, id := range []int64{first, retry, other} {
		s, e := getSubmission(a.db, id)
		if e != nil {
			t.Fatal(e)
		}
		if e = a.loadSimilarity(&s, true); e != nil {
			t.Fatal(e)
		}
		if !s.SimilarityChecked || s.SimilarityScore != 100 || len(s.Matches) != 1 {
			t.Fatalf("bad match info: %+v", s)
		}
		if s.Status != "AC" || s.Score != 100 {
			t.Fatal("grade changed")
		}
	}
	for _, id := range []int64{teacher, foreignLang, foreignProblem} {
		s, _ := getSubmission(a.db, id)
		if e = a.loadSimilarity(&s, true); e != nil {
			t.Fatal(e)
		}
		if s.SimilarityScore != 0 {
			t.Fatal("out-of-scope match")
		}
	}
	w := requestAs(t, a, 2, "GET", "/submissions/1", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Высокое сходство кода: 100%") || !strings.Contains(w.Body.String(), "Другой ученик") {
		t.Fatalf("teacher warning missing: %s", w.Body.String())
	}
	w = requestAs(t, a, 1, "GET", "/submissions/1", nil)
	if strings.Contains(w.Body.String(), "Другой ученик") || strings.Contains(w.Body.String(), "Высокое сходство") {
		t.Fatal("match leaked to student")
	}
	w = requestAs(t, a, 1, "GET", "/submissions/1/status", nil)
	if strings.Contains(w.Body.String(), "similarity") {
		t.Fatal("status leaked similarity")
	}
	w = requestAs(t, a, 2, "GET", "/submissions", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "#similarity") {
		t.Fatal("list warning missing")
	}
	// A later match must update both sides, including previously indexed attempts.
	later := add(3, pid, p.VersionID, "python3", "print(123)")
	indexNextSimilarity(a.db)
	add(1, pid, p.VersionID, "python3", "print(123)")
	indexNextSimilarity(a.db)
	s, _ := getSubmission(a.db, later)
	a.loadSimilarity(&s, true)
	if s.SimilarityScore != 100 {
		t.Fatal("older attempt did not get a match")
	}
}
