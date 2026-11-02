# Installation

Palier runs in a container on the machine whose stacks it updates.

## Build the image

```sh
docker build -t palier:local -f deploy/Dockerfile .
```

The image contains only the `palier` binary, the `docker compose` binary and the root certificates. It runs as a non-root user.

## Run with `docker run`

```sh
docker run -d --name palier --restart unless-stopped \
  --group-add "$(stat -c %g /var/run/docker.sock)" \
  --read-only --cap-drop ALL --cap-add CHOWN --cap-add DAC_OVERRIDE \
  -p 127.0.0.1:8080:8080 \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v palier-data:/var/lib/palier \
  -v /srv/stacks:/srv/stacks \
  -v "$HOME/.docker/config.json":/etc/palier-docker/config.json:ro \
  -e DOCKER_CONFIG=/etc/palier-docker \
  -e PALIER_AI_KEY=sk-ant-... \
  -e PALIER_GITHUB_TOKEN=ghp_... \
  palier:local
```

## Run with Compose

```yaml
name: palier

services:
  palier:
    image: palier:local
    restart: unless-stopped
    group_add:
      - ${DOCKER_GID:?}
    ports:
      - "127.0.0.1:8080:8080"
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - palier-data:/var/lib/palier
      - /srv/stacks:/srv/stacks
      - ${HOME}/.docker/config.json:/etc/palier-docker/config.json:ro
    environment:
      DOCKER_CONFIG: /etc/palier-docker
      PALIER_AI_KEY: sk-ant-...
      PALIER_GITHUB_TOKEN: ghp_...
    read_only: true
    cap_drop: [ALL]
    cap_add: [CHOWN, DAC_OVERRIDE]

volumes:
  palier-data: {}
```

```sh
DOCKER_GID=$(stat -c %g /var/run/docker.sock) docker compose up -d
```

## What to give Palier

| What | Why |
|---|---|
| `PALIER_AI_KEY` | Needed in practice. The AI reads the notes and security advisories, builds the plan and judges each step. Without it, Palier works on version numbers only. |
| Docker Hub credentials (mounted `config.json`, `DOCKER_CONFIG`) | Docker Hub heavily limits anonymous access. With credentials, Palier reads tags, digests and publication dates without being blocked. |
| `PALIER_GITHUB_TOKEN` | GitHub release notes and security advisories are limited to 60 requests per hour without a token. |
| `PALIER_GITLAB_TOKEN` | Same for projects hosted on gitlab.com. |

A GitHub token with no particular permission is enough: Palier only reads public data.

## Three things to get right

**Mount the stacks folder at the same path as on the host.** Palier finds the Compose file of each stack at the path given by the labels of its containers (`com.docker.compose.project.working_dir`). If your stacks are in `/srv/stacks` on the host, mount `/srv/stacks:/srv/stacks`. With a different path in the container, Palier cannot find the files.

**The group of the Docker socket.** `--group-add` gives the container user the right to talk to the socket. The value is the group number of the socket on the host.

**`CHOWN` and `DAC_OVERRIDE`.** Palier rewrites Compose files while keeping their owner, group and mode. These two capabilities allow that, and everything else is dropped. Do not add `no-new-privileges`: it would cancel these capabilities, which the binary carries as file capabilities.

## Open the interface

The interface listens on port 8080. It is protected by a token, created at first start in the data directory:

```sh
docker exec palier /palier token
```

Open `http://127.0.0.1:8080`, paste the token, and the stack list appears after the first scan. To choose the token yourself, set `PALIER_TOKEN`.

The first start takes a few minutes, and the page updates by itself. The [Usage](usage.md#first-start) page describes what happens in the meantime.

!!! note "Without an AI key"
    Palier works without `PALIER_AI_KEY`, but loses most of its value. It detects versions and proposes plans computed from versions alone, with an "unknown" risk level. These plans are applied on their own like the others, protected by the rollback, but nobody has read what changes, and required steps and dependencies are not taken into account.

## Logs

Palier writes its logs as JSON to standard output:

```sh
docker logs -f palier
```
