# A stack built like a real one: a PostgreSQL, a backup job on the same image with its own command, a migration service
# that ends, and an application. The backup job follows its database, and the ended migration does not fail the run.
mkdir -p /apps/sidecars
cat >/apps/sidecars/compose.yaml <<'Y'
services:
  db:
    image: docker.io/library/postgres:16.4
    environment:
      POSTGRES_PASSWORD: lab
    volumes:
      - db-data:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD", "pg_isready", "-U", "postgres"]
      interval: 5s
    labels:
      palier.auto: "off"
  backup:
    image: docker.io/library/postgres:16.4
    command: ["sh", "-c", "while true; do sleep 3600; done"]
    depends_on: [db]
    volumes:
      - backups:/backups
    labels:
      palier.auto: "off"
  migrate:
    image: docker.io/library/busybox:1.36.1
    command: ["sh", "-c", "echo Migrations applied successfully"]
    restart: "no"
    depends_on:
      db:
        condition: service_healthy
    labels:
      palier.auto: "off"
  app:
    image: docker.io/traefik/whoami:v1.10.1
    depends_on: [db]
    labels:
      palier.auto: "off"
volumes:
  db-data: {}
  backups: {}
Y
(cd /apps/sidecars && docker compose up -d >/dev/null 2>&1)
until docker exec sidecars-db-1 pg_isready -U postgres >/dev/null 2>&1; do sleep 2; done
docker exec sidecars-db-1 psql -U postgres -q -c "CREATE TABLE notes (id serial PRIMARY KEY, body text)" -c "INSERT INTO notes (body) VALUES ('a'), ('b'), ('c')"
state() {
	for s in db backup migrate app; do echo "  $s: $(docker inspect sidecars-$s-1 --format '{{.Config.Image}} {{.State.Status}} exit={{.State.ExitCode}}')"; done
	echo "  rows: $(docker exec sidecars-db-1 psql -U postgres -At -c 'SELECT count(*) FROM notes')"
}
echo "before:"; state
$PALIER exec -T palier palier scan --once 2>/dev/null | python3 -c '
import json, sys
for st in json.load(sys.stdin):
    if st["name"] == "sidecars":
        for s in st["services"]:
            print("  %-8s %-12s %-8s own_command=%s available %s" % (s["name"], s["role"], s.get("version"), s.get("own_command", False), [r["tag"] for r in s.get("available") or []][-2:]))'
since=$(date -u +%Y-%m-%dT%H:%M:%S)
$PALIER exec -T palier palier scan >/dev/null
for i in $(seq 1 60); do
	$PALIER exec -T palier palier show sidecars --json 2>/dev/null | python3 -c 'import json,sys; r=json.load(sys.stdin).get("record") or {}; sys.exit(0 if r.get("created","") > sys.argv[1] else 1)' "$since" && break
	sleep 10
done
$PALIER exec -T palier palier show sidecars --json 2>/dev/null | python3 -c '
import json, sys
r = json.load(sys.stdin)["record"]; p = r["plan"]
print("  plan from", r["source"])
for i, s in enumerate(p["steps"] or [], 1):
    print("  step", i, [c["service"] + " " + c["from"] + "→" + c["to"] for c in s["changes"]], [o["op"] for o in s["operations"]])
print("  withheld:", p.get("withheld") or "none")
print("  incomplete:", p.get("incomplete") or "none")'
apply_and_report sidecars
echo "after:"; state

(cd /apps/sidecars && docker compose down -v >/dev/null 2>&1)
rm -rf /apps/sidecars
