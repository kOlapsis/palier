package plan

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/kolapsis/palier/internal/catalog"
	"github.com/kolapsis/palier/internal/inventory"
	"github.com/kolapsis/palier/internal/notes"
	"github.com/kolapsis/palier/internal/version"
)

// SecurityFirst puts first, for each service an advisory touches, a step to the latest patch of its running line that the
// advisory no longer touches, when the first step of the plan for that service would leave it exposed. The services of
// the same release take that step with it, or none of them does.
func SecurityFirst(p *Plan, st *inventory.Stack) {
	var first []Step
	together := Together(st)
	done := map[string]bool{}
	for _, s := range st.Services {
		cur, ok := version.Parse(s.Version)
		if done[s.Name] || len(s.Advisories) == 0 || !ok || len(cur.Nums) < 2 {
			continue
		}
		exposed := func(v version.V) []notes.Advisory {
			return slices.DeleteFunc(slices.Clone(s.Advisories), func(a notes.Advisory) bool { return !a.Affects(v) })
		}
		step, change := firstChange(p, s.Name)
		if step < 0 {
			continue
		}
		to, ok := version.Parse(p.Steps[step].Changes[change].To)
		if !ok || len(exposed(to)) == 0 || sameMinor(cur, to) {
			continue
		}
		best, bestV := "", version.V{}
		for _, r := range s.Available {
			v, ok := version.Parse(r.Tag)
			if ok && sameMinor(cur, v) && cur.Less(v) && len(exposed(v)) < len(exposed(cur)) && (best == "" || bestV.Less(v)) {
				best, bestV = r.Tag, v
			}
		}
		if best == "" {
			continue
		}
		patch := Step{Gate: &Operation{Op: "none"}, Risk: "attention", Reversible: "yes"}
		var names []string
		for _, m := range append([]*inventory.Service{s}, together[s.Name]...) {
			tag := sameVersionTag(m, best)
			if i, _ := firstChange(p, m.Name); tag == "" || i < 0 {
				break
			}
			patch.Changes = append(patch.Changes, Change{Service: m.Name, From: m.Version, To: tag})
			patch.Operations = append(patch.Operations, Operation{Op: "set_image", Service: m.Name, Image: m.Image, Tag: tag})
			names = append(names, m.Name)
		}
		if len(names) < 1+len(together[s.Name]) {
			continue
		}
		patch.Operations = append(patch.Operations, Operation{Op: "recreate", Services: names})
		var closed []string
		for _, a := range s.Advisories {
			if a.Affects(bestV) {
				continue
			}
			closed = append(closed, cmp.Or(a.CVE, a.ID))
			for _, n := range s.Notes {
				if r := []rune(n.Body); n.URL == a.URL && len(r) >= 20 {
					patch.Facts = append(patch.Facts, Fact{Category: "security_fix", Service: s.Name, Version: best, Quote: string(r[:min(len(r), 300)])})
					break
				}
			}
		}
		if card := catalog.For(s.Image); card != nil && card.Checks["background_migrations"] != nil {
			patch.Gate = &Operation{Op: "exec_check", Service: s.Name, CheckRef: "background_migrations"}
		}
		patch.Why = fmt.Sprintf("%s %s closes %s on the running %d.%d line first, before the longer climb.", s.Name, best, strings.Join(closed, ", "), cur.Nums[0], cur.Nums[1])
		p.Steps[step].Changes[change].From = best
		for _, c := range patch.Changes[1:] {
			i, j := firstChange(p, c.Service)
			p.Steps[i].Changes[j].From = c.To
		}
		first = append(first, patch)
		for _, n := range names {
			done[n] = true
		}
	}
	p.Steps = append(first, p.Steps...)
}

func firstChange(p *Plan, service string) (step, change int) {
	for i, ps := range p.Steps {
		if j := slices.IndexFunc(ps.Changes, func(c Change) bool { return c.Service == service }); j >= 0 {
			return i, j
		}
	}
	return -1, -1
}

func sameMinor(a, b version.V) bool {
	return a.SameLine(b) && slices.Equal(a.Engine, b.Engine) && len(a.Nums) >= 2 && len(b.Nums) >= 2 && a.Nums[0] == b.Nums[0] && a.Nums[1] == b.Nums[1]
}
