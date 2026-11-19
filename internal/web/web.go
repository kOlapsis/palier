// Copyright 2026 Benjamin Touchard (Kolapsis)
// SPDX-License-Identifier: Apache-2.0

// Package web serves Palier's interface.
package web

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

//go:embed static
var staticFiles embed.FS

var assetVersion = sync.OnceValue(func() string {
	h := sha256.New()
	fs.WalkDir(staticFiles, "static", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		body, _ := staticFiles.ReadFile(name)
		h.Write([]byte(name))
		h.Write(body)
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))[:8]
})

func staticHandler() http.Handler {
	sub, _ := fs.Sub(staticFiles, "static")
	files := http.StripPrefix("/static", http.FileServerFS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if info, err := fs.Stat(sub, strings.TrimPrefix(r.URL.Path, "/static/")); err != nil || info.IsDir() {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		files.ServeHTTP(w, r)
	})
}

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
	Backups   func(stack string) ([]run.Archive, error)
	Rescan    func(stack string)
	Scanning  func() bool
	Analyzing func(stack string) bool

	Incidents    func() []notify.Incident
	Channels     func() []notify.Channel
	NotifyTest   func() error
	NotifyReport func() (bool, error)

	Config config.Config
}

type server struct {
	asked  atomic.Int64
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
	rec, _ := s.plan(st.Name)
	if runs, _ := s.deps.Runs(st.Name); len(runs) > 0 && !runs[0].DryRun {
		pending := rec != nil && len(rec.Plan.Steps) > 0
		switch status := runs[0].Status; {
		case status == run.Running, status == run.Failed && (pending || !st.Healthy()):
			return stateNames[status]
		case s.deps.Analyzing != nil && s.deps.Analyzing(st.Name):
		case pending && stateNames[status] != "":
			return stateNames[status]
		}
	}
	if s.deps.Analyzing != nil && s.deps.Analyzing(st.Name) {
		return "analyzing with AI"
	}
	if s.deps.Current().At.IsZero() {
		return "checking versions"
	}
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

// Problems is what the open incidents give the stack list: the first alerts, how many others, and two incidents per stack.
type Problems struct {
	Alerts    []notify.Incident
	More      int
	Others    int
	ByStack   map[string][]notify.Incident
	MoreStack map[string]int
}

// Summarize groups the open incidents for a list of stacks that stays readable however many there are.
func Summarize(list []notify.Incident) Problems {
	p := Problems{ByStack: map[string][]notify.Incident{}, MoreStack: map[string]int{}}
	for _, i := range list {
		if i.Level == notify.Alert {
			p.Alerts = append(p.Alerts, i)
		} else {
			p.Others++
		}
		if i.Stack != "" {
			i.Title = strings.TrimPrefix(i.Title, i.Stack+": ")
			p.ByStack[i.Stack] = append(p.ByStack[i.Stack], i)
		}
	}
	p.Alerts, p.More = shorten(p.Alerts, 3)
	for st, l := range p.ByStack {
		p.ByStack[st], p.MoreStack[st] = shorten(l, 2)
	}
	return p
}

// shorten keeps the first n incidents and counts the others, but shows a single other one rather than "and 1 more".
func shorten(l []notify.Incident, n int) ([]notify.Incident, int) {
	if len(l) <= n+1 {
		return l, 0
	}
	return l[:n], len(l) - n
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

// Elapsed says how long a run lasted, such as "47 s", "12 min" or "1 h 27 min": until now when finished is empty.
func Elapsed(started, finished string) string { return elapsedAt(started, finished, time.Now()) }

func elapsedAt(started, finished string, now time.Time) string {
	from, err := time.Parse(time.RFC3339, started)
	if err != nil {
		return ""
	}
	to := now
	if finished != "" {
		if to, err = time.Parse(time.RFC3339, finished); err != nil {
			return ""
		}
	}
	return span(to.Sub(from))
}

func span(d time.Duration) string {
	d = max(d, 0)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d s", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	case int(d.Minutes())%60 == 0:
		return fmt.Sprintf("%d h", int(d.Hours()))
	}
	return fmt.Sprintf("%d h %d min", int(d.Hours()), int(d.Minutes())%60)
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
	"plan ready": "accent", "running": "run", "computing plan": "run", "checking versions": "run", "analyzing with AI": "run",
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
		"state":    s.state,
		"since":    Since,
		"signout":  func() bool { return deps.Config.OIDCIssuer != "" },
		"scanning": func() bool { return s.scanState() != "" },
		"queued":   func() bool { return s.scanState() == "queued" },
		"scanned":  func() time.Time { return deps.Current().At },
		"asset":    func(name string) string { return "/static/" + name + "?v=" + assetVersion() },
		"plural":   plural,
		"line":     s.line,
		"config":   func() config.Config { return deps.Config },
		"clock": func(v string) string {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				return v
			}
			return t.UTC().Format("15:04:05")
		},
		"elapsed": Elapsed,
		"label": func(v string) string {
			if l, ok := labels[v]; ok {
				return l
			}
			return v
		},
		"join":       func(v []string) string { return strings.Join(v, ", ") },
		"upper":      strings.ToUpper,
		"tone":       func(v string) string { return tones[v] },
		"crumbs":     crumbs,
		"hasStop":    hasStop,
		"inc":        func(i int) int { return i + 1 },
		"sub":        func(a, b int) int { return a - b },
		"firstLine":  func(v string) string { s, _, _ := strings.Cut(v, "\n"); return s },
		"span":       span,
		"base":       path.Base,
		"capital":    func(v string) string { return strings.ToUpper(v[:min(1, len(v))]) + v[min(1, len(v)):] },
		"size":       Size,
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
		section := ""
		pageFuncs := maps.Clone(funcs)
		pageFuncs["section"] = func() string { return section }
		t := template.Must(template.New("").Funcs(pageFuncs).ParseFS(templates, "templates/layout.html", "templates/"+p))
		var name strings.Builder
		template.Must(t, t.ExecuteTemplate(&name, "section", nil))
		section = name.String()
		s.pages[p] = t
	}
	s.mux.HandleFunc("GET /{$}", s.overview)
	s.mux.HandleFunc("POST /scan", s.scan)
	s.mux.HandleFunc("POST /stacks/{name}/scan", s.scan)
	s.mux.HandleFunc("GET /stacks/{name}", s.stack)
	s.mux.HandleFunc("POST /stacks/{name}/apply", s.apply(false))
	s.mux.HandleFunc("POST /stacks/{name}/dry-run", s.apply(true))
	s.mux.HandleFunc("POST /stacks/{name}/ignore", s.decide("ignore"))
	s.mux.HandleFunc("POST /stacks/{name}/accept", s.decide("accept"))
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
	protected := http.NewCrossOriginProtection().Handler(h)
	hook := registryHook(deps)
	assets := staticHandler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/hooks/registry/"):
			hook.ServeHTTP(w, r)
		case strings.HasPrefix(r.URL.Path, "/static/"):
			assets.ServeHTTP(w, r)
		default:
			protected.ServeHTTP(w, r)
		}
	})
}

