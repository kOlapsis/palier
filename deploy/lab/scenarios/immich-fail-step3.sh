# The image of the third step of the Immich plan exits at once: Immich must stay at the verified second step, with its photos.
fake_step_image immich 2
echo "before:"; immich_state
start=$(date +%s)
apply_and_report immich
echo "duration: $(( $(date +%s) - start )) s"
echo "after:"; immich_state
echo "verified steps kept: $(sql "SELECT json_extract(checkpoint, '$.step') FROM runs WHERE stack = 'immich' ORDER BY id DESC LIMIT 1")"
echo "copy volumes left: $(docker volume ls -q --filter label=palier.run | grep -c . || true)"
grep -n 'image:' /apps/immich/docker-compose.yml
grep -n 'IMMICH_VERSION' /apps/immich/.env
docker rmi "$FAKE" >/dev/null
