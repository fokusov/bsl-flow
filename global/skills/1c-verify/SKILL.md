---
name: 1c-verify
description: Verify a 1C change with proportionate unit, integration and Vanessa evidence; reserve computer-use for justified visual or unsupported checks.
---

# 1c-verify

Verification is evidence-driven, not style-driven. Prefer a separate Codex subagent or context for L/high-risk tasks.

## Inputs

Read `AGENTS.md`, `bsl-flow.yaml`, the original user request and confirmed clarifications (`original-task.md` when present), `spec.md` for M/L, `design.md` when present, spec review/reconciliation/final validation when required, the implementation diff, and existing and new tests.

Before selecting or running tests, read [testing-policy.md](references/testing-policy.md). It defines the test-level choice, computer-use boundary, runtime safety and evidence rules; apply it also to S changes.

When the deliverable is an EPF/ERF, also read [external-artifacts.md](references/external-artifacts.md) and use its `external_artifact` gate. Static diagnostics, native build, round-trip, native load and task-specific behavior are separate evidence layers.

Before the selected runtime run, use [test-evidence.md](references/test-evidence.md) for a focused preflight, selected extension identity checks and durable attempt results. If the authorized route is an interactive engine pilot, use the sibling `1c-init-project` helper described there to preserve the exact selection/counts and update test-setup state without claiming unattended readiness. Use [test-starters.md](references/test-starters.md) only when preparing new YAxUnit/Vanessa tests. The helpers do not authorize or launch database changes, and neither generated configuration nor preview proves runtime success.

For delegated verification, use the sibling [agent-audit.md](../1c-init-project/references/agent-audit.md) to distinguish subagent completion from your accepted result. Preserve the user's routing rules and attribute defects to observed causes rather than automatically blaming the model.

## Scope and evidence

Trace the original requirements and confirmed clarifications through the spec, implementation and actual checks. If a requirement was lost in the spec, report the gap; do not declare success by checking only the spec. Confirm the diff contains no unrelated behavior and design decisions are followed. Use the smallest sufficient evidence set:

- Static: configured syntax/static analysis for changed BSL. Syntax errors and newly introduced severe diagnostics block completion; unrelated legacy smells do not expand scope.
- Unit: YaXUnit or the existing configured framework for isolated business logic.
- Integration: database state, queries, record/posting, register movements, transactions, locks, integration contracts, or background jobs.
- UI: meaningful form, command, or user-flow changes when required by policy. Prefer an existing Vanessa feature or save a minimal new scenario; MCP exploration helps author it. Use computer-use only for a stated unmet observation or explicit user request, not as automatic fallback when automation is unavailable. Source inspection alone does not prove UI behavior.
- Smoke: configured critical-flow check for high-risk or core-flow changes.
- External artifact: for EPF/ERF, require native build, round-trip and native load of the exact hashed artifact. Require behavior/migration evidence when the requirement changes behavior. `/LoadExternalDataProcessorOrReportFromFiles` alone is not syntax or behavior acceptance.

For L/high-risk changes, perform independent review when configured, focusing on requirements, 1C runtime semantics, data/integration compatibility, critical maintainability issues, and untested risk.

## Verdict

Use `PASS`, `PASS_WITH_LIMITATIONS`, `FAIL`, or `BLOCKED` according to the evidence policy. Missing required runtime evidence is not PASS_WITH_LIMITATIONS. For M/L, write `<change-dir>/verification.md` with compact rows: original requirement → spec/implementation → selected test and expected result → actual outcome/evidence; then residual risks. Include the reason for any computer-use and actual run selection/counts/skips. For S do not require a new document.

Allow at most one autonomous correction round. If a substantial failure remains, stop the rewrite/review loop and report the blocker.
