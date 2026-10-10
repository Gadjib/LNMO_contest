package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS users(id INTEGER PRIMARY KEY, login TEXT UNIQUE NOT NULL, name TEXT NOT NULL, password TEXT NOT NULL, role TEXT NOT NULL CHECK(role IN ('student','teacher','admin')));
CREATE TABLE IF NOT EXISTS sessions(token TEXT PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users(id), expires INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS contests(id INTEGER PRIMARY KEY, title TEXT NOT NULL, description TEXT NOT NULL DEFAULT '', starts INTEGER NOT NULL, ends INTEGER NOT NULL, published INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS problems(id INTEGER PRIMARY KEY, contest_id INTEGER NOT NULL REFERENCES contests(id), code TEXT NOT NULL, current_version INTEGER, UNIQUE(contest_id,code));
CREATE TABLE IF NOT EXISTS versions(id INTEGER PRIMARY KEY, problem_id INTEGER NOT NULL REFERENCES problems(id), spec TEXT NOT NULL, created INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS submissions(id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users(id), problem_id INTEGER NOT NULL REFERENCES problems(id), version_id INTEGER NOT NULL REFERENCES versions(id), language TEXT NOT NULL, source TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'QUEUED', score INTEGER NOT NULL DEFAULT 0, results TEXT NOT NULL DEFAULT '[]', diagnostic TEXT NOT NULL DEFAULT '', created INTEGER NOT NULL, finished INTEGER);
CREATE INDEX IF NOT EXISTS submission_queue ON submissions(status,id);
CREATE INDEX IF NOT EXISTS submission_user ON submissions(user_id,problem_id,id);
CREATE TABLE IF NOT EXISTS health(name TEXT PRIMARY KEY, updated INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS code_fingerprints(submission_id INTEGER PRIMARY KEY REFERENCES submissions(id) ON DELETE CASCADE, fingerprint TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS code_matches(left_id INTEGER NOT NULL REFERENCES submissions(id) ON DELETE CASCADE,right_id INTEGER NOT NULL REFERENCES submissions(id) ON DELETE CASCADE,score INTEGER NOT NULL,PRIMARY KEY(left_id,right_id),CHECK(left_id<right_id));
CREATE INDEX IF NOT EXISTS code_matches_right ON code_matches(right_id);
CREATE INDEX IF NOT EXISTS submissions_problem_language ON submissions(problem_id,language,user_id);
CREATE TABLE IF NOT EXISTS problem_images(id TEXT PRIMARY KEY, mime TEXT NOT NULL, data BLOB NOT NULL, created INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS version_images(version_id INTEGER NOT NULL REFERENCES versions(id) ON DELETE CASCADE, image_id TEXT NOT NULL REFERENCES problem_images(id), PRIMARY KEY(version_id,image_id));
CREATE INDEX IF NOT EXISTS version_images_image ON version_images(image_id);
CREATE TABLE IF NOT EXISTS contest_classes(contest_id INTEGER NOT NULL REFERENCES contests(id) ON DELETE CASCADE, class_name TEXT NOT NULL, PRIMARY KEY(contest_id,class_name));
`

func openDB(file string) (*sql.DB, error) {
	if e := os.MkdirAll(filepath.Dir(file), 0750); e != nil {
		return nil, e
	}
	db, e := sql.Open("sqlite", file)
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	for _, q := range []string{"PRAGMA busy_timeout=5000", "PRAGMA journal_mode=WAL", "PRAGMA foreign_keys=ON", schema} {
		if _, e = db.Exec(q); e != nil {
			db.Close()
			return nil, e
		}
	}
	if e = migrateClasses(db); e != nil {
		db.Close()
		return nil, e
	}
	return db, nil
}

func migrateClasses(db *sql.DB) error {
	if _, e := db.Exec("BEGIN IMMEDIATE"); e != nil {
		return e
	}
	defer db.Exec("ROLLBACK")
	var version int
	if e := db.QueryRow("PRAGMA user_version").Scan(&version); e != nil {
		return e
	}
	if version < 2 {
		if _, e := db.Exec("ALTER TABLE users ADD COLUMN class_name TEXT NOT NULL DEFAULT ''; CREATE INDEX users_class ON users(role,class_name); PRAGMA user_version=2;"); e != nil {
			return e
		}
	}
	_, e := db.Exec("COMMIT")
	return e
}

type User struct {
	ID                           int64
	Login, Name, Role, ClassName string
}

func (u User) Teacher() bool { return u.Role == "teacher" || u.Role == "admin" }

type Contest struct {
	Classes            []string
	ID                 int64
	Title, Description string
	Starts, Ends       int64
	Published          bool
}

func (c Contest) State() string {
	if !c.Published {
		return "Черновик"
	}
	n := time.Now().Unix()
	if n < c.Starts {
		return "Скоро"
	}
	if n >= c.Ends {
		return "Завершён"
	}
	return "Идёт сейчас"
}
func (c Contest) Open() bool {
	return c.Published && time.Now().Unix() >= c.Starts && time.Now().Unix() < c.Ends
}

type Problem struct {
	ID, ContestID, VersionID int64
	Spec                     ProblemSpec
}
type Result struct {
	TestID, Status   string
	Points           int
	TimeMS, MemoryKB int64
	Output, Error    string `json:",omitempty"`
	OutputTruncated  bool   `json:",omitempty"`
	Input, Answer    string `json:"-"`
	TestAvailable    bool   `json:"-"`
}

// Resolve fixtures from the immutable version used for this attempt, never the
// currently edited problem. Only the teacher page calls this method.
func (a *App) loadTestDetails(s *Submission) error {
	if len(s.Results) == 0 {
		return nil
	}
	var raw string
	if e := a.db.QueryRow("SELECT spec FROM versions WHERE id=?", s.VersionID).Scan(&raw); e != nil {
		return e
	}
	var p ProblemSpec
	if e := json.Unmarshal([]byte(raw), &p); e != nil {
		return e
	}
	tests := make(map[string]TestCase, len(p.Tests))
	for _, test := range p.Tests {
		tests[test.ID] = test
	}
	for i := range s.Results {
		r := &s.Results[i]
		if test, ok := tests[r.TestID]; ok {
			r.Input = test.Input
			r.Answer = test.Answer
			r.TestAvailable = true
		}
	}
	return nil
}

type Submission struct {
	ID, UserID, ProblemID, VersionID                                int64
	Login, Name, ProblemTitle, Language, Source, Status, Diagnostic string
	Score                                                           int
	Results                                                         []Result
	Created                                                         int64
	MaxScore                                                        int
	ClassName                                                       string
	SimilarityChecked                                               bool
	SimilarityScore                                                 int
	Matches                                                         []CodeMatch
}

func (s Submission) Pending() bool { return s.Status == "QUEUED" || s.Status == "RUNNING" }
func getContest(db *sql.DB, id int64) (c Contest, e error) {
	e = db.QueryRow("SELECT id,title,description,starts,ends,published FROM contests WHERE id=?", id).Scan(&c.ID, &c.Title, &c.Description, &c.Starts, &c.Ends, &c.Published)
	if e != nil {
		return
	}
	rows, err := db.Query("SELECT class_name FROM contest_classes WHERE contest_id=? ORDER BY class_name", id)
	if err != nil {
		e = err
		return
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if e = rows.Scan(&name); e != nil {
			return
		}
		c.Classes = append(c.Classes, name)
	}
	e = rows.Err()
	return
}
func getProblem(db *sql.DB, id int64) (p Problem, e error) {
	var spec string
	e = db.QueryRow("SELECT p.id,p.contest_id,v.id,v.spec FROM problems p JOIN versions v ON v.id=p.current_version WHERE p.id=?", id).Scan(&p.ID, &p.ContestID, &p.VersionID, &spec)
	if e == nil {
		e = json.Unmarshal([]byte(spec), &p.Spec)
	}
	return
}
func saveProblem(db *sql.DB, cid, pid int64, p ProblemSpec) (int64, error) {
	if e := p.Validate(); e != nil {
		return 0, e
	}
	tx, e := db.Begin()
	if e != nil {
		return 0, e
	}
	defer tx.Rollback()
	pid, e = saveProblemTx(tx, cid, pid, p)
	if e != nil {
		return 0, e
	}
	return pid, tx.Commit()
}

func saveProblemTx(tx *sql.Tx, cid, pid int64, p ProblemSpec) (int64, error) {
	if e := p.Validate(); e != nil {
		return 0, e
	}
	if pid == 0 {
		r, e := tx.Exec("INSERT INTO problems(contest_id,code) VALUES(?,?)", cid, p.Code)
		if e != nil {
			return 0, e
		}
		pid, _ = r.LastInsertId()
	} else {
		r, e := tx.Exec("UPDATE problems SET code=? WHERE id=? AND contest_id=?", p.Code, pid, cid)
		if e != nil {
			return 0, e
		}
		n, _ := r.RowsAffected()
		if n != 1 {
			return 0, errors.New("задача не найдена")
		}
	}
	r, e := tx.Exec("INSERT INTO versions(problem_id,spec,created) VALUES(?,?,?)", pid, encode(p), time.Now().Unix())
	if e != nil {
		return 0, e
	}
	vid, _ := r.LastInsertId()
	if e = linkImages(tx, vid, p); e != nil {
		return 0, e
	}
	if _, e = tx.Exec("UPDATE problems SET current_version=? WHERE id=?", vid, pid); e != nil {
		return 0, e
	}
	return pid, nil
}
func getSubmission(db *sql.DB, id int64) (s Submission, e error) {
	var results, spec string
	e = db.QueryRow(`SELECT s.id,s.user_id,s.problem_id,s.version_id,u.login,u.name,s.language,s.source,s.status,s.score,s.results,s.diagnostic,s.created,v.spec,u.class_name FROM submissions s JOIN users u ON u.id=s.user_id JOIN versions v ON v.id=s.version_id WHERE s.id=?`, id).Scan(&s.ID, &s.UserID, &s.ProblemID, &s.VersionID, &s.Login, &s.Name, &s.Language, &s.Source, &s.Status, &s.Score, &results, &s.Diagnostic, &s.Created, &spec, &s.ClassName)
	if e != nil {
		return
	}
	var p ProblemSpec
	if e = json.Unmarshal([]byte(spec), &p); e != nil {
		return
	}
	s.ProblemTitle = p.Title
	s.MaxScore = p.Scoring.MaxScore
	e = json.Unmarshal([]byte(results), &s.Results)
	return
}
