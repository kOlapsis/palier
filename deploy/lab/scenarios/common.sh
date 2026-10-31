# Sourced by the scenarios: how to reach Palier and its database on the machine.
set -eu
PALIER="docker compose --env-file /opt/palier/compose.env -f /opt/palier/src/deploy/compose.yaml -f /opt/palier/src/deploy/lab/compose.lab.yaml"
DB=/var/lib/docker/volumes/palier_palier-data/_data/palier.db
# Run as root only while Palier holds the WAL files, so that no file of the base ends up owned by root.
sql() { [ -e "$DB-wal" ] || { echo "Palier doit tourner"; exit 1; }; sqlite3 -cmd ".timeout 5000" "$DB" "$@"; }
whoami_image() { docker inspect whoami-whoami-1 --format '{{.Config.Image}} {{.State.Status}} restarts={{.RestartCount}}'; }
last_run() { sql "SELECT id, status, error FROM runs ORDER BY id DESC LIMIT 1"; }
outline_state() {
	for s in outline postgres redis; do echo "  $s : $(docker inspect outline-$s-1 --format '{{.Config.Image}} {{.State.Status}} {{if .State.Health}}{{.State.Health.Status}}{{end}}')"; done
	echo "  documents : $(docker exec outline-postgres-1 psql -U outline -At -c "SELECT count(*) || ' : ' || string_agg(title, ', ' ORDER BY title) FROM documents")"
}
pg_counts() {
	docker exec "$1" psql -U outline -d outline -At -c "SELECT 'tables ' || count(*) FROM information_schema.tables WHERE table_schema = 'public'" -c "SELECT 'documents ' || count(*) FROM documents" -c "SELECT 'rôles ' || string_agg(rolname, ',' ORDER BY rolname) FROM pg_roles WHERE rolname !~ '^pg_'" -c "SELECT 'version ' || current_setting('server_version')" 2>/dev/null | tr '\n' ' '
}
old_volume_counts() {
	docker rm -f pgold >/dev/null 2>&1 || true
	docker run -d --name pgold -v outline_postgres-data:/var/lib/postgresql/data -e POSTGRES_PASSWORD=unused "$1" >/dev/null
	until docker exec pgold pg_isready -U outline >/dev/null 2>&1; do sleep 1; done
	sleep 2
	echo "ancien volume outline_postgres-data, relu par $1 : $(pg_counts pgold)"
	docker rm -f pgold >/dev/null
}
mounts_of() { docker inspect "$1" --format '{{range .Mounts}}{{.Name}}:{{.Destination}} {{end}}'; }
