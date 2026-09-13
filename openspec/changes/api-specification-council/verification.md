# Verification: api-specification-council

## Current remediation, 2026-09-12

Status: ACCEPT for the agreed offline/public-path slice. All mandatory offline checks completed on one frozen source inventory, with the package tail resumed after a Windows Git path-length failure. The original wrapper failure remains recorded. This section supersedes all historical PASS claims below.

Current evidence and boundaries:

- The original ordinary-worker regression was reproduced: `--ephemeral` cannot provide a persisted rollout. Ordinary workers now permit absent observed identity; strict Council fallback uses a persisted fresh context and requires its terminal identity.
- Current-agent capability must be proved from the exact native executable, sandbox, catalog, skills inventory and current host receipt. Only the previously evidenced native Luna host is supported; the actual Astra host used for this task remains BLOCKED before fallback dispatch. Requested model values are not observed provenance.
- Transport has 58 passing offline checks, including real loopback HTTP, bounded streamed body/deadline, terminal provider envelopes, no redirect and credential handling. No new paid provider call was made.
- The public assisted entry and managed entry share the host adapter and prepared-publication recovery. The final public-entry checks passed: Routing 16, Lifecycle 31, ManagedReview 13, host capability 7, Profiled adapter 97. Independent code review: ACCEPT. Native process/RPC/capability and part of dispatcher environment remain fixtures in the scoped adapter test.
- Architecture evidence includes selected ADR excerpts and binds their content. Policy/input drift and final chair publication must be checked before replay or mutation.

The final report is `docs/THREE_SPEC_REMEDIATION_2026-09-12_RU.md`. Historical provider smoke below is prior evidence only and does not prove live execution of this changed implementation.

## Historical verification record (superseded)

# Verification: api-specification-council

- Date: 2026-09-12 (fourth independent review), second remediation pass verified same day
- Scope: current working tree against `original-task.md`, `spec.md`, and `design.md`; the two fourth-review defects (dead managed fallback, non-atomic budget admission) were remediated and re-verified with positive public and concurrent negative tests
- Verdict after second remediation: **PASS (offline + authorized live smoke)**
- Evidence boundary: offline tests; the previously retained DeepSeek smoke remains the live transport evidence and was not repeated

## Fourth-review remediation outcome

1. **The packaged managed `current_agent` fallback now produces the receipt it requires.** `codex-cli` 0.153/0.154 JSONL streams carry no model fields, so the adapters now observe the resolved identity from the rollout session file the host itself persists: new `Get-BFObservedModelEffort` (Task.Storage.ps1) locates `CODEX_HOME/sessions/YYYY/MM/DD/rollout-<ts>-<session_id>.jsonl` by exact session id, requires the `turn_context` payload, and returns its `model`/`effort`; both `adapters/Codex.ps1` and `adapters/ProfiledCodex.ps1` write those values into `host-result.json` and block when the rollout is missing, ambiguous, or lacks resolved identity. Verified against a real `codex exec` rollout (`gpt-6-astra`/`low` extracted correctly). The positive public tokenless managed scenario is now covered end-to-end: `Test-TaskManagedReview.ps1` gained a council v2 case that stubs only the process boundary, keeps the real adapter → rollout observation → host receipt → fallback-runner chain, and asserts four fresh contexts, envelope `observed.model=resolved-current-model`/`effort=resolved-medium` (never request values), `multi_role_single_model` and `fallback_visible`.
2. **Admission and reservation are one atomic ledger transaction.** New `Approve-BSLFlowCouncilBudgetDispatch` performs the ledger read, limit check, and reservation write under a single mutex hold; `Invoke-BSLFlowCouncilDispatchRole` and the chair leg use it. A concurrent negative test reproduces the reviewer's exact barrier scenario (two parallel roles, estimate 0.04, limit 0.05): exactly one role is admitted and the durable reserved total stays `0.04 <= 0.05` (Cycle test 9).

## Fourth recheck outcome (historical; both defects remediated, see the header)

The remediation is real: full dependency hashes and chair aggregate binding are present, optional terminal failures are published, member envelopes are versioned, and pure resume no longer adds a second full-cycle estimate. All 111 focused assertions pass. The implementation still failed the default tokenless managed scenario and the concurrent budget safety contract at the time of the recheck.

### [P1] The packaged managed `current_agent` fallback cannot produce the receipt it requires

