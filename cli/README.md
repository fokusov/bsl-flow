# BSL Flow Windows CLI host

This Go executable owns the clone-local registry and native task controller.
It embeds the versioned `global/` bundle: native tasks use a stateless Windows
PowerShell provider, while existing checkout-local tasks use the legacy controller.
PowerShell 7, Git and the configured Codex/OpenCode executables remain
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

Public commands are `help`, `version`, `task start/status/next/context/run/update/resume/
cancel/record/accept/deliver/publish/publish-resume`, and `runner run`. `task deliver` requires `--project`
and `--task`; `runner run` requires `--project` and `--input`, accepts optional
`--codex`, and does not accept `--task`. Both call the same embedded entrypoint
(`Deliver` and `Serve`) without adding host-side delivery or scheduling logic.
UUID arguments must be lowercase. Options are separate argv pairs, with no `--name=value`
syntax. Unknown, repeated and action-inapplicable options are errors. Task commands
relay controller JSON and exit codes unchanged. Host input errors use code 2 and
host/bundle failures use code 11, both with a controller-shaped JSON envelope.

The native clone-local task registry provides planned metadata and read projections:

```powershell
.\cli\bin\bsl-flow.exe task create --project C:\DEV\Example --input C:\DEV\task.json
.\cli\bin\bsl-flow.exe task edit --project C:\DEV\Example --task <uuid> --expected-revision 1 --input C:\DEV\task-patch.json
.\cli\bin\bsl-flow.exe task list --project C:\DEV\Example
.\cli\bin\bsl-flow.exe task show --project C:\DEV\Example --task <uuid>
.\cli\bin\bsl-flow.exe task history --project C:\DEV\Example --task <uuid>
.\cli\bin\bsl-flow.exe task overview --project C:\DEV\Example
.\cli\bin\bsl-flow.exe task archive --project C:\DEV\Example --task <uuid> --expected-revision 2
.\cli\bin\bsl-flow.exe task unarchive --project C:\DEV\Example --task <uuid> --expected-revision 3
```

Registry reads default to versioned JSON; add `--human` for tables and details. The
default list returns at most 100 non-archived tasks and supplies `next_cursor` for
the next page. `overview` scans the complete filtered repository scope and is not
limited by the list page size. The store is rooted at the verified Git common dir,
so all worktrees of one clone see the same tasks while independent clones remain
separate.

`task activate` binds a planned task to a full trusted execution request. It preserves
the UUID and metadata and appends to the same canonical journal. The Windows
provider supports source-only execution; unsupported runtime criteria block activation.
Activation requires the exact current worktree, expected revision, clean baseline
and verified provider capability:

```powershell
.\cli\bin\bsl-flow.exe task activate --project C:\DEV\Example --task <uuid> --expected-revision 1 --input C:\DEV\request.json
```

Checkout-local journals are discoverable as legacy input. Transfer is explicit:

```powershell
.\cli\bin\bsl-flow.exe task adopt --project C:\DEV\Example --source C:\DEV\LegacyWorktree --task <uuid> --preview > C:\DEV\adoption-plan.json
.\cli\bin\bsl-flow.exe task adopt --project C:\DEV\Example --task <uuid> --apply --input C:\DEV\adoption-plan.json
.\cli\bin\bsl-flow.exe task rebind --project C:\DEV\Example --task <uuid> --expected-revision <n> --input C:\DEV\request.json
```

Preview reads only. Apply preserves revision bytes and copied evidence, appends a
canonical continuation and leaves the originals intact. A changed source, conflicting
copy or unresolved operation blocks transfer. Repeating the same committed plan
does not add a revision. Imported status is historical: explicit rebind with fresh
inputs is required before execution or acceptance. Packaged legacy writers refuse
an already canonical UUID. Older binaries can still alter their own files; a later
lineage check detects that drift and blocks native continuation.

Task identity selects the controller. A canonical UUID, including a damaged target,
never falls back to legacy after an error. Registry reads do not load the provider.
Legacy rows expose `next_action: "unknown"` because a
read-only projection cannot safely reproduce the controller's `Get-BFNext` gate;
the value is informational and authorizes no operation.

`task context --project <path> --task <uuid>` is a read-only projection of the
controller journal and `Get-BFNext`. It reports the next action, blocker,
question, unknown effect, evidence freshness and the hashes it was generated
from; it writes nothing and authorizes nothing. A project may supply an optional
`docs/architecture/adr-index.json` (with `docs/architecture/adr-index.schema.json`
and the normative `docs/ARCHITECTURE_RU.md`); when absent the projection reports
`missing_context` instead of failing. The embedded bundle intentionally carries
only `global/` and `VERSION`, so a project index is resolved from the project
root.

The bundle cache is `%LOCALAPPDATA%\BSLFlow\bundles\<version>-<bundle-sha256>`.
Only local drive paths without reparse points are accepted. An exclusive Windows
file handle serializes extraction; a busy cache returns a blocker instead of
starting another extraction. Files are checked before atomic directory publication
and the full inventory is checked again on every task invocation. Modified,
missing, additional files and directories block execution; the host does not
silently repair a modified cache. A crashed unpublished extraction is never run.

The host obtains Program Files from the Windows Shell standard-folder API and
requires the machine installation `PowerShell\7\pwsh.exe` beneath it. It does not
search PATH or fall back to Windows PowerShell 5.1; portable and per-user installs
are not selected. For legacy tasks, the host invokes the fixed
embedded `Invoke-BSLFlowTask.ps1` using `-File`, and supplies its own absolute path
as `BSL_FLOW_HOST_PATH`. The controller includes that executable in policy identity
and enables UTF-8 output for the host. The user-supplied identity environment value
is replaced. The binary remains part of trusted operator code, not a security
boundary against an administrator or another unrestricted process running as the
same user. Native execution invokes fixed `Invoke-BFNativeProvider.ps1` with a
closed JSON document on stdin. Go retains the outer process receipt and is the
only canonical task writer. Worker sandboxing and controller-owned evidence remain authoritative.

Ctrl+C is not rollback. Use the exact task ID with `status`, `cancel` and `resume`
to apply controller recovery rules. The host does not retry, authorize operations,
waive gates or lift any 1C runtime restriction. Publication uses a separate trusted input and the controller described in [publication](../docs/PUBLICATION_RU.md).
