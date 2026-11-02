# Patch upgrade of a PostgreSQL whose image changes its C library: Palier must reindex and refresh the collation version.
coll() { docker exec pgcoll-postgres-1 psql -U lab -d lab -At -c "SELECT current_setting('server_version') || '; datcollversion ' || datcollversion || '; libc ' || pg_database_collation_actual_version(oid) || '; words ' || (SELECT count(*) FROM words) FROM pg_database WHERE datname = 'lab'" 2>&1 | tr '\n' ' '; }
echo "before: $(coll)"
apply_and_report pgcoll
echo "after: $(coll)"
docker exec pgcoll-postgres-1 psql -U lab -d lab -At -c "SELECT count(*) FROM words WHERE word > 'f'" 2>&1 | tr '\n' ' '; echo
