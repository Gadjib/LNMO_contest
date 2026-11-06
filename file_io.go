package main

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var fileNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*\.(txt|bin)$`)

func decodeHex(s string) ([]byte, error) {
	return hex.DecodeString(strings.Join(strings.Fields(s), ""))
}
func decodeProblem(raw string) (ProblemSpec, error) {
	var p ProblemSpec
	e := json.Unmarshal([]byte(raw), &p)
	return p, e
}

type IOChannel struct {
	Type    string `yaml:"type,omitempty"`
	Name    string `yaml:"name,omitempty"`
	Content string `yaml:"content,omitempty"`
}
type FileIO struct {
	Input  IOChannel `yaml:"input,omitempty"`
	Output IOChannel `yaml:"output,omitempty"`
}

func (c IOChannel) Binary() bool { return c.Content == "binary" }
func (c IOChannel) Label() string {
	if c.Type == "file" {
		return c.Name
	}
	return "стандартный поток"
}
func (t TestCase) inputData() string {
	if t.InputBytes != nil {
		return string(t.InputBytes)
	}
	return t.Input
}
func (t TestCase) answerData() string {
	if t.AnswerBytes != nil {
		return string(t.AnswerBytes)
	}
	return t.Answer
}
func (t TestCase) InputEdit() string {
	if t.InputBytes != nil {
		return hex.EncodeToString(t.InputBytes)
	}
	return t.Input
}
func (t TestCase) AnswerEdit() string {
	if t.AnswerBytes != nil {
		return hex.EncodeToString(t.AnswerBytes)
	}
	return t.Answer
}
func (p ProblemSpec) validateIO() error {
	for _, c := range []IOChannel{p.IO.Input, p.IO.Output} {
		if c.Type != "" && c.Type != "stdio" && c.Type != "file" {
			return errors.New("неизвестный тип ввода/вывода")
		}
		if c.Content != "" && c.Content != "text" && c.Content != "binary" {
			return errors.New("тип содержимого: text или binary")
		}
		if p.FormatVersion == 1 && (c.Type == "file" || c.Binary()) {
			return errors.New("файловые задачи требуют format_version: 2")
		}
		if c.Type == "file" {
			if len(c.Name) > 80 || !fileNamePattern.MatchString(c.Name) {
				return errors.New("имя файла: латинские буквы, цифры, _ и -, расширение .txt или .bin")
			}
		} else if c.Name != "" || c.Binary() {
			return errors.New("имя и бинарное содержимое допустимы только для файла")
		}
	}
	if p.IO.Input.Type == "file" && p.IO.Output.Type == "file" && strings.EqualFold(p.IO.Input.Name, p.IO.Output.Name) {
		return errors.New("входной и выходной файлы должны иметь разные имена")
	}
	if p.IO.Output.Binary() && p.Checker.Type != "bytes" {
		return errors.New("для бинарного ответа выберите checker.type: bytes")
	}
	if p.IO.Input.Binary() && (p.Checker.Type == "garden_beds" || p.Checker.Type == "evacuation") {
		return errors.New("этот проверяющий модуль требует текстовый ввод")
	}
	return nil
}
func collectFileOutput(box string, p ProblemSpec, r *Result) string {
	data, e := safeRead(filepath.Join(box, p.IO.Output.Name), int64(p.Limits.OutputKB)*1024+1)
	r.Output = ""
	r.OutputTruncated = false
	r.OutputBinary = p.IO.Output.Binary()
	if e != nil {
		if os.IsNotExist(e) {
			r.FileDiagnostic = "Не создан файл " + p.IO.Output.Name
		} else {
			r.FileDiagnostic = "Невозможно прочитать обычный файл " + p.IO.Output.Name
		}
		if r.Status == "AC" {
			r.Status = "WA"
		}
		return ""
	}
	if len(data) > p.Limits.OutputKB*1024 {
		if r.Status == "AC" {
			r.Status = "OLE"
		}
		r.FileDiagnostic = "Превышен размер выходного файла"
	}
	b := []byte(data)
	if len(b) > 64<<10 {
		b = b[:64<<10]
		r.OutputTruncated = true
	}
	if r.OutputBinary {
		r.OutputBytes = b
	} else {
		r.Output = string(b)
	}
	return data
}
func byteDifference(got, want string) string {
	for i := 0; i < len(got) && i < len(want); i++ {
		if got[i] != want[i] {
			return fmt.Sprintf("Первое различие: байт %d (нумерация с 0).", i)
		}
	}
	return fmt.Sprintf("Размер ответа: %d байт, ожидается %d байт.", len(got), len(want))
}
func previewData(data string, binary bool) string {
	if binary {
		b := []byte(data)
		if len(b) > 4096 {
			b = b[:4096]
		}
		return fmt.Sprintf("Размер: %d байт\n%s", len(data), hex.Dump(b))
	}
	return data
}

// Downloads are teacher-only and use the immutable version of the submission.
func (a *App) testFile(w http.ResponseWriter, r *http.Request) {
	s, ok := a.authorizedSubmission(w, r)
	if !ok {
		return
	}
	if !currentUser(r).Teacher() {
		http.Error(w, "Доступ только для учителя", 403)
		return
	}
	var raw string
	if e := a.db.QueryRow("SELECT spec FROM versions WHERE id=?", s.VersionID).Scan(&raw); e != nil {
		a.fail(w, e)
		return
	}
	p, e := decodeProblem(raw)
	if e != nil {
		a.fail(w, e)
		return
	}
	for _, t := range p.Tests {
		if t.ID == r.URL.Query().Get("test") {
			var data, name string
			switch r.URL.Query().Get("kind") {
			case "input":
				data = t.inputData()
				name = p.IO.Input.Name
			case "answer":
				data = t.answerData()
				name = p.IO.Output.Name
			case "output":
				for _, v := range s.Results {
					if v.TestID == t.ID {
						data = v.Output
						if v.OutputBinary {
							data = string(v.OutputBytes)
						}
					}
				}
				name = p.IO.Output.Name
			default:
				http.NotFound(w, r)
				return
			}
			if name == "" {
				name = "data.txt"
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Write([]byte(data))
			return
		}
	}
	http.NotFound(w, r)
}
