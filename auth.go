package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type contextKey string

const userKey contextKey = "user"
const csrfKey contextKey = "csrf"

func currentUser(r *http.Request) User { u, _ := r.Context().Value(userKey).(User); return u }
func tokenHash(s string) string        { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func secureCookie(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}
func setCookie(w http.ResponseWriter, r *http.Request, name, value string, age int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: true, Secure: secureCookie(r), SameSite: http.SameSiteLaxMode, MaxAge: age})
}
func (a *App) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		w.Header().Set("Cache-Control", "no-store")
		r.Body = http.MaxBytesReader(w, r.Body, 22<<20)
		var u User
		if c, e := r.Cookie("session"); e == nil {
			a.db.QueryRow("SELECT u.id,u.login,u.name,u.role,u.class_name FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.token=? AND s.expires>?", tokenHash(c.Value), time.Now().Unix()).Scan(&u.ID, &u.Login, &u.Name, &u.Role, &u.ClassName)
		}
		csrf := ""
		if c, e := r.Cookie("csrf"); e == nil && len(c.Value) == 64 {
			csrf = c.Value
		}
		if csrf == "" {
			csrf = randomToken()
			setCookie(w, r, "csrf", csrf, 86400)
		}
		r = r.WithContext(context.WithValue(context.WithValue(r.Context(), userKey, u), csrfKey, csrf))
		if r.Method == "POST" {
			if origin := r.Header.Get("Origin"); origin != "" {
				o, e := url.Parse(origin)
				if e != nil || o.Host != r.Host {
					http.Error(w, "Недопустимый источник запроса", 403)
					return
				}
			}
			var e error
			if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
				e = r.ParseMultipartForm(22 << 20)
				if r.MultipartForm != nil {
					defer r.MultipartForm.RemoveAll()
				}
			} else {
				e = r.ParseForm()
			}
			if e != nil {
				http.Error(w, "Запрос слишком большой или повреждён", 400)
				return
			}
			if subtle.ConstantTimeCompare([]byte(r.PostFormValue("csrf")), []byte(csrf)) != 1 {
				http.Error(w, "Сессия формы истекла. Обновите страницу.", 403)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
func (a *App) protected(h http.HandlerFunc, teacher bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := currentUser(r)
		if u.ID == 0 {
			http.Redirect(w, r, "/login", 303)
			return
		}
		if teacher && !u.Teacher() {
			http.Error(w, "Доступ только для учителя", 403)
			return
		}
		h(w, r)
	}
}

type attempt struct {
	Count int
	Until time.Time
}
type rateLimiter struct {
	mu    sync.Mutex
	items map[string]attempt
}

func (l *rateLimiter) allow(key string, limit int, period time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.items == nil {
		l.items = map[string]attempt{}
	}
	now := time.Now()
	if len(l.items) > 10000 {
		for k, v := range l.items {
			if now.After(v.Until) {
				delete(l.items, k)
			}
		}
	}
	v := l.items[key]
	if now.After(v.Until) {
		v = attempt{Until: now.Add(period)}
	}
	v.Count++
	l.items[key] = v
	return v.Count <= limit
}
func (a *App) login(w http.ResponseWriter, r *http.Request) {
	d := Page{Title: "Вход"}
	if r.Method == "GET" {
		a.render(w, r, "login", d)
		return
	}
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	if h := r.Header.Get("X-Real-IP"); h != "" {
		ip = h
	}
	if !a.limiter.allow("login:"+ip, 15, 5*time.Minute) {
		http.Error(w, "Слишком много попыток. Подождите 5 минут.", 429)
		return
	}
	var id int64
	var hash string
	e := a.db.QueryRow("SELECT id,password FROM users WHERE login=?", r.FormValue("login")).Scan(&id, &hash)
	if e != nil {
		hash = a.dummyHash
	}
	valid := bcrypt.CompareHashAndPassword([]byte(hash), []byte(r.FormValue("password"))) == nil
	if e != nil || !valid {
		d.Error = "Неверный логин или пароль"
		a.render(w, r, "login", d)
		return
	}
	token := randomToken()
	a.db.Exec("DELETE FROM sessions WHERE expires<?", time.Now().Unix())
	if _, e = a.db.Exec("INSERT INTO sessions(token,user_id,expires) VALUES(?,?,?)", tokenHash(token), id, time.Now().Add(12*time.Hour).Unix()); e != nil {
		a.fail(w, e)
		return
	}
	setCookie(w, r, "session", token, 43200)
	setCookie(w, r, "csrf", randomToken(), 86400)
	http.Redirect(w, r, "/", 303)
}
func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	if c, e := r.Cookie("session"); e == nil {
		a.db.Exec("DELETE FROM sessions WHERE token=?", tokenHash(c.Value))
	}
	setCookie(w, r, "session", "", -1)
	http.Redirect(w, r, "/login", 303)
}
func (a *App) password(w http.ResponseWriter, r *http.Request) {
	d := Page{Title: "Смена пароля"}
	if r.Method == "POST" {
		var hash string
		a.db.QueryRow("SELECT password FROM users WHERE id=?", currentUser(r).ID).Scan(&hash)
		p := r.FormValue("password")
		if bcrypt.CompareHashAndPassword([]byte(hash), []byte(r.FormValue("old"))) != nil {
			d.Error = "Текущий пароль неверен"
		} else if len(p) < 10 || len(p) > 72 {
			d.Error = "Новый пароль: 10–72 байта"
		} else {
			h, e := bcrypt.GenerateFromPassword([]byte(p), 12)
			if e != nil {
				a.fail(w, e)
				return
			}
			if _, e = a.db.Exec("UPDATE users SET password=? WHERE id=?", string(h), currentUser(r).ID); e != nil {
				a.fail(w, e)
				return
			}
			a.db.Exec("DELETE FROM sessions WHERE user_id=?", currentUser(r).ID)
			setCookie(w, r, "session", "", -1)
			http.Redirect(w, r, "/login", 303)
			return
		}
	}
	a.render(w, r, "password", d)
}
