# BSL Flow

**Lightweight AI-assisted engineering workflow for BSL development.**

BSL Flow is an open-source workflow for AI coding agents working with BSL and 1C:Enterprise projects. It focuses on the minimum engineering process needed to get reliable results without turning every change into a heavyweight software-development ceremony.

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

## Status

**Pre-release.** The public repository is being prepared. The first complete framework package, installation guide and examples will be published here shortly.

## Who it is for

BSL Flow is primarily aimed at developers and teams using AI-assisted development with BSL / 1C:Enterprise, especially when working with multiple projects, extensions, integrations, enterprise configurations and automated testing.

## Keywords

BSL, 1C:Enterprise, 1C development, AI coding agents, Codex, OpenCode, OpenSpec, spec-driven development, SDD, AI-assisted development, specification review, BSL testing, YAxUnit, Vanessa Automation.

## Trademark notice

BSL Flow is an independent open-source project and is not affiliated with or endorsed by 1C Company. 1C and 1C:Enterprise are trademarks of their respective owner and are referenced only to describe compatibility and the target development ecosystem.

## License

License will be selected before the first public framework release.

---

Russian documentation will be available in [`README.ru.md`](README.ru.md).
