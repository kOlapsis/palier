package registry

import (
	"encoding/base64"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	c := New(nil, auths, "mirror.gcr.io/", "")
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

func TestCredentialHelpers(t *testing.T) {
	bin := t.TempDir()
	write := func(name, script string) {
		if err := os.WriteFile(filepath.Join(bin, "docker-credential-"+name), []byte("#!/bin/sh\n"+script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("fake", `read server; echo "{\"ServerURL\":\"$server\",\"Username\":\"user-$server\",\"Secret\":\"pw\"}"`)
	write("token", `echo '{"Username":"<token>","Secret":"refresh"}'`)
	write("missing", `echo "credentials not found in native keychain"; exit 1`)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"auths":{"ghcr.io":{"auth":"Z2g6cHc="}},
		"credHelpers":{"123456789012.dkr.ecr.eu-west-3.amazonaws.com":"fake","https://index.docker.io/v1/":"fake","quay.io":"token","gone.io":"absent"},
		"credsStore":"missing"}`), 0o600)
	auths, err := LoadAuths(dir)
	if err != nil {
		t.Fatal(err)
	}
	c := New(nil, auths, "", "")
	decode := func(s string) string { b, _ := base64.StdEncoding.DecodeString(s); return string(b) }
	if got := decode(c.credentials("123456789012.dkr.ecr.eu-west-3.amazonaws.com")); got != "user-123456789012.dkr.ecr.eu-west-3.amazonaws.com:pw" {
		t.Errorf("ecr: %q", got)
	}
	if got := decode(c.credentials("docker.io")); got != "user-https://index.docker.io/v1/:pw" {
		t.Errorf("docker hub: %q", got)
	}
	if c.credentials("ghcr.io") != "Z2g6cHc=" {
		t.Error("auths must win over the helpers")
	}
	for _, domain := range []string{"quay.io", "gone.io", "registry.example.com"} {
		if got := c.credentials(domain); got != "" {
			t.Errorf("%s: %q, want no credentials", domain, got)
		}
	}
	if _, err := runHelper("absent", "gone.io"); err == nil || !strings.Contains(err.Error(), "mount it") {
		t.Errorf("missing helper: %v", err)
	}
	os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"credHelpers":{"x.io":"../../bin/sh"}}`), 0o600)
	if _, err := LoadAuths(dir); err == nil {
		t.Error("a helper name with a path was accepted")
	}
}

func TestRetryAfter(t *testing.T) {
	for v, want := range map[string]time.Duration{"7": 7 * time.Second, "900": 2 * time.Minute, "": 3 * time.Second, "Wed, 21 Oct 2026 07:28:00 GMT": 3 * time.Second, "-1": 3 * time.Second} {
		if got := retryAfter(http.Header{"Retry-After": {v}}, 3*time.Second); got != want {
			t.Errorf("%q: %v", v, got)
		}
	}
}