`Task.ManagedReview.ps1` now correctly refuses to fabricate observed provenance and requires non-empty `observed_model` and `observed_effort` from `host-result.json`. However, both packaged Codex adapters unconditionally write those two fields as `null`. The positive cycle fixture supplies synthetic `receipt-model`/`receipt-effort`; `Test-TaskManagedReview.ps1` covers only `fallback: block`, not a successful tokenless `current_agent` managed run. The assisted public entry point likewise supplies no capability/runner and explicitly blocks current-agent fallback.

Evidence:

- `global/skills/1c-task/scripts/Task.ManagedReview.ps1:21-58`
- `global/skills/1c-task/adapters/Codex.ps1:85-87`
- `global/skills/1c-task/adapters/ProfiledCodex.ps1:208-210`
- `global/skills/1c-spec-review/scripts/Invoke-1CSpecReview.ps1:92-101`
- `scripts/Test-TaskManagedReview.ps1:47-70`

Failure scenario: a new default project has no external tokens. The managed adapter dispatches a paid/subscription worker context, then blocks because its own receipt contains null observed identity; assisted mode blocks before dispatch. No packaged public route completes the required four fresh current-agent contexts. This violates requirements 6, 7, 24 and the first acceptance scenario.

### [P1] Parallel admission and reservation are not one atomic ledger operation

The mutex protects `Test-BSLFlowCouncilLedgerAdmission` and `Add-BSLFlowCouncilBudgetReservation` separately. Between those calls another parallel role can pass admission against the same ledger state. The claim that the mutex prevents double booking is therefore false; the test suite covers tight-limit sequential resume, not concurrent admission.

Evidence:

- `global/skills/1c-spec-review/scripts/Invoke-CouncilReview.ps1:299-333`
- `global/skills/1c-spec-review/scripts/Council.Engine.ps1:300-342,373-425`
- Independent barrier reproduction: two parallel roles each admitted `0.04` under limit `0.05`, then both reservations succeeded; durable reserved total was `0.08` (`both_admitted=True`, `oversubscribed=True`).

Failure scenario: bounded parallel critics observe the same pre-reservation ledger and collectively exceed the configured budget before any API call. This violates requirements 20 and 22.

### Evidence-report correction

The retained latest live smoke artifact is real and sanitized: four `direct_api` members completed against `deepseek-flash`, provider usage and observed model are present, final validation passed, and a durable completed event exists. Its review/chair verdict is `REVISE`, which is acceptable for transport smoke evidence but is not a council content PASS. The previous report header and historical `Not run` section contradicted each other; this fourth review treats the retained artifact only as live transport/provenance evidence and made no additional paid call.

## Fourth-recheck checks (historical) and remediation rerun

Remediation rerun (2026-09-12, after the fourth-review fixes):

- `Test-CouncilValidation.ps1`: 15 assertions.
- `Test-CouncilEngine.ps1`: 21 assertions.
- `Test-CouncilTransport.ps1`: 24 assertions.
- `Test-CouncilFallback.ps1`: 7 assertions.
- `Test-CouncilRouting.ps1`: 9 assertions.
- `Test-CouncilLifecycle.ps1`: 7 assertions.
- `Test-CouncilCycle.ps1`: 30 assertions (adds the concurrent admission/reservation barrier: two parallel roles at estimate 0.04 under limit 0.05 admit exactly one, reserved total stays within the limit).
- `Test-TaskManagedReview.ps1`: 12 checks (adds the positive public tokenless managed council scenario: rollout-observed identity through the real adapter chain, four fresh contexts, envelope provenance from the receipt).
- `Test-BSLFlowProfiledCodex.ps1`: 90 checks (adds rollout extraction fixtures and negative missing-rollout coverage).

Focused council total: 113 assertions passed.

Adjacent suites rerun: `Test-TaskExecutionProfile.ps1` (130 checks), `Test-ReviewReliability.ps1` (127 checks), `Test-TaskHardening.ps1` (24 checks), `Test-TaskLifecycle.ps1` (51 checks) — all pass.

Historical fourth-recheck result before the remediation: 111 focused assertions passed, but the concurrent barrier reproduced oversubscription (`0.08` reserved under a `0.05` limit) and no public tokenless managed scenario existed.

## Second remediation outcome (historical; superseded by the fourth review)

