package run

import (
	"reflect"
	"testing"

	"github.com/kolapsis/palier/internal/inventory"
)

func TestDependencyOrder(t *testing.T) {
	st := &inventory.Stack{Services: []*inventory.Service{
		{Name: "outline", DependsOn: []string{"postgres", "redis"}},
		{Name: "postgres"},
		{Name: "redis"},
	}}
	if got := dependencyOrder(st, []string{"outline", "postgres"}); !reflect.DeepEqual(got, []string{"postgres", "outline"}) {
		t.Fatalf("order = %v", got)
	}
}

func TestCurrentImage(t *testing.T) {
	for in, want := range map[string]string{
		"docker.io/traefik/whoami:v1.10.1": "docker.io/traefik/whoami",
		"localhost:5000/app@sha256:abc":    "localhost:5000/app",
		"redis:6.2-alpine@sha256:84":       "redis",
	} {
		got, err := currentImage([]byte("services:\n  s:\n    image: "+in+"\n"), "s")
		if err != nil || got != want {
			t.Errorf("currentImage(%s) = %q %v", in, got, err)
		}
	}
}

func TestCompareSummaries(t *testing.T) {
	before := []string{"role outline", "database outline", "table outline.public.documents=3", "index outline.public.documents_pkey", "constraint outline.documents.documents_pkey"}
	after := []string{"role outline", "database outline", "table outline.public.documents=3"}
	if got := compareSummaries(before, after); got == "" {
		t.Fatal("missing index and constraint not seen")
	}
	if got := compareSummaries(before, before); got != "" {
		t.Fatalf("identical summaries differ: %s", got)
	}
	if got := summaryLine(before); got != "rôles 1, bases 1, tables 1, lignes 3, index 1, contraintes 1" {
		t.Fatal(got)
	}
}
