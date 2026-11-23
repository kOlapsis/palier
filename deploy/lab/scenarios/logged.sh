# What Palier wrote to its log since SINCE (default: the last hour): how many lines of each kind, and one of each.
# Run it after other scenarios, whose scans, decisions and runs it sums up.
docker logs --since "${SINCE:-1h}" palier-palier-1 2>&1 | python3 -c '
import collections, json, sys
count, sample = collections.Counter(), {}
for line in sys.stdin:
    try: e = json.loads(line)
    except ValueError: continue
    key = e["level"] + " " + e["msg"]
    count[key] += 1
    sample[key] = {k: (v[:90] + "…" if isinstance(v, str) and len(v) > 90 else v) for k, v in e.items() if k not in ("time", "level", "msg")}
for key, n in sorted(count.items(), key=lambda kv: -kv[1]):
    print("%5d  %-45s %s" % (n, key, json.dumps(sample[key], ensure_ascii=False)[:260]))'
