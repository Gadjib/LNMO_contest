package main

import (
	"strings"
	"testing"
)

func TestCodeEditorPages(t *testing.T) {
	a, pid := testApp(t)
	p, err := getProblem(a.db, pid)
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.db.Exec(`INSERT INTO submissions(id,user_id,problem_id,version_id,language,source,status,created) VALUES(1,1,?,?,'cpp17',?,'AC',1)`, pid, p.VersionID, `std::cout << "<script>alert(1)</script>";`)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/problems/1", "/submissions/1"} {
		w := requestAs(t, a, 2, "GET", path, nil)
		body := w.Body.String()
		if w.Code != 200 || !strings.Contains(body, "/static/code-editor.js") || !strings.Contains(body, "/static/code-editor.css") {
			t.Fatalf("missing editor assets: %s (%d)", path, w.Code)
		}
		if path == "/submissions/1" && (!strings.Contains(body, `data-language="cpp17"`) || strings.Contains(body, "<script>alert")) {
			t.Fatal("source language or escaping incorrect")
		}
	}
	for _, path := range []string{"/static/code-editor.js", "/static/code-editor.css"} {
		if w := requestAs(t, a, 2, "GET", path, nil); w.Code != 200 {
			t.Fatalf("missing asset %s", path)
		}
	}
}
