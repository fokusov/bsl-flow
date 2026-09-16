---
name: 1c-implement
description: Implement a specified 1C change with the smallest safe diff while preserving project conventions and runtime boundaries.
---

# 1c-implement

For a registered managed task, implement only after the controller dispatches this stage, within its worker worktree and current authorization. Return changed paths and a factual result; the controller owns review, test dispatch and acceptance. Do not update task state or run a second workflow loop. Outside managed mode, use the assisted procedure below.

## Inputs

Read project `AGENTS.md`, `bsl-flow.yaml` when present, active `spec.md` for M/L, `design.md` only when it exists, review reconciliation/final validation when required, and the relevant source and tests.

Do not start M/L or high-risk implementation when required `review.json`, `review-reconciliation.json`, or a passing `final-validation.json` is missing. S remains exempt unless project routing or the user required review.

## Rules

- Existing project patterns and extension points beat invented abstractions.
- Keep the diff focused; do not refactor unrelated code or create a general layer with one caller.
- Preserve backward compatibility unless the specification explicitly changes it.
- Respect 1C client/server annotations, transaction boundaries, managed locks, permissions, and supported-code constraints.
- Do not silently change the specification to fit an easier implementation.

## Execution graph (v0.1)

When the change directory contains `execution.yaml` (the optional artifact triple `contract.yaml` / `execution.yaml` / `verification.yaml`):

- Before implementation, require a passing `Invoke-1CSpecContractLint.ps1 -ChangePath <change dir>` (in `global/skills/1c-spec-review/scripts`). Never implement against artifacts that fail the lint; changes without artifacts behave exactly as before.
- Dot-source the deterministic helpers [ExecutionGraph.ps1](scripts/ExecutionGraph.ps1): artifact reading/validation (via the lint), topological order (sequential, no waves; ties broken by task id), permission checks, the evidence writer, and the state projection.
- Walk tasks one at a time in topological order. Kinds `explore|research|review|document` never mutate files; mutating kinds write only inside `allowed_scope` and never inside `forbidden`.
- After each task write `evidence/T-NNN.json` (id, status, observations, touched_files, verify results, violations) with the evidence writer.
- A task with non-empty `verify[]` must not be declared `done` until every referenced V has a recorded observable result; otherwise record `blocked` with the reason. A scope or mutation violation is recorded in evidence and the task is recorded `blocked` — never silently ignored, never `done`.
- `state.json` is a generated projection of `evidence/` (`schema_version: 1`); regenerate it from evidence instead of hand-editing, and do not commit it (`evidence/` is committed).

## Planning and tests

A separate implementation-plan file is normally unnecessary. Use a short in-context checklist only for multiple dependent steps; a step is a meaningful 1C change, not an editor action.

Before selecting tests, read the installed sibling reference [testing-policy.md](../1c-verify/references/testing-policy.md). Select by each requirement in the original task/spec, not by which interactive tool is easiest to open:

- isolated calculation/transformation → unit;
- query, record, register, or document behavior → integration;
- form, command, or user flow → saved Vanessa feature;
- visual appearance or an automation gap → bounded justified visual/computer-use check;
- changed BSL → static when configured;
- critical cross-cutting change → smoke.

Prefer a regression test for a bug when a stable test seam exists.

Disabled/unavailable test providers do not waive required checks or automatically select computer-use. Report the setup gap. Do not click through logic already sufficiently covered by unit/integration tests, or run all test levels for every change. Preserve automated tests with the source.

## Completion

Inspect the final diff against the original task as well as the spec, remove accidental scope expansion, run available developer-side checks, and record only commands and outcomes actually observed. Follow `1c-verify` evidence rules for every size; a separate verification handoff/document is needed for M/L, not automatically for S.
