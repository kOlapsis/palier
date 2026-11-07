# Ten services from the lab registry and one from Quay in a throwaway stack: how many requests of Palier the registry served at once during a scan, and the Quay tag dates read in one list.
reg=10.10.50.10:5443
echo lab-registry-password | docker login $reg -u palier --password-stdin >/dev/null
docker pull -q traefik/whoami:v1.10.1 >/dev/null
mkdir -p /apps/reglimits
printf 'services:\n  quay:\n    image: quay.io/prometheus/node-exporter:v1.8.0\n' > /apps/reglimits/compose.yaml
for i in $(seq 1 10); do
	docker tag traefik/whoami:v1.10.1 $reg/lab/limit-$i:v1.10.1
	docker push -q $reg/lab/limit-$i:v1.10.1 >/dev/null
	printf '  w%s:\n    image: %s/lab/limit-%s:v1.10.1\n' $i $reg $i >> /apps/reglimits/compose.yaml
done
(cd /apps/reglimits && docker compose up -d >/dev/null 2>&1)
docker logout $reg >/dev/null
$PALIER exec -T palier palier postpone reglimits "$(date -d tomorrow +%F)" >/dev/null
sql "DELETE FROM tag_dates WHERE image = 'quay.io/prometheus/node-exporter'"
$PALIER restart palier >/dev/null 2>&1
until $PALIER exec -T palier palier status >/dev/null 2>&1; do sleep 2; done
start=$(date -u +%FT%TZ)
$PALIER exec -T palier palier scan >/dev/null
docker logs --since "$start" palier-labreg-1 2>&1 | python3 -c '
import re, sys
from datetime import datetime, timedelta
unit = {"s": 1, "ms": 1e-3, "µs": 1e-6, "ns": 1e-9}
spans = []
for line in sys.stdin:
    if "response completed" not in line or "Go-http-client" not in line:
        continue
    d = re.search(r"http.response.duration=([0-9.]+)(µs|ms|ns|s)", line)
    if not d:
        continue
    end = datetime.fromisoformat(re.search(r"time=\"([^\"]+)\"", line).group(1)[:26].rstrip("Z"))
    v, u = d.groups()
    spans.append((end - timedelta(seconds=float(v) * unit[u]), end))
edges = sorted([(s, 1) for s, _ in spans] + [(e, -1) for _, e in spans])
now = peak = 0
for _, d in edges:
    now += d
    peak = max(peak, now)
print(f"lab registry: {len(spans)} requests of Palier during the scan, at most {peak} at once")
'
docker logs --since "$start" palier-palier-1 2>&1 | grep -E '"tag dates|reglimits' | cut -c1-250
echo "quay tag dates stored: $(sql "SELECT count(*) || ', v1.9.0 pushed ' || coalesce((SELECT pushed FROM tag_dates WHERE image = 'quay.io/prometheus/node-exporter' AND tag = 'v1.9.0'), '?') FROM tag_dates WHERE image = 'quay.io/prometheus/node-exporter'")"
$PALIER exec -T palier palier show reglimits | sed -n '1,16p'
(cd /apps/reglimits && docker compose down >/dev/null 2>&1); rm -rf /apps/reglimits
