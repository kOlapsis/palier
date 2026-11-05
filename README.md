<p align="center">
  <img src="docs/assets/hero.svg" alt="Palier: Docker Compose updates in verified steps" width="100%">
</p>

<p align="center"><strong>Docker Compose updates that read the release notes before touching anything.</strong></p>

<p align="center">
  <a href="#quick-start">Quick start</a> ·
  <a href="docs/index.md">Documentation</a>
</p>

Palier updates the Docker Compose stacks of a machine on its own. It does more than bump a tag: it reads the release notes of every skipped version, has an AI build a plan, checks that plan, then applies it in steps. If a step fails, the whole stack goes back to the last step that worked.

One Go binary, one container, the Docker socket. No agent, no account, no license.

---

## The problem

Bumping a tag is easy. What breaks a self-hosted stack is everything else:

- a **required intermediate version** that was skipped;
- a **Compose file that changes**: a removed service, a renamed variable, a replaced base image;
- a **database** that changes major version and refuses to start on the old volume;
- a **dependency** upgraded beyond what the application supports;
- a failure in the middle, leaving a **half-updated** stack.

All of this is written in the release notes. Palier reads them for you.

## What it did on the bench

Every Palier feature is proven in a virtual machine with a real Docker, on real applications.

**GitLab CE 16.1.6 → 19.5.1, in 13 steps.** GitLab forbids skipping its required upgrade stops. Palier followed the path 16.3.9 → 16.7.10 → 16.11.10 → 17.3.7 → … → 19.5.1, waiting for the background migrations to finish at each stop (up to 14 minutes for 18.8.11). Duration: 3 h 43, unattended. At the end, GitLab is `healthy` with its project, its issue and its accounts, and `compose.yaml` kept its owner and mode.

**Immich v1.105.1 → v3.3.5, with two Compose breaking changes.** Palier removed the `immich-microservices` service, dropped a `command`, set the port variable the notes ask for, and replaced the database image (`pgvecto-rs` → VectorChord) at the step where Immich requires it. Eight steps, all verified. Photos downloaded afterwards have the same SHA-1 as before.

**A failure forced at the second step.** The `docker-compose.yml` file, the `.env` and the four volumes come back identical, the removed service reappears, and the data is intact.

## How it works

```
 inventory ──► versions ──► release notes ──► plan (AI) ──► validation ──► step-by-step apply
                              + security advisories           (code)         │
                                                                             ├─ restore point
                                                                             ├─ step
                                                                             ├─ Docker verification
                                                                             └─ failure? back to the last verified step
```

1. **Inventory.** Palier reads the containers of the Docker engine, groups the Compose ones into stacks, and records images, digests, HEALTHCHECK, volumes and `depends_on`.
2. **Versions.** It finds newer versions in the registry (Docker Hub, GHCR, any OCI registry). Prereleases are excluded, variants such as `-alpine` are kept, and a version must have matured for 72 h before it is proposed.
3. **Notes.** It reads the notes of **every** version between the running one and the target (GitHub, gitlab.com), the GitHub security advisories that affect the running version, and the documentation excerpts from its catalog.
4. **Plan.** The AI picks operations from a closed list: `set_image`, `compose_edit`, `recreate`, `stop`, `start`, `wait_stable`, `exec_check`, `db_dump_restore`. It never writes a command.
5. **Validation.** The code rejects any plan that skips a required step, targets a version that does not exist, upgrades a dependency beyond what the application cites as supported, or states something without quoting it word for word from a note.
6. **Apply.** Step by step, with a restore point of the whole stack (Compose files, `.env`, images, volume copies) before each step.
7. **Verification.** Only what Docker reports: the container is running, stays stable, does not loop, and is `healthy` if it has a HEALTHCHECK.

## Safeguards

- **A rollback on every failure.** A step is never left half applied. If Palier is stopped in the middle of a run, it puts the stack back at the last verified step when it restarts.
- **The stack follows its application.** A database or cache never goes higher than what the application declares it supports. Without a citation, it stays on its major version.
- **Database major upgrades that keep the data safe.** PostgreSQL, MySQL and MariaDB change major version by export and reimport on a new volume. The old volume is never touched, and Palier compares roles, databases, tables, rows, indexes and constraints before going on.
- **PostgreSQL collations.** After a patch release that changes the C library of the image, Palier rebuilds the affected indexes and updates the collation version.
- **Vulnerabilities first.** A version that fixes a vulnerability in the running version skips the maturation delay, and before a long climb Palier first applies the patch of the running line that closes it.
- **Notes are data.** Release notes, image names and logs are never treated as instructions or displayed as HTML.
- **The Compose file stays the source of truth.** Palier edits it in place, keeps its owner, group and mode, and hides no state on the side.
- **Emails that do not flood you.** One email when a problem opens, one when it is resolved, and a weekly report only when something happened.
- **A backup hook.** The `palier.hook.backup` label runs your backup command in the container before any change. If it fails, nothing is applied.

## Automatic mode

By default (`PALIER_AUTO=all`), Palier applies any complete plan on its own, major versions included, one stack after the other. It asks for a decision only for what it cannot do alone: a blocking step, an irreversible step or an incomplete plan.

Prefer to stay in control? `minor` excludes major versions, `patch` keeps only patch releases, `off` makes you approve everything with `palier apply`.

