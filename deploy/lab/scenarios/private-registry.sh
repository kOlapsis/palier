# A private registry that only accepts tokens from a token server, behind TLS signed by the lab CA, as ECR, Artifact
# Registry and ACR do: Palier reads its tags and docker compose pulls its images through the credential helper.
reg=10.10.50.10:5443
echo "anonymous tag list: $(curl -s -o /dev/null -w '%{http_code}' --cacert /opt/palier/labreg/certs/ca.pem https://$reg/v2/lab/whoami/tags/list)"
echo lab-registry-password | docker login $reg -u palier --password-stdin >/dev/null
for t in v1.10.1 v1.12.0; do
	docker pull -q traefik/whoami:$t >/dev/null
	docker tag traefik/whoami:$t $reg/lab/whoami:$t
	docker push -q $reg/lab/whoami:$t >/dev/null
	docker rmi -f $reg/lab/whoami:$t >/dev/null
done
echo "pushed $reg/lab/whoami v1.10.1 and v1.12.0"
mkdir -p /apps/privreg
printf 'services:\n  whoami:\n    image: %s/lab/whoami:v1.10.1\n' "$reg" > /apps/privreg/compose.yaml
(cd /apps/privreg && docker compose up -d >/dev/null 2>&1)
docker logout $reg >/dev/null
echo "privreg: $(docker inspect privreg-whoami-1 --format '{{.Config.Image}} {{.State.Status}}')"
docker rmi -f $reg/lab/whoami:v1.12.0 >/dev/null 2>&1 || true
$PALIER exec -T palier palier postpone privreg "$(date -d tomorrow +%F)" >/dev/null
$PALIER exec -T palier palier scan >/dev/null
for i in $(seq 1 30); do
	$PALIER exec -T palier palier show privreg --json | python3 -c 'import json,sys; sys.exit(0 if json.load(sys.stdin).get("record") else 1)' && break
	sleep 10
done
$PALIER exec -T palier palier show privreg | head -8
$PALIER exec -T palier palier apply privreg --yes --json </dev/null | python3 -c 'import json,sys; v=json.load(sys.stdin); print("run", v["id"], v["status"], (v.get("error") or "")[:300]); [print("  ", e["at"][11:19], e["message"].splitlines()[0][:160]) for e in v["events"]]' || true
echo "after: $(docker inspect privreg-whoami-1 --format '{{.Config.Image}} {{.State.Status}}')"
(cd /apps/privreg && docker compose down >/dev/null 2>&1); rm -rf /apps/privreg
