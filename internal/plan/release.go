package plan

import (
	"fmt"
	"slices"

	"github.com/kolapsis/palier/internal/inventory"
	"github.com/kolapsis/palier/internal/version"
)

// Together returns, for each service, the other services of the stack whose image declares the same OCI source and runs the same version.
func Together(st *inventory.Stack) map[string][]*inventory.Service {
	groups := map[string][]*inventory.Service{}
	for _, s := range st.Services {
		v, ok := version.Parse(s.Version)
		if s.Source == "" || !ok {
			continue
		}
		key := s.Source + " " + fmt.Sprint(v.Nums)
		groups[key] = append(groups[key], s)
	}
	out := map[string][]*inventory.Service{}
	for _, g := range groups {
		for _, s := range g {
			for _, o := range g {
				if o != s {
					out[s.Name] = append(out[s.Name], o)
				}
			}
		}
	}
	return out
}

func sameVersion(a, b string) bool {
	v, ok1 := version.Parse(a)
	w, ok2 := version.Parse(b)
	return ok1 && ok2 && slices.Equal(v.Nums, w.Nums)
}

func sameVersionTag(s *inventory.Service, tag string) string {
	for _, r := range s.Available {
		if sameVersion(r.Tag, tag) {
			return r.Tag
		}
	}
	return ""
}

func checkTogether(p *Plan, st *inventory.Stack) {
	together := Together(st)
	for i, s := range p.Steps {
		to := map[string]string{}
		for _, c := range s.Changes {
			to[c.Service] = c.To
		}
		for _, c := range s.Changes {
			for _, o := range together[c.Service] {
				if _, both := to[o.Name]; both && o.Name < c.Service {
					continue
				}
				if !sameVersion(to[o.Name], c.To) {
					p.incomplete("step %d: %s and %s come from %s at the same version; they change together, to the same version", i+1, c.Service, o.Name, o.Source)
				}
			}
		}
	}
}
