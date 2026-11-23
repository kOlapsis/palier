# Docker Hub tag dates: the list is read most recent first and stops at the tag sought. Without credentials it is cut
# after ten pages, and the tags beyond take the date of the oldest tag read instead of one manifest read each.
# The nginx stack of the bench runs 1.27.0; nginx 1.21.0 has two more years of versions behind it. Each scan runs in a
# process of its own, after forgetting the dates of nginx.
once() {
	name=$1; shift
	sql "DELETE FROM tag_dates WHERE image = 'docker.io/library/nginx'"
	start=$(date +%s)
	$PALIER exec -T "$@" palier palier scan --once >/tmp/hub-$name.json 2>/tmp/hub-$name.log || true
	echo "  scan: $(( $(date +%s) - start )) s"
	python3 - /tmp/hub-$name.json /tmp/hub-$name.log <<'EOF'
import collections, json, sys
api, manifests, lines = collections.Counter(), collections.Counter(), []
for line in open(sys.argv[2]):
    try: e = json.loads(line)
    except ValueError: continue
    url = e.get("url", "")
    if e.get("msg") == "registry request" and "repositories/nginx/tags" in url:
        api[e.get("status")] += 1
    elif e.get("msg") == "registry request" and "/library/nginx/" in url:
        manifests[e["method"] + " " + url.split("/")[0] + " " + ("manifest" if "/manifests/" in url else "blob" if "/blobs/" in url else "tags")] += 1
    elif e.get("msg", "").startswith("tag dates") and e.get("image") == "docker.io/library/nginx":
        lines.append({k: v for k, v in e.items() if k not in ("time", "level")})
    elif e.get("msg") in ("registry throttled", "credential helper"):
        lines.append({k: v for k, v in e.items() if k not in ("time", "level")})
print("  Docker Hub API, tag list of nginx:", dict(api) or "no request")
print("  registry requests for nginx:", dict(manifests) or "none")
for l in lines:
    print("  log:", str(l)[:330])
for st in json.load(open(sys.argv[1])):
    for s in st["services"]:
        if st["name"] in ("hubdates", "nginx"):
            rel = (s.get("available") or []) + (s.get("maturing") or [])
            dates = collections.Counter(r.get("pushed", "none")[:10] for r in rel)
            print("  %s %s: %d newer versions, %d different dates, %s … %s; most frequent date %s; error: %s" % (st["name"], s["tag"], len(rel), len(dates),
                rel[0]["tag"] + " " + rel[0].get("pushed", "")[:10] if rel else "-", rel[-1]["tag"] + " " + rel[-1].get("pushed", "")[:10] if rel else "-",
                dates.most_common(1), s.get("detection_error") or "none"))
EOF
}
echo "== with the Docker Hub credentials of the bench, nginx 1.27.0 of the bench alone"
once alone
mkdir -p /apps/hubdates
printf 'services:\n  web:\n    image: docker.io/library/nginx:1.21.0\n    labels:\n      palier.auto: "off"\n' >/apps/hubdates/compose.yaml
(cd /apps/hubdates && docker compose up -d >/dev/null 2>&1)
echo "hubdates: $(docker inspect hubdates-web-1 --format '{{.Config.Image}} {{.State.Status}}')"
echo "== the same with nginx 1.21.0 beside it"
once with
echo "== without credentials"
once without -e DOCKER_index_docker_io_USR= -e DOCKER_index_docker_io_PSW=
sql "DELETE FROM tag_dates WHERE image = 'docker.io/library/nginx'"

(cd /apps/hubdates && docker compose down -v --rmi all >/dev/null 2>&1)
rm -rf /apps/hubdates
