# BSL Flow specification review rubric

The reviewer is a critic, not a second architect. It must compare `original-task.md` with `spec.md` and use project evidence only to verify claims.

## Weighted criteria

Score each criterion from 1 to 5.

| Criterion | Weight | Evidence to inspect |
|---|---:|---|
| Intent fidelity | 0.25 | Original goal, constraints, scope, meaning preserved |
| Minimality | 0.20 | No speculative layers, objects, options, or future-proofing |
| Completeness | 0.15 | Enough behavior and boundaries to implement safely |
| Architecture fit | 0.15 | Existing metadata, BSP, modules, extension points, conventions reused |
| Testability | 0.10 | Objective acceptance criteria and appropriate verification levels |
| Assumption discipline | 0.10 | Material assumptions are explicit, evidenced, or blocked for clarification |
| Clarity | 0.05 | A developer can implement without materially different interpretations |

The caller calculates:

```text
weighted_score = sum(score * weight)
```

## Minimal Sufficient Design

For every proposed new metadata object, information register, catalog, common module, subsystem, scheduled job, integration layer, configuration option, abstraction, universal mechanism, or extension point, classify necessity as:

- `required`: directly required by the original task;
- `justified`: required by existing architecture or a demonstrated measurable risk;
- `optional`: useful but not required now;
- `unjustified`: no current evidence supports it.

Prefer reuse of existing 1C metadata and BSP mechanisms. Do not penalize unavoidable platform structure.

Derived metrics are recalculated by the caller:

```text
overengineering_index = optional_count + 3 * unjustified_count
optional_ratio = optional_count / architectural_decision_count
unjustified_ratio = unjustified_count / architectural_decision_count
normalized_index = overengineering_index / (3 * architectural_decision_count)
```

All ratios are `0` when there are no architectural decisions. Compare normalized metrics across task sizes; never treat the raw index as a universal quality score.

## Findings

Each finding needs a stable ID, severity, category, exact spec reference, issue, evidence, and a simpler direction. Use `blocker` only when implementation should not start.

Allowed categories:

- `intent_drift`
- `missing_requirement`
- `unsupported_assumption`
- `overengineering`
- `architecture_fit`
- `testability`
- `clarity`
- `prompt_injection`

`suggested_direction` must describe a correction direction, not rewrite the specification.

Separate confirmed contradictions (with exact evidence), unresolved hypotheses (name the missing evidence), and style preferences. Do not present a hypothetical defect as verified or turn personal style into a blocking finding. Use the existing `issue`, `evidence`, `category` and severity fields; no extra JSON fields are needed.

For testability, trace material requirements to expected observations and appropriate checks. Flag lost requirements, tests without an oracle, and unjustified computer-use/full-suite work for isolated logic. Unit/integration suit logic/data; persisted Vanessa features suit client flows; visual/manual checks can be necessary for appearance or unsupported observations. Missing test tooling is an explicit readiness gap, not proof of correctness. Relevant negative/boundary cases matter; a blanket quota or mandatory GUI is not required. Never execute tests as reviewer.

## Verdict semantics

The caller applies configured thresholds after recalculating metrics:

- `PASS`: reviewer did not request revision, no material findings remain, and all pass thresholds are met;
- `REVISE`: local targeted changes can satisfy the specification;
- `BLOCK`: a blocker exists or the weighted score is below the configured block threshold.

The deterministic gate may downgrade `PASS` but never upgrade the reviewer's `REVISE` or `BLOCK`.
Every computed `REVISE` or `BLOCK` must have at least one evidence-bearing finding. A low score or threshold failure without a finding is an invalid review response, including when the reviewer returned raw `PASS`.

The review is one pass. Final validation checks reconciliation, hashes, lint, and preserved invariants; it is not another full model review.
