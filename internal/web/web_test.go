package web

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kolapsis/palier/internal/config"
	"github.com/kolapsis/palier/internal/inventory"
	"github.com/kolapsis/palier/internal/notes"
	"github.com/kolapsis/palier/internal/notify"
	"github.com/kolapsis/palier/internal/plan"
	"github.com/kolapsis/palier/internal/run"
)

func TestCrossSiteChangesRefused(t *testing.T) {
	applied := 0
	h := New(Deps{
		Current: func() Snapshot { return Snapshot{} },
		Runs:    func(string) ([]run.View, error) { return nil, nil },
		Plan:    func(string) (*plan.Record, error) { return nil, nil },
		Apply:   func(string, string, bool) (int64, error) { applied++; return 1, nil },
	})
	do := func(method, path, site string) int {
		r := httptest.NewRequest(method, path, nil)
		if site != "" {
			r.Header.Set("Sec-Fetch-Site", site)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if code := do("GET", "/", ""); code != http.StatusOK {
		t.Fatalf("GET /: %d", code)
	}
	if code := do("POST", "/stacks/whoami/apply", "cross-site"); code != http.StatusForbidden || applied != 0 {
		t.Fatalf("cross-site apply: %d, applied %d", code, applied)
	}
	if code := do("POST", "/stacks/whoami/apply", "same-origin"); code != http.StatusSeeOther || applied != 1 {
		t.Fatalf("same-origin apply: %d, applied %d", code, applied)
	}
}

func TestPagesRender(t *testing.T) {
	st := &inventory.Stack{Name: "whoami", Services: []*inventory.Service{{Name: "whoami", Role: "application", Image: "docker.io/traefik/whoami", Tag: "v1.10.1", Version: "v1.10.1", Available: []inventory.Release{{Tag: "v1.12.0"}}}}}
	p := &plan.Plan{Steps: []plan.Step{{Risk: "safe", Reversible: "yes", Why: "A sentence.",
		Changes:    []plan.Change{{Service: "whoami", From: "v1.10.1", To: "v1.12.0"}},
		Operations: []plan.Operation{{Op: "set_image", Service: "whoami", Image: "traefik/whoami", Tag: "v1.12.0"}, {Op: "recreate", Services: []string{"whoami"}}},
		Gate:       &plan.Operation{Op: "exec_check", Service: "whoami", CheckRef: "background_migrations"},
		Facts:      []plan.Fact{{Category: "security_fix", Service: "whoami", Version: "v1.12.0", Quote: "<script>alert(1)</script> a quote", SourceURL: "https://github.com/traefik/whoami/releases"}}},
		{Risk: "attention", Reversible: "yes", Operations: []plan.Operation{{Op: "compose_edit", Service: "whoami", Edits: []plan.Edit{{Action: "rename_env", Name: "SERVER_PORT", NewName: "IMMICH_PORT"}}}}}}}
	h := New(Deps{
		Current:   func() Snapshot { return Snapshot{Stacks: []*inventory.Stack{st}} },
		Runs:      func(string) ([]run.View, error) { return []run.View{{ID: 1, Status: "rolled_back"}}, nil },
		Plan:      func(string) (*plan.Record, error) { return &plan.Record{Stack: "whoami", Source: "ai", Plan: p}, nil },
		Postponed: func(string) time.Time { return time.Time{} },
		Run: func(int64) (*run.View, error) {
			return &run.View{ID: 1, Stack: "whoami", Status: "running", Steps: []string{"gitlab 16.1.6 to 16.3.9", "gitlab 16.3.9 to 16.7.10"}, StepsDone: 1, Events: []run.Event{{Message: "step 1"}}}, nil
		},
	})
	for path, want := range map[string]string{"/runs/1?progress": `<li class="done" style="--i:1"></li><li class="current" style="--i:2"></li>`, "/": "rolled back", "/stacks/whoami": "rename_env SERVER_PORT → IMMICH_PORT", "/runs/1": "step 1", "/settings": "PALIER_AUTO"} {
		r := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), want) {
			t.Errorf("%s: %d, %q missing", path, w.Code, want)
		}
		if strings.Contains(w.Body.String(), "<script>alert") {
			t.Errorf("%s: quote not escaped", path)
		}
	}
}

