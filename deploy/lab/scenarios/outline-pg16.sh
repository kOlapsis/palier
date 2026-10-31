# Puts postgres back on 16.14 and its old volume, and redis on 7.4.11, by hand, to replay the major upgrade.
f=/apps/outline/compose.yaml
sed -i -e 's|image: docker.io/library/postgres:.*|image: docker.io/library/postgres:16.14|' \
	-e 's|- postgres-data-18:/var/lib/postgresql$|- postgres-data:/var/lib/postgresql/data|' \
	-e '/^  postgres-data-18: {}$/d' \
	-e 's|image: docker.io/library/redis:.*|image: docker.io/library/redis:7.4.11|' "$f"
(cd /apps/outline && docker compose up -d --wait --renew-anon-volumes postgres redis >/dev/null 2>&1 && docker compose up -d --wait >/dev/null 2>&1)
docker volume rm outline_postgres-data-18
docker volume ls -q | grep postgres-data | tr '\n' ' '; echo
outline_state
echo "postgres : $(pg_counts outline-postgres-1) ; montages : $(mounts_of outline-postgres-1)"
