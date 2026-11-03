# authentik 2025.8.3 is touched by advisories; those patched on its own line must be closed by a first step on the 2025.8
# line, before the climb. The stack is only created: a plan does not need it to run.
mkdir -p /apps/authsec
printf 'services:\n  server:\n    image: ghcr.io/goauthentik/server:2025.8.3\n    command: server\n' > /apps/authsec/compose.yaml
(cd /apps/authsec && docker compose pull -q && docker compose create >/dev/null 2>&1)
show_plan() {
	python3 -c '
import json, sys
rec = json.load(sys.stdin)
for i, s in enumerate(rec["plan"]["steps"] or [], 1):
    print("  step", i, ", ".join(c["service"] + " " + c["from"] + " -> " + c["to"] for c in s["changes"]), "|", s.get("why", "")[:170])
for r in rec["plan"].get("incomplete") or []:
    print("  incomplete:", r[:160])
'
}
echo "== plan from versions only"
$PALIER exec -T -e PALIER_AI_KEY= palier palier plan authsec --once 2>/tmp/plan-versions.log | show_plan
grep -o '"msg":"security fix first".*' /tmp/plan-versions.log | head -2
echo "== plan of the AI"
$PALIER exec -T palier palier plan authsec --once 2>/tmp/plan-ai.log | show_plan
grep -o '"msg":"security fix first".*' /tmp/plan-ai.log | head -2
(cd /apps/authsec && docker compose down >/dev/null 2>&1); rm -rf /apps/authsec
