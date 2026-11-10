# Coming from Watchtower

The `containrrr/watchtower` repository was [archived on December 17, 2025](https://github.com/containrrr/watchtower). The image still runs, but it no longer gets fixes. If you are looking for what to put in its place, this page says where Palier fits, and where it does not.

## What Watchtower did, and what it never did

Watchtower watches the tag a container runs. When the image behind that tag changes in the registry, it pulls it and restarts the container with the same options.

That covers one case well: a container on `latest` or on a moving tag such as `16`, for an application whose updates never need anything else.

It leaves the rest to you:

- A service pinned to `1.105.1` never moves, because that tag never changes.
- A service on `latest` jumps to whatever is published, including a major version with a migration to do first.
- Nobody reads the release notes. A required intermediate version, a renamed variable or a removed service shows up as a broken stack.
- The Compose file is not edited. The running container and the file on disk drift apart.
- A failed update leaves the new container in a restart loop. There is no return to the previous state, and no copy of the volumes.
- Services are updated one by one. An application and its database are not treated as one stack.

## What Palier does differently

| | Watchtower | Palier |
|---|---|---|
| Unit of update | One container | One Compose stack, application and dependencies in order |
| What it follows | The digest behind the running tag | Version numbers in the registry, release notes of every skipped version, security advisories |
| Pinned tags | Never updated | Updated, by editing the Compose file in place |
| Required intermediate versions | Unknown | Followed step by step |
| Compose changes asked by the notes | Not done | Done from a closed list of edits, each backed by a quoted note |
| Database major version | Whatever the tag brings | PostgreSQL, MySQL and MariaDB by export and reimport on a new volume; a database never goes beyond what the application says it supports |
| Before a step | Nothing | Restore point: Compose files, `.env`, images, volume copies |
| After a step | Nothing | Docker verification: running, stable, no restart loop, `healthy` if there is a HEALTHCHECK |
| On failure | Left as is | Back to the last verified step |
| New releases | Applied as soon as seen | Held for 72 h by default, unless they fix a vulnerability in the running version |

Two reports show this on real applications: [GitLab CE 16.1 to 19.5 in 13 stops](upgrades/gitlab-16-to-19.md) and [Immich v1.105 to v3.3 with two Compose breaking changes](upgrades/immich-1-105-to-3-3.md).

## About the AI

Palier uses a language model to read release notes. This is the part people distrust first, so here is exactly what it is allowed to do.

- It picks operations from a closed list. It never writes a shell command.
- Every claim in its plan must quote a release note word for word. The code checks the quote against the note and rejects the plan otherwise.
- The code, not the model, checks that no required step is skipped, that every target version exists, and that no dependency goes beyond what the application supports.
- Release notes are treated as untrusted text, never as instructions. What remains possible is described in [Limitations](limitations.md#prompt-injection).
- You bring the model: an API key, or a model you host through any OpenAI-compatible API such as Ollama ([Try a local model](local-model.md)). Palier has no account and sends nothing anywhere else.

Without a model, Palier still works on version numbers, but nobody has read what changes.

## When Palier is not the right replacement

- **You only want to be told.** Use a notifier such as [Diun](https://github.com/crazy-max/diun). Palier can also wait for your decision on everything with `PALIER_AUTO=off`, but it is a bigger tool than you need.
- **You want Watchtower as it was.** A [maintained fork](https://github.com/nicholas-fedor/watchtower) keeps the same behavior.
- **Your Compose files live in Git and deploy through CI.** A bot that opens pull requests, such as Renovate, fits that workflow better. Palier edits the Compose file on the machine.
- **Your containers are started with `docker run`.** Palier only observes containers outside Compose.
- **You run several machines from one place.** Palier is one container per Docker machine, with no agent and no remote machine.
- **Your stack is built locally.** Services with a `build` section are not updated.

The full list is in [Limitations](limitations.md).

## Moving over

1. Stop and remove the Watchtower container. Two tools updating the same containers will step on each other.
2. Start Palier as described in [Installation](installation.md), with the folder of your stacks mounted at the same path as on the host.
3. Run `palier` and read the list. Nothing you care about has to be applied yet: set `PALIER_AUTO=off` for the first start if you want to read every plan before it runs.
4. Open a stack with `palier show <stack>`, read the plan and its quoted notes, then `palier apply <stack>`.
5. When you trust what you see, set `PALIER_AUTO` to `patch`, `minor` or `all`, and use the [`palier.auto`](configuration.md#palierauto) label for the services that need a different rule.

Palier works from precise version tags. For a service on `latest`, it identifies the running version from the image digest ([How it works](how-it-works.md#2-version-detection)).