func TestCrumbsShowMandatoryStops(t *testing.T) {
	svc := &inventory.Service{Name: "gitlab", Image: "docker.io/gitlab/gitlab-ce", Tag: "16.1.6-ce.0", Version: "16.1.6-ce.0",
		Available: []inventory.Release{{Tag: "16.2.9-ce.0"}, {Tag: "16.3.9-ce.0"}, {Tag: "16.6.8-ce.0"}, {Tag: "16.7.10-ce.0"}, {Tag: "17.0.8-ce.0"}}}
	want := []Crumb{{Tag: "16.1.6-ce.0"}, {Tag: "16.3.9-ce.0", Stop: true}, {Tag: "16.7.10-ce.0", Stop: true}, {Tag: "17.0.8-ce.0", Last: true}}
	if got := crumbs(svc, nil); !slices.Equal(got, want) {
		t.Fatalf("without plan: %v", got)
	}
	rec := &plan.Record{Plan: &plan.Plan{Steps: []plan.Step{{Changes: []plan.Change{{Service: "gitlab", To: "16.3.9-ce.0"}}}, {Changes: []plan.Change{{Service: "gitlab", To: "16.6.8-ce.0"}}}}}}
	want = []Crumb{{Tag: "16.1.6-ce.0"}, {Tag: "16.3.9-ce.0", Stop: true}, {Tag: "16.6.8-ce.0", Last: true}}
	if got := crumbs(svc, rec); !slices.Equal(got, want) {
		t.Fatalf("with plan: %v", got)
	}
}

func TestPlanOlderThanLastRunIsHidden(t *testing.T) {
	created := time.Date(2026, 11, 1, 22, 50, 0, 0, time.UTC)
	rec := &plan.Record{Stack: "immich", Created: created, Plan: &plan.Plan{Steps: []plan.Step{{Changes: []plan.Change{{Service: "database", From: "14", To: "16"}}}}}}
	runs := []run.View{{ID: 16, Status: "done", Finished: "2026-11-01T22:56:39Z"}}
	s := &server{deps: Deps{Plan: func(string) (*plan.Record, error) { return rec, nil }, Runs: func(string) ([]run.View, error) { return runs, nil }}}
	if got, _ := s.plan("immich"); got != nil {
		t.Fatal("a plan computed before the last run is still shown")
	}
	runs[0].Finished = "2026-11-01T22:40:00Z"
	if got, _ := s.plan("immich"); got != rec {
		t.Fatal("a plan computed after the last run is hidden")
	}
}

func TestSeverities(t *testing.T) {
	list := []notes.Advisory{{ID: "a", Severity: "medium"}, {ID: "b", Severity: "high"}, {ID: "c", Severity: "critical"}, {ID: "d", Severity: "high"}, {ID: "e"}}
	if got, want := Severities(list), "5 security advisories: 1 critical, 2 high, 1 medium, 1 unknown"; got != want {
		t.Errorf("severities = %q, want %q", got, want)
	}
	if got := WorstFirst(list); got[0].ID != "c" || got[1].ID != "b" || got[2].ID != "d" || got[4].ID != "e" {
		t.Errorf("worstFirst order = %v", got)
	}
}

