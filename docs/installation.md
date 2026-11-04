# Installation

Palier runs in a container on the machine whose stacks it updates.

## The image

```
ghcr.io/kolapsis/palier:0.2.2
```

It is published for `linux/amd64` and `linux/arm64`. It contains only the `palier` binary, the `docker compose` binary and the root certificates, and runs as a non-root user.

## Verify the image

Each release image is built by the `release` workflow of this repository, signed with Sigstore, and carries its build provenance and SBOM. Check the signature with [cosign](https://docs.sigstore.dev/cosign/system_config/installation/):

```sh
cosign verify ghcr.io/kolapsis/palier:0.2.2 \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity https://github.com/kOlapsis/palier/.github/workflows/release.yml@refs/tags/v0.2.2
```

And the build provenance with the GitHub CLI:

```sh
gh attestation verify oci://ghcr.io/kolapsis/palier:0.2.2 --owner kOlapsis
```

To build it yourself instead: `docker build -t palier:local -f deploy/Dockerfile .`

## Run with `docker run`

```sh
docker run -d --name palier --restart unless-stopped \
  --group-add "$(stat -c %g /var/run/docker.sock)" \
  --read-only --cap-drop ALL --cap-add CHOWN --cap-add DAC_OVERRIDE \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v palier-data:/var/lib/palier \
  -v /srv/stacks:/srv/stacks \
  -v "$HOME/.docker/config.json":/etc/palier-docker/config.json:ro \
  -e DOCKER_CONFIG=/etc/palier-docker \
  -e PALIER_AI_KEY=sk-ant-... \
  -e PALIER_GITHUB_TOKEN=ghp_... \
  ghcr.io/kolapsis/palier:0.2.2
```

## Run with Compose

```yaml
name: palier

services:
  palier:
    image: ghcr.io/kolapsis/palier:0.2.2
    restart: unless-stopped
    group_add:
      - ${DOCKER_GID:?}
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

## Check that it runs

Add this alias on the host, in your `~/.bashrc` or `~/.zshrc`, then call `palier`:

```sh
alias palier='docker exec -it palier palier'
palier
```

The stacks appear as soon as the containers are read, marked "checking versions" until the first scan ends. It takes a few minutes; the [Usage](usage.md#first-start) page describes what happens in the meantime.

## Turn on the web interface

The web interface is off by default. To use it, set `PALIER_LISTEN=:8080`, publish the port, for example `-p 127.0.0.1:8080:8080`, and open `http://127.0.0.1:8080`.

To let only admins in, configure [sign-in with OpenID Connect](configuration.md#sign-in-with-openid-connect): authentik, Keycloak, Google, Microsoft Entra ID or any other provider.

!!! warning "Without OpenID Connect"
    Without `PALIER_OIDC_ISSUER`, the web interface has no sign-in: whoever reaches the port can apply updates, which is as powerful as the Docker socket. Keep it on `127.0.0.1`, or behind a reverse proxy that authenticates. In both cases, Palier refuses the changes a page from another site would try to send.

!!! note "Without an AI key"
    Palier works without `PALIER_AI_KEY`, but loses most of its value. It detects versions and proposes plans computed from versions alone, with an "unknown" risk level. These plans are applied on their own like the others, protected by the rollback, but nobody has read what changes, and required steps and dependencies are not taken into account.

## Logs

Palier writes its logs as JSON to standard output:

```sh
docker logs -f palier
```
