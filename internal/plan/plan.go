// Package plan holds the update plan of a stack and checks it against the stack and its release notes.
package plan

import (
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kolapsis/palier/internal/inventory"
	"github.com/kolapsis/palier/internal/notes"
	"github.com/kolapsis/palier/internal/registry"
	"github.com/kolapsis/palier/internal/version"
)

// Plan is an ordered list of steps; a plan with reasons in Incomplete is never applied.
type Plan struct {
	Steps      []Step   `json:"steps"`
	Incomplete []string `json:"incomplete"`
	Withheld   []string `json:"withheld,omitempty"`
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
	implemented = []string{"set_image", "recreate", "stop", "start", "wait_stable"}
	Risks       = []string{"safe", "attention", "blocking", "unknown"}
	Reversibles = []string{"yes", "conditional", "no", "unknown"}
	Categories  = []string{"breaking_change", "manual_action", "configuration_change", "data_migration", "security_fix", "deprecation", "mandatory_step", "min_dependency", "supported_dependency", "storage_format_change"}
	databases   = []string{"postgres", "mysql", "mariadb"}
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
		current[s.Name] = s.Version
	}
	for i := range p.Steps {
		s := &p.Steps[i]
		n := i + 1
		if !slices.Contains(Risks, s.Risk) {
			p.incomplete("étape %d : niveau de risque %q inconnu", n, s.Risk)
		}
		if !slices.Contains(Reversibles, s.Reversible) {
			p.incomplete("étape %d : réversibilité %q inconnue", n, s.Reversible)
		}
		for _, name := range s.StopDuring {
			if services[name] == nil {
				p.incomplete("étape %d : le service %s à arrêter n'existe pas", n, name)
			}
		}
		for _, op := range s.Operations {
			validateOp(p, n, op, services)
		}
		if s.Gate != nil && s.Gate.Op != "" && s.Gate.Op != "none" {
			p.incomplete("étape %d : la condition de passage %s n'est pas encore prise en charge", n, s.Gate.Op)
		}
		seen := map[string]bool{}
		for _, c := range s.Changes {
			svc := services[c.Service]
			if svc == nil {
				p.incomplete("étape %d : le service %s n'existe pas", n, c.Service)
				continue
			}
			if seen[c.Service] {
				p.incomplete("étape %d : %s change deux fois ; un seul palier par service et par étape", n, c.Service)
			}
			seen[c.Service] = true
			if c.From != current[c.Service] {
				p.incomplete("étape %d : %s part de %s alors qu'il sera en %s", n, c.Service, c.From, current[c.Service])
			}
			if !slices.ContainsFunc(svc.Available, func(r inventory.Release) bool { return r.Tag == c.To }) {
				p.incomplete("étape %d : %s %s n'est pas une version disponible au registre", n, c.Service, c.To)
			}
			checkChangeOps(p, n, s, c, svc)
			checkDatabase(p, n, c, svc, s)
			current[c.Service] = c.To
		}
		s.Facts = checkFacts(p, n, s.Facts, st)
	}
	current = map[string]string{}
	for _, s := range st.Services {
		current[s.Name] = s.Version
	}
	for i, s := range p.Steps {
		for _, c := range s.Changes {
			current[c.Service] = c.To
		}
		checkIntervals(p, i+1, s.Changes, current, services)
	}
	p.Withheld = withheld(p, st, current)
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
		why := fmt.Sprintf("aucune note de %s ne cite de version de %s", clients(st, s.Name), s.Name)
		for _, step := range p.Steps {
			for _, f := range step.Facts {
				if f.Category == "supported_dependency" && f.Dependency == s.Name && f.MaxVersion != "" {
					why = fmt.Sprintf("%s %s supporte %s jusqu'à %s", f.Service, f.Version, s.Name, f.MaxVersion)
				}
			}
		}
		verb := "existe mais n'est pas proposé"
		if len(tags) > 1 {
			verb = "existent mais ne sont pas proposés"
		}
		out = append(out, fmt.Sprintf("%s %s %s : %s ; %s reste en %d.", s.Name, strings.Join(tags, ", "), verb, why, s.Name, reached.Nums[0]))
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
			p.incomplete("étape %d : %s est une dépendance ; sa majeure ne change pas sans un fait cité de l'application qui la supporte à sa version de cette étape", n, c.Service)
		}
	}
	for _, f := range latest {
		dep := current[f.Dependency]
		if c, ok := compareTo(dep, f.MinVersion); f.MinVersion != "" && ok && c < 0 {
			p.incomplete("étape %d : %s %s exige %s %s au moins, le plan le laisse en %s", n, f.Service, current[f.Service], f.Dependency, f.MinVersion, dep)
		}
		if c, ok := compareTo(dep, f.MaxVersion); f.MaxVersion != "" && ok && c > 0 {
			p.incomplete("étape %d : %s %s supporte %s jusqu'à %s, le plan le passe en %s", n, f.Service, current[f.Service], f.Dependency, f.MaxVersion, dep)
		}
	}
}

