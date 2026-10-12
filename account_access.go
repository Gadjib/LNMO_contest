package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

const passwordPrefix = "$lnmo-sha256$"

// Prehashing avoids bcrypt's 72-byte input limit. Legacy bcrypt hashes remain
// valid, so existing passwords do not need to be reset during deployment.
func hashPassword(password string) (string, error) {
	if password == "" {
		return "", errors.New("пароль не может быть пустым")
	}
	sum := sha256.Sum256([]byte(password))
	hash, err := bcrypt.GenerateFromPassword([]byte(hex.EncodeToString(sum[:])), 12)
	return passwordPrefix + string(hash), err
}
func checkPassword(hash, password string) bool {
	if strings.HasPrefix(hash, passwordPrefix) {
		sum := sha256.Sum256([]byte(password))
		password = hex.EncodeToString(sum[:])
		hash = strings.TrimPrefix(hash, passwordPrefix)
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

func (u User) AccessClass(name string) bool {
	return u.Role == "admin" || (u.Role == "teacher" && (u.AllClasses || slices.Contains(u.TeachingClasses, name)))
}
func (u User) AccessUser(target User) bool {
	return u.Role == "admin" || u.ID == target.ID || (target.Role == "student" && u.AccessClass(target.ClassName))
}

// Fixed aliases only; request parameters are passed as bound SQL arguments.
func studentScope(u User, alias string, own bool) (string, []any) {
	if alias == "" {
		alias = "users."
	}
	if u.Role == "admin" {
		return "", nil
	}
	var cond string
	var args []any
	if u.Role == "teacher" {
		cond = alias + "role='student'"
		if !u.AllClasses {
			cond += " AND EXISTS(SELECT 1 FROM teacher_classes tc WHERE tc.user_id=? AND tc.class_name=" + alias + "class_name)"
			args = append(args, u.ID)
		}
	} else {
		cond = "0"
	}
	if own {
		cond = "(" + cond + ") OR " + alias + "id=?"
		args = append(args, u.ID)
	}
	return " AND (" + cond + ")", args
}

func getAccount(db *sql.DB, id int64) (User, error) {
	var u User
	e := db.QueryRow(`SELECT u.id,u.login,u.name,u.role,u.class_name,coalesce(a.blocked,0),coalesce(a.teacher_all,1) FROM users u LEFT JOIN account_settings a ON a.user_id=u.id WHERE u.id=?`, id).Scan(&u.ID, &u.Login, &u.Name, &u.Role, &u.ClassName, &u.Blocked, &u.AllClasses)
	if e != nil {
		return u, e
	}
	rows, e := db.Query("SELECT class_name FROM teacher_classes WHERE user_id=? ORDER BY class_name", id)
	if e != nil {
		return u, e
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if e = rows.Scan(&name); e != nil {
			return u, e
		}
		u.TeachingClasses = append(u.TeachingClasses, name)
	}
	return u, rows.Err()
}

func (a *App) accountFromForm(r *http.Request, id int64) (User, error) {
	u := User{ID: id, Login: strings.TrimSpace(r.FormValue("login")), Name: strings.TrimSpace(r.FormValue("name")), Role: r.FormValue("role"), Blocked: r.FormValue("blocked") == "on", AllClasses: r.FormValue("teacher_access") == "all"}
	if !codePattern.MatchString(u.Login) || u.Name == "" || len(u.Name) > 200 {
		return u, errors.New("укажите имя до 200 байт и логин: 1–40 латинских букв, цифр, _ или -")
	}
	if u.Role != "student" && u.Role != "teacher" && u.Role != "admin" {
		return u, errors.New("выберите роль")
	}
	var e error
	if u.Role == "student" {
		u.ClassName, e = normalizeClass(r.FormValue("class_name"))
		if e != nil {
			return u, e
		}
	}
	if u.Role == "teacher" && !u.AllClasses {
		entries := append([]string(nil), r.PostForm["teacher_classes"]...)
		entries = append(entries, strings.Split(r.FormValue("extra_classes"), ",")...)
		for _, raw := range entries {
			if strings.TrimSpace(raw) == "" {
				continue
			}
			name := ""
			if raw != "~" {
				name, e = normalizeClass(raw)
				if e != nil {
					return u, e
				}
			}
			if !slices.Contains(u.TeachingClasses, name) {
				u.TeachingClasses = append(u.TeachingClasses, name)
			}
		}
		if len(u.TeachingClasses) > 100 {
			return u, errors.New("не больше 100 классов")
		}
	}
	return u, nil
}

func saveAccount(db *sql.DB, u User, password string) (int64, error) {
	var hash string
	var e error
	if u.ID == 0 || password != "" {
		hash, e = hashPassword(password)
		if e != nil {
			return 0, e
		}
	}
	tx, e := db.Begin()
	if e != nil {
		return 0, e
	}
	defer tx.Rollback()
	if u.ID != 0 {
		var oldRole string
		if e = tx.QueryRow("SELECT role FROM users WHERE id=?", u.ID).Scan(&oldRole); e != nil {
			return 0, e
		}
		if oldRole == "admin" && (u.Role != "admin" || u.Blocked) {
			var count int
			if e = tx.QueryRow("SELECT count(*) FROM users u LEFT JOIN account_settings a ON a.user_id=u.id WHERE u.role='admin' AND coalesce(a.blocked,0)=0 AND u.id<>?", u.ID).Scan(&count); e != nil {
				return 0, e
			}
			if count == 0 {
				return 0, errors.New("нельзя отключить или понизить последнего активного администратора")
			}
		}
		_, e = tx.Exec("UPDATE users SET login=?,name=?,role=?,class_name=? WHERE id=?", u.Login, u.Name, u.Role, u.ClassName, u.ID)
	} else {
		var res sql.Result
		res, e = tx.Exec("INSERT INTO users(login,name,role,class_name,password) VALUES(?,?,?,?,?)", u.Login, u.Name, u.Role, u.ClassName, hash)
		if e == nil {
			u.ID, e = res.LastInsertId()
		}
	}
	if e != nil {
		return 0, e
	}
	if hash != "" {
		if _, e = tx.Exec("UPDATE users SET password=? WHERE id=?", hash, u.ID); e != nil {
			return 0, e
		}
	}
	if _, e = tx.Exec("INSERT INTO account_settings(user_id,blocked,teacher_all) VALUES(?,?,?) ON CONFLICT(user_id) DO UPDATE SET blocked=excluded.blocked,teacher_all=excluded.teacher_all", u.ID, u.Blocked, u.AllClasses); e != nil {
		return 0, e
	}
	if _, e = tx.Exec("DELETE FROM teacher_classes WHERE user_id=?", u.ID); e != nil {
		return 0, e
	}
	if u.Role == "teacher" {
		for _, name := range u.TeachingClasses {
			if _, e = tx.Exec("INSERT INTO teacher_classes(user_id,class_name) VALUES(?,?)", u.ID, name); e != nil {
				return 0, e
			}
		}
	}
	if _, e = tx.Exec("DELETE FROM sessions WHERE user_id=?", u.ID); e != nil {
		return 0, e
	}
	return u.ID, tx.Commit()
}

func (a *App) editAccount(w http.ResponseWriter, r *http.Request) {
	if currentUser(r).Role != "admin" {
		http.Error(w, "Доступ только для администратора", 403)
		return
	}
	u, e := getAccount(a.db, idParam(r))
	if e != nil {
		http.NotFound(w, r)
		return
	}
	d := Page{Title: "Редактор аккаунта", Account: u}
	if r.Method == "POST" {
		d.Account, e = a.accountFromForm(r, u.ID)
		if e == nil {
			_, e = saveAccount(a.db, d.Account, r.FormValue("password"))
		}
		if e == nil {
			http.Redirect(w, r, "/teacher/users?saved=1", 303)
			return
		}
		d.Error = accountError(e)
	}
	a.render(w, r, "edit-account", d)
}
func accountError(e error) string {
	if strings.Contains(e.Error(), "UNIQUE constraint failed") {
		return "Этот логин уже занят"
	}
	if strings.Contains(e.Error(), "SQL") {
		return "Не удалось сохранить аккаунт"
	}
	return fmt.Sprint(e)
}
