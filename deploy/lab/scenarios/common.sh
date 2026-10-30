# Sourced by the scenarios: how to reach Palier and its database on the machine.
set -eu
PALIER="docker compose --env-file /opt/palier/compose.env -f /opt/palier/src/deploy/compose.yaml -f /opt/palier/src/deploy/lab/compose.lab.yaml"
DB=/var/lib/docker/volumes/palier_palier-data/_data/palier.db
# Run as root only while Palier holds the WAL files, so that no file of the base ends up owned by root.
sql() { [ -e "$DB-wal" ] || { echo "Palier doit tourner"; exit 1; }; sqlite3 "$DB" "$@"; }
whoami_image() { docker inspect whoami-whoami-1 --format '{{.Config.Image}} {{.State.Status}} restarts={{.RestartCount}}'; }
last_run() { sql "SELECT id, status, error FROM runs ORDER BY id DESC LIMIT 1"; }
