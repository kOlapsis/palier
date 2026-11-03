package plan

import (
	"strings"
	"testing"

	"github.com/kolapsis/palier/internal/inventory"
	"github.com/kolapsis/palier/internal/notes"
)

func TestSecurityFirst(t *testing.T) {
	adv := notes.Advisory{ID: "GHSA-x", CVE: "CVE-2026-1", URL: "https://example/adv", Affected: []string{">= 16.0.0, < 16.1.8", ">= 16.2.0, < 16.7.10"}, Patched: []string{"16.1.8", "16.7.10"}}
	gitlab := func() *inventory.Stack {
		var avail []inventory.Release
		for _, tag := range []string{"16.1.7-ce.0", "16.1.8-ce.0", "16.3.9-ce.0", "16.7.10-ce.0"} {
			avail = append(avail, inventory.Release{Tag: tag})
		}
		return &inventory.Stack{Name: "gitlab", Services: []*inventory.Service{{Name: "gitlab", Image: "docker.io/gitlab/gitlab-ce", Version: "16.1.6-ce.0", Available: avail,
			Advisories: []notes.Advisory{adv}, Notes: []notes.Note{{URL: adv.URL, Body: "GHSA-x CVE-2026-1 critical: account takeover through the password reset"}}}}}
	}
	climb := func() *Plan {
		return &Plan{Steps: []Step{
			{Changes: []Change{{Service: "gitlab", From: "16.1.6-ce.0", To: "16.3.9-ce.0"}}},
			{Changes: []Change{{Service: "gitlab", From: "16.3.9-ce.0", To: "16.7.10-ce.0"}}},
		}}
	}
	p := climb()
	SecurityFirst(p, gitlab())
	if len(p.Steps) != 3 {
		t.Fatalf("steps: %+v", p.Steps)
	}
	first := p.Steps[0]
	if c := first.Changes[0]; c.From != "16.1.6-ce.0" || c.To != "16.1.8-ce.0" || p.Steps[1].Changes[0].From != "16.1.8-ce.0" {
		t.Errorf("first step %+v, then from %s", c, p.Steps[1].Changes[0].From)
	}
	if first.Gate == nil || first.Gate.CheckRef != "background_migrations" || len(first.Facts) != 1 || first.Facts[0].Category != "security_fix" || !strings.Contains(first.Why, "CVE-2026-1") {
		t.Errorf("first step: %+v", first)
	}
	if len(first.Operations) != 2 || first.Operations[0].Tag != "16.1.8-ce.0" || first.Operations[1].Op != "recreate" {
		t.Errorf("operations: %+v", first.Operations)
	}

	direct := &Plan{Steps: []Step{{Changes: []Change{{Service: "gitlab", From: "16.1.6-ce.0", To: "16.1.8-ce.0"}}}}}
	SecurityFirst(direct, gitlab())
	if len(direct.Steps) != 1 {
		t.Errorf("a plan that closes it at once got another step: %+v", direct.Steps)
	}
	st := gitlab()
	st.Services[0].Available = st.Services[0].Available[2:]
	noFix := climb()
	SecurityFirst(noFix, st)
	if len(noFix.Steps) != 2 {
		t.Errorf("no patch on the line, yet a step was added: %+v", noFix.Steps)
	}
}

func TestSecurityFirstStepPassesValidation(t *testing.T) {
	adv := notes.Advisory{ID: "GHSA-x", CVE: "CVE-2026-1", URL: "https://example/adv", Affected: []string{">= 16.0.0, < 16.1.8", ">= 16.2.0, < 16.7.10"}, Patched: []string{"16.1.8", "16.7.10"}}
	st := &inventory.Stack{Name: "gitlab", Services: []*inventory.Service{{Name: "gitlab", Role: "application", Image: "docker.io/gitlab/gitlab-ce", Version: "16.1.6-ce.0",
		Available:  []inventory.Release{{Tag: "16.1.8-ce.0"}, {Tag: "16.3.9-ce.0"}},
		Advisories: []notes.Advisory{adv}, Notes: []notes.Note{{URL: adv.URL, Body: "GHSA-x CVE-2026-1 critical: account takeover through the password reset"}}}}}
	p := &Plan{Steps: []Step{{Risk: "safe", Reversible: "yes", Gate: &Operation{Op: "exec_check", Service: "gitlab", CheckRef: "background_migrations"},
		Changes:    []Change{{Service: "gitlab", From: "16.1.6-ce.0", To: "16.3.9-ce.0"}},
		Operations: []Operation{{Op: "set_image", Service: "gitlab", Image: "docker.io/gitlab/gitlab-ce", Tag: "16.3.9-ce.0"}, {Op: "recreate", Services: []string{"gitlab"}}}}}}
	SecurityFirst(p, st)
	Validate(p, st)
	if len(p.Steps) != 2 || len(p.Incomplete) > 0 || len(p.Steps[0].Facts) != 1 {
		t.Fatalf("steps %d, incomplete %q, facts %+v", len(p.Steps), p.Incomplete, p.Steps[0].Facts)
	}
}
