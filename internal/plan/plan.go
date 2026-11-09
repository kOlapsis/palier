// Package plan holds the update plan of a stack and checks it against the stack and its release notes.
package plan

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kolapsis/palier/internal/catalog"
	"github.com/kolapsis/palier/internal/inventory"
	"github.com/kolapsis/palier/internal/notes"
	"github.com/kolapsis/palier/internal/registry"
	"github.com/kolapsis/palier/internal/version"
)

// Plan is an ordered list of steps; a plan with reasons in Incomplete is never applied.
type Plan struct {
	Steps      []Step            `json:"steps"`
	Incomplete []string          `json:"incomplete"`
	Withheld   []string          `json:"withheld,omitempty"`
	Suspicious []string          `json:"suspicious,omitempty"`
	Auto       map[string]string `json:"auto,omitempty"`
}

// Step changes some services and passes when its gate and verification pass.
type Step struct {
	Changes    []Change    `json:"changes"`
	StopDuring []string    `json:"stop_during"`
	Operations []Operation `json:"operations"`
	Gate       *Operation  `json:"gate"`
	Risk       string      `json:"risk"`
	Reversible string      `json:"reversible"`
	Facts      []Fact      `json:"facts"`
	Why        string      `json:"why"`
}

// Change moves one service from one version to another.
type Change struct {
	Service string `json:"service"`
	From    string `json:"from"`
	To      string `json:"to"`
}

// Operation is one entry of the closed vocabulary, with the parameters it uses.
type Operation struct {
	Op       string   `json:"op"`
	Service  string   `json:"service,omitempty"`
	Services []string `json:"services,omitempty"`
	Image    string   `json:"image,omitempty"`
	Tag      string   `json:"tag,omitempty"`
	Seconds  int      `json:"seconds,omitempty"`
	CheckRef string   `json:"check_ref,omitempty"`
	Engine   string   `json:"engine,omitempty"`
	ToImage  string   `json:"to_image,omitempty"`
	ToTag    string   `json:"to_tag,omitempty"`
	Edits    []Edit   `json:"edits,omitempty"`
}

// Targets returns the services an operation names, through service or services.
func (o Operation) Targets() []string {
	if o.Service == "" {
		return o.Services
	}
	return append([]string{o.Service}, o.Services...)
}

// Edit is one structured change of a Compose service.
type Edit struct {
	Action  string `json:"action"`
	Name    string `json:"name,omitempty"`
	Value   string `json:"value,omitempty"`
	NewName string `json:"new_name,omitempty"`
	Source  string `json:"source,omitempty"`
}

// Fact is a statement of the release notes, with its exact quote.
type Fact struct {
	Category      string `json:"category"`
	Service       string `json:"service"`
	Version       string `json:"version"`
	Quote         string `json:"quote"`
	SourceURL     string `json:"source_url"`
	Dependency    string `json:"dependency,omitempty"`
	Image         string `json:"image,omitempty"`
	MinVersion    string `json:"min_version,omitempty"`
	MaxVersion    string `json:"max_version,omitempty"`
	When          string `json:"when,omitempty"`
	Scope         string `json:"scope,omitempty"`
	Reversibility string `json:"reversibility,omitempty"`
	FromVersion   string `json:"from_version,omitempty"`
	ToVersion     string `json:"to_version,omitempty"`
}

// Record is a plan as stored for a stack.
type Record struct {
	Stack     string    `json:"stack"`
	Source    string    `json:"source"`
	Created   time.Time `json:"created"`
	InputHash string    `json:"input_hash"`
	Plan      *Plan     `json:"plan"`
}

// Vocabulary of operations, risks, reversibility and fact categories.
var (
	Ops         = []string{"set_image", "compose_edit", "recreate", "stop", "start", "wait_stable", "exec_check", "db_dump_restore"}
	implemented = []string{"set_image", "compose_edit", "recreate", "stop", "start", "wait_stable", "db_dump_restore", "exec_check"}
	Risks       = []string{"safe", "attention", "blocking", "unknown"}
	Reversibles = []string{"yes", "conditional", "no", "unknown"}
	EditActions = []string{"set_env", "unset_env", "rename_env", "set_command", "unset_command", "set_healthcheck", "unset_healthcheck", "add_volume", "remove_volume", "remove_service"}
	Categories  = []string{"breaking_change", "manual_action", "configuration_change", "data_migration", "security_fix", "deprecation", "mandatory_step", "min_dependency", "supported_dependency", "storage_format_change"}
	Engines     = []string{"postgres", "mysql", "mariadb"}
)

