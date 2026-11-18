---
template: home.html
title: Docker Compose updates in verified steps
description: Palier reads the release notes of every version it skips, plans the upgrade, verifies each step with Docker and goes back to the last good step if something breaks.
hide:
  - navigation
  - toc
---

# Palier

Palier updates the Docker Compose stacks of a machine on its own, one stack at a time and in steps. It reads the release notes of every skipped version and the security advisories, has an AI propose a plan, checks that plan, then applies it step by step with a restore point at each step.

## Where to start

1. [Installation](installation.md): start the container and check that it runs.
2. [Usage](usage.md): read a plan, apply it, set automatic apply.
3. [Configuration](configuration.md): environment variables and labels.
4. [How it works](how-it-works.md): what Palier does at each stage.
5. [Limitations](limitations.md): what Palier does not do.

## Seen on real applications

- [GitLab CE 16.1 to 19.5](upgrades/gitlab-16-to-19.md): 13 required stops, 3 h 43, unattended.
- [Immich v1.105 to v3.3](upgrades/immich-1-105-to-3-3.md): two Compose breaking changes and a database image swap.
- [Coming from Watchtower](watchtower-alternative.md): what changes, and when Palier is not the right replacement.
