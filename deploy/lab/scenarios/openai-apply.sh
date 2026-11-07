# Applies the plan of a stack (STACK, default outline) computed by OpenAI: a second Palier with PALIER_AI_PROVIDER=openai, its own data volume and no notification channel, beside the Palier of the bench.
STACK=${STACK:-outline}
env_of() { sed -n "s/^$1=//p" /opt/palier/compose.env; }
docker rm -f palier-openai >/dev/null 2>&1 || true
docker volume rm -f palier_openai-data >/dev/null
$PALIER run -d --name palier-openai --no-deps -v palier_openai-data:/var/lib/palier \
	-e PALIER_AI_PROVIDER=openai -e PALIER_AI_MODEL="$(env_of LAB_OPENAI_MODEL)" -e PALIER_AI_KEY="$(env_of LAB_OPENAI_KEY)" \
	-e PALIER_LISTEN= -e PALIER_AUTO=off -e PALIER_WEBHOOK_URL= -e PALIER_WEBHOOK_SECRET= -e PALIER_SMTP_HOST= \
	-e PALIER_NTFY_URL= -e PALIER_SLACK_URL= -e PALIER_DISCORD_URL= -e PALIER_TELEGRAM_URL= -e PALIER_OIDC_ISSUER= \
	palier serve >/dev/null
until docker exec palier-openai palier show "$STACK" 2>/dev/null | grep -q "AI plan"; do sleep 10; done
docker exec palier-openai palier show "$STACK" | sed -n '/^Plan/,/^$/p' | head -20
echo "before:"; outline_state
docker exec palier-openai palier apply "$STACK" --yes --json </dev/null | python3 -c 'import json,sys; v=json.load(sys.stdin); print("run", v["id"], v["status"], (v.get("error") or "")[:300]); [print("  ", e["at"][11:19], e["message"].splitlines()[0][:160]) for e in v["events"]]' || true
echo "after:"; outline_state
docker rm -f palier-openai >/dev/null
docker volume rm -f palier_openai-data >/dev/null
