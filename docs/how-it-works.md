# How it works

## 1. Inventory

Palier reads all containers of the Docker engine. Those that carry the Compose labels form the stacks. For each service, it records the image, tag, digest, state, HEALTHCHECK and volumes. It reads the Compose file on disk to learn the `depends_on`.

A container started outside Compose forms a single-service stack, which Palier observes and never modifies.

## 2. Version detection

For each image, Palier reads the tags in the registry: Docker Hub, GHCR or any OCI registry. An image written `registry.hub.docker.com/…` or `index.docker.io/…` is recognized as coming from Docker Hub.

The running version comes, in this order:

1. from the tag, when it is precise: `1.12.0`, `v2.5.5`, `6.2.14-alpine`, or `pg14-v0.2.0`, whose `pg14-v` prefix defines a separate line;
2. otherwise from the digest, compared with that of the precise tags in the registry;
3. otherwise from the OCI annotation `org.opencontainers.image.version` that the registry gives for this digest;
4. otherwise from the `org.opencontainers.image.version` label of the image.

The publication date of each version comes from the Docker Hub API, read in pages of a hundred tags with the Docker Hub credentials, or from the creation date of the image for other registries. It is used for the maturation delay.

Newer versions are sorted. Prereleases are excluded, and variants such as `-alpine` are kept: a service on `-alpine` stays on `-alpine`. A tag republished with different content is not a new version. A version younger than `PALIER_MATURATION` waits, unless it fixes a vulnerability in the running version (see below).

## 3. Release notes

Palier finds the origin project through the `org.opencontainers.image.source` label of the image, or through the entry in its catalog. It reads the notes of **every** version between the running version and the target, in GitHub releases or gitlab.com, and caches them. If the forge does not answer, Palier reuses the notes it already read.

These notes are treated as untrusted data: the AI is instructed never to follow what they contain, and the interface displays them as text.

### Security advisories

For a project on GitHub, Palier also reads its published security advisories (GitHub Security Advisories) and keeps those that affect the running version, based on their affected and fixed version ranges. These advisories are given to the AI along with the notes, and the plan must reach a fixed version.

A version that fixes a vulnerability in the running version skips the maturation delay. During a wave of vulnerabilities, every hour counts, and verification and rollback remain the safety net.

The built-in catalog complements the notes with excerpts from the official documentation, copied word for word with their address, which the AI cites like the notes. It has entries for:

| Application | What the entry provides |
|---|---|
| GitLab | Required upgrade stops, and a check that background migrations are finished between two stops |
| Nextcloud | One major version at a time, through the last patch release of each; supported databases |
| authentik | Each `year.month` version in order, through its last patch release |
| Mastodon | At least one version of each minor |
| Synapse | Read the notes of every skipped version; PostgreSQL 14 at least, and the end-of-support policy |
| Outline | PostgreSQL 14 and later, Redis 4 and later |
| Immich | Postgres 14 to 19 |
| Forgejo, Gitea, Keycloak | Supported PostgreSQL, MySQL and MariaDB versions |

The stops of an entry are checked by the code: a plan that skips one is incomplete.

## 4. Plan

The AI receives the Compose file, the services, their versions, the dependencies, the catalog entry, the notes and the security advisories. It answers with a plan in steps. For each step, it judges:

- **the risk**: "blocking" only if the step needs an action that no operation performs, "attention" if the notes flag a change the stack may feel, "safe" otherwise, "unknown" without notes;
- **the reversibility**: "no" only if even restoring the files, images and volumes copied before the step would not undo it.

