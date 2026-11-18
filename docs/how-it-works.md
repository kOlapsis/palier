# How it works

## 1. Inventory

Palier reads all containers of the Docker engine. Those that carry the Compose labels form the stacks. For each service, it records the image, tag, digest, state, HEALTHCHECK and volumes. It reads the Compose file on disk to learn the `depends_on`.

A container started outside Compose forms a single-service stack, which Palier observes and never modifies.

## 2. Version detection

For each image, Palier reads the tags in the registry: Docker Hub, GHCR or any OCI registry. An image written `registry.hub.docker.com/…` or `index.docker.io/…` is recognized as coming from Docker Hub.

The running version comes, in this order:

1. from the tag, when it is precise: `1.12.0`, `v2.5.5`, `6.2.14-alpine`, or `pg14-v0.2.0`, whose `pg14-v` prefix is read as its own line;
2. otherwise from the digest, compared with that of the precise tags in the registry;
3. otherwise from the OCI annotation `org.opencontainers.image.version` that the registry gives for this digest;
4. otherwise from the `org.opencontainers.image.version` label of the image.

The publication date of each version is used for the maturation delay. It comes from the list of tags with their dates, where the registry has one: the Docker Hub API, read in pages of a hundred tags with the Docker Hub credentials; the Quay API, anonymous for public images; and, with a `PALIER_GITHUB_TOKEN` that has the `read:packages` scope, the GitHub Packages API for GHCR. Elsewhere, or when that list is refused, it is the creation date of the image, two or three requests per tag. Palier keeps each date once read.

Palier sends at most four requests at a time to the same registry, and when a registry answers 429 it waits the time given by `Retry-After` (two minutes at most) before trying again.

An image built on the machine is not looked up in a registry: a service with a `build` section or `pull_policy: never` in its Compose file, an image without registry digest, or an image its registry does not know (401, 403 or 404 while Palier has no credentials for that registry) shows as built locally, without error. With credentials, the same refusal stays an error, since it points at the credentials.

Newer versions are sorted. Prereleases are excluded, and variants such as `-alpine` are kept: a service on `-alpine` stays on `-alpine`. A tag that names the PostgreSQL it is built on, such as `pg14-v0.2.0` or `0.8.0-pg16`, is a PostgreSQL image: `pg16-v0.3.0` is a newer version of it, a PostgreSQL major upgrade done by export and reimport, and the requirements of an application ("PostgreSQL 14 or later") are compared with that PostgreSQL version. A tag republished with different content is not a new version. A version younger than `PALIER_MATURATION` waits, unless it fixes a vulnerability in the running version (see below).

## 3. Release notes

Palier finds the origin project through the `org.opencontainers.image.source` label of the image, or through the entry in its catalog. It reads the notes of **every** version between the running version and the target, in GitHub releases or gitlab.com, and caches them. If the forge does not answer, Palier reuses the notes it already read.

These notes are treated as untrusted data: the AI is instructed never to follow what they contain, and the interface displays them as text.