1. **Attempt binding now covers every frozen dependency.** `input_hashes` carries `original_task_sha256`, `spec_sha256`, `design_sha256`, `evidence_sha256`, `policy_hash` and `rubric_sha256` for every role, plus `member_aggregate_sha256` for the chair (computed over the canonical member aggregate after the members complete). `New-BSLFlowCouncilAttempt` rejects bindings missing any field. Negative tests: design drift, evidence drift, and policy drift each create a new sequenced attempt (Engine suite), and a full-cycle rerun after editing `design.md` produces fresh dispatches and a new `attempt-0002` id instead of stale reuse (Cycle test 6).
2. **Fallback provenance comes only from the host receipt.** The managed runner reads `host-result.json` written by the adapter after the fresh worker context terminates and returns `observed_model`/`observed_effort`; the dispatcher refuses to promote requested capability values into observed provenance and blocks when a claimed capability has no terminal receipt. The runner also verifies the receipt's `requested_model/effort` match the registered reviewer route. `adapters/Codex.ps1` now selects `models.reviewer*` for `spec_review` exactly like the profiled and OpenCode adapters, removing the worker-model substitution. Test: cycle test 3 asserts the envelope carries `receipt-model`/`receipt-effort`, not the capability claim.
3. **Optional terminal failures are published.** Report assembly now includes every enabled role's terminal envelope; `Get-BSLFlowCouncilDiversity` sees the failure and reports `degraded`. Negative test: an enabled optional brainstorm that fails pre-acceptance stays in `review.members` with `failed_before_acceptance` and degrades diversity (Cycle test 7).
4. **Resume budget no longer double-counts.** The cycle-level pre-admission that re-added all binding estimates was removed; admission is now per-dispatch — `Invoke-BSLFlowCouncilDispatchRole` and the chair leg admit the single estimate against the whole durable ledger immediately before the reservation, under a named `Global\...` mutex that serializes reservation/outcome/admission reads and writes across thread jobs and processes. Negative test: with a limit that forbids any new call (`0.045` vs `0.04` retained spend), a pure resume passes admission with zero new dispatches, while double-counting estimates would refuse it (Cycle test 8).
5. **Versioned payload contract closed.** Member envelopes now carry `schema_version: 1` and a sanitized `summary`; the validator, engine, and `council-review-schema.json` require them, and the validator additionally binds member input hashes to the review's design/policy inputs.

## Third recheck outcome (historical; superseded by the second remediation)

The seven focused council suites pass (103 assertions), and the earlier duplicate reconciler, basic fallback wiring, immutable terminal files, question fan-in, provider-observed direct-API model, and prepared-publication changes are present. However, the focused suites do not cover the following negative scenarios, so the agent-authored offline PASS is not accepted.

### [P1] Attempt binding still ignores material frozen dependencies

`Get-BSLFlowCouncilRoleBindings` stores only `original_task_sha256` and `spec_sha256` in `input_hashes`. It omits `design_sha256`, `evidence_sha256`, `policy_hash`, role/rubric binding, and—specifically for the chair—the canonical member aggregate/payload hashes. `New-BSLFlowCouncilAttempt` therefore reuses an old attempt when any omitted dependency changes.

Evidence:

- `global/skills/1c-spec-review/scripts/Invoke-CouncilReview.ps1:113-119`
- `global/skills/1c-spec-review/scripts/Council.Engine.ps1:102-133`
- Reproduction: run public `-DryRun`, change both `design.md` and `EvidenceText`, run it again; result was `same_attempt_ids: True` for all four enabled roles.

Failure scenario: a corrected design or new inspected evidence is supplied, but retained critic/chair results for the old inputs are reused without a new dispatch. This violates requirements 10, 19, and the explicit drift/replay contract in the design.

### [P1] Managed current-agent provenance is asserted from request values, not observed by the host

The managed adapter builds the capability's `model` and `effort` directly from `State.request.models.reviewer*`, then the fallback dispatcher copies those values into `observed`. No terminal host receipt is read. On the legacy Codex adapter path, `spec_review` actually selects the **worker** model/effort because only `code_review` selects reviewer values; the persisted fallback provenance can therefore name a model that was not dispatched. On the profiled path, `host-result.json` explicitly records `observed_model=null` and `observed_effort=null`, but the council still reports the requested values as observed.

Evidence:

- `global/skills/1c-task/scripts/Task.ManagedReview.ps1:21-28,37-40`
- `global/skills/1c-task/adapters/Codex.ps1:51-58,85-87`
- `global/skills/1c-task/adapters/ProfiledCodex.ps1:94-97,208-210`
- `global/skills/1c-spec-review/scripts/Invoke-CouncilReview.ps1:223-236`

