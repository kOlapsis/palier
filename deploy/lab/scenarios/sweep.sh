# What a Palier stopped in the middle of a run can leave behind is removed when it starts: a throwaway container and the
# volume copy of a finished run. The copy of a failed run stays.
done_run=$(sql "SELECT id FROM runs WHERE status = 'done' ORDER BY id DESC LIMIT 1")
[ -n "$done_run" ] || { echo "no finished run to borrow an id from"; exit 1; }
failed_run=$(sql "INSERT INTO runs (stack, status, dry_run, author, plan, started) VALUES ('sweep', 'failed', 0, 'scenario', '{}', '2026-01-01T00:00:00Z') RETURNING id")
docker create --name sweep-helper --label palier.helper=copy busybox:1.37.0 true >/dev/null
docker volume create --label palier.run="$done_run" "palier-run$done_run-step0-sweep-0" >/dev/null
docker volume create --label palier.run="$failed_run" "palier-run$failed_run-step0-sweep-0" >/dev/null
echo "before: helper $(docker ps -aq --filter name=sweep-helper | wc -l), copies $(docker volume ls -q | grep -c 'step0-sweep-0')"
$PALIER restart palier >/dev/null 2>&1
until [ -n "$(docker logs --since 1m palier-palier-1 2>&1 | grep 'palier started')" ]; do sleep 2; done
docker logs --since 2m palier-palier-1 2>&1 | grep 'left by a stopped Palier' | cut -c1-220
echo "after: helper $(docker ps -aq --filter name=sweep-helper | wc -l), copies left: $(docker volume ls -q | grep 'step0-sweep-0' | tr '\n' ' ')"
docker volume rm "palier-run$failed_run-step0-sweep-0" >/dev/null
sql "DELETE FROM runs WHERE id = $failed_run"
