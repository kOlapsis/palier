# The target image of whoami exits as soon as it starts: a local image under the target tag replaces the real one.
target=$(sql "SELECT json_extract(body, '$.steps[0].changes[0].to') FROM plans WHERE stack = 'whoami'")
[ -n "$target" ] || { echo "pas de plan pour whoami : lancer d'abord palier plan whoami"; exit 1; }
printf 'FROM busybox:1.37.0\nCMD ["sh", "-c", "echo whoami cassé; exit 1"]\n' | docker build -q -t "docker.io/traefik/whoami:$target" - >/dev/null
echo "image locale docker.io/traefik/whoami:$target : quitte à l'instant"
echo "avant : $(whoami_image)"
start=$(date +%s)
$PALIER exec -T palier /palier apply whoami || true
echo "durée : $(( $(date +%s) - start )) s"
echo "après : $(whoami_image)"
grep image: /apps/whoami/compose.yaml
docker rmi "docker.io/traefik/whoami:$target" >/dev/null
