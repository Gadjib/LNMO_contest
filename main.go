package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

//go:embed templates/* static/*
var assets embed.FS

func main() {
	if len(os.Args) < 2 {
		log.Fatal("usage: contests serve|worker|init|user|sandbox-check")
	}
	fs := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	dbPath := fs.String("db", "data/contests.db", "SQLite file")
	addr := fs.String("addr", "127.0.0.1:8080", "HTTP listen address")
	login := fs.String("login", "admin", "account login")
	name := fs.String("name", "Администратор", "display name")
	role := fs.String("role", "admin", "student, teacher or admin")
	fs.Parse(os.Args[2:])
	if os.Args[1] == "sandbox-check" {
		if e := sandboxCheck(); e != nil {
			log.Fatal(e)
		}
		fmt.Println("sandbox OK")
		return
	}
	db, e := openDB(*dbPath)
	if e != nil {
		log.Fatal(e)
	}
	defer db.Close()
	switch os.Args[1] {
	case "init":
		var n int
		db.QueryRow("SELECT count(*) FROM users").Scan(&n)
		if n != 0 {
			log.Fatal("database already has users; use user command")
		}
		if e = createUser(db, *login, *name, "admin", os.Getenv("CONTEST_PASSWORD")); e != nil {
			log.Fatal(e)
		}
		r, e := db.Exec("INSERT INTO contests(title,description,starts,ends,published) VALUES(?,?,?,?,1)", "Первый контест", "Знакомство с системой: отправьте решение задачи на Python или C++.", time.Now().Add(-time.Hour).Unix(), time.Now().AddDate(1, 0, 0).Unix())
		if e != nil {
			log.Fatal(e)
		}
		cid, _ := r.LastInsertId()
		if _, e = saveProblem(db, cid, 0, defaultProblem()); e != nil {
			log.Fatal(e)
		}
		fmt.Println("Initialized; admin:", *login)
	case "user":
		if e = createUser(db, *login, *name, *role, os.Getenv("CONTEST_PASSWORD")); e != nil {
			log.Fatal(e)
		}
		fmt.Println("Account created:", *login)
	case "worker":
		if e = runWorker(db, *dbPath); e != nil {
			log.Fatal(e)
		}
	case "serve":
		app := newApp(db)
		server := &http.Server{Addr: *addr, Handler: app.routes(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16384}
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		go func() {
			<-ctx.Done()
			c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			server.Shutdown(c)
		}()
		log.Printf("listening on %s", *addr)
		if e = server.ListenAndServe(); e != nil && e != http.ErrServerClosed {
			log.Fatal(e)
		}
	default:
		log.Fatal("unknown command")
	}
}
func createUser(db *sql.DB, login, name, role, password string, class ...string) error {
	className := ""
	if role == "student" && len(class) > 0 {
		var e error
		className, e = normalizeClass(class[0])
		if e != nil {
			return e
		}
	}
	if !codePattern.MatchString(login) || strings.TrimSpace(name) == "" || len(name) > 200 {
		return fmt.Errorf("логин: латинские буквы, цифры, _ и -; имя обязательно")
	}
	if role != "student" && role != "teacher" && role != "admin" {
		return fmt.Errorf("неверная роль")
	}
	_, e := saveAccount(db, User{Login: login, Name: name, Role: role, ClassName: className}, password)
	return e
}
func randomToken() string {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func statusLabel(s string) string {
	m := map[string]string{"QUEUED": "В очереди", "RUNNING": "Проверяется", "AC": "Принято", "WA": "Неверный ответ", "TLE": "Превышено время", "MLE": "Превышена память", "RE": "Ошибка выполнения", "CE": "Ошибка компиляции", "OLE": "Превышен вывод", "JE": "Ошибка системы"}
	if v, ok := m[s]; ok {
		return v
	}
	return s
}
func dateTime(n int64) string {
	loc, _ := time.LoadLocation("Europe/Moscow")
	if loc == nil {
		loc = time.UTC
	}
	return time.Unix(n, 0).In(loc).Format("02.01.2006 15:04")
}
func formTime(n int64) string {
	loc, _ := time.LoadLocation("Europe/Moscow")
	if loc == nil {
		loc = time.UTC
	}
	return time.Unix(n, 0).In(loc).Format("2006-01-02T15:04")
}
func parseTime(s string) (int64, error) {
	loc, _ := time.LoadLocation("Europe/Moscow")
	if loc == nil {
		loc = time.UTC
	}
	t, e := time.ParseInLocation("2006-01-02T15:04", s, loc)
	return t.Unix(), e
}
