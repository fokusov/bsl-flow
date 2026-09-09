# Task CLI contract, schema 1

`scripts/Invoke-BSLFlowTask.ps1` accepts `-Action`, `-ProjectPath`, optional `-TaskId`, `-InputFile`, `-AttemptId`, and `-CodexPath` (native executable). Paths containing spaces must be passed as PowerShell arguments, never concatenated shell commands. PowerShell 5.1 and 7 are supported.

Actions: `Start`, `Status`, `Next`, `Run`, `Record`, `Update`, `Accept`, `Resume`, `Cancel`, `Deliver`, `Serve`. `Record` consumes only the terminal result in the exact registered attempt directory. It does not import a user-supplied `passed=true` file. There is no force-pass command. `Serve` takes queue input without a task ID and returns a queue envelope; exit 0 requires every registered task to remain completed, 10 means user input is needed, and 11 means waiting or blocked.

The Go executable exposes `bsl-flow task <action> --project <path> ...` with the same state and JSON/exit contract. `task deliver` creates an immutable local source handoff from an existing current implementation acceptance; it performs no commit/push/deployment. `runner run --project <path> --input <queue.json>` supervises explicitly listed registered tasks. The executable embeds the installed engine/instructions and verifies their bytes before execution; Windows PowerShell, Git and the configured model provider remain dependencies. Its own executable identity is included in the policy snapshot through the host-owned `BSL_FLOW_HOST_PATH` environment field. Moving/upgrading the executable or switching between direct script and binary requires an explicit policy scope reconciliation for existing tasks.

## Request

```json
{
  "schema_version": 1,
  "request_id": "e450d763-776a-4786-8dbe-6d71a9e25fb4",
  "prompt": "Add a short description to hello.txt explaining its purpose.",
  "mode": "implement",
  "analysis_goal": "analysis",
  "complexity": "S",
  "risk": "low",
  "impact_flags": [],
  "criteria": [{"id":"description","kind":"file_assertion","observation":"The file describes its purpose.","path":"hello.txt","contains":"Example greeting"}],
  "provenance": {"source":"user","reference":"current-user-request","text":"Add the description."},
  "models": {"worker":"gpt-6-astra","worker_effort":"medium","reviewer":"gpt-6-astra","reviewer_effort":"high"}
}
```

Generate a fresh UUID for a new task. Repeating the same initial request with the same UUID is idempotent; changed payload under the same UUID conflicts. Model names are examples, not global routing overrides. The operator/parent chooses them from project instructions and actual availability.

`mode`: `analysis_only` or `implement`. `analysis_goal`: `analysis` or `specification`. Classification is `S|M|L`, risk `low|medium|high`. Inspection can strengthen the initial route; a user scope update is needed to weaken it. Flags: `permissions`, `data_migration`, `data_deletion`, `posting`, `data_exchange`, `form_flow`, `external_artifact`, `ambiguous_business_rule`.

Optional fields: `source_paths` (discovery hints, default `["."]`), `require_spec_review`, `require_code_review`, `max_attempts` (default 16, range 1–64), `timeout_seconds` (default 1800, range 1–14400). All fields outside the contract are rejected. The manifest always covers the complete worktree, including tracked/untracked files and baseline deletions, because the worker can write that complete tree. `.git`, `.bsl-flow`, `.bsl-flow-worker` are excluded administrative/generated paths. A clean committed baseline is required; the controller never stashes or discards dirty work. Applicable executable Git filters block checkout before any source script executes.

Every implementation needs criteria selected before dispatch. `file_assertion` is only a literal source/document observation, never evidence of runtime behavior. `static`/`unit` criteria specify an absolute native `executable`, `arguments` array, a `report` path under `.bsl-flow-worker/`, and nonempty exact `expected_tests` names. Before a permitted test attempt, an existing report is preserved in that attempt's raw evidence and removed from the generated path; the process must produce a new original report. Executable processes run inside the same restricted sandbox; required skips, missing reports and changed sources block acceptance. `integration`/`ui` additionally require an exact `target`; they and `external_artifact` remain blocked until a runtime adapter has been validated and authorized. Do not relabel these criteria as static to bypass that boundary.

### Bounded source repair

`max_source_repairs` is optional, defaults to 0, range 0–3. A completed deterministic failure may enter `diagnose → implement → code_review → verify` only when every criterion is a file assertion or a `static`/`unit` command explicitly declared `retry_safe: true`. Repairable command criteria must also declare nonempty `protected_paths` covering their test code and fixtures. These relative files/directories are frozen to the failed verification's observed manifest during automatic repairs; changing, deleting, or adding protected inputs blocks further execution. Include all test dependencies, not just the top-level runner. Test executables are bound by SHA-256. Changing a test contract requires a trusted scope revision and fresh applicable gates.

