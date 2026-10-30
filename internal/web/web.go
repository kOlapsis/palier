// Package web serves Palier's interface.
package web

import (
	"bytes"
	"crypto/subtle"
	"embed"
	"html/template"
	"log/slog"
	"net/http"
)

//go:embed templates
var templates embed.FS

const cookieName = "palier_token"

type server struct {
	token string
	mux   *http.ServeMux
	pages map[string]*template.Template
}

// New returns the handler of the interface, protected by token everywhere but on /login.
func New(token string) http.Handler {
	s := &server{token: token, mux: http.NewServeMux(), pages: map[string]*template.Template{}}
	for _, p := range []string{"index.html", "login.html"} {
		s.pages[p] = template.Must(template.ParseFS(templates, "templates/layout.html", "templates/"+p))
	}
	s.mux.HandleFunc("GET /login", func(w http.ResponseWriter, r *http.Request) { s.render(w, http.StatusOK, "login.html", nil) })
	s.mux.HandleFunc("POST /login", s.login)
	s.mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) { s.render(w, http.StatusOK, "index.html", nil) })
	return s
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
