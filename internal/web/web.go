// Package web serves Palier's interface.
package web

import (
	"bytes"
	"cmp"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kolapsis/palier/internal/catalog"
	"github.com/kolapsis/palier/internal/config"
	"github.com/kolapsis/palier/internal/inventory"
	"github.com/kolapsis/palier/internal/notes"
	"github.com/kolapsis/palier/internal/notify"
	"github.com/kolapsis/palier/internal/plan"
	"github.com/kolapsis/palier/internal/run"
	"github.com/kolapsis/palier/internal/version"
)

//go:embed templates
var templates embed.FS

// Snapshot is the result of the last inspection.
type Snapshot struct {
	Stacks []*inventory.Stack
	Began  time.Time
	At     time.Time
	Err    string
}

// Deps is what the interface reads and asks of the rest of Palier.
type Deps struct {
	Current   func() Snapshot
	Plan      func(stack string) (*plan.Record, error)
	Apply     func(stack, author string, dryRun bool) (int64, error)
	Decide    func(stack, author, kind, service, version, until string) error
	Postponed func(stack string) time.Time
	Automatic func(stack string, p *plan.Plan) (bool, string)
	Run       func(id int64) (*run.View, error)
	Runs      func(stack string) ([]run.View, error)
	Rescan    func()

	Incidents    func() []notify.Incident
	Channels     func() []notify.Channel
	NotifyTest   func() error
	NotifyReport func() (bool, error)

	Config config.Config
}

type server struct {
	author string
	deps   Deps
	mux    *http.ServeMux
	pages  map[string]*template.Template
}

var stateNames = map[string]string{
	run.Running:    "running",
	run.Failed:     "failed",
	run.RolledBack: "rolled back",
	run.Abandoned:  "abandoned",
	run.Stopped:    "stopped",
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
	if s.deps.Current().At.IsZero() {
		return "checking versions"
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

// Since says how long ago t was, in words.
func Since(t time.Time) string {
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

func severityRank(s string) int {
	if i := slices.Index([]string{"critical", "high", "medium", "low"}, s); i >= 0 {
		return i
	}
	return 4
}

// WorstFirst returns the advisories sorted from the most to the least severe.
func WorstFirst(list []notes.Advisory) []notes.Advisory {
	out := slices.Clone(list)
	slices.SortStableFunc(out, func(a, b notes.Advisory) int { return cmp.Compare(severityRank(a.Severity), severityRank(b.Severity)) })
	return out
}

// Severities sums up advisories by severity, such as "3 security advisories: 1 critical, 2 high".
func Severities(list []notes.Advisory) string {
	counts := map[string]int{}
	for _, a := range list {
		counts[cmp.Or(a.Severity, "unknown")]++
	}
	var parts []string
	for _, a := range WorstFirst(list) {
		s := cmp.Or(a.Severity, "unknown")
		if counts[s] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[s], s))
			counts[s] = 0
		}
	}
	noun := "security advisories"
	if len(list) == 1 {
		noun = "security advisory"
	}
	return fmt.Sprintf("%d %s: %s", len(list), noun, strings.Join(parts, ", "))
}

var labels = map[string]string{
	"done": "completed", "rolled_back": "rolled back",
	"ai": "AI", "versions": "versions only",
}

// Tone returns the color family of a state: ok, accent, run, warn, bad, or "".
func Tone(state string) string { return tones[state] }

var tones = map[string]string{
	"up to date": "ok", "done": "ok", "safe": "ok", "yes": "ok",
	"plan ready": "accent", "running": "run", "computing plan": "run", "checking versions": "run",
	"awaiting decision": "warn", "detection incomplete": "warn", "rolled back": "warn", "stopped": "warn", "rolled_back": "warn", "attention": "warn", "conditional": "warn",
	"incomplete plan": "bad", "failed": "bad", "blocking": "bad", "no": "bad",
}

