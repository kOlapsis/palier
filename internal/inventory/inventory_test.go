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
