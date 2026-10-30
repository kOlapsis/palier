package compose

import (
	"os"
	"path/filepath"
	"testing"
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
