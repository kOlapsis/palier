# Tags made of a build number: an image on stable-11248-2 is read from its tag, and moves to the next builds.
mkdir -p /apps/buildtag
cat >/apps/buildtag/compose.yaml <<Y
services:
  web:
    image: ghcr.io/jitsi/web:stable-11248-2
    labels:
      palier.auto: "off"
Y
(cd /apps/buildtag && docker compose up -d >/dev/null 2>&1)
show() { $PALIER exec -T palier palier show buildtag --json 2>/dev/null; }
plan_ready() { show | python3 -c 'import json,sys; sys.exit(0 if (json.load(sys.stdin).get("record") or {}).get("plan", {}).get("steps") else 1)'; }
$PALIER exec -T palier palier scan >/dev/null
show | python3 -c 'import json,sys; s=json.load(sys.stdin)["stack"]["services"][0]; print("web: version", s.get("version"), "read from the", s.get("version_source"), "| available", [r["tag"] for r in s.get("available") or []], "| maturing", [r["tag"] for r in s.get("maturing") or []], "| error", s.get("detection_error"))'
until plan_ready; do sleep 5; done
show | python3 -c 'import json,sys; v=json.load(sys.stdin); [print("  step", i+1, [c["from"]+" → "+c["to"] for c in s["changes"]], "risk", s["risk"]) for i, s in enumerate(v["record"]["plan"]["steps"])]; print("  state:", v["state"], "| source:", v["record"]["source"]); [print("  incomplete:", r) for r in v["record"]["plan"].get("incomplete") or []]; [print("  withheld:", r) for r in v["record"]["plan"].get("withheld") or []]'
echo "before: $(docker inspect buildtag-web-1 --format '{{.Config.Image}} {{.State.Status}}')"
apply_and_report buildtag
echo "after: $(docker inspect buildtag-web-1 --format '{{.Config.Image}} {{.State.Status}}')"
grep image: /apps/buildtag/compose.yaml

(cd /apps/buildtag && docker compose down -v >/dev/null 2>&1)
rm -rf /apps/buildtag
