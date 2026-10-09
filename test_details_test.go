package main

import (
	"strings"
	"testing"
)

func TestTeacherSeesOriginalTestData(t *testing.T) {
	a, pid := testApp(t)
	p, _ := getProblem(a.db, pid)
	p.Spec.Tests[0].Input = "PRIVATE_ORIGINAL_INPUT\n"
	p.Spec.Tests[0].Answer = "PRIVATE_ORIGINAL_ANSWER\n"
	p.Spec.Tests[1].Input = ""
	p.Spec.Tests[1].Answer = ""
	if _, e := saveProblem(a.db, 1, pid, p.Spec); e != nil {
		t.Fatal(e)
	}
	p, _ = getProblem(a.db, pid)
	results := []Result{{TestID: "01", Status: "WA", Output: "<script>privateOutput()</script>", Error: "PRIVATE_STDERR"}, {TestID: "02", Status: "RE"}}
	if _, e := a.db.Exec("INSERT INTO submissions(id,user_id,problem_id,version_id,language,source,status,results,created) VALUES(1,1,?,?,'python3','print(0)','WA',?,1)", pid, p.VersionID, encode(results)); e != nil {
		t.Fatal(e)
	}
	p.Spec.Tests[0].Input = "NEW_TEST_INPUT"
	p.Spec.Tests[0].Answer = "NEW_TEST_ANSWER"
	if _, e := saveProblem(a.db, 1, pid, p.Spec); e != nil {
		t.Fatal(e)
	}
	w := requestAs(t, a, 2, "GET", "/submissions/1", nil)
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, s := range []string{"PRIVATE_ORIGINAL_INPUT", "PRIVATE_ORIGINAL_ANSWER", "PRIVATE_STDERR", "&lt;script&gt;privateOutput()&lt;/script&gt;", "Нет вывода", "Пустой ввод", "Пустой ответ", "Данные теста 02"} {
		if !strings.Contains(body, s) {
			t.Fatalf("missing %q", s)
		}
	}
	for _, s := range []string{"NEW_TEST_INPUT", "NEW_TEST_ANSWER", "<script>privateOutput()"} {
		if strings.Contains(body, s) {
			t.Fatalf("unsafe or wrong data: %q", s)
		}
	}
	for _, route := range []string{"/submissions/1", "/submissions/1/status"} {
		w = requestAs(t, a, 1, "GET", route, nil)
		for _, s := range []string{"PRIVATE_ORIGINAL", "PRIVATE_STDERR", "privateOutput", "Данные теста"} {
			if strings.Contains(w.Body.String(), s) {
				t.Fatalf("private data leaked at %s", route)
			}
		}
	}
	w = requestAs(t, a, 3, "GET", "/submissions/1", nil)
	if w.Code != 404 {
		t.Fatal("other student accessed submission")
	}
	submission, e := getSubmission(a.db, 1)
	if e != nil {
		t.Fatal(e)
	}
	if e = a.loadTestDetails(&submission); e != nil {
		t.Fatal(e)
	}
	if strings.Contains(encode(submission.Results), "PRIVATE_ORIGINAL") {
		t.Fatal("test fixtures serialized into result JSON")
	}
}
