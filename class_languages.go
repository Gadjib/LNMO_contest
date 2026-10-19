package main

import (
	"database/sql"
	"net/http"
)

type ClassLanguage struct {
	Name, Mode string
	Students   int
}

// Class restrictions narrow, but never expand, the task's allowed languages.
// Teachers can still test tasks in every language allowed by the task.
func (a *App) applyClassLanguages(u User, p *Problem) error {
	if u.Role != "student" {
		return nil
	}
	var mode string
	err := a.db.QueryRow("SELECT mode FROM class_language_settings WHERE class_name=?", u.ClassName).Scan(&mode)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	if mode == "both" {
		return nil
	}
	allowed := []string{}
	for _, language := range p.Spec.AllowedLanguages {
		if language == mode {
			allowed = append(allowed, language)
		}
	}
	p.Spec.AllowedLanguages = allowed
	return nil
}

func (a *App) classLanguages(w http.ResponseWriter, r *http.Request) {
	if currentUser(r).Role != "admin" {
		http.Error(w, "Доступ только для администратора", http.StatusForbidden)
		return
	}
	if r.Method == "POST" {
		name, err := normalizeClass(r.FormValue("class_name"))
		mode := r.FormValue("mode")
		if err != nil || (mode != "both" && mode != "python3" && mode != "cpp17") {
			http.Error(w, "Некорректный класс или язык", http.StatusBadRequest)
			return
		}
		_, err = a.db.Exec("INSERT INTO class_language_settings(class_name,mode) VALUES(?,?) ON CONFLICT(class_name) DO UPDATE SET mode=excluded.mode", name, mode)
		if err != nil {
			a.fail(w, err)
			return
		}
		http.Redirect(w, r, "/admin/classes?saved=1", http.StatusSeeOther)
		return
	}
	rows, err := a.db.Query(`WITH classes AS (
 SELECT class_name FROM users WHERE role='student'
 UNION SELECT class_name FROM class_language_settings
 ) SELECT c.class_name,coalesce(s.mode,'both'),(SELECT count(*) FROM users u WHERE u.role='student' AND u.class_name=c.class_name)
 FROM classes c LEFT JOIN class_language_settings s ON s.class_name=c.class_name ORDER BY c.class_name`)
	if err != nil {
		a.fail(w, err)
		return
	}
	var settings []ClassLanguage
	for rows.Next() {
		var row ClassLanguage
		if err = rows.Scan(&row.Name, &row.Mode, &row.Students); err != nil {
			rows.Close()
			a.fail(w, err)
			return
		}
		settings = append(settings, row)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		a.fail(w, err)
		return
	}
	page := Page{Title: "Классы и языки", ClassLanguages: settings}
	if r.URL.Query().Get("saved") == "1" {
		page.Notice = "Языки класса сохранены."
	}
	a.render(w, r, "class-languages", page)
}
