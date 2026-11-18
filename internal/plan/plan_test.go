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
			Notes: []notes.Note{{Tag: "v1.12.0", URL: "https://example/1.12.0", Body: "Outline now supports PostgreSQL 14 to 17 in production."},
				{URL: "https://example/requirements", Body: "* [PostgreSQL](https://www.postgresql.org/) (v14+)"}}},
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
		"missing version":   {&Plan{Steps: []Step{step(Change{"outline", "1.10.1", "1.13.0"})}}, "not a version available"},
		"wrong start":       {&Plan{Steps: []Step{step(Change{"outline", "1.11.0", "1.12.0"})}}, "starts from 1.11.0"},
		"two versions":      {&Plan{Steps: []Step{step(Change{"outline", "1.10.1", "1.11.0"}, Change{"outline", "1.11.0", "1.12.0"})}}, "one version per service"},
		"major redis":       {&Plan{Steps: []Step{step(Change{"redis", "7.2.4", "8.2.1"})}}, "its major does not change"},
		"major postgres":    {&Plan{Steps: []Step{step(Change{"postgres", "16.4", "17.6"})}}, "without db_dump_restore"},
		"unknown operation": {&Plan{Steps: []Step{{Risk: "safe", Reversible: "yes", Operations: []Operation{{Op: "shell", Service: "outline"}}}}}, "outside the vocabulary"},
		"unknown risk":      {&Plan{Steps: []Step{{Risk: "low", Reversible: "yes"}}}, "risk level"},
		"missing quote":     {&Plan{Steps: []Step{{Risk: "safe", Reversible: "yes", Facts: []Fact{{Category: "breaking_change", Quote: "this sentence is not in the notes at all"}}}}}, "word for word"},
		"no recreate":       {&Plan{Steps: []Step{{Risk: "safe", Reversible: "yes", Changes: []Change{{"redis", "7.2.4", "7.2.16"}}, Operations: []Operation{{Op: "set_image", Service: "redis", Image: "redis", Tag: "7.2.16"}}}}}, "no recreate"},
		"autre image":       {&Plan{Steps: []Step{{Risk: "safe", Reversible: "yes", Changes: []Change{{"redis", "7.2.4", "7.2.16"}}, Operations: []Operation{{Op: "set_image", Service: "redis", Image: "busybox", Tag: "7.2.16"}, {Op: "recreate", Services: []string{"redis"}}}}}}, "changes the image"},
	}
	for name, c := range cases {
		Validate(c.plan, outline())
		if !strings.Contains(reasons(c.plan), c.want) {
			t.Errorf("%s: want %q in:\n%s", name, c.want, reasons(c.plan))
		}
	}
}

func TestFallbackKeepsDependencyMajors(t *testing.T) {
	p := Fallback(outline(), "no notes")
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
	attention := &Plan{Steps: []Step{step(Change{"uptimekuma", "2.5.5", "2.5.8"})}}
	attention.Steps[0].Risk, attention.Steps[0].Reversible = "attention", "conditional"
	major := &Plan{Steps: []Step{step(Change{"postgres", "16.14", "17.6"})}}
	blocking := &Plan{Steps: []Step{step(Change{"redis", "7.2.4", "7.2.16"})}}
	blocking.Steps[0].Risk = "blocking"
	irreversible := &Plan{Steps: []Step{step(Change{"redis", "7.2.4", "7.2.16"})}}
	irreversible.Steps[0].Reversible = "no"
	incomplete := &Plan{Steps: patch.Steps, Incomplete: []string{"citation absente"}}
	for _, c := range []struct {
		p    *Plan
		auto string
		want bool
	}{{patch, "patch", true}, {minor, "patch", false}, {minor, "minor", true}, {patch, "off", false},
		{unknown, "all", true}, {attention, "all", true}, {attention, "patch", true}, {major, "all", true}, {major, "minor", false},
		{blocking, "all", false}, {irreversible, "all", false}, {incomplete, "all", false}} {
		if ok, why := Automatic(c.p, c.auto); ok != c.want {
			t.Errorf("Automatic(%s) = %v (%s)", c.auto, ok, why)
		}
	}
}

