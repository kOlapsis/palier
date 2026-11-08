# A service whose explicit registry refuses Palier is reported and its problem sent on every channel; a name Docker Hub does not list stays "not in its registry", shown as a notice and never sent.
docker pull -q busybox:1.37.0 >/dev/null
docker tag busybox:1.37.0 ghcr.io/kolapsis/palier-lab-missing:1.0
docker tag busybox:1.37.0 palier-lab-local:1.0
mkdir -p /apps/refused
printf 'services:\n  app:\n    image: ghcr.io/kolapsis/palier-lab-missing:1.0\n    command: ["sleep", "infinity"]\n  local:\n    image: palier-lab-local:1.0\n    command: ["sleep", "infinity"]\n' > /apps/refused/compose.yaml
(cd /apps/refused && docker compose up -d --pull never >/dev/null 2>&1)
$PALIER exec -T palier palier scan >/dev/null
$PALIER exec -T palier palier show refused | sed -n '1,8p'
$PALIER exec -T palier palier show refused --json | python3 -c 'import json,sys; v=json.load(sys.stdin); [print(" ", s["name"], {k: s.get(k) for k in ("registry_refused", "not_in_registry", "detection_error")}) for s in v["stack"]["services"]]' 2>/dev/null || true
until [ "$(sql "SELECT count(*) FROM outbox WHERE subject = 'refused: the registry of app refuses Palier' AND sent != ''")" -ge 5 ]; do sleep 5; done
sql "SELECT channel, attempts, sent FROM outbox WHERE subject = 'refused: the registry of app refuses Palier'"
echo "open problems of the stack:"; sql "SELECT '  ' || level || ': ' || title FROM incidents WHERE stack = 'refused' AND resolved = ''"
echo "messages about local: $(sql "SELECT count(*) FROM outbox WHERE subject LIKE '%local%'")"
[ -n "${KEEP:-}" ] && exit 0
(cd /apps/refused && docker compose down >/dev/null 2>&1); rm -rf /apps/refused
docker rmi ghcr.io/kolapsis/palier-lab-missing:1.0 palier-lab-local:1.0 >/dev/null
$PALIER exec -T palier palier scan >/dev/null
until [ "$(sql "SELECT count(*) FROM outbox WHERE subject = 'Resolved: refused: the registry of app refuses Palier' AND sent != ''")" -ge 5 ]; do sleep 5; done
echo "resolved sent on $(sql "SELECT group_concat(channel) FROM outbox WHERE subject = 'Resolved: refused: the registry of app refuses Palier'")"
