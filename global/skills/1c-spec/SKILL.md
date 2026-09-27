---
name: 1c-spec
description: Write a concise, behavior-oriented OpenSpec specification for a 1C change after inspecting real metadata and code. Use before implementing an M/L or high-risk 1C change, or when the user asks for a specification.
---

# 1c-spec

## When to use

- The change is M or L, or its risk is high; the higher of size and risk decides the route.
- The user explicitly asks for a specification, including for an S change.
- For S/low risk without such a request, keep a short brief in context and continue with `1c-implement`.

## Inputs

Project `AGENTS.md` and `bsl-flow.yaml`, the relevant metadata, BSL modules, existing tests and extension points.

## Steps

1. Read the inputs and classify complexity S/M/L and risk low/medium/high. Result: class and risk with a one-line reason.
2. Trace the affected mechanism: entry -> calls -> reads/writes and side effects -> change point, citing real modules, methods and metadata; label every gap as unknown. Result: a short evidence path for the 1C context section, scoped to the change.
3. Run `openspec new change <name> --schema bsl-flow --goal "<goal>"` and `openspec instructions spec --change <name>`, and fill the resolved template. Result: `spec.md`.
4. For L or high risk also write `design.md` (`openspec instructions design --change <name>`); for M, only when a real architecture decision emerges. Result: design present exactly when needed.
5. Copy the user's request and explicit constraints verbatim into `original-task.md` beside `spec.md`, kept apart from interpretation and proposed solutions. Result: `original-task.md`.
6. Select verification with [testing-policy.md](../1c-verify/references/testing-policy.md): map each material criterion to the smallest sufficient check and expected observation, with negative/boundary cases where relevant; give each visual/manual check its concrete reason. Result: a filled verification section.
7. For an M/L or high-risk change run `scripts/Set-1CActiveChange.ps1 -ProjectPath <project> -ChangeName <name>`. Result: `.bsl-flow/active-change.json`, read by host edit gates and by `Test-1CChangeGate.ps1` in `1c-verify`.
8. Hand the change to `1c-spec-review`.

Keep the change to `spec.md`, optional `design.md`, `original-task.md` and review sidecars; implementation steps stay in context rather than a `tasks.md`. An M/L change may add the machine-readable triple `contract.yaml`, `execution.yaml`, `verification.yaml`; validate it with `Invoke-1CSpecContractLint.ps1 -ChangePath <change dir>` from `1c-spec-review`.

## Outputs

A change ready for review: explicit goal; current and required behavior distinguished; affected 1C boundaries named; objectively checkable acceptance criteria; verification levels selected; non-goals that block obvious scope growth; material unknowns stated. Report class, risk, created artifacts, why design was or was not needed, and open assumptions or blockers.

## Checks

- `Test-1CSpec.ps1 -ChangePath <change dir>` (scripts of `1c-spec-review`) writes `spec-lint.json` with `passed: true`.
- Grounding lint `Test-1CSpecGrounding.ps1 -ChangePath <change dir>` (scripts of `1c-spec-review`) writes `spec-grounding.json` with `passed: true`: every referenced metadata object, attribute and common-module method exists. Mark planned objects `(new)` or list them under `## New metadata objects`. Status `unavailable` (no local sources) is a warning to report.
- `openspec status --change <name>` shows the change.

## Stop and ask when

- A business rule (identity, grouping, replacement, partial success) is ambiguous and the code gives no answer.
- The solution seems to need a new subsystem, metadata object, generic framework or unrelated refactoring; show the concrete evidence first.
- The user limited the task to analysis: end with the analysis or specification.

## Managed mode

Inside a 1c-task stage, follow [references/stage-contract.md of 1c-task](../1c-task/references/stage-contract.md) instead.
