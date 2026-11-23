# Stops the operator adds to the plan (palier via), decisions taken back (palier forget), then the run in two steps
# and what Palier scans after it. A second stack updates on its own, to see what an automatic run scans.
# KEEP=1 stops before the run and leaves the stack with one stop and one ignored version, to look at its page.
reg=10.10.50.10:5443
repo=$reg/lab/steps
echo lab-registry-password | docker login $reg -u palier --password-stdin >/dev/null 2>&1
for v in 1.0.0 1.1.0 1.2.0 1.3.0; do
	lab_image "$repo:$v" "echo steps $v; exec sleep infinity"
	$PALIER exec -T palier palier forget stops web $v >/dev/null 2>&1 || true
	docker push -q "$repo:$v" >/dev/null
done
docker rmi "$repo:1.1.0" "$repo:1.2.0" "$repo:1.3.0" >/dev/null
sql "DELETE FROM tag_dates WHERE image = '$repo'"
mkdir -p /apps/stops
cat >/apps/stops/compose.yaml <<Y
services:
  web:
    image: $repo:1.0.0
    labels:
      palier.auto: "off"
      palier.maturation: 0s
Y
(cd /apps/stops && docker compose up -d >/dev/null 2>&1)
show() { $PALIER exec -T palier palier show "$1" --json 2>/dev/null | python3 -c '
import json, sys
v = json.load(sys.stdin)
s = v["stack"]["services"][0]
p = (v.get("record") or {}).get("plan") or {}
print("  available", [r["tag"] for r in s.get("available") or []], "| stops", s.get("via") or [], "| ignored", s.get("ignored") or [],
      "| plan", [[c["from"] + " → " + c["to"] for c in st["changes"]] for st in p.get("steps") or []] or "no step", "| incomplete:", p.get("incomplete") or "none")'; }
decide() {
	since=$(date -u +%FT%T); sleep 1
	echo "palier $*: $($PALIER exec -T palier palier "$@" 2>&1 | tr '\n' ' ')"
	settled "$2" "$since"
	show "$2"
}
scans() { docker logs --since "$1" palier-palier-1 2>&1 | python3 -c '
import json, sys
for line in sys.stdin:
    try: e = json.loads(line)
    except ValueError: continue
    if e.get("msg") in ("apply requested", "automatic apply", "scan started", "scan finished"):
        print("  ", e["time"][11:19], e["msg"], {k: v for k, v in e.items() if k not in ("time", "level", "msg")})'; }

rescan stops
echo "== before any decision"
show stops
echo "== stops"
decide via stops web 1.1.0
decide via stops web 1.2.0
echo "palier via stops web 9.9.9: $($PALIER exec -T palier palier via stops web 9.9.9 2>&1 | tr '\n' ' ')"
decide forget stops web 1.2.0
echo "== an ignored version, then taken back"
decide ignore stops web 1.3.0
$PALIER exec -T palier palier show stops 2>&1 | grep -E "stops at|ignored" | sed 's/^/  show: /'
[ -z "${KEEP:-}" ] || { docker logout $reg >/dev/null; exit 0; }
decide forget stops web 1.3.0
echo "== run"
start=$(date -u +%Y-%m-%dT%H:%M:%SZ)
apply_and_report stops
echo "  web: $(docker inspect stops-web-1 --format '{{.Config.Image}} {{.State.Status}}'), says $(docker logs stops-web-1 2>&1 | head -1)"
sleep 15
echo "  scans since the run was asked:"
scans "$start"
show stops

echo "== a stack that updates on its own"
mkdir -p /apps/selfrun
printf 'services:\n  web:\n    image: %s:1.0.0\n    labels:\n      palier.maturation: 0s\n' "$repo" >/apps/selfrun/compose.yaml
(cd /apps/selfrun && docker compose up -d >/dev/null 2>&1)
start=$(date -u +%Y-%m-%dT%H:%M:%SZ)
$PALIER exec -T palier palier scan >/dev/null 2>&1
for i in $(seq 1 60); do
	[ "$(sql "SELECT status FROM runs WHERE stack = 'selfrun' ORDER BY id DESC LIMIT 1")" = done ] && break
	sleep 5
done
sleep 15
echo "  $(sql "SELECT 'run ' || id || ' ' || status || ', started by ' || author FROM runs WHERE stack = 'selfrun' ORDER BY id DESC LIMIT 1"); web: $(docker inspect selfrun-web-1 --format '{{.Config.Image}} {{.State.Status}}')"
scans "$start"
finished=$(sql "SELECT finished FROM runs WHERE stack = 'selfrun' ORDER BY id DESC LIMIT 1")
echo "  services read again since the run ended: $(docker logs --since "$finished" palier-palier-1 2>&1 | sed -n 's/.*"msg":"service detected","stack":"\([^"]*\)","service":"\([^"]*\)".*/\1\/\2/p' | sort -u | tr '\n' ' ')"

for s in stops selfrun; do (cd /apps/$s && docker compose down -v --rmi all >/dev/null 2>&1); rm -rf /apps/$s; done
docker logout $reg >/dev/null
