package catalog

import (
	"path"
	"regexp"
	"strings"
	"testing"

	"github.com/kolapsis/palier/internal/version"
)

func TestCardsLoad(t *testing.T) {
	c := For("docker.io/outlinewiki/outline")
	if c == nil || len(c.Excerpts) == 0 || For("docker.io/library/nginx") != nil {
		t.Fatalf("outline card: %+v", c)
	}
}

var bulkPath = regexp.MustCompile(`^(/[A-Za-z0-9._-]+)+$`)

func TestEveryCardIsUsable(t *testing.T) {
	seen := map[string]string{}
	for _, c := range cards {
		if c.ID == "" || len(c.Images) == 0 || len(c.Excerpts) == 0 {
			t.Errorf("%s: id, images and excerpts are required", c.ID)
		}
		for _, img := range c.Images {
			if other, dup := seen[img]; dup {
				t.Errorf("%s is in both %s and %s", img, other, c.ID)
			}
			seen[img] = c.ID
			if For(img) == nil {
				t.Errorf("%s: For(%s) finds nothing", c.ID, img)
			}
		}
		for _, e := range c.Excerpts {
			if n := len([]rune(e.Text)); n < 20 || !strings.HasPrefix(e.URL, "https://") {
				t.Errorf("%s: excerpt of %d characters from %q", c.ID, n, e.URL)
			}
		}
		for _, b := range c.Bulk {
			if !bulkPath.MatchString(b) || path.Clean(b) != b || b == "/" {
				t.Errorf("%s: bulk path %q is not a clean absolute path", c.ID, b)
			}
		}
		for _, s := range c.Stops {
			if v, ok := version.Parse(s); !ok || len(v.Nums) < 2 {
				t.Errorf("%s: stop %q is not a major.minor", c.ID, s)
			}
		}
	}
	for _, img := range []string{"docker.io/library/nextcloud", "ghcr.io/goauthentik/server", "docker.gitea.com/gitea", "quay.io/keycloak/keycloak"} {
		if For(img) == nil {
			t.Errorf("no card for %s", img)
		}
	}
}