// Overview is the list of stacks sorted by what they ask of the person.
type Overview struct {
	Snapshot
	Problems
	Query    string
	Needs    []StackView
	Updating []StackView
	Planned  []StackView
	Checking []StackView
	Partial  []StackView
	Quiet    []StackView
}

// group sorts stacks by what they ask of the person; q, in lower case, keeps only the matching stacks among the partly tracked and the quiet ones.
func group(views []StackView, alerting map[string]bool, q string) (o Overview) {
	for _, v := range views {
		switch {
		case v.State == "running":
			o.Updating = append(o.Updating, v)
		case alerting[v.Stack.Name]:
			o.Needs = append(o.Needs, v)
		case v.State == "awaiting decision" && v.Postponed != "":
			o.Planned = append(o.Planned, v)
		case slices.Contains([]string{"failed", "rolled back", "stopped", "abandoned", "incomplete plan", "awaiting decision"}, v.State):
			o.Needs = append(o.Needs, v)
		case v.State == "plan ready":
			o.Planned = append(o.Planned, v)
		case v.State == "computing plan" || v.State == "checking versions" || v.State == "analyzing with AI":
			o.Checking = append(o.Checking, v)
		case !matches(v.Stack, q):
		case v.State == "detection incomplete":
			o.Partial = append(o.Partial, v)
		default:
			o.Quiet = append(o.Quiet, v)
		}
	}
	return o
}

