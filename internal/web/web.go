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

	"github.com/kolapsis/palier/internal/catalog"
	"github.com/kolapsis/palier/internal/config"
	"github.com/kolapsis/palier/internal/inventory"
	"github.com/kolapsis/palier/internal/plan"
	"github.com/kolapsis/palier/internal/run"
	"github.com/kolapsis/palier/internal/version"
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
	Automatic func(stack string, p *plan.Plan) (bool, string)
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
	run.Running:    "running",
	run.Failed:     "failed",
	run.RolledBack: "rolled back",
	run.Abandoned:  "abandoned",
}

// plan returns the stored plan of a stack, or nil when a run finished after it was computed and it no longer describes the stack.
func (s *server) plan(stack string) (*plan.Record, error) {
	rec, err := s.deps.Plan(stack)
	if err != nil || rec == nil {
		return rec, err
	}
	if runs, _ := s.deps.Runs(stack); len(runs) > 0 && !runs[0].DryRun {
		if done, err := time.Parse(time.RFC3339, runs[0].Finished); err == nil && done.After(rec.Created) {
			return nil, nil
		}
	}
	return rec, nil
}

func (s *server) automatic(stack string, p *plan.Plan) (bool, string) {
	if s.deps.Automatic != nil {
		return s.deps.Automatic(stack, p)
	}
	return plan.Automatic(p, s.deps.Config.Auto)
}

func (s *server) state(st *inventory.Stack) string {
	if st.Observed {
		return "observed only"
	}
	if runs, _ := s.deps.Runs(st.Name); len(runs) > 0 && !runs[0].DryRun {
		if name, ok := stateNames[runs[0].Status]; ok {
			return name
		}
	}
	rec, _ := s.plan(st.Name)
	switch {
	case rec != nil && len(rec.Plan.Incomplete) > 0:
		return "incomplete plan"
	case rec != nil && len(rec.Plan.Steps) > 0:
		if ok, _ := s.automatic(st.Name, rec.Plan); ok {
			return "plan ready"
		}
		return "awaiting decision"
	}
	for _, svc := range st.Services {
		if svc.DetectionError != "" {
			return "detection incomplete"
		}
	}
	for _, svc := range st.Services {
		if len(svc.Available) > 0 && rec == nil {
			return "computing plan"
		}
	}
	return "up to date"
}

func since(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t).Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d s ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	}
	return fmt.Sprintf("%d h ago", int(d.Hours()))
}

var labels = map[string]string{
	"done": "completed", "rolled_back": "rolled back",
	"ai": "AI", "versions": "versions only",
}

var tones = map[string]string{
	"up to date": "ok", "done": "ok", "safe": "ok", "yes": "ok",
	"plan ready": "accent", "running": "run", "computing plan": "run",
	"awaiting decision": "warn", "detection incomplete": "warn", "rolled back": "warn", "rolled_back": "warn", "attention": "warn", "conditional": "warn",
	"incomplete plan": "bad", "failed": "bad", "blocking": "bad", "no": "bad",
}

// Crumb is one version on the path of a service, from the running version to its target.
type Crumb struct {
	Tag  string
	Stop bool
	Last bool
}

// crumbs returns the versions a service goes through: the running one, each target of the plan, the mandatory stops of its card marked.
func crumbs(svc *inventory.Service, rec *plan.Record) []Crumb {
	path := []Crumb{{Tag: svc.Version}}
	if svc.Version == "" {
		path[0].Tag = svc.Tag
	}
	if rec != nil {
		for _, st := range rec.Plan.Steps {
			for _, c := range st.Changes {
				if c.Service == svc.Name {
					path = append(path, Crumb{Tag: c.To, Stop: isStop(svc.Image, c.To)})
				}
			}
		}
	}
	if len(path) == 1 && len(svc.Available) > 0 && rec == nil {
		for _, tag := range stopsBetween(svc) {
			path = append(path, Crumb{Tag: tag, Stop: true})
		}
		path = append(path, Crumb{Tag: svc.Available[len(svc.Available)-1].Tag})
	}
	if len(path) > 1 {
		path[len(path)-1].Last = true
	}
	return path
}

func hasStop(st *inventory.Stack, rec *plan.Record) bool {
	for _, svc := range st.Services {
		for _, c := range crumbs(svc, rec) {
			if c.Stop {
				return true
			}
		}
	}
	return false
}

// stopsBetween returns, for each mandatory stop of the card above the running version, the highest available tag of its minor.
func stopsBetween(svc *inventory.Service) []string {
	card := catalog.For(svc.Image)
	from, ok := version.Parse(svc.Version)
	if card == nil || !ok || len(from.Nums) < 2 {
		return nil
	}
	last := svc.Available[len(svc.Available)-1].Tag
	var out []string
	for _, stop := range card.Stops {
		s, ok := version.Parse(stop)
		if !ok || len(s.Nums) < 2 || !minorAfter(s.Nums, from.Nums) {
			continue
		}
		highest := ""
		for _, r := range svc.Available {
			if v, ok := version.Parse(r.Tag); ok && len(v.Nums) >= 2 && v.Nums[0] == s.Nums[0] && v.Nums[1] == s.Nums[1] {
				highest = r.Tag
			}
		}
		if highest != "" && highest != last {
			out = append(out, highest)
		}
	}
	return out
}

func minorAfter(a, b []int) bool {
	return a[0] > b[0] || a[0] == b[0] && a[1] > b[1]
}

func isStop(image, tag string) bool {
	card := catalog.For(image)
	v, ok := version.Parse(tag)
	if card == nil || !ok || len(v.Nums) < 2 {
		return false
	}
	for _, stop := range card.Stops {
		if s, ok := version.Parse(stop); ok && len(s.Nums) >= 2 && s.Nums[0] == v.Nums[0] && s.Nums[1] == v.Nums[1] {
			return true
		}
	}
	return false
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
		"join":    func(v []string) string { return strings.Join(v, ", ") },
		"tone":    func(v string) string { return tones[v] },
		"crumbs":  crumbs,
		"hasStop": hasStop,
		"inc":     func(i int) int { return i + 1 },
		"when": func(v string) string {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				return v
			}
			return t.Format("Jan 2, 2006 3:04:05 PM") + " UTC"
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
	data := map[string]any{"Stack": st, "At": at, "Error": r.URL.Query().Get("error")}
	if rec, err := s.plan(st.Name); err != nil {
		data["Error"] = err.Error()
	} else {
		data["Record"] = rec
		if rec != nil {
			data["Auto"], data["AutoWhy"] = s.automatic(st.Name, rec.Plan)
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
		target += "?error=" + url.QueryEscape(err.Error())
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
			err = errors.New("service and version to ignore are missing")
		case kind == "postpone":
			if _, e := time.Parse("2006-01-02", until); e != nil {
				err = errors.New("invalid postpone date")
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
		s.render(w, http.StatusUnauthorized, "login.html", map[string]string{"Error": "Incorrect token."})
		return
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: token, Path: "/", MaxAge: 30 * 24 * 3600, HttpOnly: true, Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https", SameSite: http.SameSiteStrictMode})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *server) render(w http.ResponseWriter, status int, page string, data any) {
	var buf bytes.Buffer
	if err := s.pages[page].ExecuteTemplate(&buf, "layout", data); err != nil {
		slog.Error("page render", "page", page, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	buf.WriteTo(w)
}