Failure scenario: request says reviewer=Astra, fallback launches the worker model, while `review.json` claims observed=Astra and may derive a false diversity result. This violates requirements 6, 7, 13, 17, and 18.

### [P1] Optional terminal failures disappear from the council report

Terminal failure files are persisted, but report assembly adds only envelopes whose status is `completed`. Consequently an enabled optional role that times out or returns invalid output is absent from `review.members`; `Get-BSLFlowCouncilDiversity` cannot see the failure and may report `multi_model`/`multi_role_single_model` instead of `degraded`.

Evidence:

- `global/skills/1c-spec-review/scripts/Invoke-CouncilReview.ps1:573-585`
- `global/skills/1c-spec-review/scripts/Council.Validation.ps1:210-227`

Failure scenario: optional brainstorm/critic fails, chair completes, and the published sanitized report contains neither that role nor its terminal status. This violates requirements 8, 17, and 18.

### [P1] Resume budget admission double-counts already retained dispatches

At cycle entry, admission sums every durable prior reservation/outcome and then adds estimates for every configured binding again, before determining which completed attempts will be reused. A resume with no new dispatch can therefore exceed the limit and block solely because the same calls are counted twice.

Evidence:

- `global/skills/1c-spec-review/scripts/Invoke-CouncilReview.ps1:365-375`
- `global/skills/1c-spec-review/scripts/Council.Engine.ps1:327-365`

Failure scenario: the first cycle consumes a budget near its limit and crashes before publication; resume should reuse all completed calls, but admission adds a second full-cycle estimate and refuses recovery. This violates requirements 19, 22, and 25.

### Additional contract gap

Requirement 13 calls for a versioned role payload, and the design requires `schema_version` plus `summary` in each member envelope. The current payload validator rejects those fields and the v2 member schema contains neither. This is a schema evolution gap even though current fixtures agree with the implementation.

## Third-recheck checks (historical) and second-remediation checks

Second remediation rerun (2026-09-12):

- `Test-CouncilValidation.ps1`: 15 assertions.
- `Test-CouncilEngine.ps1`: 21 assertions (adds design/evidence/policy drift attempt checks).
- `Test-CouncilTransport.ps1`: 24 assertions.
- `Test-CouncilFallback.ps1`: 7 assertions.
- `Test-CouncilRouting.ps1`: 9 assertions.
- `Test-CouncilLifecycle.ps1`: 7 assertions.
- `Test-CouncilCycle.ps1`: 28 assertions (adds design-drift fresh dispatch/new attempt id, optional failure envelope in members + degraded diversity, and tight-limit resume without new dispatches).
- `Test-TaskManagedReview.ps1`: 8 checks (adds a council v2 negative case: blocked fallback with missing token refuses before any dispatch and never substitutes the worker model).

Total: 111 focused council assertions passed.

Adjacent suites rerun: `Test-TaskExecutionProfile.ps1` (130 checks), `Test-ReviewReliability.ps1` (127 checks), `Test-TaskHardening.ps1` (24 checks), `Test-TaskLifecycle.ps1` (51 checks) — all pass.

Historical third-recheck result before the second remediation: the seven focused council suites passed (103 assertions) but did not cover the four negative scenarios above; `Test-TaskManagedReview.ps1` exercised only the legacy single-reviewer path; `Test-BSLFlowProfiledCodex.ps1` passed 88 checks and confirmed host receipts carry no observed model/effort.

## Previous remediation claim (historical; superseded by the third review)

The implementation has materially improved since the first verification. The public council cycle now exists, provider envelopes and effort are handled, attempts are sequenced, local `base_url` is applied, unknown configuration keys are rejected, publication checks third content, final validation runs before the completed event, and the council suites are wired into package/CI tests.

The second-review findings below were all fixed in the remediation pass. Summary of fixes:

