package main

import (
	"net/http"
	"strings"
)

type CompareLine struct {
	Number                        int
	Text, Prefix, Changed, Suffix string
}
type CompareRow struct {
	Left, Right CompareLine
	Kind        string
}
type CodeComparison struct {
	Left, Right Submission
	Rows        []CompareRow
	Score       int
	Simple      bool
}

func (a *App) compareSubmissions(w http.ResponseWriter, r *http.Request) {
	left, ok := a.authorizedSubmission(w, r)
	if !ok {
		return
	}
	viewer := currentUser(r)
	if e := a.loadSimilarity(&left, true, viewer); e != nil {
		a.fail(w, e)
		return
	}
	other, e := queryID(r, "other")
	if e != nil {
		http.Error(w, e.Error(), 400)
		return
	}
	if other == 0 && len(left.Matches) > 0 {
		other = left.Matches[0].ID
	}
	if other == 0 {
		a.render(w, r, "code-compare", Page{Title: "Сравнение кода", Comparison: &CodeComparison{Left: left}})
		return
	}
	right, e := getSubmission(a.db, other)
	if e != nil {
		http.NotFound(w, r)
		return
	}
	owner, e := getAccount(a.db, right.UserID)
	if e != nil || !viewer.AccessUser(owner) || right.ID == left.ID || right.ProblemID != left.ProblemID || right.Language != left.Language {
		http.NotFound(w, r)
		return
	}
	lo, hi := left.ID, right.ID
	if lo > hi {
		lo, hi = hi, lo
	}
	var score int
	if e = a.db.QueryRow("SELECT score FROM code_matches WHERE left_id=? AND right_id=?", lo, hi).Scan(&score); e != nil {
		http.NotFound(w, r)
		return
	}
	rows, simple := compareCodeLines(left.Source, right.Source)
	a.render(w, r, "code-compare", Page{Title: "Сравнение кода", Comparison: &CodeComparison{Left: left, Right: right, Rows: rows, Score: score, Simple: simple}})
}

func compareCodeLines(a, b string) ([]CompareRow, bool) {
	left := strings.Split(strings.ReplaceAll(a, "\r\n", "\n"), "\n")
	right := strings.Split(strings.ReplaceAll(b, "\r\n", "\n"), "\n")
	simple := len(left)*len(right) > 1000000
	var rows []CompareRow
	add := func(i, j int) {
		row := CompareRow{Kind: "changed"}
		if i >= 0 {
			row.Left = CompareLine{Number: i + 1, Text: left[i]}
		}
		if j >= 0 {
			row.Right = CompareLine{Number: j + 1, Text: right[j]}
		}
		if i < 0 {
			row.Kind = "added"
		} else if j < 0 {
			row.Kind = "removed"
		} else if left[i] == right[j] {
			row.Kind = "same"
		} else {
			x, y := []rune(left[i]), []rune(right[j])
			p, s := 0, 0
			for p < len(x) && p < len(y) && x[p] == y[p] {
				p++
			}
			for s < len(x)-p && s < len(y)-p && x[len(x)-1-s] == y[len(y)-1-s] {
				s++
			}
			row.Left.Prefix = string(x[:p])
			row.Left.Changed = string(x[p : len(x)-s])
			row.Left.Suffix = string(x[len(x)-s:])
			row.Right.Prefix = string(y[:p])
			row.Right.Changed = string(y[p : len(y)-s])
			row.Right.Suffix = string(y[len(y)-s:])
		}
		rows = append(rows, row)
	}
	if simple {
		for i := 0; i < len(left) || i < len(right); i++ {
			l, r := -1, -1
			if i < len(left) {
				l = i
			}
			if i < len(right) {
				r = i
			}
			add(l, r)
		}
		return rows, true
	}
	width := len(right) + 1
	dp := make([]int32, (len(left)+1)*width)
	for i := len(left) - 1; i >= 0; i-- {
		for j := len(right) - 1; j >= 0; j-- {
			if left[i] == right[j] {
				dp[i*width+j] = 1 + dp[(i+1)*width+j+1]
			} else {
				dp[i*width+j] = max(dp[(i+1)*width+j], dp[i*width+j+1])
			}
		}
	}
	i, j := 0, 0
	for i < len(left) || j < len(right) {
		if i < len(left) && j < len(right) && left[i] == right[j] {
			add(i, j)
			i++
			j++
			continue
		}
		li, rj := i, j
		for (i < len(left) || j < len(right)) && !(i < len(left) && j < len(right) && left[i] == right[j]) {
			if j == len(right) || (i < len(left) && dp[(i+1)*width+j] >= dp[i*width+j+1]) {
				i++
			} else {
				j++
			}
		}
		for k := 0; k < i-li || k < j-rj; k++ {
			l, r := -1, -1
			if k < i-li {
				l = li + k
			}
			if k < j-rj {
				r = rj + k
			}
			add(l, r)
		}
	}
	return rows, false
}
