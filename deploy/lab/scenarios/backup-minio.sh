# Backups to MinIO before each update: pgcoll (dump and volumes) from a fresh 16.4, MariaDB 10.11.9 and MySQL 8.4.2 (dump) on their old volumes.
# Each archive is read back from MinIO, checked against its .sha256, restored into a fresh container; four old pgcoll backups test the rotation.
docker inspect palier-palier-1 --format '{{range .Config.Env}}{{println .}}{{end}}' | grep '^RCLONE_' >/tmp/palier-rclone.env
rc() { docker run --rm -i --env-file /tmp/palier-rclone.env docker.io/rclone/rclone:1.75.2 -q "$@"; }
label() { grep -q 'palier.backup:' "$1" || sed -i "/^  $2:\$/,/^    restart:/ s|^    restart: always\$|    restart: always\n    labels:\n      palier.backup: $3|" "$1"; }
verify() {
	want=$(rc cat "$1.sha256")
	got=$(rc cat "$1" | sha256sum | cut -d' ' -f1)
	echo "  $1: $(rc size --json "$1" | python3 -c 'import json,sys; print(json.load(sys.stdin)["bytes"])') bytes, sha256 read back $got; .sha256: $want"
	[ "$got" = "${want%% *}" ] && echo "  identical" || echo "  DIFFERENT"
}
latest() { rc lsf --dirs-only "minio:palier/$1" | grep -E '^[0-9]{8}T[0-9]{6}Z/$' | sort | tail -1 | tr -d /; }
started=$(date +%s)

echo "== pgcoll"
(cd /apps/pgcoll && docker compose down -v >/dev/null 2>&1 && sed -i 's|image: docker.io/library/postgres:.*|image: docker.io/library/postgres:16.4|' compose.yaml)
label /apps/pgcoll/compose.yaml postgres dump,volumes
(cd /apps/pgcoll && docker compose up -d >/dev/null 2>&1)
until docker exec pgcoll-postgres-1 psql -U lab -d lab -h 127.0.0.1 -c 'SELECT count(*) FROM words' >/dev/null 2>&1; do sleep 2; done
docker exec pgcoll-postgres-1 psql -U lab -d lab -c "INSERT INTO words (word) VALUES ('backup-$(date +%s)')" >/dev/null
words=$(docker exec pgcoll-postgres-1 psql -U lab -d lab -At -c 'SELECT count(*) FROM words')
echo "before: postgres 16.4, words $words"
rc purge minio:palier/pgcoll 2>/dev/null || true
for d in 20261101T000000Z 20261102T000000Z 20261103T000000Z 20261104T000000Z; do echo old | rc rcat "minio:palier/pgcoll/postgres/$d/dump.sql"; done
echo keep | rc rcat minio:palier/pgcoll/postgres/notes/readme.txt
echo "on MinIO before: $(rc lsf minio:palier/pgcoll/postgres | tr '\n' ' ')"
docker exec palier-palier-1 palier plan pgcoll --once >/dev/null 2>&1
apply_and_report pgcoll
echo "after: $(docker inspect pgcoll-postgres-1 --format '{{.Config.Image}} {{.State.Status}}'), words $(docker exec pgcoll-postgres-1 psql -U lab -d lab -At -c 'SELECT count(*) FROM words')"
echo "on MinIO after: $(rc lsf minio:palier/pgcoll/postgres | tr '\n' ' ')"
dir=minio:palier/pgcoll/postgres/$(latest pgcoll/postgres)
verify "$dir/dump.sql"
docker rm -f pgcheck >/dev/null 2>&1 || true
docker run -d --name pgcheck -e POSTGRES_PASSWORD=unused docker.io/library/postgres:16.4 >/dev/null
until docker exec pgcheck pg_isready -U postgres -h 127.0.0.1 >/dev/null 2>&1; do sleep 1; done
rc cat "$dir/dump.sql" | docker exec -i pgcheck psql -q -U postgres -d postgres >/dev/null 2>&1
echo "  dump restored into a fresh postgres:16.4: words $(docker exec pgcheck psql -U lab -d lab -At -c 'SELECT count(*) FROM words')"
docker rm -f pgcheck >/dev/null
verify "$dir/volumes.tar"
echo "  content: $(rc cat "$dir/volumes.tar" | tar -tf - | grep -c .) entries, $(rc cat "$dir/volumes.tar" | tar -tf - | grep -E '^[^/]+/[^/]+/[^/]+/[^/]+/$|seed.sql' | tr '\n' ' ')"
docker volume rm -f pgcheck-data >/dev/null
rc cat "$dir/volumes.tar" | docker run --rm -i -v pgcheck-data:/restore docker.io/library/busybox:1.37.0 tar -C /restore -xf -
docker run -d --name pgcheck -v pgcheck-data:/restore -e PGDATA=/restore/var/lib/postgresql/data docker.io/library/postgres:16.4 >/dev/null
until docker exec pgcheck pg_isready -U lab -h 127.0.0.1 >/dev/null 2>&1; do sleep 1; done
echo "  volumes extracted into a new volume, postgres:16.4 started on them: words $(docker exec pgcheck psql -U lab -d lab -At -c 'SELECT count(*) FROM words')"
docker rm -f pgcheck >/dev/null; docker volume rm pgcheck-data >/dev/null

