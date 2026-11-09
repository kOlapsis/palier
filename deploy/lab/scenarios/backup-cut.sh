# The export of pgcoll breaks in the middle of its upload to MinIO: pg_dump is killed while it streams a 1.2 million row table.
# The run must be abandoned with pgcoll left on 16.4, and the partial dump.sql that rclone wrote must be gone from MinIO.
docker inspect palier-palier-1 --format '{{range .Config.Env}}{{println .}}{{end}}' | grep '^RCLONE_' >/tmp/palier-rclone.env
rc() { docker run --rm -i --env-file /tmp/palier-rclone.env docker.io/rclone/rclone:1.75.2 -q "$@"; }
started=$(date +%s)
(cd /apps/pgcoll && docker compose down -v >/dev/null 2>&1 && sed -i 's|image: docker.io/library/postgres:.*|image: docker.io/library/postgres:16.4|' compose.yaml && docker compose up -d >/dev/null 2>&1)
until docker exec pgcoll-postgres-1 psql -U lab -d lab -h 127.0.0.1 -c 'SELECT count(*) FROM words' >/dev/null 2>&1; do sleep 2; done
docker exec pgcoll-postgres-1 psql -U lab -d lab -q -c "CREATE TABLE big AS SELECT g AS id, md5(g::text) || repeat('x', 200) AS filler FROM generate_series(1, 1200000) g"
echo "before: $(docker inspect pgcoll-postgres-1 --format '{{.Config.Image}}'), $(docker exec pgcoll-postgres-1 psql -U lab -d lab -At -c "SELECT 'big ' || count(*) || ' rows, ' || pg_size_pretty(pg_total_relation_size('big')) FROM big")"
rc purge minio:palier/pgcoll 2>/dev/null || true
docker exec palier-palier-1 palier plan pgcoll --once >/dev/null 2>&1
(
	for _ in $(seq 1 1200); do pgrep -f 'bin/pg_dump .*dbname=lab' >/dev/null && break; sleep 0.1; done
	sleep 2
	pkill -f 'bin/pg_dump .*dbname=lab' && echo "pg_dump of lab killed at $(date +%T)"
) &
killer=$!
(
	seen=
	for _ in $(seq 1 600); do
		l=$(rc lsf -R --files-only minio:palier/pgcoll 2>/dev/null | tr '\n' ' ')
		[ -n "$l" ] && [ "$l" != "$seen" ] && { echo "seen on MinIO at $(date +%T): $l"; seen=$l; }
		[ -n "$seen" ] && [ -z "$l" ] && { echo "gone from MinIO at $(date +%T)"; break; }
		sleep 0.3
	done
) &
watcher=$!
apply_and_report pgcoll
sleep 3; kill $watcher $killer 2>/dev/null || true; wait $watcher $killer 2>/dev/null || true
echo "after: $(docker inspect pgcoll-postgres-1 --format '{{.Config.Image}} {{.State.Status}}'), words $(docker exec pgcoll-postgres-1 psql -U lab -d lab -At -c 'SELECT count(*) FROM words')"
echo "files left under minio:palier/pgcoll: [$(rc lsf -R --files-only minio:palier/pgcoll 2>/dev/null | tr '\n' ' ')]"
curl -s http://127.0.0.1:8081/api/session/00000000-0000-4000-8000-000000000001/requests | python3 -c '
import base64, json, sys
start = int(sys.argv[1]) * 1000
for r in sorted((r for r in json.load(sys.stdin) if r["captured_at_unix_milli"] >= start), key=lambda r: r["captured_at_unix_milli"]):
    e = json.loads(base64.b64decode(r["request_payload_base64"]))
    if e["event"].startswith("backup_") or e["event"].startswith("run_"):
        print(" ", e["event"], e["stack"], e.get("service") or "", e.get("kind") or "", (e.get("error") or "")[:300])
' "$started"
docker exec pgcoll-postgres-1 psql -U lab -d lab -q -c 'DROP TABLE big'
