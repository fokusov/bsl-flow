# Test evidence contracts

These helpers are offline evidence gates. They do not start 1C, load a configuration, retry a run, or create business data.

## Preflight

`Test-1CTestPreflight.ps1 -RequestPath request.json -ObservedPath preview.json [-ProfilePath profile.json] [-OutputPath preflight.json]`

The request declares the runner, `operation: test`, exact FILE target, source-set/extension identities, exact test selection, relevant versions, durable `reportPath`, required route `capabilities`, and an explicit JSON boolean `noBuild`. The observed file is a saved preview/receipt from the selected public route and must contain an `effective` object with target, sources, selection, versions, report path and capabilities; copied request fields at the envelope root are not treated as effective state. The result compares declared and observed values and reports `PASS` only when the route, target, sources, selection, versions, schema and no-build evidence are present. A test-module filter is not no-build evidence; an observed build step must be explicitly skipped or the effective receipt must include a structured no-build step. Missing observed source/version/profile evidence is a blocker, not an inference. `launch_performed` is always false.

This is a local evidence manifest contract, not the public Unica request format and not a promise that every runner emits these fields. Preserve the original response separately. Populate effective values only from explicit tool/configuration/version evidence, with source references alongside the manifest; never copy desired values as observed facts. Unsupported raw output stays BLOCKED. The helper does not interrogate installed tools, launch the runner, grant permission or implement a no-build workaround. A PASS is only consistency of supplied evidence, not independent attestation of the machine or permission to repeat a prior applied operation.

Minimal declaration/observation shape (paths and versions are examples, not a public runner request):

`request.json`:

```json
{"runner":"v8-runner","operation":"test","schema":"v8-runner-command-envelope","target":"C:\\test\\bp1","sources":["BP1Tests"],"selection":["Pilot.Add"],"versions":{"platform":"8.3.27.2074","extension":"1.0.0.1"},"noBuild":true,"reportPath":"reports\\pilot.json","capabilities":["test_no_build","durable_report"]}
```

`preview.json`:

```json
{"ok":true,"runner":"v8-runner","schema":"v8-runner-command-envelope","command":"test","phase":"preview","steps":[{"name":"build","status":"skipped","message":"--no-build"}],"effective":{"target":"C:\\test\\bp1","sources":["BP1Tests"],"selection":["Pilot.Add"],"versions":{"extension":"1.0.0.1","platform":"8.3.27.2074"},"reportPath":"reports\\pilot.json","capabilities":["test_no_build","durable_report"]}}
```

An applied/error envelope (`ok: false`), missing raw runner evidence, or an unknown `phase` remains `BLOCKED`; a preview can only establish route admission. `expected.json` for durable saving additionally requires `started_at_utc` (or `-RunStartedAtUtc`) so a fresh report cannot be claimed from an unknown run.

## Durable result

`Save-1CTestResult.ps1 -ReceiptPath receipt.json -JUnitPath junit.xml -ExpectedPath expected.json -RunId <id> -HistoryDirectory .bsl-flow/reports/tests/history -SummaryPath .bsl-flow/reports/tests/current.json`

`expected.json` must contain a non-empty `selection` (or `tests`), positive `total`, exact `target`, `sources` and `versions` (the selection count is used when total is omitted). Receipt counts and JUnit counts are checked against the expected total and each other; failed, errored, skipped, zero, stale, malformed, missing or mismatched evidence cannot become `PASS`. A parsed runner receipt is not the original JUnit: when the runner has deleted its temporary `report.xml`, the attempt records `junit_status: missing_or_deleted`, preserves the receipt if available, and remains `BLOCKED`.

Each attempt is written once to `history/<run-id>.json`; an existing ID is never overwritten. Raw receipt/JUnit files are copied, when present, to `raw/<run-id>/` with no-clobber semantics. The attempt records source paths and hashes, expected/observed target, selection and counts, timestamps, failure state and a safe read-only next action. The current summary is regenerated from all history and chooses the newest observed completion timestamp, so a late old attempt cannot replace a newer result.

Failure states are explicit: `not_started`, `not_applied`, `applied_followup_failed`, `business_record_written_verification_incomplete`, or `unknown`. The default next action is state inspection and preservation of evidence; no automatic build/load/retry or document creation is performed.

`-PostFailureState` is a caller declaration, explicitly marked as not independently verified. Keep the actual read-only state check next to the attempt; a missing receipt never implies `not_started`. Case IDs are `classname.name` when classname exists, otherwise `name`, matching the JUnit parser rather than a module-only filter. Pass `-SourceManifestPath` to preserve a previously captured source-hash manifest; otherwise the hash is null with an explicit reason. A version string alone does not prove source byte identity.

## Interactive engine pilot

An explicitly authorized Configurator/Enterprise smoke can prove that one exact selection ran in one exact target even when the runner cannot emit a supported receipt. Persist that limited result with the sibling `1c-init-project/scripts/Save-1CInteractiveTestPilot.ps1` contract documented in its [test-setup reference](../../1c-init-project/references/test-setup.md). The helper updates `.bsl-flow/reports/test-setup/current.json` and immutable setup history; it never creates a durable test PASS.

Keep these verdicts separate:

- YAxUnit interactive PASS proves the selected engine test ran; unattended execution remains unproven.
- Vanessa scenario-runner PASS proves the selected feature ran; `test_client_connection_observed: false` does not prove UI/TestClient readiness.
- A pre-existing or older JUnit is rejected for the current run. Use `Save-1CTestResult.ps1` only with a fresh original report/receipt and exact expected selection.
- The interactive helper records caller-observed UI facts and performs no runtime action. It cannot authorize a load, protection change, retry or business write.

## Extension identities

`Test-ExtensionIdentities.ps1 -ProjectRoot <root> -SourceRoots <relative-root>...`

The check reads selected extension XML, excludes generated `ConfigDumpInfo.xml`, requires at least one selected metadata file, and verifies owned metadata `uuid` values are unique across all selected roots. It never changes UUIDs or any source file. Zero selected files and duplicate IDs produce a non-PASS result. UUIDs used only as references/borrowed mappings are not rewritten by this helper; identity correction belongs to an explicitly authorised source operation.

## Evidence boundaries

Static/package tests, preview, receipt parsing and JUnit parsing are separate from runtime acceptance. A zero process exit, successful build, transport-success envelope or a filtered module does not prove the requested test run. Keep the original project path and source/extension version evidence alongside the durable attempt, without passwords or client data.
