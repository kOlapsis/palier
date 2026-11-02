# Configuration

All configuration goes through environment variables. The `/settings` page of the interface shows the values read, read-only. An invalid value stops Palier at startup with a message that names the variable.

## Variables

| Variable | Default | Purpose |
|---|---|---|
| `PALIER_DOCKER_HOST` | `unix:///var/run/docker.sock` | Docker engine. |
| `PALIER_DATA_DIR` | `/var/lib/palier` | SQLite database, token, database exports. |
| `PALIER_LISTEN` | `:8080` | Listen address of the interface. |
| `PALIER_TOKEN` | generated at first start | Access token for the interface. Without a value, it is written to `token` in the data directory. |
| `PALIER_AI_KEY` | empty | Anthropic API key. Without a key, plans are computed from versions alone. |
| `PALIER_AI_MODEL` | `claude-sonnet-5-5` | Model used for plans. |
| `PALIER_SCAN_INTERVAL` | `1h` | Interval between two scans (Go duration: `30m`, `2h`…). |
| `PALIER_MATURATION` | `72h` | Minimum age of a version before it is proposed. A version that fixes a vulnerability in the running version does not wait. |
| `PALIER_STABLE_SECONDS` | `60` | Time a service must stay stable at each verification. |
| `PALIER_AUTO` | `all` | `all`, `minor`, `patch` or `off`. See [automatic apply](usage.md#automatic-apply). |
| `PALIER_WEBHOOK_URL` | empty | Address that receives events as JSON. |
| `PALIER_GITHUB_TOKEN` | empty | Reads GitHub release notes and security advisories without the anonymous limit. |
| `PALIER_GITLAB_TOKEN` | empty | Reads GitLab release notes (gitlab.com) without the anonymous limit, and those of private projects. |
| `PALIER_REGISTRY_MIRROR` | empty | Docker Hub mirror (`mirror.gcr.io`, for example), used for reads by digest. |
| `DOCKER_CONFIG` | `$HOME/.docker` | Folder of the `config.json` that holds the credentials of private registries. |

## Private registries

Palier reads the `auths` section of the Docker `config.json`, in the usual format. Mount this file in the container and point `DOCKER_CONFIG` at its folder:

```sh
-v /root/.docker/config.json:/etc/palier-docker/config.json:ro \
-e DOCKER_CONFIG=/etc/palier-docker
```

Docker Hub credentials are also used for the Docker Hub API, which gives the publication date of tags, a hundred at a time: with them, the request limit is higher than with anonymous access.

Credentials stored in a `credsStore` or `credHelpers` (ECR, GAR, ACR) are not read.

## Docker Hub mirror

The mirror reduces the requests made to Docker Hub, which limits anonymous access. Palier reads from it only what is designated by a digest, and falls back to Docker Hub if the mirror does not have it. The tag list and the digest of a tag are still read from Docker Hub: a mirror keeps only part of them, sometimes outdated. Docker Hub credentials are never sent to the mirror.

## Labels on your services

Two labels, set in the Compose file of your stacks, change how Palier behaves.

### `palier.hook.backup`

A backup command, run in the container of the service with `sh -c` before any change to the stack. If it fails, the run is abandoned and nothing is changed.

```yaml
services:
  postgres:
    image: postgres:16.4
    labels:
      palier.hook.backup: "pg_dumpall -U outline > /backup/before-palier.sql"
```

### `palier.depends_on`

Dependencies in addition to those of `depends_on`, separated by commas. Useful when an application depends on a service without the Compose file saying so.

```yaml
services:
  app:
    labels:
      palier.depends_on: "postgres,redis"
```

A dependency follows its application: its target version depends on what the application declares it supports (see [How it works](how-it-works.md#the-stack-follows-its-application)).
