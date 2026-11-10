# GitLab CE 16.1 to 19.5 in Docker Compose, unattended

A GitLab that has not been updated for two years cannot jump to the current version. This page is the report of one run where Palier took a GitLab CE container from 16.1.6 to 19.5.1 through 13 required stops, in 3 h 43, with nobody at the keyboard.

Everything here was run in a virtual machine with a real Docker engine. Nothing is simulated.

## Why this upgrade is painful by hand

GitLab documents [required upgrade stops](https://docs.gitlab.com/update/upgrade_paths/): versions you must install and start, one after the other, before going further. Skip one and the next version may refuse to start or leave the database in a bad state.

Between two stops, the [batched background migrations](https://docs.gitlab.com/update/background_migrations/) must be finished. They run after GitLab reports `healthy`, so a container that looks ready is often not ready for the next image.

By hand, this means: read the upgrade path page, edit the tag, recreate, wait, run the migration check, repeat 13 times, and keep a backup you trust in case stop number 9 goes wrong.

## The starting point

| | |
|---|---|
| Machine | Virtual machine, 6 vCPU, 14 GB of RAM, 150 GB of disk |
| GitLab | `gitlab/gitlab-ce:16.1.6-ce.0`, one service in a `compose.yaml` |
| Data | One project, one issue, the `root` account |
| Palier | One container, run started with `palier apply gitlab` |

This is a small instance. See [what this run does not prove](#what-this-run-does-not-prove).

## The plan

Palier read the release notes of every version between 16.1.6 and the target, plus the upgrade path page quoted in its GitLab catalog entry. The AI proposed a plan, and the code checked it: a plan that skips an available stop is rejected, and so is a plan that does not wait for the background migrations after a GitLab step.

The target was 19.5.1. A newer 19.5.3 existed but was younger than the 72 h maturation delay, so Palier left it for later.

Each stop ran the same operations: change the image, recreate the container, wait for `healthy`, wait 600 s for stability, then check every 30 s that no batched background migration is still pending.

| Stop | Version | Migration checks before passing |
|---|---|---|
| 1 | 16.3.9 | 1 |
| 2 | 16.7.10 | 1 |
| 3 | 16.11.10 | 1 |
| 4 | 17.3.7 | 1 |
| 5 | 17.5.5 | 1 |
| 6 | 17.8.7 | 5 |
| 7 | 17.11.7 | 8 |
| 8 | 18.2.8 | 1 |
| 9 | 18.5.7 | 10 |
| 10 | 18.8.11 | 29 |
| 11 | 18.11.14 | 10 |
| 12 | 19.2.9 | 1 |
| 13 | 19.5.1 | 4 |

GitLab became `healthy` in 1 to 7 minutes at each stop. The long wait is at 18.8.11: 29 checks, about 14 minutes, for the `BackfillNamespaceState` migration to finish. A script that only waits for `healthy` would have moved on too early there.

## The result

- Run finished in 3 h 43, all 13 stops verified.
- `gitlab-ce:19.5.1-ce.0` is `healthy`, `/-/readiness` answers 200, and the version stored in the database is 19.5.1.
- The project, its issue and the `root` account are there.
- `compose.yaml` still belongs to `root:root` with mode `644`. Palier edited the tag in place and left no state file beside it.
- The SSH session that launched the run was cut after 30 minutes. The run went on inside Palier.

## The same upgrade with a forced failure

Before the full run, the bench replaced the image of the second stop with one that exits right away, to see what a failure in the middle looks like.

1. Restore point: GitLab is stopped and its three mounted directories (`/etc/gitlab`, `/var/log/gitlab`, `/var/opt/gitlab`) are copied in 13 s.
2. Stop 1 passes: 16.3.9, `healthy` after 7 minutes, migrations finished.
3. Stop 2 fails: the container restarts in a loop.
4. Rollback: the three directories are restored and GitLab comes back in 16.1.6, `healthy`, with its project, its issue and the version in the database at 16.1.6.

The whole failed run took 889 s. Since that run, Palier takes a new restore point after each verified stop, so a failure at stop 9 brings the stack back to stop 8 and not to the start.

## What this run does not prove

- **The dataset is tiny.** One project and one issue. On a GitLab with years of data, the background migrations take much longer. Palier waits for them the same way, but the 3 h 43 is not a promise for your instance.
- **Disk.** Each GitLab image on the path weighs about 3 GB, and Palier does not clean old images. Thirteen stops need room.
- **Restore points copy the volumes.** That was 13 s here. On a large instance, count the time and the space of a full copy of the three directories at each stop.
- **One Compose service.** This is the omnibus image. A GitLab split across several containers was not on the bench.

## Try it on your own GitLab

Palier never has to apply anything to show you its plan.

```sh
palier show gitlab             # the stops, the operations, the quoted notes
palier apply gitlab --dry-run  # checks that every target image exists
```

To keep GitLab under your control while the rest of the machine updates on its own, add a label to the service:

```yaml
services:
  gitlab:
    labels:
      palier.auto: "off"
```

The plan then waits for `palier apply gitlab`. See [Installation](../installation.md) to start Palier and [Usage](../usage.md) to read a plan.
