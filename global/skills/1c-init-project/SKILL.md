---
name: 1c-init-project
description: Initialize or upgrade a confirmed 1C project for BSL Flow and set up first-use test tooling. Use when a 1C project lacks bsl-flow.yaml, the .bsl-flow sentinel or openspec/config.yaml with schema bsl-flow, when the user names a directory as a new 1C project, or on its first test-setup request.
---

# 1c-init-project

## When to use

- The directory holds 1C sources (`Configuration.xml`, `*.mdo`, `*.bsl`, `src/cf|cfe|epf`) or the user explicitly names it as the 1C project root, and a bootstrap component is missing.
- An initialized project has an older framework version in `.bsl-flow/project.yaml`.
- The first test-setup request, or delegated work that needs the audit journal.

## Inputs

- At the candidate root: `.git`, `AGENTS.md`, `bsl-flow.yaml`, `.bsl-flow/project.yaml`, `openspec/config.yaml`.
- The user's statement of the project root and of any authorized test database.

## Steps

1. Confirm the root: the directory the user named or the one holding the 1C sources. Result: one absolute project path.
2. Run `scripts/Initialize-BSLFlowProject.ps1 -ProjectPath <root>`; add `-Explicit1CProject` when the user named an empty or undetectable directory. Result: Git, `openspec/config.yaml` (schema `bsl-flow`), `AGENTS.md`, `bsl-flow.yaml` and sentinel exist; existing files are preserved.
3. For an existing project the script applies `scripts/Update-BSLFlowProject.ps1 -Apply`. Run it without `-Apply` first when the user wants to review the plan. Result: a deterministic plan that adds only missing template keys and keeps user values and comments.
4. On the first test setup, follow [test-setup.md](references/test-setup.md): inventory local YAxUnit/Vanessa files with `scripts/Get-1CTestTooling.ps1`, read the real installed extension state, install only missing, needed and authorized components. Result: each component recorded as `missing`, `unknown`, `installed_not_verified`, `ready` or `blocked`.
5. For a workstation whose registered FILE development base is also its test base, run `scripts/Enable-BSLFlowWorkstationProfile.ps1` once, then bootstrap with `-DevelopmentDatabasePath <exact path> -ConfigureTests [-ConfigureUi]`. Result: ignored local configuration that stays `BLOCKED` until inventory and pilots exist.
6. Save an authorized interactive pilot with `scripts/Save-1CInteractiveTestPilot.ps1`. Result: `test-setup/current.json` shows the pilot, with unattended readiness still separate.
7. When native subagents are used, keep the journal per [agent-audit.md](references/agent-audit.md). For an optional project ADR index, read [architecture-context.md](references/architecture-context.md).

## Outputs

Tell the user which components were created and which were preserved. Bootstrap proves project structure and OpenSpec readiness; 1C connectivity, runtime tests and managed-host readiness each need their own evidence. An engine counts as ready after a passing pilot, not after files are generated.

## Checks

- `Initialize-BSLFlowProject.ps1` exits 0 after its schema validation. It blocks on unsupported YAML or a managed mapping conflict and then leaves the sentinel version unchanged.
- `Get-1CTestTooling.ps1` reports `file_found_unverified` for a matching file; installed state comes only from the database's real extension list.

## Stop and ask when

- The root is unclear, a drive root, the user profile, a container of several projects, or an empty directory the user has not named as the 1C root.
- A parent Git repository encloses the root, OpenSpec uses another schema, or an existing file needs an incompatible edit.
- Installed extension state is unknown (unknown is not absent), an installed extension has another version, or setup would weaken safe-mode protection.
- The test database target is not explicitly authorized.

## Managed mode

Inside a 1c-task stage, follow [references/stage-contract.md of 1c-task](../1c-task/references/stage-contract.md) instead.
