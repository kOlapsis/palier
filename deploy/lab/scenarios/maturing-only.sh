# A stack with nothing to apply: the application has only a version still maturing, its database only a newer major
# that the application does not cite. The containers are only created: a scan does not need them to run.
# APP_IMAGE, DB_IMAGE: the images, by default tymeslot 1.48.1-slim and postgres 18.7-alpine3.24; DB_MATURATION, 24h by
# default, lets postgres 19 out of its maturation while tymeslot keeps the 72 h of the bench. KEEP=1 leaves the stack in place.
mkdir -p /apps/onlymat
cat >/apps/onlymat/compose.yaml <<Y
services:
  app:
    image: ${APP_IMAGE:-docker.io/luka1thb/tymeslot:1.48.1-slim}
    depends_on: [db]
  db:
    image: ${DB_IMAGE:-docker.io/library/postgres:18.7-alpine3.24}
    environment:
      POSTGRES_PASSWORD: lab
    labels:
      palier.maturation: ${DB_MATURATION:-24h}
Y
(cd /apps/onlymat && docker compose pull -q >/dev/null 2>&1; docker compose create >/dev/null 2>&1)
rescan onlymat
$PALIER exec -T palier palier show onlymat --json | python3 -c '
import json, sys
v = json.load(sys.stdin)
print("state:", v["state"])
for s in v["stack"]["services"]:
    print("  %-3s %-12s available %s | maturing %s | notes %d" % (s["name"], s.get("version"), [r["tag"] for r in s.get("available") or []][-3:],
        [r["tag"] + " " + r.get("pushed", "")[:16] for r in s.get("maturing") or []], len(s.get("notes") or [])))
r = v.get("record") or {}
p = r.get("plan") or {}
print("plan from %s: %d steps" % (r.get("source"), len(p.get("steps") or [])), "| incomplete:", p.get("incomplete") or "none", "| left out:", p.get("withheld") or "none")'
$PALIER exec -T palier palier show onlymat | sed -n '/^Plan/,$p' | cut -c1-220
docker logs --since 2m palier-palier-1 2>&1 | grep -E '"(plan computed|computing plan with AI|reading release notes)".*onlymat' | cut -c1-240

[ -z "${KEEP:-}" ] || exit 0
(cd /apps/onlymat && docker compose down -v >/dev/null 2>&1)
rm -rf /apps/onlymat