The controller validates the original failed result and exact current dependencies before diagnosis. The diagnosis returns a category, rationale, evidence and bounded instructions; only `implementation` permits repair. `business_rule` creates a durable question, while `environment`, `test_contract` and `unknown` block. Worker text saying FAIL is not proof of a completed safe verification. Missing/corrupt/stale JUnit, required skips, timeouts, source changes during verification and unknown effects remain blocked. Unchanged failed source or exhausted budget terminates further diagnosis. Every repair forces independent code review, even for S. Original failures remain in the journal. A saved completed read-only diagnosis can be recovered without another model call.

### Local queue

A queue input is a trusted operator document with `schema_version:1`, `queue_id` UUID, unique `task_ids`, `poll_seconds` (1–60), and `max_cycles` (1–10000). It selects only registered tasks in one explicit project. The runner has one OS owner lock, records queue identity and operational progress, and uses the controller's decisions. Questions, errors, cancellation and uncertain effects remain stopped until an applicable user update; unchanged blocked tasks are not retried every poll. Restarting the same queue command uses the saved task state. The installer does not register an OS service or startup schedule. `max_cycles` bounds queue polling; an in-progress task retains its own request timeout/attempt budget, so it is not a hard wall-time limit for the supervisor.

## Updates and recovery

An update requires `schema_version:1`, `input_event_id` UUID, `expected_revision`, `kind`, and the same `provenance` object. `clarification` includes `text`; `authorization` includes `mode` and/or `resume:true`; `scope_change` includes the full new `request` with unchanged task ID. Include the current `question_id` when answering a question. Duplicate event IDs are checked before the expected revision; conflicting duplicate payloads are rejected. Authorization-only changes preserve the intent hash and existing valid spec review.

The explicit input file is a trusted operator channel, not cryptographic proof of human identity. Managed workers cannot write it or the authoritative state. Direct human/external-terminal actions are outside the enforcement boundary.

An uncertain source/test effect survives ordinary authorization and scope changes. `kind: "recovery"` additionally requires `resolution: {"attempt_id":"the exact attempt UUID","scope":"source_only","source_sha256":"the current complete source manifest hash","observation":"the result of inspecting retained output and actual sources"}`. The controller checks the fresh hash and preserves the observed manifest. This is a trusted operator control-read channel; it does not resolve or authorize 1C runtime writes. A cancelled task also needs an explicit resume authorization after reconciliation.

`Cancel` disables new dispatch and stops saved owned process trees by PID and start time, including a child left after its controller exited. Already sent external actions might have applied; cancellation is not rollback. `Resume` can record an existing terminal receipt without rerunning an action; completed read-only raw results can be finalized after their controller exits. A live controller or child is not duplicated. Missing terminal receipt with unknown effects is blocked for inspection. An exact source-only `recovery` update may close an orphan after the controller and all saved children exit; it saves the control-read manifest and abandoned attempt identity without fabricating a PASS. Recovery preserves cancellation until an explicit resume authorization.

## State, output and exit codes

`.bsl-flow/tasks/<uuid>/revisions` is an immutable hash-linked history. `current.json` is derived; deleting or corrupting it cannot change truth. An OS file lock serializes writes, and a saved attempt precedes dispatch. Input events, raw evidence and acceptance receipts remain in this directory; worker sources are in `.bsl-flow/worktrees/<uuid>`.

The public CLI JSON envelope includes `schema_version`, `task_id`, `revision`, `status`, `stage` (last registered stage), `next_stage` (computed next stage), `next_action`, `blockers`, `evidence_refs`, plus the worktree/acceptance when known. A blocked stage retains its concrete reason, including lint diagnostics; another `Run` does not silently retry it. `Status` and `Next` do not call models or tests. Statuses are `ready`, `running`, `needs_input`, `blocked`, `failed`, `completed`, `cancelled`.

Exit codes: 0 command success/accepted run; 10 needs input; 11 blocked; 12 demonstrated failure; 13 cancelled run; 2 invalid input; 3 identity/revision/lock conflict; 4 internal controller error. Read-only commands can return 0 with a blocked envelope. Successful `Cancel` returns 0; subsequent `Run` returns 13. Always inspect the envelope.

Acceptance binds intent, policy, baseline, exact source manifest and current required gates. The receipt authorizes handoff only. It does not authorize merge/push/deploy or 1C database operations.