## Quick start

The image is published on GHCR for `linux/amd64` and `linux/arm64`, signed, with its build provenance and SBOM ([how to verify it](docs/installation.md#verify-the-image)).

```yaml
name: palier

services:
  palier:
    image: ghcr.io/kolapsis/palier:0.2.4
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
alias palier='docker exec -it palier palier'
palier
```

The state of every stack appears as soon as the containers are read, and the versions after the first scan.

Three things to get right:

- **Mount the stacks folder at the same path as on the host** (`/srv/stacks` here). Palier finds each Compose file at the path given by the container labels.
- **`CHOWN` and `DAC_OVERRIDE`** let it rewrite Compose files while keeping owner, group and mode. Everything else is dropped. Do not add `no-new-privileges`, which would cancel these two capabilities.
- **An AI key** is needed in practice. Without it, Palier works on version numbers only, without reading what changes.

## Configuration

| Variable | Default | Purpose |
|---|---|---|
| `PALIER_AI_KEY`, `PALIER_AI_MODEL` | empty, `claude-sonnet-5-5` | AI that reads the notes and builds the plan |
| `PALIER_AUTO` | `all` | `all`, `minor`, `patch` or `off` |
| `PALIER_SCAN_INTERVAL` | `1h` | Scan frequency |
| `PALIER_MATURATION` | `72h` | Minimum age of a proposed version |
| `PALIER_STABLE_SECONDS` | `60` | Stability time required at each verification |
| `PALIER_SMTP_HOST`, `PALIER_SMTP_PORT`, `PALIER_SMTP_USER`, `PALIER_SMTP_PASSWORD`, `PALIER_SMTP_FROM`, `PALIER_EMAIL_TO` | empty, `587` | Emails: one when a problem opens, one when it is resolved, and a weekly report when something happened |
| `PALIER_EMAIL_LEVEL` | `info` | `alert` keeps only the alerts and the weekly report |
| `PALIER_WEBHOOK_URL` | empty | JSON on every plan ready, run end, rollback or failure |
| `PALIER_GITHUB_TOKEN`, `PALIER_GITLAB_TOKEN` | empty | Notes and security advisories without the anonymous limit |
| `PALIER_REGISTRY_MIRROR` | empty | Docker Hub mirror |
| `PALIER_LISTEN` | empty | Address of the web interface, off when empty (`:8080` to turn it on) |
| `PALIER_OIDC_ISSUER`, `PALIER_OIDC_CLIENT_ID`, `PALIER_OIDC_CLIENT_SECRET`, `PALIER_OIDC_ADMINS` | empty | Sign-in to the web interface with authentik, Keycloak, Google, Microsoft…, admins only |

The full list is in [docs/configuration.md](docs/configuration.md).

## Command line

Palier runs in its container. Add this alias on the host, in your `~/.bashrc` or `~/.zshrc`:

```sh
alias palier='docker exec -it palier palier'
```

The `-it` gives the command a terminal: colors, and the confirmation of `apply`. Then:

```sh
palier                       # state of every stack
palier show gitlab           # services, advisories, upgrade path, plan and its sources
palier apply gitlab          # shows the plan, asks, then follows the run live
palier apply gitlab --dry-run
palier runs                  # last runs
palier log 14 -f             # log of a run
palier ignore gitlab gitlab 18.11.4-ce.0
palier postpone gitlab 2026-12-01   # no automatic apply before this date
palier scan                  # scan now
palier help                  # every command
```

```
Last scan 4 min ago.

STACK      STATE              RUNNING      TARGET        NOTE
authentik  awaiting decision  2025.8.3     2026.8.5      13 security advisories, 1 critical; 6 steps
gitlab     stopped            18.5.3-ce.0  18.11.4-ce.0  2 steps; run 15: stopped after step 1 of 3: background_migrations still failing…
traefik    up to date         v3.5.2
```

Every command takes `--json`. `apply` asks for confirmation in a terminal; `--yes` skips it, `--detach` returns as soon as the run starts. The run goes on in Palier if you close the terminal. In a script or a cron job, call `docker exec palier palier …` without `-it`.

The web interface is optional: set `PALIER_LISTEN=:8080` and publish the port. With `PALIER_OIDC_*`, only the admins of your identity provider get in ([details](docs/configuration.md#sign-in-with-openid-connect)); without it, keep it on `127.0.0.1` or behind a reverse proxy that authenticates.

## Limitations

- One Palier per Docker machine: no agent, no remote machine.
- No accounts or roles: the web interface lets in the admins of an OpenID Connect provider.
- Email and the webhook are the only channels.
- Databases other than PostgreSQL, MySQL and MariaDB stay blocked on their major version.
- Notes outside GitHub and gitlab.com are not read.
- Containers started outside Compose are only observed.
- No cleanup of old images, no update of Palier itself.

The details are in [docs/limitations.md](docs/limitations.md).

## Documentation

The full documentation is in [`docs/`](docs/index.md): [installation](docs/installation.md), [usage](docs/usage.md), [configuration](docs/configuration.md), [how it works](docs/how-it-works.md). To read it locally:

```sh
docker compose -f docs/compose.yaml up
```

then open http://127.0.0.1:8000.
