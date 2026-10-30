package plan

import (
	"strings"
	"testing"

	"github.com/kolapsis/palier/internal/inventory"
	"github.com/kolapsis/palier/internal/notes"
)

func rel(tags ...string) []inventory.Release {
	var out []inventory.Release
	for _, t := range tags {
		out = append(out, inventory.Release{Tag: t})
	}
	return out
}

func outline() *inventory.Stack {
	return &inventory.Stack{Name: "outline", Services: []*inventory.Service{
		{Name: "outline", Role: inventory.RoleApplication, Image: "docker.io/outlinewiki/outline", Version: "1.10.1", Available: rel("1.11.0", "1.12.0"),
			Notes: []notes.Note{{Tag: "v1.12.0", URL: "https://example/1.12.0", Body: "Outline now supports PostgreSQL 14 to 17 in production."}}},
		{Name: "postgres", Role: inventory.RoleDependency, Image: "docker.io/library/postgres", Version: "16.4", Available: rel("16.10", "17.6", "18.1")},
		{Name: "redis", Role: inventory.RoleDependency, Image: "docker.io/library/redis", Version: "7.2.4", Available: rel("7.2.16", "8.2.1")},
	}}
}

func step(changes ...Change) Step {
	s := Step{Risk: "safe", Reversible: "yes", Changes: changes}
	var names []string
	for _, c := range changes {
		image := map[string]string{"outline": "outlinewiki/outline", "postgres": "postgres", "redis": "redis"}[c.Service]
		s.Operations = append(s.Operations, Operation{Op: "set_image", Service: c.Service, Image: image, Tag: c.To})
		names = append(names, c.Service)
	}
	s.Operations = append(s.Operations, Operation{Op: "recreate", Services: names})
	if len(names) == 1 {
		s.Operations[len(s.Operations)-1] = Operation{Op: "recreate", Service: names[0]}
	}
	return s
}

func reasons(p *Plan) string { return strings.Join(p.Incomplete, "\n") }

func TestValidatePasses(t *testing.T) {
	p := &Plan{Steps: []Step{step(Change{"redis", "7.2.4", "7.2.16"}), step(Change{"outline", "1.10.1", "1.11.0"}), step(Change{"outline", "1.11.0", "1.12.0"})}}
	p.Steps[2].Facts = []Fact{{Category: "supported_dependency", Service: "outline", Version: "1.12.0", Dependency: "postgres", Quote: "supports PostgreSQL 14 to 17", SourceURL: "x"}}
	Validate(p, outline())
	if len(p.Incomplete) > 0 {
		t.Fatal(reasons(p))
	}
	if p.Steps[2].Facts[0].SourceURL != "https://example/1.12.0" {
		t.Fatalf("source url not taken from the note: %q", p.Steps[2].Facts[0].SourceURL)
	}
}

func TestValidateRejects(t *testing.T) {
	cases := map[string]struct {
		plan *Plan
		want string
	}{
		"version absente":    {&Plan{Steps: []Step{step(Change{"outline", "1.10.1", "1.13.0"})}}, "pas une version disponible"},
		"départ faux":        {&Plan{Steps: []Step{step(Change{"outline", "1.11.0", "1.12.0"})}}, "part de 1.11.0"},
		"deux paliers":       {&Plan{Steps: []Step{step(Change{"outline", "1.10.1", "1.11.0"}, Change{"outline", "1.11.0", "1.12.0"})}}, "un seul palier"},
		"majeure redis":      {&Plan{Steps: []Step{step(Change{"redis", "7.2.4", "8.2.1"})}}, "sa majeure ne change pas"},
		"majeure postgres":   {&Plan{Steps: []Step{step(Change{"postgres", "16.4", "17.6"})}}, "sans db_dump_restore"},
		"opération inconnue": {&Plan{Steps: []Step{{Risk: "safe", Reversible: "yes", Operations: []Operation{{Op: "shell", Service: "outline"}}}}}, "hors du vocabulaire"},
		"risque inconnu":     {&Plan{Steps: []Step{{Risk: "low", Reversible: "yes"}}}, "niveau de risque"},
		"citation absente":   {&Plan{Steps: []Step{{Risk: "safe", Reversible: "yes", Facts: []Fact{{Category: "breaking_change", Quote: "this sentence is not in the notes at all"}}}}}, "mot pour mot"},
		"sans recreate":      {&Plan{Steps: []Step{{Risk: "safe", Reversible: "yes", Changes: []Change{{"redis", "7.2.4", "7.2.16"}}, Operations: []Operation{{Op: "set_image", Service: "redis", Image: "redis", Tag: "7.2.16"}}}}}, "aucun recreate"},
		"autre image":        {&Plan{Steps: []Step{{Risk: "safe", Reversible: "yes", Changes: []Change{{"redis", "7.2.4", "7.2.16"}}, Operations: []Operation{{Op: "set_image", Service: "redis", Image: "busybox", Tag: "7.2.16"}, {Op: "recreate", Services: []string{"redis"}}}}}}, "change l'image"},
	}
	for name, c := range cases {
		Validate(c.plan, outline())
		if !strings.Contains(reasons(c.plan), c.want) {
			t.Errorf("%s: want %q in:\n%s", name, c.want, reasons(c.plan))
		}
	}
}

func TestFallbackKeepsDependencyMajors(t *testing.T) {
	p := Fallback(outline(), "aucune note")
	got := map[string]string{}
	for _, c := range p.Steps[0].Changes {
		got[c.Service] = c.To
	}
	if got["outline"] != "1.12.0" || got["postgres"] != "16.10" || got["redis"] != "7.2.16" || p.Steps[0].Risk != "unknown" {
		t.Fatalf("fallback: %v", got)
	}
	Validate(p, outline())
	if len(p.Incomplete) > 0 {
		t.Fatal(reasons(p))
	}
}

func TestAutomatic(t *testing.T) {
	patch := &Plan{Steps: []Step{step(Change{"redis", "7.2.4", "7.2.16"})}}
	minor := &Plan{Steps: []Step{step(Change{"outline", "1.10.1", "1.11.0"})}}
	unknown := &Plan{Steps: []Step{{Risk: "unknown", Reversible: "unknown"}}}
	for _, c := range []struct {
		p    *Plan
		auto string
		want bool
	}{{patch, "patch", true}, {minor, "patch", false}, {minor, "minor", true}, {patch, "off", false}, {unknown, "minor", false}} {
		if ok, why := Automatic(c.p, c.auto); ok != c.want {
			t.Errorf("Automatic(%s) = %v (%s)", c.auto, ok, why)
		}
	}
}
