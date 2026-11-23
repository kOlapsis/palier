# The palier.maturation label sets the maturation delay of one service: shorter, longer, or refused when it is not a duration.
# web runs versions pushed to the lab registry minutes ago, long runs memcached 1.6.30 from Docker Hub, years of versions behind.
# KEEP=1 leaves the stack in place with the labels of the last case, to look at its page.
reg=10.10.50.10:5443
repo=$reg/lab/fresh
echo lab-registry-password | docker login $reg -u palier --password-stdin >/dev/null 2>&1
for v in 1.0.0 1.1.0 1.2.0; do
	lab_image "$repo:$v" "echo fresh $v; exec sleep infinity"
	docker push -q "$repo:$v" >/dev/null
done
docker rmi "$repo:1.1.0" "$repo:1.2.0" >/dev/null
sql "DELETE FROM tag_dates WHERE image = '$repo'"
mkdir -p /apps/matur
# labelled <maturation of web> <maturation of long>: writes the Compose file, a label only where a value is given, and starts the stack.
labelled() {
	{
		printf 'services:\n  web:\n    image: %s:1.0.0\n    labels:\n      palier.auto: "off"\n' "$repo"
		[ -z "$1" ] || printf '      palier.maturation: "%s"\n' "$1"
		printf '  long:\n    image: docker.io/library/memcached:1.6.30\n    labels:\n      palier.auto: "off"\n'
		[ -z "$2" ] || printf '      palier.maturation: "%s"\n' "$2"
	} >/apps/matur/compose.yaml
	(cd /apps/matur && docker compose up -d >/dev/null 2>&1)
	rescan matur
	$PALIER exec -T palier palier show matur --json | python3 -c '
import json, sys
v = json.load(sys.stdin)
short = lambda l: [r["tag"] + " " + r["pushed"][:10] for r in l or []]
for s in v["stack"]["services"]:
    a, m = short(s.get("available")), short(s.get("maturing"))
    print("  %-4s label %-7s available %d %s | maturing %d %s" % (s["name"], s.get("maturation") or "none", len(a), a[:1] + ["…"] + a[-1:] if len(a) > 2 else a, len(m), m[:1] + ["…"] + m[-1:] if len(m) > 2 else m))
print("  stack errors:", v["stack"].get("errors") or "none")
p = (v.get("record") or {}).get("plan") or {}
print("  plan:", [c["service"] + " " + c["from"] + " → " + c["to"] for st in p.get("steps") or [] for c in st["changes"]] or "no step")'
}

echo "== no label: PALIER_MATURATION, 72 h on the bench"
labelled "" ""
echo "== web 3d, long -1h: neither is a duration Palier accepts"
labelled 3d -1h
echo "== web 0s, long 8760h (one year)"
labelled 0s 8760h
[ -z "${KEEP:-}" ] || { docker logout $reg >/dev/null; exit 0; }

(cd /apps/matur && docker compose down -v >/dev/null 2>&1)
docker rmi "$repo:1.0.0" >/dev/null 2>&1 || true
rm -rf /apps/matur
docker logout $reg >/dev/null
