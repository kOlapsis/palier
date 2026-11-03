# Palier

Palier updates the Docker Compose stacks of a machine on its own, one stack at a time and in steps. It does not update blindly. It first reads what changes, with an AI, then decides and applies without waiting for anyone.

For each stack that has a newer version, it reads the release notes of every skipped version and the security advisories that affect the running version, and has an AI propose a plan. It checks that plan, then applies it on its own, step by step, with a restore point at each step. It verifies each step with what Docker reports, and if a step fails, it puts the whole stack back at the last verified step.

## At a glance

- **Autonomous by default.** Palier applies any complete plan on its own, major versions included. It asks for a decision only for what it cannot do itself: a blocking step, an irreversible step or an incomplete plan.
- **Vulnerabilities first.** Palier reads the security advisories of the projects. A version that fixes a vulnerability in the running version skips the maturation delay.
- **One container.** Palier talks to the Docker of its machine through the socket. It has no agent, no account and no license.
- **The whole stack.** An application and its dependencies (database, cache) are updated together, in the right order. A database never goes higher than what its application declares it supports.
- **A verified plan.** The AI picks operations from a closed list, and every claim in the plan must quote a release note word for word. The code rejects any plan that fails its checks.
- **A return to the last verified step.** Compose files, images and volumes go back to the state of the last step that passed its checks, even if Palier is stopped in the middle of a run.
- **Database major versions.** PostgreSQL, MySQL and MariaDB change major version by export and reimport on a new volume. The old volume is never touched.

## Where to start

1. [Installation](installation.md): start the container and check that it runs.
2. [Usage](usage.md): read a plan, apply it, set automatic apply.
3. [Configuration](configuration.md): environment variables and labels.
4. [How it works](how-it-works.md): what Palier does at each stage.
5. [Limitations](limitations.md): what Palier does not do.
