---
name: 1c-implement
description: Implement a specified 1C change with the smallest safe diff while preserving project conventions and runtime boundaries.
---

# 1c-implement

## Inputs

Read project `AGENTS.md`, `bsl-flow.yaml` when present, active `spec.md` for M/L, `design.md` only when it exists, review reconciliation/final validation when required, and the relevant source and tests.

Do not start M/L or high-risk implementation when required `review.json`, `review-reconciliation.json`, or a passing `final-validation.json` is missing. S remains exempt unless project routing or the user required review.

## Rules

- Existing project patterns and extension points beat invented abstractions.
- Keep the diff focused; do not refactor unrelated code or create a general layer with one caller.
- Preserve backward compatibility unless the specification explicitly changes it.
- Respect 1C client/server annotations, transaction boundaries, managed locks, permissions, and supported-code constraints.
- Do not silently change the specification to fit an easier implementation.

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
