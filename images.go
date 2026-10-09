package main

import (
	"archive/zip"
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
	"gopkg.in/yaml.v3"
)

const maxImageBytes = 1 << 20

type ProblemImage struct {
	Data []byte
	MIME string
}

func validateImage(data []byte) (ProblemImage, error) {
	bad := errors.New("нужна картинка PNG или JPEG до 1 МБ и до 4096 × 4096 пикселей")
	if len(data) == 0 || len(data) > maxImageBytes {
		return ProblemImage{}, bad
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "png" && format != "jpeg") || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 4096 || cfg.Height > 4096 {
		return ProblemImage{}, bad
	}
	if _, _, err = image.Decode(bytes.NewReader(data)); err != nil {
		return ProblemImage{}, bad
	}
	return ProblemImage{Data: data, MIME: "image/" + format}, nil
}

// Only Markdown image destinations are interpreted as attachments; code blocks
// and ordinary links cannot accidentally publish a draft image.
func imageDestinations(p ProblemSpec) []string {
	seen := map[string]bool{}
	for _, field := range markdownFields(&p) {
		doc := goldmark.DefaultParser().Parse(text.NewReader([]byte(*field)))
		ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
			if img, ok := n.(*ast.Image); ok && entering {
				seen[string(img.Destination)] = true
			}
			return ast.WalkContinue, nil
		})
	}
	var result []string
	for dest := range seen {
		result = append(result, dest)
	}
	sort.Strings(result)
	return result
}

func markdownFields(p *ProblemSpec) []*string {
	fields := []*string{&p.Statement, &p.InputFormat, &p.OutputFormat, &p.Notes}
	for i := range p.Examples {
		fields = append(fields, &p.Examples[i].Explanation)
	}
	return fields
}

func rewriteImages(p *ProblemSpec, replacements map[string]string) {
	p.Examples = append([]Example(nil), p.Examples...)
	var pairs []string
	var keys []string
	for k := range replacements {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	for _, k := range keys {
		pairs = append(pairs, k, replacements[k])
	}
	replacer := strings.NewReplacer(pairs...)
	for _, field := range markdownFields(p) {
		*field = replacer.Replace(*field)
	}
}

func imageID(dest string) (string, bool) {
	id, ok := strings.CutPrefix(dest, "/images/")
	if !ok || len(id) != 64 {
		return "", false
	}
	for _, c := range id {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return "", false
		}
	}
	return id, true
}

func importImages(p *ProblemSpec, files map[string]*zip.File) error {
	p.Images = map[string]ProblemImage{}
	replacements := map[string]string{}
	for _, dest := range imageDestinations(*p) {
		if !strings.HasPrefix(dest, "assets/") {
			continue
		}
		f := files[dest]
		if !safeArchivePath(dest) || f == nil {
			return fmt.Errorf("нет изображения %s в архиве", dest)
		}
		r, err := f.Open()
		if err != nil {
			return err
		}
		data, err := io.ReadAll(io.LimitReader(r, maxImageBytes+1))
		r.Close()
		if err != nil {
			return err
		}
		img, err := validateImage(data)
		if err != nil {
			return fmt.Errorf("%s: %w", dest, err)
		}
		id := randomToken()
		p.Images[id] = img
		replacements[dest] = "/images/" + id
	}
	rewriteImages(p, replacements)
	return nil
}

func exportImages(p *ProblemSpec, z *zip.Writer) error {
	replacements := map[string]string{}
	for _, dest := range imageDestinations(*p) {
		id, ok := imageID(dest)
		if !ok {
			continue
		}
		img, ok := p.Images[id]
		if !ok {
			return errors.New("не найдено изображение для экспорта")
		}
		ext := ".png"
		if img.MIME == "image/jpeg" {
			ext = ".jpg"
		}
		name := "assets/" + id + ext
		w, err := z.Create(name)
		if err != nil {
			return err
		}
		if _, err = w.Write(img.Data); err != nil {
			return err
		}
		replacements[dest] = name
	}
	rewriteImages(p, replacements)
	return nil
}

