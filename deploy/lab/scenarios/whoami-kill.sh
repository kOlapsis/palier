# Palier is killed while it verifies whoami, then started again.
echo "before: $(whoami_image)"
previous=$(sql "SELECT coalesce(max(id), 0) FROM runs")
$PALIER exec -T palier palier apply whoami --yes --detach
until sql "SELECT message FROM run_events WHERE run_id > $previous" | grep -q '^verifying'; do sleep 1; done
echo "during verification: $(whoami_image)"
docker kill palier-palier-1 >/dev/null
echo "palier killed; run in database: $(sql "SELECT id, status, modified FROM runs ORDER BY id DESC LIMIT 1")"
start=$(date +%s)
$PALIER up -d palier 2>/dev/null
sleep 2
until [ "$(sql "SELECT status FROM runs ORDER BY id DESC LIMIT 1")" != running ]; do sleep 2; done
echo "recovery time: $(( $(date +%s) - start )) s"
last_run
echo "after: $(whoami_image)"
grep image: /apps/whoami/compose.yaml
