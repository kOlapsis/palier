package version

import (
	"reflect"
	"testing"
)

func tags(vs []V) []string {
	var out []string
	for _, v := range vs {
		out = append(out, v.Tag)
	}
	return out
}

func TestNewer(t *testing.T) {
	cases := []struct {
		current string
		tags    []string
		want    []string
	}{
		{"1.27.0", []string{"1.27.0", "1.26.9", "1.31.2", "1.28.0", "1.31.10", "1.31", "1.31.2-alpine", "latest", "1.29.0-rc1"}, []string{"1.28.0", "1.31.2", "1.31.10"}},
		{"v1.10.1", []string{"v1.10.0", "v1.11.0", "1.12.0", "v1.12.0"}, []string{"v1.11.0", "v1.12.0"}},
		{"16.4", []string{"16.10", "16.4-bookworm", "17.0", "18.1", "16", "17rc1"}, []string{"16.10", "17.0", "18.1"}},
		{"16.1.6-ce.0", []string{"16.3.9-ce.0", "16.3.9-ee.0", "17.0.0-ce.0"}, []string{"16.3.9-ce.0", "17.0.0-ce.0"}},
	}
	for _, c := range cases {
		cur, ok := Parse(c.current)
		if !ok {
			t.Fatalf("parse %q", c.current)
		}
		if got := tags(Newer(cur, c.tags)); !reflect.DeepEqual(got, c.want) {
			t.Errorf("Newer(%s) = %v, want %v", c.current, got, c.want)
		}
	}
}

func TestExtends(t *testing.T) {
	for _, c := range []struct {
		v, w string
		want bool
	}{{"2.2", "2.2.7", true}, {"2", "2.3.0", true}, {"2.2", "2.3.0", false}, {"16.4", "16.4-bookworm", false}, {"2.2.7", "2.2", false}} {
		v, _ := Parse(c.v)
		w, _ := Parse(c.w)
		if v.Extends(w) != c.want {
			t.Errorf("%s.Extends(%s) = %v", c.v, c.w, !c.want)
		}
	}
	if _, ok := Parse("latest"); ok {
		t.Error("latest parsed as a version")
	}
}
