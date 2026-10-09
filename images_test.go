package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func imageFixture(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func imageRequest(t *testing.T, a *App, uid int64, data []byte, validCSRF bool) *httptest.ResponseRecorder {
	t.Helper()
	token := randomToken()
	if _, err := a.db.Exec("INSERT INTO sessions(token,user_id,expires) VALUES(?,?,?)", tokenHash(token), uid, time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	m := multipart.NewWriter(&b)
	csrf := strings.Repeat("c", 64)
	if validCSRF {
		m.WriteField("csrf", csrf)
	}
	f, err := m.CreateFormFile("image", "untrusted.svg")
	if err != nil {
		t.Fatal(err)
	}
	f.Write(data)
	m.Close()
	r := httptest.NewRequest("POST", "/teacher/images", &b)
	r.Header.Set("Content-Type", m.FormDataContentType())
	r.AddCookie(&http.Cookie{Name: "session", Value: token})
	r.AddCookie(&http.Cookie{Name: "csrf", Value: csrf})
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, r)
	return w
}

func TestImagesUploadAccessVersionsAndExport(t *testing.T) {
	a, pid := testApp(t)
	data := imageFixture(t)
	for _, tc := range []struct {
		uid    int64
		csrf   bool
		data   []byte
		status int
	}{
		{1, true, data, 403}, {2, false, data, 403}, {2, true, []byte("<svg onload='alert(1)'></svg>"), 400},
		{2, true, bytes.Repeat([]byte("x"), maxImageBytes+1), 400}, {2, true, data[:len(data)/2], 400},
	} {
		if w := imageRequest(t, a, tc.uid, tc.data, tc.csrf); w.Code != tc.status {
			t.Fatalf("upload status %d want %d", w.Code, tc.status)
		}
	}
	w := imageRequest(t, a, 2, data, true)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var result map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	url := result["url"]
	if w := requestAs(t, a, 1, "GET", url, nil); w.Code != 404 {
		t.Fatal("student can view unattached image")
	}
	if w := requestAs(t, a, 2, "GET", url, nil); w.Code != 200 || w.Header().Get("Content-Type") != "image/png" || !bytes.Equal(w.Body.Bytes(), data) {
		t.Fatal("teacher preview failed")
	}
	p := defaultProblem()
	p.Statement += "\n![Схема](" + url + ")"
	if _, err := saveProblem(a.db, 1, pid, p); err != nil {
		t.Fatal(err)
	}
	if w := requestAs(t, a, 1, "GET", url, nil); w.Code != 200 {
		t.Fatal("student cannot view published image")
	}
	page := requestAs(t, a, 1, "GET", "/problems/1", nil).Body.String()
	if !strings.Contains(page, `<img src="`+url+`"`) {
		t.Fatal("image removed by sanitizer", page)
	}
	for _, q := range []string{"UPDATE contests SET published=0", "UPDATE contests SET published=1,starts=4102444800"} {
		if _, err := a.db.Exec(q); err != nil {
			t.Fatal(err)
		}
		if w := requestAs(t, a, 1, "GET", url, nil); w.Code != 404 {
			t.Fatal("hidden image leaked")
		}
	}
	a.db.Exec("UPDATE contests SET starts=0")
	w = requestAs(t, a, 2, "GET", "/teacher/problems/1/export", nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	imported, err := importProblem(w.Body.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(imported.Statement, url) || len(imported.Images) != 1 {
		t.Fatal("image was not portably imported")
	}
	imported.Code = "copy"
	if _, err = saveProblem(a.db, 1, 0, imported); err != nil {
		t.Fatal(err)
	}
	newURL := imageDestinations(imported)[0]
	if w := requestAs(t, a, 1, "GET", newURL, nil); w.Code != 200 || !bytes.Equal(w.Body.Bytes(), data) {
		t.Fatal("roundtrip data mismatch")
	}
	if _, err = saveProblem(a.db, 1, pid, defaultProblem()); err != nil {
		t.Fatal(err)
	}
	if w := requestAs(t, a, 1, "GET", url, nil); w.Code != 200 {
		t.Fatal("old version image lost")
	}
	p.Statement = "![Missing](/images/" + strings.Repeat("a", 64) + ")"
	var before, after int
	a.db.QueryRow("SELECT count(*) FROM versions").Scan(&before)
	if _, err = saveProblem(a.db, 1, pid, p); err == nil {
		t.Fatal("missing image accepted")
	}
	a.db.QueryRow("SELECT count(*) FROM versions").Scan(&after)
	if before != after {
		t.Fatal("failed save was not rolled back")
	}
}

func TestArchiveImageValidation(t *testing.T) {
	base, err := exportProblem(defaultProblem())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		dest string
		data []byte
		ok   bool
	}{
		{"assets/diagram.png", imageFixture(t), true},
		{"assets/missing.png", nil, false},
		{"assets/diagram.png", []byte("<svg></svg>"), false},
		{"assets/../secret.png", imageFixture(t), false},
	} {
		var b bytes.Buffer
		z := zip.NewWriter(&b)
		r, _ := zip.NewReader(bytes.NewReader(base), int64(len(base)))
		for _, f := range r.File {
			in, _ := f.Open()
			data, _ := io.ReadAll(in)
			in.Close()
			if f.Name == "problem.yaml" {
				data = bytes.Replace(data, []byte("statement: "), []byte("statement: |\n  ![Diagram]("+tc.dest+")\n  "), 1)
			}
			out, _ := z.Create(f.Name)
			out.Write(data)
		}
		if tc.data != nil {
			out, _ := z.Create(tc.dest)
			out.Write(tc.data)
		}
		z.Close()
		p, err := importProblem(b.Bytes())
		if (err == nil) != tc.ok {
			t.Fatalf("%s: %v", tc.dest, err)
		}
		if tc.ok && len(p.Images) != 1 {
			t.Fatal("missing imported image")
		}
	}
}

func TestImageReferencesAndLimits(t *testing.T) {
	p := defaultProblem()
	id := randomToken()
	p.Statement = "![ref][diagram]\n\n[diagram]: /images/" + id + "\n\n```\n![ignored](/images/" + randomToken() + ")\n```"
	if refs := imageDestinations(p); len(refs) != 1 || refs[0] != "/images/"+id {
		t.Fatal(refs)
	}
	a, pid := testApp(t)
	p.Images = map[string]ProblemImage{}
	p.Statement = ""
	for i := 0; i < 21; i++ {
		id = randomToken()
		p.Images[id] = ProblemImage{Data: imageFixture(t), MIME: "image/png"}
		p.Statement += "![image](/images/" + id + ")\n"
	}
	if _, err := saveProblem(a.db, 1, pid, p); err == nil {
		t.Fatal("too many images accepted")
	}
	var n int
	a.db.QueryRow("SELECT count(*) FROM problem_images").Scan(&n)
	if n != 0 {
		t.Fatal("rollback left orphan images")
	}
}
