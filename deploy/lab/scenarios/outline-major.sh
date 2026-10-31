# Moves postgres to the major Outline supports through export and import, then reads the old volume again.
before_image=$(docker inspect outline-postgres-1 --format '{{.Config.Image}}')
echo "avant : $(pg_counts outline-postgres-1) ; montages : $(mounts_of outline-postgres-1)"
outline_state
start=$(date +%s)
$PALIER exec -T palier /palier apply outline </dev/null | python3 -c 'import json,sys; v=json.load(sys.stdin); print("exécution", v["id"], v["status"], v.get("error", "")[:400]); [print("  ", e["at"][11:19], e["message"].splitlines()[0][:220]) for e in v["events"]]' || true
echo "durée : $(( $(date +%s) - start )) s"
echo "après : $(pg_counts outline-postgres-1) ; montages : $(mounts_of outline-postgres-1)"
outline_state
grep -A3 -E 'image: docker.io/library/postgres' /apps/outline/compose.yaml
old_volume_counts "$before_image"
curl -s -o /dev/null -w "outline _health %{http_code}\n" http://127.0.0.1:3000/_health
