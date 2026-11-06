# Computes the plans of whoami, Outline and Immich twice in a scratch data volume: with Anthropic, then with a local model served by Ollama through PALIER_AI_PROVIDER=openai.
MODEL=${MODEL:-qwen3:4b-instruct-2507-q4_K_M}
$PALIER --profile ai-local up -d --quiet-pull ollama 2>/dev/null
until $PALIER exec -T ollama ollama list >/dev/null 2>&1; do sleep 1; done
$PALIER exec -T ollama ollama pull "$MODEL" >/dev/null && echo "model $MODEL ready"
docker volume rm -f palier_ai-local >/dev/null
for stack in ${STACKS:-whoami outline immich}; do
	for provider in ${PROVIDERS:-anthropic openai}; do
		set --
		[ "$provider" = openai ] && set -- -e PALIER_AI_PROVIDER=openai -e PALIER_AI_URL=http://ollama:11434/v1 -e PALIER_AI_MODEL="$MODEL" -e PALIER_AI_KEY=
		out=/tmp/ai-$provider-$stack
		start=$(date +%s)
		$PALIER run --rm --no-deps -T -v palier_ai-local:/var/lib/palier -e PALIER_LISTEN= "$@" \
			palier plan "$stack" --once --json </dev/null >$out.json 2>$out.log || { echo "== $stack, $provider: failed"; tail -5 $out.log; continue; }
		echo "== $stack, $provider, $(( $(date +%s) - start )) s"
		python3 - $out.json <<'PY'
import json, sys
r = json.load(open(sys.argv[1]))
if not r:
    print("  up to date"); sys.exit()
p = r["plan"]
print("  source", r["source"])
for i, s in enumerate(p.get("steps") or []):
    print(f"  step {i+1}: {', '.join(c['service']+' '+c['from']+' -> '+c['to'] for c in s['changes'])}; risk {s['risk']}, reversible {s['reversible']}, {len(s.get('facts') or [])} facts")
for x in p.get("incomplete") or []:
    print("  incomplete:", x[:300])
for x in p.get("suspicious") or []:
    print("  suspicious:", x[:200])
PY
	done
done
$PALIER --profile ai-local stop ollama >/dev/null
