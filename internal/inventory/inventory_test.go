package inventory

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseDependsOnAndRoles(t *testing.T) {
	deps, local, err := parseDependsOn([]byte(`
services:
  outline:
    depends_on:
      postgres:
        condition: service_healthy
      redis:
        condition: service_started
  worker:
    depends_on: [redis]
  postgres: {}
  redis: {}
  backup:
    image: selfstack-backup
    pull_policy: never
  runner:
    build: ./runner
  pinned:
    image: nginx
    pull_policy: always
`))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(local, map[string]bool{"backup": true, "runner": true}) {
		t.Errorf("built locally: %v", local)
	}
	want := map[string][]string{"outline": {"postgres", "redis"}, "worker": {"redis"}}
	if !reflect.DeepEqual(deps, want) {
		t.Fatalf("depends_on = %v", deps)
	}
	st := &Stack{}
	for _, n := range []string{"outline", "worker", "postgres", "redis"} {
		st.Services = append(st.Services, &Service{Name: n, DependsOn: deps[n]})
	}
	assignRoles(st)
	got := map[string]string{}
	for _, s := range st.Services {
		got[s.Name] = s.Role
	}
	if !reflect.DeepEqual(got, map[string]string{"outline": RoleApplication, "worker": RoleApplication, "postgres": RoleDependency, "redis": RoleDependency}) {
		t.Fatalf("roles = %v", got)
	}
}

func TestRoleLabel(t *testing.T) {
	st := &Stack{Services: []*Service{
		{Name: "n8n", roleLabel: "application"},
		{Name: "runners", DependsOn: []string{"n8n"}},
		{Name: "cache", roleLabel: "dependency"},
		{Name: "proxy", roleLabel: "sidecar"},
	}}
	assignRoles(st)
	got := map[string]string{}
	for _, s := range st.Services {
		got[s.Name] = s.Role
	}
	if !reflect.DeepEqual(got, map[string]string{"n8n": RoleApplication, "runners": RoleApplication, "cache": RoleDependency, "proxy": RoleApplication}) {
		t.Errorf("roles = %v", got)
	}
	if len(st.Errors) != 1 || !strings.Contains(st.Errors[0], `palier.role of proxy: "sidecar"`) {
		t.Errorf("errors = %q", st.Errors)
	}
}

func TestOrphans(t *testing.T) {
	project := func(name string) map[string]string { return map[string]string{labelProject: name} }
	stacks := map[string]*Stack{"mariadb": {Name: "mariadb"}, "\x00lab-observed": {Name: "lab-observed", Observed: true}}
	unknown := orphans([]dockerVolume{
		{Name: "mariadb_data-13", Mountpoint: "/var/lib/docker/volumes/mariadb_data-13/_data", Labels: project("mariadb")},
		{Name: "mariadb_data", Mountpoint: "/var/lib/docker/volumes/mariadb_data/_data", Labels: project("mariadb")},
		{Name: "gone_data", Labels: project("gone")},
		{Name: "palier-run7-step0-db-0", Labels: map[string]string{"palier.run": "7"}},
	}, map[string]bool{"mariadb_data-13": true}, stacks)
	got := stacks["mariadb"].Unused
	if !unknown || len(got) != 1 || got[0].Name != "mariadb_data" || got[0].Size != -1 || got[0].Mountpoint == "" {
		t.Errorf("unused: %+v, unknown %v", got, unknown)
	}
}
