package notes

import (
	"strings"
	"testing"

	"github.com/kolapsis/palier/internal/version"
)

func TestRepoAndSame(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/traefik/whoami":       "traefik/whoami",
		"https://github.com/outline/outline.git":  "outline/outline",
		"git@github.com:immich-app/immich.git":    "immich-app/immich",
		"https://github.com/a/b/tree/main/docker": "a/b",
		"https://gitlab.com/gitlab-org/gitlab":    "gitlab.com/gitlab-org/gitlab",
		"https://gitlab.com/a/sub/p/-/tree/main":  "gitlab.com/a/sub/p",
		"git@gitlab.com:a/p.git":                  "gitlab.com/a/p",
		"https://gitlab.com/a":                    "",
		"https://gitlab.example.org/a/b":          "",
	} {
		if got := Repo(in); got != want {
			t.Errorf("Repo(%q) = %q, want %q", in, got, want)
		}
	}
	v, _ := version.Parse("1.12.0")
	if !Same("v1.12.0", v) || !Same("1.12", v) || Same("v1.12.1", v) {
		t.Error("Same")
	}
	a, _ := version.Parse("v1.10.1")
	b, _ := version.Parse("1.10.1")
	if Less(a, b) || Less(b, a) {
		t.Error("Less ignores the prefix")
	}
}

func TestAdvisoryRanges(t *testing.T) {
	adv := []Advisory{
		{ID: "host-header", Affected: []string{">= 2.1.0, <= 2.5.8"}, Patched: []string{"2.5.9"}},
		{ID: "ssti", Affected: []string{">= 1.23.0"}, Patched: []string{"2.2.1"}},
		{ID: "badge", Affected: []string{">= 2.0.0 && <= 2.1.3 "}, Patched: []string{"2.2.0"}},
		{ID: "dos", Affected: []string{"<=2.5.3"}, Patched: []string{"2.5.4"}},
		{ID: "unreadable", Affected: []string{"see description"}, Patched: []string{""}},
	}
	affected := func(tag string) []string {
		v, _ := version.Parse(tag)
		var ids []string
		for _, a := range adv {
			if a.Affects(v) {
				ids = append(ids, a.ID)
			}
		}
		return ids
	}
	for tag, want := range map[string]string{"2.5.5": "host-header", "2.5.9": "", "2.1.0": "host-header ssti badge dos", "1.23.5": "ssti dos"} {
		if got := strings.Join(affected(tag), " "); got != want {
			t.Errorf("%s affected by %q, want %q", tag, got, want)
		}
	}
	v, _ := version.Parse("2.5.2")
	if fixed, ok := FixedFrom(adv, v); !ok || fixed.Tag != "2.5.4" {
		t.Errorf("FixedFrom(2.5.2) = %v %v, want 2.5.4", fixed.Tag, ok)
	}
	v, _ = version.Parse("2.5.9")
	if _, ok := FixedFrom(adv, v); ok {
		t.Error("2.5.9 is affected by nothing")
	}
}