func (p *Plan) incomplete(format string, args ...any) {
	p.Incomplete = append(p.Incomplete, fmt.Sprintf(format, args...))
}

// Validate checks p against the stack and the notes of its services, drops the facts whose quote is not in the
// notes, and records every reason that makes the plan incomplete.
func Validate(p *Plan, st *inventory.Stack) {
	services := map[string]*inventory.Service{}
	current := map[string]string{}
	for _, s := range st.Services {
		services[s.Name] = s
		current[s.Name] = running(s)
	}
	normalize(p, current)
	for i := range p.Steps {
		s := &p.Steps[i]
		n := i + 1
		if !slices.Contains(Risks, s.Risk) {
			p.incomplete("step %d: unknown risk level %q", n, s.Risk)
		}
		if !slices.Contains(Reversibles, s.Reversible) {
			p.incomplete("step %d: unknown reversibility %q", n, s.Reversible)
		}
		for _, name := range s.StopDuring {
			if services[name] == nil {
				p.incomplete("step %d: the service %s to stop does not exist", n, name)
			}
		}
		for j, op := range s.Operations {
			validateOp(p, n, op, services)
			if op.Op == "compose_edit" {
				checkFreeValues(p, n, &s.Operations[j], st)
			}
		}
		checkGate(p, n, s, services)
		seen := map[string]bool{}
		for _, c := range s.Changes {
			svc := services[c.Service]
			if svc == nil {
				p.incomplete("step %d: service %s does not exist", n, c.Service)
				continue
			}
			if seen[c.Service] {
				p.incomplete("step %d: %s changes twice; one version per service per step", n, c.Service)
			}
			seen[c.Service] = true
			if c.From != current[c.Service] {
				p.incomplete("step %d: %s starts from %s but will be at %s", n, c.Service, c.From, current[c.Service])
			}
			if !slices.ContainsFunc(svc.Available, func(r inventory.Release) bool { return r.Tag == c.To }) && !citedImageChange(p, s, c, st) {
				p.incomplete("step %d: %s %s is not a version available in the registry", n, c.Service, c.To)
			}
			checkChangeOps(p, n, s, c, svc, st)
			checkDatabase(p, n, c, svc, s)
			current[c.Service] = c.To
		}
		s.Facts = checkFacts(p, n, s.Facts, st)
	}
	current = map[string]string{}
	for _, s := range st.Services {
		current[s.Name] = running(s)
	}
	for i, s := range p.Steps {
		for _, c := range s.Changes {
			current[c.Service] = c.To
		}
		checkIntervals(p, i+1, s.Changes, current, services)
	}
	p.Withheld = withheld(p, st, current)
	for _, svc := range st.Services {
		checkStops(p, svc)
	}
	checkSuspicious(p, st)
	p.Auto = nil
	for _, svc := range st.Services {
		switch svc.Auto {
		case "":
		case "off", "patch", "minor", "all":
			if p.Auto == nil {
				p.Auto = map[string]string{}
			}
			p.Auto[svc.Name] = svc.Auto
		default:
			p.incomplete("label palier.auto of %s: %q, expected all, minor, patch or off", svc.Name, svc.Auto)
		}
	}
}

// checkGate requires the background check of the card after each step that changes a service whose card has one.
func checkGate(p *Plan, n int, s *Step, services map[string]*inventory.Service) {
	gated := s.Gate != nil && s.Gate.Op != "" && s.Gate.Op != "none"
	if gated {
		svc := services[s.Gate.Service]
		switch {
		case s.Gate.Op != "exec_check":
			p.incomplete("step %d: unknown gate condition %s", n, s.Gate.Op)
		case svc == nil:
			p.incomplete("step %d: the gate targets %s, which does not exist", n, s.Gate.Service)
		case catalog.For(svc.Image) == nil || catalog.For(svc.Image).Checks[s.Gate.CheckRef] == nil:
			p.incomplete("step %d: the condition %s is not a command of the card of %s", n, s.Gate.CheckRef, s.Gate.Service)
		}
	}
	for _, c := range s.Changes {
		svc := services[c.Service]
		if svc == nil {
			continue
		}
		if card := catalog.For(svc.Image); card != nil && card.Checks["background_migrations"] != nil && (!gated || s.Gate.Service != c.Service) {
			p.incomplete("step %d: %s must wait for its background migrations to finish (exec_check background_migrations)", n, c.Service)
		}
	}
}

