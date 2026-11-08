package main

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kolapsis/palier/internal/inventory"
	"github.com/kolapsis/palier/internal/notes"
	"github.com/kolapsis/palier/internal/notify"
	"github.com/kolapsis/palier/internal/plan"
	"github.com/kolapsis/palier/internal/run"
	"github.com/kolapsis/palier/internal/web"
)

func TestProblems(t *testing.T) {
	up := func(name string, extra ...func(*inventory.Service)) *inventory.Stack {
		s := &inventory.Service{Name: name, State: "running"}
		for _, f := range extra {
			f(s)
		}
		return &inventory.Stack{Name: name, Services: []*inventory.Service{s}}
	}
	step := []plan.Step{{Risk: "safe", Reversible: "yes", Changes: []plan.Change{{Service: "x", From: "1", To: "2"}}}}
	blocking := []plan.Step{{Risk: "blocking", Reversible: "yes", Changes: []plan.Change{{Service: "x", From: "1", To: "2"}}}}
	critical := func(s *inventory.Service) {
		s.Advisories = []notes.Advisory{{ID: "GHSA-1", Severity: "critical", Summary: "bypass"}}
	}
	snap := web.Snapshot{At: time.Now(), Stacks: []*inventory.Stack{
		up("rolled"), up("failed"), up("fixed"), up("blocked", critical), up("patched", critical),
		up("registry", func(s *inventory.Service) { s.DetectionError = "429" }),
		up("private", func(s *inventory.Service) { s.DetectionError, s.RegistryRefused = "ghcr.io refuses", true }),
		up("hub", func(s *inventory.Service) { s.NotInRegistry = true }),
		{Name: "watched", Observed: true},
	}}
	last := map[string]run.View{
		"rolled": {ID: 3, Status: run.RolledBack, Error: "step 1 failed\nlogs"},
		"failed": {ID: 4, Status: run.Failed},
		"fixed":  {ID: 5, Status: run.RolledBack},
	}
	plans := map[string]*plan.Plan{"rolled": {Steps: step}, "failed": {Steps: step}, "blocked": {Steps: blocking}, "patched": {Steps: step}}
	got := map[string]notify.Problem{}
	for _, p := range problems(snap, last, plans, "all") {
		got[p.Key] = p
	}
	var keys []string
	for k := range got {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	want := []string{"blocked:decision", "blocked:vulnerable", "failed:failed", "hub/hub:detection", "private/private:detection", "registry/registry:detection", "rolled:rolled_back"}
	if !slices.Equal(keys, want) {
		t.Fatalf("problems %v, want %v", keys, want)
	}
	if p := got["rolled:rolled_back"]; p.Level != notify.Info || p.Detail != "step 1 failed\nlogs" || p.Path != "/runs/3" {
		t.Errorf("rolled back: %+v", p)
	}
	if p := got["hub/hub:detection"]; p.Level != notify.Notice || p.Flaky {
		t.Errorf("not on Docker Hub must be shown: %+v", p)
	}
	if p := got["private/private:detection"]; p.Level != notify.Info || p.Flaky {
		t.Errorf("registry refusal must be sent at once: %+v", p)
	}
	if got["failed:failed"].Level != notify.Alert || got["blocked:vulnerable"].Level != notify.Alert || !got["registry/registry:detection"].Flaky || got["registry/registry:detection"].Level != notify.Notice {
		t.Errorf("levels: %+v", got)
	}
}

func TestSummarizeGroupsByStack(t *testing.T) {
	day := func(d int) string { return time.Date(2026, 11, d, 9, 0, 0, 0, time.UTC).Format(time.RFC3339) }
	runs := []run.View{
		{ID: 1, Stack: "gitlab", Status: run.Done, Finished: day(2), Changes: []plan.Change{{Service: "gitlab", From: "17.11.7", To: "18.2.8"}, {Service: "gitlab", From: "18.2.8", To: "19.5.3"}}},
		{ID: 2, Stack: "nginx", Status: run.Abandoned, Finished: day(2), Error: "the backup hook failed\ntar: exit 2"},
		{ID: 3, Stack: "nginx", Status: run.Abandoned, Finished: day(3), Error: "the backup hook failed\ntar: exit 2"},
		{ID: 4, Stack: "outline", Status: run.Done, Finished: day(3), Changes: []plan.Change{{Service: "postgres", From: "16.4", To: "18.4"}, {Service: "outline", From: "1.10.1", To: "1.12.0"}}},
		{ID: 5, Stack: "whoami", Status: run.Done, Finished: day(3), DryRun: true, Changes: []plan.Change{{Service: "whoami", From: "v1.10.1", To: "v1.12.0"}}},
		{ID: 6, Stack: "old", Status: run.Done, Finished: day(1), Changes: []plan.Change{{Service: "old", From: "1", To: "2"}}},
	}
	m := summarize(runs, time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC), time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC))
	if len(m.Lists) != 2 || !slices.Equal(m.Lists[0].Items, []string{"gitlab: 17.11.7 → 19.5.3", "outline: postgres 16.4 → 18.4, outline 1.10.1 → 1.12.0"}) {
		t.Fatalf("updated: %+v", m.Lists)
	}
	if got := m.Lists[1].Items; len(got) != 1 || !strings.HasPrefix(got[0], "nginx: not started 2 times, last on Tue Nov 3: the backup hook failed") {
		t.Errorf("did not go through: %q", got)
	}
	if m.Subject != "Weekly report: 2 stacks updated" || m.Summary != "2 stacks updated, 1 with an update that did not go through." {
		t.Errorf("%q / %q", m.Subject, m.Summary)
	}
}
