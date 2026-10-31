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
