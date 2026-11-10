# Immich v1.105 to v3.3: two Compose breaking changes, handled

Immich moves fast, and its release notes regularly ask you to edit your `docker-compose.yml`: a service to remove, a variable to rename, a database image to replace. Skip a few months of updates and bumping `IMMICH_VERSION` is no longer enough.

This page is the report of one run where Palier took an Immich stack from v1.105.1 to v3.3.5 in 8 steps, editing the Compose file where the notes ask for it. It ran in a virtual machine with a real Docker engine.

## The starting point

- Immich v1.105.1, with the `docker-compose.yml` and the `.env` published by the project at that version.
- Three photos uploaded through the API. Their SHA-1 is recorded before the run, and each original is downloaded and hashed again after.

## What the notes ask for between these versions

Reading every release between v1.105.1 and v3.3.5, Palier found three things a tag bump would miss:

1. **The `immich-microservices` container is removed** in v1.106. The service has to leave the Compose file, and a `command` has to go from `immich-server`.
2. **The internal port of `immich-server` changes** in v1.118. Palier cannot change a published port, so it set the `IMMICH_PORT` variable the notes name and kept the container on its old port.
3. **The database image changes** in v1.133, from `tensorchord/pgvecto-rs` to the VectorChord image published by Immich. The notes also say that from 1.133.0, Immich only supports upgrading directly from 1.107.2 or later, which makes v1.107.2 a required stop.

## The plan

| Step | Version | What changes besides the tag |
|---|---|---|
| 1 | v1.106.4 | Remove the `immich-microservices` service, drop the `command` of `immich-server` |
| 2 | v1.107.2 | Required stop before 1.133 |
| 3 | v1.118.2 | Set `IMMICH_PORT=3001` on `immich-server` |
| 4 | v1.132.3 | |
| 5 | v1.133.1 | Database image `tensorchord/pgvecto-rs:pg14-v0.2.0` replaced by `ghcr.io/immich-app/postgres:14-vectorchord0.3.0-pgvectors0.2.0`, its `command` dropped |
| 6 | v1.136.0 | |
| 7 | v3.0.3 | |
| 8 | v3.3.5 | |

Every edit in this table is backed by a sentence quoted word for word from a release note, with a link to it. A plan that states something without a quote is rejected by the code before anything runs.

The AI does not write commands. It picks from a closed list of operations (`set_image`, `compose_edit`, `recreate`, `wait_stable`…), and the Compose edits themselves come from a fixed vocabulary: set, unset or rename a variable, set or unset a command, remove a service, add or remove a volume.

Going one minor version at a time would have meant 51 steps and about 140 GB of images. Palier stops only at the versions the notes single out and jumps between them.

## The result

- 8 steps verified, in about 50 minutes.
- `immich-server` and `immich-machine-learning` are in v3.3.5 and `healthy`. The two services share the `IMMICH_VERSION` variable and moved together at each step.
- `immich-microservices` is gone from the Compose file.
- The database runs the VectorChord image, `healthy`.
- Immich still answers on port 2283.
- The three photos downloaded after the run have the same SHA-1 as before.

In a later run on the same bench, Palier also took the Immich database from PostgreSQL 14 to 16 by export and reimport on a new volume, then compared both sides before going on: 1 role, 2 databases, 73 tables, 233,855 rows, 255 indexes, 167 constraints. The old data directory was left untouched.

## The same upgrade with a forced failure

The bench replaced the image of one step with one that exits right away.

**Failure at step 2.** Step 1 had already removed `immich-microservices` and edited the `.env`. After the rollback, `docker-compose.yml` and `.env` are identical to the originals, the four volumes are restored, the removed service is back, Immich runs v1.105.1 and the three photos have the same SHA-1. Duration: 531 s.

**Failure at step 3, with a restore point after each verified step.** Steps 1 and 2 pass. Step 3 fails. Immich comes back to v1.107.2, the state of step 2: `immich-microservices` stays removed, `IMMICH_VERSION` is `v1.107.2`, the `IMMICH_PORT` added by step 3 is gone, the photos are intact. Duration: 561 s.

## What did not work the first time

Two runs failed before the ones above, and the bench log keeps them:

- One run was abandoned by mistake: a freshness check compared versions on a database image whose tag is not a version.
- One run could not copy `/etc/localtime`, mounted read-only. The rollback worked, and read-only mounts are now left out of restore points.

## What this run does not prove

- **Three photos.** A real library is thousands of files. Palier does not copy the bulk data of applications it knows, so a rollback restores the database and the configuration, not a large photo folder (see [Limitations](../limitations.md#unsupported-cases)). The backup of the library stays your job.
- **The official Compose file.** A heavily customized file (other service names, an external database) was not on the bench.
- **Notes are the input.** Palier follows what the release notes say. A breaking change the notes do not mention is caught only by the verification after the step, then rolled back.

## Try it on your own Immich

```sh
palier show immich             # the steps, the Compose edits, the quoted notes
palier apply immich --dry-run  # checks that every target image exists
```

Nothing is applied by these two commands. See [Installation](../installation.md) to start Palier and [Usage](../usage.md) to read a plan.