// Crumb is one version on the path of a service, from the running version to its target.
type Crumb struct {
	Tag  string `json:"tag"`
	Stop bool   `json:"stop,omitempty"`
	Last bool   `json:"last,omitempty"`
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

// New returns the handler of the interface, behind OIDC sign-in when it is configured, refusing changes that come from another site.
func New(deps Deps) http.Handler {
	s := &server{author: "interface", deps: deps, mux: http.NewServeMux(), pages: map[string]*template.Template{}}
	funcs := template.FuncMap{
		"state":   s.state,
		"since":   Since,
		"signout": func() bool { return deps.Config.OIDCIssuer != "" },
		"label": func(v string) string {
			if l, ok := labels[v]; ok {
				return l
			}
			return v
		},
		"join":       func(v []string) string { return strings.Join(v, ", ") },
		"tone":       func(v string) string { return tones[v] },
		"crumbs":     crumbs,
		"hasStop":    hasStop,
		"inc":        func(i int) int { return i + 1 },
		"worstFirst": WorstFirst,
		"severities": Severities,
		"when": func(v string) string {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				return v
			}
			return t.Format("Jan 2, 2006 3:04:05 PM") + " UTC"
		},
	}
	for _, p := range []string{"index.html", "stack.html", "run.html", "settings.html", "gate.html"} {
		s.pages[p] = template.Must(template.New("").Funcs(funcs).ParseFS(templates, "templates/layout.html", "templates/"+p))
	}
	s.mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		s.render(w, http.StatusOK, "index.html", struct {
			Snapshot
			Incidents []notify.Incident
		}{s.deps.Current(), s.incidents()})
	})
	s.mux.HandleFunc("GET /stacks/{name}", s.stack)
	s.mux.HandleFunc("POST /stacks/{name}/apply", s.apply(false))
	s.mux.HandleFunc("POST /stacks/{name}/dry-run", s.apply(true))
	s.mux.HandleFunc("POST /stacks/{name}/ignore", s.decide("ignore"))
	s.mux.HandleFunc("POST /stacks/{name}/postpone", s.decide("postpone"))
	s.mux.HandleFunc("GET /runs/{id}", s.run)
	s.mux.HandleFunc("GET /settings", func(w http.ResponseWriter, r *http.Request) {
		s.render(w, http.StatusOK, "settings.html", struct {
			config.Config
			Channels []notify.Channel
			Tested   string
			Error    string
		}{s.deps.Config, s.channels(), r.URL.Query().Get("tested"), r.URL.Query().Get("error")})
	})
	s.mux.HandleFunc("POST /settings/test", func(w http.ResponseWriter, r *http.Request) {
		target := "/settings?tested=1"
		if err := s.test(); err != nil {
			target = "/settings?error=" + url.QueryEscape(err.Error())
		}
		http.Redirect(w, r, target, http.StatusSeeOther)
	})
	var h http.Handler = s.mux
	if deps.Config.OIDCIssuer != "" {
		h = newGate(deps.Config, func(w http.ResponseWriter, status int, title, message string) {
			s.render(w, status, "gate.html", map[string]string{"Title": title, "Message": message})
		}).wrap(h)
	}
	return http.NewCrossOriginProtection().Handler(h)
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
	v := s.view(st, at)
	if e := r.URL.Query().Get("error"); e != "" {
		v.Error = e
	}
	s.render(w, http.StatusOK, "stack.html", v)
}

// StackView is everything known about one stack: its services, their paths, its plan and its runs.
type StackView struct {
	Stack     *inventory.Stack   `json:"stack"`
	At        time.Time          `json:"at"`
	State     string             `json:"state"`
	Paths     map[string][]Crumb `json:"paths"`
	Record    *plan.Record       `json:"record"`
	Auto      bool               `json:"auto"`
	AutoWhy   string             `json:"auto_why,omitempty"`
	Postponed string             `json:"postponed,omitempty"`
	Runs      []run.View         `json:"runs"`
	Error     string             `json:"error,omitempty"`
}

func (s *server) view(st *inventory.Stack, at time.Time) StackView {
	v := StackView{Stack: st, At: at, State: s.state(st), Paths: map[string][]Crumb{}}
	if rec, err := s.plan(st.Name); err != nil {
		v.Error = err.Error()
	} else if v.Record = rec; rec != nil {
		v.Auto, v.AutoWhy = s.automatic(st.Name, rec.Plan)
	}
	for _, svc := range st.Services {
		v.Paths[svc.Name] = crumbs(svc, v.Record)
	}
	if until := s.deps.Postponed(st.Name); time.Now().Before(until) {
		v.Postponed = until.Format("2006-01-02")
	}
	v.Runs, _ = s.deps.Runs(st.Name)
	return v
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
		id, err := s.deps.Apply(r.PathValue("name"), s.who(r), dryRun)
		if err != nil {
			back(w, r, err)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/runs/%d", id), http.StatusSeeOther)
	}
}

func (s *server) decide(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		back(w, r, s.record(s.who(r), r.PathValue("name"), kind, r.FormValue("service"), r.FormValue("version"), r.FormValue("until")))
	}
}

// who is the author of a decision: the person signed in, or the interface itself.
func (s *server) who(r *http.Request) string { return cmp.Or(who(r), s.author) }

func (s *server) record(author, stack, kind, service, version, until string) error {
	switch {
	case kind == "ignore" && (service == "" || version == ""):
		return errors.New("service and version to ignore are missing")
	case kind == "postpone":
		if _, err := time.Parse("2006-01-02", until); err != nil {
			return errors.New("invalid postpone date, expected YYYY-MM-DD")
		}
	}
	return s.deps.Decide(stack, author, kind, service, version, until)
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
