# The Palier bench

Testing Palier on a development machine mixes its containers with the machine's own, and Palier then offers to update the machine's tools. This bench runs Palier in a libvirt virtual machine of its own, built and provisioned by Ansible, next to the services it needs to be tested against:

- Palier, built from this working copy, with `deploy/compose.yaml` and the override `compose.lab.yaml`;
- webhook-tester, which receives every webhook Palier sends;
- stacks under `/apps`, all in old versions, listed on the `apps` line of `topology.yml`. The default set is small: nginx, whoami, and Outline with its PostgreSQL and Redis. The large set is GitLab CE and Immich, whose upgrades are hard, and Vaultwarden, whose upgrade must go through end to end.

Nothing runs on the Docker engine of the host, and nothing in it is created, changed or deleted: everything happens in the machine.

## Prerequisites on the host

- Linux on x86_64 with KVM (`/dev/kvm`), libvirt (`qemu:///system`), `qemu-img`, and `xorrisofs` (or `genisoimage`) to build the cloud-init seed;
- `ansible-core` 2.15 or later, with the `community.libvirt` and `ansible.posix` collections (`requirements.yml`);
- `rsync`, `curl` and `ssh`;
- an SSH key pair: `LAB_SSH_KEY` points to the private key, its `.pub` must sit next to it. Without `LAB_SSH_KEY`, the script takes the first of `~/.ssh/id_ed25519`, `id_ecdsa` and `id_rsa`. The key must be usable without a passphrase prompt (no passphrase, or loaded in an ssh agent);
- 4 GiB of free memory with the small set, 10 GiB with the large set, and 60 GiB of disk under `/var/lib/libvirt/images`, the sizes set in `topology.yml`. To switch to the large set, set `apps: [gitlab, immich, vaultwarden]`, `vcpu: 4` and `memory_gb: 10` in `topology.yml`, then `down` and `up`.

The bench runs as the current user. Disks are libvirt storage volumes, so libvirtd creates their directories itself and `up` and `down` need no root.

## host-setup

```sh
deploy/lab/lab host-setup
```

Run it once. Through `sudo`, it installs the virtualization packages, starts libvirtd and adds the current user to the `libvirt` and `kvm` groups (log in again afterwards). Then, as the current user, it installs the Ansible collections. On a host that already has all of this, it is not needed.

## Commands

All commands are `deploy/lab/lab <command>`.

| Command | What it does |
|---|---|
| `up` | Creates what is missing (base image in cache, storage pools, system disk on top of the base image, cloud-init seed with your key and the static address, NAT network, machine), then provisions it with the `docker`, `palier_stack` and `lab_apps` roles. A second `up` changes nothing. |
| `deploy` | Redeploys Palier from the working copy, and only Palier: the stacks under `/apps` are not restarted, unlike `up`. Check first that no run is in progress, since restarting Palier rolls it back to its last verified step. |
| `status` | Prints the state of the machine, Palier, webhook-tester and each stack of `topology.yml`, checked over HTTP from the host. Exits 0 only if all of them are in service, 1 otherwise. |
| `ssh [cmd]` | Opens a shell on the machine as `debian`, or runs `cmd` there. |
| `scenario <name> [VAR=value…]` | Copies `scenarios/common.sh` and `scenarios/<name>.sh` to the machine and runs them as root, with the variables given: the replayable bench scenarios (`whoami-apply`, `whoami-missing-tag`, `whoami-exits`, `whoami-kill`, `whoami-reset` to put whoami back to v1.10.1 between them, `outline-apply`, `outline-fail-step3`, `outline-major`, `outline-major-fail`, `outline-pg16` to put postgres back on 16.14 and its old volume, `gitlab-fail-step2`, `gitlab-apply`, `immich-fail-step2`, `immich-apply`, `whoami-failed`, `webhooks` to list what webhook-tester received, and `ai-local` to compare the plans of Anthropic and of a local model; `reset-apps` puts Outline back to its start with its three seeded documents). |
| `reset-apps` | Stops the stacks of `topology.yml`, deletes their data and their compose files, then starts them again at the versions of `versions.yml` with fresh data (and, for GitLab and Immich, the seed: a project, an account). Use it to replay an upgrade scenario. |
| `down` | Deletes the machine, its disks, its seed, its storage pool and its network. Only the base image stays, in `~/.cache/palier-lab` and in the `palier-lab-base` pool, so the next `up` downloads nothing. |

