---
name: 1c-estimate
description: Estimate a finalized 1C OpenSpec change as min/max forks in man-hours (middle developer, 3 years, full cycle) and AI agent-hours, writing estimate.md and estimate.json beside the spec. Use when the user asks how long a specified 1C change will take or for an effort estimate.
---

# 1c-estimate

Assisted, consultative skill. The estimate is not a stage, gate or authorization of the controller, leaves the task lifecycle unchanged, and uses only the current session model.

## When to use

- The user asks for an effort estimate of a 1C change that has a finalized spec.
- The user asks for a timebox for an explicitly marked research/spike task.

## Inputs

`spec.md`, plus `design.md` and `original-task.md` when present, and `final-validation.json` (or `spec-lint.json` for an S low/medium-risk change without required review). Detailed rules: [estimate-rules.md](references/estimate-rules.md); anchors: [anchors.md](references/anchors.md), [anchors.json](references/anchors.json).

## Steps

1. Confirm the preconditions in estimate-rules.md: passed final validation (or passed lint for S), no open BLOCK finding, no material unknown. Result: go, or a refusal with the exact reason.
2. Take the S/M/L × risk classification from the spec; for a marked spike or an explicit timebox request, switch to `kind=timebox`. Result: the estimate kind.
3. Decompose by requirements and acceptance criteria into work blocks with human (full-cycle) and AI forks and a justification each; unestimable blocks go to `exclusions`. Result: the blocks table.
4. Start from the classification anchor, apply the request-flag modifiers, adjust with decomposition evidence. For a total boundary more than 30% from its anchor, write the `## Расхождение с якорем` section with each exact percent. Result: totals and divergence notes.
5. Set `ai_basis.model` (session model, else profile `estimate.ai_basis.model`, else `unknown`), confidence, assumptions and fork drivers with the applicable request flags. Result: the basis fields.
6. Write `estimate.json` per [estimate-schema.json](references/estimate-schema.json) and `estimate.md` with the same input hashes. Result: both sidecars beside `spec.md`.
7. Run `scripts/Test-1CEstimate.ps1 -ChangePath <change-dir>`; fix and rerun until it passes. Result: a passing validation.
8. Answer with both total forks (or the timebox), confidence, the top fork drivers and the path to `estimate.md`.

## Outputs

`estimate.json`, `estimate.md`, and a short answer: kind, both totals, confidence, main drivers, written artifacts and the validation verdict.

## Checks

`Test-1CEstimate.ps1` passes. It re-checks the gate, schema, sums, rounding, hashes and anchor divergence (with the request-flag AI modifiers), and cross-checks the percents stated in the divergence section.

## Stop and ask when

- A material unknown (scope, architecture, metadata composition, or anything needing separate agreement) is open in `Неопределённости / допущения`.
- A recorded estimate's hashes differ from the live files: regenerate it before presenting it.
- The user wants actuals recorded: `actuals` is filled by the user or from controller journals.

## Managed mode

Inside a 1c-task stage, follow [references/stage-contract.md of 1c-task](../1c-task/references/stage-contract.md) instead.
