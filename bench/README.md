# BSL Flow benchmark harness

Evidence base for Ф6 of `docs/plans/2026-09-26-remediation-plan.md`: does BSL Flow (Core
skills/bootstrap) measurably beat a bare agent, and by how much? The kill-list decisions in
Ф0.2 are settled from this data, per pre-registered rules in `bench/DECISION_RULES.md`.

This harness runs fully offline by default (a scripted `fake` agent, no paid model calls).
Real-agent runs (`claude`, `codex`) are supported only as an explicit local operator action.
The manual GitHub workflow runs the offline fake agent exclusively: it has no model secrets,
does not contact a model service, and produces no paid-run evidence.

## Task format

Each task lives at `bench/tasks/<id>/task.yaml`, validated against
`bench/schemas/task.schema.json`:

```yaml
id: s-print-form
size: S            # S | M | L
risk: low          # low | medium | high
fixture:
  path: bench/fixtures/demo-mini   # or: {repo: <url>, commit: <sha>}
request: original-task.md          # prompt text, as if from the customer
expected_scope:
  - CommonModules/ПечатьСчетов/**
acceptance:
  - kind: file_contains
    path: CommonModules/ПечатьСчетов/Ext/Module.bsl
    pattern: 'QR'
  - kind: bslls_new_errors_max
    value: 0
  - kind: yaxunit
    tests: [bench/tasks/s-print-form/tests/*.bsl]
    requires_runtime: true
ambiguous: false
```

Fields:
- `size` / `risk` — task classification used only for reporting, not for scoring.
- `fixture` — either a local directory under `bench/fixtures/` (`path`, relative to the repo
  root) or a public repo pinned at a commit (`repo` + `commit`), cloned by the runner.
- `request` — relative path (from the task directory) to the prompt handed to the agent
  verbatim. The agent never sees `acceptance` or `expected_scope`.
- `expected_scope` — glob patterns the changed files must stay within; anything else is
  scope drift.
- `acceptance` — hidden checks, each with a `kind`:
  - `file_contains` / `file_absent` — regex over one file's content (or its absence);
    checkable fully offline.
  - `bslls_new_errors_max` — caps new BSL Language Server `Error`-severity diagnostics
    introduced by the change, via `global/skills/1c-verify/scripts/Invoke-1CStaticDiff.ps1`
    (a `NOT_RUN` verdict, e.g. bslls not installed, is tolerated and reported, not scored FAIL).
  - `static_diff` — runs the same static-diff gate as a plain pass/fail check (any `FAIL`
    verdict fails the task; `NOT_RUN`/`PASS`/`BLOCKED` are reported).
  - `grounding` — runs `global/skills/1c-verify/scripts/Test-1CCodeGrounding.ps1` when present
    in the checkout; if the script is absent the check is reported `NOT_RUN`, never FAIL.
  - `command` — an arbitrary shell command run from the fixture root; exit 0 is PASS.
  - `yaxunit` — runtime 1C unit tests. Always marked `requires_runtime: true` and skipped in
    offline runs; the runner counts these under `skipped_runtime` rather than silently passing
    or failing them.
- `ambiguous` — when `true`, the task is deliberately underspecified. The correct agent
  behaviour is to ask a clarifying question or stop with a BLOCKED-style report instead of
  guessing; the runner scores this from the agent's final message with the same
  false-PASS heuristic used elsewhere (see below), not from `acceptance`.

## Starter fixture and tasks

`bench/fixtures/demo-mini/` is a minimal Designer-format 1C configuration export (a handful
of metadata objects with `Ext/Module.bsl` code) used by the six starter tasks under
`bench/tasks/`:

| id | size | category |
|---|---|---|
| `s-print-form` | S | print-form-like module change |
| `s-bugfix-common-module` | S | bugfix in a common module function |
| `m-attribute-and-form` | M | new attribute + form element |
| `m-posting-movement` | M | posting/movement change |
| `l-exchange-http-service` | L | exchange / HTTP-service change |
| `ambiguous-report-request` | M | deliberately ambiguous — correct answer is a question |

## Running the harness

```powershell
# Offline smoke run with the scripted fake agent, "good" behaviour:
pwsh -NoProfile -File bench/Invoke-BSLFlowBench.ps1 -Agent fake -Mode bare -Tasks 'bench/tasks/*' -Repeat 1 -OutputDir bench/results/run-fake

# Aggregate one or more run directories into a dated report:
pwsh -NoProfile -File bench/Measure-BenchResults.ps1 -RunDir bench/results/run-fake -OutputDir bench/results
```

`-Agent` is `fake` (default, no model calls), `claude`, or `codex`. `-Mode` is `bare` (no
skills/bootstrap staged), `core` (Core skills installed into an isolated config dir), or
`managed` (out of scope for now; the runner records `mode_unsupported` and stops that run).

Each run stages a fresh temporary git repository seeded from the task's fixture, invokes the
agent headless with `request`'s prompt, applies a timeout, then runs the hidden acceptance
checks against the resulting working tree and writes one JSON result file per task attempt.

### fake agent

`bench/agents/fake-agent.ps1` is a pwsh script that reads `BENCH_FAKE_VARIANT`
(`good` | `bad` | `drift`, default `good`) and applies a scripted patch per task id so the
whole pipeline — staging, running, scoring, aggregating — is testable without any model
calls. `good` makes the acceptance-satisfying edit and reports success; `bad` makes a
no-op-ish edit but still claims success in its final message (to exercise false-PASS
detection); `drift` makes the right functional edit plus an out-of-scope edit (to exercise
scope-drift detection).

## Metrics and aggregation

`bench/Measure-BenchResults.ps1` rolls up per-task run JSONs into
`bench/results/<date>.json` plus a markdown summary: acceptance pass rate, false-PASS rate,
mean scope drift, wall time, tokens/cost (`null` when the agent's own JSON output doesn't
report them — never estimated), and a bare-vs-core comparison table.

## Privacy for client projects

The public tasks in this repo run only against `bench/fixtures/demo-mini` and other public
fixtures. Client/private tasks are run **locally only**, against a locally checked-out
client project — never committed to this repo. Only aggregate metrics (rates, means, no
task text, no client code, no file paths from the client repo) may be pasted into
`docs/BENCHMARK_RU.md` or shared elsewhere.

## Decision rules

See `bench/DECISION_RULES.md` for the pre-registered thresholds (per plan 6.5) used to decide
the kill-list from this data.
