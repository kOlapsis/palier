# Puts Outline back to its starting versions (1.10.1, postgres 16.4, redis 7.2.4) with fresh data and its three seeded documents.
f=/apps/outline/compose.yaml
(cd /apps/outline && docker compose down -v --remove-orphans >/dev/null 2>&1)
docker volume ls -q | grep '^outline_' | xargs -r docker volume rm >/dev/null
sed -i -e 's|image: docker.io/outlinewiki/outline:.*|image: docker.io/outlinewiki/outline:1.10.1|' \
	-e 's|image: docker.io/library/postgres:.*|image: docker.io/library/postgres:16.4|' \
	-e 's|image: docker.io/library/redis:.*|image: docker.io/library/redis:7.2.4|' \
	-e 's|- postgres-data-[0-9]*:/var/lib/postgresql$|- postgres-data:/var/lib/postgresql/data|' -e '/^  postgres-data-[0-9]*: {}$/d' "$f"
(cd /apps/outline && docker compose up -d >/dev/null 2>&1)
until curl -sf -o /dev/null http://127.0.0.1:3000/_health; do sleep 3; done
docker exec -i outline-postgres-1 psql -q -U outline -d outline < /tmp/seed-outline.sql
outline_state
