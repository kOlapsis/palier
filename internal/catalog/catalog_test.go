package catalog

import "testing"

func TestCardsLoad(t *testing.T) {
	c := For("docker.io/outlinewiki/outline")
	if c == nil || len(c.Excerpts) == 0 || For("docker.io/library/nginx") != nil {
		t.Fatalf("outline card: %+v", c)
	}
}
