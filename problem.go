package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

type Limits struct {
	TimeMS   int `yaml:"time_ms" json:"time_ms"`
	MemoryMB int `yaml:"memory_mb" json:"memory_mb"`
	OutputKB int `yaml:"output_kb" json:"output_kb"`
}
type Example struct {
	Input       string `yaml:"input"`
	Output      string `yaml:"output"`
	Explanation string `yaml:"explanation,omitempty"`
}
type TestCase struct {
	ID         string `yaml:"id"`
	InputFile  string `yaml:"input_file,omitempty"`
	AnswerFile string `yaml:"answer_file,omitempty"`
	Points     int    `yaml:"points"`
	Input      string `yaml:"-"`
	Answer     string `yaml:"-"`
}
type ProblemSpec struct {
	Images           map[string]ProblemImage `yaml:"-" json:"-"`
	FormatVersion    int                     `yaml:"format_version"`
	Code             string                  `yaml:"code"`
	Title            string                  `yaml:"title"`
	Statement        string                  `yaml:"statement"`
	InputFormat      string                  `yaml:"input_format"`
	OutputFormat     string                  `yaml:"output_format"`
	Notes            string                  `yaml:"notes,omitempty"`
	Limits           Limits                  `yaml:"limits"`
	AllowedLanguages []string                `yaml:"allowed_languages"`
	Checker          struct {
		Type string `yaml:"type"`
	} `yaml:"checker"`
	Scoring struct {
		Type     string `yaml:"type"`
		MaxScore int    `yaml:"max_score"`
	} `yaml:"scoring"`
	Examples []Example  `yaml:"examples"`
	Tests    []TestCase `yaml:"tests"`
}

var codePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,39}$`)

const maxTestBytes = 1 << 20
const maxArchiveBytes = 20 << 20

func (p ProblemSpec) Validate() error {
	if p.FormatVersion != 1 {
		return errors.New("поддерживается только format_version: 1")
	}
	if !codePattern.MatchString(p.Code) {
		return errors.New("код: 1–40 латинских букв, цифр, _ или -")
	}
	if strings.TrimSpace(p.Title) == "" || len(p.Title) > 300 || strings.TrimSpace(p.Statement) == "" || len(p.Statement) > 100000 {
		return errors.New("нужны название (до 300 байт) и условие (до 100 КБ)")
	}
	if p.InputFormat == "" || p.OutputFormat == "" {
		return errors.New("укажите формат ввода и вывода")
	}
	if p.Limits.TimeMS < 100 || p.Limits.TimeMS > 10000 || p.Limits.MemoryMB < 16 || p.Limits.MemoryMB > 512 || p.Limits.OutputKB < 1 || p.Limits.OutputKB > 1024 {
		return errors.New("лимиты: время 100–10000 мс, память 16–512 МБ, вывод 1–1024 КБ")
	}
	if len(p.AllowedLanguages) == 0 || len(p.AllowedLanguages) > 2 {
		return errors.New("выберите Python 3 и/или C++17")
	}
	seenLang := map[string]bool{}
	for _, v := range p.AllowedLanguages {
		if (v != "python3" && v != "cpp17") || seenLang[v] {
			return errors.New("неизвестный или повторяющийся язык")
		}
		seenLang[v] = true
	}
	if p.Checker.Type != "tokens" && p.Checker.Type != "exact" && p.Checker.Type != "evacuation" && p.Checker.Type != "garden_beds" {
		return errors.New("неизвестный checker.type")
	}
	if p.Scoring.Type != "sum" || p.Scoring.MaxScore < 1 || p.Scoring.MaxScore > 10000 {
		return errors.New("scoring: type=sum, max_score от 1 до 10000")
	}
	if len(p.Tests) < 1 || len(p.Tests) > 100 {
		return errors.New("нужно от 1 до 100 тестов")
	}
	sum, total := 0, 0
	ids := map[string]bool{}
	for _, t := range p.Tests {
		if !codePattern.MatchString(t.ID) || ids[t.ID] {
			return errors.New("идентификаторы тестов должны быть допустимыми и уникальными")
		}
		ids[t.ID] = true
		if t.Points < 0 || t.Points > 10000 {
			return errors.New("недопустимый вес теста")
		}
		sum += t.Points
		if len(t.Input) > maxTestBytes || len(t.Answer) > maxTestBytes || !utf8.ValidString(t.Input) || !utf8.ValidString(t.Answer) {
			return errors.New("тесты должны быть UTF-8, каждый файл не больше 1 МБ")
		}
		total += len(t.Input) + len(t.Answer)
	}
	if sum != p.Scoring.MaxScore {
		return errors.New("сумма баллов тестов должна совпадать с max_score")
	}
	if total > 16<<20 {
		return errors.New("суммарный размер тестов превышает 16 МБ")
	}
	if len(p.Examples) > 20 {
		return errors.New("не больше 20 примеров")
	}
	for _, e := range p.Examples {
		if len(e.Input)+len(e.Output)+len(e.Explanation) > 100000 {
			return errors.New("пример слишком большой")
		}
	}
	return nil
}

// Archives are read in memory, never extracted onto the filesystem.
func importProblem(data []byte) (ProblemSpec, error) {
	var p ProblemSpec
	if len(data) > maxArchiveBytes {
		return p, errors.New("архив больше 20 МБ")
	}
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return p, err
	}
	if len(z.File) > 225 {
		return p, errors.New("слишком много файлов")
	}
	files := map[string]*zip.File{}
	var total uint64
	for _, f := range z.File {
		if f.FileInfo().IsDir() {
			continue
		}
		if !safeArchivePath(f.Name) || !f.Mode().IsRegular() {
			return p, errors.New("недопустимый путь или тип файла")
		}
		if _, ok := files[f.Name]; ok {
			return p, errors.New("повторяющееся имя файла")
		}
		if f.UncompressedSize64 > maxTestBytes || f.UncompressedSize64 > maxArchiveBytes-total {
			return p, errors.New("слишком большой распакованный архив")
		}
		total += f.UncompressedSize64
		files[f.Name] = f
	}
	read := func(name string) (string, error) {
		f, ok := files[name]
		if !ok {
			return "", fmt.Errorf("нет файла %s", name)
		}
		r, e := f.Open()
		if e != nil {
			return "", e
		}
		defer r.Close()
		b, e := io.ReadAll(io.LimitReader(r, maxTestBytes+1))
		if e != nil {
			return "", e
		}
		if len(b) > maxTestBytes || !utf8.Valid(b) {
			return "", errors.New("недопустимый размер или кодировка файла")
		}
		return string(b), nil
	}
	manifest, err := read("problem.yaml")
	if err != nil {
		return p, err
	}
	dec := yaml.NewDecoder(strings.NewReader(manifest))
	dec.KnownFields(true)
	if err = dec.Decode(&p); err != nil {
		return p, err
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return p, errors.New("ожидается один YAML-документ")
	}
	for i := range p.Tests {
		t := &p.Tests[i]
		if !safeArchivePath(t.InputFile) || !safeArchivePath(t.AnswerFile) {
			return p, errors.New("некорректный путь теста")
		}
		if t.Input, err = read(t.InputFile); err != nil {
			return p, err
		}
		if t.Answer, err = read(t.AnswerFile); err != nil {
			return p, err
		}
	}
	if err = importImages(&p, files); err != nil {
		return p, err
	}
	return p, p.Validate()
}
func safeArchivePath(s string) bool {
	return s != "" && s != "." && !strings.HasPrefix(s, "/") && !strings.Contains(s, "\\") && path.Clean(s) == s && s != ".." && !strings.HasPrefix(s, "../")
}
func exportProblem(p ProblemSpec) ([]byte, error) {
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	if err := exportImages(&p, z); err != nil {
		return nil, err
	}
	p.Tests = append([]TestCase(nil), p.Tests...)
	for i := range p.Tests {
		t := &p.Tests[i]
		t.InputFile = fmt.Sprintf("tests/%03d.in", i+1)
		t.AnswerFile = fmt.Sprintf("tests/%03d.out", i+1)
		for _, f := range []struct{ n, s string }{{t.InputFile, t.Input}, {t.AnswerFile, t.Answer}} {
			w, e := z.Create(f.n)
			if e != nil {
				return nil, e
			}
			if _, e = io.WriteString(w, f.s); e != nil {
				return nil, e
			}
		}
	}
	y, e := yaml.Marshal(p)
	if e != nil {
		return nil, e
	}
	w, e := z.Create("problem.yaml")
	if e != nil {
		return nil, e
	}
	if _, e = w.Write(y); e != nil {
		return nil, e
	}
	if e = z.Close(); e != nil {
		return nil, e
	}
	return b.Bytes(), nil
}
func encode(v any) string {
	b, e := json.Marshal(v)
	if e != nil {
		panic(e)
	}
	return string(b)
}
func defaultProblem() ProblemSpec {
	p := ProblemSpec{FormatVersion: 1, Code: "sum", Title: "Сумма двух чисел", Statement: "Даны два целых числа. Найдите их сумму.", InputFormat: "Одна строка содержит два целых числа a и b: −10⁹ ≤ a, b ≤ 10⁹.", OutputFormat: "Выведите одно целое число — сумму a и b.", Limits: Limits{1000, 256, 1024}, AllowedLanguages: []string{"python3", "cpp17"}, Examples: []Example{{"2 3\n", "5\n", "2 + 3 = 5."}}, Tests: []TestCase{{ID: "01", Input: "2 3\n", Answer: "5\n", Points: 25}, {ID: "02", Input: "-5 5\n", Answer: "0\n", Points: 25}, {ID: "03", Input: "1000000000 1000000000\n", Answer: "2000000000\n", Points: 25}, {ID: "04", Input: "-17 -23\n", Answer: "-40\n", Points: 25}}}
	p.Checker.Type = "tokens"
	p.Scoring.Type = "sum"
	p.Scoring.MaxScore = 100
	return p
}
