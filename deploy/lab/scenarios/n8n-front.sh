# n8n on docker.n8n.io, a front of Docker Hub, on its moving tag stable: the running version is read from Docker Hub.
mkdir -p /apps/n8nfront
cat >/apps/n8nfront/compose.yaml <<Y
services:
  n8n:
    image: docker.n8n.io/n8nio/n8n:stable
    labels:
      palier.auto: "off"
Y
(cd /apps/n8nfront && docker compose up -d) 2>&1 | tail -2
echo "running: $(docker inspect n8nfront-n8n-1 --format '{{.Config.Image}} {{.State.Status}}') | repo digests: $(docker image inspect docker.n8n.io/n8nio/n8n:stable --format '{{join .RepoDigests " "}}')"
$PALIER exec -T palier palier scan >/dev/null
$PALIER exec -T palier palier show n8nfront --json 2>/dev/null | python3 -c 'import json,sys; s=json.load(sys.stdin)["stack"]["services"][0]; print("n8n: version", s.get("version"), "read from the", s.get("version_source"), "| available", [r["tag"] for r in s.get("available") or []][:5], "| error", s.get("detection_error"))'

(cd /apps/n8nfront && docker compose down -v --rmi all >/dev/null 2>&1)
rm -rf /apps/n8nfront
