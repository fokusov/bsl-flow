---
name: 1c-init-project
description: Initialize a confirmed 1C project and inspect first-use test tooling; install missing local test components only after verified target, inventory and authorization.
---

# 1c-init-project

Bootstrap retains assisted mode by default. Installing/upgrading the managed entrypoint does not start a task or prove runtime readiness. Initialize before registering a managed task; do not bootstrap inside its worker worktree or change controller policy during an active attempt.

Use this skill before development when a confirmed 1C project lacks one or more bootstrap components. It also applies when the user explicitly asks to initialize an empty directory as a 1C/BSL Flow project.

## Safety boundary

Do not infer that an arbitrary empty directory, drive root, user profile, or project-container directory is a project root. For an empty directory, require an explicit user statement that it is the intended 1C project root.

Inspect existing `.git`, `AGENTS.md`, `bsl-flow.yaml`, `.bsl-flow/project.yaml`, and `openspec/config.yaml` before mutation. If the directory is inside a different parent Git repository, OpenSpec uses another schema, the intended root is unclear, or an incompatible edit is required, stop and ask the user. Do not overwrite the conflict.

## Run the deterministic bootstrap

The bundled script performs preflight, initialization, managed project upgrade and validation:

```powershell
& "<skill-dir>\scripts\Initialize-BSLFlowProject.ps1" -ProjectPath "<project-root>"
```

When the user explicitly identified an empty or otherwise undetectable directory as the intended 1C project root, add:

```powershell
-Explicit1CProject
```

Tell the user whenever this skill causes initialization. The script:

- validates Git, OpenSpec, and the globally installed `bsl-flow` schema before changes;
- initializes Git only at the confirmed project root;
- runs `openspec init --tools none` only when `openspec/` is absent;
- sets `openspec/config.yaml` to `schema: bsl-flow`;
- creates only missing project files from `assets/project/`;
- preserves existing project `AGENTS.md`, `bsl-flow.yaml`, and sentinel;
- appends a marked block to an existing `.gitignore` at most once;
- creates `.bsl-flow/reports` and `.bsl-flow/evidence` placeholders;
- validates the resulting schema selection.

New v0.6 projects receive reviewer routing/model/permission ceiling/thresholds and proportionate testing policy in `bsl-flow.yaml`; local runner overrides and database files are excluded from new Git projects. Existing BSL Flow project configuration and marked ignore blocks are preserved. The deterministic workspace script does not install test frameworks or provision a database.

For an existing BSL Flow project, bootstrap invokes `scripts/Update-BSLFlowProject.ps1 -Apply`. It prints a deterministic plan, adds only missing template keys, preserves user values/comments/unknown blocks, and updates sentinel version only after successful validation. Use the upgrade script without `-Apply` for a read-only plan. Unsupported YAML or a managed mapping conflict blocks without a partial version advance.

Do not reimplement these mechanics manually when the script is available.

## First-use test environment

After bootstrap, follow [test-setup.md](references/test-setup.md) to inventory local YAxUnit/Vanessa files, inspect actual database extensions and carry out only needed authorized installation through a supported public route. Also apply this section to an existing project on its first test-setup request; do not rerun workspace bootstrap unnecessarily. When a bounded interactive pilot is the authorized route, persist it with the packaged `Save-1CInteractiveTestPilot.ps1` helper so `test-setup/current.json` does not remain a stale pre-pilot snapshot. Interactive observation never substitutes for a fresh durable test result, and a Vanessa runner smoke does not prove TestClient readiness.

For a workstation where each explicitly registered FILE development base is also its test target, create the one-time local profile with `scripts/Enable-BSLFlowWorkstationProfile.ps1`. Then bootstrap may use `-DevelopmentDatabasePath <exact-path> -ConfigureTests [-ConfigureUi]`. The setup script creates only ignored local configuration/evidence and remains `BLOCKED` until real database inventory and pilots exist; it never derives readiness from downloaded files.

Do not stop at "files downloaded": distinguish local availability, installed state and an actual passing pilot. An unavailable extension-list API blocks unattended installation; it must never trigger blind reinstallation. Optional UI/MCP engines do not block tasks that do not need them. Keep project/Git roots separate from physical base directories.

For a new test scaffold, use [test-starters.md](../1c-verify/references/test-starters.md). Before runtime, follow [test-evidence.md](../1c-verify/references/test-evidence.md) to record the selected route and preserve its actual result. Generated files are not proof of an installed or passing engine.

## Delegated work audit

When this project uses native subagents, read [agent-audit.md](references/agent-audit.md) and start the compact journal at delegation. Follow the existing AGENTS/model-routing policy, not a new BSL Flow model table. Record requested/observed settings separately and parent acceptance after verification. The helper does not spawn agents or capture internal reasoning. Missing usage remains explicitly partial; do not substitute account limits or an agent's estimate.

## Completion

Report which components were created and which were preserved. A successful bootstrap proves only project structure and OpenSpec readiness; it does not prove connectivity to a 1C database or runtime tests.
