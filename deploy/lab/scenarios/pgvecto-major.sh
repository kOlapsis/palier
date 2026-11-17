# A PostgreSQL image whose tag names its PostgreSQL (tensorchord/pgvecto-rs:pg14-v0.2.0): the pg15 to pg17 tags are newer
# versions, reached by export and reimport. A plan the AI cannot compute says why and what to do.
mkdir -p /apps/pgvecto
cat >/apps/pgvecto/compose.yaml <<'EOF'
services:
  db:
    image: docker.io/tensorchord/pgvecto-rs:pg14-v0.2.0
    environment:
      POSTGRES_USER: lab
      POSTGRES_PASSWORD: lab
      POSTGRES_DB: lab
    volumes:
      - data:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD", "pg_isready", "-U", "lab"]
      interval: 5s
    labels:
      palier.auto: "off"
volumes:
  data: {}
EOF
(cd /apps/pgvecto && docker compose up -d --wait >/dev/null 2>&1)
docker exec pgvecto-db-1 psql -U lab -d lab -v ON_ERROR_STOP=1 -q -c "CREATE EXTENSION IF NOT EXISTS vectors" \
	-c "CREATE TABLE items (id serial PRIMARY KEY, name text, embedding vector(3))" \
	-c "INSERT INTO items (name, embedding) VALUES ('a', '[1,2,3]'), ('b', '[4,5,6]'), ('c', '[7,8,9]')"
data() {
	docker exec pgvecto-db-1 psql -U lab -d lab -At -c "SELECT current_setting('server_version') || ' | items ' || count(*) || ': ' || string_agg(name || embedding::text, ' ' ORDER BY id) FROM items" \
		-c "SELECT 'vectors ' || extversion FROM pg_extension WHERE extname = 'vectors'" 2>&1 | tr '\n' ' '
}
echo "before: $(docker inspect pgvecto-db-1 --format '{{.Config.Image}}') | $(data)"

echo "== versions seen"
$PALIER exec -T palier palier scan --once 2>/dev/null | python3 -c '
import json, sys
for st in json.load(sys.stdin):
    if st["name"] == "pgvecto":
        s = st["services"][0]
        print("  db", s["role"], s.get("version"), "available", [r["tag"] for r in s.get("available") or []])'

echo "== plan with an invalid AI key"
$PALIER exec -T -e PALIER_AI_KEY=invalid palier palier plan --once pgvecto --json 2>/dev/null | python3 -c '
import json, sys
for r in json.load(sys.stdin)["plan"]["incomplete"]:
    print("  incomplete:", r[:400])'
since=$(date -u +%Y-%m-%dT%H:%M:%S)
$PALIER exec -T palier palier scan >/dev/null
$PALIER exec -T palier palier status 2>/dev/null | grep pgvecto || true
$PALIER exec -T palier palier show pgvecto 2>/dev/null | sed -n '1,12p'

echo "== plan of the server, then apply"
for i in $(seq 1 60); do
	$PALIER exec -T palier palier show pgvecto --json 2>/dev/null | python3 -c 'import json,sys; r=json.load(sys.stdin).get("record") or {}; sys.exit(0 if r.get("created","") > sys.argv[1] else 1)' "$since" && break
	sleep 10
done
$PALIER exec -T palier palier show pgvecto --json 2>/dev/null | python3 -c '
import json, sys
p = json.load(sys.stdin)["record"]["plan"]
for i, s in enumerate(p["steps"] or [], 1):
    print("  step", i, s["risk"], [c["service"] + " " + c["from"] + "→" + c["to"] for c in s["changes"]], [o["op"] for o in s["operations"]])
print("  withheld:", p.get("withheld") or "none")
print("  incomplete:", p.get("incomplete") or "none")'
apply_and_report pgvecto
echo "after: $(docker inspect pgvecto-db-1 --format '{{.Config.Image}}') | $(data)"
echo "volumes: $(docker volume ls -q | grep '^pgvecto_' | tr '\n' ' ')"

(cd /apps/pgvecto && docker compose down -v >/dev/null 2>&1)
docker volume ls -q | grep '^pgvecto_' | xargs -r docker volume rm >/dev/null
rm -rf /apps/pgvecto
