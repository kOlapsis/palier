# Lists the webhooks webhook-tester received from Palier, oldest first.
curl -s http://127.0.0.1:8081/api/session/00000000-0000-4000-8000-000000000001/requests | python3 -c '
import base64, json, sys
reqs = sorted(json.load(sys.stdin), key=lambda r: r["captured_at_unix_milli"])
print(len(reqs), "webhooks received")
for r in reqs:
    e = json.loads(base64.b64decode(r["request_payload_base64"]))
    detail = e.get("status") or ", ".join(e.get("changes") or [])
    print(" ", e.get("at"), e.get("event"), e.get("stack"), "run=%s" % e["run"] if "run" in e else "", detail[:120], (e.get("error") or "")[:120])
'
