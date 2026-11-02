# The export of postgres is cut in half before the import: the comparison fails and Outline goes back to postgres 16 on the old volume.
echo "before: $(pg_counts outline-postgres-1) ; montages : $(mounts_of outline-postgres-1)"
outline_state
previous=$(sql "SELECT coalesce(max(id), 0) FROM runs")
(
	until sql "SELECT message FROM run_events WHERE run_id > $previous" | grep -q 'on the new volume'; do sleep 0.2; done
	dump=$(ls /var/lib/docker/volumes/palier_palier-data/_data/dumps/run*-postgres.sql | tail -1)
	size=$(stat -c %s "$dump")
	truncate -s $((size / 2)) "$dump"
	echo "$(date -u +%T) export cut: $dump from $size to $((size / 2)) bytes"
) &
start=$(date +%s)
$PALIER exec -T palier /palier apply outline </dev/null | python3 -c 'import json,sys; v=json.load(sys.stdin); print("run", v["id"], v["status"], v.get("error", "")[:400]); [print("  ", e["at"][11:19], e["message"].splitlines()[0][:200]) for e in v["events"]]' || true
wait
echo "duration: $(( $(date +%s) - start )) s"
echo "after: $(pg_counts outline-postgres-1) ; montages : $(mounts_of outline-postgres-1)"
outline_state
docker volume ls -q | grep -E 'postgres-data' | tr '\n' ' '; echo
