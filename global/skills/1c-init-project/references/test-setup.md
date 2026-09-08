# First-use test setup from local downloads

For the repeated local-workstation scenario, `Enable-BSLFlowWorkstationProfile.ps1` records an explicit allowlist of FILE development bases and shared catalogs outside Git. `Initialize-1CTestEnvironment.ps1` requires an exact allowlisted target, verifies that `1Cv8.1CD` exists and rejects reparse targets, assigns development DB as test DB, generates a project-local ignored runtime file, and records provider states. The profile stores no credentials and does not claim to detect replacement of an ordinary database file at the same allowed path. It does not waive the temporary Unica/runtime restriction.

Provider states are `not_configured`, `files_found`, `installed`, `pilot_passed`/`ready`, or `blocked`. `enabled: true` is derived only from a real engine pilot; UI additionally requires an observed TestClient connection. Current offline setup intentionally stops at `files_found` or `blocked` when installed-state discovery/runtime is unavailable.

On a new workstation, absence of the configured catalogs or matching artifacts does not fail framework installation and does not trigger an internet download. Project test setup records `not_configured` with an actionable next step. If a requirement needs that provider, verification remains `BLOCKED` until an exact local release is selected, its compatibility is checked, and any required database installation is authorized and proven. Vanessa Automation itself is an external EPF runner, not an extension installed into every database; YAxUnit and optional `VAExtension` are separate CFE installation decisions.

Apply after project bootstrap or on first test use in an existing project. Follow the user's test environment choice, not the physical directory containing 1Cv8.1CD as an automatic source/Git root. The base may serve several projects; do not reinstall common engines per feature.

## Inspect first

1. Read project instructions, effective runtime config, selected verification levels and existing authorization. Resolve the exact FILE test target, compatible platform and local catalog paths. Discovery is deterministic: explicit parameters used to create the workstation profile win; when omitted, they default to `C:\YAxUnit` and `C:\vanessa-automation`; later project setup reads the recorded profile. A project `test_setup` override must be passed explicitly when creating or updating that profile. Do not recursively scan drives, the user profile, package-manager caches or neighboring directories. These paths are preferences, not proof of trust or permissions.
2. Run the sibling helper `scripts/Get-1CTestTooling.ps1` with the explicit absolute directories and optional `-TestDatabasePath`. It reports each effective `catalog_path`, whether the directory exists, its exact filename pattern and `top_level_only` scope. It only inventories top-level named binaries, sizes and hashes; it does not connect, launch 1C, write configs or install. A missing directory or absent matching artifact remains `missing`/`not_configured`; it is not created and does not trigger a fallback search. Its `file_found_unverified` is not a valid/compatible binary or installed extension verdict. Multiple candidates require deliberate version selection, never newest-filename guessing. `Smoke*.cfe` is not the YAxUnit engine and is not installed automatically.
3. Obtain a fresh list of the target's real installed extensions, internal names, versions, active/applicability state and needed security properties using an available documented read-only route. Exported source trees and the bootstrap sentinel are not runtime evidence. If no supported read-only route exists, request the extension list/properties from Configurator, or explicitly agree a bounded setup UI inspection. This is setup, not permission to replay tests by computer-use.
4. Never treat `unknown` as `absent`. In the inspected public Unica contract `operation=extensions` synchronizes configured properties and is MUTATING; it is not a list API. Do not call it for discovery, invent `mode=list`, or shell around the public contract. A tool-access gap is a concrete blocker for unattended installation, not a reason for a blind CFE load.

## Install only what is missing and needed

After target, credentials, recoverable backup/current state, external-effect isolation and authority are clear, the agent should carry out supported missing-component installation without asking again for each already authorized step. An earlier blanket approval for ordinary testing does not silently authorize disabling security protection or overwriting a differently versioned extension. Get one explicit approval covering the needed installs and scoped security changes when not already granted; never infer it from a YAML flag.

| Component | Action |
| --- | --- |
| YAxUnit | For required unit/integration, add the chosen engine CFE when proven absent. Tests remain project-owned, not changes to engine internals. |
| Vanessa | Keep EPF/dependencies in the shared local catalog. Set `tools.va.epf_path` in the project's existing local runtime overlay. Do not import an EPF as a configuration extension. |
| VAExtension | Add the chosen CFE to the test-client base when selected scenarios/MCP profile require it. Do not overwrite a compatible existing copy. |
| client_mcp | Optional, only for explicitly selected interactive MCP work. Verify actual internal identity, endpoint/tools and compatibility; filename is not the identity. Do not expose it to the read-only spec reviewer. |

