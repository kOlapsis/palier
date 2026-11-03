# Configuration

All configuration goes through environment variables. The `/settings` page of the web interface shows the values read, read-only. An invalid value stops Palier at startup with a message that names the variable.

## Variables

| Variable | Default | Purpose |
|---|---|---|
| `PALIER_DOCKER_HOST` | `unix:///var/run/docker.sock` | Docker engine. |
| `PALIER_DATA_DIR` | `/var/lib/palier` | SQLite database, database exports, and the `palier.sock` socket the command line talks to. |
| `PALIER_LISTEN` | empty | Port or address of the web interface, such as `8080` or `127.0.0.1:8080`. Empty, the web interface is off and only the command line is available. |
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
| `PALIER_SMTP_HOST`, `PALIER_SMTP_PORT` | empty, `587` | Mail server for the emails. Port 465 uses TLS from the start, any other port STARTTLS, which is required unless the server is on the local network. |
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

## Registry webhooks

A registry that announces each new tag can make Palier scan at once instead of at the next interval. Set `PALIER_REGISTRY_WEBHOOK_SECRET` to at least 32 random characters (`openssl rand -hex 32`), turn the web interface on, and give the registry this address:

```
https://palier.example.com/hooks/registry/<secret>
```

Docker Hub, GitLab, Harbor and Quay can call it. Palier reads nothing from the request but the secret in its address: it never trusts what the registry says, it only scans, at most once a minute and never while a scan already runs, and checks the tags itself. A wrong secret, another method or another address all get the same 404. The address bypasses the OpenID Connect sign-in, since a registry cannot sign in.

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
