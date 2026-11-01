package registry

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseRef(t *testing.T) {
	cases := map[string]Ref{
		"nginx":                                                      {"docker.io", "library/nginx", "latest"},
		"docker.io/library/nginx:1.27.0":                             {"docker.io", "library/nginx", "1.27.0"},
		"traefik/whoami:v1.10.1":                                     {"docker.io", "traefik/whoami", "v1.10.1"},
		"ghcr.io/immich-app/immich-server:v1.105.1":                  {"ghcr.io", "immich-app/immich-server", "v1.105.1"},
		"localhost:5000/app:2.1@sha256:abc":                          {"localhost:5000", "app", "2.1"},
		"registry.hub.docker.com/library/redis:6.2-alpine@sha256:84": {"docker.io", "library/redis", "6.2-alpine"},
		"index.docker.io/redis:7":                                    {"docker.io", "library/redis", "7"},
	}
	for in, want := range cases {
		if got := ParseRef(in); got != want {
			t.Errorf("ParseRef(%q) = %+v, want %+v", in, got, want)
		}
	}
}

func TestCredentials(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"auths":{"https://index.docker.io/v1/":{"auth":"aHViOnB3"},"ghcr.io":{"auth":"Z2g6cHc="},"empty.io":{}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	auths, err := LoadAuths(dir)
	if err != nil {
		t.Fatal(err)
	}
	c := New(nil, auths, "mirror.gcr.io/")
	if c.mirror != "https://mirror.gcr.io" || c.UsesCredentials("mirror.gcr.io") {
		t.Fatalf("mirror %q must not carry the Docker Hub credentials", c.mirror)
	}
	if c.credentials("docker.io") != "aHViOnB3" || c.credentials("ghcr.io") != "Z2g6cHc=" || c.UsesCredentials("empty.io") || c.UsesCredentials("quay.io") {
		t.Fatalf("credentials: %v", auths)
	}
	if a, err := c.authorize(t.Context(), "ghcr.io", `Basic realm="x"`); err != nil || a != "Basic Z2g6cHc=" {
		t.Fatalf("basic: %q %v", a, err)
	}
	if auths, err := LoadAuths(t.TempDir()); err != nil || auths != nil {
		t.Fatalf("no config: %v %v", auths, err)
	}
}
