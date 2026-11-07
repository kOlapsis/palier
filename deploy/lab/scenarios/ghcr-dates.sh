# Two GHCR images behind their latest version in a throwaway stack, one of an organization and one of a user: their tag dates come from the GitHub Packages API, with PALIER_GITHUB_TOKEN.
mkdir -p /apps/ghcrdates
printf 'services:\n  homepage:\n    image: ghcr.io/gethomepage/homepage:v2.5.0\n  tester:\n    image: ghcr.io/tarampampam/webhook-tester:2.2.6\n' > /apps/ghcrdates/compose.yaml
(cd /apps/ghcrdates && docker compose up -d >/dev/null 2>&1)
$PALIER exec -T palier palier postpone ghcrdates "$(date -d tomorrow +%F)" >/dev/null
sql "DELETE FROM tag_dates WHERE image IN ('ghcr.io/gethomepage/homepage', 'ghcr.io/tarampampam/webhook-tester')"
start=$(date -u +%FT%TZ)
$PALIER restart palier >/dev/null 2>&1
until $PALIER exec -T palier palier status >/dev/null 2>&1; do sleep 2; done
$PALIER exec -T palier palier scan >/dev/null
docker logs --since "$start" palier-palier-1 2>&1 | grep -E '"tag dates' | cut -c1-260
sql "SELECT image, count(*) FROM tag_dates WHERE image IN ('ghcr.io/gethomepage/homepage', 'ghcr.io/tarampampam/webhook-tester') GROUP BY image"
sql "SELECT image, tag, pushed FROM tag_dates WHERE (image, tag) IN (VALUES ('ghcr.io/gethomepage/homepage', 'v2.7.0'), ('ghcr.io/tarampampam/webhook-tester', '2.3.0'))"
$PALIER exec -T palier palier show ghcrdates | sed -n '4,7p'
(cd /apps/ghcrdates && docker compose down >/dev/null 2>&1); rm -rf /apps/ghcrdates
