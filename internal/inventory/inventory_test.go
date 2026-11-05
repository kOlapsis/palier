package inventory

import (
	"reflect"
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
