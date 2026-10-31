# Applies the whole Immich plan, with its Compose edits, and checks that the seeded photos are still served.
echo "avant :"; immich_state
start=$(date +%s)
apply_and_report immich
echo "durée : $(( $(date +%s) - start )) s"
echo "après :"; immich_state
cat /apps/immich/.env; cat /apps/immich/docker-compose.yml
