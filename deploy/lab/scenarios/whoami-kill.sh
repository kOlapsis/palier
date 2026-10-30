# Palier is killed while it verifies whoami, then started again.
echo "avant : $(whoami_image)"
previous=$(sql "SELECT coalesce(max(id), 0) FROM runs")
$PALIER exec -d palier /palier apply whoami
until sql "SELECT message FROM run_events WHERE run_id > $previous" | grep -q '^vérification'; do sleep 1; done
echo "pendant la vérification : $(whoami_image)"
docker kill palier-palier-1 >/dev/null
echo "palier tué ; exécution en base : $(sql "SELECT id, status, modified FROM runs ORDER BY id DESC LIMIT 1")"
start=$(date +%s)
$PALIER up -d palier 2>/dev/null
sleep 2
until [ "$(sql "SELECT status FROM runs ORDER BY id DESC LIMIT 1")" != running ]; do sleep 2; done
echo "durée de la reprise : $(( $(date +%s) - start )) s"
last_run
echo "après : $(whoami_image)"
grep image: /apps/whoami/compose.yaml
