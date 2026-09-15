# Changelog

## 0.8.0-dev.3

- Native `spec lint`, `spec final` and `spec review` write the change-directory sidecars (`spec-lint.json`, `final-validation.json`) in the exact shapes the PowerShell validators publish, so the estimate gate and finalization run without PowerShell (BF-1/BF-2 of the 2026-09-14 field report).
- `spec review` also persists `spec-lint.json` like the legacy single-reviewer script route.
- Spec lint diagnostics point at the broken scenario's own GIVEN occurrence and at each selected verification item instead of the section heading, and repeated identical "line N: message" pairs are suppressed — ported to both the Go validator and `Test-1CSpec.ps1` with the parity crosscheck kept green.
- CLI conventions: `--version` prints the version document, `--help`/`-h` exit 0, and every usage error names the full `spec <lint|final|review|metric>` group; `help` lists the `spec` commands in the command list and describes `capability`.
- 1c-estimate: the `## Расхождение с якорем` section is cross-checked against the validator's own computation — every divergent boundary must be named with its computed percent and stated percents must match; request flags `external_artifact` (AI anchor ×2.0) and `posting` (×1.5) deterministically raise the AI anchor before divergence, with the effective anchor and applied flags recorded in the validator result.

## 0.8.0-dev.2

- Controller-owned native FILE extension execution with exact platform/source/target binding, original JUnit, private credential input, durable process intent and control-read recovery. Test-only continuation reuses a proven prior load without repeating database writes.
- Trusted requirement-to-criterion mapping and independent test sufficiency review, bound to protected test files and accepted evidence. Real native and coverage pilots are documented separately.
- Recover queue notifications from the complete durable journal; reject malformed/torn records and prevent repeated execution after a recorded dispatch error.
- Separate accepted-source publication commands with explicit remote/ref authorization, deterministic Git objects, create-only branches and read-only recovery after uncertain push. Publication integration status is tracked in `docs/SDLC_COMPLETION_RU.md`.
- Documented architecture decisions and verification boundaries; arbitrary business, UI, EPF and production acceptance remain environment-specific.

## 0.8.0-dev.1

- Fix Windows CI dependencies with pinned OpenSpec and an isolated package schema; preserve the historical Windows PowerShell 5.1 encoding regression evidence.
- Go executable with embedded versioned instructions/engine, strict task CLI, verified cache and host identity binding. PowerShell 7 from the standard machine installation `C:\Program Files\PowerShell\7\pwsh.exe`, Git and the model provider remain external dependencies; PS5.1 fallback is not supported.
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
