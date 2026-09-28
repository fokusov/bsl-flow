---
name: 1c-implement
description: Implement a specified 1C change with the smallest safe diff that follows project conventions and 1C runtime boundaries. Use for an S change directly, or for an M/L or high-risk change after 1c-spec-review passed its final validation.
---

# 1c-implement

## When to use

- An S change the user asked to implement.
- An M/L or high-risk change whose `final-validation.json` has `passed: true`.
- An authorized fix after `1c-debug` found the root cause.

## Inputs

Project `AGENTS.md`, `bsl-flow.yaml`, the active `spec.md` for M/L, `design.md` when it exists, `review.json`, `review-reconciliation.json` and `final-validation.json` when review was required, and the relevant source and tests.

## Steps

1. Read the inputs. For M/L or high risk confirm that `final-validation.json` has `passed: true`. Result: the gate is open, or you stop and ask. S skips this unless routing or the user required review.
2. When the change directory contains `execution.yaml`, run the contract lint and walk the task graph per [execution-graph.md](references/execution-graph.md). Result: `evidence/T-NNN.json` per task.
3. Make the change with existing project patterns and extension points. Respect client/server annotations, transaction boundaries, managed locks, permissions and supported-code constraints; keep backward compatibility unless the spec changes it. Result: a focused diff with no one-caller abstraction layers.
4. Add or update tests per requirement, following [testing-policy.md](../1c-verify/references/testing-policy.md). Result: tests saved with the source:
   - isolated calculation or transformation -> unit;
   - query, record, register or document behavior -> integration;
   - form, command or user flow -> saved Vanessa feature;
   - visual appearance or an automation gap -> a bounded, justified visual/computer-use check;
   - changed BSL -> static, when configured;
   - critical cross-cutting change -> smoke;
   - a bug with a stable test seam -> a regression test.
5. Review the final diff against the original task and the spec; remove accidental scope. Result: every changed line traces to a requirement.
6. Run the available developer-side checks and record the commands and outcomes you observed. Result: a factual handoff to `1c-verify`.

Plan multi-step work as a short in-context checklist of meaningful 1C changes; a separate plan file is normally unnecessary.

## Outputs

Changed source and tests, the observed check results, and for an execution graph its `evidence/` files. `1c-verify` applies to every size; M/L also gets `verification.md` there.

## Checks

- `Invoke-1CSpecContractLint.ps1` passes before an execution-graph change starts.
- In `1c-verify`, `Test-1CChangeGate.ps1` reports `process_violation` when M/L source edits predate the passing final validation, and `Invoke-1CStaticDiff.ps1` / `Test-1CCodeGrounding.ps1` check the new code.

## Stop and ask when

- The spec looks wrong or an easier solution needs a different behavior: update it through `1c-spec` and `1c-spec-review` rather than in code.
- A required test provider is disabled or unavailable: report the setup gap; the check stays required.
- The change touches unrelated code, vendor objects or a general refactoring.

## Managed mode

Inside a 1c-task stage, follow [references/stage-contract.md of 1c-task](../1c-task/references/stage-contract.md) instead.