func TestIntervalsAtEachStep(t *testing.T) {
	support := Fact{Category: "supported_dependency", Service: "outline", Version: "1.12.0", Dependency: "postgres", MinVersion: "14", MaxVersion: "17", Quote: "supports PostgreSQL 14 to 17"}
	pg := step(Change{"postgres", "16.4", "17.6"})
	pg.Operations = []Operation{{Op: "db_dump_restore", Service: "postgres", Engine: "postgres", ToImage: "postgres", ToTag: "17.6"}}
	// postgres 17 before Outline reaches 1.12, the first version that supports it: refused.
	early := &Plan{Steps: []Step{pg, step(Change{"outline", "1.10.1", "1.12.0"})}}
	early.Steps[1].Facts = []Fact{support}
	Validate(early, outline())
	if !strings.Contains(reasons(early), "at its version of this step") {
		t.Fatalf("early major accepted:\n%s", reasons(early))
	}
	// postgres 18 after Outline 1.12, beyond what it supports: refused.
	pg18 := step(Change{"postgres", "16.4", "18.1"})
	beyond := &Plan{Steps: []Step{step(Change{"outline", "1.10.1", "1.12.0"}), pg18}}
	beyond.Steps[0].Facts = []Fact{support}
	Validate(beyond, outline())
	if !strings.Contains(reasons(beyond), "supports postgres up to 17") {
		t.Fatalf("postgres 18 accepted:\n%s", reasons(beyond))
	}
	// a minimum the dependency does not reach yet: refused.
	minimum := &Plan{Steps: []Step{step(Change{"outline", "1.10.1", "1.11.0"})}}
	minimum.Steps[0].Facts = []Fact{{Category: "min_dependency", Service: "outline", Version: "1.11.0", Dependency: "redis", MinVersion: "7.4", Quote: "supports PostgreSQL 14 to 17"}}
	Validate(minimum, outline())
	if !strings.Contains(reasons(minimum), "requires redis 7.4 at least") {
		t.Fatalf("minimum not checked:\n%s", reasons(minimum))
	}
}

func TestMajorAfterSupport(t *testing.T) {
	support := Fact{Category: "supported_dependency", Service: "outline", Version: "1.12.0", Dependency: "redis", MinVersion: "7", MaxVersion: "8", Quote: "supports PostgreSQL 14 to 17"}
	ok := &Plan{Steps: []Step{step(Change{"outline", "1.10.1", "1.12.0"}), step(Change{"redis", "7.2.4", "8.2.1"})}}
	ok.Steps[0].Facts = []Fact{support}
	Validate(ok, outline())
	if len(ok.Incomplete) > 0 {
		t.Fatal(reasons(ok))
	}
}

func TestWithheldSaysWhy(t *testing.T) {
	st := outline()
	st.Services[0].DependsOn = []string{"postgres", "redis"}
	p := &Plan{Steps: []Step{step(Change{"postgres", "16.4", "16.10"})}}
	Validate(p, st)
	got := strings.Join(p.Withheld, "\n")
	if !strings.Contains(got, "postgres 17.6, 18.1 exist but are not offered: no release note of outline cites a version of postgres; postgres stays on 16.") || !strings.Contains(got, "redis 8.2.1 exists") {
		t.Fatalf("withheld:\n%s", got)
	}
}

func TestDumpRestoreChecked(t *testing.T) {
	support := Fact{Category: "supported_dependency", Service: "outline", Dependency: "postgres", MinVersion: "14", Quote: "[PostgreSQL](https://www.postgresql.org/) (v14+)"}
	good := &Plan{Steps: []Step{{Risk: "attention", Reversible: "yes", Facts: []Fact{support},
		Changes:    []Change{{"postgres", "16.4", "18.1"}},
		Operations: []Operation{{Op: "db_dump_restore", Service: "postgres", Engine: "postgres", ToImage: "postgres", ToTag: "18.1"}}}}}
	Validate(good, outline())
	if len(good.Incomplete) > 0 {
		t.Fatal(reasons(good))
	}
	mysql := &Plan{Steps: []Step{{Risk: "attention", Reversible: "yes", Facts: []Fact{support},
		Changes:    []Change{{"postgres", "16.4", "18.1"}},
		Operations: []Operation{{Op: "db_dump_restore", Service: "postgres", Engine: "oracle", ToTag: "18.1"}}}}}
	Validate(mysql, outline())
	if !strings.Contains(reasons(mysql), "no export and reimport method") {
		t.Fatal(reasons(mysql))
	}
}

