# The image of the third step of the Outline plan exits at once: the stack must stay at the verified second step.
step=$(sql "SELECT json_extract(body, '$.steps[2].changes[0].service') || ' ' || json_extract(body, '$.steps[2].changes[0].to') FROM plans WHERE stack = 'outline'")
set -- $step
[ -n "${2:-}" ] || { echo "pas de troisième étape dans le plan d'outline"; exit 1; }
repo=$(docker inspect "outline-$1-1" --format '{{.Config.Image}}'); repo=${repo%:*}
printf 'FROM busybox:1.37.0\nCMD ["sh", "-c", "echo %s cassé; exit 1"]\n' "$1" | docker build -q -t "$repo:$2" - >/dev/null
echo "étape 3 : $1 vers $2 ; image locale $repo:$2 qui quitte à l'instant"
echo "avant :"; outline_state
start=$(date +%s)
$PALIER exec -T palier /palier apply outline </dev/null | python3 -c 'import json,sys; v=json.load(sys.stdin); print("exécution", v["id"], v["status"], v.get("error", "").splitlines()[0]); [print("  ", e["at"][11:19], e["message"].splitlines()[0]) for e in v["events"]]' || true
echo "durée : $(( $(date +%s) - start )) s"
echo "après :"; outline_state
grep image: /apps/outline/compose.yaml
docker rmi "$repo:$2" >/dev/null
