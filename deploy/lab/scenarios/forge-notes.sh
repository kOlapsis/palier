
# GitLab release notes with PALIER_GITLAB_TOKEN, and Docker Hub reads by digest through PALIER_REGISTRY_MIRROR.
mkdir -p /apps/crafty
cat > /apps/crafty/compose.yaml <<'EOF'
services:
  crafty:
    image: arcadiatechnology/crafty-4:4.10.0
EOF
docker compose -f /apps/crafty/compose.yaml pull -q
docker compose -f /apps/crafty/compose.yaml create >/dev/null
echo "source de l'image : $(docker image inspect arcadiatechnology/crafty-4:4.10.0 --format '{{index .Config.Labels "org.opencontainers.image.source"}}')"
echo "PALIER_GITLAB_TOKEN dans Palier : $($PALIER config | grep -q 'PALIER_GITLAB_TOKEN: ..*' && echo défini || echo absent)"
echo "PALIER_REGISTRY_MIRROR dans Palier : $($PALIER config | sed -n 's/ *PALIER_REGISTRY_MIRROR: //p')"

crafty() {
	python3 -c '
import json, sys
for st in json.load(sys.stdin):
    for s in st["services"]:
        if s["name"] == "crafty":
            print("  version en service", s.get("version"), "| disponibles", [r["tag"] for r in s.get("available") or []])
            print("  notes", len(s.get("notes") or []), [n["tag"] for n in s.get("notes") or []][:6])
            print("  première source", (s.get("notes") or [{}])[0].get("url"))
            print("  erreur de notes", s.get("notes_error") or "aucune")
'
}
echo "== inspection avec le jeton du banc"
sql "DELETE FROM releases WHERE repo LIKE 'gitlab.com/%'"
$PALIER exec -T palier /palier scan 2>/dev/null | crafty
echo "== inspection avec un jeton invalide (les notes viennent alors du cache)"
$PALIER exec -T -e PALIER_GITLAB_TOKEN=invalide palier /palier scan 2>/dev/null | crafty

echo "== un tag Docker Hub republié : traefik/whoami:v1.9.0 porte localement le contenu de v1.8.7"
docker pull -q traefik/whoami:v1.8.7 >/dev/null
docker tag traefik/whoami:v1.8.7 traefik/whoami:v1.9.0
mkdir -p /apps/hubrepub
printf 'services:\n  whoami:\n    image: traefik/whoami:v1.9.0\n    pull_policy: never\n' > /apps/hubrepub/compose.yaml
docker compose -f /apps/hubrepub/compose.yaml create >/dev/null 2>&1

echo "== miroir : un serveur qui note les requêtes et répond 404, pour voir ce que Palier lui demande"
pkill -f 'http.server 5999' 2>/dev/null || true
mkdir -p /tmp/mirror && cd /tmp/mirror && (python3 -m http.server 5999 >/tmp/mirror.log 2>&1 &) && sleep 1
$PALIER exec -T -e PALIER_REGISTRY_MIRROR=http://10.10.50.10:5999 palier /palier scan 2>/dev/null >/tmp/scan-logger.json
$PALIER exec -T palier /palier scan 2>/dev/null >/tmp/scan-gcr.json
pkill -f 'http.server 5999' || true
echo "requêtes reçues par le faux miroir : $(grep -c '/v2/' /tmp/mirror.log)"
grep -o '"GET /v2/[^"]*" [0-9]*' /tmp/mirror.log | sed 's/sha256:\(.\{12\}\)[^ ]*/sha256:\1…/' | sort | uniq -c | head
republished() { python3 -c '
import json, sys
print(sorted(st["name"] + "/" + s["name"] for st in json.load(open(sys.argv[1])) for s in st["services"] if s.get("republished")))' "$1"; }
echo "tags republiés, miroir en 404 puis repli sur Docker Hub : $(republished /tmp/scan-logger.json)"
echo "tags republiés, miroir mirror.gcr.io : $(republished /tmp/scan-gcr.json)"
echo "erreurs de détection avec mirror.gcr.io : $(python3 -c '
import json
print([s["name"] + ": " + s["detection_error"] for st in json.load(open("/tmp/scan-gcr.json")) for s in st["services"] if s.get("detection_error")] or "aucune")')"

docker compose -f /apps/crafty/compose.yaml down --rmi all >/dev/null 2>&1
docker compose -f /apps/hubrepub/compose.yaml down >/dev/null 2>&1
docker rmi traefik/whoami:v1.9.0 traefik/whoami:v1.8.7 >/dev/null 2>&1 || true
rm -rf /apps/crafty /apps/hubrepub
