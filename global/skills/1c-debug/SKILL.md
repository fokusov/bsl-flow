---
name: 1c-debug
description: Diagnose a 1C runtime error, wrong behavior, failing test or regression through reproducible evidence before changing code. Use when the user reports a bug, an error message, a failed test or behavior that differs from the expected result.
---

# 1c-debug

## When to use

- A 1C runtime error, wrong result, failing test or regression.
- A diagnosis-only request: the steps end at the root cause and a proposed correction.

## Inputs

The user's report, expected and actual behavior, platform and configuration version, client type, input/data state, error text and logs, and the relevant source.

## Steps

1. Build the smallest reliable reproduction. Result: expected vs actual behavior, platform and configuration version, client type, data state and the error or log output.
2. Localize the failing boundary: client form, server call, query, transaction/lock, register movement, scheduled job, integration, permissions/RLS or platform runtime. Result: one named boundary.
3. Keep a short ranked hypothesis list, each with a discriminating check. Result: hypotheses and the check for each; code changes wait for a confirmed hypothesis.
4. Test the hypotheses with logs, targeted queries, debugger/runtime evidence, a YaXUnit reproduction or a saved Vanessa scenario for client behavior, following [testing-policy.md](../1c-verify/references/testing-policy.md). Use computer-use as a justified, narrow diagnostic or visual step. Result: evidence that confirms or rejects each hypothesis.
5. State the root cause as `condition -> code/runtime behavior -> observed failure`, labelled unproven when evidence is incomplete. Result: the cause statement.
6. When a fix is authorized, make the smallest root-cause fix with `1c-implement` rules and no unrelated cleanup. A diagnosis-only request ends with the cause, evidence and proposed correction. Result: fix or proposal.
7. Rerun the reproduction and add the most stable available regression check. Result: the reproduction passes and a regression check exists, or the gap is reported.

## Outputs

Root cause (proven or unproven), evidence, the fix or proposed correction, and the reproduction and regression results. Hand a fixed change to `1c-verify`.

## Checks

- The original reproduction now gives the expected behavior.
- The regression check passes and runs through the configured test route.

## Stop and ask when

- Reproduction is impossible, the required environment is unavailable, or a platform/vendor defect resists further isolation: report the blocker instead of guessing.
- Missing credentials, permissions or a test database prevent evidence collection.
- A reproduction step would write business data to a database that is not an authorized test target.

## Managed mode

Inside a 1c-task stage, follow [references/stage-contract.md of 1c-task](../1c-task/references/stage-contract.md) instead.
