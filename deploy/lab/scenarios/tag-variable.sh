# An image whose tag holds a variable and a suffix (redis:${REDIS_VERSION}-alpine): Palier changes the variable in the
# env file, without the suffix.
mkdir -p /apps/tagvar
printf 'REDIS_VERSION=7.4.0\n' >/apps/tagvar/.env
cat >/apps/tagvar/compose.yaml <<'Y'
services:
  cache:
    image: docker.io/library/redis:${REDIS_VERSION}-alpine
    labels:
      palier.auto: "off"
Y
(cd /apps/tagvar && docker compose up -d --wait >/dev/null 2>&1)
state() { echo "  $(docker inspect tagvar-cache-1 --format '{{.Config.Image}} {{.State.Status}}') | .env: $(cat /apps/tagvar/.env) | compose: $(grep -o 'image: .*' /apps/tagvar/compose.yaml)"; }
echo "before:"; state
since=$(date -u +%Y-%m-%dT%H:%M:%S)
$PALIER exec -T palier palier scan >/dev/null
for i in $(seq 1 30); do
	$PALIER exec -T palier palier show tagvar --json 2>/dev/null | python3 -c 'import json,sys; r=json.load(sys.stdin).get("record") or {}; sys.exit(0 if r.get("created","") > sys.argv[1] else 1)' "$since" && break
	sleep 5
done
apply_and_report tagvar
echo "after:"; state

(cd /apps/tagvar && docker compose down -v >/dev/null 2>&1)
rm -rf /apps/tagvar