func gitlab() *inventory.Stack {
	return &inventory.Stack{Name: "gitlab", Services: []*inventory.Service{{Name: "gitlab", Role: inventory.RoleApplication, Image: "docker.io/gitlab/gitlab-ce", Version: "16.1.6-ce.0",
		Available: rel("16.1.8-ce.0", "16.3.8-ce.0", "16.3.9-ce.0", "16.7.10-ce.0", "16.8.1-ce.0", "16.11.10-ce.0", "17.0.1-ce.0")}}}
}

func gitlabStep(from, to string) Step {
	return Step{Risk: "attention", Reversible: "conditional", Changes: []Change{{"gitlab", from, to}},
		Operations: []Operation{{Op: "set_image", Service: "gitlab", Image: "gitlab/gitlab-ce", Tag: to}, {Op: "recreate", Service: "gitlab"}},
		Gate:       &Operation{Op: "exec_check", Service: "gitlab", CheckRef: "background_migrations"}}
}

func TestStopsAndGates(t *testing.T) {
	direct := &Plan{Steps: []Step{gitlabStep("16.1.6-ce.0", "17.0.1-ce.0")}}
	Validate(direct, gitlab())
	for _, want := range []string{"required stop 16.3 (16.3.9-ce.0", "16.7 (16.7.10-ce.0", "16.11 (16.11.10-ce.0"} {
		if !strings.Contains(reasons(direct), want) {
			t.Errorf("direct jump: %q missing in\n%s", want, reasons(direct))
		}
	}
	path := &Plan{Steps: []Step{gitlabStep("16.1.6-ce.0", "16.3.9-ce.0"), gitlabStep("16.3.9-ce.0", "16.7.10-ce.0"), gitlabStep("16.7.10-ce.0", "16.11.10-ce.0"), gitlabStep("16.11.10-ce.0", "17.0.1-ce.0")}}
	Validate(path, gitlab())
	if len(path.Incomplete) > 0 {
		t.Fatal(reasons(path))
	}
	ungated := &Plan{Steps: []Step{gitlabStep("16.1.6-ce.0", "16.3.9-ce.0")}}
	ungated.Steps[0].Gate = &Operation{Op: "none"}
	Validate(ungated, gitlab())
	if !strings.Contains(reasons(ungated), "background migrations") {
		t.Fatal(reasons(ungated))
	}
}

func TestNormalize(t *testing.T) {
	p := &Plan{Steps: []Step{step(Change{"redis", "", "7.2.16"})}}
	Validate(p, outline())
	if len(p.Incomplete) > 0 || p.Steps[0].Changes[0].From != "7.2.4" {
		t.Fatalf("%v %+v", p.Incomplete, p.Steps[0].Changes[0])
	}
}

func TestFallbackDatabaseMajor(t *testing.T) {
	st := &inventory.Stack{Services: []*inventory.Service{{Name: "db", Role: inventory.RoleApplication, Image: "docker.io/library/mariadb", Version: "10.11.9", Available: rel("10.11.14", "11.8.3")}}}
	p := Fallback(st, "x")
	Validate(p, st)
	if len(p.Incomplete) > 0 || p.Steps[0].Operations[0].Op != "db_dump_restore" || p.Steps[0].Operations[0].Engine != "mariadb" {
		t.Fatalf("%v %+v", p.Incomplete, p.Steps[0].Operations)
	}
}