1. Managed `spec_review` (council v2) no longer dispatches the legacy `spec_reconcile` worker; the controller materializes the reconciliation sidecar from the chair record and re-runs final validation (`Task.Stages.ps1`).
2. The default `current_agent` fallback is connected: `Invoke-BSLFlowCouncilReview` accepts `-FallbackRunner`/`-Capabilities`; the managed host supplies a trusted capability receipt from the registered request models and a fresh-context runner over `Invoke-BFManagedWorker`; a tokenless default route now completes four fresh contexts offline (cycle test 3).
3. Dispatch errors are classified and persisted as immutable per-attempt `result-<seq>.json` terminal results; retained completed results are reused without a second paid call; `unknown_after_dispatch` blocks resume instead of repeating the dispatch (cycle tests 4-5).
4. Chair fan-in carries brainstorm output, member questions, role rubrics and classification; the chair contract requires `final_spec_text`, references must resolve against the actual final text, and unanswered member questions force a `needs_input` verdict (validation + cycle test 1).
5. Direct-API provenance now reports the model and usage observed in the provider response envelope instead of the requested binding (transport test).
6. Member envelopes carry `attempt_id`, sanitized usage, `cost_state` and dispatch timestamps; a durable per-dispatch budget ledger (reservation + outcome) feeds admission; `max_parallel` bounds direct-API member concurrency via thread jobs; the cycle accepts a `-Cancelled` probe.

## Findings (from the second review; all remediated)

### [P1] The managed stage still launches a second model reconciler after the council chair

`Invoke-BFProfileSpecCritic` now runs the complete council and returns its v2 review. `Invoke-BFSpecReviewStage` nevertheless continues into the legacy `spec_reconcile` worker, writes a separate v1 reconciliation, and may rewrite `spec.md`/`design.md` after the council has already published and validated its final bytes.

Evidence:

- `global/skills/1c-task/scripts/Task.ManagedReview.ps1:13-22`
- `global/skills/1c-task/scripts/Task.Stages.ps1:92-109`

Failure scenario: the chair accepts a finding and publishes the corrected spec; the second reconciler returns different text. The v2 review remains bound to the chair's bytes, so final validation blocks, or two model stages make conflicting reconciliation decisions. This directly violates requirement 24 that the chair replace `spec_reconcile` in managed mode.

### [P1] The specified default current-agent fallback is still not connected to either public host route

The default dispatcher explicitly refuses `current_agent_fallback` and requires a custom fallback-capable dispatcher plus capability receipts. The assisted entry point and managed entry point call the council without supplying either. Therefore the packaged default configuration (no tokens, fallback `current_agent`) always blocks even when the surrounding Codex task has a capable current model.

Evidence:

- `global/skills/1c-spec-review/scripts/Invoke-CouncilReview.ps1:160-170`
- `global/skills/1c-spec-review/scripts/Invoke-1CSpecReview.ps1:92-98`
- `global/skills/1c-task/scripts/Task.ManagedReview.ps1:18-22`
- `global/skills/1c-spec-review/scripts/Test-CouncilCycle.ps1:99-104`

Failure scenario: a new project has no external tokens, as expected by the first acceptance criterion. Required review blocks before the first critic instead of creating four fresh current-model contexts.

### [P1] Unknown dispatch outcomes are not persisted and completed attempts are dispatched again

The cycle calls the dispatcher without classifying exceptions. A transport timeout raises `BF_UNKNOWN_AFTER_DISPATCH`, leaving only the pre-dispatch attempt file and no terminal member envelope. On resume, the identical attempt is returned and dispatched again. Even successful completed attempts are unconditionally dispatched again because the cycle never reads and reuses their retained terminal result. `member.json` is one mutable file per role rather than an immutable result bound to the attempt sequence.

Evidence:

- `global/skills/1c-spec-review/scripts/Council.Engine.ps1:115-136`
- `global/skills/1c-spec-review/scripts/Council.Engine.ps1:179-181`
- `global/skills/1c-spec-review/scripts/Invoke-CouncilReview.ps1:215-246`
- `global/skills/1c-spec-review/scripts/Council.Transport.ps1:218-230`

Failure scenario: a provider accepts a paid request and the connection times out. Re-running the same council blindly sends the same role again instead of retaining `unknown_after_dispatch` as a blocker. This violates requirements 19, 21, and 22.

### [P1] Chair fan-in loses material member output and does not require a revised final specification

The aggregate passed to the chair contains only findings, protected items, and the requirement manifest. Brainstorm alternatives/risks/unknowns/questions and critic `needs_input_questions` are discarded. The prompt does not include role rubrics or classification, and the chair contract does not require `final_spec_text`/`final_design_text`. Those fields are optional in assembly; if absent, the original draft is reused. Final references are checked only as non-empty strings, not as references to the actual final text.

Evidence:

