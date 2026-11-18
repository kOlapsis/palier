# A stack whose update keeps failing: Palier waits a day after a rollback, stops after three in a row, and starts again
# once a run applied by hand passes. Its migration service, which ends with code 0, does not fail the checks.
mkdir -p /apps/loop
compose() { cat >/apps/loop/compose.yaml <<Y
services:
  cache:
    image: docker.io/library/memcached:1.6.30
$1  migrate:
    image: docker.io/library/busybox:1.36.1
    command: ["sh", "-c", "echo Migrations applied successfully"]
    restart: "no"
Y
(cd /apps/loop && docker compose up -d >/dev/null 2>&1); }
compose '    labels:
      palier.auto: "off"
'
plan_ready() { $PALIER exec -T palier palier show loop --json 2>/dev/null | python3 -c 'import json,sys; sys.exit(0 if (json.load(sys.stdin).get("record") or {}).get("plan", {}).get("steps") else 1)'; }
skipped() { docker logs --since "$1" palier-palier-1 2>&1 | grep '"stack":"loop"' | grep -o '"msg":"automatic apply[^}]*' | tail -1; }
runs() { sql "SELECT id || ' ' || status || ' ' || author FROM runs WHERE stack = 'loop' ORDER BY id" | tr '\n' ';'; }
broken() { printf 'FROM busybox:1.37.0\nCMD ["sh", "-c", "echo cache broken; exit 1"]\n' | docker build -q -t "docker.io/library/memcached:$target" - >/dev/null; }
$PALIER exec -T palier palier scan >/dev/null
until plan_ready; do sleep 5; done
target=$($PALIER exec -T palier palier show loop --json | python3 -c 'import json,sys; print([c["to"] for s in json.load(sys.stdin)["record"]["plan"]["steps"] for c in s["changes"] if c["service"] == "cache"][0])')
echo "cache 1.6.30 → $target, local image memcached:$target that exits at once"

echo "== automatic apply, then the next scans"
broken
compose ''
since=$(date -u +%Y-%m-%dT%H:%M:%S)
$PALIER exec -T palier palier scan >/dev/null
until [ -n "$(sql "SELECT 1 FROM runs WHERE stack = 'loop' AND status != 'running'")" ]; do sleep 5; done
echo "  runs: $(runs)"
for n in 1 2; do
	$PALIER exec -T palier palier scan >/dev/null; sleep 20
	echo "  scan $n: $(skipped "$since") | runs: $(runs)"
done

echo "== two more rollbacks, applied by hand"
for n in 1 2; do
	broken
	until plan_ready; do sleep 5; done
	$PALIER exec -T palier palier apply loop --yes --json </dev/null | python3 -c 'import json,sys; v=json.load(sys.stdin); print("  run", v["id"], v["status"])' || true
done
$PALIER exec -T palier palier scan >/dev/null; sleep 20
echo "  scan: $(skipped "$since")"
$PALIER exec -T palier palier status 2>/dev/null | grep '^loop' | cut -c1-200

echo "== the real image, applied by hand"
docker rmi -f "docker.io/library/memcached:$target" >/dev/null 2>&1 || true
until plan_ready; do sleep 5; done
apply_and_report loop
for s in cache migrate; do echo "  $s: $(docker inspect loop-$s-1 --format '{{.Config.Image}} {{.State.Status}} exit={{.State.ExitCode}}')"; done

(cd /apps/loop && docker compose down -v >/dev/null 2>&1)
rm -rf /apps/loop
