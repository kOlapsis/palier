# Try a local model

Palier can plan with a model you host, through any API compatible with OpenAI's Chat Completions: Ollama, vLLM, LM Studio. This page shows how to try one on your own stacks before switching to it, without touching the Palier that runs.

## What to expect

The model must follow long instructions, answer in a strict JSON schema, and copy quotes word for word from release notes that can exceed 35,000 tokens. Palier checks everything it returns: a plan with an invented operation, a missing step or a quote that is not in the notes is marked incomplete and never applied. A model that is too small therefore costs you nothing but time: its plans are refused.

On the Palier bench (6 CPU cores, no GPU), `qwen3:4b-instruct-2507-q4_K_M` took 6 to 7 minutes per stack, and every plan it returned was refused: invented checks, a missing `recreate` after `set_image`, facts quoted inexactly. `qwen3:8b` gave a much better plan for a small stack in 16 minutes, refused only for an invented check, and did not answer within 30 minutes for Outline, whose notes take 35,000 tokens. Palier waits 30 minutes per request by default: raise `PALIER_AI_TIMEOUT` (`2h`, for example) for a model on CPU. Claude Sonnet and OpenAI's models take 7 to 50 seconds and their plans pass. Plan on a GPU, then judge on your own stacks.

## 1. Start Ollama next to Palier

Add the service to the Compose file of Palier, in the same project so that Palier reaches it by its name:

```yaml
services:
  ollama:
    image: ollama/ollama
    restart: unless-stopped
    environment:
      OLLAMA_CONTEXT_LENGTH: "65536"
    volumes:
      - ollama-models:/root/.ollama

volumes:
  ollama-models: {}
```

`OLLAMA_CONTEXT_LENGTH` matters: below the size of the prompt, Ollama refuses the request, and Palier falls back to a plan computed from versions alone. With an NVIDIA GPU, give the service the GPU as the [Ollama image documentation](https://hub.docker.com/r/ollama/ollama) describes.

```sh
docker compose up -d ollama
docker compose exec ollama ollama pull qwen3:8b
```

A model takes several gigabytes of disk and as much memory, plus the context: count 5 GB of disk and 10 GB of memory for an 8B model with a context of 65,536.

## 2. Compute a plan with it, beside the running Palier

`palier plan <stack> --once` computes a plan in a one-off container and prints it as JSON. Give that container its own data volume, so that the plans of the running Palier stay as they are:

```sh
docker compose run --rm --no-deps -T \
  -v palier-trial:/var/lib/palier \
  -e PALIER_AI_PROVIDER=openai \
  -e PALIER_AI_URL=http://ollama:11434/v1 \
  -e PALIER_AI_MODEL=qwen3:8b \
  -e PALIER_AI_KEY= \
  -e PALIER_LISTEN= \
  palier plan outline --once --json > plan-local.json
```

Run the same command without the four `PALIER_AI_*` variables to get the plan of your current provider in `plan-anthropic.json`, then compare. Only a stack with an update available has a plan; an up-to-date stack prints `null`.

## 3. Read the result

```sh
jq '{source, steps: [(.plan.steps // [])[] | {changes, risk, reversible, facts: (.facts | length)}], incomplete: .plan.incomplete}' plan-local.json
```

- `source` is `ai` when the model answered. `versions` means it did not, and the first line of `incomplete` says why, with the message of the server (context too small, model not pulled, timeout).
- `incomplete` lists everything Palier refused in the plan. An empty list means the plan would be applied.
- `facts` counts the statements of the notes the model quoted. A good plan for an application with release notes quotes several.

Remove the trial volume when you are done: `docker volume rm palier-trial`.

## 4. Switch

When the plans of the local model suit you, set the same variables on the `palier` service and restart it:

```yaml
services:
  palier:
    environment:
      PALIER_AI_PROVIDER: openai
      PALIER_AI_URL: http://ollama:11434/v1
      PALIER_AI_MODEL: qwen3:8b
```

Changing the provider, the address or the model computes every plan again. The `/settings` page shows the provider in use.

vLLM works the same way: start it with `--max-model-len 65536`, and set `PALIER_AI_URL` to its address, ending in `/v1`.
