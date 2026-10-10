package main

import (
	"archive/zip"
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type ContestManifest struct {
	FormatVersion int      `yaml:"format_version"`
	Title         string   `yaml:"title"`
	Description   string   `yaml:"description,omitempty"`
	Starts        string   `yaml:"starts"`
	Ends          string   `yaml:"ends"`
	Classes       []string `yaml:"classes"`
	Problems      []string `yaml:"problems"`
}

func importContest(data []byte) (Contest, []ProblemSpec, error) {
	var c Contest
	bad := func(e error) (Contest, []ProblemSpec, error) { return c, nil, e }
	if len(data) > maxArchiveBytes {
		return bad(errors.New("архив больше 20 МБ"))
	}
	z, e := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if e != nil {
		return bad(e)
	}
	if len(z.File) > 11251 {
		return bad(errors.New("слишком много файлов"))
	}
	files := map[string]*zip.File{}
	var total uint64
	for _, f := range z.File {
		name := f.Name
		if f.FileInfo().IsDir() {
			name = strings.TrimSuffix(name, "/")
		}
		if !safeArchivePath(name) || (!f.FileInfo().IsDir() && !f.Mode().IsRegular()) {
			return bad(errors.New("недопустимый путь или тип файла"))
		}
		if _, ok := files[f.Name]; ok {
			return bad(errors.New("повторяющееся имя файла"))
		}
		if f.UncompressedSize64 > maxTestBytes || f.UncompressedSize64 > maxArchiveBytes-total {
			return bad(errors.New("распакованный архив больше 20 МБ или файл больше 1 МБ"))
		}
		total += f.UncompressedSize64
		files[f.Name] = f
	}
	f := files["contest.yaml"]
	if f == nil {
		return bad(errors.New("нет contest.yaml в корне архива"))
	}
	r, e := f.Open()
	if e != nil {
		return bad(e)
	}
	raw, e := io.ReadAll(io.LimitReader(r, maxTestBytes+1))
	r.Close()
	if e != nil {
		return bad(e)
	}
	if len(raw) > maxTestBytes {
		return bad(errors.New("contest.yaml больше 1 МБ"))
	}
	var m ContestManifest
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if e = dec.Decode(&m); e != nil {
		return bad(e)
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return bad(errors.New("ожидается один YAML-документ"))
	}
	if m.FormatVersion != 1 || strings.TrimSpace(m.Title) == "" || len(m.Title) > 300 || len(m.Description) > 100000 {
		return bad(errors.New("нужны format_version: 1 и название до 300 байт; описание до 100 КБ"))
	}
	start, e := time.Parse(time.RFC3339, m.Starts)
	if e != nil {
		return bad(errors.New("starts: укажите дату RFC3339 с часовым поясом"))
	}
	end, e := time.Parse(time.RFC3339, m.Ends)
	if e != nil || !end.After(start) {
		return bad(errors.New("ends: дата RFC3339 должна быть позже starts"))
	}
	c = Contest{Title: strings.TrimSpace(m.Title), Description: m.Description, Starts: start.Unix(), Ends: end.Unix()}
	if len(m.Classes) > 100 {
		return bad(errors.New("не больше 100 классов"))
	}
	seenClasses := map[string]bool{}
	for _, s := range m.Classes {
		name, err := normalizeClass(s)
		if err != nil || name == "" {
			return bad(errors.New("некорректный класс"))
		}
		if !seenClasses[name] {
			c.Classes = append(c.Classes, name)
			seenClasses[name] = true
		}
	}
	if len(m.Problems) < 1 || len(m.Problems) > 50 {
		return bad(errors.New("нужно от 1 до 50 задач"))
	}
	dirs := map[string]bool{}
	codes := map[string]bool{}
	var specs []ProblemSpec
	for _, dir := range m.Problems {
		part, ok := strings.CutPrefix(dir, "problems/")
		if !ok || !codePattern.MatchString(part) || dirs[dir] {
			return bad(errors.New("папки задач: problems/КОД, без повторов"))
		}
		dirs[dir] = true
		var b bytes.Buffer
		w := zip.NewWriter(&b)
		for _, f := range z.File {
			if strings.HasPrefix(f.Name, dir+"/") && !f.FileInfo().IsDir() {
				copy := *f
				copy.Name = strings.TrimPrefix(f.Name, dir+"/")
				if e = w.Copy(&copy); e != nil {
					return bad(e)
				}
			}
		}
		if e = w.Close(); e != nil {
			return bad(e)
		}
		p, err := importProblem(b.Bytes())
		if err != nil {
			return bad(fmt.Errorf("%s: %w", dir, err))
		}
		if codes[p.Code] {
			return bad(fmt.Errorf("повторяющийся код задачи %s", p.Code))
		}
		codes[p.Code] = true
		for _, dest := range imageDestinations(p) {
			if id, ok := imageID(dest); ok {
				if _, included := p.Images[id]; !included {
					return bad(errors.New("картинки должны находиться в assets/ внутри папки задачи"))
				}
			}
		}
		specs = append(specs, p)
	}
	for name, f := range files {
		if name == "contest.yaml" || f.FileInfo().IsDir() {
			continue
		}
		parts := strings.Split(name, "/")
		if len(parts) < 3 || !dirs[strings.Join(parts[:2], "/")] {
			return bad(fmt.Errorf("лишний файл вне папок задач: %s", name))
		}
	}
	return c, specs, nil
}

func saveImportedContest(db *sql.DB, c Contest, problems []ProblemSpec) (int64, error) {
	tx, e := db.Begin()
	if e != nil {
		return 0, e
	}
	defer tx.Rollback()
	res, e := tx.Exec("INSERT INTO contests(title,description,starts,ends,published) VALUES(?,?,?,?,0)", c.Title, c.Description, c.Starts, c.Ends)
	if e != nil {
		return 0, e
	}
	id, e := res.LastInsertId()
	if e != nil {
		return 0, e
	}
	for _, name := range c.Classes {
		if _, e = tx.Exec("INSERT INTO contest_classes(contest_id,class_name) VALUES(?,?)", id, name); e != nil {
			return 0, e
		}
	}
	for _, p := range problems {
		if _, e = saveProblemTx(tx, id, 0, p); e != nil {
			return 0, fmt.Errorf("задача %s: %w", p.Code, e)
		}
	}
	return id, tx.Commit()
}

func exportContest(c Contest, problems []ProblemSpec) ([]byte, error) {
	if len(problems) < 1 || len(problems) > 50 {
		return nil, errors.New("для экспорта нужно от 1 до 50 задач")
	}
	m := ContestManifest{FormatVersion: 1, Title: c.Title, Description: c.Description, Starts: time.Unix(c.Starts, 0).Format(time.RFC3339), Ends: time.Unix(c.Ends, 0).Format(time.RFC3339), Classes: c.Classes}
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	var total uint64
	for _, p := range problems {
		data, e := exportProblem(p)
		if e != nil {
			return nil, e
		}
		z, e := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if e != nil {
			return nil, e
		}
		dir := "problems/" + p.Code
		m.Problems = append(m.Problems, dir)
		for _, f := range z.File {
			if f.UncompressedSize64 > maxTestBytes || f.UncompressedSize64 > maxArchiveBytes-total {
				return nil, errors.New("контест превышает лимиты ZIP: 20 МБ всего, 1 МБ на файл")
			}
			total += f.UncompressedSize64
			copy := *f
			copy.Name = dir + "/" + f.Name
			if e = w.Copy(&copy); e != nil {
				return nil, e
			}
		}
	}
	manifest, e := yaml.Marshal(m)
	if e != nil {
		return nil, e
	}
	if total+uint64(len(manifest)) > maxArchiveBytes {
		return nil, errors.New("контест больше 20 МБ")
	}
	file, e := w.Create("contest.yaml")
	if e != nil {
		return nil, e
	}
	if _, e = file.Write(manifest); e != nil {
		return nil, e
	}
	if e = w.Close(); e != nil {
		return nil, e
	}
	if b.Len() > maxArchiveBytes {
		return nil, errors.New("архив больше 20 МБ")
	}
	return b.Bytes(), nil
}

func (a *App) importContestPage(w http.ResponseWriter, r *http.Request) {
	d := Page{Title: "Импорт контеста"}
	if r.Method == "POST" {
		f, _, e := r.FormFile("archive")
		if e != nil {
			d.Error = "Выберите ZIP-архив"
		} else {
			defer f.Close()
			data, err := io.ReadAll(io.LimitReader(f, maxArchiveBytes+1))
			e = err
			var c Contest
			var problems []ProblemSpec
			if e == nil {
				c, problems, e = importContest(data)
			}
			if e == nil {
				var id int64
				id, e = saveImportedContest(a.db, c, problems)
				if e == nil {
					http.Redirect(w, r, fmt.Sprintf("/teacher/contests/%d/edit", id), 303)
					return
				}
			}
			d.Error = "Не удалось импортировать контест: " + e.Error()
		}
	}
	a.render(w, r, "import-contest", d)
}

func (a *App) exportContestPage(w http.ResponseWriter, r *http.Request) {
	c, e := getContest(a.db, idParam(r))
	if e != nil {
		http.NotFound(w, r)
		return
	}
	var count, size int
	e = a.db.QueryRow("SELECT count(*),coalesce(sum(length(v.spec)),0) FROM problems p JOIN versions v ON v.id=p.current_version WHERE p.contest_id=?", c.ID).Scan(&count, &size)
	if e != nil {
		a.fail(w, e)
		return
	}
	if count < 1 || count > 50 || size > 2*maxArchiveBytes {
		http.Error(w, "Для ZIP нужно 1–50 задач, общий размер до 20 МБ", 400)
		return
	}
	problems, e := a.problemList(c.ID)
	if e != nil {
		a.fail(w, e)
		return
	}
	var specs []ProblemSpec
	imageBytes := 0
	for _, p := range problems {
		if e = loadImages(a.db, &p.Spec); e != nil {
			a.fail(w, e)
			return
		}
		for _, img := range p.Spec.Images {
			imageBytes += len(img.Data)
		}
		if imageBytes > maxArchiveBytes {
			http.Error(w, "Картинки контеста превышают 20 МБ", 400)
			return
		}
		specs = append(specs, p.Spec)
	}
	data, e := exportContest(c, specs)
	if e != nil {
		http.Error(w, e.Error(), 400)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="contest-%d.zip"`, c.ID))
	w.Write(data)
}
