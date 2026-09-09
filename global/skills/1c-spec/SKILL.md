---
name: 1c-spec
description: Prepare a concise behavior-oriented specification for a medium, large, or risky 1C change after inspecting real metadata and code.
---

# 1c-spec

For a registered managed task, follow the current `1c-task` stage contract. Return the specification to the controller; it writes, lints and binds the artifact. Do not independently advance stages or treat OpenSpec readiness as implementation authorization. Outside managed mode, use the assisted procedure below.

## Purpose

Prepare the minimum planning artifact needed for a 1C development task. This skill is not an architecture generator or project manager. Its primary job is to remove ambiguity and define observable behavior.

## Procedure

1. Read project `AGENTS.md` and `bsl-flow.yaml` when present.
2. Inspect relevant metadata, BSL modules, existing tests, and extension points before specifying a solution.
3. Classify complexity as S/M/L and risk as low/medium/high.
4. Apply the higher risk-driven workflow when complexity and risk disagree.

For the affected mechanism, record a short evidence-backed path in the existing context section: entry → calls → reads/writes and side effects → change point. Cite exact modules/methods or observed metadata where available; mark unknowns, do not invent names. Do not map the whole configuration for a local change.

### S and low risk

Do not create OpenSpec artifacts unless the user explicitly asks for a spec. Retain a compact implementation brief and proceed to implementation. If a spec is explicitly requested, lint it; external review remains optional unless project routing or the user requires it.

### M or medium risk

Create an OpenSpec change using schema `bsl-flow` and create `spec.md`. Do not create `design.md` unless a real architecture decision emerges.

### L or high risk

Create `spec.md` and `design.md`.

For every created change, preserve the user's original request and explicit constraints in `original-task.md` beside `spec.md`. Keep source wording distinct from later interpretation. Do not put reviewer instructions or proposed solutions into this file.

## OpenSpec usage

Typical commands:

```text
openspec new change <change-name> --schema bsl-flow --goal "<goal>"
openspec instructions spec --change <change-name>
openspec instructions design --change <change-name>
openspec status --change <change-name>
```

Follow the resolved OpenSpec template and instructions rather than inventing another format.

## Quality gate

A spec is ready when the goal is explicit, current and required behavior are distinguishable, affected 1C boundaries are named, acceptance criteria are objectively checkable, verification levels are selected, non-goals prevent obvious scope expansion, and material unknowns are explicit.

Read [testing-policy.md](../1c-verify/references/testing-policy.md) when selecting verification. Map each material criterion to the smallest sufficient check and expected observation; include negative/boundary cases where relevant, not a fixed quota. Prefer unit/integration for logic/data and persistent Vanessa features for client flows. Do not default to computer-use or require every test level. A planned visual/manual check needs a concrete reason; a missing provider remains a readiness gap.

Do not propose a new subsystem, metadata object, generic framework, unrelated refactoring, or future-proofing without concrete evidence. Do not generate `tasks.md` or a 2–5 minute task breakdown.

After creating a spec, use `1c-spec-review`. It always runs deterministic lint and applies project routing. M/L and high-risk specs require the independent review and final invariant validation before implementation.

## Output

State the class and risk, which OpenSpec artifacts and review sidecars were created, why design was or was not needed, and any material assumptions or blockers.
