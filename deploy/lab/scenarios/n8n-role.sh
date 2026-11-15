# n8n with external task runners that depend on it: inferred roles, then the palier.role label, then the major upgrade.
mkdir -p /apps/n8n
printf 'N8N_VERSION=2.45.7\n' >/apps/n8n/.env
cat >/apps/n8n/compose.yaml <<'EOF'
services:
  n8n:
    image: docker.io/n8nio/n8n:${N8N_VERSION}
    restart: always
    environment:
      N8N_RUNNERS_ENABLED: "true"
      N8N_RUNNERS_MODE: external
      N8N_RUNNERS_BROKER_LISTEN_ADDRESS: 0.0.0.0
      N8N_RUNNERS_AUTH_TOKEN: palier-lab-runners
    volumes:
      - n8n-data:/home/node/.n8n
    labels:
      palier.auto: "off"
  task-runners:
    image: docker.io/n8nio/runners:${N8N_VERSION}
    restart: always
    environment:
      N8N_RUNNERS_TASK_BROKER_URI: http://n8n:5679
      N8N_RUNNERS_AUTH_TOKEN: palier-lab-runners
    depends_on:
      - n8n
    labels:
      palier.auto: "off"
volumes:
  n8n-data: {}
EOF
up() {
	(cd /apps/n8n && docker compose up -d --wait >/dev/null 2>&1)
	for i in $(seq 1 30); do docker exec n8n-n8n-1 wget -qO- http://127.0.0.1:5678/healthz >/dev/null 2>&1 && break; sleep 2; done
}
state() {
	for s in n8n task-runners; do echo "  $s: $(docker inspect n8n-$s-1 --format '{{.Config.Image}} {{.State.Status}} restarts={{.RestartCount}} role-label={{index .Config.Labels "palier.role"}}')"; done
	echo "  .env: $(cat /apps/n8n/.env)"
	echo "  n8n /healthz: $(docker exec n8n-n8n-1 wget -qO- http://127.0.0.1:5678/healthz 2>&1)"
	echo "  last lines of task-runners:"; docker logs --tail 4 n8n-task-runners-1 2>&1 | sed 's/^/    /'
	echo "  n8n lines about runners:"; docker logs n8n-n8n-1 2>&1 | grep -i runner | tail -4 | sed 's/^/    /'
}
roles() {
	$PALIER exec -T palier palier scan --once 2>/dev/null | python3 -c '
import json, sys
for st in json.load(sys.stdin):
    if st["name"] == "n8n":
        for s in st["services"]:
            print("  %-13s %-12s %-8s available %s" % (s["name"], s["role"], s.get("version"), [r["tag"] for r in s.get("available") or []][-3:]))
        print("  stack errors:", st.get("errors") or "none")'
}
planned() {
	$PALIER exec -T palier palier plan --once n8n --json 2>/dev/null | python3 -c '
import json, sys
p = json.load(sys.stdin)["plan"]
for i, s in enumerate(p["steps"], 1):
    print("  step", i, s["risk"], s["reversible"], [c["service"] + " " + c["from"] + "→" + c["to"] for c in s["changes"]], "|", s["why"][:160])
print("  withheld:", p.get("withheld") or "none")
print("  incomplete:", p.get("incomplete") or "none")'
}
label() { python3 - "$1" <<'EOF'
import re, sys
f = "/apps/n8n/compose.yaml"
s = re.sub(r"\n      palier\.role: .*", "", open(f).read())
if sys.argv[1]:
    s = s.replace('    labels:\n      palier.auto: "off"\n  task-runners:', '    labels:\n      palier.auto: "off"\n      palier.role: ' + sys.argv[1] + '\n  task-runners:', 1)
open(f, "w").write(s)
EOF
}

echo "== without label"
up
state
roles
planned
echo "== palier.role: sidecar on n8n"
label sidecar && up
roles
echo "== palier.role: application on n8n"
label application && up
state
roles
planned
echo "== apply"
$PALIER exec -T palier palier scan >/dev/null
for i in $(seq 1 30); do
	$PALIER exec -T palier palier show n8n --json 2>/dev/null | python3 -c 'import json,sys; sys.exit(0 if json.load(sys.stdin).get("record") else 1)' 2>/dev/null && break
	sleep 10
done
$PALIER exec -T palier palier show n8n | head -12
apply_and_report n8n
state

(cd /apps/n8n && docker compose down -v --rmi all >/dev/null 2>&1)
rm -rf /apps/n8n
