# Images of a repository that publishes no version, only latest and commit tags: compared by digest and build date.
# Part 1 runs excalidraw from Docker Hub on its last commit tag. Part 2 uses the lab registry to put a commit tag
# ahead of latest, move latest, then publish a latest that prints 80 lines and exits.
# KEEP=1 leaves the stacks in place, to look at their pages.
reg=10.10.50.10:5443
repo=$reg/lab/moving
show() { $PALIER exec -T palier palier show "$1" --json 2>/dev/null | python3 -c '
import json, sys
v = json.load(sys.stdin)
for s in v["stack"]["services"]:
    print("  %-6s tag %-12s version %-12s read from %-6s available %s maturing %s error: %s" % (s["name"], s["tag"], s.get("version"), s.get("version_source"),
        [r["tag"] for r in s.get("available") or []], [r["tag"] + " built " + r["pushed"][:10] for r in s.get("maturing") or []], s.get("detection_error") or "none"))
r = v.get("record") or {}
p = r.get("plan") or {}
print("  plan from %s:" % r.get("source"), [[c["service"] + " " + c["from"] + " → " + c["to"] for c in st["changes"]] for st in p.get("steps") or []] or "no step", "| incomplete:", p.get("incomplete") or "none")'; }
decide() { since=$(date -u +%FT%T); sleep 1; $PALIER exec -T palier palier "$@" | sed 's/^/  /'; settled "$2" "$since"; }
running() { docker inspect "$1" --format '{{.Config.Image}} {{.State.Status}}, image {{.Image}}' | cut -c1-120; }
served() { docker buildx imagetools inspect "$1" --format '{{.Manifest.Digest}}' 2>/dev/null | cut -c1-19; }
push() { docker push "$1" | sed -n 's/.*digest: \(sha256:[0-9a-f]\{12\}\).*/\1/p'; }

for s in draw moving; do [ ! -d /apps/$s ] || (cd /apps/$s && docker compose down -v --rmi all >/dev/null 2>&1); done
echo "== Docker Hub: excalidraw on its last commit tag"
mkdir -p /apps/draw
cat >/apps/draw/compose.yaml <<'Y'
services:
  draw:
    image: docker.io/excalidraw/excalidraw:sha-4bfc5bb
    labels:
      palier.auto: "off"
Y
(cd /apps/draw && docker compose up -d --wait >/dev/null 2>&1)
echo "  before: $(running draw-draw-1)"
echo "  the registry serves sha-4bfc5bb as $(served excalidraw/excalidraw:sha-4bfc5bb), latest as $(served excalidraw/excalidraw:latest)"
rescan draw
show draw
apply_and_report draw
echo "  after: $(running draw-draw-1)"
echo "  compose: $(grep image: /apps/draw/compose.yaml | tr -s ' ')"
rescan draw
show draw

echo "== lab registry: a repository with latest and commit tags only"
echo lab-registry-password | docker login $reg -u palier --password-stdin >/dev/null 2>&1
# publish <command> <tag…>: build an image that runs the command and push it under each tag; the machine keeps under latest what it had pulled, and nothing else of the build.
# The RUN line differs at each build: an image made of instructions without a layer takes the build date of its base.
publish() {
	held=$(docker images -q "$repo:latest" 2>/dev/null | head -1)
	printf 'FROM busybox:1.37.0\nRUN echo %s >/built\nCMD ["sh", "-c", "%s"]\n' "$(date +%s.%N)" "$1" | docker build -q -t lab-build:tmp - >/dev/null
	shift
	for t in "$@"; do docker tag lab-build:tmp "$repo:$t"; served_latest=$(push "$repo:$t"); done
	for t in "$@"; do
		if [ "$t" = latest ] && [ -n "$held" ]; then docker tag "$held" "$repo:latest"; else docker rmi "$repo:$t" >/dev/null; fi
	done
	docker rmi lab-build:tmp >/dev/null 2>&1 || true
}
publish 'echo build one; exec sleep infinity' sha-aaaaaaa latest
sleep 2
publish 'echo build two; exec sleep infinity' sha-bbbbbbb
mkdir -p /apps/moving
cat >/apps/moving/compose.yaml <<Y
services:
  app:
    image: $repo:latest
    labels:
      palier.auto: "off"
  pin:
    image: $repo:sha-aaaaaaa
    labels:
      palier.auto: "off"
  ahead:
    image: $repo:sha-bbbbbbb
    labels:
      palier.auto: "off"
Y
(cd /apps/moving && docker compose up -d >/dev/null 2>&1)
state() { for s in app pin ahead; do echo "  $s: $(running moving-$s-1), says $(docker logs moving-$s-1 2>&1 | head -1)"; done; }
echo "-- latest and sha-aaaaaaa are build one, sha-bbbbbbb is build two, built later than latest"
state
rescan moving
show moving

echo "-- the registry moves latest to build two; the machine still has build one under latest"
held=$(docker images -q "$repo:latest" | head -1)
docker tag "$repo:sha-bbbbbbb" "$repo:latest" && served_latest=$(push "$repo:latest") && docker tag "$held" "$repo:latest"
echo "  latest in the registry: $served_latest, on the machine: sha256:$(docker images -q "$repo:latest" | head -1 | cut -c1-12)"
rescan moving
show moving
decide accept moving app latest
decide accept moving pin latest
show moving
apply_and_report moving
state
echo "  compose: $(grep image: /apps/moving/compose.yaml | tr -s ' ' | tr '\n' ' ')"
rescan moving
show moving

echo "-- the registry publishes a latest that prints 80 lines and exits 1"
publish 'i=1; while [ $i -le 80 ]; do echo line $i of 80; i=$((i+1)); done; exit 1' latest
rescan moving
show moving
apply_and_report moving
sql "SELECT error FROM runs ORDER BY id DESC LIMIT 1" | python3 -c '
import re, sys
lines = re.findall(r"line (\d+) of 80", sys.stdin.read())
print("  log lines kept in the error of the run: %d, from line %s to line %s" % (len(lines), lines[0] if lines else "-", lines[-1] if lines else "-"))'
state
echo "  latest in the registry: $served_latest, on the machine: sha256:$(docker images -q "$repo:latest" | head -1 | cut -c1-12); copies of the image that exits left on the machine: $(docker images -q --no-trunc | grep -c "^$served_latest" || true)"

[ -z "${KEEP:-}" ] || { docker logout $reg >/dev/null; exit 0; }
for s in app pin; do $PALIER exec -T palier palier forget moving $s latest >/dev/null; done
(cd /apps/moving && docker compose down -v --rmi all >/dev/null 2>&1)
(cd /apps/draw && docker compose down -v --rmi all >/dev/null 2>&1)
rm -rf /apps/moving /apps/draw
docker logout $reg >/dev/null