// checkStops requires every stop of the card between the running version and the last target, at the latest patch of its minor.
func checkStops(p *Plan, svc *inventory.Service) {
	card := catalog.For(svc.Image)
	if card == nil || len(card.Stops) == 0 {
		return
	}
	var targets []string
	for _, s := range p.Steps {
		for _, c := range s.Changes {
			if c.Service == svc.Name {
				targets = append(targets, c.To)
			}
		}
	}
	if len(targets) == 0 {
		return
	}
	from, _ := version.Parse(svc.Version)
	last, _ := version.Parse(targets[len(targets)-1])
	for _, stop := range card.Stops {
		minor, ok := version.Parse(stop)
		if !ok || len(minor.Nums) < 2 || len(from.Nums) < 2 || len(last.Nums) < 2 {
			continue
		}
		after := compareNums(minor.Nums[:2], from.Nums[:2]) > 0
		before := compareNums(minor.Nums[:2], last.Nums[:2]) < 0
		if !after || !before {
			continue
		}
		highest := ""
		for _, r := range svc.Available {
			if v, ok := version.Parse(r.Tag); ok && len(v.Nums) >= 2 && compareNums(v.Nums[:2], minor.Nums[:2]) == 0 {
				highest = r.Tag
			}
		}
		if highest == "" {
			continue
		}
		if !slices.Contains(targets, highest) {
			p.incomplete("%s must go through the required stop %s (%s, latest patch of %s)", svc.Name, stop, highest, stop)
		}
	}
}

func compareNums(a, b []int) int {
	for i := range min(len(a), len(b)) {
		if a[i] != b[i] {
			return a[i] - b[i]
		}
	}
	return len(a) - len(b)
}

// withheld says, for each dependency, which newer majors the plan leaves out and why.
func withheld(p *Plan, st *inventory.Stack, final map[string]string) []string {
	var out []string
	for _, s := range st.Services {
		if s.Role != inventory.RoleDependency {
			continue
		}
		reached, ok := version.Parse(final[s.Name])
		if !ok {
			continue
		}
		highest := map[int]string{}
		var majors []int
		for _, r := range s.Available {
			if v, ok := version.Parse(r.Tag); ok && v.Nums[0] > reached.Nums[0] {
				if _, seen := highest[v.Nums[0]]; !seen {
					majors = append(majors, v.Nums[0])
				}
				highest[v.Nums[0]] = r.Tag
			}
		}
		if len(majors) == 0 {
			continue
		}
		var tags []string
		for _, m := range majors {
			tags = append(tags, highest[m])
		}
		why := fmt.Sprintf("no release note of %s cites a version of %s", clients(st, s.Name), s.Name)
		for _, step := range p.Steps {
			for _, f := range step.Facts {
				if f.Category == "supported_dependency" && f.Dependency == s.Name && f.MaxVersion != "" {
					why = fmt.Sprintf("%s %s supports %s up to %s", f.Service, f.Version, s.Name, f.MaxVersion)
				}
			}
		}
		verb := "exists but is not offered"
		if len(tags) > 1 {
			verb = "exist but are not offered"
		}
		out = append(out, fmt.Sprintf("%s %s %s: %s; %s stays on %d.", s.Name, strings.Join(tags, ", "), verb, why, s.Name, reached.Nums[0]))
	}
	return out
}

func clients(st *inventory.Stack, dependency string) string {
	var names []string
	for _, s := range st.Services {
		if slices.Contains(s.DependsOn, dependency) {
			names = append(names, s.Name)
		}
	}
	return strings.Join(names, ", ")
}

// compareTo compares v with bound on the components bound gives: "17.2" is within a bound "17".
func compareTo(v, bound string) (int, bool) {
	a, ok1 := version.Parse(v)
	b, ok2 := version.Parse(bound)
	if !ok1 || !ok2 {
		return 0, false
	}
	for i, n := range b.Nums {
		if i >= len(a.Nums) {
			return -1, true
		}
		if a.Nums[i] != n {
			return a.Nums[i] - n, true
		}
	}
	return 0, true
}

