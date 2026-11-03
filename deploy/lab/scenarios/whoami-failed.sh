# The rollback itself fails: compose.yaml is made immutable while Palier verifies a broken target, so it cannot be restored and the run ends failed.
target=$(sql "SELECT json_extract(body, '$.steps[0].changes[0].to') FROM plans WHERE stack = 'whoami'")
[ -n "$target" ] || { echo "no plan for whoami"; exit 1; }
printf 'FROM busybox:1.37.0\nCMD ["sh", "-c", "echo whoami broken; exit 1"]\n' | docker build -q -t "docker.io/traefik/whoami:$target" - >/dev/null
previous=$(sql "SELECT coalesce(max(id), 0) FROM runs")
$PALIER exec -T palier palier apply whoami --yes --detach
until sql "SELECT message FROM run_events WHERE run_id > $previous" | grep -q '^verifying'; do sleep 1; done
chattr +i /apps/whoami/compose.yaml && echo "compose.yaml made immutable during verification"
until [ "$(sql "SELECT status FROM runs WHERE id > $previous ORDER BY id DESC LIMIT 1")" != running ]; do sleep 2; done
sql "SELECT id, status, substr(error, 1, 300) FROM runs WHERE id > $previous"
echo "whoami: $(whoami_image)"
chattr -i /apps/whoami/compose.yaml
docker rmi "docker.io/traefik/whoami:$target" >/dev/null 2>&1 || true
