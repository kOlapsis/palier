# A version still maturing is proposed at once when someone accepts it: the newest build of an image, held back by the delay.
mkdir -p /apps/accept
cat >/apps/accept/compose.yaml <<Y
services:
  web:
    image: ghcr.io/jitsi/web:stable-11248-2
    labels:
      palier.auto: "off"
Y
(cd /apps/accept && docker compose up -d >/dev/null 2>&1)
versions() { $PALIER exec -T palier palier show accept --json 2>/dev/null | python3 -c 'import json,sys; v=json.load(sys.stdin); s=v["stack"]["services"][0]; print("  available", [r["tag"] for r in s.get("available") or []], "| maturing", [r["tag"] for r in s.get("maturing") or []], "| plan", [c["from"]+" → "+c["to"] for st in ((v.get("record") or {}).get("plan") or {}).get("steps") or [] for c in st["changes"]])'; }
$PALIER exec -T palier palier scan >/dev/null
sleep 5
echo "before:"; versions
held=$($PALIER exec -T palier palier show accept --json | python3 -c 'import json,sys; print((json.load(sys.stdin)["stack"]["services"][0].get("maturing") or [{"tag": ""}])[-1]["tag"])')
[ -n "$held" ] || { echo "no version is maturing: nothing to accept"; (cd /apps/accept && docker compose down -v >/dev/null 2>&1); rm -rf /apps/accept; exit 1; }
$PALIER exec -T palier palier accept accept web "$held"
sleep 8
echo "after palier accept accept web $held:"; versions
docker logs --since 20s palier-palier-1 2>&1 | grep -o '"msg":"scan finished","stacks":[0-9]*' | tail -1

(cd /apps/accept && docker compose down -v >/dev/null 2>&1)
rm -rf /apps/accept
