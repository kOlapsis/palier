# The image of the third step of the Immich plan exits at once: Immich must stay at the verified second step, with its photos.
fake_step_image immich 2
echo "avant :"; immich_state
start=$(date +%s)
apply_and_report immich
echo "durée : $(( $(date +%s) - start )) s"
echo "après :"; immich_state
echo "étapes vérifiées gardées : $(sql "SELECT json_extract(checkpoint, '$.step') FROM runs WHERE stack = 'immich' ORDER BY id DESC LIMIT 1")"
echo "volumes de copie restants : $(docker volume ls -q --filter label=palier.run | grep -c . || true)"
grep -n 'image:' /apps/immich/docker-compose.yml
grep -n 'IMMICH_VERSION' /apps/immich/.env
docker rmi "$FAKE" >/dev/null
