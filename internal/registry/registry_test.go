package registry

import "testing"

func TestParseRef(t *testing.T) {
	cases := map[string]Ref{
		"nginx":                                                      {"docker.io", "library/nginx", "latest"},
		"docker.io/library/nginx:1.27.0":                             {"docker.io", "library/nginx", "1.27.0"},
		"traefik/whoami:v1.10.1":                                     {"docker.io", "traefik/whoami", "v1.10.1"},
		"ghcr.io/immich-app/immich-server:v1.105.1":                  {"ghcr.io", "immich-app/immich-server", "v1.105.1"},
		"localhost:5000/app:2.1@sha256:abc":                          {"localhost:5000", "app", "2.1"},
		"registry.hub.docker.com/library/redis:6.2-alpine@sha256:84": {"registry.hub.docker.com", "library/redis", "6.2-alpine"},
	}
	for in, want := range cases {
		if got := ParseRef(in); got != want {
			t.Errorf("ParseRef(%q) = %+v, want %+v", in, got, want)
		}
	}
}