func mustParse(v string) version.V {
	parsed, _ := version.Parse(v)
	return parsed
}

func validateOp(p *Plan, n int, op Operation, services map[string]*inventory.Service) {
	if !slices.Contains(Ops, op.Op) {
		p.incomplete("étape %d : opération %q hors du vocabulaire", n, op.Op)
		return
	}
	if !slices.Contains(implemented, op.Op) {
		p.incomplete("étape %d : l'opération %s n'est pas encore prise en charge", n, op.Op)
	}
	names := op.Targets()
	if len(names) == 0 {
		p.incomplete("étape %d : l'opération %s ne nomme aucun service", n, op.Op)
	}
	for _, name := range names {
		if services[name] == nil {
			p.incomplete("étape %d : l'opération %s vise le service %s, qui n'existe pas", n, op.Op, name)
		}
	}
	if op.Op == "set_image" && (op.Image == "" || op.Tag == "") {
		p.incomplete("étape %d : set_image sans image ni tag", n)
	}
	if op.Op == "wait_stable" && op.Seconds <= 0 {
		p.incomplete("étape %d : wait_stable sans durée", n)
	}
}

func checkChangeOps(p *Plan, n int, s *Step, c Change, svc *inventory.Service) {
	set := slices.IndexFunc(s.Operations, func(o Operation) bool { return o.Op == "set_image" && o.Service == c.Service })
	if set < 0 {
		if !slices.ContainsFunc(s.Operations, func(o Operation) bool { return o.Op == "db_dump_restore" && o.Service == c.Service }) {
			p.incomplete("étape %d : aucun set_image ne passe %s en %s", n, c.Service, c.To)
		}
		return
	}
	op := s.Operations[set]
	if op.Tag != c.To {
		p.incomplete("étape %d : set_image donne le tag %s à %s, le changement annonce %s", n, op.Tag, c.Service, c.To)
	}
	if registry.ParseRef(op.Image).Name() != svc.Image {
		p.incomplete("étape %d : set_image change l'image de %s (%s au lieu de %s), ce qui n'est pas encore pris en charge", n, c.Service, op.Image, svc.Image)
	}
	if !slices.ContainsFunc(s.Operations[set:], func(o Operation) bool { return o.Op == "recreate" && slices.Contains(o.Targets(), c.Service) }) {
		p.incomplete("étape %d : aucun recreate après le set_image de %s", n, c.Service)
	}
}

func engine(image string) string {
	name := image[strings.LastIndex(image, "/")+1:]
	for _, db := range databases {
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
	if db := engine(svc.Image); db != "" && !slices.ContainsFunc(s.Operations, func(o Operation) bool { return o.Op == "db_dump_restore" && o.Service == c.Service }) {
		p.incomplete("étape %d : %s change de majeure (%s vers %s) sans db_dump_restore, obligatoire pour %s", n, c.Service, c.From, c.To, db)
	}
}

func checkFacts(p *Plan, n int, facts []Fact, st *inventory.Stack) []Fact {
	var kept []Fact
	for _, f := range facts {
		length := utf8.RuneCountInString(f.Quote)
		if !slices.Contains(Categories, f.Category) || length < 20 || length > 1000 {
			p.incomplete("étape %d : fait %s retiré (catégorie inconnue ou citation hors de 20 à 1000 caractères)", n, f.Category)
			continue
		}
		url, tag := "", ""
		for _, s := range st.Services {
			for _, note := range s.Notes {
				if url == "" && strings.Contains(note.Body, f.Quote) {
					url, tag = note.URL, note.Tag
				}
			}
		}
		if url == "" {
			p.incomplete("étape %d : fait %s retiré, sa citation ne figure pas mot pour mot dans les notes", n, f.Category)
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
		step.Operations = append(step.Operations, Operation{Op: "set_image", Service: s.Name, Image: s.Image, Tag: target})
		changed = append(changed, s.Name)
	}
	if len(changed) == 0 {
		return &Plan{}
	}
	step.Operations = append(step.Operations, Operation{Op: "recreate", Services: changed})
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
	case auto == "off":
		return false, "PALIER_AUTO=off"
	case len(p.Incomplete) > 0:
		return false, "plan incomplet"
	case len(p.Steps) == 0:
		return false, "rien à appliquer"
	}
	for i, s := range p.Steps {
		if s.Risk != "safe" {
			return false, fmt.Sprintf("étape %d de niveau %s", i+1, s.Risk)
		}
		if s.Reversible != "yes" {
			return false, fmt.Sprintf("étape %d de réversibilité %s", i+1, s.Reversible)
		}
		for _, c := range s.Changes {
			if l := Level(c.From, c.To); l == "major" || (auto == "patch" && l == "minor") {
				return false, fmt.Sprintf("%s %s vers %s est une version %s", c.Service, c.From, c.To, l)
			}
		}
	}
	return true, ""
}
