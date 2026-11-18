# A rollback does not pull: one service has pull_policy: always and an image whose registry does not answer, and the
# target of the other one exits at once. The stack must come back as it was, without asking the registry for anything.
mkdir -p /apps/nopull
docker tag busybox:1.37.0 registry.invalid/lab/side:1
cat >/apps/nopull/compose.yaml <<Y
services:
  cache:
    image: docker.io/library/memcached:1.6.30
    labels:
      palier.auto: "off"
  side:
    image: registry.invalid/lab/side:1
    pull_policy: always
    command: ["sleep", "infinity"]
Y
(cd /apps/nopull && docker compose up -d --pull missing >/dev/null 2>&1)
plan_ready() { $PALIER exec -T palier palier show nopull --json 2>/dev/null | python3 -c 'import json,sys; sys.exit(0 if (json.load(sys.stdin).get("record") or {}).get("plan", {}).get("steps") else 1)'; }
state() { for s in cache side; do echo "  $s: $(docker inspect nopull-$s-1 --format '{{.Config.Image}} {{.State.Status}}')"; done; }
$PALIER exec -T palier palier scan >/dev/null
until plan_ready; do sleep 5; done
target=$($PALIER exec -T palier palier show nopull --json | python3 -c 'import json,sys; print([c["to"] for s in json.load(sys.stdin)["record"]["plan"]["steps"] for c in s["changes"] if c["service"] == "cache"][0])')
printf 'FROM busybox:1.37.0\nCMD ["sh", "-c", "echo cache broken; exit 1"]\n' | docker build -q -t "docker.io/library/memcached:$target" - >/dev/null
echo "cache 1.6.30 → $target, local image memcached:$target that exits at once; side has pull_policy: always on registry.invalid"
echo "before:"; state
apply_and_report nopull
echo "after:"; state

docker rmi -f "docker.io/library/memcached:$target" >/dev/null 2>&1 || true
(cd /apps/nopull && docker compose down -v >/dev/null 2>&1)
rm -rf /apps/nopull
docker rmi registry.invalid/lab/side:1 >/dev/null 2>&1 || true