for db in mariadb mysql; do
	client=$db old=10.11.9 major=13; [ $db = mariadb ] || { old=8.4.2 major=26; }
	[ $db = mysql ] || client=mariadb
	echo "== $db"
	f=/apps/$db/compose.yaml
	sed -i -e "s|image: docker.io/library/$db:.*|image: docker.io/library/$db:$old|" -e "s|- data-$major:/var/lib/mysql\$|- data:/var/lib/mysql|" -e "/^  data-$major: {}\$/d" "$f"
	label "$f" "$db" dump
	(cd /apps/$db && docker compose up -d --wait >/dev/null 2>&1); docker volume rm "${db}_data-$major" >/dev/null 2>&1 || true
	until docker exec -e MYSQL_PWD=palier-lab-password "$db-$db-1" "$client" -uroot -h 127.0.0.1 -e 'SELECT 1' >/dev/null 2>&1; do sleep 2; done
	count() { docker exec -e MYSQL_PWD="$2" "$1" "$client" -uroot -N -B -e "SELECT CONCAT(VERSION(), '; customers ', (SELECT COUNT(*) FROM shop.customers), '; orders ', COUNT(*)) FROM shop.orders" 2>&1; }
	echo "before: $(count "$db-$db-1" palier-lab-password)"
	rc purge "minio:palier/$db" 2>/dev/null || true
	docker exec palier-palier-1 palier plan "$db" --once >/dev/null 2>&1
	apply_and_report "$db"
	echo "after: $(count "$db-$db-1" palier-lab-password)"
	dir=minio:palier/$db/$db/$(latest "$db/$db")
	verify "$dir/dump.sql"
	docker rm -f dbcheck >/dev/null 2>&1 || true
	docker run -d --name dbcheck -e MYSQL_ROOT_PASSWORD=unused -e MARIADB_ROOT_PASSWORD=unused "docker.io/library/$db:$old" >/dev/null
	until docker exec -e MYSQL_PWD=unused dbcheck "$client" -uroot -h 127.0.0.1 --protocol=TCP -e 'SELECT 1' >/dev/null 2>&1; do sleep 2; done
	rc cat "$dir/dump.sql" | docker exec -i -e MYSQL_PWD=unused dbcheck "$client" -uroot
	echo "  dump restored into a fresh $db:$old: $(count dbcheck unused)"
	docker rm -f dbcheck >/dev/null
done

echo "== webhooks since the start"
curl -s http://127.0.0.1:8081/api/session/00000000-0000-4000-8000-000000000001/requests | python3 -c '
import base64, json, sys
start = int(sys.argv[1]) * 1000
for r in sorted((r for r in json.load(sys.stdin) if r["captured_at_unix_milli"] >= start), key=lambda r: r["captured_at_unix_milli"]):
    e = json.loads(base64.b64decode(r["request_payload_base64"]))
    if e["event"].startswith("backup_"):
        print(" ", e["event"], e["stack"], e["service"], e["kind"], e.get("engine") or "", ",".join(e.get("databases") or []), e.get("location"), e.get("size"), e.get("sha256"), "%ss" % e.get("seconds"), e.get("error") or "")
    else:
        print(" ", e["event"], e.get("stack"), e.get("status") or "")
' "$started"
