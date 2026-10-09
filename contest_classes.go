package main

import (
	"database/sql"
	"fmt"
	"net/http"
	"slices"
)

// alias is a fixed SQL identifier supplied by the caller, never request input.
func contestClassSQL(alias string) string {
	return "(NOT EXISTS (SELECT 1 FROM contest_classes cc WHERE cc.contest_id=" + alias + ".id) OR EXISTS (SELECT 1 FROM contest_classes cc WHERE cc.contest_id=" + alias + ".id AND cc.class_name=?))"
}

func contestAllowed(c Contest, u User) bool {
	return u.Teacher() || (c.Published && (len(c.Classes) == 0 || slices.Contains(c.Classes, u.ClassName)))
}

func contestClassesFromForm(r *http.Request) ([]string, error) {
	mode := r.FormValue("audience")
	if mode == "" || mode == "all" {
		return nil, nil
	}
	if mode != "classes" {
		return nil, fmt.Errorf("выберите доступ к контесту")
	}
	var result []string
	for _, raw := range r.PostForm["classes"] {
		name, err := normalizeClass(raw)
		if err != nil || name == "" {
			return result, fmt.Errorf("некорректное название класса")
		}
		if !slices.Contains(result, name) {
			result = append(result, name)
		}
	}
	if len(result) == 0 || len(result) > 100 {
		return result, fmt.Errorf("выберите от 1 до 100 классов")
	}
	slices.Sort(result)
	return result, nil
}

func saveContest(db *sql.DB, c *Contest) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	id := c.ID
	if id == 0 {
		res, e := tx.Exec("INSERT INTO contests(title,description,starts,ends,published) VALUES(?,?,?,?,?)", c.Title, c.Description, c.Starts, c.Ends, c.Published)
		if e != nil {
			return e
		}
		id, err = res.LastInsertId()
	} else {
		_, err = tx.Exec("UPDATE contests SET title=?,description=?,starts=?,ends=?,published=? WHERE id=?", c.Title, c.Description, c.Starts, c.Ends, c.Published, id)
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM contest_classes WHERE contest_id=?", id); err != nil {
		return err
	}
	for _, name := range c.Classes {
		if _, err = tx.Exec("INSERT INTO contest_classes(contest_id,class_name) VALUES(?,?)", id, name); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err == nil {
		c.ID = id
	}
	return err
}
