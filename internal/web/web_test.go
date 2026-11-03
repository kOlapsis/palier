package web

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kolapsis/palier/internal/inventory"
	"github.com/kolapsis/palier/internal/notes"
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
	for path, want := range map[string]string{"/runs/1?progress": `<li class="done">Step 1: gitlab 16.1.6 to 16.3.9</li><li class="current">Step 2`, "/": "rolled back", "/stacks/whoami": "rename_env SERVER_PORT → IMMICH_PORT", "/runs/1": "step 1", "/settings": "PALIER_AUTO"} {
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
