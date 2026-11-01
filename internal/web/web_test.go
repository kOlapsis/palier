package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kolapsis/palier/internal/inventory"
	"github.com/kolapsis/palier/internal/plan"
	"github.com/kolapsis/palier/internal/run"
)

func TestTokenGuardsEverything(t *testing.T) {
	h := New("secret", Deps{
		Current: func() Snapshot { return Snapshot{} },
		Runs:    func(string) ([]run.View, error) { return nil, nil },
		Plan:    func(string) (*plan.Record, error) { return nil, nil },
	})
	do := func(method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if body != "" {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	if w := do("GET", "/", "", nil); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/login" {
		t.Fatalf("anonymous /: %d %s", w.Code, w.Header().Get("Location"))
	}
	if w := do("GET", "/anything", "", &http.Cookie{Name: cookieName, Value: "wrong"}); w.Code != http.StatusSeeOther {
		t.Fatalf("wrong cookie: %d", w.Code)
	}
	if w := do("POST", "/login", url.Values{"token": {"wrong"}}.Encode(), nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d", w.Code)
	}
	w := do("POST", "/login", url.Values{"token": {"secret"}}.Encode(), nil)
	cookies := w.Result().Cookies()
	if w.Code != http.StatusSeeOther || len(cookies) != 1 || !cookies[0].HttpOnly {
		t.Fatalf("good token: %d %v", w.Code, cookies)
	}
	if w := do("GET", "/", "", cookies[0]); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Stacks") {
		t.Fatalf("authorized /: %d", w.Code)
	}
}

func TestPagesRender(t *testing.T) {
	st := &inventory.Stack{Name: "whoami", Services: []*inventory.Service{{Name: "whoami", Role: "application", Image: "docker.io/traefik/whoami", Tag: "v1.10.1", Version: "v1.10.1", Available: []inventory.Release{{Tag: "v1.12.0"}}}}}
	p := &plan.Plan{Steps: []plan.Step{{Risk: "safe", Reversible: "yes", Why: "Une phrase.",
		Changes:    []plan.Change{{Service: "whoami", From: "v1.10.1", To: "v1.12.0"}},
		Operations: []plan.Operation{{Op: "set_image", Service: "whoami", Image: "traefik/whoami", Tag: "v1.12.0"}, {Op: "recreate", Services: []string{"whoami"}}},
		Gate:       &plan.Operation{Op: "exec_check", Service: "whoami", CheckRef: "background_migrations"},
		Facts:      []plan.Fact{{Category: "security_fix", Service: "whoami", Version: "v1.12.0", Quote: "<script>alert(1)</script> a quote", SourceURL: "https://github.com/traefik/whoami/releases"}}},
		{Risk: "attention", Reversible: "yes", Operations: []plan.Operation{{Op: "compose_edit", Service: "whoami", Edits: []plan.Edit{{Action: "rename_env", Name: "SERVER_PORT", NewName: "IMMICH_PORT"}}}}}}}
	h := New("secret", Deps{
		Current:   func() Snapshot { return Snapshot{Stacks: []*inventory.Stack{st}} },
		Runs:      func(string) ([]run.View, error) { return []run.View{{ID: 1, Status: "rolled_back"}}, nil },
		Plan:      func(string) (*plan.Record, error) { return &plan.Record{Stack: "whoami", Source: "ai", Plan: p}, nil },
		Postponed: func(string) time.Time { return time.Time{} },
		Run: func(int64) (*run.View, error) {
			return &run.View{ID: 1, Stack: "whoami", Status: "running", Events: []run.Event{{Message: "étape 1"}}}, nil
		},
	})
	for path, want := range map[string]string{"/": "revenue en arrière", "/stacks/whoami": "rename_env SERVER_PORT → IMMICH_PORT", "/runs/1": "étape 1", "/settings": "PALIER_AUTO"} {
		r := httptest.NewRequest("GET", path, nil)
		r.AddCookie(&http.Cookie{Name: cookieName, Value: "secret"})
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