// checkIntervals checks that after step n each dependency stays in the range its client supports at the client's version of that step.
func checkIntervals(p *Plan, n int, changes []Change, current map[string]string, services map[string]*inventory.Service) {
	latest := map[[3]string]Fact{}
	for _, s := range p.Steps {
		for _, f := range s.Facts {
			if f.Category != "supported_dependency" && f.Category != "min_dependency" || services[f.Dependency] == nil {
				continue
			}
			if c, ok := compareTo(current[f.Service], f.Version); f.Version != "" && (!ok || c < 0) {
				continue
			}
			key := [3]string{f.Service, f.Dependency, f.Category}
			if prev, seen := latest[key]; !seen || prev.Version == "" || notes.Less(mustParse(prev.Version), mustParse(f.Version)) {
				latest[key] = f
			}
		}
	}
	for _, c := range changes {
		svc := services[c.Service]
		if svc == nil || svc.Role != inventory.RoleDependency || Level(c.From, c.To) != "major" {
			continue
		}
		cited := false
		for k := range latest {
			cited = cited || k[1] == c.Service
		}
		if !cited {
			p.incomplete("step %d: %s is a dependency; its major does not change without a quoted fact from the application that supports it at its version of this step", n, c.Service)
		}
	}
	for _, f := range latest {
		dep := current[f.Dependency]
		if c, ok := compareTo(dep, f.MinVersion); f.MinVersion != "" && ok && c < 0 {
			p.incomplete("step %d: %s %s requires %s %s at least, the plan leaves it at %s", n, f.Service, current[f.Service], f.Dependency, f.MinVersion, dep)
		}
		if c, ok := compareTo(dep, f.MaxVersion); f.MaxVersion != "" && ok && c > 0 {
			p.incomplete("step %d: %s %s supports %s up to %s, the plan moves it to %s", n, f.Service, current[f.Service], f.Dependency, f.MaxVersion, dep)
		}
	}
}

func mustParse(v string) version.V {
	parsed, _ := version.Parse(v)
	return parsed
}

func validateOp(p *Plan, n int, op Operation, services map[string]*inventory.Service) {
	if !slices.Contains(Ops, op.Op) {
		p.incomplete("step %d: operation %q is outside the vocabulary", n, op.Op)
		return
	}
	if !slices.Contains(implemented, op.Op) {
		p.incomplete("step %d: operation %s is not supported yet", n, op.Op)
	}
	names := op.Targets()
	if len(names) == 0 {
		p.incomplete("step %d: operation %s names no service", n, op.Op)
	}
	for _, name := range names {
		if services[name] == nil {
			p.incomplete("step %d: operation %s targets service %s, which does not exist", n, op.Op, name)
		}
	}
	if op.Op == "compose_edit" {
		if len(op.Edits) == 0 {
			p.incomplete("step %d: compose_edit without an edit", n)
		}
		for _, e := range op.Edits {
			switch {
			case !slices.Contains(EditActions, e.Action):
				p.incomplete("step %d: edit %q is outside the vocabulary", n, e.Action)
			case (e.Action == "set_env" || e.Action == "unset_env" || e.Action == "rename_env") && e.Name == "",
				e.Action == "rename_env" && e.NewName == "",
				(e.Action == "set_command" || e.Action == "set_healthcheck" || e.Action == "add_volume" || e.Action == "remove_volume") && e.Value == "":
				p.incomplete("step %d: edit %s of %s is incomplete", n, e.Action, op.Service)
			}
		}
	}
	if op.Op == "set_image" && (op.Image == "" || op.Tag == "") {
		p.incomplete("step %d: set_image without image or tag", n)
	}
	if op.Op == "db_dump_restore" {
		switch {
		case !slices.Contains(Engines, op.Engine):
			p.incomplete("step %d: no export and reimport method for engine %q", n, op.Engine)
		case op.ToTag == "":
			p.incomplete("step %d: db_dump_restore without a target version", n)
		case services[op.Service] != nil && EngineOf(services[op.Service].Image) != op.Engine:
			p.incomplete("step %d: db_dump_restore %s on %s, whose image is not a %s database", n, op.Engine, op.Service, op.Engine)
		}
	}
	if svc := services[op.Service]; op.Op == "exec_check" && (svc == nil || catalog.For(svc.Image) == nil || catalog.For(svc.Image).Checks[op.CheckRef] == nil) {
		p.incomplete("step %d: exec_check %s is not a command of the card of %s", n, op.CheckRef, op.Service)
	}
	if op.Op == "wait_stable" && op.Seconds <= 0 {
		p.incomplete("step %d: wait_stable without a duration", n)
	}
}

