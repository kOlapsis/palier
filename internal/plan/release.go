// Copyright 2026 Benjamin Touchard (Kolapsis)
// SPDX-License-Identifier: Apache-2.0

package plan

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/kolapsis/palier/internal/inventory"
	"github.com/kolapsis/palier/internal/version"
)

// Together returns, for each service, the other services of the stack it changes with: those of the same OCI source at
// the same version, and the service of the same image it depends on.
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
	for _, s := range st.Services {
		if d := Followed(st, s); d != nil && sameVersion(s.Version, d.Version) && !slices.Contains(out[s.Name], d) {
			out[s.Name], out[d.Name] = append(out[s.Name], d), append(out[d.Name], s)
		}
	}
	return out
}

// Followed returns the service of the same image that s depends on, a backup job next to its database for instance, or nil.
func Followed(st *inventory.Stack, s *inventory.Service) *inventory.Service {
	for _, name := range s.DependsOn {
		if d := service(st, name); d != nil && d != s && d.Image == s.Image {
			return d
		}
	}
	return nil
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
	removed := map[string]bool{}
	for i, s := range p.Steps {
		for _, op := range s.Operations {
			if op.Op == "compose_edit" && slices.ContainsFunc(op.Edits, func(e Edit) bool { return e.Action == "remove_service" }) {
				removed[op.Service] = true
			}
		}
		to := map[string]string{}
		for _, c := range s.Changes {
			to[c.Service] = c.To
		}
		for _, c := range s.Changes {
			for _, o := range together[c.Service] {
				if _, both := to[o.Name]; removed[o.Name] || both && o.Name < c.Service {
					continue
				}
				if !sameVersion(to[o.Name], c.To) {
					p.incomplete("step %d: %s and %s come from %s at the same version; they change together, to the same version", i+1, c.Service, o.Name, cmp.Or(o.Source, o.Image))
				}
			}
		}
	}
}
