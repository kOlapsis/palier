# The target image of whoami exits as soon as it starts: a local image under the target tag replaces the real one.
target=$(sql "SELECT json_extract(body, '$.steps[0].changes[0].to') FROM plans WHERE stack = 'whoami'")
[ -n "$target" ] || { echo "no plan for whoami: run palier plan whoami first"; exit 1; }
printf 'FROM busybox:1.37.0\nCMD ["sh", "-c", "echo whoami broken; exit 1"]\n' | docker build -q -t "docker.io/traefik/whoami:$target" - >/dev/null
echo "local image docker.io/traefik/whoami:$target: exits at once"
echo "before: $(whoami_image)"
start=$(date +%s)
$PALIER exec -T palier /palier apply whoami </dev/null || true
echo "duration: $(( $(date +%s) - start )) s"
echo "after: $(whoami_image)"
grep image: /apps/whoami/compose.yaml
docker rmi "docker.io/traefik/whoami:$target" >/dev/null
