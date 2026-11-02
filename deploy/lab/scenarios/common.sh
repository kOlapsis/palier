# Sourced by the scenarios: how to reach Palier and its database on the machine.
set -eu
PALIER="docker compose --env-file /opt/palier/compose.env -f /opt/palier/src/deploy/compose.yaml -f /opt/palier/src/deploy/lab/compose.lab.yaml"
DB=/var/lib/docker/volumes/palier_palier-data/_data/palier.db
# Run as root only while Palier holds the WAL files, so that no file of the base ends up owned by root.
sql() { [ -e "$DB-wal" ] || { echo "Palier must be running"; exit 1; }; sqlite3 -cmd ".timeout 5000" "$DB" "$@"; }
whoami_image() { docker inspect whoami-whoami-1 --format '{{.Config.Image}} {{.State.Status}} restarts={{.RestartCount}}'; }
last_run() { sql "SELECT id, status, error FROM runs ORDER BY id DESC LIMIT 1"; }
outline_state() {
	for s in outline postgres redis; do echo "  $s: $(docker inspect outline-$s-1 --format '{{.Config.Image}} {{.State.Status}} {{if .State.Health}}{{.State.Health.Status}}{{end}}')"; done
	echo "  documents: $(docker exec outline-postgres-1 psql -U outline -At -c "SELECT count(*) || ' : ' || string_agg(title, ', ' ORDER BY title) FROM documents")"
}
pg_counts() {
	docker exec "$1" psql -U outline -d outline -At -c "SELECT 'tables ' || count(*) FROM information_schema.tables WHERE table_schema = 'public'" -c "SELECT 'documents ' || count(*) FROM documents" -c "SELECT 'roles ' || string_agg(rolname, ',' ORDER BY rolname) FROM pg_roles WHERE rolname !~ '^pg_'" -c "SELECT 'version ' || current_setting('server_version')" 2>/dev/null | tr '\n' ' '
}
old_volume_counts() {
	docker rm -f pgold >/dev/null 2>&1 || true
	docker run -d --name pgold -v outline_postgres-data:/var/lib/postgresql/data -e POSTGRES_PASSWORD=unused "$1" >/dev/null
	until docker exec pgold pg_isready -U outline >/dev/null 2>&1; do sleep 1; done
	sleep 2
	echo "old volume outline_postgres-data, read again by $1: $(pg_counts pgold)"
	docker rm -f pgold >/dev/null
}
mounts_of() { docker inspect "$1" --format '{{range .Mounts}}{{.Name}}:{{.Destination}} {{end}}'; }
gitlab_state() {
	echo "  gitlab: $(docker inspect gitlab-gitlab-1 --format '{{.Config.Image}} {{.State.Status}} {{if .State.Health}}{{.State.Health.Status}}{{end}}')"
	echo "  seed: $(docker exec gitlab-gitlab-1 gitlab-psql -At -c "SELECT 'project ' || string_agg(p.path, ',') || ', issues ' || (SELECT count(*) FROM issues) || ', account ' || (SELECT username FROM users WHERE username = 'root') FROM projects p" 2>/dev/null)"
	echo "  version in database: $(docker exec gitlab-gitlab-1 cat /opt/gitlab/version-manifest.txt 2>/dev/null | head -1)"
}
apply_and_report() {
	if [ -n "${PALIER_NEXT:-}" ]; then
		bash /tmp/next-bg.sh palier-next apply "$1" >/dev/null
		docker wait palier-next >/dev/null
		docker logs palier-next 2>/dev/null | python3 -c 'import json,sys; t=sys.stdin.read(); v=json.loads(t[t.index("{\n"):]); print("run", v["id"], v["status"], (v.get("error") or "")[:400]); [print("  ", e["at"][11:19], e["message"].splitlines()[0][:200]) for e in v["events"]]' || docker logs --tail 5 palier-next
		return
	fi
	$PALIER exec -T palier /palier apply "$1" </dev/null | python3 -c 'import json,sys; v=json.load(sys.stdin); print("run", v["id"], v["status"], (v.get("error") or "")[:400]); [print("  ", e["at"][11:19], e["message"].splitlines()[0][:200]) for e in v["events"]]' || true
}
fake_step_image() {
	step=$(sql "SELECT json_extract(body, '$.steps[$2].changes[0].service') || ' ' || json_extract(body, '$.steps[$2].changes[0].to') FROM plans WHERE stack = '$1'")
	set -- $1 $step
	[ -n "${3:-}" ] || { echo "no such step in the plan of $1"; exit 1; }
	repo=$(docker inspect "$(docker ps -aq --filter label=com.docker.compose.project=$1 --filter label=com.docker.compose.service=$2 | head -1)" --format '{{.Config.Image}}'); repo=${repo%:*}
	printf 'FROM busybox:1.37.0\nCMD ["sh", "-c", "echo %s broken; exit 1"]\n' "$2" | docker build -q -t "$repo:$3" - >/dev/null
	FAKE="$repo:$3"
	echo "local image $FAKE that exits at once"
}
immich_state() {
	for c in immich_server immich_microservices immich_machine_learning immich_postgres immich_redis; do
		echo "  $c: $(docker inspect "$c" --format '{{.Config.Image}} {{.State.Status}} {{if .State.Health}}{{.State.Health.Status}}{{end}}' 2>/dev/null || echo missing)"
	done
	echo "  version in database: $(docker exec immich_postgres psql -U postgres -d immich -At -c "SELECT count(*) || ' assets' FROM assets" 2>/dev/null)"
	python3 /tmp/immich-check.py
}
