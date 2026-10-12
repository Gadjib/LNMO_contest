package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"hash/fnv"
	"sort"
	"strings"
	"unicode"
)

const similarityThreshold = 85
const minSimilarityTokens = 20

type codePrint struct {
	Exact  string
	Tokens int
	Grams  []uint64
}
type CodeMatch struct {
	ID              int64
	Name, ClassName string
	Score           int
}

// Lexical heuristic, not a proof of copying. Literal contents remain significant.
func codeTokens(source, language string) []string {
	r := []rune(source)
	var tokens []string
	for i := 0; i < len(r); {
		if unicode.IsSpace(r[i]) {
			i++
			continue
		}
		if language == "python3" && r[i] == '#' {
			for i < len(r) && r[i] != '\n' {
				i++
			}
			continue
		}
		if language == "cpp17" && i+1 < len(r) && r[i] == '/' {
			if r[i+1] == '/' {
				for i < len(r) && r[i] != '\n' {
					i++
				}
				continue
			}
			if r[i+1] == '*' {
				i += 2
				for i+1 < len(r) && !(r[i] == '*' && r[i+1] == '/') {
					i++
				}
				if i+1 < len(r) {
					i += 2
				} else {
					i = len(r)
				}
				continue
			}
		}
		start := i
		if r[i] == '\'' || r[i] == '"' {
			quote := r[i]
			width := 1
			if language == "python3" && i+2 < len(r) && r[i+1] == quote && r[i+2] == quote {
				width = 3
			}
			i += width
			for i < len(r) {
				if r[i] == '\\' {
					i++
					if i < len(r) {
						i++
					}
					continue
				}
				if r[i] == quote && (width == 1 || i+2 < len(r) && r[i+1] == quote && r[i+2] == quote) {
					i += width
					break
				}
				i++
			}
		} else if unicode.IsLetter(r[i]) || r[i] == '_' {
			i++
			for i < len(r) && (unicode.IsLetter(r[i]) || unicode.IsDigit(r[i]) || r[i] == '_') {
				i++
			}
		} else if unicode.IsDigit(r[i]) {
			i++
			for i < len(r) && (unicode.IsLetter(r[i]) || unicode.IsDigit(r[i]) || r[i] == '.' || r[i] == '_') {
				i++
			}
		} else {
			i++
		}
		tokens = append(tokens, string(r[start:i]))
	}
	return tokens
}

var keptWords = func() map[string]bool {
	m := map[string]bool{}
	for _, s := range strings.Fields(`False None True and as assert async await break class continue def del elif else except finally for from global if import in is lambda nonlocal not or pass raise return try while with yield print input int float str bool list dict set tuple range len sum map zip enumerate sorted min max abs open read write append extend split join strip count index lower upper reverse sort any all std cin cout endl main auto bool char const double enum extern false float inline int long namespace new nullptr operator private protected public short signed sizeof static struct switch template this throw true typedef typename union unsigned using virtual void volatile case default do delete include vector string iostream algorithm numeric bits stdc h printf scanf size push_back begin end`) {
		m[s] = true
	}
	return m
}()

func fingerprint(source, language string) codePrint {
	tokens := codeTokens(source, language)
	b, _ := json.Marshal(tokens)
	h := sha256.Sum256(b)
	p := codePrint{Exact: hex.EncodeToString(h[:]), Tokens: len(tokens)}
	ids := map[string]string{}
	next := 0
	for i, s := range tokens {
		r := []rune(s)
		if len(r) > 0 && (unicode.IsLetter(r[0]) || r[0] == '_') && !keptWords[s] {
			v, ok := ids[s]
			if !ok {
				next++
				v = string(rune(0xE000 + next))
				ids[s] = v
			}
			tokens[i] = "identifier:" + v
		}
	}
	for i := 0; i+5 <= len(tokens); i++ {
		h := fnv.New64a()
		for _, s := range tokens[i : i+5] {
			h.Write([]byte(s))
			h.Write([]byte{0})
		}
		p.Grams = append(p.Grams, h.Sum64())
	}
	sort.Slice(p.Grams, func(i, j int) bool { return p.Grams[i] < p.Grams[j] })
	return p
}
func similarity(a, b codePrint) int {
	if a.Tokens == 0 || b.Tokens == 0 {
		return 0
	}
	if a.Exact == b.Exact {
		return 100
	}
	if a.Tokens < minSimilarityTokens || b.Tokens < minSimilarityTokens {
		return 0
	}
	i, j, common := 0, 0, 0
	for i < len(a.Grams) && j < len(b.Grams) {
		if a.Grams[i] == b.Grams[j] {
			common++
			i++
			j++
		} else if a.Grams[i] < b.Grams[j] {
			i++
		} else {
			j++
		}
	}
	return 200 * common / (len(a.Grams) + len(b.Grams))
}

