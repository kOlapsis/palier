# A plan whose run is rolled back twice is computed again with the full notes of the step that did not pass.
# KEEP=1 leaves the stack in place, to look at its page.
mkdir -p /apps/diag
cat >/apps/diag/compose.yaml <<'Y'
services:
  kuma:
    image: docker.io/louislam/uptime-kuma:2.5.5
    volumes:
      - data:/app/data
    labels:
      palier.auto: "off"
volumes:
  data: {}
Y
(cd /apps/diag && docker compose up -d --wait >/dev/null 2>&1)
record() { $PALIER exec -T palier palier show diag --json 2>/dev/null | python3 -c "$1" "${2:-}"; }
since=$(date -u +%Y-%m-%dT%H:%M:%S)
$PALIER exec -T palier palier scan >/dev/null
for i in $(seq 1 60); do
	record 'import json,sys; r=json.load(sys.stdin).get("record") or {}; sys.exit(0 if r.get("created","") > sys.argv[1] and r.get("source") == "ai" else 1)' "$since" && break
	sleep 10
done
show() { record '
import json, sys
p = json.load(sys.stdin)["record"]["plan"]
for i, s in enumerate(p["steps"] or [], 1):
    print("  step", i, s["risk"], [c["service"] + " " + c["from"] + "→" + c["to"] for c in s["changes"]], "|", s["why"][:200])
print("  refined:", p.get("refined") or "no")
print("  incomplete:", p.get("incomplete") or "none")'; }
echo "== first plan"; show
target=$(record 'import json,sys; print(json.load(sys.stdin)["record"]["plan"]["steps"][0]["changes"][0]["to"])')
printf 'FROM busybox:1.37.0\nCMD ["sh", "-c", "echo kuma broken; exit 1"]\n' | docker build -q -t "docker.io/louislam/uptime-kuma:$target" - >/dev/null
echo "local image docker.io/louislam/uptime-kuma:$target that exits at once"
for n in 1 2; do
	for i in $(seq 1 30); do record 'import json,sys; sys.exit(0 if json.load(sys.stdin).get("record") else 1)' && break; sleep 5; done
	$PALIER exec -T palier palier apply diag --yes --json </dev/null | python3 -c 'import json,sys; v=json.load(sys.stdin); print("run", v["id"], v["status"], "|", (v.get("error") or "").splitlines()[0][:160])' || true
done
echo "kuma: $(docker inspect diag-kuma-1 --format '{{.Config.Image}} {{.State.Status}}')"
docker rmi -f "docker.io/louislam/uptime-kuma:$target" >/dev/null 2>&1 || true
for i in $(seq 1 40); do
	record 'import json,sys; sys.exit(0 if (json.load(sys.stdin).get("record") or {}).get("plan", {}).get("refined") else 1)' && break
	sleep 10
done
echo "== plan after two rolled back runs"; show
$PALIER exec -T palier palier show diag 2>/dev/null | sed -n '/^Plan/,/^$/p' | cut -c1-400
docker logs --since 20m palier-palier-1 2>&1 | grep -E 'computing plan again|plan not computed again' | cut -c1-400

[ -z "${KEEP:-}" ] || exit 0
(cd /apps/diag && docker compose down -v >/dev/null 2>&1)
rm -rf /apps/diag
