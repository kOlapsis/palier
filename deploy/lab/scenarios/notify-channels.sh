# Shows what ntfy.sh and the Slack, Discord, Telegram and webhook sessions of webhook-tester received since START (epoch seconds, default the last 10 minutes); ACTION=test sends a test message first, ACTION=outage stops webhook-tester during a test.
NTFY=$(docker inspect palier-palier-1 --format '{{range .Config.Env}}{{println .}}{{end}}' | sed -n 's/^PALIER_NTFY_URL=//p')
START=${START:-$(( $(date +%s) - 600 ))}
case ${ACTION:-show} in
test) $PALIER exec -T palier palier notify test || true ;;
outage)
	$PALIER stop webhook-tester >/dev/null 2>&1
	$PALIER exec -T palier palier notify test || true
	$PALIER start webhook-tester >/dev/null 2>&1
	;;
esac
sleep 2
$PALIER exec -T palier palier notify | sed -n '1,12p'
for s in 1:webhook 2:slack 3:discord 4:telegram; do
	curl -s "http://127.0.0.1:8081/api/session/00000000-0000-4000-8000-00000000000${s%%:*}/requests" | python3 -c '
import base64, hashlib, hmac, json, sys
name, start = sys.argv[1], int(sys.argv[2])
reqs = sorted((r for r in json.load(sys.stdin) if r["captured_at_unix_milli"] >= start * 1000), key=lambda r: r["captured_at_unix_milli"])
print(f"== {name}: {len(reqs)} received")
for r in reqs:
    body = base64.b64decode(r["request_payload_base64"])
    headers = {h["name"]: h["value"] for h in r["headers"]}
    path = r["url"].split("8080", 1)[-1]
    if name == "webhook":
        sig = "sha256=" + hmac.new(b"lab-webhook-secret", body, hashlib.sha256).hexdigest()
        e = json.loads(body)
        print(" ", e.get("at"), e.get("event"), e.get("stack"), "signature", "valid" if headers.get("X-Palier-Signature") == sig else "INVALID " + headers.get("X-Palier-Signature", "missing"))
        continue
    v = json.loads(body)
    text = v.get("text") or v.get("content") or ""
    print(" ", path[-30:], {k: v[k] for k in v if k not in ("text", "content")})
    print("   ", text.replace("\n", "\n    ")[:900])
' "${s#*:}" "$START"
done
echo "== ntfy ($NTFY)"
curl -s "$NTFY/json?poll=1&since=$START" | python3 -c '
import json, sys
for line in sys.stdin:
    m = json.loads(line)
    print(" ", m.get("title"), "| priority", m.get("priority", 3), "| tags", m.get("tags"), "| click", m.get("click"))
    print("   ", m["message"].replace("\n", "\n    ")[:600])
'
