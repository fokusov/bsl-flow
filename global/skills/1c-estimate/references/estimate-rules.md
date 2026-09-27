# Estimate rules

Detail moved from `1c-estimate/SKILL.md`. [anchors.md](anchors.md) explains anchors, phases and AI defaults; [anchors.json](anchors.json) is the machine-readable source that `Test-1CEstimate.ps1` reads.

## Preconditions

1. The OpenSpec change (schema `bsl-flow`) contains `spec.md`.
2. Final validation passed: `final-validation.json` with `passed=true`. For an S change with low/medium risk that requires no external review, a passed lint (`spec-lint.json` with `passed=true`) is the final validation. Otherwise refuse with the exact reason; a preliminary estimate is out of scope. `Test-1CEstimate.ps1` re-checks this gate.
3. Unclosed BLOCK findings make the estimate impossible. A passed final validation already proves closure; the lint path has no review by construction.
4. Material unknowns refuse the estimate: an unknown that changes scope, architecture, metadata object composition or needs separate agreement. Check the `Неопределённости / допущения` section of the spec. Examples: unknown data-migration volume, undefined integration, missing rights requirements.
5. Spike detection: when the task is explicitly marked as research/spike in `original-task.md`/`spec.md`, or the user explicitly asks for a timebox, produce `kind=timebox` (no blocks, totals, exclusions or forks; a single `timebox_hours`) instead of a fork.

## Decomposition

- Split by requirements and acceptance criteria: metadata objects, modules/code, forms, rights, integrations/BSP, data migrations, tests, instructions.
- Every block gets its own human and AI forks and a justification. Human hours are full-cycle hours: the phase shares from anchors.md are already inside the block fork.
- A block that cannot be estimated from the spec goes to `exclusions` with a reason.
- A single number without decomposition is rejected.

## Anchors and divergence

Start from the anchor for the classification and adjust with decomposition evidence. The `external_artifact` and `posting` request flags deterministically raise the AI anchor before divergence (modifiers in anchors.json); record the flags verbatim in `fork_drivers`. When a total boundary still deviates from its anchor by more than 30%, `estimate.md` contains a `## Расхождение с якорем` section naming every divergent boundary (for example `ai_min`) with its exact percent. The validator recomputes the percents and rejects a section that states other numbers or omits a divergent boundary.

## AI basis and confidence

- `ai_basis.model`: the current session model when the host reports it; otherwise `estimate.ai_basis.model` from the user profile (`~/.bsl-flow/config.yaml`) when present; otherwise `unknown`.
- Effort `medium` and attempts 1–4 are constants of this skill (not config); record the actually assumed values and the expected attempts fork.
- Confidence is `high|medium|low`. Record assumptions and fork drivers, including the applicable request flags (`permissions`, `data_migration`, `data_deletion`, `posting`, `data_exchange`, `form_flow`, `external_artifact`, `ambiguous_business_rule`). Uncalibrated anchors are themselves a reason to stay below high confidence.

## Output files

- `estimate.json` exactly per [estimate-schema.json](estimate-schema.json): closed schema, `schema_version: 1`, `status: final`. Rounding: human and timebox 0.5 h, AI 0.25 h. Totals are the conservative sums of blocks.
- `estimate.md` with the same input hashes and creation timestamp, the blocks table, the phase multipliers used, totals, confidence, assumptions, drivers, exclusions and, when divergent, the anchor-explanation section.

## Staleness and plan/fact

On any later read, compare the recorded hashes with the live files (`null` equals only `null`; a file appearing where `null` was recorded is a change). Present only a current estimate: regenerate a stale one. `actuals` (`human_hours`, `ai_hours`, `attempts`, `source`, `recorded_at`) is filled later — agent facts from controller journals, human facts by the user. In v1 it is filled manually and leaves the plan numbers as they are.
