package main

import (
	"strings"
	"testing"
)

func TestCompareLines(t *testing.T) {
	for _, pair := range [][2]string{{"a\nb\nc", "a\ninsert\nb\nc"}, {"one\nПривет\nthree", "one\nПривёт\nthree"}, {"", ""}, {"<script>\n\tfoo", "other"}, {strings.Repeat("a\n", 1100), strings.Repeat("b\n", 1100)}} {
		rows, _ := compareCodeLines(pair[0], pair[1])
		var a, b []string
		for _, r := range rows {
			if r.Left.Number > 0 {
				a = append(a, r.Left.Text)
			}
			if r.Right.Number > 0 {
				b = append(b, r.Right.Text)
			}
			if r.Kind == "same" && r.Left.Text != r.Right.Text {
				t.Fatal("false match")
			}
		}
		if strings.Join(a, "\n") != pair[0] || strings.Join(b, "\n") != pair[1] {
			t.Fatal("lost source")
		}
	}
	rows, _ := compareCodeLines("a\nb", "a\nx\nb")
	if len(rows) != 3 || rows[1].Kind != "added" || rows[2].Kind != "same" {
		t.Fatal(rows)
	}
	rows, _ = compareCodeLines("print(1)", "print(2)")
	if rows[0].Left.Changed != "1" || rows[0].Right.Changed != "2" {
		t.Fatal(rows)
	}
}
func TestCompareAccess(t *testing.T) {
	a, _ := scopedFixture(t)
	if _, e := a.db.Exec("INSERT INTO code_matches(left_id,right_id,score) VALUES(1,3,95)"); e != nil {
		t.Fatal(e)
	}
	a.db.Exec("UPDATE submissions SET source=? WHERE id=3", "<script>alert('x')</script>\nPRIVATE_CODE")
	for _, path := range []string{"/submissions/1/compare?other=3", "/submissions/3/compare?other=1"} {
		if w := requestAs(t, a, 1, "GET", path, nil); w.Code != 403 {
			t.Fatal("student allowed", w.Code)
		}
		if w := requestAs(t, a, 2, "GET", path, nil); w.Code != 404 {
			t.Fatal("foreign class allowed", w.Code)
		}
		w := requestAs(t, a, 4, "GET", path, nil)
		if w.Code != 200 || !strings.Contains(w.Body.String(), "PRIVATE_CODE") || strings.Contains(w.Body.String(), "<script>alert") {
			t.Fatal("admin comparison", w.Code)
		}
	}
	w := requestAs(t, a, 2, "GET", "/submissions/1/compare", nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), "PRIVATE_CODE") {
		t.Fatal("default leaks")
	}
	a.db.Exec("INSERT INTO teacher_classes(user_id,class_name) VALUES(2,'9м')")
	w = requestAs(t, a, 2, "GET", "/submissions/1/compare", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "PRIVATE_CODE") {
		t.Fatal("default match missing")
	}
	if w := requestAs(t, a, 4, "GET", "/submissions/1/compare?other=1", nil); w.Code != 404 {
		t.Fatal("self compare")
	}
	a.db.Exec("DELETE FROM code_matches")
	if w := requestAs(t, a, 4, "GET", "/submissions/1/compare?other=3", nil); w.Code != 404 {
		t.Fatal("unrelated pair allowed")
	}
}
