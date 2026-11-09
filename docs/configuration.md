# Configuration

All configuration goes through environment variables. The `/settings` page of the web interface shows the values read, read-only. An invalid value stops Palier at startup with a message that names the variable.

## Variables

| Variable | Default | Purpose |
|---|---|---|
| `PALIER_DOCKER_HOST` | `unix:///var/run/docker.sock` | Docker engine. |
| `PALIER_DATA_DIR` | `/var/lib/palier` | SQLite database, database exports, and the `palier.sock` socket the command line talks to. |
| `PALIER_LISTEN` | empty | Port or address of the web interface, such as `8080` or `127.0.0.1:8080`. Empty, the web interface is off and only the command line is available. |
| `PALIER_AI_PROVIDER` | `anthropic` | `anthropic`, or `openai` for any API compatible with OpenAI's Chat Completions: OpenAI, Mistral, or a model you host (Ollama, vLLM, LM Studio). See [AI provider](#ai-provider). |
| `PALIER_AI_URL` | `https://api.openai.com/v1` | Address of the OpenAI-compatible API, such as `http://ollama:11434/v1`. Only with `PALIER_AI_PROVIDER=openai`. |
| `PALIER_AI_KEY` | empty | API key. With Anthropic, no key means plans computed from versions alone. With `openai`, a server of your own can do without. |
| `PALIER_AI_MODEL` | `claude-sonnet-5-5` | Model used for plans. Required with `openai`. |
| `PALIER_AI_TIMEOUT` | `30m` | Longest wait for one answer of the model (Go duration). Raise it for a model on CPU: without an answer in time, Palier keeps a plan computed from versions alone, marked incomplete. |
| `PALIER_SCAN_INTERVAL` | `1h` | Interval between two scans (Go duration: `30m`, `2h`…). |
| `PALIER_MATURATION` | `72h` | Minimum age of a version before it is proposed. A version that fixes a vulnerability in the running version does not wait. |
| `PALIER_STABLE_SECONDS` | `60` | Time a service must stay stable at each verification. |
| `PALIER_AUTO` | `all` | `all`, `minor`, `patch` or `off`. See [automatic apply](usage.md#automatic-apply). The [`palier.auto`](#palierauto) label overrides it for one service. |
| `PALIER_WEBHOOK_URL` | empty | Address that receives events as JSON. See [the webhook](usage.md#the-webhook). |
| `PALIER_WEBHOOK_SECRET` | empty | Signs each webhook with HMAC-SHA256 in `X-Palier-Signature`. |
| `PALIER_NTFY_URL`, `PALIER_SLACK_URL`, `PALIER_DISCORD_URL`, `PALIER_TELEGRAM_URL` | empty | Notification channels. See [notification channels](#notification-channels). |
| `PALIER_NTFY_LEVEL`, `PALIER_SLACK_LEVEL`, `PALIER_DISCORD_LEVEL`, `PALIER_TELEGRAM_LEVEL` | `info` | `alert` or `info`, as `PALIER_EMAIL_LEVEL`. |
| `PALIER_GITHUB_TOKEN` | empty | Reads GitHub release notes and security advisories without the anonymous limit. A classic token with the `read:packages` scope also lists the publication dates of GHCR tags in one request. |
| `PALIER_GITLAB_TOKEN` | empty | Reads GitLab release notes (gitlab.com) without the anonymous limit, and those of private projects. |
| `PALIER_REGISTRY_MIRROR` | empty | Docker Hub mirror (`mirror.gcr.io`, for example), used for reads by digest. |
| `DOCKER_CONFIG` | `$HOME/.docker` | Folder of the `config.json` that holds the credentials of private registries. |
| `PALIER_SMTP_HOST`, `PALIER_SMTP_PORT` | empty, `587` | Mail server for the emails. Port 465 uses TLS from the start, any other port STARTTLS. For a server on the internet, STARTTLS is required and its certificate fully checked. For a server on the local network (`localhost`, a name without a dot such as a Compose service, a private or loopback address), STARTTLS is used when offered, without checking the certificate, so that a relay with a self-signed certificate works, and plain SMTP is accepted when it is not. |
| `PALIER_SMTP_USER`, `PALIER_SMTP_PASSWORD` | empty | Account on the mail server, when it asks for one. |
| `PALIER_SMTP_FROM` | the user, when it is an email | Sender of the emails. |
| `PALIER_EMAIL_TO` | empty | Recipients, separated by commas. Required with `PALIER_SMTP_HOST`. |
| `PALIER_EMAIL_LEVEL` | `info` | `alert`: only alerts and the weekly report. `info`: also the updates that did not go through. See [notifications](usage.md#notifications). |
| `PALIER_REGISTRY_WEBHOOK_SECRET` | empty | Secret of the address registries call to announce a new tag. See [registry webhooks](#registry-webhooks). |
| `PALIER_PUBLIC_URL` | empty | Address of the web interface, such as `https://palier.example.com`, for the links of the emails. Without it, the emails give the command to run. |
| `TZ` | UTC | Time zone of the dates of the emails and of the weekly report, such as `Europe/Paris`. |
| `PALIER_OIDC_ISSUER` | empty | OpenID Connect provider that signs people in to the web interface. Empty, the web interface has no sign-in. |
| `PALIER_OIDC_CLIENT_ID`, `PALIER_OIDC_CLIENT_SECRET` | empty | Client registered at the provider. The secret can stay empty for a public client. |
| `PALIER_OIDC_ADMINS` | empty | Who may use Palier, separated by commas: groups or roles, emails, or `@domain`. Required with `PALIER_OIDC_ISSUER`. |
| `PALIER_BACKUP_TO` | empty | rclone remote and path where the [`palier.backup`](#palierbackup) labels send their backups, such as `offsite:palier`. See [backups](#backups). |
| `PALIER_BACKUP_KEEP` | `7` | Backups kept for each service; the older ones are deleted after each new one. |
| `RCLONE_*` | empty | Passed as they are to rclone, which defines the remote of `PALIER_BACKUP_TO`: `RCLONE_CONFIG_OFFSITE_TYPE=s3` and the other `RCLONE_CONFIG_OFFSITE_` variables. |

## AI provider

Palier talks to the Anthropic API by default. `PALIER_AI_PROVIDER=openai` sends the same instructions and the same strict JSON schema to any API compatible with OpenAI's Chat Completions and structured outputs (`response_format` of type `json_schema`). The guarantees do not depend on the model: the operations come from a closed list, every fact needs a quote found word for word in the notes, and the code validates the plan before anything runs. A weaker model gives plans that are more often incomplete or refused, never a plan applied without validation. Changing the provider, the address or the model computes the plans again.

A model you host, with Ollama in the same Compose project as Palier (to compare it with your current provider first, see [Try a local model](local-model.md)):

```yaml
services:
  palier:
    environment:
      PALIER_AI_PROVIDER: openai
      PALIER_AI_URL: http://ollama:11434/v1
      PALIER_AI_MODEL: qwen3:4b-instruct-2507-q4_K_M
  ollama:
    image: ollama/ollama
    environment:
      OLLAMA_CONTEXT_LENGTH: "65536"
    volumes:
      - ollama-models:/root/.ollama
volumes:
  ollama-models: {}
```

!!! warning "Context length"
    The instructions, the Compose files and the release notes of a stack take from a few thousand tokens to more than 35,000 for Outline. Give the model a context of 65,536 tokens: `OLLAMA_CONTEXT_LENGTH` for Ollama, `--max-model-len` for vLLM. When a prompt does not fit, the server refuses it, and Palier keeps a plan computed from versions alone, marked incomplete with the message of the server.

## Backups

Before each update, Palier backs up the services that carry a [`palier.backup`](#palierbackup) label and sends the backups off the machine. If one fails, the run is abandoned and nothing is changed.

Palier runs rclone 1.75.2 in a throwaway container, which supports S3 and compatible stores, SFTP, WebDAV, Backblaze B2, Azure Blob, Google Drive and many others. The remote is defined by the [standard rclone variables](https://rclone.org/docs/#config-file), passed to Palier: every `RCLONE_` variable goes to the container. An S3 bucket:

```yaml
services:
  palier:
    environment:
      PALIER_BACKUP_TO: offsite:palier
      RCLONE_CONFIG_OFFSITE_TYPE: s3
      RCLONE_CONFIG_OFFSITE_PROVIDER: Minio
      RCLONE_CONFIG_OFFSITE_ENDPOINT: https://s3.example.com
      RCLONE_CONFIG_OFFSITE_ACCESS_KEY_ID: palier
      RCLONE_CONFIG_OFFSITE_SECRET_ACCESS_KEY: ${S3_SECRET}
```

An SFTP server, whose password rclone wants in its own obscured form (`docker run --rm rclone/rclone:1.75.2 obscure 'the password'`):

```yaml
      PALIER_BACKUP_TO: nas:backups
      RCLONE_CONFIG_NAS_TYPE: sftp
      RCLONE_CONFIG_NAS_HOST: nas.example.com
      RCLONE_CONFIG_NAS_USER: palier
      RCLONE_CONFIG_NAS_PASS: ${NAS_PASS_OBSCURED}
```

Each backup goes to `<PALIER_BACKUP_TO>/<stack>/<service>/<date>/`, or under the [`palier.backup.to`](#palierbackupto) of the service, the date in UTC such as `20261109T202416Z`:

- `dump.sql`, the logical export of the database: `pg_dumpall` for PostgreSQL, `mysqldump` or `mariadb-dump` of every database but the system ones for MySQL and MariaDB. It restores with `psql -U postgres -d postgres < dump.sql` or `mariadb -uroot < dump.sql` on the same major version.
- `volumes.tar`, an archive of the volumes and host directories the service mounts read-write, each under its path in the container (`var/lib/postgresql/data/…`). Host system paths, logs and the bulk data of a catalog card are left out, as for the [restore point](how-it-works.md#5-execution).
- next to each file, its `.sha256`, in the format of `sha256sum -c`.

The data goes from the container to rclone in a stream, through the memory of Palier: neither the export nor the archive is written to the disk of the machine. To encrypt or compress them, define the remote as a rclone [`crypt`](https://rclone.org/crypt/) or [`compress`](https://rclone.org/compress/) remote over the real one. On S3, rclone sends a stream in parts of 5 MiB by default, which limits one file to about 48 GiB; raise `RCLONE_S3_CHUNK_SIZE` for more.

`palier show` and the stack page list the last backup of each service and kind, with its location, size and SHA-256, and the last failure when it came after.

The database credentials are read in the environment of its container, as for a major upgrade, and never appear in a log or an event. Each backup sends a `backup_done` or `backup_failed` event to [the webhook](usage.md#the-webhook), with the location, size and SHA-256 of the file, for a tool that fetches it and tests its restore.

## Notification channels

Besides email, Palier posts its messages to the channels whose address is set. Each address holds the secret of the service: keep it out of version control. Palier never shows it, neither in the web interface nor in the errors of a channel.

| Variable | Address |
|---|---|
| `PALIER_NTFY_URL` | The topic, such as `https://ntfy.sh/my-palier-topic` or `http://ntfy/palier` for a server of your own in the same Compose project. With an access token: `https://:tk_…@ntfy.example.com/palier`; with an account: `https://user:password@ntfy.example.com/palier`. Alerts are sent with high priority. |
| `PALIER_SLACK_URL` | An incoming webhook, `https://hooks.slack.com/services/…`. |
| `PALIER_DISCORD_URL` | A channel webhook, `https://discord.com/api/webhooks/…`. Mentions such as `@everyone` are disabled. |
| `PALIER_TELEGRAM_URL` | `https://api.telegram.org/bot<token>/sendMessage?chat_id=<chat>`. Create the bot with @BotFather, which gives the token. Send the bot a message, or add it to a group, then open `https://api.telegram.org/bot<token>/getUpdates`: the chat id is in `message.chat.id` (negative for a group). |

Messages are plain text: release notes and logs quoted in them cannot add links, mentions or formatting. An address must use https, except for a server on the local network. `palier notify test` sends a test message to every channel and prints the refusal of each one.

## Sign-in with OpenID Connect

Only admins enter the web interface. Anyone else who signs in at the provider gets a page that says they need to be an admin to access Palier. Each decision taken in the web interface is signed with the email of the person, or their name.

At the provider, create a web application (confidential client) with this redirect address, the public address of Palier followed by `/auth/callback`:

```
https://palier.example.com/auth/callback
```

Then set the four variables. Palier reads everything else from the discovery document of the issuer: the endpoints, the scopes it supports (it asks for `groups` when the provider offers it), the client authentication method. PKCE, `state` and `nonce` protect every sign-in, and a session lasts 12 hours.

An entry of `PALIER_OIDC_ADMINS` is compared with:

- the `groups` and `roles` claims of the ID token or of the userinfo endpoint, for a group or role name (a leading `/`, as Keycloak writes full paths, is ignored);
- the verified email, for an email;
- the domain of the verified email, for `@domain`.

| Provider | `PALIER_OIDC_ISSUER` | `PALIER_OIDC_ADMINS` |
|---|---|---|
| authentik | `https://auth.example.com/application/o/<application slug>/` | a group name, sent in the `profile` scope |
| Keycloak | `https://keycloak.example.com/realms/<realm>` | a group, with a "Group Membership" mapper on the client, or an email |
| Google | `https://accounts.google.com` | emails, or `@your-workspace-domain` |
| Microsoft Entra ID | `https://login.microsoftonline.com/<tenant id>/v2.0` | the object ID of a group (Token configuration, "Add groups claim"), or the value of an app role |

Behind a reverse proxy, Palier builds its redirect address from the `Host` and `X-Forwarded-Proto` headers (or `X-Forwarded-Host`). The issuer must be in `https`, except on `localhost` or a private address.

## Private registries

Palier reads the Docker `config.json`, in the usual format: its `auths` section, and the credential helpers of `credHelpers` and `credsStore`. Mount this file in the container and point `DOCKER_CONFIG` at its folder:

```sh
-v /root/.docker/config.json:/etc/palier-docker/config.json:ro \
-e DOCKER_CONFIG=/etc/palier-docker
```

Docker Hub credentials are also used for the Docker Hub API, which gives the publication date of tags, a hundred at a time: with them, the request limit is higher than with anonymous access.

### Credential helpers

A credential helper is a program, `docker-credential-<name>`, that Palier and `docker compose` call as Docker does: the image tags and the pulls of an update then use the same credentials. The image of Palier holds no helper: mount the one you need in `/usr/local/bin`, and give it its variables. It must be a standalone binary, since the image has no shell.

```yaml
    volumes:
      - /usr/local/bin/docker-credential-ecr-login:/usr/local/bin/docker-credential-ecr-login:ro
    environment:
      AWS_ACCESS_KEY_ID: ...
      AWS_SECRET_ACCESS_KEY: ...
      AWS_REGION: eu-west-3
```

with, in `config.json`:

```json
{ "credHelpers": { "123456789012.dkr.ecr.eu-west-3.amazonaws.com": "ecr-login" } }
```

| Registry | Credentials |
|---|---|
| AWS ECR | `docker-credential-ecr-login` with the `AWS_*` variables, or `docker-credential-env`, which also exchanges AWS keys for an ECR token. ECR Public needs none. |
| Google Artifact Registry, GCR | In `auths`, the user `_json_key` with the JSON key of a service account as password (`docker login -u _json_key --password-stdin europe-docker.pkg.dev < key.json`), or `docker-credential-gcr`. |
| Azure Container Registry | In `auths`, the client ID and secret of a service principal (`docker login myregistry.azurecr.io -u <client id> -p <secret>`), or `docker-credential-acr-env`. |
| Any registry | `docker-credential-env` reads the user and password from `DOCKER_<host>_USR` and `DOCKER_<host>_PSW`, for example `DOCKER_index_docker_io_USR` for Docker Hub. |

An entry of `auths` wins over a helper. A helper answer is kept ten minutes, a failure one minute, and a failure is logged without its secret. Helpers that give an identity token instead of a user and a password are not supported, and the desktop stores (`desktop`, `osxkeychain`, `wincred`, `pass`, `secretservice`) do not work inside a container.

Proven on the bench: `docker-credential-env` for Docker Hub, and for a private registry that, like ECR, Artifact Registry and ACR, only accepts tokens from a token server behind TLS, for the tag reads of Palier and the pulls of `docker compose`. ECR, Artifact Registry and ACR themselves were not tried against a real account.

### Certificate authorities

For a registry whose certificate comes from your own authority, mount the authority in the container and point `SSL_CERT_DIR` at its folder: Palier then trusts it on top of the public authorities. The Docker engine needs it too, in `/etc/docker/certs.d/<registry host:port>/ca.crt` on the host, for the pulls.

```yaml
    volumes:
      - /etc/palier/ca:/etc/palier/ca:ro
    environment:
      SSL_CERT_DIR: /etc/palier/ca
```

When a registry other than Docker Hub refuses Palier (401, 403 or 404 on the tag list, or on its token server), the service shows "versions unknown" with the answer of the registry, and a problem of level Information is sent on every channel: missing credentials are never silent. Docker Hub gives the same answer for a private image and for a name that only exists on the machine, so such a service shows "not in its registry", with a notice on the home page and in the weekly report, without a message.

## Registry webhooks

A registry that announces each new tag can make Palier scan at once instead of at the next interval. Set `PALIER_REGISTRY_WEBHOOK_SECRET` to at least 32 random characters (`openssl rand -hex 32`), turn the web interface on, and give the registry this address:

```
https://palier.example.com/hooks/registry/<secret>
```

Docker Hub, GitLab, Harbor and Quay can call it. Palier reads nothing from the request but the secret in its address: it never trusts what the registry says, it only scans, at most once a minute and never while a scan already runs, and checks the tags itself. A wrong secret, another method or another address all get the same 404. The address bypasses the OpenID Connect sign-in, since a registry cannot sign in.

## Docker Hub mirror

The mirror reduces the requests made to Docker Hub, which limits anonymous access. Palier reads from it only what is designated by a digest, and falls back to Docker Hub if the mirror does not have it. The tag list and the digest of a tag are still read from Docker Hub: a mirror keeps only part of them, sometimes outdated. Docker Hub credentials are never sent to the mirror.

## Labels on your services

Five labels, set in the Compose file of your stacks, change how Palier behaves.

### `palier.backup`

What Palier backs up before each update of the stack: `dump`, `volumes`, or `dump,volumes`. Requires `PALIER_BACKUP_TO`; see [backups](#backups).

- `dump` exports the database of a PostgreSQL, MySQL or MariaDB image while it runs.
- `volumes` stops the service and the services that depend on it, archives its mounts, and starts them again.

```yaml
services:
  postgres:
    image: postgres:16.4
    labels:
      palier.backup: dump
```

### `palier.backup.to`

Where the backups of this service go, instead of `PALIER_BACKUP_TO`: an rclone remote and a path, such as `nas:backups`, defined by its `RCLONE_CONFIG_` variables like any other. A service can have it without `PALIER_BACKUP_TO`.

```yaml
services:
  database:
    image: ghcr.io/immich-app/postgres:16-vectorchord0.3.0-pgvectors0.2.0
    labels:
      palier.backup: dump
      palier.backup.to: nas:backups
```

### `palier.hook.backup`

A backup command of your own, run in the container of the service with `sh -c` before any change to the stack, before the backups of `palier.backup`. If it fails, the run is abandoned and nothing is changed.

```yaml
services:
  postgres:
    image: postgres:16.4
    labels:
      palier.hook.backup: "pg_dumpall -U outline > /backup/before-palier.sql"
```

### `palier.auto`

The automatic apply of this service, instead of `PALIER_AUTO`: `all`, `minor`, `patch` or `off`. It works both ways: `palier.auto: patch` keeps a sensitive service to patch releases while the rest of the machine takes everything, and `palier.auto: all` lets one service update on its own while `PALIER_AUTO=off`. A plan applies on its own only if every service it changes allows its change; another value makes the plan incomplete.

```yaml
services:
  gitlab:
    image: gitlab/gitlab-ce:18.5.3-ce.0
    labels:
      palier.auto: patch
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
