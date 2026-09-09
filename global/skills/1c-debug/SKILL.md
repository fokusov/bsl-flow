---
name: 1c-debug
description: Diagnose a 1C runtime error, wrong behavior, failing test, or regression through reproducible evidence before changing code.
---

# 1c-debug

If this investigation belongs to a registered managed task, preserve its task ID and current stage. Return findings or the pending question to the controller; use a trusted `Update` for a changed requirement. Do not restart the task or replay an uncertain write. Independent investigations retain the assisted workflow below.

## Workflow

1. Define the smallest reliable reproduction and capture expected/actual behavior, platform and configuration version, client type, input/data state, and relevant error or log output.
2. Localize the failing boundary: client form, server call, query, transaction/lock, register movement, scheduled job, integration, permissions/RLS, or platform runtime.
3. Keep a short ranked hypothesis list, each with a discriminating check. Do not modify code because a hypothesis merely sounds plausible.
4. Read [testing-policy.md](../1c-verify/references/testing-policy.md). Test hypotheses using logs, targeted queries, debugger/runtime evidence, YaXUnit reproduction, or a saved Vanessa scenario for client behavior. Computer-use is a justified narrow diagnostic/visual step, not the default regression runner.
5. State the root cause as `condition → code/runtime behavior → observed failure`. Label it unproven when evidence is incomplete.
6. When a fix is authorized, implement the smallest root-cause fix without unrelated cleanup. A diagnosis-only request ends with cause/evidence and a proposed correction, not an unsolicited code change.
7. Run the reproduction again and add the most stable available regression check.

Stop and report a blocker rather than guessing when reproduction is impossible, the required environment is unavailable, a platform/vendor defect cannot be isolated further, or missing credentials, permissions, or a test database prevent evidence collection.
