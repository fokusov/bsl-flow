# BSL Flow

[Русская версия](README.md)

BSL Flow helps AI agents develop 1C/BSL changes with proportionate specifications, independent review and observable verification. Core supplies assisted skills and checks; Managed adds controller-owned stages for explicitly requested tasks.

## Quickstart

Install PowerShell 7, Git, Node.js 20.19+ and OpenSpec CLI. The working version is **0.9.0-dev.1**, a development release. The repository contains **8 skills**: six primary skills, optional `1c-estimate`, and Managed `1c-task`.

From a Core archive or repository checkout:

```powershell
./scripts/Install-BSLFlowCore.ps1 -Host codex -WhatIf
./scripts/Install-BSLFlowCore.ps1 -Host codex
# For Claude Code, use -Host claude; other targets: opencode, agents.
```

Codex uses the shared `~/.agents/skills` directory (`%USERPROFILE%\.agents\skills` on Windows). Claude installation includes local skills, read-only reviewers and hooks. See [installation](INSTALL.md), [migration from 0.8](docs/MIGRATION_0.9_RU.md), and [Claude Code](docs/hosts/CLAUDE_CODE_RU.md) for the local plugin option. The installer backs up affected files. Model bindings belong in the user profile, outside the project template.

Ask the agent to initialize the confirmed 1C project with `1c-init-project`, preserving existing settings. Then describe the change and its expected observable result.

## S / M / L workflow

```text
S / low risk: inspect -> implement -> verify
M:            inspect -> spec -> single review -> reconcile -> implement -> verify
L / high:     inspect -> spec + design -> Council -> reconcile
                      -> implement -> independent code review -> verify
```

M defaults to the selected host's reviewer. L/high requires Managed Council or an explicit owner override recording accepted risks. An unavailable required provider means `BLOCKED`.

Use unit checks for logic, integration assertions for data/posting, Vanessa for user journeys, and focused visual checks for appearance. Metadata grounding and BSL LS diagnostic diffs establish static evidence; runtime acceptance remains separate.

## Examples

- [S print form](examples/s-print-form/): request, patch and verification limits.
- [M attribute and form](examples/m-attribute-and-form/): specification and review/final sidecars.
- [Cookbook](docs/COOKBOOK_RU.md): eight common tasks and their acceptance checks.

Examples use synthetic review data and do not claim real agent or 1C execution. The [benchmark](docs/BENCHMARK_RU.md) distinguishes fake harness checks from real-agent measurements.

## Managed

Managed remains Windows-only and starts through an explicit `1c-task` request. Installation and project bootstrap does not activate Managed or prove host/runtime readiness. The controller owns task state, source-bound evidence, recovery and acceptance. See the [guide](docs/FRAMEWORK_GUIDE_RU.md), [architecture](docs/ARCHITECTURE_RU.md), [CLI contract](global/skills/1c-task/references/task-contract.md), and [glossary](docs/GLOSSARY_RU.md).

Install with `Install-BSLFlowManaged.ps1` over a compatible Core version; `Install-BSLFlow.ps1` retains the full installation. `Next -Format Prompt` and `Submit` use a single-use dispatch ID and record `isolation: current_agent`. This receipt has a different isolation boundary from a separate worker.

| Capability | Codex | Claude Code | OpenCode | Other CLI/MCP agent |
| --- | --- | --- | --- | --- |
| Core | Shared skills | Plugin / local skills | Shared skills | Available CLI |
| Review-before-code checks | Post-hoc | Hooks + post-hoc | Post-hoc | Post-hoc |
| M reviewer | `codex_exec` | `claude_subagent` / `claude_cli` | `opencode` | API provider |
| Managed | Sandbox capability gate | BLOCKED pending path isolation | Adapter; host pilot required | Current-agent |

Core targets Windows/Linux/macOS with pwsh 7 and a CI matrix; a workflow definition is not evidence that CI has run. Real host pilots and 1C runtime gates remain separate from offline contract tests.

Registry, estimation, execution contracts, publication and Experience Ledger are explicit optional capabilities. Memory defaults off. Acceptance does not authorize publication, deployment or new database operations.

## 1C environment and evidence

Tools are inventoried from explicitly selected local YAxUnit/Vanessa directories. A file does not prove installed extension state; unknown is not absent. A missing provider is `not_configured`; required evidence stays `BLOCKED`. See [test setup](docs/TEST_ENVIRONMENT_GUIDE_RU.md).

The `onec-ops` port checks operation authorization and result contracts. Mock tests do not validate real platform flags or database behavior. The temporary Unica runtime restriction remains in force.

Build `full`, `core` or `managed` archives with `scripts/Build-BSLFlowPackage.ps1 -Package <name>`; `-Test` checks the extracted artifact. See [CHANGELOG](CHANGELOG.md) for release changes and limits.

BSL Flow is independent of 1C. Product names describe compatibility. [MIT license](LICENSE).
