package plan

import (
	"strings"
	"testing"

	"github.com/kolapsis/palier/internal/inventory"
	"github.com/kolapsis/palier/internal/notes"
)

func n8n(n8nRole string, runners ...string) *inventory.Stack {
	src := "https://github.com/n8n-io/n8n"
	return &inventory.Stack{Name: "n8n", Services: []*inventory.Service{
		{Name: "n8n", Role: n8nRole, Image: "docker.io/n8nio/n8n", Version: "2.45.7", Source: src, Available: rel("2.45.9", "2.48.4", "3.2.1", "3.2.2")},
		{Name: "task-runners", Role: inventory.RoleApplication, Image: "docker.io/n8nio/runners", Version: "2.45.7", Source: src, Available: rel(runners...)},
		{Name: "postgres", Role: inventory.RoleDependency, Image: "docker.io/library/postgres", Version: "16.4", Available: rel("16.10")},
	}}
}

func targets(p *Plan) map[string]string {
	got := map[string]string{}
	for _, s := range p.Steps {
		for _, c := range s.Changes {
			got[c.Service] = c.To
		}
	}
	return got
}

func TestTogether(t *testing.T) {
	st := n8n(inventory.RoleApplication, "3.2.2")
	st.Services = append(st.Services, &inventory.Service{Name: "old", Version: "2.44.0", Source: st.Services[0].Source})
	got := Together(st)
	if len(got) != 2 || len(got["n8n"]) != 1 || got["n8n"][0].Name != "task-runners" || got["task-runners"][0].Name != "n8n" {
		t.Fatalf("together: %v", got)
	}
}

func TestTogetherValidated(t *testing.T) {
	split := &Plan{Steps: []Step{step(Change{"n8n", "2.45.7", "2.48.4"}, Change{"task-runners", "2.45.7", "3.2.2"})}}
	Validate(split, n8n(inventory.RoleDependency, "2.48.4", "3.2.2"))
	if r := reasons(split); strings.Count(r, "change together") != 1 || !strings.Contains(r, "n8n and task-runners come from https://github.com/n8n-io/n8n") {
		t.Errorf("split plan: %s", r)
	}
	alone := &Plan{Steps: []Step{step(Change{"n8n", "2.45.7", "3.2.2"})}}
	Validate(alone, n8n(inventory.RoleApplication, "3.2.2"))
	if !strings.Contains(reasons(alone), "change together") {
		t.Errorf("n8n alone: %s", reasons(alone))
	}
	both := &Plan{Steps: []Step{step(Change{"n8n", "2.45.7", "3.2.2"}, Change{"task-runners", "2.45.7", "3.2.2"})}}
	Validate(both, n8n(inventory.RoleApplication, "3.2.2"))
	if strings.Contains(reasons(both), "change together") {
		t.Errorf("aligned plan: %s", reasons(both))
	}
}

func TestFallbackTogether(t *testing.T) {
	for _, c := range []struct {
		role    string
		runners []string
		want    string
	}{
		{inventory.RoleDependency, []string{"2.48.4", "3.2.2"}, "2.48.4"},
		{inventory.RoleApplication, []string{"2.48.4", "3.2.2"}, "3.2.2"},
		{inventory.RoleApplication, []string{"2.48.4", "3.2.1"}, "3.2.1"},
	} {
		st := n8n(c.role, c.runners...)
		p := Fallback(st, "x")
		if got := targets(p); got["n8n"] != c.want || got["task-runners"] != c.want {
			t.Errorf("%s, runners %v: %v", c.role, c.runners, got)
		}
		Validate(p, st)
		if strings.Contains(reasons(p), "change together") {
			t.Errorf("%s: %s", c.role, reasons(p))
		}
	}
}

func TestSecurityFirstTogether(t *testing.T) {
	adv := notes.Advisory{ID: "GHSA-n", URL: "https://example/adv", Affected: []string{"< 2.45.9", ">= 3.0.0, < 3.2.2"}, Patched: []string{"2.45.9", "3.2.2"}}
	climb := func() *Plan {
		return &Plan{Steps: []Step{step(Change{"n8n", "2.45.7", "3.2.1"}, Change{"task-runners", "2.45.7", "3.2.1"})}}
	}
	st := n8n(inventory.RoleApplication, "2.45.9", "3.2.1")
	for _, s := range st.Services[:2] {
		s.Advisories = []notes.Advisory{adv}
	}
	p := climb()
	SecurityFirst(p, st)
	if len(p.Steps) != 2 || len(p.Steps[0].Changes) != 2 || targets(&Plan{Steps: p.Steps[:1]})["task-runners"] != "2.45.9" || p.Steps[1].Changes[1].From != "2.45.9" {
		t.Fatalf("steps: %+v", p.Steps)
	}
	st.Services[1].Available = rel("3.2.1")
	p = climb()
	SecurityFirst(p, st)
	if len(p.Steps) != 1 {
		t.Errorf("task-runners has no 2.45.9, yet a step was added: %+v", p.Steps)
	}
}