## Trying a local model

`ai-local` starts Ollama (the `ai-local` profile of `compose.lab.yaml`, CPU only, context of 65,536 tokens), pulls the model, then computes the plan of each stack with `palier plan <stack> --once` in a scratch data volume, once per provider. The Palier of the bench keeps its own plans. Postpone the stacks first (`palier postpone whoami 2026-11-30` in the Palier container), otherwise the Palier of the bench may update them in the middle of the comparison.

```sh
deploy/lab/lab scenario whoami-reset
deploy/lab/lab scenario ai-local                                   # whoami, outline, immich; Anthropic then qwen3 4B
deploy/lab/lab scenario ai-local MODEL=qwen3:8b STACKS=whoami PROVIDERS=openai
```

Each plan is summed up on one line per step, followed by what Palier refused in it; the full JSON stays in `/tmp/ai-<provider>-<stack>.json` on the machine. A model takes 2.5 GB (4B) to 5 GB (8B) of disk in the `ollama-models` volume: check `df -h /` first. Ollama is stopped at the end; `docker compose --profile ai-local rm -sf ollama` and `docker volume rm palier_ollama-models` free the space. [Try a local model](../../docs/local-model.md) gives the same steps for a Palier outside the bench.

The engine of the machine pulls Docker Hub images through `mirror.gcr.io`, because the anonymous Docker Hub limit is shared with every machine behind the same public address.

The first `up` takes a while: it downloads the Debian image, installs Docker, builds Palier and pulls the images of the stacks. With the large set it also pulls GitLab (about 1.3 GB) and Immich, and waits for GitLab to start, which alone takes several minutes.

## Files

| File | Role |
|---|---|
| `topology.yml` | The only place where the address, the size of the machine, the base image, the stacks and the published ports are set. |
| `versions.yml` | The versions the stacks start from, and why each one was chosen. |
| `provision.yml`, `tasks/`, `templates/` | The machine on libvirt (`lab_state=present` or `absent`). |
| `site.yml`, `roles/` | Provisioning of the machine: `docker` (engine and compose plugin), `palier_stack` (Palier, webhook-tester), `lab_apps` (the stacks under `/apps`). |
| `compose.lab.yaml` | Override of `deploy/compose.yaml`: builds the image from the working copy, publishes the ports on the machine address, mounts `/apps` at the same path, turns the web interface on (`PALIER_LISTEN`) behind the OIDC test provider, and starts that provider. `compose.yaml` is never changed. |
| `host-setup.yml` | What needs root, run once. |

## Addresses and credentials

The machine is `10.10.50.10`, on the NAT network `palier-lab` (host side `10.10.50.1`).

| Service | Address | Credentials |
|---|---|---|
| Palier | http://10.10.50.10:8080 | sign-in on the test provider below, any user name, claims `{"groups": ["palier-admins"]}`; or `deploy/lab/lab ssh sudo docker exec -it palier-palier-1 palier` |
| Lab registry | https://10.10.50.10:5443, tokens from https://10.10.50.10:5001 | `palier` / `lab-registry-password`; TLS signed by the lab CA in `/opt/palier/labreg` |
| Mailpit | http://10.10.50.10:8025 | none; every email Palier sends arrives here |
| OIDC test provider | http://10.10.50.10:8090/palier | none; `mock-oauth2-server` signs in whoever fills its form, with the claims typed there |
| webhook-tester | http://10.10.50.10:8081 | none; the webhooks arrive in session `00000000-0000-4000-8000-000000000001` |
| nginx | http://10.10.50.10:8088 | none |
| whoami | http://10.10.50.10:8089 | none |
| Outline | http://10.10.50.10:3000 | none; no sign-in provider is configured, the bench only needs it running |
| GitLab | http://10.10.50.10:8929 (ssh on port 2224) | `root` / `palier-lab-password`; project `root/lab-demo` |
| Immich | http://10.10.50.10:2283 | `admin@palier.lab` / `palier-lab-password` |
| Vaultwarden | http://10.10.50.10:8222 | none; create an account from the web vault (the admin page is off) |