// normalize fills what the model leaves out but the plan already says: an empty "from" is the version the service
// has at that step, and a db_dump_restore without to_tag takes its tag.
func normalize(p *Plan, start map[string]string) {
	current := maps.Clone(start)
	for i := range p.Steps {
		s := &p.Steps[i]
		for j := range s.Changes {
			if s.Changes[j].From == "" {
				s.Changes[j].From = current[s.Changes[j].Service]
			}
			current[s.Changes[j].Service] = s.Changes[j].To
		}
		for j := range s.Operations {
			if op := &s.Operations[j]; op.Op == "db_dump_restore" && op.ToTag == "" {
				op.ToTag = op.Tag
			}
		}
	}
}

// running is the version a plan starts from: the version read for the service, else its tag.
func running(s *inventory.Service) string {
	if s.Version != "" {
		return s.Version
	}
	return s.Tag
}

// citedImageChange reports whether the set_image of c moves the service to another image that its client names in a
// quoted fact, with a target tag written in the notes.
func citedImageChange(p *Plan, s *Step, c Change, st *inventory.Stack) bool {
	i := slices.IndexFunc(s.Operations, func(o Operation) bool { return o.Op == "set_image" && o.Service == c.Service })
	svc := service(st, c.Service)
	if i < 0 || svc == nil || registry.ParseRef(s.Operations[i].Image).Name() == svc.Image {
		return false
	}
	target := registry.ParseRef(s.Operations[i].Image).Name()
	cited := false
	for _, step := range p.Steps {
		for _, f := range step.Facts {
			cited = cited || f.Category == "supported_dependency" && f.Dependency == c.Service && f.Image != "" && registry.ParseRef(f.Image).Name() == target
		}
	}
	inNotes := false
	for _, other := range st.Services {
		for _, n := range other.Notes {
			inNotes = inNotes || strings.Contains(n.Body, c.To)
		}
	}
	return cited && inNotes
}

func service(st *inventory.Stack, name string) *inventory.Service {
	for _, s := range st.Services {
		if s.Name == name {
			return s
		}
	}
	return nil
}

func checkChangeOps(p *Plan, n int, s *Step, c Change, svc *inventory.Service, st *inventory.Stack) {
	set := slices.IndexFunc(s.Operations, func(o Operation) bool { return o.Op == "set_image" && o.Service == c.Service })
	dump := slices.ContainsFunc(s.Operations, func(o Operation) bool { return o.Op == "db_dump_restore" && o.Service == c.Service })
	if dump && set >= 0 {
		p.incomplete("step %d: set_image of %s in addition to db_dump_restore, which already changes the image", n, c.Service)
		return
	}
	if set < 0 {
		if !slices.ContainsFunc(s.Operations, func(o Operation) bool { return o.Op == "db_dump_restore" && o.Service == c.Service && o.ToTag == c.To }) {
			p.incomplete("step %d: no set_image or db_dump_restore moves %s to %s", n, c.Service, c.To)
		}
		return
	}
	op := s.Operations[set]
	if op.Tag != c.To {
		p.incomplete("step %d: set_image gives tag %s to %s, the change says %s", n, op.Tag, c.Service, c.To)
	}
	if registry.ParseRef(op.Image).Name() != svc.Image && !citedImageChange(p, s, c, st) {
		p.incomplete("step %d: set_image changes the image of %s (%s instead of %s) without a quoted fact from the application that requires this image", n, c.Service, op.Image, svc.Image)
	}
	if !slices.ContainsFunc(s.Operations[set:], func(o Operation) bool { return o.Op == "recreate" && slices.Contains(o.Targets(), c.Service) }) {
		p.incomplete("step %d: no recreate after the set_image of %s", n, c.Service)
	}
}

// EngineOf returns the database engine of an image, or "" when it is not a known database.
func EngineOf(image string) string {
	name := image[strings.LastIndex(image, "/")+1:]
	for _, db := range Engines {
		if strings.HasPrefix(name, db) {
			return db
		}
	}
	return ""
}

func checkDatabase(p *Plan, n int, c Change, svc *inventory.Service, s *Step) {
	if Level(c.From, c.To) != "major" {
		return
	}
	if db := EngineOf(svc.Image); db != "" && !slices.ContainsFunc(s.Operations, func(o Operation) bool { return o.Op == "db_dump_restore" && o.Service == c.Service }) {
		p.incomplete("step %d: %s changes major (%s to %s) without db_dump_restore, which is required for %s", n, c.Service, c.From, c.To, db)
	}
}

