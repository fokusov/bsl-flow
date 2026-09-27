---
name: 1c-verify
description: Verify a 1C change with proportionate static, unit, integration and Vanessa evidence and give a PASS / PASS_WITH_LIMITATIONS / FAIL / BLOCKED verdict. Use after 1c-implement for every change size, and whenever a 1C result needs evidence.
---

# 1c-verify

## When to use

- After `1c-implement`, for S as well as M/L.
- When a 1C result, fix or EPF/ERF deliverable needs an evidence-backed verdict.

## Inputs

`AGENTS.md`, `bsl-flow.yaml`, the original request and confirmed clarifications (`original-task.md`), `spec.md`/`design.md` for M/L, review sidecars when required, the diff, and existing and new tests. Read [testing-policy.md](references/testing-policy.md) first.

## Steps

1. Trace each original requirement through spec, implementation and planned check. Result: a requirement list with the smallest sufficient evidence level; a requirement lost in the spec is reported as a gap.
2. Static gate: `scripts/Invoke-1CStaticDiff.ps1 -ProjectPath <project> [-BaseRef <ref>]` ([static-diff.md](references/static-diff.md)). Result: verdict JSON that counts only new diagnostics; a new `Error` is `FAIL`.
3. Code grounding: `scripts/Test-1CCodeGrounding.ps1 -ProjectPath <project>`. Result: `PASS` when every metadata object and common-module method used by the changed BSL exists.
4. Runtime evidence on an authorized target: preflight and durable attempts per [test-evidence.md](references/test-evidence.md); new YAxUnit/Vanessa scaffolds per [test-starters.md](references/test-starters.md); an EPF/ERF through the `external_artifact` gates in [external-artifacts.md](references/external-artifacts.md). Result: run identity, target, versions, selection, counts, failures and skips.
5. For L/high risk, get an independent review in a separate subagent or context: requirements, 1C runtime semantics, data/integration compatibility, untested risk. Log delegation per [agent-audit.md](../1c-init-project/references/agent-audit.md) and attribute defects to observed causes. Result: findings with evidence.
6. Change gate: `scripts/Test-1CChangeGate.ps1 -ProjectPath <project>`. Result: `PASS`, or reasons; recorded overrides become limitations.
7. Decide the verdict (below). For M/L write `<change-dir>/verification.md`: original requirement -> spec/implementation -> selected test and expected result -> actual outcome/evidence, then residual risks, any computer-use reason and actual selection/counts/skips. For S the response is enough.
8. Clear the active change: `../1c-spec/scripts/Set-1CActiveChange.ps1 -ProjectPath <project> -Clear`. Result: no `.bsl-flow/active-change.json`.

## Outputs

One verdict with its evidence:

- `PASS` — every required piece of evidence exists and agrees.
- `PASS_WITH_LIMITATIONS` — explicit non-critical residual risks that leave every required criterion intact.
- `FAIL` — demonstrated wrong behavior, a new static `Error`, grounding `FAIL`, or a `process_violation` from the change gate.
- `BLOCKED` — required evidence is unobtainable. Missing required evidence is BLOCKED, not PASS and not PASS_WITH_LIMITATIONS.

## Checks

- `Invoke-1CStaticDiff.ps1` exits 0 (`PASS`/`NOT_RUN`); add `-Required` when static checks are required, so a missing tool is `BLOCKED`.
- `Test-1CCodeGrounding.ps1` returns `passed: true`.
- `Test-1CChangeGate.ps1` exits 0; any `process_violation` makes the verdict `FAIL`.

## Stop and ask when

- The run needs a database target or operation the user has not explicitly authorized, or another session owns the target.
- A check seems to need computer-use: state the criterion and why unit, integration or Vanessa leave it unobserved, or get an explicit user request.
- One autonomous correction round is done and a substantial failure remains: report the blocker.

## Managed mode

Inside a 1c-task stage, follow [references/stage-contract.md of 1c-task](../1c-task/references/stage-contract.md) instead.
