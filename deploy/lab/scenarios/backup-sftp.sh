# A second Palier beside the bench one backs pgcoll up before its update: first to an SFTP address where nothing listens, which must abandon the run
# and leave pgcoll on 16.4, then to the SFTP server of the bench, where the dump and the volume archive are checked against their .sha256 and restored.
label() { grep -q 'palier.backup:' "$1" || sed -i "/^  $2:\$/,/^    restart:/ s|^    restart: always\$|    restart: always\n    labels:\n      palier.backup: $3|" "$1"; }
(cd /apps/pgcoll && docker compose down -v >/dev/null 2>&1 && sed -i 's|image: docker.io/library/postgres:.*|image: docker.io/library/postgres:16.4|' compose.yaml)
label /apps/pgcoll/compose.yaml postgres dump,volumes
(cd /apps/pgcoll && docker compose up -d >/dev/null 2>&1)
until docker exec pgcoll-postgres-1 psql -U lab -d lab -h 127.0.0.1 -c 'SELECT count(*) FROM words' >/dev/null 2>&1; do sleep 2; done
state() { echo "$(docker inspect pgcoll-postgres-1 --format '{{.Config.Image}} {{.State.Status}}'), words $(docker exec pgcoll-postgres-1 psql -U lab -d lab -At -c 'SELECT count(*) FROM words')"; }
second() {
	docker rm -f palier-backup >/dev/null 2>&1 || true
	docker volume rm -f palier_backup-data >/dev/null
	$PALIER run -d --name palier-backup --no-deps -v palier_backup-data:/var/lib/palier \
		-e PALIER_BACKUP_TO="$1" -e RCLONE_CONFIG_DEAD_TYPE=sftp -e RCLONE_CONFIG_DEAD_HOST=10.10.50.10 -e RCLONE_CONFIG_DEAD_PORT=1 -e RCLONE_CONFIG_DEAD_USER=palier -e RCLONE_CONFIG_DEAD_PASS=Lrx0JAiJow7v_jI3ygeAymjwzPTj5bJfU6E9I2Xkqb50 \
		-e PALIER_LISTEN= -e PALIER_AUTO=off -e PALIER_SMTP_HOST= -e PALIER_NTFY_URL= -e PALIER_SLACK_URL= -e PALIER_DISCORD_URL= -e PALIER_TELEGRAM_URL= -e PALIER_OIDC_ISSUER= \
		palier serve >/dev/null
	until docker exec palier-backup palier runs >/dev/null 2>&1; do sleep 2; done
	docker exec palier-backup palier plan pgcoll --once >/dev/null 2>&1
	docker exec palier-backup palier apply pgcoll --yes --json </dev/null | python3 -c 'import json,sys; v=json.load(sys.stdin); print("run", v["id"], v["status"], (v.get("error") or "")[:400]); [print("  ", e["at"][11:19], e["message"].splitlines()[0][:220]) for e in v["events"]]' || true
}

echo "== destination where nothing listens: dead:palier (sftp on port 1)"
echo "before: $(state)"
second dead:palier
echo "after: $(state)"

echo "== SFTP server of the bench: sftp:backups"
rm -rf /opt/palier/sftp/pgcoll
second sftp:backups
echo "after: $(state)"
dir=$(ls -d /opt/palier/sftp/pgcoll/postgres/*/ | tail -1)
echo "on the SFTP server: $(find /opt/palier/sftp/pgcoll -type f -printf '%P %s bytes, ' )"
(cd "$dir" && sha256sum -c dump.sql.sha256 volumes.tar.sha256)
docker rm -f pgcheck >/dev/null 2>&1 || true
docker run -d --name pgcheck -e POSTGRES_PASSWORD=unused docker.io/library/postgres:16.4 >/dev/null
until docker exec pgcheck pg_isready -U postgres -h 127.0.0.1 >/dev/null 2>&1; do sleep 1; done
docker exec -i pgcheck psql -q -U postgres -d postgres <"$dir/dump.sql" >/dev/null 2>&1
echo "dump restored into a fresh postgres:16.4: words $(docker exec pgcheck psql -U lab -d lab -At -c 'SELECT count(*) FROM words')"
docker rm -f pgcheck >/dev/null
docker rm -f palier-backup >/dev/null; docker volume rm -f palier_backup-data >/dev/null