- `global/skills/1c-spec-review/scripts/Invoke-CouncilReview.ps1:116-157`
- `global/skills/1c-spec-review/scripts/Invoke-CouncilReview.ps1:258-290`
- `global/skills/1c-spec-review/scripts/Invoke-CouncilReview.ps1:326-357`
- `global/skills/1c-spec-review/scripts/Council.Validation.ps1:266-307`

Failure scenarios:

- A critic returns `needs_input` with a material question; the question never reaches the chair, which can return `PASS`.
- The chair accepts a finding, supplies arbitrary non-empty `resolution_refs`, omits final text, and the unchanged draft can still pass the structural gate.

This violates requirements 10-16 and the main purpose of producing a minimally revised executable specification.

### [P1] Direct API provenance is reported from requested configuration, not provider observations

After a real API response, the default dispatcher sets `observed.provider/model/effort` directly from the attempt binding. The transport extracts the model text but does not return provider-observed identity/usage. Diversity can therefore be reported as `multi_model` merely because configured profiles differ, even if a provider aliases or reroutes models or the actual model is unknown.

Evidence:

- `global/skills/1c-spec-review/scripts/Invoke-CouncilReview.ps1:176-180`
- `global/skills/1c-spec-review/scripts/Invoke-CouncilReview.ps1:242-246`
- `global/skills/1c-spec-review/scripts/Council.Transport.ps1:231-250`

Failure scenario: two configured model IDs resolve to one actual model. The report still claims `multi_model`, contrary to requirements 13, 17, and 18.

### [P2] Budget, concurrency, and sanitized receipt contracts remain partial

Budget code performs one in-memory admission calculation, but it does not create durable per-dispatch reservations, reconcile outcomes, or preserve provider-reported usage separately from billed/observed cost. Member schema/envelopes omit `attempt_id`, usage, cost state, and timestamps. `max_parallel` is parsed but the role loop is sequential and never uses it; cancellation is not connected to the council cycle.

Evidence:

- `global/skills/1c-spec-review/scripts/Invoke-CouncilReview.ps1:202-215`
- `global/skills/1c-spec-review/scripts/Council.Transport.ps1:33-65`
- `global/skills/1c-spec-review/scripts/Council.Engine.ps1:161-181`
- `global/skills/1c-spec-review/references/council-review-schema.json:64-107`

This leaves requirements 19-22 only partially implemented and makes the live paid-call safety claim unproven.

## Requirement coverage

| Requirements | Result | Current evidence |
| --- | --- | --- |
| 1-5 | PASS (offline) | Executable cycle, API adapters, config validation, local overlay, endpoint protection, and publication wiring exist; full cycle passes offline. |
| 6-9 | PASS (offline) | Fallback policy plus connected host runner/capability contract; tokenless default route completes four fresh contexts (cycle test 3); missing credential with `fallback: block` refuses before dispatch; the public tokenless managed scenario completes through the real adapter chain with rollout-observed identity (managed review positive case). |
| 10-16 | PASS (offline) | Fan-in carries classification, rubrics, brainstorm and questions; chair must return full final text with resolvable references; questions force needs_input (cycle test 1). |
| 17-18 | PASS (offline) | Direct-API observed provenance from the provider envelope (transport test); fallback observed identity from the terminal host receipt sourced from the host rollout session file, never from request values (cycle test 3 + managed review positive case); optional failures published and degrade diversity (cycle test 7). |
| 19-22 | PASS (offline) | Full binding (design/evidence/policy/rubric + chair aggregate) with drift tests; terminal reuse, unknown-effect blocking, durable budget ledger with atomic admission+reservation under one mutex hold including the concurrent barrier test (cycle tests 4-6, 8-9), usage/cost_state/timestamps, cancellation probe, bounded thread-job concurrency. |
| 23 | PASS (offline + live smoke) | Bounded input and token separation implemented; authorized live smoke produced real provider receipts with sanitized usage and provider-observed model over HTTPS. |
| 24 | PASS (offline) | Council v2 managed path returns after chair publication; no second reconciler is dispatched; reconciliation sidecar + final validation are controller-owned; council v2 negative case added to managed review suite. |
| 25 | PASS (offline) | Prepared/resume checks spec/design/review third content and runs the deterministic gate before `completed`; resume reuses retained results without re-counting budget (cycle test 8). |
| 26 | PASS (offline) | New defaults, explicit migration blocker, compatibility route, packaging, and CI wiring are present. |

## Checks performed

