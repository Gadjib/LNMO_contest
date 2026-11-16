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

func TestCompareStructureAlignment(t *testing.T) {
	left := "#include <string>\nvoid calculate() {\n    fstream source(\"input.txt\", ios::in);\n    fstream target(\"output.txt\", ios::out);\n    int total = 0, value;\n\n    if (source.is_open()) {\n        while (source >> value) {\n            total += value;\n        }\n    }\n    target << total;\n}\nint main(){\n\ncalculate();\n}"
	right := "\nvoid solve() {\n    fstream input(\"input.txt\", ios::in);\n    fstream output(\"output.txt\", ios::out);\n    int result = 0;\n    int item;\n\n    if (input.is_open()) {\n        while (input >> item) {\n            result += item;\n        }\n    }\n    output << result;\n}\n\nint main(){\n    solve();\n}"
	rows, simple := compareCodeLines(left, right)
	if simple {
		t.Fatal("unexpected fallback")
	}
	want := map[int]int{2: 2, 3: 3, 4: 4, 5: 5, 7: 8, 8: 9, 9: 10, 12: 13, 14: 16, 16: 17}
	for _, row := range rows {
		if expected, ok := want[row.Left.Number]; ok {
			if row.Right.Number != expected {
				t.Errorf("left line %d paired with %d, want %d", row.Left.Number, row.Right.Number, expected)
			}
			delete(want, row.Left.Number)
		}
	}
	if len(want) > 0 {
		t.Fatal("missing lines", want)
	}
	assertComparisonSource(t, left, right, rows)
}

func assertComparisonSource(t *testing.T, left, right string, rows []CompareRow) {
	t.Helper()
	var a, b []string
	for _, row := range rows {
		for i, line := range []CompareLine{row.Left, row.Right} {
			if line.Number == 0 {
				continue
			}
			if row.Kind == "changed" && line.Prefix+line.Changed+line.Suffix != line.Text {
				t.Fatal("highlight lost text")
			}
			if i == 0 {
				if line.Number != len(a)+1 {
					t.Fatal("left order")
				}
				a = append(a, line.Text)
			} else {
				if line.Number != len(b)+1 {
					t.Fatal("right order")
				}
				b = append(b, line.Text)
			}
		}
	}
	if strings.Join(a, "\n") != strings.ReplaceAll(left, "\r\n", "\n") || strings.Join(b, "\n") != strings.ReplaceAll(right, "\r\n", "\n") {
		t.Fatal("lost source")
	}
}

func TestCompareRelatedLinesOnly(t *testing.T) {
	for _, pair := range [][2]string{
		{"void original() {", "void renamed () {"},
		{"Привет мир", "Привёт мир"},
		{"\tprint(1)", "  print(2)"},
	} {
		rows, _ := compareCodeLines(pair[0], pair[1])
		if len(rows) != 1 || rows[0].Kind != "changed" {
			t.Fatal("related lines separated", rows)
		}
		assertComparisonSource(t, pair[0], pair[1], rows)
	}
	for _, pair := range [][2]string{
		{"if (ready) {", "void solve() {"},
		{"return 42;", "fstream input(\"data\");"},
		{"alpha", "omega"},
		{strings.Repeat("a", 5000), strings.Repeat("b", 5000)},
	} {
		rows, _ := compareCodeLines(pair[0], pair[1])
		for _, row := range rows {
			if row.Left.Number > 0 && row.Right.Number > 0 {
				t.Fatal("unrelated lines paired", row)
			}
		}
		assertComparisonSource(t, pair[0], pair[1], rows)
	}
}

func TestCompareBoundedAlignment(t *testing.T) {
	left := strings.Repeat("int value = 1;\n", 1100)
	right := "void inserted() {}\n" + strings.Repeat("int renamed = 1;\n", 1100)
	rows, simple := compareCodeLines(left, right)
	if !simple || rows[0].Kind != "added" || rows[1].Kind != "changed" {
		t.Fatal("bounded alignment failed")
	}
	assertComparisonSource(t, left, right, rows)
	rows, _ = compareCodeLines("a\r\n\t\r\n", "a\n\n")
	assertComparisonSource(t, "a\r\n\t\r\n", "a\n\n", rows)
}
