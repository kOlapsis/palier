package run

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
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
	if got := summaryLine(before); got != "roles 1, databases 1, tables 1, rows 3, indexes 1, constraints 1" {
		t.Fatal(got)
	}
}

func TestSummarizeCountsRows(t *testing.T) {
	query := func(db, sql string) ([]string, error) {
		switch {
		case strings.HasPrefix(sql, "roles"):
			return []string{"app@%"}, nil
		case strings.HasPrefix(sql, "dbs"):
			return []string{"shop"}, nil
		case strings.HasPrefix(sql, "idx"):
			return []string{"orders.PRIMARY"}, nil
		}
		return nil, nil
	}
	counted := func(db string) ([]string, error) { return []string{"orders=3"}, nil }
	lines, err := summarize(query, "roles", "dbs", "", func(string) []string { return []string{"", "idx", "cons"} }, counted)
	if err != nil {
		t.Fatal(err)
	}
	if got := summaryLine(lines); got != "roles 1, databases 1, tables 1, rows 3, indexes 1, constraints 0" {
		t.Fatal(got, lines)
	}
}

func TestNewVolumeName(t *testing.T) {
	for _, c := range [][4]string{
		{"postgres-data", "postgres", "17.6", "postgres-data-17"},
		{"${DB_DATA_LOCATION}", "database", "16-vectorchord0.3.0-pgvectors0.2.0", "database-data-16-vectorchord0"},
		{"./postgres", "database", "16.2", "database-data-16"},
	} {
		if got := newVolumeName(c[0], c[1], c[2]); got != c[3] {
			t.Errorf("newVolumeName(%q) = %q, want %q", c[0], got, c[3])
		}
	}
}

func TestDataMounts(t *testing.T) {
	s := &inventory.Service{Mounts: []inventory.Mount{
		{Type: "bind", Source: "/", Destination: "/rootfs"},
		{Type: "bind", Source: "/var/run/docker.sock", Destination: "/var/run/docker.sock"},
		{Type: "bind", Source: "/usr/share/zoneinfo", Destination: "/usr/share/zoneinfo"},
		{Type: "bind", Source: "/srv/traefik/logs", Destination: "/var/log/traefik"},
		{Type: "volume", Source: "gitlab_logs", Destination: "/var/log/gitlab"},
		{Type: "bind", Source: "/runner", Destination: "/etc/gitlab-runner"},
		{Type: "volume", Source: "tmp", Destination: "/tmp"},
		{Type: "volume", Source: "gitlab_data", Destination: "/var/opt/gitlab"},
	}}
	copies, bulk := dataMounts(s)
	var got []string
	for _, c := range copies {
		got = append(got, c.Mount.Source)
	}
	if want := []string{"/runner", "tmp", "gitlab_data"}; !slices.Equal(got, want) || len(bulk) > 0 || copies[2].Exclude != nil {
		t.Errorf("dataMounts = %v %v", copies, bulk)
	}
	gitlab := &inventory.Service{Image: "docker.io/gitlab/gitlab-ce", Mounts: []inventory.Mount{
		{Type: "bind", Source: "/app/gitlab/data/config", Destination: "/etc/gitlab"},
		{Type: "bind", Source: "/app/gitlab/data/data", Destination: "/var/opt/gitlab"},
		{Type: "bind", Source: "/app/gitlab/repos", Destination: "/var/opt/gitlab/git-data"},
	}}
	copies, bulk = dataMounts(gitlab)
	if len(copies) != 2 || copies[0].Exclude != nil || !slices.Contains(copies[1].Exclude, "git-data") || !slices.Contains(copies[1].Exclude, "gitlab-rails/shared/artifacts") || !slices.Equal(bulk, []string{"/var/opt/gitlab/git-data"}) {
		t.Errorf("gitlab dataMounts = %+v %v", copies, bulk)
	}
}

func TestAncestors(t *testing.T) {
	got := ancestors([]string{"git-data", "gitlab-rails/uploads", "gitlab-rails/shared/artifacts", "gitlab-ci/builds"})
	if want := []string{"gitlab-rails", "gitlab-rails/shared", "gitlab-ci"}; !slices.Equal(got, want) {
		t.Errorf("ancestors = %v, want %v", got, want)
	}
}

func TestCopyVerdict(t *testing.T) {
	const gib = 1 << 20
	for _, c := range []struct {
		out   string
		state bool
		skip  bool
	}{
		{"5242880 104857600 209715200", false, false},
		{"du: x: Permission denied\n5242880 104857600 209715200", false, false},
		{"31457280 104857600 209715200", false, true},
		{"31457280 104857600 209715200", true, false},
		{"31457280 40000000 209715200", true, true},
		{fmt.Sprint(gib, " ", 3*gib, " ", 8*gib), true, false},
		{fmt.Sprint(gib, " ", 2*gib, " ", 8*gib), true, true},
	} {
		why, err := copyVerdict(c.out, c.state)
		if err != nil || (why != "") != c.skip {
			t.Errorf("copyVerdict(%q, %v) = %q, %v", c.out, c.state, why, err)
		}
	}
	if _, err := copyVerdict("garbage", true); err == nil {
		t.Error("copyVerdict accepts garbage")
	}
}

func TestBackupPaths(t *testing.T) {
	if got := remotePath("offsite:palier/", "pgcoll", "postgres"); got != "offsite:palier/pgcoll/postgres" {
		t.Errorf("remotePath = %s", got)
	}
	if got := remotePath("minio:", "pgcoll", "postgres"); got != "minio:pgcoll/postgres" {
		t.Errorf("remotePath at the root = %s", got)
	}
	names := []string{"20261109T100000Z/", "notes/", "20261107T100000Z/", "20261108T100000Z/", "20261110T100000Z/"}
	if got := expired(names, 2); !reflect.DeepEqual(got, []string{"20261107T100000Z", "20261108T100000Z"}) {
		t.Errorf("expired = %v", got)
	}
	if got := expired(names, 7); got != nil {
		t.Errorf("expired with room left = %v", got)
	}
}
