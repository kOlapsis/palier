# Puts whoami back to v1.10.1 by hand, outside Palier.
sed -i 's|image: docker.io/traefik/whoami:.*|image: docker.io/traefik/whoami:v1.10.1|' /apps/whoami/compose.yaml
(cd /apps/whoami && docker compose up -d --wait)
echo "whoami: $(whoami_image)"
