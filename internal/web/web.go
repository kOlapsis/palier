// Package web serves Palier's interface.
package web

import (
	"bytes"
	"crypto/subtle"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/kolapsis/palier/internal/config"
	"github.com/kolapsis/palier/internal/inventory"
	"github.com/kolapsis/palier/internal/plan"
	"github.com/kolapsis/palier/internal/run"
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

// Deps is what the interface reads and asks of the rest of Palier.
type Deps struct {
	Current   func() Snapshot
	Plan      func(stack string) (*plan.Record, error)
	Apply     func(stack string, dryRun bool) (int64, error)
	Decide    func(stack, kind, service, version, until string) error
	Postponed func(stack string) time.Time
	Run       func(id int64) (*run.View, error)
	Runs      func(stack string) ([]run.View, error)
	Config    config.Config
}

type server struct {
	token string
	deps  Deps
	mux   *http.ServeMux
	pages map[string]*template.Template
}

var stateNames = map[string]string{
	run.Running:    "en cours",
	run.Failed:     "échec",
	run.RolledBack: "revenue en arrière",
}

func (s *server) state(st *inventory.Stack) string {
	if st.Observed {
		return "observée seulement"
	}
	if runs, _ := s.deps.Runs(st.Name); len(runs) > 0 && !runs[0].DryRun {
		if name, ok := stateNames[runs[0].Status]; ok {
			return name
		}
	}
	rec, _ := s.deps.Plan(st.Name)
	switch {
	case rec != nil && len(rec.Plan.Incomplete) > 0:
		return "plan incomplet"
	case rec != nil && len(rec.Plan.Steps) > 0:
		if ok, _ := plan.Automatic(rec.Plan, s.deps.Config.Auto); ok {
			return "plan prêt"
		}
		return "en attente de décision"
	}
	for _, svc := range st.Services {
		if svc.DetectionError != "" {
			return "détection incomplète"
		}
	}
	return "à jour"
}

func since(t time.Time) string {
	if t.IsZero() {
		return "jamais"
	}
	d := time.Since(t).Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("il y a %d s", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("il y a %d min", int(d.Minutes()))
	}
	return fmt.Sprintf("il y a %d h", int(d.Hours()))
}

var labels = map[string]string{
	"safe": "sûr", "attention": "attention", "blocking": "bloquant", "unknown": "inconnu",
	"yes": "oui", "conditional": "sous condition", "no": "non",
	"done": "terminée", "rolled_back": "revenue en arrière", "failed": "échec", "abandoned": "abandonnée", "running": "en cours",
	"application": "application", "dependency": "dépendance", "observed": "observé",
	"ai": "IA", "versions": "versions seules",
}

// New returns the handler of the interface, protected by token everywhere but on /login.
func New(token string, deps Deps) http.Handler {
	s := &server{token: token, deps: deps, mux: http.NewServeMux(), pages: map[string]*template.Template{}}
	funcs := template.FuncMap{
		"state": s.state,
		"since": since,
		"label": func(v string) string {
			if l, ok := labels[v]; ok {
				return l
			}
			return v
		},
		"join": func(v []string) string { return strings.Join(v, ", ") },
		"inc":  func(i int) int { return i + 1 },
		"when": func(v string) string {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				return v
			}
			return t.Format("02/01/2006 15:04:05") + " UTC"
		},
	}
	for _, p := range []string{"index.html", "login.html", "stack.html", "run.html", "settings.html"} {
		s.pages[p] = template.Must(template.New("").Funcs(funcs).ParseFS(templates, "templates/layout.html", "templates/"+p))
	}
	s.mux.HandleFunc("GET /login", func(w http.ResponseWriter, r *http.Request) { s.render(w, http.StatusOK, "login.html", nil) })
	s.mux.HandleFunc("POST /login", s.login)
	s.mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		s.render(w, http.StatusOK, "index.html", s.deps.Current())
	})
	s.mux.HandleFunc("GET /stacks/{name}", s.stack)
	s.mux.HandleFunc("POST /stacks/{name}/apply", s.apply(false))
	s.mux.HandleFunc("POST /stacks/{name}/dry-run", s.apply(true))
	s.mux.HandleFunc("POST /stacks/{name}/ignore", s.decide("ignore"))
	s.mux.HandleFunc("POST /stacks/{name}/postpone", s.decide("postpone"))
	s.mux.HandleFunc("GET /runs/{id}", s.run)
	s.mux.HandleFunc("GET /settings", func(w http.ResponseWriter, r *http.Request) {
		s.render(w, http.StatusOK, "settings.html", s.deps.Config)
	})
	return s
}

func (s *server) find(name string) (*inventory.Stack, time.Time) {
	snap := s.deps.Current()
	for _, st := range snap.Stacks {
		if st.Name == name {
			return st, snap.At
		}
	}
	return nil, snap.At
}

func (s *server) stack(w http.ResponseWriter, r *http.Request) {
	st, at := s.find(r.PathValue("name"))
	if st == nil {
		http.NotFound(w, r)
		return
	}
	data := map[string]any{"Stack": st, "At": at, "Error": r.URL.Query().Get("erreur")}
	if rec, err := s.deps.Plan(st.Name); err != nil {
		data["Error"] = err.Error()
	} else {
		data["Record"] = rec
		if rec != nil {
			data["Auto"], data["AutoWhy"] = plan.Automatic(rec.Plan, s.deps.Config.Auto)
		}
	}
	if until := s.deps.Postponed(st.Name); time.Now().Before(until) {
		data["Postponed"] = until.Format("2006-01-02")
	}
	data["Runs"], _ = s.deps.Runs(st.Name)
	s.render(w, http.StatusOK, "stack.html", data)
}

func back(w http.ResponseWriter, r *http.Request, err error) {
	target := "/stacks/" + url.PathEscape(r.PathValue("name"))
	if err != nil {
		target += "?erreur=" + url.QueryEscape(err.Error())
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (s *server) apply(dryRun bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := s.deps.Apply(r.PathValue("name"), dryRun)
		if err != nil {
			back(w, r, err)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/runs/%d", id), http.StatusSeeOther)
	}
}

func (s *server) decide(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		service, version, until := r.FormValue("service"), r.FormValue("version"), r.FormValue("until")
		var err error
		switch {
		case kind == "ignore" && (service == "" || version == ""):
			err = errors.New("service et version à ignorer manquants")
		case kind == "postpone":
			if _, e := time.Parse("2006-01-02", until); e != nil {
				err = errors.New("date de report invalide")
			}
		}
		if err == nil {
			err = s.deps.Decide(r.PathValue("name"), kind, service, version, until)
		}
		back(w, r, err)
	}
}

func (s *server) run(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	v, err := s.deps.Run(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.render(w, http.StatusOK, "run.html", v)
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