func TestAPIStatus(t *testing.T) {
	gitlab := &inventory.Stack{Name: "gitlab", Services: []*inventory.Service{
		{Name: "gitlab", Role: "application", Image: "docker.io/gitlab/gitlab-ce", Tag: "18.5.3-ce.0", Version: "18.5.3-ce.0", Available: []inventory.Release{{Tag: "18.8.4-ce.0"}},
			Advisories: []notes.Advisory{{Severity: "high"}, {Severity: "critical"}}},
		{Name: "runner", Role: "dependency", Tag: "latest", DetectionError: "401"},
	}}
	p := &plan.Plan{Steps: []plan.Step{{Changes: []plan.Change{{Service: "gitlab", From: "18.5.3-ce.0", To: "18.8.4-ce.0"}}}}}
	h := API(Deps{
		Current: func() Snapshot { return Snapshot{Stacks: []*inventory.Stack{gitlab}, At: time.Now()} },
		Runs: func(string) ([]run.View, error) {
			return []run.View{{ID: 15, Status: "stopped", Error: "stopped after step 1 of 3: background_migrations still failing\nmore"}}, nil
		},
		Plan: func(string) (*plan.Record, error) {
			return &plan.Record{Stack: "gitlab", Plan: p, Created: time.Now().Add(-time.Hour)}, nil
		},
		Postponed: func(string) time.Time { return time.Time{} },
	})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/status", nil))
	want := `"stacks":[{"name":"gitlab","state":"stopped","running":"18.5.3-ce.0","target":"18.8.4-ce.0","note":"versions of runner unknown; 2 security advisories, 1 critical; 1 step; run 15: stopped after step 1 of 3: background_migrations still failing"}]`
	if w.Code != 200 || !strings.Contains(w.Body.String(), want) {
		t.Errorf("%d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/stacks/nope", nil))
	if w.Code != 400 || !strings.Contains(w.Body.String(), `"error":"no stack \"nope\""`) {
		t.Errorf("%d %s", w.Code, w.Body.String())
	}
}

func TestRegistryHook(t *testing.T) {
	secret := strings.Repeat("a1", 16)
	scans, running := 0, true
	deps := Deps{
		Current:  func() Snapshot { return Snapshot{} },
		Runs:     func(string) ([]run.View, error) { return nil, nil },
		Plan:     func(string) (*plan.Record, error) { return nil, nil },
		Rescan:   func(string) { scans++ },
		Scanning: func() bool { return running },
		Config:   config.Config{HookSecret: secret, OIDCIssuer: "https://idp.invalid", OIDCClientID: "palier", OIDCAdmins: []string{"admins"}},
	}
	h := New(deps)
	do := func(method, path string) int {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(`{"push_data":{"tag":"evil"}}`)))
		return w.Code
	}
	if code := do("POST", "/hooks/registry/"+secret); code != 202 || scans != 0 {
		t.Fatalf("while a scan runs: %d with %d scans", code, scans)
	}
	running = false
	for _, c := range []struct {
		method, path string
		code, scans  int
	}{
		{"POST", "/hooks/registry/" + secret, 202, 1},
		{"POST", "/hooks/registry/" + secret, 202, 1},
		{"POST", "/hooks/registry/" + secret[:31], 404, 1},
		{"POST", "/hooks/registry/", 404, 1},
		{"GET", "/hooks/registry/" + secret, 404, 1},
	} {
		if code := do(c.method, c.path); code != c.code || scans != c.scans {
			t.Errorf("%s %s: %d with %d scans, want %d with %d", c.method, c.path, code, scans, c.code, c.scans)
		}
	}
	deps.Config.HookSecret = ""
	if code := (func() int {
		w := httptest.NewRecorder()
		New(deps).ServeHTTP(w, httptest.NewRequest("POST", "/hooks/registry/", nil))
		return w.Code
	})(); code != 404 {
		t.Errorf("without a secret: %d", code)
	}
}