Before the plan, the AI reads each note on its own and keeps the facts it states that matter for an upgrade (breaking change, action asked of administrators, configuration change, data migration, security fix, required stop, supported dependency versions), each with its quote. A quote that is not word for word in the note is dropped. Palier stores what was read, so each note is read once, even across stacks; a new release costs only its own note. A note shared by several services, such as the server and the machine learning of Immich, is read once. The plan is then computed from these facts, which keeps the request small however far behind the stack is. Each note still has to fit, whole, in the context of the model: a note longer than that stops the AI plan of its stack (see [Context length](configuration.md#ai-provider)).

### Security advisories

For a project on GitHub, Palier also reads its published security advisories (GitHub Security Advisories) and keeps those that affect the running version, based on their affected and fixed version ranges. These advisories are given to the AI along with the notes, and the plan must reach a fixed version.

A version that fixes a vulnerability in the running version skips the maturation delay. During a wave of vulnerabilities, every hour counts, and verification and rollback remain the safety net.

When the first step of a plan would leave a service exposed, because a long climb starts with versions the advisory still touches, Palier puts a step first: the latest patch of the running line (same major and minor) that the advisory no longer touches, for example GitLab 16.1.6 to 16.1.8 before the 16.3, 16.7 and later stops. The plan then goes on from that patch. Without a patch on the running line, the plan stays as it is.

The built-in catalog complements the notes with excerpts from the official documentation, copied word for word with their address, which the AI cites like the notes. It has entries for:

| Application | What the entry provides |
|---|---|
| GitLab | Required upgrade stops, and a check that background migrations are finished before the first step and between two stops; a migration whose class is not in the running GitLab can never finish and is ignored |
| Nextcloud | One major version at a time, through the last patch release of each; supported databases |
| authentik | Each `year.month` version in order, through its last patch release |
| Mastodon | At least one version of each minor |
| Synapse | Read the notes of every skipped version; PostgreSQL 14 at least, and the end-of-support policy |
| Outline | PostgreSQL 14 and later, Redis 4 and later |
| Immich | Postgres 14 to 19 |
| Forgejo, Gitea, Keycloak | Supported PostgreSQL, MySQL and MariaDB versions |

The stops of an entry are checked by the code: a plan that skips one is incomplete.

## 4. Plan

The AI receives the Compose file, the services, their versions, the dependencies, the catalog entry, and the facts read from the notes and the security advisories. It answers with a plan in steps. For each step, it judges:

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

A fact without a valid citation is removed. A plan that does not pass is marked incomplete, with the reason, and is never applied. When a plan computed from the facts does not hold, Palier computes it once more with the full text of the notes concerned: at once when the plan is incomplete, with the notes of the steps its reasons name, or else those that state a fact; and after two runs of the same plan were rolled back, with the notes of the versions of the step that did not pass. The AI is told which step and what Docker reported (a service that stopped, keeps restarting or is unhealthy), never the logs of a container. The plan then says which notes were read in full, and why.

When the AI gives no plan (wrong key, unknown model, notes too long for the model, service unavailable), the stack shows an incomplete plan computed from versions only, the answer of the AI and what to do; Palier asks again at the next scan.

## Text written to steer the AI

Release notes are written by others, and a compromised account or an unreviewed contribution can slip instructions for an AI into them. Palier assumes it can happen:

- **Cleaned before reading.** HTML comments, Markdown comments, hidden elements (`hidden`, `display: none`) and invisible characters are removed before the AI reads the notes. `palier show` and the stack page list what was removed.
- **Unguessable delimiters.** Each note is sent between tags whose name carries a mark the notes cannot guess. A note that imitates a tag of the prompt (`</release_notes>`, `<system>`…) is flagged.
- **A second look.** While it reads each note, the AI also lists the passages addressed to an AI or trying to change how an automated tool judges or acts. Only passages found word for word in the notes are kept.
- **Flagged plans wait for a person.** A plan with a flagged passage is never applied on its own: `palier show` and the stack page show the passage, and an alert is sent.
- **Free values must be cited.** A variable, command, healthcheck or mount that the AI writes into the Compose file must appear word for word in the notes of the service or already in the Compose file. A command or healthcheck cannot run shell (`|`, `;`, `&`, `$(`…), a healthcheck cannot reach outside the container, a mount cannot leave the directory of the stack nor target a socket, and a variable cannot read or copy a secret of the stack. Otherwise the plan is incomplete.
- **Traced.** The run log gives, for each of these values, the note it was written in.

## The stack follows its application

In a stack, the application is the service that no other service depends on. The services it depends on (database, cache) follow. The [`palier.role`](configuration.md#palierrole) label sets the role of a service when `depends_on` only expresses the start order.

The target version of a dependency is not the newest that exists. It is the highest that the application declares it supports, based on a citation from its notes or its entry. With no citation at all, the dependency stays on its major version and only gets patch and minor releases. The plan then says which major versions exist and why they are not proposed.

This rule applies at each step, and it sets the order: if a version of the application requires a newer database, the database goes first.

## One release, several images

Some applications ship as several images that must run the same version: n8n and its task runners, the server and the machine learning of Immich, a web service and its worker built from the same image. Palier treats as one release the services of a stack whose images declare the same source (the `org.opencontainers.image.source` label) and run the same version. They change in the same step, to the same version: the highest that every one of them allows, so a dependency among them holds the others on its major version. A plan that separates them is incomplete and is not applied. This works whether the tags are written in the Compose file or come from the same variable of the env file.

The same holds for a service that depends, through `depends_on`, on a service of the same image, such as a backup job next to its PostgreSQL: it follows it, at the same version, in the same step, and never goes past it. It holds no database itself: when the database changes major version by export and reimport, the service that follows it only changes image.

## 5. Execution

1. The backup hooks (`palier.hook.backup`) run, then the backups of the `palier.backup` labels are made and sent off the machine (see [backups](configuration.md#backups)). If one fails, nothing is changed.
2. Palier takes a restore point: a copy of the Compose files and their `.env` file, the image and digest of each service, and a copy of the volumes of the services that change.

   - Single files, host system paths (`/proc`, `/sys`, `/dev`, `/run`, `/tmp`, `/var/lib/docker`…) and logs under `/var/log` are not copied.
   - For an application with a catalog card, the bulk data the upgrade does not rewrite is not copied either: Git repositories, uploads, artifacts, media. For GitLab, `/var/opt/gitlab` is copied without `git-data`, `gitlab-rails/uploads` or `gitlab-rails/shared/artifacts`, so the copy holds the database and the configuration.
   - Before each copy, Palier measures it and the free space on the Docker disk. It does not copy when less than 10% of the disk, or 2 GiB, would stay free, nor when an application without a catalog card has more than 20 GiB to copy. The run log says so, and a rollback does not restore that mount.
3. Each step applies its operations in order. Clients are stopped in reverse dependency order and restarted in order.
4. Verification follows each step. Once the step is verified, Palier takes a new restore point of the stack as it is, then deletes the previous one, and moves to the next step.
5. At the end of the run, the last restore point is deleted, and so are the images the run replaced: the one each service started from and those of the steps in between. After a rollback, the images pulled for the step that did not pass go too. Docker keeps any image a container still uses, so an image shared with another stack stays.

What a run leaves on the disk is therefore the images in service, and nothing of its own. When Palier starts, it also removes the throwaway containers and the volume copies that a Palier stopped in the middle of a run left behind, except the copies of a run whose rollback did not pass.

Modified Compose files keep their owner, group and mode. An image whose tag holds a variable is changed in the `.env` file: `image: postgres:${PG_TAG}`, or with text around the variable, as `image: app:${APP_VERSION}-slim`, where Palier writes the version without the `-slim`.

## 6. Verification

Palier verifies only what Docker reports:

- the container is running, or ended with exit code 0 when it has no restart policy, as a migration service does;
- it stays stable for `PALIER_STABLE_SECONDS`;
- it does not loop in restarts;
- it is `healthy` if its image has a HEALTHCHECK.

No HTTP probe and no application test is added.

## 7. Rollback

If a step fails, the whole stack goes back to the last restore point: that of the last verified step, or the initial one if the first step fails. Files, images and volumes are restored, then verified again. If GitLab fails at its sixth stop, it stays at the fifth, already verified and running, and the next run starts from there. A step is never left half applied.

If Palier is stopped in the middle of a run, it resumes it at restart: the stack goes back to the last verified step.

Palier does not insist. After a run that did not pass, it applies nothing on that stack on its own for a day, whatever the plan has become and even if Palier restarts. After three rollbacks in a row, or one rollback that did not pass, it stops until a run passes: `palier apply` or the Apply button, once the stack is in order. `palier status` and the stack page say why.

## Databases

A major version change of PostgreSQL, MySQL or MariaDB always goes through `db_dump_restore`, even if the AI forgets it:

1. the clients of the database are stopped;
2. Palier exports the database from the running container;
3. it creates a new volume and starts the new major version on it;
4. it reimports the export;
5. it compares before and after: roles, databases, tables, row counts, indexes and constraints;
6. the clients restart.

The old volume is never modified. If the reimport or the comparison fails, the stack goes back to the previous step, on the old major version and the old volume. After a successful upgrade the old volume stays on the disk: Palier never removes it, and `palier show` and the stack page list the volumes of a stack that no container mounts any more, with their size and location.

After a PostgreSQL patch release that changes the C library of the image, Palier rebuilds the indexes of the affected databases and updates their collation version, as PostgreSQL requires.

Another database engine has no export and reimport method: its major versions stay blocked. The plan is then incomplete, and it is not applied.