func TestAutomaticPerService(t *testing.T) {
	two := func(auto map[string]string) *Plan {
		return &Plan{Auto: auto, Steps: []Step{step(Change{"outline", "1.10.1", "1.11.0"}), step(Change{"redis", "7.2.4", "7.2.16"})}}
	}
	for _, c := range []struct {
		auto     map[string]string
		global   string
		want     bool
		mentions string
	}{
		{nil, "all", true, ""},
		{map[string]string{"outline": "patch"}, "all", false, "palier.auto of outline=patch"},
		{map[string]string{"redis": "off"}, "all", false, "palier.auto of redis=off"},
		{nil, "off", false, "PALIER_AUTO=off"},
		{map[string]string{"outline": "all", "redis": "patch"}, "off", true, ""},
		{map[string]string{"outline": "minor"}, "off", false, "PALIER_AUTO=off"},
	} {
		ok, why := Automatic(two(c.auto), c.global)
		if ok != c.want || !strings.Contains(why, c.mentions) {
			t.Errorf("auto %v, global %s: %v %q", c.auto, c.global, ok, why)
		}
	}
	if ok, _ := Automatic(&Plan{Steps: []Step{{Risk: "unknown", Reversible: "unknown"}}}, "off"); ok {
		t.Error("a step without change escaped PALIER_AUTO=off")
	}
}

func TestAutoLabel(t *testing.T) {
	st := &inventory.Stack{Services: []*inventory.Service{{Name: "outline", Auto: "patch"}, {Name: "redis"}, {Name: "postgres", Auto: "major"}}}
	p := &Plan{}
	Validate(p, st)
	if len(p.Auto) != 1 || p.Auto["outline"] != "patch" || !strings.Contains(strings.Join(p.Incomplete, ";"), `palier.auto of postgres: "major"`) {
		t.Errorf("auto %v, incomplete %q", p.Auto, p.Incomplete)
	}
}

func TestQuoted(t *testing.T) {
	body := "### Patches\r\n\r\nauthentik 2026.8.2, 2026.5.7 and  2026.2.7 fix this issue.\r\n"
	for quote, want := range map[string]bool{
		"authentik 2026.8.2, 2026.5.7 and 2026.2.7 fix this issue.": true,
		"### Patches\nauthentik 2026.8.2":                           true,
		"authentik 2026.8.2 and 2026.2.7 fix this issue.":           false,
		"authentik 2026.8.3, 2026.5.7 and 2026.2.7 fix this issue.": false,
	} {
		if Quoted(body, quote) != want {
			t.Errorf("Quoted(%q) != %v", quote, want)
		}
	}
}

func TestEngineOfTaggedDependency(t *testing.T) {
	immich := func(min string) (*Plan, *inventory.Stack) {
		st := &inventory.Stack{Services: []*inventory.Service{
			{Name: "immich-server", Role: inventory.RoleApplication, Image: "ghcr.io/immich-app/immich-server", Version: "v1.105.1", Available: rel("v1.106.4"),
				Notes: []notes.Note{{URL: "https://example/docs", Body: "Immich is known to work with Postgres versions >= 14, < 20."}}},
			{Name: "database", Role: inventory.RoleDependency, Image: "docker.io/tensorchord/pgvecto-rs", Version: "pg14-v0.2.0"},
		}}
		s := step(Change{"immich-server", "v1.105.1", "v1.106.4"})
		s.Facts = []Fact{{Category: "supported_dependency", Service: "immich-server", Dependency: "database", MinVersion: min, MaxVersion: "19", Quote: "Immich is known to work with Postgres versions >= 14, < 20."}}
		return &Plan{Steps: []Step{s}}, st
	}
	p, st := immich("14")
	Validate(p, st)
	if strings.Contains(reasons(p), "database") {
		t.Errorf("pg14-v0.2.0 read as PostgreSQL 14: %s", reasons(p))
	}
	p, st = immich("16")
	Validate(p, st)
	if !strings.Contains(reasons(p), "requires database 16 at least, the plan leaves it at pg14-v0.2.0") {
		t.Errorf("PostgreSQL 16 required: %s", reasons(p))
	}
}

func TestPostgresTaggedImage(t *testing.T) {
	if EngineOf("docker.io/tensorchord/pgvecto-rs", "pg14-v0.2.0") != "postgres" || EngineOf("docker.io/library/redis", "7.4.2") != "" {
		t.Error("engine of a tag naming PostgreSQL")
	}
	db := &inventory.Service{Name: "database", Image: "docker.io/tensorchord/pgvecto-rs", Tag: "pg14-v0.2.0"}
	for to, major := range map[string]bool{"14-vectorchord0.3.0-pgvectors0.2.0": false, "pg14-v0.4.0": false, "pg16-v0.3.0": true} {
		p := &Plan{}
		checkDatabase(p, 1, Change{"database", "pg14-v0.2.0", to}, db, &Step{}, &inventory.Stack{Services: []*inventory.Service{db}})
		if got := len(p.Incomplete) > 0; got != major {
			t.Errorf("pg14-v0.2.0 to %s: dump and restore required %v, want %v", to, got, major)
		}
	}
}

