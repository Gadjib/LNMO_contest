package main

import (
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"
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
	// Bound both memory and work, including extremely long individual lines.
	simple := len(left) > 1000000/len(right)
	ls, rs := comparisonSignatures(left), comparisonSignatures(right)
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
		// A bounded lookahead still handles nearby insertions. Never present
		// unrelated lines as a replacement just because their numbers agree.
		for i, j := 0, 0; i < len(left) || j < len(right); {
			bi, bj, best := -1, -1, 0
			for k := 0; k < 32; k++ {
				if i+k < len(left) && j < len(right) {
					if score := comparisonLineScore(ls[i+k], rs[j]) - k*4; score > best {
						bi, bj, best = i+k, j, score
					}
				}
				if i < len(left) && j+k < len(right) {
					if score := comparisonLineScore(ls[i], rs[j+k]) - k*4; score > best {
						bi, bj, best = i, j+k, score
					}
				}
			}
			if best > 0 {
				for i < bi {
					add(i, -1)
					i++
				}
				for j < bj {
					add(-1, j)
					j++
				}
				add(i, j)
				i++
				j++
			} else if i < len(left) {
				add(i, -1)
				i++
			} else {
				add(-1, j)
				j++
			}
		}
		return rows, true
	}
	width := len(right) + 1
	dp := make([]int32, (len(left)+1)*width)
	matches := make([]uint8, len(left)*len(right))
	for i := len(left) - 1; i >= 0; i-- {
		for j := len(right) - 1; j >= 0; j-- {
			score := comparisonLineScore(ls[i], rs[j])
			matches[i*len(right)+j] = uint8(score)
			dp[i*width+j] = max(dp[(i+1)*width+j], dp[i*width+j+1])
			if score > 0 {
				dp[i*width+j] = max(dp[i*width+j], int32(score)+dp[(i+1)*width+j+1])
			}
		}
	}
	i, j := 0, 0
	for i < len(left) || j < len(right) {
		if i < len(left) && j < len(right) && matches[i*len(right)+j] > 0 && dp[i*width+j] == int32(matches[i*len(right)+j])+dp[(i+1)*width+j+1] {
			add(i, j)
			i++
			j++
			continue
		}
		if j == len(right) || (i < len(left) && dp[(i+1)*width+j] >= dp[i*width+j+1]) {
			add(i, -1)
			i++
		} else {
			add(-1, j)
			j++
		}
	}
	return rows, false
}

type comparisonSignature struct {
	text, trimmed, shape string
	tokens               []string
	runes                []rune
	bigrams              map[string]int
	weight               int
}

func comparisonSignatures(lines []string) []comparisonSignature {
	result := make([]comparisonSignature, len(lines))
	for i, line := range lines {
		s := comparisonSignature{text: line, trimmed: strings.TrimSpace(line), weight: 110}
		if len(s.trimmed) <= 512 {
			s.runes = []rune(s.trimmed)
		}
		if s.trimmed == "" {
			s.weight = 2
		} else if strings.Trim(s.trimmed, "{}();") == "" {
			s.weight = 8
		}
		if len(line) <= 4096 {
			// An empty language keeps comment markers as visible tokens. These
			// signatures only guide alignment, never the similarity percentage.
			s.tokens = codeTokens(line, "")
			if len(s.tokens) > 128 {
				s.tokens = nil
			}
			for k, token := range s.tokens {
				r, _ := utf8.DecodeRuneInString(token)
				if (unicode.IsLetter(r) || r == '_') && !keptWords[token] {
					s.tokens[k] = "identifier"
				}
			}
			s.shape = strings.Join(s.tokens, "\x00")
			s.bigrams = make(map[string]int)
			for k := 1; k < len(s.tokens); k++ {
				s.bigrams[s.tokens[k-1]+"\x00"+s.tokens[k]]++
			}
		}
		result[i] = s
	}
	return result
}

func comparisonLineScore(a, b comparisonSignature) int {
	if a.text == b.text {
		return a.weight
	}
	if a.trimmed == b.trimmed {
		return min(a.weight, b.weight, 100)
	}
	if len(a.tokens) > 0 && len(b.tokens) > 0 && a.tokens[0] != b.tokens[0] && (keptWords[a.tokens[0]] || keptWords[b.tokens[0]]) {
		return 0
	}
	if a.shape != "" && len(a.tokens) >= 3 && len(b.tokens) >= 3 && a.tokens[0] == b.tokens[0] {
		if a.shape == b.shape {
			return 90
		}
		common := 0
		for pair, count := range a.bigrams {
			common += min(count, b.bigrams[pair])
		}
		// A strong majority of adjacent tokens must agree. This permits a
		// changed literal or an extra declarator without pairing if with while.
		dice := 200 * common / (len(a.tokens) + len(b.tokens) - 2)
		if dice >= 60 {
			return 40 + dice/3
		}
	}
	// Small edits in comments or plain Unicode text remain useful matches.
	if len(a.runes) > 0 && len(b.runes) > 0 {
		x, y := a.runes, b.runes
		p, s := 0, 0
		for p < len(x) && p < len(y) && x[p] == y[p] {
			p++
		}
		for s < len(x)-p && s < len(y)-p && x[len(x)-1-s] == y[len(y)-1-s] {
			s++
		}
		if min(len(x), len(y)) >= 4 && 3*(p+s) >= 2*max(len(x), len(y)) {
			return 55
		}
	}
	return 0
}