func matches(st *inventory.Stack, q string) bool {
	if strings.Contains(strings.ToLower(st.Name), q) {
		return true
	}
	return slices.ContainsFunc(st.Services, func(svc *inventory.Service) bool { return strings.Contains(strings.ToLower(svc.Name), q) })
}

func (s *server) overview(w http.ResponseWriter, r *http.Request) {
	snap := s.deps.Current()
	incidents := s.incidents()
	alerting := map[string]bool{}
	var global []notify.Incident
	for _, i := range incidents {
		switch {
		case i.Level != notify.Alert:
		case i.Stack == "":
			global = append(global, i)
		default:
			alerting[i.Stack] = true
		}
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	views := make([]StackView, 0, len(snap.Stacks))
	for _, st := range snap.Stacks {
		views = append(views, s.view(st, snap.At))
	}
	o := group(views, alerting, strings.ToLower(query))
	o.Snapshot, o.Problems, o.Query = snap, Summarize(incidents), query
	o.Alerts, o.More = shorten(global, 3)
	s.render(w, http.StatusOK, "index.html", o)
}

// scanState says whether a scan is "running", "queued" behind what Palier is doing since it was asked for, or "".
func (s *server) scanState() string {
	if s.deps.Scanning != nil && s.deps.Scanning() {
		return "running"
	}
	if asked, began := s.asked.Load(), s.deps.Current().Began; asked != 0 && (began.IsZero() || asked > began.UnixNano()) {
		return "queued"
	}
	return ""
}

func (s *server) scan(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if st, _ := s.find(name); name != "" && st == nil {
		http.NotFound(w, r)
		return
	}
	s.asked.Store(time.Now().UnixNano())
	if s.deps.Rescan != nil {
		s.deps.Rescan(name)
	}
	if name != "" {
		back(w, r, nil)
		return
	}
	http.Redirect(w, r, refererPath(r), http.StatusSeeOther)
}

// registryHook asks for a scan when a registry announces a new tag, at most once a minute and never while one runs; it reads nothing from the request but its secret.
func registryHook(deps Deps) http.Handler {
	var mu sync.Mutex
	var last time.Time
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secret := strings.TrimPrefix(r.URL.Path, "/hooks/registry/")
		if deps.Config.HookSecret == "" || r.Method != http.MethodPost || subtle.ConstantTimeCompare([]byte(secret), []byte(deps.Config.HookSecret)) != 1 {
			http.NotFound(w, r)
			return
		}
		io.Copy(io.Discard, io.LimitReader(r.Body, 64<<10))
		mu.Lock()
		due := time.Since(last) >= time.Minute && (deps.Scanning == nil || !deps.Scanning())
		if due {
			last = time.Now()
		}
		mu.Unlock()
		if due && deps.Rescan != nil {
			slog.Info("registry webhook: scan requested", "from", r.RemoteAddr)
			deps.Rescan("")
		}
		w.WriteHeader(http.StatusAccepted)
	})
}

// Size writes a number of bytes in KiB, MiB or GiB.
func Size(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}

func (s *server) find(name string) (*inventory.Stack, time.Time) {
	snap := s.deps.Current()
	for _, st := range snap.Stacks {
		if st.Name == name {
			return st, st.Scanned
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
	if len(v.Runs) > 0 && v.Runs[0].Status == run.Running && s.deps.Run != nil {
		if current, err := s.deps.Run(v.Runs[0].ID); err == nil && current != nil && len(current.Events) > 0 {
			v.Now = current.Events[len(current.Events)-1].Message
		}
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
	Backups   []run.Archive      `json:"backups"`
	Error     string             `json:"error,omitempty"`
	Now       string             `json:"now,omitempty"`
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
	if s.deps.Backups != nil {
		v.Backups, _ = s.deps.Backups(st.Name)
	}
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
	case (kind == "ignore" || kind == "accept") && (service == "" || version == ""):
		return errors.New("service and version to " + kind + " are missing")
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
