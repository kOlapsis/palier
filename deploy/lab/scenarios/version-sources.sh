
# Versions read from the registry's OCI annotation and from tags with a named prefix, compared with the previous scan.
$PALIER exec -T palier /palier scan 2>/dev/null >/tmp/scan-now.json
python3 - /tmp/scan-gcr.json /tmp/scan-now.json <<'EOF'
import json, sys
def load(path):
    return {f'{st["name"]}/{s["name"]}': s for st in json.load(open(path)) for s in st["services"]}
before, now = load(sys.argv[1]), load(sys.argv[2])
for key in ("immich/database", "immich/redis"):
    s = now.get(key, {})
    print(key, s.get("image"), s.get("tag"), "→ version", s.get("version"), "from", s.get("version_source"),
          "| available", [r["tag"] for r in s.get("available") or []][-4:], "| error", s.get("detection_error") or "none")
changed = [k for k in now if k in before and k not in ("immich/database", "immich/redis")
           and (now[k].get("version"), [r["tag"] for r in now[k].get("available") or []]) != (before[k].get("version"), [r["tag"] for r in before[k].get("available") or []])]
print("other services compared:", len([k for k in now if k in before]) - 2, "| version or available versions changed:", changed or "none")
print("detection errors:", [k + ": " + s["detection_error"] for k, s in now.items() if s.get("detection_error")] or "none")
EOF
