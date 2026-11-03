# Usage

## First start

At first start, Palier works alone for a few minutes before everything is visible:

1. **The scan.** It reads all containers, the tags of each image in the registry, the release notes and the security advisories. Allow one to two minutes for about ten stacks. The home page shows "First scan in progress".
2. **The plans.** For each stack that has a newer version, the AI computes a plan. This takes from a few seconds to two minutes per stack. In the meantime, the stack shows "computing plan".
3. **The updates.** Each plan that Palier can apply on its own starts right away, one stack after the other. The stack goes to "running", and its page shows the steps being checked off as they finish.

A large version jump can take a long time. GitLab, with 13 required stops and their background migrations, takes about two hours. The logs (`docker logs -f palier`) show at any moment what Palier is doing.

## The stack list

The home page shows each stack with its status and the age of the last scan. Palier scans all stacks at startup, then every hour (`PALIER_SCAN_INTERVAL`), and also right after each run or decision.

| Status | What it means |
|---|---|
| up to date | No newer version is available. |
| computing plan | Newer versions exist, and the AI has not returned its plan yet. |
| plan ready | A plan exists, and Palier will apply it on its own as soon as the previous stack is done. |
| awaiting decision | A plan exists, but Palier cannot apply it on its own: a step is blocking or irreversible, the stack is postponed, or `PALIER_AUTO` excludes it. The reason is given on the stack page. |
| incomplete plan | The plan did not pass the checks. The reason is shown, and the plan is not applied. |
| detection incomplete | The versions of a service could not be read from the registry. |
| running, completed, rolled back, failed, abandoned, stopped | Result of the last run. |
| observed only | Container started outside Compose. Palier shows it but does not touch it. |

## The stack page

It shows each service with its role (application or dependency), its running version, its dependencies and the available versions. A version that is too recent appears as "maturing" until it reaches the age set by `PALIER_MATURATION` (72 hours by default), unless it fixes a vulnerability in the running version. Each known vulnerability of the running version is flagged under the service, with its severity and a link to the advisory.

Below the services comes the plan, step by step. Each step shows:

- the services that change version, and those that are stopped during the step;
- the operations, in order;
- the risk level (safe, attention, blocking, unknown) and the reversibility;
- the quoted facts, with a link to the original note.

The plan also says which versions it rules out and why, for example a database major version that the application does not cite.

## Decisions

| Button | Effect |
|---|---|
| Dry run | Checks that each target image exists in the registry and describes what Palier would do, without changing anything. |
| Apply plan | Starts the run right away, without waiting for automatic apply. The button does not appear if the plan is incomplete. |
| Ignore *service version* | Removes this version from the proposed versions. The plan is recomputed without it. |
| Postpone | Prevents automatic apply for this stack until the chosen date. |

Each decision is recorded with its author (`interface`, `cli` or `auto`) and its date.

## Follow a run

The `/runs/{id}` page refreshes by itself. It shows the result at the top, the steps of the plan (verified, running, failed or upcoming), then each phase of the run. During a run, the stack page also shows these steps. On failure, it gives the cause, the details of the rollback and an excerpt of the logs of the service that failed.

A run ends in one of these states:

| Result | What it means |
|---|---|
| completed | All steps passed. |
| rolled back | A step failed, or Palier was restarted during the run, and the whole stack went back to the last verified step. Steps verified before that stay applied, and in automatic mode, a new run starts from there at the next scan. |
| abandoned | Nothing was changed: the stack had changed since the plan was computed, a check of the catalog card (GitLab background migrations) did not pass yet, a backup hook failed, or the restore point was impossible. |
| stopped | A step was applied and verified, but the check of the catalog card that must pass before the next step (GitLab background migrations) still failed after 2 hours. The stack stays on that step, and the next run changes nothing until the check passes. |
| failed | Even the rollback failed. The details say what is left in the modified state. |

## Automatic apply

Palier is built to work alone. By default (`PALIER_AUTO=all`), it applies any complete plan, major versions included, whatever the risk level judged by the AI. What protects it is the restore point at each step, the verification by Docker and the return to the last verified step. Automatic runs go one stack after the other, to avoid loading the machine.

A plan waits for your decision only in these cases:

- it is incomplete: a citation is missing, a required step is skipped, a dependency goes beyond what the application supports;
- a step is blocking: it needs an action that Palier does not know how to do;
- a step is irreversible: even restoring the volumes would not undo it;
- the stack is postponed.

The "attention" level says what changes, it does not block. `PALIER_AUTO=minor` excludes major versions, `PALIER_AUTO=patch` keeps only patch releases, and `PALIER_AUTO=off` turns automatic apply off: every plan then waits for a click.

On the stack page, a line under the plan says whether automatic apply will take it, or why it rules it out.

A plan is kept as long as its inputs do not change: Compose files, versions, release notes, model and Palier version. Identical inputs therefore always get the same plan and the same judgment, without a new AI call.

## The webhook

If `PALIER_WEBHOOK_URL` is set, Palier sends a JSON `POST` to it for each of these events:

| `event` | When |
|---|---|
| `plan_ready` | A new complete plan has just been computed. |
| `run_done` | A run is finished. |
| `run_rolled_back` | A run was rolled back. |
| `run_failed` | The rollback failed. |
| `run_abandoned` | A run was abandoned before any change. |
| `run_stopped` | A run stopped after a verified step, waiting for a check of the catalog card. |

```json
{"event": "run_rolled_back", "stack": "outline", "run": 12, "status": "rolled_back",
 "error": "…", "at": "2026-11-01T06:14:02Z"}
```

```json
{"event": "plan_ready", "stack": "whoami", "steps": 1,
 "changes": ["whoami v1.10.1 → v1.12.0"], "at": "2026-11-01T06:14:02Z"}
```

Dry runs do not send a webhook.

## The command line

The same operations exist on the command line, through the same code. They write JSON to standard output and their logs to standard error.

```sh
docker exec palier /palier scan                  # one scan of all stacks
docker exec palier /palier plan outline          # the plan of a stack
docker exec palier /palier apply outline --dry-run
docker exec palier /palier apply outline         # applies, exit code 1 if the run does not end "completed"
docker exec palier /palier runs                  # the last 50 runs
docker exec palier /palier token                 # the interface access token
```

With no argument, `palier` runs `serve`, that is, the interface and the regular scans.
