# Applies the plan of the Outline stack and checks that the seeded documents are still there.
echo "avant :"; outline_state
start=$(date +%s)
$PALIER exec -T palier /palier apply outline </dev/null | python3 -c 'import json,sys; v=json.load(sys.stdin); print("exécution", v["id"], v["status"], v.get("error", "")); [print("  ", e["at"][11:19], e["message"].splitlines()[0]) for e in v["events"]]' || true
echo "durée : $(( $(date +%s) - start )) s"
echo "après :"; outline_state
grep image: /apps/outline/compose.yaml
