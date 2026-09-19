# Changelog

## 2026-09-16 — Three re-anchored specs implemented in PowerShell

- `user-profile-council-config`: the optional user profile config (`%USERPROFILE%\.bsl-flow\config.yaml` / `$HOME/.bsl-flow/config.yaml`, override `BSL_FLOW_USER_CONFIG`) is now a base layer under the project config with field-level merge inside named providers/model profiles/role bindings, a fail-closed allowlist (file+key named on rejection), and the policy hash computed over a canonical JSON form of the effective policy when a profile or overlay contributes (raw-file hash preserved otherwise). The council review republished the spec (REVISE consumed); the implementation matches the published final (55-check suite, council suites green).
- `execution-contract-v01`: optional machine-readable change artifacts (`contract.yaml`, `execution.yaml`, `verification.yaml`) with a deterministic PowerShell lint (`Invoke-1CSpecContractLint.ps1`, same `-ChangePath` contract as `Test-1CSpec.ps1`) and skill-level executor helpers (`1c-implement/scripts/ExecutionGraph.ps1`: topological order, kind/mutation/scope permissions, `evidence/T-NNN.json`, rebuildable `state.json`, BLOCKED rule enforced in code). Independent review is BLOCKED at the deterministic chair gate after two attempts (`review-blocked.md`); the implementation and both suites (100+37 checks) are green.
- `repository-task-registry`: PS-native clone-local repository store (`<git common dir>/bsl-flow/tasks/<uuid>/revisions`, schema v1) with planned tasks, hash-linked `parent_hash` journals, graph-locked dependency writes, read commands `list/show/history/overview`, `archive/unarchive`, staged-BLOCKED `activate` (trusted request schema v1, `controller_contract: repository-store-aware/v1`), legacy read-only discovery with verified-history dedupe/conflict diagnostics, and a thin `bsl-flow task <subcommand>` wrapper. The council review republished the spec with normative JSON schemas (REVISE, 14/17 findings accepted); the implementation was aligned to the published final (289 + 34 concurrency checks).

## Unreleased

- Made self-learning memory a project opt-in (`features.self_learning_memory.enabled: false` by default): disabled tasks neither read nor write the Experience Ledger, while mandatory managed journals, context and recovery remain unchanged. Execution contracts, estimation, the planned-task registry and publication remain separate explicit extensions.
- Added tiered specification-review routing: S defaults to deterministic lint, M uses one isolated reviewer, and L/high-risk requires the API Council with fail-closed behavior instead of a single-reviewer downgrade.
- Documented the Core/Managed boundary: assisted skills remain the bootstrap default; Managed starts only for an explicitly requested `1c-task` run. Installation, project config and sentinel files do not activate Managed or authorize registry activation, estimation, publication, merge, push, deploy or runtime access.
- Consolidated parity harness (req 20–21): a frozen-trace format with classified divergences (schema vs behavior change, load-bearing approved annotations) replaces boolean diffs; shadow mode drives only the allow-listed read/decision paths (spec lint/final, runner decide, memory projection) over frozen inputs with no writes, processes or model calls. Five PowerShell traces are frozen with capture provenance.
- Post-rollback packaging evidence (Windows scope): the deterministic PowerShell/.NET lane builds the package ZIP, verifies its manifest after extraction and runs the offline package suite from the extracted artifact. The current release has no native executable or no-pwsh claim; Linux/macOS support remains out of scope.
- Council dispatch hardening (field report): the live dispatcher travels as a per-runspace sentinel across parallel thread-jobs, the council review assertion sources its engine dependency, and run metrics map council schema v2 reviews to the common record shape.

## 0.8.0-dev.3

- `spec review` also persists `spec-lint.json` like the legacy single-reviewer script route.
- Spec lint diagnostics point at the broken scenario's own GIVEN occurrence and at each selected verification item instead of the section heading, and repeated identical "line N: message" pairs are suppressed — ported to both the Go validator and `Test-1CSpec.ps1` with the parity crosscheck kept green.
- 1c-estimate: the `## Расхождение с якорем` section is cross-checked against the validator's own computation — every divergent boundary must be named with its computed percent and stated percents must match; request flags `external_artifact` (AI anchor ×2.0) and `posting` (×1.5) deterministically raise the AI anchor before divergence, with the effective anchor and applied flags recorded in the validator result.

## 0.8.0-dev.2

- Controller-owned native FILE extension execution with exact platform/source/target binding, original JUnit, private credential input, durable process intent and control-read recovery. Test-only continuation reuses a proven prior load without repeating database writes.
- Trusted requirement-to-criterion mapping and independent test sufficiency review, bound to protected test files and accepted evidence. Real native and coverage pilots are documented separately.
- Recover queue notifications from the complete durable journal; reject malformed/torn records and prevent repeated execution after a recorded dispatch error.
- Separate accepted-source publication commands with explicit remote/ref authorization, deterministic Git objects, create-only branches and read-only recovery after uncertain push. Publication integration status is tracked in the changelog history.
- Documented architecture decisions and verification boundaries; arbitrary business, UI, EPF and production acceptance remain environment-specific.

## 0.8.0-dev.1

- Fix Windows CI dependencies with pinned OpenSpec and an isolated package schema; preserve the historical Windows PowerShell 5.1 encoding regression evidence.
- Go executable with embedded versioned instructions/engine, strict task CLI, verified cache and host identity binding. PowerShell 7 from the standard machine installation `C:\Program Files\PowerShell\7\pwsh.exe`, Git and the model provider remain external dependencies; PS5.1 fallback is not supported. (Removed by the 2026-09-16 rollback of `native-cross-platform-cli`.)
- Opt-in bounded source failure diagnosis/repair with frozen declared test inputs, exact failed evidence, fresh independent code review and verification. Defaults preserve fail-stop behavior.
- Local supervisor for explicitly registered tasks and immutable accepted-source handoff; no implicit startup installation, push, deployment or database authority.
- JUnit aggregate consistency and read-only diagnosis recovery checks; standalone CLI and updated package verification.
- Documented remaining 1C runtime, test-environment, business-coverage and applied-delivery gates. This development version does not claim autonomous 1C runtime acceptance.
- Validated a separately authorized native 1C pilot with two passing YAxUnit cases and retained original JUnit; recorded failed attempts, language-binding and internal-UUID corrections. The local pilot wrapper is not the public managed runtime adapter.

## 0.7.0-dev.1

- Add the managed `1c-task` package surface and deterministic task lifecycle contracts.
- Harden specification review and test-evidence freshness gates.
- Install and inventory seven managed skills with isolated installer regression coverage.
- Add an offline default package suite and a reproducible ZIP build with a SHA-256 manifest.
- Validate installed managed S and M source-only pilots, including review reconciliation, code review and sandboxed source verification; the 1C runtime gate remains blocked.

This development version has not completed the M8 end-to-end pilot and is not a final managed-SDLC release.
