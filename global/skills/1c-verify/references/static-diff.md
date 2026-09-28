# Static diff gate (BSL Language Server)

`Invoke-1CStaticDiff.ps1` reports only diagnostics that are NEW relative to a baseline, so
pre-existing (legacy) BSL Language Server findings never expand the scope of a change. Run it
after the diff is final and before verification is accepted, for any change that touches
`.bsl`/`.os` files.

## Parameters

- `-ProjectPath` — repository root (default: current directory). Must be a git repository.
- `-BaseRef` — comparison point for the diff (default `HEAD`).
- `-SourcePath` — source root to scope the diff to (default: first entry of `source.paths` in
  `bsl-flow.yaml`, else `src`).
- `-BslLsCommand` — explicit path to a `bsl-language-server.jar` or native executable. Discovery
  order otherwise: `-BslLsCommand`, env var `BSL_FLOW_BSLLS`, `bsl-language-server` on `PATH`,
  `bslls_path` in `~/.bsl-flow/workstation.json`.
- `-BaselineReport` / `-CurrentReport` — pre-computed BSL LS JSON-reporter reports. Use these in
  tests, or when you ran BSL LS yourself; either can be supplied independently of the other.
- `-Required` — treat a missing tool as blocking rather than a soft skip.
- `-OutputPath` — where the verdict JSON is written (default `.bsl-flow/reports/static/<ts>.json`).

## Verdict semantics

`verdict` is one of `PASS`, `FAIL`, `BLOCKED`, `NOT_RUN`.

- No changed `.bsl`/`.os` files under scope → `PASS`, empty `new`.
- Any NEW diagnostic with `severity: Error` → `FAIL`.
- New diagnostics of lower severity are listed under `new` but do not fail the gate.
- Tool not found and no reports supplied: `-Required` → `BLOCKED` (exit 11); otherwise `NOT_RUN`
  (exit 0), reason `bslls_not_found`.
- Exit codes: `0` PASS/NOT_RUN, `1` FAIL, `11` BLOCKED.

Diagnostics are matched by `(file, code, normalized message, hash of the source line ± the next
line)`, not by line number, so a line shift from unrelated edits does not manufacture a new
finding. A project `.bsl-language-server.json` is honored automatically when present. Baseline
reports are cached under `.bsl-flow/cache/bslls/<commit>-<file-list-hash>.json`.

## Installing BSL Language Server

Download a release jar from https://github.com/1c-syntax/bsl-language-server/releases and either
put its path in `BSL_FLOW_BSLLS`, add a native build to `PATH`, or record `bslls_path` in
`~/.bsl-flow/workstation.json`.