func TestSummarize(t *testing.T) {
	var list []notify.Incident
	for _, st := range []string{"help", "supabase", "a", "b"} {
		list = append(list, notify.Incident{Stack: st, Level: "alert", Title: st + ": 1 critical advisory"})
	}
	for range 3 {
		list = append(list, notify.Incident{Stack: "grafana", Level: "notice", Title: "grafana: versions of loki unknown"})
	}
	list = append(list, notify.Incident{Level: "alert", Title: "Palier cannot read the Docker engine"})
	p := Summarize(list)
	if len(p.Alerts) != 3 || p.More != 2 || p.Others != 3 {
		t.Fatalf("%+v", p)
	}
	if g := p.ByStack["grafana"]; len(g) != 3 || g[0].Title != "versions of loki unknown" || p.MoreStack["grafana"] != 0 {
		t.Errorf("grafana: one more is shown, not counted: %+v, more %d", g, p.MoreStack["grafana"])
	}
	list = append(list, notify.Incident{Stack: "grafana", Level: "notice", Title: "grafana: versions of tempo unknown"})
	if p := Summarize(list); len(p.ByStack["grafana"]) != 2 || p.MoreStack["grafana"] != 2 {
		t.Errorf("grafana with four: %d shown, %d more", len(p.ByStack["grafana"]), p.MoreStack["grafana"])
	}
	if p := Summarize(list[:4]); len(p.Alerts) != 4 || p.More != 0 {
		t.Errorf("four alerts: %d shown, %d more", len(p.Alerts), p.More)
	}
}

func TestMainService(t *testing.T) {
	st := &inventory.Stack{Name: "authentik", Services: []*inventory.Service{
		{Name: "backup", Role: "application", Tag: "local", NotInRegistry: true},
		{Name: "postgresql", Role: "dependency", Version: "16.4"},
		{Name: "server", Role: "application", Image: "ghcr.io/goauthentik/server", Version: "2025.8.3"},
		{Name: "worker", Role: "application", Image: "ghcr.io/goauthentik/server", Version: "2025.8.3"},
	}}
	if got := mainService(st); got.Name != "server" {
		t.Errorf("main service %s", got.Name)
	}
	st.Services = st.Services[:2]
	if got := mainService(st); got.Name != "backup" {
		t.Errorf("with only a backup and a database: %s", got.Name)
	}
}

func TestElapsed(t *testing.T) {
	now := time.Date(2026, 11, 18, 17, 0, 0, 0, time.UTC)
	for _, c := range []struct{ started, finished, want string }{
		{"2026-11-18T16:59:13Z", "", "47 s"},
		{"2026-11-18T16:48:00Z", "", "12 min"},
		{"2026-11-18T15:33:00Z", "", "1 h 27 min"},
		{"2026-11-18T15:00:00Z", "", "2 h"},
		{"2026-11-18T10:32:58Z", "2026-11-18T10:34:18Z", "1 min"},
		{"2026-11-18T10:32:58Z", "2026-11-18T10:33:03Z", "5 s"},
		{"2026-11-18T18:00:00Z", "", "0 s"},
		{"yesterday", "", ""},
		{"2026-11-18T10:32:58Z", "later", ""},
	} {
		if got := elapsedAt(c.started, c.finished, now); got != c.want {
			t.Errorf("elapsed(%q, %q) = %q, want %q", c.started, c.finished, got, c.want)
		}
	}
}

