---
name: 1c-task
description: Start, inspect or resume an explicitly requested BSL Flow managed task, with controller-owned stages, evidence and acceptance. Use when the user requests managed execution or continuation of a registered task.
---

# 1c-task

## When to use

- The user explicitly requests a managed task.
- Inspect or resume an existing registered task by its exact ID.
- Otherwise use the assisted skills for the requested scope.

## Inputs

Read [task-contract.md](references/task-contract.md), project `AGENTS.md`, model routing, the original request and current authorization. Repository planning commands and the installed [wrapper](scripts/bsl-flow.ps1) are described in [registry.md](references/registry.md).

## Steps

1. At the confirmed clean Git root, derive the request and observable acceptance criteria; preserve the original text and provenance. Keep an analysis-only request within analysis.
2. Generate a UUID before `Start`, retain it for retries and write the request outside the worker checkout. Call [the controller](scripts/Invoke-BSLFlowTask.ps1) with `-Action Start -ProjectPath <root> -InputFile <request.json>`.
3. Call `-Action Run -ProjectPath <root> -TaskId <uuid>` once. The controller owns stages, isolation, review, verification and acceptance; use its current gates. Adapter capability and target authorization are separate checks.
4. Read the JSON envelope: relay the specific question/blocker or hand off the accepted worktree and receipt. Acceptance covers the bound task and source; publication requires explicit authorization and the separate Publish/PublishResume contract.
5. After interruption inspect `Status` and use `Resume` with the same ID. Inspect actual state before repeating an uncertain business write. After `Cancel`, an explicit user `Update` is needed to continue. Record scope changes through trusted `Update`.

For a current-session worker, use `Next -Format Prompt`, then `Submit -Stage <stage> -DispatchId <id> -ResultFile <file outside the worktree>`. Each dispatch is single-use and records `isolation: current_agent`. Required independent review and acceptance stay controller-owned.

## Outputs

The task ID, controller status, current question/blocker or accepted worktree and bound receipt. Worker messages, OpenSpec readiness and process exit codes are evidence inputs; the controller decides acceptance.

## Checks

- M specification review uses the configured single reviewer; L/high risk uses Council. Preserve the selected route when its provider is unavailable.
- Confirm adapter isolation and supported capabilities before execution. Runtime requires the exact authorized route and target; historical `native_1c` FILE/YAxUnit evidence covers only its narrow profile. Active runtime restrictions remain in force, including the temporary Unica durable-job restriction.
- Experience Ledger is an optional extension and defaults off. Enable it only via `features.self_learning_memory.enabled: true`; controller journals and acceptance gates remain independent.

## Stop and ask when

- Business identity, grouping, replacement or partial-success rules remain ambiguous.
- The next action exceeds existing authorization, requires publication or needs an unconfirmed runtime route.
- Isolation, reviewer or required evidence is unavailable: report BLOCKED with the concrete gap.

## Managed mode

Inside a dispatched stage follow [stage-contract.md](references/stage-contract.md) and return its payload. The controller owns further dispatch and state changes.
