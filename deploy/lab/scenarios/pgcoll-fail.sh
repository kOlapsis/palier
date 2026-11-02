# pgcoll back to a fresh postgres 16.4, then the 16.15 image exits at once: the stack must come back to 16.4 with its collation version and its rows.
coll() { docker exec pgcoll-postgres-1 psql -U lab -d lab -At -c "SELECT current_setting('server_version') || '; datcollversion ' || datcollversion || '; libc ' || pg_database_collation_actual_version(oid) || '; words ' || (SELECT count(*) FROM words) FROM pg_database WHERE datname = 'lab'" 2>&1 | tr '\n' ' '; }
(cd /apps/pgcoll && docker compose down -v >/dev/null 2>&1 && sed -i 's|image: docker.io/library/postgres:.*|image: docker.io/library/postgres:16.4|' compose.yaml && docker compose up -d >/dev/null 2>&1)
until docker exec pgcoll-postgres-1 psql -U lab -d lab -h 127.0.0.1 -c 'SELECT count(*) FROM words' >/dev/null 2>&1; do sleep 2; done
docker exec palier-palier-1 /palier plan pgcoll >/dev/null 2>&1
fake_step_image pgcoll 0
echo "before: $(coll)"
apply_and_report pgcoll
echo "after: $(coll)"
grep image: /apps/pgcoll/compose.yaml
docker rmi "$FAKE" >/dev/null
