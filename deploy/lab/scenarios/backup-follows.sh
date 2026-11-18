# A backup job on the image of its database, which it depends on: it follows the database, never past it, and is not
# exported and reimported itself. First a patch, then a major once the database is made the application of the stack.
mkdir -p /apps/follow
compose() { cat >/apps/follow/compose.yaml <<Y
services:
  postgres:
    image: docker.io/library/postgres:16.4
    environment:
      POSTGRES_PASSWORD: lab
    volumes:
      - db-data:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD", "pg_isready", "-U", "postgres"]
      interval: 5s
    labels:
      palier.auto: "off"$1
  postgres-backup:
    image: docker.io/library/postgres:16.4
    command: ["sh", "-c", "while true; do sleep 3600; done"]
    depends_on:
      postgres:
        condition: service_healthy
    labels:
      palier.auto: "off"
volumes:
  db-data: {}
Y
(cd /apps/follow && docker compose up -d --wait >/dev/null 2>&1); }
state() {
	for s in postgres postgres-backup; do echo "  $s: $(docker inspect follow-$s-1 --format '{{.Config.Image}} {{.State.Status}}')"; done
	echo "  rows: $(docker exec follow-postgres-1 psql -U postgres -At -c 'SELECT count(*) FROM notes') | server $(docker exec follow-postgres-1 psql -U postgres -At -c 'SHOW server_version')"
}
plan() {
	since=$(date -u +%Y-%m-%dT%H:%M:%S)
	$PALIER exec -T palier palier scan >/dev/null
	for i in $(seq 1 60); do
		$PALIER exec -T palier palier show follow --json 2>/dev/null | python3 -c 'import json,sys; r=json.load(sys.stdin).get("record") or {}; sys.exit(0 if r.get("created","") > sys.argv[1] else 1)' "$since" && break
		sleep 5
	done
	$PALIER exec -T palier palier show follow --json 2>/dev/null | python3 -c '
import json, sys
v = json.load(sys.stdin); p = v["record"]["plan"]
print("  roles:", [(s["name"], s["role"]) for s in v["stack"]["services"]])
for i, s in enumerate(p["steps"] or [], 1):
    print("  step", i, [c["service"] + " " + c["from"] + "→" + c["to"] for c in s["changes"]], [o["op"] + ":" + (o.get("service") or ",".join(o.get("services") or [])) for o in s["operations"]])
print("  withheld:", p.get("withheld") or "none")
print("  incomplete:", p.get("incomplete") or "none")'
}
compose ''
docker exec follow-postgres-1 psql -U postgres -q -c "CREATE TABLE notes (id serial PRIMARY KEY, body text)" -c "INSERT INTO notes (body) VALUES ('a'), ('b'), ('c')"
echo "== the database is a dependency of its backup job"; state; plan
apply_and_report follow; state
echo "== the database made the application of the stack"
compose '
      palier.role: application'
plan
apply_and_report follow; state

(cd /apps/follow && docker compose down -v >/dev/null 2>&1)
docker volume ls -q | grep '^follow_' | xargs -r docker volume rm >/dev/null
rm -rf /apps/follow
