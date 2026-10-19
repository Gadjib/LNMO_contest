package main

import (
	"strings"
	"testing"
)

func TestStudentRuntimeErrors(t *testing.T) {
	a, pid := testApp(t)
	p, e := getProblem(a.db, pid)
	if e != nil {
		t.Fatal(e)
	}
	results := []Result{
		{TestID: "01", Status: "RE", Error: "Traceback: ZeroDivisionError <script>alert(1)</script>", Output: "PRIVATE_STDOUT"},
		{TestID: "02", Status: "RE"},
		{TestID: "03", Status: "TLE", Error: "TIME_STDERR"},
		{TestID: "04", Status: "MLE", Error: "MEMORY_STDERR"},
		{TestID: "05", Status: "OLE", Error: "OUTPUT_STDERR"},
		{TestID: "06", Status: "WA", Error: "WA_PRIVATE"},
	}
	_, e = a.db.Exec("INSERT INTO submissions(id,user_id,problem_id,version_id,language,source,status,results,created) VALUES(1,1,?,?,'python3','1/0','RE',?,1)", pid, p.VersionID, encode(results))
	if e != nil {
		t.Fatal(e)
	}
	w := requestAs(t, a, 1, "GET", "/submissions/1", nil)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	for _, text := range []string{"ZeroDivisionError &lt;script&gt;", "Программа не оставила сообщения", "TIME_STDERR", "MEMORY_STDERR", "OUTPUT_STDERR"} {
		if !strings.Contains(w.Body.String(), text) {
			t.Fatal("missing", text)
		}
	}
	for _, text := range []string{"PRIVATE_STDOUT", "WA_PRIVATE", "<script>alert", "Ожидаемый ответ", "Данные теста"} {
		if strings.Contains(w.Body.String(), text) {
			t.Fatal("leaked", text)
		}
	}
	for _, path := range []string{"/submissions/1", "/submissions/1/status"} {
		if w := requestAs(t, a, 3, "GET", path, nil); w.Code != 404 {
			t.Fatal("another pupil can access errors")
		}
	}
	w = requestAs(t, a, 1, "GET", "/submissions/1/status", nil)
	if strings.Contains(w.Body.String(), "STDERR") || strings.Contains(w.Body.String(), "ZeroDivisionError") {
		t.Fatal("polling payload expanded")
	}
	w = requestAs(t, a, 2, "GET", "/submissions/1", nil)
	if !strings.Contains(w.Body.String(), "PRIVATE_STDOUT") || !strings.Contains(w.Body.String(), "ZeroDivisionError") {
		t.Fatal("teacher details broken")
	}
}
