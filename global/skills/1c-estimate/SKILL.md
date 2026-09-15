---
name: 1c-estimate
description: Estimate a finalized 1C OpenSpec change as min/max forks in man-hours (middle developer, 3 years, full cycle) and AI agent-hours, writing estimate.md and estimate.json sidecars beside the spec.
---

# 1c-estimate

Assisted skill: builds a consultative effort estimate for a finalized 1C change. The estimate is not a stage, gate or authorization of the controller, does not change task lifecycle, and calls no external services beyond the current session model.

## Preconditions

1. The OpenSpec change (schema `bsl-flow`) contains `spec.md`.
2. Final validation passed: `final-validation.json` with `passed=true`. For an S change with low/medium risk that requires no external review, a passed lint (`spec-lint.json` with `passed=true`) is the final validation. Otherwise refuse with the exact reason; preliminary estimates are forbidden.
3. Unclosed BLOCK findings make the estimate impossible (a passed final validation already proves closure; the lint path has no review by construction).
4. Material unknowns refuse the estimate: an unknown that changes scope, architecture, metadata object composition or needs separate agreement — check the `Неопределённости / допущения` section of the spec; examples: unknown data-migration volume, undefined integration, missing rights requirements.
5. Spike detection: if the task is explicitly marked as research/spike in `original-task.md`/`spec.md` or the user explicitly asks for a timebox, produce `kind=timebox` (no blocks, totals, exclusions or forks; a single `timebox_hours`), never a fork.

## Procedure

1. Read `spec.md` plus `design.md`/`original-task.md` when present; take the classification (S/M/L × low/medium/high) from the spec.
2. Decompose into work blocks by requirements and acceptance criteria (metadata objects, modules/code, forms, rights, integrations/BSP, data migrations, tests, instructions). Every block gets its own human/ai forks and a justification; human hours are full-cycle hours (the phase shares from [references/anchors.md](references/anchors.md) are already inside the block fork). A block that cannot be estimated from the spec goes to `exclusions` with a reason. A single-number estimate without decomposition is forbidden.
3. Start from the anchor for the classification ([references/anchors.json](references/anchors.json)), adjust with decomposition evidence. The `external_artifact` and `posting` request flags deterministically raise the AI anchor before divergence (modifiers in [references/anchors.json](references/anchors.json), explained in [references/anchors.md](references/anchors.md)); record the flags verbatim in `fork_drivers`. If any total boundary still deviates from its anchor by more than 30%, `estimate.md` must contain a `## Расхождение с якорем` section that names every divergent boundary (for example `ai_min`) with its exact percent — the validator recomputes the percents and rejects sections that state other numbers or omit a divergent boundary.
4. `ai_basis`: defaults are `gpt-6-astra` / effort `medium` / attempts 1–4 (constants of this skill, not config); record the actually assumed values and the expected attempts fork.
5. Record confidence `high|medium|low`, assumptions, and fork drivers — include the applicable request flags (`permissions`, `data_migration`, `data_deletion`, `posting`, `data_exchange`, `form_flow`, `external_artifact`, `ambiguous_business_rule`). Uncalibrated anchors are themselves a reason not to claim high confidence.
6. Write `estimate.json` exactly per [references/estimate-schema.json](references/estimate-schema.json) (closed schema, `schema_version: 1`, `status: final`; rounding: human and timebox 0.5 h, ai 0.25 h; totals are the conservative sums of blocks). Write `estimate.md` with the same input hashes and creation timestamp, the blocks table, the phase multipliers used, totals, confidence, assumptions, drivers, exclusions and (when divergent) the anchor-explanation section.
7. Validate deterministically: `scripts/Test-1CEstimate.ps1 -ChangePath <change-dir>`. It must pass; fix and re-run otherwise. It re-checks the gate, schema, sums, rounding, hashes, anchor divergence (with the request-flag AI modifiers applied) and cross-checks the percents stated in the explanation section against its own computation.
8. Answer in chat with: both total forks (or the timebox), confidence, the top fork drivers, and the path to `estimate.md`.

## Staleness and plan/fact

On any later read, compare the recorded hashes with the live files (`null` equals only `null`; a file appearing where `null` was recorded is a change). A stale estimate is never presented as current — regenerate it. `actuals` (`human_hours`, `ai_hours`, `attempts`, `source`, `recorded_at`) is filled later: agent facts from controller journals, human facts by the user; never auto-collected and never re-normalizing the plan in v1.

## Output

State the kind, both totals, confidence, main drivers, written artifacts and the validation verdict.