It judges knowing that Palier applies on its own, with a restore point at each step. Only "blocking", "irreversible" and an incomplete plan stop automatic apply (see [Usage](usage.md#automatic-apply)).

Each step picks its operations from a closed list:

| Operation | What it does |
|---|---|
| `set_image` | Changes the image of a service in the Compose file. |
| `compose_edit` | Edits the Compose file in a structured way: environment variables, command, healthcheck, volumes, removing a service. |
| `recreate` | Recreates the services whose file changed. |
| `stop` / `start` | Stops or starts services in dependency order. |
| `wait_stable` | Waits for services to stay stable. |
| `exec_check` | Runs a check defined by a catalog entry, never by the AI. |
| `db_dump_restore` | Changes the major version of a database by export and reimport. |

The AI never writes a command. The code then checks the whole plan before showing it:

- the services and versions exist;
- the required stops of the entry are respected, one stop per step;
- at each step, every dependency stays within the range the application declares it supports;
- every quoted fact is found word for word in the notes.

A fact without a valid citation is removed. A plan that does not pass is marked incomplete, with the reason, and is never applied.

## The stack follows its application

In a stack, the application is the service that no other service depends on. The services it depends on (database, cache) follow.

The target version of a dependency is not the newest that exists. It is the highest that the application declares it supports, based on a citation from its notes or its entry. With no citation at all, the dependency stays on its major version and only gets patch and minor releases. The plan then says which major versions exist and why they are not proposed.

This rule applies at each step, and it sets the order: if a version of the application requires a newer database, the database goes first.

## 5. Execution

1. The backup hooks (`palier.hook.backup`) run. If one fails, nothing is changed.
2. Palier takes a restore point: a copy of the Compose files and their `.env` file, the image and digest of each service, and a copy of the volumes of the services that change.

   - Single files, host system paths (`/proc`, `/sys`, `/dev`, `/run`, `/tmp`, `/var/lib/docker`…) and logs under `/var/log` are not copied.
   - For an application with a catalog card, the bulk data the upgrade does not rewrite is not copied either: Git repositories, uploads, artifacts, media. For GitLab, `/var/opt/gitlab` is copied without `git-data`, `gitlab-rails/uploads` or `gitlab-rails/shared/artifacts`, so the copy holds the database and the configuration.
   - Before each copy, Palier measures it and the free space on the Docker disk. It does not copy when less than 10% of the disk, or 2 GiB, would stay free, nor when an application without a catalog card has more than 20 GiB to copy. The run log says so, and a rollback does not restore that mount.
3. Each step applies its operations in order. Clients are stopped in reverse dependency order and restarted in order.
4. Verification follows each step. Once the step is verified, Palier takes a new restore point of the stack as it is, then deletes the previous one, and moves to the next step.

Modified Compose files keep their owner, group and mode. An image given by a variable (`image: postgres:${PG_TAG}`) is changed in the `.env` file.

## 6. Verification

Palier verifies only what Docker reports:

- the container is running;
- it stays stable for `PALIER_STABLE_SECONDS`;
- it does not loop in restarts;
- it is `healthy` if its image has a HEALTHCHECK.

No HTTP probe and no application test is added.

## 7. Rollback

If a step fails, the whole stack goes back to the last restore point: that of the last verified step, or the initial one if the first step fails. Files, images and volumes are restored, then verified again. If GitLab fails at its sixth stop, it stays at the fifth, already verified and running, and the next run starts from there. A step is never left half applied.

If Palier is stopped in the middle of a run, it resumes it at restart: the stack goes back to the last verified step. In automatic mode, a new run starts from there at the next scan.

## Databases

A major version change of PostgreSQL, MySQL or MariaDB always goes through `db_dump_restore`, even if the AI forgets it:

1. the clients of the database are stopped;
2. Palier exports the database from the running container;
3. it creates a new volume and starts the new major version on it;
4. it reimports the export;
5. it compares before and after: roles, databases, tables, row counts, indexes and constraints;
6. the clients restart.

The old volume is never modified. If the reimport or the comparison fails, the stack goes back to the previous step, on the old major version and the old volume.

After a PostgreSQL patch release that changes the C library of the image, Palier rebuilds the indexes of the affected databases and updates their collation version, as PostgreSQL requires.

Another database engine has no export and reimport method: its major versions stay blocked. The plan is then incomplete, and it is not applied.
