# The image of the second step of the Immich plan exits at once: Immich must come back to v1.105.1 with its Compose file, its env file and its photos.
fake_step_image immich 1
echo "before:"; immich_state
cp /apps/immich/docker-compose.yml /tmp/immich-compose-before.yml; cp /apps/immich/.env /tmp/immich-env-before
start=$(date +%s)
apply_and_report immich
echo "duration: $(( $(date +%s) - start )) s"
echo "after:"; immich_state
diff /tmp/immich-compose-before.yml /apps/immich/docker-compose.yml >/dev/null && echo "docker-compose.yml identical to the original" || echo "docker-compose.yml differs from the original"
diff /tmp/immich-env-before /apps/immich/.env >/dev/null && echo ".env identical to the original" || echo ".env differs from the original"
docker rmi "$FAKE" >/dev/null
