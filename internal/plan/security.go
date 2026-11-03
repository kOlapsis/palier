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
// advisory no longer touches, when the first step of the plan for that service would leave it exposed.
func SecurityFirst(p *Plan, st *inventory.Stack) {
	var first []Step
	for _, s := range st.Services {
		cur, ok := version.Parse(s.Version)
		if len(s.Advisories) == 0 || !ok || len(cur.Nums) < 2 {
			continue
		}
		exposed := func(v version.V) []notes.Advisory {
			return slices.DeleteFunc(slices.Clone(s.Advisories), func(a notes.Advisory) bool { return !a.Affects(v) })
		}
		step, change := -1, -1
		for i, ps := range p.Steps {
			if j := slices.IndexFunc(ps.Changes, func(c Change) bool { return c.Service == s.Name }); j >= 0 {
				step, change = i, j
				break
			}
		}
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
		var closed []string
		patch := Step{
			Changes:    []Change{{Service: s.Name, From: s.Version, To: best}},
			Operations: []Operation{{Op: "set_image", Service: s.Name, Image: s.Image, Tag: best}, {Op: "recreate", Services: []string{s.Name}}},
			Gate:       &Operation{Op: "none"},
			Risk:       "attention",
			Reversible: "yes",
		}
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
		first = append(first, patch)
	}
	p.Steps = append(first, p.Steps...)
}

func sameMinor(a, b version.V) bool {
	return a.SameLine(b) && len(a.Nums) >= 2 && len(b.Nums) >= 2 && a.Nums[0] == b.Nums[0] && a.Nums[1] == b.Nums[1]
}
