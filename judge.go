package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const isolateBin = "/usr/local/bin/isolate"

func isolate(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, isolateBin, append([]string{"--cg", "--box-id=0"}, args...)...)
	b, e := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return string(b), ctx.Err()
	}
	return string(b), e
}
func initBox() (string, error) {
	s, e := isolate("--init")
	if e != nil {
		return "", fmt.Errorf("sandbox init: %w: %s", e, s)
	}
	root := strings.TrimSpace(s)
	if root != "/var/local/lib/isolate/0" {
		return "", fmt.Errorf("unexpected sandbox path: %q", root)
	}
	return filepath.Join(root, "box"), nil
}
func cleanupBox() {
	if out, e := isolate("--cleanup"); e != nil {
		log.Printf("sandbox cleanup: %v %s", e, out)
	}
}
func sandboxCheck() error {
	box, e := initBox()
	if e != nil {
		return e
	}
	defer cleanupBox()
	source := `import os, socket
assert os.getuid() == 60000
assert not os.path.exists('/var/lib/contests/contests.db')
assert not os.path.exists('/etc/shadow')
s=socket.socket();s.settimeout(0.2)
assert s.connect_ex(('192.168.10.5',80)) != 0
print('sandbox-ok')
`
	if e = os.WriteFile(filepath.Join(box, "main.py"), []byte(source), 0644); e != nil {
		return e
	}
	result, out, _, e := runBox(box, Limits{1000, 64, 16}, false, []string{"/usr/bin/python3", "-I", "-B", "/box/main.py"})
	if e != nil {
		return e
	}
	if result.Status != "AC" || strings.TrimSpace(out) != "sandbox-ok" {
		return fmt.Errorf("sandbox self-test: %+v output=%q", result, out)
	}
	return nil
}
func safeRead(file string, max int64) (string, error) {
	fd, e := syscall.Open(file, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if e != nil {
		return "", e
	}
	f := os.NewFile(uintptr(fd), file)
	defer f.Close()
	st, e := f.Stat()
	if e != nil {
		return "", e
	}
	if !st.Mode().IsRegular() {
		return "", errors.New("sandbox output is not a regular file")
	}
	b, e := io.ReadAll(io.LimitReader(f, max))
	return string(b), e
}
func runBox(box string, l Limits, compile bool, command []string) (Result, string, string, error) {
	var result Result
	temp, e := os.MkdirTemp("", "contests-meta-")
	if e != nil {
		return result, "", "", e
	}
	defer os.RemoveAll(temp)
	wall := float64(l.TimeMS)/1000*3 + 2
	args := []string{fmt.Sprintf("--time=%.3f", float64(l.TimeMS)/1000), fmt.Sprintf("--wall-time=%.3f", wall), fmt.Sprintf("--cg-mem=%d", l.MemoryMB*1024), fmt.Sprintf("--fsize=%d", l.OutputKB), "--open-files=64", "--env=PATH=/usr/bin:/bin", "--env=LANG=C.UTF-8", "--env=HOME=/box", "--env=TMPDIR=/box", "--stdout=/box/stdout", "--stderr=/box/stderr", "--meta=" + filepath.Join(temp, "meta")}
	if compile {
		args = append(args, "--processes=16")
	} else {
		args = append(args, "--processes=1", "--stdin=/box/input")
	}
	// Self-test also uses the same stdin setup.
	if _, e = os.Stat(filepath.Join(box, "input")); os.IsNotExist(e) {
		if e = os.WriteFile(filepath.Join(box, "input"), nil, 0644); e != nil {
			return result, "", "", e
		}
	}
	args = append(args, "--run", "--")
	args = append(args, command...)
	out, runErr := isolate(args...)
	b, e := os.ReadFile(filepath.Join(temp, "meta"))
	if e != nil {
		return result, "", "", fmt.Errorf("missing sandbox metadata: %w: %s", e, out)
	}
	meta := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(line, ":")
		if ok {
			meta[k] = v
		}
	}
	if meta["status"] == "XX" {
		return result, "", "", fmt.Errorf("sandbox failure: %s", out)
	}
	if runErr != nil {
		var ex *exec.ExitError
		if !errors.As(runErr, &ex) || ex.ExitCode() != 1 {
			return result, "", "", fmt.Errorf("sandbox execution: %w: %s", runErr, out)
		}
	}
	result.Status = "AC"
	sec, _ := strconv.ParseFloat(meta["time"], 64)
	result.TimeMS = int64(sec * 1000)
	result.MemoryKB, _ = strconv.ParseInt(meta["cg-mem"], 10, 64)
	stdout, oe := safeRead(filepath.Join(box, "stdout"), int64(l.OutputKB)*1024+1)
	stderr, se := safeRead(filepath.Join(box, "stderr"), 16384)
	switch {
	case meta["cg-oom-killed"] != "" && meta["cg-oom-killed"] != "0":
		result.Status = "MLE"
	case meta["status"] == "TO":
		result.Status = "TLE"
	case meta["exitsig"] == "25" || len(stdout) >= l.OutputKB*1024:
		result.Status = "OLE"
	case meta["status"] != "" || meta["exitcode"] != "0" || oe != nil || se != nil:
		result.Status = "RE"
	}
	result.Output = truncate(stdout, 64<<10)
	result.OutputTruncated = len(stdout) > 64<<10
	result.Error = truncate(stderr, 4096)
	return result, stdout, stderr, nil
}
func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "\n[обрезано]"
	}
	return s
}
func compareOutput(kind, got, want string) bool {
	normalize := func(s string) string { return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n") }
	if kind == "exact" {
		return normalize(got) == normalize(want)
	}
	a, b := strings.Fields(got), strings.Fields(want)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func judge(s Submission, p ProblemSpec) (string, int, []Result, string, error) {
	if e := p.Validate(); e != nil {
		return "JE", 0, nil, "", e
	}
	if len(s.Source) > 65536 {
		return "JE", 0, nil, "", errors.New("oversized source")
	}
	var executable string
	switch s.Language {
	case "cpp17":
		box, e := initBox()
		if e != nil {
			return "JE", 0, nil, "", e
		}
		if e = os.WriteFile(filepath.Join(box, "main.cpp"), []byte(s.Source), 0644); e != nil {
			cleanupBox()
			return "JE", 0, nil, "", e
		}
		result, _, stderr, e := runBox(box, Limits{10000, 512, 16384}, true, []string{"/usr/bin/g++", "-std=c++17", "-O2", "-pipe", "/box/main.cpp", "-o", "/box/main"})
		if e != nil {
			cleanupBox()
			return "JE", 0, nil, "", e
		}
		if result.Status != "AC" {
			cleanupBox()
			return "CE", 0, nil, truncate(stderr+"\n"+statusLabel(result.Status), 16384), nil
		}
		executable, e = safeRead(filepath.Join(box, "main"), 16<<20)
		cleanupBox()
		if e != nil {
			return "JE", 0, nil, "", e
		}
	case "python3":
		executable = s.Source
	default:
		return "JE", 0, nil, "", errors.New("unsupported language")
	}
	var results []Result
	score := 0
	status := "AC"
	for _, test := range p.Tests {
		box, e := initBox()
		if e != nil {
			return "JE", 0, nil, "", e
		}
		command := []string{"/box/main"}
		file := "main"
		mode := os.FileMode(0755)
		if s.Language == "python3" {
			command = []string{"/usr/bin/python3", "-I", "-B", "/box/main.py"}
			file = "main.py"
			mode = 0644
		}
		if e = os.WriteFile(filepath.Join(box, file), []byte(executable), mode); e == nil {
			e = os.WriteFile(filepath.Join(box, "input"), []byte(test.Input), 0644)
		}
		if e != nil {
			cleanupBox()
			return "JE", 0, nil, "", e
		}
		result, stdout, _, e := runBox(box, p.Limits, false, command)
		cleanupBox()
		if e != nil {
			return "JE", 0, nil, "", e
		}
		result.TestID = test.ID
		if result.Status == "AC" && !checkOutput(p.Checker.Type, test.Input, stdout, test.Answer) {
			result.Status = "WA"
		}
		if result.Status == "AC" {
			score += test.Points
			result.Points = test.Points
		} else if status == "AC" {
			status = result.Status
		}
		results = append(results, result)
	}
	return status, score, results, "", nil
}
func runWorker(db *sql.DB, dbPath string) error {
	lock, e := os.OpenFile(dbPath+".worker.lock", os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	defer lock.Close()
	if e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		return fmt.Errorf("worker already running: %w", e)
	}
	if e = sandboxCheck(); e != nil {
		return e
	}
	if _, e = db.Exec("UPDATE submissions SET status='QUEUED',score=0,results='[]',diagnostic='' WHERE status='RUNNING'"); e != nil {
		return e
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	heartbeat := func() {
		if _, e := db.Exec("INSERT INTO health(name,updated) VALUES('worker',?) ON CONFLICT(name) DO UPDATE SET updated=excluded.updated", time.Now().Unix()); e != nil {
			log.Printf("heartbeat: %v", e)
		}
	}
	heartbeat()
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				heartbeat()
			}
		}
	}()
	log.Print("worker ready; concurrency=1")
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		indexed, indexErr := indexNextSimilarity(db)
		if indexErr != nil {
			log.Printf("code similarity: %v", indexErr)
		}
		var id int64
		e = db.QueryRow("UPDATE submissions SET status='RUNNING' WHERE id=(SELECT id FROM submissions WHERE status='QUEUED' ORDER BY id LIMIT 1) RETURNING id").Scan(&id)
		if e == sql.ErrNoRows {
			if indexed {
				continue
			}
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(time.Second):
				continue
			}
		}
		if e != nil {
			return e
		}
		s, e := getSubmission(db, id)
		if e != nil {
			return e
		}
		var spec string
		if e = db.QueryRow("SELECT spec FROM versions WHERE id=?", s.VersionID).Scan(&spec); e != nil {
			return e
		}
		var p ProblemSpec
		if e = json.Unmarshal([]byte(spec), &p); e != nil {
			return e
		}
		status, score, results, diagnostic, err := judge(s, p)
		if err != nil {
			status = "JE"
			score = 0
			diagnostic = "Внутренняя ошибка проверки. Учитель может повторить проверку."
			log.Printf("submission %d: %v", id, err)
		}
		if results == nil {
			results = []Result{}
		}
		if _, e = db.Exec("UPDATE submissions SET status=?,score=?,results=?,diagnostic=?,finished=? WHERE id=?", status, score, encode(results), diagnostic, time.Now().Unix(), id); e != nil {
			return e
		}
		log.Printf("submission %d: %s %d", id, status, score)
	}
}
