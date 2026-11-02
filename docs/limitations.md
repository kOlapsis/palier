# Limitations

## What Palier does not do

- No accounts or roles: a single token protects the interface.
- No agent or remote machine: one Palier per Docker machine.
- No alert other than the webhook.
- No cleanup of old images.
- No update of Palier itself.
- No translation of release notes.
- No local catalog entries: only the built-in entries are read.

## Unsupported cases

- **Databases other than PostgreSQL, MySQL and MariaDB.** Their major versions stay blocked.
- **Registry credentials in a `credsStore` or `credHelpers`.** Only the `auths` section of `config.json` is read.
- **Containers outside Compose.** They are only observed.
- **Volumes not copied.** Bulk data of known applications, mounts of applications without a catalog card above 20 GiB, and copies that would leave the Docker disk short of space are not restored by a rollback.
- **Services with a `build` section.** Their image is built on the machine and is not in any registry, so Palier does not update them, even when the base image of their Dockerfile has a newer version.
- **Release notes outside GitHub and gitlab.com.** A self-hosted GitLab instance, Codeberg or another forge is not read; only the catalog entries then complete the notes.
- **Versions without notes.** Without release notes, the plan has an "unknown" risk level: it is applied on its own, protected by verification and rollback, but nobody has read what changes.

## Prompt injection

Release notes and security advisories are texts written by others. Palier treats them as data, the AI only chooses from a closed vocabulary, and the code checks the plan. But the free values of `compose_edit` (command, healthcheck, variable, volume) are not yet checked against the notes.

## What the plan cache implies

A plan is kept as long as its inputs do not change. If the AI misjudges a stack the first time, that judgment stays until a new version, new notes or a new version of Palier arrives.
