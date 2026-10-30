// Package web serves Palier's interface.
package web

import (
	"bytes"
	"crypto/subtle"
	"embed"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"time"

	"github.com/kolapsis/palier/internal/inventory"
)

//go:embed templates
var templates embed.FS

const cookieName = "palier_token"

// Snapshot is the result of the last inspection.
type Snapshot struct {
	Stacks []*inventory.Stack
	At     time.Time
	Err    string
}

type server struct {
	token   string
	current func() Snapshot
	mux     *http.ServeMux
	pages   map[string]*template.Template
}

var funcs = template.FuncMap{
	"state": func(st *inventory.Stack) string {
		if st.Observed {
			return "observée seulement"
		}
		state := "à jour"
		for _, s := range st.Services {
			if len(s.Available) > 0 {
				return "versions disponibles"
			}
			if s.DetectionError != "" {
				state = "détection incomplète"
			}
		}
		return state
	},
	"since": func(t time.Time) string {
		if t.IsZero() {
			return "jamais"
		}
		d := time.Since(t).Round(time.Second)
		if d < time.Minute {
			return fmt.Sprintf("il y a %d s", int(d.Seconds()))
		}
		return fmt.Sprintf("il y a %d min", int(d.Minutes()))
	},
}

// New returns the handler of the interface, protected by token everywhere but on /login.
func New(token string, current func() Snapshot) http.Handler {
	s := &server{token: token, current: current, mux: http.NewServeMux(), pages: map[string]*template.Template{}}
	for _, p := range []string{"index.html", "login.html", "stack.html"} {
		s.pages[p] = template.Must(template.New("").Funcs(funcs).ParseFS(templates, "templates/layout.html", "templates/"+p))
	}
	s.mux.HandleFunc("GET /login", func(w http.ResponseWriter, r *http.Request) { s.render(w, http.StatusOK, "login.html", nil) })
	s.mux.HandleFunc("POST /login", s.login)
	s.mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) { s.render(w, http.StatusOK, "index.html", s.current()) })
	s.mux.HandleFunc("GET /stacks/{name}", s.stack)
	return s
}

func (s *server) stack(w http.ResponseWriter, r *http.Request) {
	snap := s.current()
	for _, st := range snap.Stacks {
		if st.Name == r.PathValue("name") {
			s.render(w, http.StatusOK, "stack.html", map[string]any{"Stack": st, "At": snap.At})
			return
		}
	}
	http.NotFound(w, r)
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/login" {
		c, err := r.Cookie(cookieName)
		if err != nil || !s.valid(c.Value) {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
	}
	s.mux.ServeHTTP(w, r)
}

func (s *server) valid(token string) bool {
	return subtle.ConstantTimeCompare([]byte(token), []byte(s.token)) == 1
}

func (s *server) login(w http.ResponseWriter, r *http.Request) {
	token := r.FormValue("token")
	if !s.valid(token) {
		s.render(w, http.StatusUnauthorized, "login.html", map[string]string{"Error": "Jeton incorrect."})
		return
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: token, Path: "/", MaxAge: 30 * 24 * 3600, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *server) render(w http.ResponseWriter, status int, page string, data any) {
	var buf bytes.Buffer
	if err := s.pages[page].ExecuteTemplate(&buf, "layout", data); err != nil {
		slog.Error("rendu de page", "page", page, "err", err)
		http.Error(w, "erreur interne", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	buf.WriteTo(w)
}
