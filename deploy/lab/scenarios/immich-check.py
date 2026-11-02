# Signs in to Immich, lists the assets and downloads each original, printing its name and SHA-1.
import hashlib, json, sys, urllib.request

base = "http://127.0.0.1:2283/api"

def call(path, body=None, token=None):
    req = urllib.request.Request(base + path, data=json.dumps(body).encode() if body is not None else None, method="POST" if body is not None else "GET")
    req.add_header("Content-Type", "application/json")
    if token:
        req.add_header("Authorization", "Bearer " + token)
    with urllib.request.urlopen(req, timeout=30) as r:
        return r.read()

try:
    token = json.loads(call("/auth/login", {"email": "admin@palier.lab", "password": "palier-lab-password"}))["accessToken"]
    items = json.loads(call("/search/metadata", {}, token))["assets"]["items"]
except Exception as e:
    print("  photos: Immich does not answer on 2283:", e)
    sys.exit(0)
for a in sorted(items, key=lambda a: a["originalFileName"]):
    data = None
    for path in ("/assets/%s/original" % a["id"], "/asset/file/%s" % a["id"]):
        try:
            data = call(path, token=token)
            break
        except Exception:
            pass
    print("  photo %s : %s" % (a["originalFileName"], hashlib.sha1(data).hexdigest() if data else "original unreadable"))