The `lab` script reads its variables from the environment and from `deploy/lab/lab.env` when that file exists (one `NAME=value` per line, ignored by git and never copied to the machine): `LAB_SSH_KEY`, `LAB_ANTHROPIC_API_KEY`, which becomes `PALIER_AI_KEY`, and the registry credentials `DOCKERHUB_USERNAME` and `DOCKERHUB_TOKEN`, `REGISTRY_ADDRESS`, `REGISTRY_USERNAME` and `REGISTRY_PASSWORD`. `up` writes the credentials into `/opt/palier/docker/config.json` on the machine, which Palier reads through `DOCKER_CONFIG`.

On the machine, Palier lives in `/opt/palier`: `src/` is the copy of the working tree, `compose.env` holds the values the override reads (ports, group of the Docker socket, keys). To drive it by hand:

```sh
deploy/lab/lab ssh
cd /opt/palier/src/deploy
sudo docker compose --env-file /opt/palier/compose.env -f compose.yaml -f lab/compose.lab.yaml ps
```

## Redeploying after a code change

```sh
deploy/lab/lab up
```

`up` copies the working tree to the machine with rsync (only what the image build reads: `go.mod`, `go.sum`, `cmd/`, `internal/`, `deploy/`; never `.env` files), builds the `palier:local` image on the machine with `docker compose build`, and recreates the container when the image changed. The build runs in the machine, so the host engine is never used. Without a code change, the build is served from the cache and nothing is recreated.

## The stacks

The stacks are Compose projects named after their folder and started from `/apps`, which the override mounts at the same path, so the paths in the Compose labels are valid inside Palier. The reasons for each starting version are in `versions.yml`.

### nginx 1.27.0, whoami v1.10.1 and Outline 1.10.1 (small set)

nginx and whoami are single containers without data. Outline migrates its database when it starts, so its update changes data; it depends on PostgreSQL 16.4, behind on its own 16.x line, and on Redis 7.2.4. The `lab_apps` role writes three documents into its database (`files/seed-outline.sql`), which the scenarios count after each upgrade.

### lab-observed and lab-republished

Two containers started with `docker run`, outside Compose, from GHCR images: stacks Palier only observes. `lab-observed` runs the moving tag `2.2`, whose version Palier reads from the digest; `lab-republished` runs the content of 2.2.7 under the tag 2.3.0, as if the registry had republished 2.3.0. The reasons are in `versions.yml`.

### GitLab CE 16.1.6 (large set)

The image carries the label `org.opencontainers.image.version=22.04`, inherited from its Ubuntu base, which contradicts the tag: the tag wins.

GitLab forbids skipping its required upgrade stops ([upgrade paths](https://docs.gitlab.com/update/upgrade_paths/)): from 16.1, the path goes through 16.3.9, 16.7.10, 16.11.10, then the 17.x stops (17.3.7, 17.5.5, 17.8.7, 17.11.7) and the 18.x stops, with the background migrations finished after each stop. Palier's GitLab card carries them. The page also lists conditional stops: 16.1.8 (instances with NPM packages), 16.2.11 (large pipeline variables history) and 17.1.8.

### Immich v1.105.1 (large set)

The last release before two sets of Compose breaking changes, from the official release notes:

- [v1.106.1](https://github.com/immich-app/immich/releases/tag/v1.106.1): the `immich-microservices` service is removed, `command: ['start.sh', 'immich']` goes away from `immich-server`, `SERVER_PORT`, `MICROSERVICES_PORT` and `MACHINE_LEARNING_PORT` become `IMMICH_PORT`, `HOST` and `MACHINE_LEARNING_HOST` become `IMMICH_HOST`;
- [v1.133.0](https://github.com/immich-app/immich/releases/tag/v1.133.0): the database image `tensorchord/pgvecto-rs` is replaced by `ghcr.io/immich-app/postgres` (VectorChord), the `command:` and healthcheck of the database go away, and upgrading directly is only supported from 1.107.2 or later.

The stack runs the upstream `docker-compose.yml` of v1.105.1 with its `.env`. `ghcr.io/immich-app/immich-server` has more tags than the default page size of a tag list.

### Vaultwarden 1.35.4 (large set)

One container on SQLite, a semver release every few weeks, a `HEALTHCHECK` in the image, and no Compose or configuration change between 1.35.4 and the latest 1.37.x in the [release notes](https://github.com/dani-garcia/vaultwarden/releases). The bench publishes it on 8222, runs it without a seed, and `lab status` calls `/alive`.
