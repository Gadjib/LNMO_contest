package main

import (
	"bytes"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileIOPersistence(t *testing.T) {
	for _, p := range fileDemoProblems() {
		if e := p.Validate(); e != nil {
			t.Fatal(p.Code, e)
		}
		raw := encode(p)
		var stored ProblemSpec
		if e := json.Unmarshal([]byte(raw), &stored); e != nil {
			t.Fatal(e)
		}
		archive, e := exportProblem(stored)
		if e != nil {
			t.Fatal(e)
		}
		imported, e := importProblem(archive)
		if e != nil {
			t.Fatal(e)
		}
		for i, original := range p.Tests {
			if imported.Tests[i].inputData() != original.inputData() || imported.Tests[i].answerData() != original.answerData() {
				t.Fatal("binary roundtrip", p.Code, i)
			}
		}
		c := Contest{Title: "Files", Starts: 1, Ends: 4102444800}
		z, e := exportContest(c, []ProblemSpec{p})
		if e != nil {
			t.Fatal(e)
		}
		_, ps, e := importContest(z)
		if e != nil || ps[0].Tests[4].inputData() != p.Tests[4].inputData() {
			t.Fatal("contest roundtrip", e)
		}
	}
	p := fileDemoProblems()[2]
	for _, name := range []string{"../x.bin", "/x.bin", "main.py", "stdout", "a/b.txt", "a\\b.bin", "x.bin\n", ".bin"} {
		q := p
		q.IO.Output.Name = name
		if q.Validate() == nil {
			t.Fatal("unsafe name", name)
		}
	}
	p.IO.Output.Name = p.IO.Input.Name
	if p.Validate() == nil {
		t.Fatal("input overwrite accepted")
	}
	p = fileDemoProblems()[2]
	p.Checker.Type = "tokens"
	if p.Validate() == nil {
		t.Fatal("binary token comparison")
	}
	if compareOutput("bytes", "\r\n", "\n") || compareOutput("bytes", "\xff", "\xfe") {
		t.Fatal("byte comparison normalizes")
	}
}

func TestCollectFileOutput(t *testing.T) {
	p := fileDemoProblems()[2]
	dir := t.TempDir()
	r := Result{Status: "AC"}
	collectFileOutput(dir, p, &r)
	if r.Status != "WA" || !strings.Contains(r.FileDiagnostic, "Не создан") {
		t.Fatal(r)
	}
	name := filepath.Join(dir, p.IO.Output.Name)
	if e := os.Symlink("/etc/passwd", name); e != nil {
		t.Fatal(e)
	}
	r = Result{Status: "AC"}
	collectFileOutput(dir, p, &r)
	if r.Status != "WA" || len(r.OutputBytes) != 0 {
		t.Fatal("symlink followed")
	}
	os.Remove(name)
	b := []byte{0, 255, 128, 13, 10}
	os.WriteFile(name, b, 0600)
	r = Result{Status: "AC"}
	got := collectFileOutput(dir, p, &r)
	if got != string(b) || !bytes.Equal(r.OutputBytes, b) {
		t.Fatal("bytes corrupted")
	}
	var restored Result
	json.Unmarshal([]byte(encode(r)), &restored)
	if !bytes.Equal(restored.OutputBytes, b) {
		t.Fatal("output JSON corrupted")
	}
	p.Limits.OutputKB = 1
	os.WriteFile(name, bytes.Repeat([]byte{255}, 1025), 0600)
	r = Result{Status: "AC"}
	collectFileOutput(dir, p, &r)
	if r.Status != "OLE" {
		t.Fatal("no file size limit")
	}
}

func TestFileDetailsAndForm(t *testing.T) {
	a, pid := testApp(t)
	p := fileDemoProblems()[2]
	p.Code = "sum"
	_, e := saveProblem(a.db, 1, pid, p)
	if e != nil {
		t.Fatal(e)
	}
	current, _ := getProblem(a.db, pid)
	res := []Result{{TestID: "05", Status: "AC", OutputBinary: true, OutputBytes: p.Tests[4].AnswerBytes}}
	_, e = a.db.Exec("INSERT INTO submissions(id,user_id,problem_id,version_id,language,source,status,results,created) VALUES(1,1,?,?,'python3','x','AC',?,1)", pid, current.VersionID, encode(res))
	if e != nil {
		t.Fatal(e)
	}
	for _, kind := range []string{"input", "answer", "output"} {
		path := "/submissions/1/file?test=05&kind=" + kind
		for _, uid := range []int64{1, 3} {
			if w := requestAs(t, a, uid, "GET", path, nil); w.Code != 403 {
				t.Fatal("student download allowed", w.Code)
			}
		}
		w := requestAs(t, a, 2, "GET", path, nil)
		want := p.Tests[4].answerData()
		if kind == "input" {
			want = p.Tests[4].inputData()
		}
		if w.Code != 200 || w.Body.String() != want {
			t.Fatal("wrong download", kind, w.Code)
		}
	}
	if w := requestAs(t, a, 2, "GET", "/submissions/1", nil); w.Code != 200 || !strings.Contains(w.Body.String(), "Размер: 256 байт") {
		t.Fatal("hex preview", w.Code)
	}
	if w := requestAs(t, a, 2, "GET", "/teacher/problems/1/edit", nil); w.Code != 200 || !strings.Contains(w.Body.String(), "source.bin") {
		t.Fatal("editor", w.Code)
	}
	form := url.Values{"code": {"sum"}, "title": {"Files"}, "statement": {"Read source.bin"}, "input_format": {"binary"}, "output_format": {"binary"}, "time_ms": {"1000"}, "memory_mb": {"128"}, "output_kb": {"16"}, "checker": {"bytes"}, "max_score": {"100"}, "languages": {"python3"}, "io_input_type": {"file"}, "io_input_name": {"source.bin"}, "io_input_content": {"binary"}, "io_output_type": {"file"}, "io_output_name": {"result.bin"}, "io_output_content": {"binary"}, "test_id": {"01"}, "test_points": {"100"}, "test_input": {"00 ff 80"}, "test_answer": {"ff 00 7f"}}
	w := requestAs(t, a, 2, "POST", "/teacher/problems/1/edit", form)
	if w.Code != 303 {
		t.Fatal(w.Code, w.Body.String())
	}
	after, _ := getProblem(a.db, pid)
	if after.Spec.Tests[0].inputData() != string([]byte{0, 255, 128}) {
		t.Fatal("form corrupted binary")
	}
	w = requestAs(t, a, 2, "GET", "/submissions/1/file?test=05&kind=input", nil)
	if w.Body.String() != p.Tests[4].inputData() {
		t.Fatal("old version lost")
	}
}

// Run only on the judge host while the production worker is stopped (box 0).
func TestFileIOIntegration(t *testing.T) {
	if os.Getenv("FILE_IO_INTEGRATION") != "1" {
		t.Skip("requires isolate and exclusive access to box 0")
	}
	py := []string{
		`with open('numbers.txt') as f: n=sum(map(int,f.read().split()))
with open('sum.txt','w') as f: f.write(str(n))`,
		`with open('data.bin','rb') as f: print(sum(f.read()))`,
		`with open('source.bin','rb') as f: b=f.read()
with open('result.bin','wb') as f: f.write(bytes(255-x for x in b))`,
		`with open('values.txt') as f: b=bytes(map(int,f.read().split()))
with open('packed.bin','wb') as f: f.write(b)`,
	}
	cpp := []string{
		`#include <fstream>
int main(){std::ifstream f("numbers.txt");long long x,s=0;while(f>>x)s+=x;std::ofstream("sum.txt")<<s;}`,
		`#include <fstream>
#include <iostream>
int main(){std::ifstream f("data.bin",std::ios::binary);char c;long long s=0;while(f.get(c))s+=(unsigned char)c;std::cout<<s;}`,
		`#include <fstream>
int main(){std::ifstream f("source.bin",std::ios::binary);std::ofstream o("result.bin",std::ios::binary);char c;while(f.get(c))o.put((char)(255-(unsigned char)c));}`,
		`#include <fstream>
int main(){std::ifstream f("values.txt");std::ofstream o("packed.bin",std::ios::binary);int x;while(f>>x)o.put((char)x);}`,
	}
	for i, p := range fileDemoProblems() {
		for lang, sources := range map[string][]string{"python3": py, "cpp17": cpp} {
			status, score, results, diag, e := judge(Submission{Language: lang, Source: sources[i]}, p)
			if e != nil || status != "AC" || score != 100 {
				t.Fatalf("%s task %d: %s %d %v %s %+v", lang, i+1, status, score, e, diag, results)
			}
		}
	}
	p := fileDemoProblems()[2]
	for _, source := range []string{"print('not a file')", "import os; os.symlink('/etc/passwd','result.bin')", "import os; os.mkdir('result.bin')"} {
		status, _, _, _, e := judge(Submission{Language: "python3", Source: source}, p)
		if e != nil || status != "WA" {
			t.Fatal("missing/unsafe output", status, e)
		}
	}
}