func TestGroup(t *testing.T) {
	stack := func(name string, services ...string) *inventory.Stack {
		st := &inventory.Stack{Name: name}
		for _, s := range services {
			st.Services = append(st.Services, &inventory.Service{Name: s})
		}
		return st
	}
	var views []StackView
	for _, c := range []struct{ name, state, postponed string }{
		{"a-running", "running", ""}, {"b-failed", "failed", ""}, {"c-rolled", "rolled back", ""}, {"d-stopped", "stopped", ""},
		{"e-abandoned", "abandoned", ""}, {"f-incomplete", "incomplete plan", ""}, {"g-awaiting", "awaiting decision", ""},
		{"h-postponed", "awaiting decision", "2026-12-01"}, {"i-ready", "plan ready", ""}, {"j-computing", "computing plan", ""},
		{"k-checking", "checking versions", ""}, {"l-partial", "detection incomplete", ""}, {"m-ok", "up to date", ""},
		{"n-observed", "observed only", ""}, {"o-alert", "up to date", ""}, {"p-alert-running", "running", ""},
	} {
		views = append(views, StackView{Stack: stack(c.name, "web", "db-"+c.name), State: c.state, Postponed: c.postponed})
	}
	names := func(l []StackView) string {
		var out []string
		for _, v := range l {
			out = append(out, v.Stack.Name[:1])
		}
		return strings.Join(out, "")
	}
	o := group(views, map[string]bool{"o-alert": true, "p-alert-running": true}, "")
	for name, got := range map[string]string{"updating": names(o.Updating), "needs": names(o.Needs), "planned": names(o.Planned), "checking": names(o.Checking), "partial": names(o.Partial), "quiet": names(o.Quiet)} {
		want := map[string]string{"updating": "ap", "needs": "bcdefgo", "planned": "hi", "checking": "jk", "partial": "l", "quiet": "mn"}[name]
		if got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	o = group(views, nil, "db-m")
	if names(o.Quiet) != "m" || len(o.Partial) != 0 || len(o.Needs) != 6 {
		t.Errorf("filter on a service: quiet %q, partial %d, needs %d", names(o.Quiet), len(o.Partial), len(o.Needs))
	}
	if o = group(views, nil, "-l"); names(o.Partial) != "l" || len(o.Quiet) != 0 {
		t.Errorf("filter on a name: partial %q, quiet %q", names(o.Partial), names(o.Quiet))
	}
}

func overviewDeps(stacks ...*inventory.Stack) Deps {
	return Deps{
		Current:   func() Snapshot { return Snapshot{Stacks: stacks, At: time.Now()} },
		Runs:      func(string) ([]run.View, error) { return nil, nil },
		Plan:      func(string) (*plan.Record, error) { return nil, nil },
		Postponed: func(string) time.Time { return time.Time{} },
	}
}

func get(h http.Handler, path string, header ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", path, nil)
	for i := 0; i+1 < len(header); i += 2 {
		r.Header.Set(header[i], header[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestOverviewFilter(t *testing.T) {
	h := New(overviewDeps(
		&inventory.Stack{Name: "immich", Services: []*inventory.Service{{Name: "server", Role: "application"}, {Name: "redis"}}},
		&inventory.Stack{Name: "whoami", Services: []*inventory.Service{{Name: "whoami"}}},
	))
	body := get(h, "/").Body.String()
	if !strings.Contains(body, "immich") || !strings.Contains(body, "whoami") {
		t.Fatal("both stacks should be listed without a query")
	}
	body = get(h, "/?q=REDIS").Body.String()
	if !strings.Contains(body, "/stacks/immich") || strings.Contains(body, "/stacks/whoami") {
		t.Errorf("q=REDIS: %s", body)
	}
}

func TestStairsKeepTheirCSSValues(t *testing.T) {
	h := New(Deps{
		Current: func() Snapshot { return Snapshot{} },
		Runs:    func(string) ([]run.View, error) { return nil, nil },
		Plan:    func(string) (*plan.Record, error) { return nil, nil },
		Run: func(id int64) (*run.View, error) {
			return &run.View{ID: id, Stack: "gitlab", Status: "rolled_back", Steps: []string{"a", "b", "c"}, StepsDone: 1}, nil
		},
	})
	body := get(h, "/runs/1").Body.String()
	want := `<ol class="stairs" aria-hidden="true" style="--n:3"><li class="done" style="--i:1"></li><li class="failed" style="--i:2"></li><li style="--i:3"></li></ol>`
	if !strings.Contains(body, want) {
		t.Errorf("stairs markup missing or filtered:\n%s", body)
	}
}

func TestScan(t *testing.T) {
	scans := 0
	deps := overviewDeps()
	deps.Rescan = func(string) { scans++ }
	var began time.Time
	deps.Current = func() Snapshot { return Snapshot{Began: began, At: began} }
	h := New(deps)
	for referer, want := range map[string]string{
		"http://example.com/stacks/gitlab?error=x": "/stacks/gitlab?error=x",
		"http://example.com/settings":              "/settings",
		"":                                         "/",
		"https://evil.example/x":                   "/",
		"//evil.example":                           "/",
		"http://example.com//evil.example":         "/",
		"http://example.com/auth/logout":           "/",
	} {
		r := httptest.NewRequest("POST", "/scan", nil)
		if referer != "" {
			r.Header.Set("Referer", referer)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != want {
			t.Errorf("referer %q: %d to %q, want %q", referer, w.Code, w.Header().Get("Location"), want)
		}
	}
	if scans != 7 {
		t.Errorf("%d scans, want 7", scans)
	}
	r := httptest.NewRequest("POST", "/scan", nil)
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	if w := httptest.NewRecorder(); func() int { h.ServeHTTP(w, r); return w.Code }() != http.StatusForbidden {
		t.Error("a cross-site scan was not refused")
	}
	page := func() string {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/settings", nil))
		return w.Body.String()
	}
	if !strings.Contains(page(), "Scan queued") {
		t.Error("a scan asked for and not begun is not shown as queued")
	}
	began = time.Now().Add(time.Second)
	if strings.Contains(page(), "Scan queued") {
		t.Error("a scan begun after it was asked for is still shown as queued")
	}
}

func TestStaticFilesNeedNoSession(t *testing.T) {
	deps := overviewDeps()
	deps.Config = config.Config{OIDCIssuer: "https://idp.invalid", OIDCClientID: "palier", OIDCAdmins: []string{"admins"}}
	h := New(deps)
	for path, kind := range map[string]string{"/static/app.css": "text/css", "/static/app.js": "text/javascript", "/static/fonts/IBMPlexSans-Regular-Latin1.woff2": "font/woff2"} {
		w := get(h, path)
		if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), kind) || !strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
			t.Errorf("%s: %d %q %q", path, w.Code, w.Header().Get("Content-Type"), w.Header().Get("Cache-Control"))
		}
	}
	for _, path := range []string{"/static/", "/static/fonts/", "/static/nope.css", "/static/../web.go"} {
		if w := get(h, path); w.Code != 404 || w.Header().Get("Cache-Control") != "" {
			t.Errorf("%s: %d", path, w.Code)
		}
	}
	if w := get(h, "/"); w.Code == http.StatusOK {
		t.Error("the page itself is reachable without a session")
	}
}

func TestHTMXWithoutSessionRefreshes(t *testing.T) {
	deps := overviewDeps()
	deps.Config = config.Config{OIDCIssuer: "https://idp.invalid", OIDCClientID: "palier", OIDCAdmins: []string{"admins"}}
	w := get(New(deps), "/stacks/whoami", "HX-Request", "true")
	if w.Code != http.StatusUnauthorized || w.Header().Get("HX-Refresh") != "true" || w.Body.Len() != 0 {
		t.Errorf("%d, HX-Refresh %q, %d bytes", w.Code, w.Header().Get("HX-Refresh"), w.Body.Len())
	}
}

func TestPageIsMorphedAndAssetsVersioned(t *testing.T) {
	body := get(New(overviewDeps()), "/").Body.String()
	for _, want := range []string{`<div id="page" hx-ext="morph" hx-get="" hx-select="#page" hx-target="this" hx-swap="morph" hx-trigger="every 30s">`, `href="/static/app.css?v=`, `<a href="/" aria-current="page">Stacks</a>`, `<form method="post" action="/scan" hx-post="/scan" hx-replace-url="true">`} {
		if !strings.Contains(body, want) {
			t.Errorf("%q missing", want)
		}
	}
	if strings.Contains(body, "http-equiv") {
		t.Error("the page still refreshes by itself with a meta tag")
	}
	if got := get(New(overviewDeps()), "/settings").Body.String(); !strings.Contains(got, `<a href="/settings" aria-current="page">Settings</a>`) || strings.Contains(got, `<a href="/" aria-current`) {
		t.Error("the settings page does not mark its own link")
	}
}

func TestStateForgetsARunThatNoLongerMatters(t *testing.T) {
	up := &inventory.Stack{Name: "app", Services: []*inventory.Service{{Name: "app", State: "running"}}}
	down := &inventory.Stack{Name: "app", Services: []*inventory.Service{{Name: "app", State: "exited"}}}
	pending := &plan.Record{Plan: &plan.Plan{Steps: []plan.Step{{}}}}
	for _, c := range []struct {
		status string
		st     *inventory.Stack
		rec    *plan.Record
		want   string
	}{
		{"failed", up, nil, "up to date"},
		{"failed", down, nil, "failed"},
		{"failed", up, pending, "failed"},
		{"rolled_back", up, nil, "up to date"},
		{"rolled_back", up, pending, "rolled back"},
		{"abandoned", down, nil, "up to date"},
		{"running", up, nil, "running"},
	} {
		s := &server{deps: Deps{
			Current: func() Snapshot { return Snapshot{At: time.Now()} },
			Runs:    func(string) ([]run.View, error) { return []run.View{{ID: 1, Status: c.status}}, nil },
			Plan:    func(string) (*plan.Record, error) { return c.rec, nil },
		}}
		if got := s.state(c.st); got != c.want {
			t.Errorf("last run %s, plan pending %t, healthy %t: %q, want %q", c.status, c.rec != nil, c.st.Healthy(), got, c.want)
		}
	}
}

func TestScanOneStack(t *testing.T) {
	var asked []string
	deps := overviewDeps(&inventory.Stack{Name: "whoami"}, &inventory.Stack{Name: "outside", Observed: true})
	deps.Rescan = func(stack string) { asked = append(asked, stack) }
	h := New(deps)
	post := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", path, nil))
		return w
	}
	if w := post("/stacks/whoami/scan"); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/stacks/whoami" || !slices.Equal(asked, []string{"whoami"}) {
		t.Errorf("scan of whoami: %d to %q, asked %v", w.Code, w.Header().Get("Location"), asked)
	}
	if w := post("/stacks/nope/scan"); w.Code != http.StatusNotFound || len(asked) != 1 {
		t.Errorf("scan of an unknown stack: %d, asked %v", w.Code, asked)
	}
	if body := get(h, "/stacks/whoami").Body.String(); !strings.Contains(body, `action="/stacks/whoami/scan"`) {
		t.Error("the page of a Compose stack has no scan button")
	}
	if body := get(h, "/stacks/outside").Body.String(); strings.Contains(body, "/scan\" hx-post=\"/stacks/outside/scan") {
		t.Error("the page of an observed stack offers a scan")
	}
}

func TestAcceptMaturingVersion(t *testing.T) {
	var got []string
	st := &inventory.Stack{Name: "shm", Services: []*inventory.Service{{Name: "app", Tag: "1.2.0", Version: "1.2.0", State: "running", Maturing: []inventory.Release{{Tag: "1.3.0"}}}}}
	deps := overviewDeps(st)
	deps.Decide = func(stack, author, kind, service, version, until string) error {
		got = append(got, stack+" "+kind+" "+service+" "+version)
		return nil
	}
	h := New(deps)
	post := func(form string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/stacks/shm/accept", strings.NewReader(form))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := post("service=app&version=1.3.0"); w.Header().Get("Location") != "/stacks/shm" || !slices.Equal(got, []string{"shm accept app 1.3.0"}) {
		t.Errorf("accept: to %q, decided %v", w.Header().Get("Location"), got)
	}
	if w := post("service=app"); !strings.Contains(w.Header().Get("Location"), "error=") || len(got) != 1 {
		t.Errorf("accept without a version: to %q, decided %v", w.Header().Get("Location"), got)
	}
	if body := get(h, "/stacks/shm").Body.String(); !strings.Contains(body, `action="/stacks/shm/accept"`) || !strings.Contains(body, "Take 1.3.0 now") {
		t.Error("the page does not offer to take the maturing version now")
	}
}
