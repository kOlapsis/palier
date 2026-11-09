# palier.backup.to sends one service elsewhere than PALIER_BACKUP_TO (minio:palier): MariaDB to the WebDAV server (dav:palier), with four old
# backups there to test the rotation, and the database of Immich to the SFTP server (sftp:backups), after redis is put back on 6.2.23.
# Each export is read back, checked against its .sha256 and restored into a fresh container of the same image.
docker inspect palier-palier-1 --format '{{range .Config.Env}}{{println .}}{{end}}' | grep '^RCLONE_' >/tmp/palier-rclone.env
rc() { docker run --rm -i --env-file /tmp/palier-rclone.env docker.io/rclone/rclone:1.75.2 -q "$@"; }
verify() {
	want=$(rc cat "$1.sha256")
	got=$(rc cat "$1" | sha256sum | cut -d' ' -f1)
	echo "  $1: sha256 read back $got; .sha256: $want"
	[ "$got" = "${want%% *}" ] && echo "  identical" || echo "  DIFFERENT"
}
latest() { rc lsf --dirs-only "$1" | grep -E '^[0-9]{8}T[0-9]{6}Z/$' | sort | tail -1 | tr -d /; }
started=$(date +%s)

echo "== mariadb to WebDAV"
f=/apps/mariadb/compose.yaml
sed -i -e "s|image: docker.io/library/mariadb:.*|image: docker.io/library/mariadb:10.11.9|" -e "s|- data-13:/var/lib/mysql\$|- data:/var/lib/mysql|" -e "/^  data-13: {}\$/d" "$f"
grep -q 'palier.backup.to:' "$f" || sed -i 's|^      palier.backup: dump$|      palier.backup: dump\n      palier.backup.to: dav:palier|' "$f"
grep -A3 'labels:' "$f"
(cd /apps/mariadb && docker compose up -d --wait >/dev/null 2>&1); docker volume rm mariadb_data-13 >/dev/null 2>&1 || true
until docker exec -e MYSQL_PWD=palier-lab-password mariadb-mariadb-1 mariadb -uroot -h 127.0.0.1 -e 'SELECT 1' >/dev/null 2>&1; do sleep 2; done
count() { docker exec -e MYSQL_PWD="$2" "$1" mariadb -uroot -N -B -e "SELECT CONCAT(VERSION(), '; customers ', (SELECT COUNT(*) FROM shop.customers), '; orders ', COUNT(*)) FROM shop.orders" 2>&1; }
echo "before: $(count mariadb-mariadb-1 palier-lab-password)"
rc purge dav:palier/mariadb 2>/dev/null || true
for d in 20261101T000000Z 20261102T000000Z 20261103T000000Z 20261104T000000Z; do echo old | rc rcat "dav:palier/mariadb/mariadb/$d/dump.sql"; done
echo "on WebDAV before: $(rc lsf dav:palier/mariadb/mariadb | tr '\n' ' ')"
minio_before=$(rc lsf -R minio:palier/mariadb 2>/dev/null | grep -c dump.sql$)
docker exec palier-palier-1 palier plan mariadb --once >/dev/null 2>&1
apply_and_report mariadb
echo "after: $(count mariadb-mariadb-1 palier-lab-password)"
echo "on WebDAV after: $(rc lsf dav:palier/mariadb/mariadb | tr '\n' ' ')"
echo "on the WebDAV server disk: $(find /opt/palier/webdav/palier/mariadb -type f -newermt "@$started" -printf '%P %s bytes, ')"
echo "dumps of mariadb on MinIO: $minio_before before, $(rc lsf -R minio:palier/mariadb 2>/dev/null | grep -c dump.sql$) after"
dir=dav:palier/mariadb/mariadb/$(latest dav:palier/mariadb/mariadb)
verify "$dir/dump.sql"
docker rm -f dbcheck >/dev/null 2>&1 || true
docker run -d --name dbcheck -e MARIADB_ROOT_PASSWORD=unused docker.io/library/mariadb:10.11.9 >/dev/null
until docker exec -e MYSQL_PWD=unused dbcheck mariadb -uroot -h 127.0.0.1 --protocol=TCP -e 'SELECT 1' >/dev/null 2>&1; do sleep 2; done
rc cat "$dir/dump.sql" | docker exec -i -e MYSQL_PWD=unused dbcheck mariadb -uroot
echo "  dump restored into a fresh mariadb:10.11.9: $(count dbcheck unused)"
docker rm -f dbcheck >/dev/null

echo "== immich database to SFTP"
f=/apps/immich/docker-compose.yml
grep -q 'palier.backup:' "$f" || sed -i '/^    container_name: immich_postgres$/{n;s|$|\n    labels:\n      palier.backup: dump\n      palier.backup.to: sftp:backups|}' "$f"
sed -i 's|image: registry.hub.docker.com/library/redis:6.2.24-alpine|image: registry.hub.docker.com/library/redis:6.2.23-alpine|' "$f"
grep -B2 -A3 'palier.backup' "$f"
(cd /apps/immich && docker compose up -d --wait >/dev/null 2>&1)
idb() { docker exec "$1" psql -U postgres -d immich -At -c "SELECT 'assets ' || (SELECT count(*) FROM asset) || ', users ' || (SELECT count(*) FROM \"user\") || ', tables ' || (SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public')" 2>&1; }
echo "before: redis $(docker inspect immich_redis --format '{{.Config.Image}}'), $(idb immich_postgres)"
rm -rf /opt/palier/sftp/immich
docker exec palier-palier-1 palier plan immich --once >/dev/null 2>&1
apply_and_report immich
immich_state
echo "after: redis $(docker inspect immich_redis --format '{{.Config.Image}}'), $(idb immich_postgres)"
dir=$(ls -d /opt/palier/sftp/immich/database/*/ | tail -1)
echo "on the SFTP server: $(find /opt/palier/sftp/immich -type f -printf '%P %s bytes, ')"
(cd "$dir" && sha256sum -c dump.sql.sha256)
image=$(docker inspect immich_postgres --format '{{.Config.Image}}')
docker rm -f idbcheck >/dev/null 2>&1 || true
docker run -d --name idbcheck -e POSTGRES_PASSWORD=unused "$image" >/dev/null
until docker exec idbcheck pg_isready -U postgres -h 127.0.0.1 >/dev/null 2>&1; do sleep 1; done
docker exec -i idbcheck psql -q -U postgres -d postgres <"$dir/dump.sql" 2>&1 | grep -v 'already exists' | head -5
echo "  dump restored into a fresh $image: $(idb idbcheck)"
docker rm -f idbcheck >/dev/null

echo "== webhooks since the start"
curl -s http://127.0.0.1:8081/api/session/00000000-0000-4000-8000-000000000001/requests | python3 -c '
import base64, json, sys
start = int(sys.argv[1]) * 1000
for r in sorted((r for r in json.load(sys.stdin) if r["captured_at_unix_milli"] >= start), key=lambda r: r["captured_at_unix_milli"]):
    e = json.loads(base64.b64decode(r["request_payload_base64"]))
    if e["event"].startswith("backup_") or e["event"].startswith("run_"):
        print(" ", e["event"], e["stack"], e.get("service") or "", e.get("kind") or "", ",".join(e.get("databases") or []), e.get("location") or "", e.get("size") or "", e.get("sha256") or "", e.get("status") or "", (e.get("error") or "")[:200])
' "$started"