Fourth-review remediation rerun (2026-09-12):

- `Test-CouncilValidation.ps1`: 15 assertions.
- `Test-CouncilEngine.ps1`: 21 assertions.
- `Test-CouncilTransport.ps1`: 24 assertions.
- `Test-CouncilFallback.ps1`: 7 assertions.
- `Test-CouncilRouting.ps1`: 9 assertions.
- `Test-CouncilCycle.ps1`: 30 assertions (adds the concurrent admission/reservation barrier: two parallel roles at estimate 0.04 under limit 0.05 admit exactly one; durable reserved total stays within the limit).
- `Test-CouncilLifecycle.ps1`: 7 assertions.
- `Test-BSLFlowProfiledCodex.ps1`: 90 checks (adds rollout extraction fixtures and missing-rollout negative coverage).
- `Test-TaskManagedReview.ps1`: 12 checks (adds the positive public tokenless managed council scenario through the real adapter chain).

Focused council total: 113 assertions passed.

Second remediation rerun (2026-09-12, historical):

- Focused council suites: 111 assertions; cycle covered design-drift fresh dispatch/new attempt id, optional failure envelope in members + degraded diversity, tight-limit resume without new dispatches; managed review 8 checks (council v2 negative case).

Authorized live smoke (2026-09-12, explicitly budgeted by the user):

- Real outbound council cycle against the DeepSeek API (`deepseek-flash`, all four enabled roles, `openai_compatible` transport).
- All members `completed` via `direct_api`; `observed.model=deepseek-flash` extracted from the provider response envelope (requirement 17/18 evidence).
- Provider usage captured end-to-end and sanitized into member envelopes and the durable ledger: intent_critic in=6497/out=6865/reasoning=4728, architecture_critic in=6503/out=8917/reasoning=4987, executability_critic in=6495/out=8103/reasoning=3898, chair in=22114/out=45899/reasoning=24869; ledger `cost_state=provider_usage_reported`, `outcome=completed` for every reservation.
- Chair produced the complete revised final specification (42 aggregated findings, per-decision scopes, subsection anchors); diversity `multi_role_single_model`; deterministic final validation passed and the durable `completed` publication event was written.

Live smoke forced hardening that offline fixtures had missed, all now covered by the suites: optional wire fields (`needs_input_questions`, brainstorm lists, per-decision scopes) tolerated without weakening fail-closed checks; role-rubric category vocabularies added to prompts and shared schema; chair verdict/field contract strengthened after a real `accepted_with_revisions` reply was correctly rejected; per-role dispatch artifact directories no longer collide; chat `prompt_tokens/completion_tokens` usage vocabulary mapped; balanced single-JSON extraction with control-character repair (strict rejection preserved for ambiguous payloads); configurable `review.council.request_timeout_seconds` (60–900, default 300) after a real chair response exceeded 120 s.

Adjacent suites rerun after the changes: `Test-TaskExecutionProfile.ps1` (130 checks), `Test-ReviewReliability.ps1` (127 checks), `Test-TaskHardening.ps1` (24 checks), `Test-TaskLifecycle.ps1` (51 checks) — all pass.

Environment notes (unchanged, pre-existing, reproduced on a pristine HEAD worktree without this change):

- `Test-TaskPublication.ps1` fails with "Publication tree lost additions/deletions or gained files" both with and without the remediation changes; environment-level failure outside this change's scope.
- `scripts/Test-BSLFlowPackage.ps1` therefore stops at that suite; all suites it runs before it (including every council suite) pass in the same run.
- 1C runtime/YAxUnit/Vanessa/UI: not applicable to this controller/provider-only change.

## Acceptance conclusion

The fourth-review remediation closes both remaining defects. The packaged managed `current_agent` fallback now produces controller-observed provenance from the host's own rollout session file (`model`/`effort` resolved by the provider, verified against a real `codex exec` rollout), and the positive public tokenless managed scenario completes four fresh contexts end-to-end with envelopes sourced from the terminal receipt. Budget admission and reservation are a single atomic ledger transaction under one mutex hold, with a concurrent barrier test proving that over-limit roles cannot collectively oversubscribe. Together with the previously verified offline contracts (full binding with drift tests, optional failure visibility, unknown-dispatch blocking, resume reuse without double-counting) and the authorized live DeepSeek smoke (real provider receipts, sanitized usage, deterministic gate PASS), all acceptance criteria that are verifiable in this environment are met.
