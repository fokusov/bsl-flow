# BSL Flow

**Lightweight AI-assisted engineering workflow for BSL development.**

BSL Flow is a public workflow project for AI coding agents working with BSL and 1C:Enterprise projects. It focuses on the minimum engineering process needed to get reliable results without turning every change into a heavyweight software-development ceremony.

The core idea is simple:

```text
task
  -> minimal sufficient specification
  -> independent specification review
  -> implementation
  -> verification and tests
  -> reusable project knowledge
```

## Why BSL Flow

AI coding agents can produce useful BSL code, but they also tend to overengineer specifications, introduce unnecessary abstractions, expand task scope, and create implementation plans that are harder to follow than the original task.

BSL Flow is designed around a few constraints:

- **Minimal sufficient design** instead of speculative architecture.
- **Risk-based workflow** instead of mandatory process for every task.
- **Independent spec review** to catch scope drift, unsupported assumptions and overengineering.
- **Evidence-driven verification** instead of "the code looks correct".
- **BSL / 1C-specific engineering context**: metadata objects, managed forms, client/server boundaries, registers, document posting, integration contracts and test databases.
- **Agent-native orchestration**: Codex or another coding agent remains the orchestrator; BSL Flow does not try to replace it.

## Planned workflow

```text
S / low-risk
inspect -> implement -> verify

M
inspect -> spec -> independent review -> targeted revision -> implement -> verify

L / high-risk
inspect -> spec/design -> independent review -> targeted revision
        -> implement -> independent code review -> verify
```

The framework is intended to work with tools such as:

- OpenAI Codex and other coding agents;
- OpenSpec for lightweight specification artifacts;
- OpenCode with an independent reviewer model;
- BSL Language Server for static analysis;
- YAxUnit for unit and integration testing;
- Vanessa Automation / TestClient for UI and end-to-end scenarios.

## Current release

The repository contains **BSL Flow v0.6.1**, including six agent skills, the OpenSpec schema, independent specification review, test-environment guidance, durable verification evidence and standalone installers for Codex and OpenCode. The patch release also preserves bounded interactive YAxUnit/Vanessa pilots as immutable setup history and refreshes provider state without misclassifying UI observation as unattended evidence.

The bounded interactive YAxUnit and Vanessa engine pilots passed on the explicitly authorized FILE demo base. Unattended execution, fresh durable reports and Vanessa TestClient readiness remain separate evidence gates; see [verification boundaries](VERIFICATION.md).

## Install

Requirements and the full procedure are in [INSTALL.md](INSTALL.md). On Windows, first inspect the installer plan and then apply it:

```powershell
Set-ExecutionPolicy -Scope Process Bypass
.\scripts\Install-BSLFlow.ps1 -WhatIf
.\scripts\Install-BSLFlow.ps1
```

For standalone OpenCode:

```powershell
.\scripts\Install-BSLFlowForOpenCode.ps1
.\scripts\Install-BSLFlowForOpenCode.ps1 -Apply
.\scripts\Test-BSLFlowOpenCode.ps1
```

The OpenCode installer does not rewrite `opencode.json`, model routing, providers or credentials.

## YAxUnit and Vanessa discovery

BSL Flow does not recursively search disks or silently download test tools. The one-time workstation profile stores the exact shared catalogs used by all explicitly registered FILE development bases. If no paths are supplied, the defaults are `C:\YAxUnit` and `C:\vanessa-automation`; tools in another location must be registered explicitly:

```powershell
& "$env:USERPROFILE\.agents\skills\1c-init-project\scripts\Enable-BSLFlowWorkstationProfile.ps1" `
  -DevelopmentDatabasePath "C:\BASES\DEMO\bp1" `
  -PlatformBin "C:\Program Files\1cv8\8.3.27.2074\bin" `
  -YaxunitDirectory "D:\1c-tools\YAxUnit" `
  -VanessaDirectory "D:\1c-tools\vanessa-automation"
```

Inventory checks only the top level of those exact directories: `YAxUnit*.cfe`, `vanessa-automation*.epf`, `VAExtension*.cfe` and `client_mcp.cfe`. A missing directory or artifact becomes `not_configured`; multiple candidates or an empty file become `blocked`. A discovered file is only `files_found`: database installation, compatibility and readiness still require inspection and a focused runtime pilot. See the [test environment guide](TEST_ENVIRONMENT_GUIDE_RU.md).

On a machine without these tools, BSL Flow still installs, but tests that require the missing provider stay `BLOCKED`. The framework does not silently download third-party releases. Vanessa Automation is an external EPF runner; YAxUnit and optional `VAExtension` have separate database-installation requirements.

## Repository layout

- `global/skills/` — installable agent skills;
- `global/openspec/` — the OpenSpec schema and templates;
- `scripts/` — installers and offline regression checks;
- [OPENCODE_SETUP_RU.md](OPENCODE_SETUP_RU.md) — standalone OpenCode setup;
- [TEST_ENVIRONMENT_GUIDE_RU.md](TEST_ENVIRONMENT_GUIDE_RU.md) — persistent YAxUnit/Vanessa test environment;
- [VERIFICATION.md](VERIFICATION.md) — proven and blocked evidence boundaries.

## Who it is for

BSL Flow is primarily aimed at developers and teams using AI-assisted development with BSL / 1C:Enterprise, especially when working with multiple projects, extensions, integrations, enterprise configurations and automated testing.

## Keywords

BSL, 1C:Enterprise, 1C development, AI coding agents, Codex, OpenCode, OpenSpec, spec-driven development, SDD, AI-assisted development, specification review, BSL testing, YAxUnit, Vanessa Automation.

## Trademark notice

BSL Flow is an independent project and is not affiliated with or endorsed by 1C Company. 1C and 1C:Enterprise are trademarks of their respective owner and are referenced only to describe compatibility and the target development ecosystem.

## License

Licensed under the [MIT License](LICENSE).

---

Russian documentation will be available in [`README.ru.md`](README.ru.md).