func TestPostgresTaggedLine(t *testing.T) {
	for from, to := range map[[2]string]string{{"pg14-v0.2.0", "pg16-v0.3.0"}: "major", {"pg14-v0.2.0", "pg14-v0.4.0"}: "minor", {"pg14-v0.2.0", "pg14-v0.2.1"}: "patch", {"pg14-v0.2.0", "14-vectorchord0.3.0-pgvectors0.2.0"}: "minor"} {
		if got := Level(from[0], from[1]); got != to {
			t.Errorf("Level(%s, %s) = %s, want %s", from[0], from[1], got, to)
		}
	}
	db := func(role string) *inventory.Stack {
		return &inventory.Stack{Services: []*inventory.Service{{Name: "db", Role: role, Image: "docker.io/tensorchord/pgvecto-rs", Tag: "pg14-v0.2.0", Version: "pg14-v0.2.0", Available: rel("pg14-v0.4.0", "pg16-v0.3.0")}}}
	}
	held := Fallback(db(inventory.RoleDependency), "x")
	Validate(held, db(inventory.RoleDependency))
	if c := held.Steps[0].Changes[0]; c.To != "pg14-v0.4.0" || !strings.Contains(strings.Join(held.Withheld, ";"), "db pg16-v0.3.0 exists but is not offered") || !strings.Contains(strings.Join(held.Withheld, ";"), "stays on 14") {
		t.Errorf("dependency: %+v, withheld %q", c, held.Withheld)
	}
	up := Fallback(db(inventory.RoleApplication), "x")
	Validate(up, db(inventory.RoleApplication))
	if op := up.Steps[0].Operations[0]; op.Op != "db_dump_restore" || op.ToTag != "pg16-v0.3.0" || op.Engine != "postgres" || len(up.Incomplete) > 0 {
		t.Errorf("application: %+v, incomplete %q", up.Steps[0].Operations, up.Incomplete)
	}
}

func TestBlamed(t *testing.T) {
	steps := Blamed(&Plan{Incomplete: []string{"step 3: no recreate after the set_image of db", "step 3: x", "step 5: y"}})
	if len(steps) != 2 || steps[0] != 2 || steps[1] != 4 {
		t.Errorf("blamed: %v", steps)
	}
	if steps := Blamed(&Plan{Incomplete: []string{"step 3: x", "The target cannot be reached"}}); steps != nil {
		t.Errorf("a reason without a step: %v", steps)
	}
}

func TestWithheldNotForMovedImage(t *testing.T) {
	st := &inventory.Stack{Services: []*inventory.Service{
		{Name: "app", Role: inventory.RoleApplication, Image: "docker.io/example/app", Version: "1.0.0",
			Notes: []notes.Note{{URL: "https://example/n", Body: "The database image is now ghcr.io/immich-app/postgres:14-vectorchord0.3.0-pgvectors0.2.0 for everyone."}}},
		{Name: "database", Role: inventory.RoleDependency, Image: "docker.io/tensorchord/pgvecto-rs", Tag: "pg14-v0.2.0", Version: "pg14-v0.2.0", Available: rel("pg16-v0.4.0")},
	}}
	stay := &Plan{}
	Validate(stay, st)
	if !strings.Contains(strings.Join(stay.Withheld, ";"), "pg16-v0.4.0") {
		t.Errorf("held on its image: %q", stay.Withheld)
	}
	move := &Plan{Steps: []Step{{Risk: "safe", Reversible: "yes", Changes: []Change{{"database", "pg14-v0.2.0", "14-vectorchord0.3.0-pgvectors0.2.0"}},
		Operations: []Operation{{Op: "set_image", Service: "database", Image: "ghcr.io/immich-app/postgres", Tag: "14-vectorchord0.3.0-pgvectors0.2.0"}, {Op: "recreate", Service: "database"}}}}}
	Validate(move, st)
	if len(move.Withheld) != 0 {
		t.Errorf("moved to another image: %q", move.Withheld)
	}
}
