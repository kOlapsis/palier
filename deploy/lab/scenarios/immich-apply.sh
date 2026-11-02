# Applies the whole Immich plan, with its Compose edits, and checks that the seeded photos are still served.
echo "before:"; immich_state
start=$(date +%s)
apply_and_report immich
echo "duration: $(( $(date +%s) - start )) s"
echo "after:"; immich_state
cat /apps/immich/.env; cat /apps/immich/docker-compose.yml
