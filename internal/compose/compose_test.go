package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestSetImage(t *testing.T) {
	in := `# Started by the bench.
name: whoami

services:
  whoami:
    image: docker.io/traefik/whoami:v1.10.1   # pinned
    restart: always
  other:
    image: "nginx:1.27.0"
  third:
    image: 'redis:7.2.4'
`
	out, err := SetImage([]byte(in), "whoami", "docker.io/traefik/whoami:v1.12.0")
	if err != nil {
		t.Fatal(err)
	}
	out, err = SetImage(out, "other", "nginx:1.31.7")
	if err != nil {
		t.Fatal(err)
	}
	out, err = SetImage(out, "third", "redis:7.2.16")
	if err != nil {
		t.Fatal(err)
	}
	want := `# Started by the bench.
name: whoami

services:
  whoami:
    image: docker.io/traefik/whoami:v1.12.0   # pinned
    restart: always
  other:
    image: "nginx:1.31.7"
  third:
    image: 'redis:7.2.16'
`
	if string(out) != want {
		t.Fatalf("got:\n%s", out)
	}
	if _, err := SetImage([]byte("services:\n  app:\n    image: ghcr.io/x/app:${TAG}\n"), "app", "ghcr.io/x/app:2"); err == nil {
		t.Fatal("variable image accepted")
	}
	if _, err := SetImage([]byte(in), "missing", "x:1"); err == nil {
		t.Fatal("missing service accepted")
	}
}

func TestWriteKeepsMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "compose.yaml")
	if err := os.WriteFile(path, []byte("a"), 0o640); err != nil {
		t.Fatal(err)
	}
	st, err := ReadState(path)
	if err != nil {
		t.Fatal(err)
	}
	st.Data = "b"
	if err := Write(st); err != nil {
		t.Fatal(err)
	}
	after, err := ReadState(path)
	if err != nil || after.Data != "b" || after.Mode != 0o640 || after.UID != st.UID {
		t.Fatalf("after: %+v %v", after, err)
	}
}

func TestReplaceMount(t *testing.T) {
	in := `# bench
name: outline
services:
  postgres:
    image: docker.io/library/postgres:16.4
    volumes:
      - postgres-data:/var/lib/postgresql/data
  outline:
    volumes:
      - type: volume
        source: outline-data
        target: /var/lib/outline/data
volumes:
  outline-data: {}
  postgres-data: {}
`
	out, err := Edit([]byte(in), func(root *yaml.Node) error {
		if err := ReplaceMount(root, "postgres", "/var/lib/postgresql/data", "postgres-data-18", "/var/lib/postgresql"); err != nil {
			return err
		}
		return ReplaceMount(root, "outline", "/var/lib/outline/data", "outline-data-2", "/data")
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# bench", "- postgres-data-18:/var/lib/postgresql\n", "source: outline-data-2", "target: /data", "postgres-data-18: {}", "outline-data-2: {}", "postgres-data: {}"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("%q missing in:\n%s", want, out)
		}
	}
	if _, err := Edit([]byte(in), func(root *yaml.Node) error { return ReplaceMount(root, "postgres", "/nope", "v", "/x") }); err == nil {
		t.Error("missing mount accepted")
	}
}

func TestImmichEdits(t *testing.T) {
	in := `name: immich
services:
  immich-server:
    image: ghcr.io/immich-app/immich-server:${IMMICH_VERSION:-release}
    command: ['start.sh', 'immich']
    environment:
      - SERVER_PORT=3001
    depends_on:
      - redis
      - database
  immich-microservices:
    image: ghcr.io/immich-app/immich-server:${IMMICH_VERSION:-release}
    depends_on:
      - redis
  database:
    image: registry.hub.docker.com/tensorchord/pgvecto-rs:pg14-v0.2.0@sha256:90
    command: ["postgres", "-c", "shared_preload_libraries=vectors.so"]
    healthcheck:
      test: pg_isready
  web:
    depends_on: [immich-microservices]
`
	if repo, v, ok := ImageVariable("ghcr.io/immich-app/immich-server:${IMMICH_VERSION:-release}"); !ok || repo != "ghcr.io/immich-app/immich-server" || v != "IMMICH_VERSION" {
		t.Fatalf("ImageVariable: %s %s %v", repo, v, ok)
	}
	out, err := Edit([]byte(in), func(root *yaml.Node) error {
		for _, e := range [][]string{
			{"immich-microservices", "remove_service", "", "", ""},
			{"immich-server", "unset_command", "", "", ""},
			{"immich-server", "rename_env", "SERVER_PORT", "", "IMMICH_PORT"},
			{"immich-server", "set_env", "IMMICH_HOST", "0.0.0.0", ""},
			{"database", "unset_command", "", "", ""},
			{"database", "unset_healthcheck", "", "", ""},
			{"database", "unset_env", "NOPE", "", ""},
		} {
			if _, err := ApplyEdit(root, e[0], e[1], e[2], e[3], e[4]); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, bad := range []string{"immich-microservices", "start.sh", "shared_preload_libraries", "pg_isready", "SERVER_PORT"} {
		if strings.Contains(s, bad) {
			t.Errorf("%q still in:\n%s", bad, s)
		}
	}
	for _, good := range []string{"IMMICH_PORT=3001", "IMMICH_HOST=0.0.0.0", "- redis", "- database"} {
		if !strings.Contains(s, good) {
			t.Errorf("%q missing in:\n%s", good, s)
		}
	}
	env := SetEnvVar([]byte("UPLOAD_LOCATION=./library\nIMMICH_VERSION=v1.105.1\n"), "IMMICH_VERSION", "v1.106.4")
	if v, _ := EnvVar(env, "IMMICH_VERSION"); v != "v1.106.4" || !strings.Contains(string(env), "UPLOAD_LOCATION=./library") {
		t.Fatalf("env: %s", env)
	}
}
