package main

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

var classPattern = regexp.MustCompile(`^[\p{L}\p{N}][\p{L}\p{N} ._-]{0,39}$`)

func normalizeClass(s string) (string, error) {
	s = strings.ToLower(strings.Join(strings.Fields(s), " "))
	if s != "" && !classPattern.MatchString(s) {
		return "", fmt.Errorf("класс: до 40 букв, цифр, пробелов, точек, дефисов или подчёркиваний")
	}
	return s, nil
}

// Empty filter means everyone; ~ means students without an assigned class.
func classWhere(filter, alias string) (string, []any) {
	if filter == "" {
		return "", nil
	}
	if filter == "~" {
		filter = ""
	}
	return " AND " + alias + "role='student' AND " + alias + "class_name=?", []any{filter}
}

func (a *App) changeClass(w http.ResponseWriter, r *http.Request) {
	name, e := normalizeClass(r.FormValue("class_name"))
	if e != nil {
		http.Error(w, e.Error(), 400)
		return
	}
	result, e := a.db.Exec("UPDATE users SET class_name=? WHERE id=? AND role='student'", name, idParam(r))
	if e != nil {
		a.fail(w, e)
		return
	}
	n, e := result.RowsAffected()
	if e != nil {
		a.fail(w, e)
		return
	}
	if n != 1 {
		http.NotFound(w, r)
		return
	}
	q := url.Values{"class": {r.FormValue("filter_class")}, "class_saved": {"1"}}
	http.Redirect(w, r, "/teacher/users?"+q.Encode(), http.StatusSeeOther)
}