func loadImages(db *sql.DB, p *ProblemSpec) error {
	p.Images = map[string]ProblemImage{}
	for _, dest := range imageDestinations(*p) {
		id, ok := imageID(dest)
		if !ok {
			continue
		}
		var img ProblemImage
		if err := db.QueryRow("SELECT data,mime FROM problem_images WHERE id=?", id).Scan(&img.Data, &img.MIME); err != nil {
			return err
		}
		p.Images[id] = img
	}
	return nil
}

func linkImages(tx *sql.Tx, vid int64, p ProblemSpec) error {
	count, total := 0, 0
	for _, dest := range imageDestinations(p) {
		id, ok := imageID(dest)
		if !ok {
			if strings.HasPrefix(dest, "assets/") || strings.HasPrefix(dest, "/images/") {
				return errors.New("некорректная ссылка на картинку; загрузите её заново")
			}
			continue
		}
		count++
		if img, ok := p.Images[id]; ok {
			valid, err := validateImage(img.Data)
			if err != nil {
				return err
			}
			if _, err = tx.Exec("INSERT INTO problem_images(id,mime,data,created) VALUES(?,?,?,?)", id, valid.MIME, valid.Data, time.Now().Unix()); err != nil {
				return err
			}
		}
		var size int
		if err := tx.QueryRow("SELECT length(data) FROM problem_images WHERE id=?", id).Scan(&size); err != nil {
			return errors.New("картинка не найдена; загрузите её заново")
		}
		total += size
		if count > 20 || total > 3<<20 {
			return errors.New("не больше 20 картинок и 3 МБ изображений на задачу")
		}
		if _, err := tx.Exec("INSERT INTO version_images(version_id,image_id) VALUES(?,?)", vid, id); err != nil {
			return err
		}
	}
	// Reserve space for the canonical test paths and ZIP headers so that any
	// image-bearing task saved through the editor can be imported after export.
	if count > 0 {
		manifest, err := yaml.Marshal(p)
		if err != nil {
			return err
		}
		if len(manifest) > maxTestBytes-8192 {
			return errors.New("описание задачи и примеры слишком велики для ZIP")
		}
		total += len(manifest)
		for _, test := range p.Tests {
			total += len(test.Input) + len(test.Answer)
		}
		if total > maxArchiveBytes-(64<<10) {
			return errors.New("задача с картинками превышает лимит архива 20 МБ")
		}
	}
	return nil
}

func (a *App) uploadImage(w http.ResponseWriter, r *http.Request) {
	if !a.limiter.allow(fmt.Sprintf("images:%d", currentUser(r).ID), 60, time.Hour) {
		http.Error(w, "Лимит загрузок: 60 картинок в час", 429)
		return
	}
	f, _, err := r.FormFile("image")
	if err != nil {
		http.Error(w, "Выберите картинку", 400)
		return
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxImageBytes+1))
	if err != nil {
		http.Error(w, "Не удалось прочитать картинку", 400)
		return
	}
	img, err := validateImage(data)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	id := randomToken()
	if _, err = a.db.Exec("INSERT INTO problem_images(id,mime,data,created) VALUES(?,?,?,?)", id, img.MIME, img.Data, time.Now().Unix()); err != nil {
		a.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"url": "/images/" + id})
}

func (a *App) serveImage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := imageID("/images/" + id); !ok {
		http.NotFound(w, r)
		return
	}
	if !currentUser(r).Teacher() {
		var allowed bool
		err := a.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM version_images vi JOIN versions v ON v.id=vi.version_id JOIN problems p ON p.id=v.problem_id JOIN contests c ON c.id=p.contest_id WHERE vi.image_id=? AND c.published=1 AND c.starts<=? AND `+contestClassSQL("c")+`)`, id, time.Now().Unix(), currentUser(r).ClassName).Scan(&allowed)
		if err != nil {
			a.fail(w, err)
			return
		}
		if !allowed {
			http.NotFound(w, r)
			return
		}
	}
	var img ProblemImage
	if err := a.db.QueryRow("SELECT data,mime FROM problem_images WHERE id=?", id).Scan(&img.Data, &img.MIME); err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", img.MIME)
	w.Write(img.Data)
}
