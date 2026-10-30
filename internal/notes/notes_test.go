package notes

import (
	"testing"

	"github.com/kolapsis/palier/internal/version"
)

func TestRepoAndSame(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/traefik/whoami":       "traefik/whoami",
		"https://github.com/outline/outline.git":  "outline/outline",
		"git@github.com:immich-app/immich.git":    "immich-app/immich",
		"https://github.com/a/b/tree/main/docker": "a/b",
		"https://gitlab.com/gitlab-org/gitlab":    "",
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
