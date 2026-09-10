---
name: 1c-task
description: Start, inspect, or resume a BSL Flow managed development task whose stages and acceptance are controlled by the installed task CLI.
---

# Managed BSL Flow task

Use for a request to let BSL Flow carry a registered task through its required stages. The six existing skills remain available in assisted mode. Do not call a managed task complete from a worker message, an OpenSpec `ready` state, or a test exit code.

Read [the task contract](references/task-contract.md) to prepare a request or relay a user update. Reuse actual user authorization; do not ask again for reversible work it already covers. Record questions and scope changes through `Update`. Worker output cannot authorize itself.

1. At the confirmed clean Git project root, derive a concise request and observable acceptance criteria from the user's task. Honor project `AGENTS.md` and model routing. Preserve the original request text and its provenance. An ambiguity in business identity, grouping, replacement, or partial success requires a focused question, not an invented rule.
2. Generate a UUID before `Start`; retain it for retries. Write the request outside the worker checkout. Use the installed [entrypoint](scripts/Invoke-BSLFlowTask.ps1) with `-Action Start -ProjectPath <root> -InputFile <request.json>`.
3. Call `-Action Run -ProjectPath <root> -TaskId <uuid>` once. The controller owns stage sequencing, isolated worktree, reviews, verification and acceptance. A strict `native_1c` integration criterion may use the managed Windows FILE adapter for its exact authorized target; every other runtime gate requiring an unconfirmed adapter or target remains `BLOCKED`.
4. Read the JSON envelope. Show the specific pending question/blocker, or hand off the accepted worktree and receipt. Acceptance does not merge, publish, deploy, or authorize new database changes. An explicitly authorized publication uses the separate Publish/PublishResume contract and exact acceptance identity.
5. After interruption use `Status` and `Resume` with the exact task ID. Do not start a duplicate worker or repeat an uncertain business write. After `Cancel`, explicit user `Update` is required to continue.

The worker adapter supports the verified Windows native Codex CLI version and explicit source permissions described in the contract. It blocks source-controlled execution configuration. Do not silently relax isolation or substitute an unavailable reviewer. The existing OpenCode spec reviewer keeps its configured model; the task's Codex reviewer applies to code and requirement-coverage review.

The first managed 1C runtime adapter is intentionally narrow: one authorized FILE target, one extension snapshot and exact YAxUnit tests declared by `native_1c`. Supply credentials only through `--runtime-auth stdin`; they stay in controller memory. New native requests declare trusted requirements and coverage mapping. The earlier BSLFlowPilot 5/5 acceptance proves the native adapter; the new coverage gate has separate source-only model and offline native integration evidence. These results do not prove arbitrary 1C, server, UI, EPF/ERF or production behavior.  Keep the temporary Unica durable-job restriction; do not substitute Unica runtime jobs for the native adapter.
