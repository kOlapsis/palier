# Limitations

## What Palier does not do

- No accounts or roles: the web interface lets in the admins of an OpenID Connect provider, or, without one, whoever reaches its port.
- No agent or remote machine: one Palier per Docker machine.
- No channel other than email and the webhook.
- No cleanup of old images.
- No update of Palier itself.
- No translation of release notes.
- No local catalog entries: only the built-in entries are read.

## Unsupported cases

- **Databases other than PostgreSQL, MySQL and MariaDB.** Their major versions stay blocked.
- **Credential helpers that give an identity token**, and the desktop credential stores, which do not run in a container.
- **Containers outside Compose.** They are only observed.
- **Volumes not copied.** Bulk data of known applications, mounts of applications without a catalog card above 20 GiB, and copies that would leave the Docker disk short of space are not restored by a rollback.
- **Services with a `build` section.** Their image is built on the machine and is not in any registry, so Palier does not update them, even when the base image of their Dockerfile has a newer version.
- **Release notes outside GitHub and gitlab.com.** A self-hosted GitLab instance, Codeberg or another forge is not read; only the catalog entries then complete the notes.
- **Versions without notes.** Without release notes, the plan has an "unknown" risk level: it is applied on its own, protected by verification and rollback, but nobody has read what changes.

## Prompt injection

Release notes and security advisories are texts written by others. Palier treats them as data and checks what the AI makes of them, as [How it works](how-it-works.md#text-written-to-steer-the-ai) describes. What remains: a maintainer who publishes, in plain words, a malicious instruction that reads like an ordinary upgrade note and stays within what the checks accept (a cited variable, a cited mount inside the stack directory) would be followed.

## What the plan cache implies

A plan is kept as long as its inputs do not change. If the AI misjudges a stack the first time, that judgment stays until a new version, new notes or a new version of Palier arrives.
