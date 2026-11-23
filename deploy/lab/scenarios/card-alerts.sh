# The card of a stack shows its alert before its other problems: authentik 2025.8.3 has critical or high advisories,
# and the other service runs an image Docker Hub does not list. The containers are only created: a scan does not need them to run.
# KEEP=1 leaves the stack in place, to look at its card.
mkdir -p /apps/cards
docker tag busybox:1.37.0 kolapsis/palier-not-there:local
cat >/apps/cards/compose.yaml <<'Y'
services:
  server:
    image: ghcr.io/goauthentik/server:2025.8.3
    command: server
    labels:
      palier.auto: "off"
  private:
    image: kolapsis/palier-not-there:local
    command: ["sleep", "infinity"]
Y
(cd /apps/cards && docker compose pull -q server >/dev/null 2>&1; docker compose create >/dev/null 2>&1)
rescan cards
echo "open problems of cards, in the order Palier records them:"
$PALIER exec -T palier palier notify 2>&1 | grep " cards: " | cut -c1-160 | sed 's/^/  /'

[ -z "${KEEP:-}" ] || exit 0
(cd /apps/cards && docker compose down -v >/dev/null 2>&1)
rm -rf /apps/cards
docker rmi kolapsis/palier-not-there:local >/dev/null 2>&1 || true