// quoted tells whether quote appears word for word in body, a run of spaces and line breaks counting as one space.
func quoted(body, quote string) bool {
	return strings.Contains(body, quote) || strings.Contains(strings.Join(strings.Fields(body), " "), strings.Join(strings.Fields(quote), " "))
}

func checkFacts(p *Plan, n int, facts []Fact, st *inventory.Stack) []Fact {
	var kept []Fact
	for _, f := range facts {
		length := utf8.RuneCountInString(f.Quote)
		if !slices.Contains(Categories, f.Category) || length < 20 || length > 1000 {
			p.incomplete("step %d: fact %s dropped (unknown category or quote outside 20 to 1000 characters)", n, f.Category)
			continue
		}
		url, tag := "", ""
		for _, s := range st.Services {
			for _, note := range s.Notes {
				if url == "" && quoted(note.Body, f.Quote) {
					url, tag = note.URL, note.Tag
				}
			}
		}
		if url == "" {
			p.incomplete("step %d: fact %s dropped, its quote does not appear word for word in the notes", n, f.Category)
			continue
		}
		f.SourceURL = url
		if f.Version == "" {
			f.Version = tag
		}
		kept = append(kept, f)
	}
	return kept
}

// Fallback is the plan without analysis: applications to their highest version, dependencies inside their major.
func Fallback(st *inventory.Stack, why string) *Plan {
	step := Step{Risk: "unknown", Reversible: "unknown", Why: why}
	var changed []string
	for _, s := range st.Services {
		target := ""
		cur, _ := version.Parse(s.Version)
		for _, r := range s.Available {
			v, _ := version.Parse(r.Tag)
			if s.Role != inventory.RoleDependency || v.Nums[0] == cur.Nums[0] {
				target = r.Tag
			}
		}
		if target == "" {
			continue
		}
		step.Changes = append(step.Changes, Change{Service: s.Name, From: s.Version, To: target})
		if db := EngineOf(s.Image); db != "" && Level(s.Version, target) == "major" {
			step.Operations = append(step.Operations, Operation{Op: "db_dump_restore", Service: s.Name, Engine: db, ToImage: s.Image, ToTag: target})
			continue
		}
		step.Operations = append(step.Operations, Operation{Op: "set_image", Service: s.Name, Image: s.Image, Tag: target})
		changed = append(changed, s.Name)
	}
	if len(step.Changes) == 0 {
		return &Plan{}
	}
	if len(changed) > 0 {
		step.Operations = append(step.Operations, Operation{Op: "recreate", Services: changed})
	}
	return &Plan{Steps: []Step{step}}
}

// Level is "patch", "minor" or "major" for a change between two versions.
func Level(from, to string) string {
	a, ok1 := version.Parse(from)
	b, ok2 := version.Parse(to)
	switch {
	case !ok1 || !ok2 || a.Nums[0] != b.Nums[0]:
		return "major"
	case len(a.Nums) > 1 && len(b.Nums) > 1 && a.Nums[1] == b.Nums[1]:
		return "patch"
	}
	return "minor"
}

// Automatic reports whether PALIER_AUTO lets p be applied without a click, and why not otherwise.
func Automatic(p *Plan, auto string) (bool, string) {
	switch {
	case len(p.Suspicious) > 0:
		return false, "the release notes contain text that seems written to steer an AI"
	case len(p.Incomplete) > 0:
		return false, "incomplete plan"
	case len(p.Steps) == 0:
		return false, "nothing to apply"
	}
	for i, s := range p.Steps {
		if s.Risk == "blocking" {
			return false, fmt.Sprintf("step %d is blocking: it needs an action Palier cannot perform", i+1)
		}
		if s.Reversible == "no" {
			return false, fmt.Sprintf("step %d is irreversible, even by restoring the volumes", i+1)
		}
		if len(s.Changes) == 0 && auto == "off" {
			return false, "PALIER_AUTO=off"
		}
		for _, c := range s.Changes {
			level, source := auto, "PALIER_AUTO"
			if v := p.Auto[c.Service]; v != "" {
				level, source = v, "palier.auto of "+c.Service
			}
			switch l := Level(c.From, c.To); {
			case level == "off":
				return false, source + "=off"
			case level == "minor" && l == "major" || level == "patch" && l != "patch":
				return false, fmt.Sprintf("%s %s to %s is a %s version, beyond %s=%s", c.Service, c.From, c.To, l, source, level)
			}
		}
	}
	return true, ""
}