// Called by the single worker, also backfills old submissions. Commit the marker
// and all symmetric pairs together, so a restart cannot leave partial results.
func indexNextSimilarity(db *sql.DB) (bool, error) {
	var id, uid, pid int64
	var source, lang, role string
	e := db.QueryRow(`SELECT s.id,s.user_id,s.problem_id,s.source,s.language,u.role FROM submissions s JOIN users u ON u.id=s.user_id LEFT JOIN code_fingerprints f ON f.submission_id=s.id WHERE f.submission_id IS NULL ORDER BY s.id LIMIT 1`).Scan(&id, &uid, &pid, &source, &lang, &role)
	if e == sql.ErrNoRows {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	p := fingerprint(source, lang)
	var matches []CodeMatch
	if role == "student" {
		rows, e := db.Query(`SELECT s.id,f.fingerprint FROM code_fingerprints f JOIN submissions s ON s.id=f.submission_id JOIN users u ON u.id=s.user_id WHERE s.problem_id=? AND s.language=? AND s.user_id<>? AND u.role='student'`, pid, lang, uid)
		if e != nil {
			return false, e
		}
		for rows.Next() {
			var oid int64
			var raw string
			if e = rows.Scan(&oid, &raw); e != nil {
				rows.Close()
				return false, e
			}
			var other codePrint
			if e = json.Unmarshal([]byte(raw), &other); e != nil {
				rows.Close()
				return false, e
			}
			score := similarity(p, other)
			if score >= similarityThreshold {
				matches = append(matches, CodeMatch{ID: oid, Score: score})
			}
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return false, e
		}
	}
	tx, e := db.Begin()
	if e != nil {
		return false, e
	}
	defer tx.Rollback()
	if _, e = tx.Exec("INSERT INTO code_fingerprints(submission_id,fingerprint) VALUES(?,?)", id, encode(p)); e != nil {
		return false, e
	}
	for _, m := range matches {
		left, right := id, m.ID
		if left > right {
			left, right = right, left
		}
		if _, e = tx.Exec("INSERT INTO code_matches(left_id,right_id,score) VALUES(?,?,?)", left, right, m.Score); e != nil {
			return false, e
		}
	}
	return true, tx.Commit()
}
func (a *App) loadSimilarity(s *Submission, details bool, viewers ...User) error {
	if e := a.db.QueryRow("SELECT EXISTS(SELECT 1 FROM code_fingerprints WHERE submission_id=?)", s.ID).Scan(&s.SimilarityChecked); e != nil {
		return e
	}
	scope := ""
	var sa []any
	if len(viewers) > 0 {
		scope, sa = studentScope(viewers[0], "u.", false)
	}
	base := ` FROM code_matches m JOIN submissions other ON other.id=CASE WHEN m.left_id=? THEN m.right_id ELSE m.left_id END JOIN users u ON u.id=other.user_id WHERE (m.left_id=? OR m.right_id=?)` + scope
	args := append([]any{s.ID, s.ID, s.ID}, sa...)
	if e := a.db.QueryRow("SELECT coalesce(max(m.score),0)"+base, args...).Scan(&s.SimilarityScore); e != nil {
		return e
	}
	if !details || s.SimilarityScore == 0 {
		return nil
	}
	rows, e := a.db.Query(`SELECT other.id,u.id,u.name,u.class_name,m.score`+base+` ORDER BY m.score DESC,other.id`, args...)
	if e != nil {
		return e
	}
	defer rows.Close()
	seen := map[int64]bool{}
	for rows.Next() {
		var m CodeMatch
		var uid int64
		if e = rows.Scan(&m.ID, &uid, &m.Name, &m.ClassName, &m.Score); e != nil {
			return e
		}
		if !seen[uid] {
			s.Matches = append(s.Matches, m)
			seen[uid] = true
		}
		if len(s.Matches) == 20 {
			break
		}
	}
	return rows.Err()
}