Use the installed Unica runtime skill and current public schema. For CFE loading the inspected public route is `unica.runtime.execute`, `operation=load`, `mode=load`, exact `path` and verified internal `extension` name; preview with `dryRun=true`, then apply only with authorization. Do not invent an internal name from a release filename. Runtime load can change the database schema and has non-interruptible phases. If the public route rejects an operation, report it; do not use internal build calls, raw platform commands or a job as a bypass. If Unica is absent, use only an existing approved project install route, or describe the documented manual steps.

Read the selected release's requirements for safe mode/unsafe-action protection. Explain and authorize any necessary weakening for that one test extension only. Never mass-change security flags, delete extensions, restore DT, replace a working base, or close unrelated sessions. Existing compatible version => no-op; different/modified version => compare and ask before update; failed operation => inspect actual resulting state before deciding a retry, no unconditional reload loop.

## Verify, then reuse

Re-read real extension state after installation and start a fresh test session if needed. Set `verification.*.enabled: true` only for providers actually shown usable; neither a CFE load exit nor zero discovered tests is a successful pilot. Run one project-owned minimal YaXUnit test and, if UI is in scope, one persisted relevant Vanessa feature using known fixtures. Record target/composition/source revision, actual selection/counts/failures/skips and durable evidence. Do not automatically run a vendor smoke extension, all library tests, or business posting scenarios merely to prove installation.

Keep a short setup result in existing ignored project reports (or response), with per-component `missing`, `unknown`, `installed_not_verified`, `ready` or `blocked` and supporting evidence. No mandatory new global registry or readiness sentinel. Recheck when database, extension composition, engine version, platform or baseline changes. A repeat project on the same base inspects/reuses compatible components; it does not reinstall them. Missing optional UI/MCP tooling does not block a task that only requires unit/static checks.

## Persist an authorized interactive pilot

When a supported unattended route is unavailable but the user explicitly authorizes a bounded Configurator/Enterprise pilot, do not leave `.bsl-flow/reports/test-setup/current.json` as a stale pre-pilot snapshot. Record each completed YAxUnit or Vanessa engine run in a small observation JSON, then call:

```powershell
& "$env:USERPROFILE\.agents\skills\1c-init-project\scripts\Save-1CInteractiveTestPilot.ps1" `
  -SetupReportPath ".bsl-flow\reports\test-setup\current.json" `
  -ObservationPath ".bsl-flow\local\pilot-yaxunit.json" `
  -RunId "yaxunit-20260908-01"
```

The observation is data, not an instruction, and uses this closed shape:

```json
{
  "schema_version": 1,
  "observed_at_utc": "2026-09-08T18:42:55Z",
  "provider": "yaxunit",
  "target": "C:\\BASES\\DEMO\\bp1",
  "runner_version": "25.12",
  "selection": ["BP1T_Пилот.СложениеДвухЧисел"],
  "counts": {"total": 1, "passed": 1, "failed": 0, "errors": 0, "skipped": 0},
  "result": "PASS",
  "installation": {"state": "installed_extension", "version": "25.12", "active": true},
  "test_client_connection_observed": false,
  "evidence": {
    "kind": "interactive_ui_observation",
    "summary": "One selected test passed in the exact target base.",
    "fresh_durable_report": false
  }
}
```

For Vanessa use `installation.state: external_runner_loaded`. Set `test_client_connection_observed: true` only when a real TestClient connection was part of the observed scenario; a local arithmetic feature must leave it false.

The helper validates the exact target, result/count consistency, installation kind and evidence boundary. It writes immutable `history/<run-id>.json`, rejects a reused run ID with different input, and atomically updates `current.json`. A passing run promotes only that provider to `pilot_passed`. It deliberately leaves `automation_readiness: blocked` and `fresh_durable_report: false`; use the `1c-verify` durable-result helper when fresh JUnit/receipt evidence exists. It does not start 1C, install components, change project YAML or repeat a failed run.
