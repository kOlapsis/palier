# Applies the plan of the Outline stack and checks that the seeded documents are still there.
echo "before:"; outline_state
start=$(date +%s)
$PALIER exec -T palier palier apply outline --yes --json </dev/null | python3 -c 'import json,sys; v=json.load(sys.stdin); print("run", v["id"], v["status"], v.get("error", "")); [print("  ", e["at"][11:19], e["message"].splitlines()[0]) for e in v["events"]]' || true
echo "duration: $(( $(date +%s) - start )) s"
echo "after:"; outline_state
grep image: /apps/outline/compose.yaml
