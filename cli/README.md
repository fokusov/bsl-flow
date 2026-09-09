# BSL Flow Windows CLI host

This Go executable embeds the versioned `global/` bundle and calls its existing
PowerShell controller. It does not implement a second task state machine.
Windows PowerShell 5.1, Git and the configured Codex/OpenCode executables remain
external runtime dependencies. No dependency or toolchain is downloaded by the build.

Build from the repository root with Go 1.22 or newer and PowerShell 7:

```powershell
.\scripts\Build-BSLFlowCli.ps1 -Test
.\scripts\Test-BSLFlowCli.ps1
.\cli\bin\bsl-flow.exe version
.\cli\bin\bsl-flow.exe task start --project C:\DEV\Example --input C:\DEV\request.json
.\cli\bin\bsl-flow.exe task run --project C:\DEV\Example --task <uuid>
```

`-GoPath` selects an already installed compiler. The first build stages `global/`
and `VERSION` into generated `internal/resources/bundle.zip` and `version.txt`.
Changes to controller instructions require a rebuild. `-Test` runs Go tests and
compares two native builds from the same staged snapshot. The executable and its
SHA-256 sidecar are written to `cli/bin`; generated Go caches stay in `cli/.cache`.
Native smoke tests retain their isolated evidence under `work/cli-smoke-*`; they
register and cancel source-only tasks without model calls or 1C execution.

Public commands are `help`, `version`, `task start/status/next/run/update/resume/
cancel/record/accept/deliver`, and `runner run`. `task deliver` requires `--project`
and `--task`; `runner run` requires `--project` and `--input`, accepts optional
`--codex`, and does not accept `--task`. Both call the same embedded entrypoint
(`Deliver` and `Serve`) without adding host-side delivery or scheduling logic.
UUID arguments must be lowercase. Options are separate argv pairs, with no `--name=value`
syntax. Unknown, repeated and action-inapplicable options are errors. Task commands
relay controller JSON and exit codes unchanged. Host input errors use code 2 and
host/bundle failures use code 11, both with a controller-shaped JSON envelope.

The bundle cache is `%LOCALAPPDATA%\BSLFlow\bundles\<version>-<bundle-sha256>`.
Only local drive paths without reparse points are accepted. An exclusive Windows
file handle serializes extraction; a busy cache returns a blocker instead of
starting another extraction. Files are checked before atomic directory publication
and the full inventory is checked again on every task invocation. Modified,
missing, additional files and directories block execution; the host does not
silently repair a modified cache. A crashed unpublished extraction is never run.

The host obtains the system PowerShell directory from Windows, invokes the fixed
embedded `Invoke-BSLFlowTask.ps1` using `-File`, and supplies its own absolute path
as `BSL_FLOW_HOST_PATH`. The controller includes that executable in policy identity
and enables UTF-8 output for the host. The user-supplied identity environment value
is replaced. The binary remains part of trusted operator code, not a security
boundary against an administrator or another unrestricted process running as the
same user. Worker sandboxing and controller-owned evidence remain authoritative.

Ctrl+C is not rollback. Use the exact task ID with `status`, `cancel` and `resume`
to apply controller recovery rules. The host does not retry, authorize operations,
waive gates, publish changes or lift any 1C runtime restriction.
