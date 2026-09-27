# Review routes, providers and run storage

Moved from `1c-spec-review/SKILL.md`. `Invoke-1CSpecReview.ps1` enforces the routing below; this page explains it.

## Routing

| Change | Route | `review.json` |
| --- | --- | --- |
| S, low/medium risk | Lint only. An explicitly requested S review (`-ForceReview` or project routing) uses one isolated reviewer. | none, or schema v1 |
| M, low/medium risk | One isolated single reviewer from `review.reviewer.provider`. | schema v1 + `review-reconciliation.json` sidecar |
| L or high risk | The API Council (`review.council.*`), dispatched through the budget ledger and the council final gate. | schema v2 with inline reconciliation |

Each route keeps its own engine. `review.council.enabled` makes the Council available for L/high risk; ordinary M work still goes to the single reviewer. The per-role council `fallback: current_agent` policy applies only inside a council cycle that already passed admission; a route that refused to start reports `BLOCKED`. When the configured reviewer, the Council providers or a model binding are unavailable, the required review is reported as a blocker and stays required. `Test-1CSpecFinal.ps1` accepts both schema versions.

## Single-reviewer providers

`review.reviewer.provider` in `bsl-flow.yaml`:

- `opencode` (default) — requires the OpenCode CLI (`-OpenCodePath` to override discovery).
- `claude_cli` / `codex_exec` — the host's own CLI in read-only/sandboxed mode (`-ClaudeCliPath`, `-CodexCliPath`).
- `api` — one call through the Council transport.
- `claude_subagent` — assisted import: run the packaged `bsl-flow-spec-reviewer` subagent, then pass its raw output with `-ImportRaw <path>`.

`review.reviewer.model` is required for every provider except `claude_subagent`. The script has no default model; a missing model is a configuration error.

## Model bindings and the user profile

Council model bindings may come from the optional user profile `%USERPROFILE%\.bsl-flow\config.yaml` (override the path with `BSL_FLOW_USER_CONFIG`). Layering:

1. The profile is the base layer.
2. Project `bsl-flow.yaml` overrides it per named provider, model profile and role binding.
3. `.bsl-flow/providers.local.yaml` has the highest priority for `token`/`base_url`.

The profile may define only `llm.providers.<name>`, `llm.models.<name>` and `review.council.roles.<role>.model`. Any other key or a literal token fails the run with the file and key named; an absent profile changes nothing. `review.council.independence` (`distinct_models` by default) is checked at admission before any paid call.

## Input snapshot and run directory

The invocation snapshots the exact `original-task.md`, `spec.md` and optional `design.md` bytes and hashes before starting the provider and rejects publication if any live input changes during the run.

Each run keeps an ignored project-local directory `.bsl-flow/reports/spec-review/<run-id>/`. Provider events and the raw response are appended while the process runs; `status.json` records the actual phase; a failure writes `diagnostic.json` without copying response contents into common reports.

Project configuration may bound `review.runtime.timeout_seconds` (default 600) and `review.runtime.max_output_bytes` (default 1048576). Timeout termination targets only the process this invocation started. A timeout, provider error, ambiguous or malformed JSON, or schema failure publishes no `review.json` and leaves the required review unsatisfied.

Both model routes validate the response, recalculate derived metrics and the gate verdict, and write `review.json` only after all checks pass.
