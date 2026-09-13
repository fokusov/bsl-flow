# OpenCode capability spike

This is a bounded discovery result for BSL Flow integration on 2026-09-10. It
does not authorize OpenCode to mutate a 1C base, use MCP, or run a paid model.

The installed `C:\Users\ifokusov\.bun\bin\opencode.exe` is version **1.18.30**.
Its `run` command accepts `--agent`, `--model`, `--variant`, `--file`, `--dir`,
`--pure`, and `--format json`. The latter is documented by the CLI as raw JSON
events. The CLI help has no stable event schema, JSON Schema output option, or
per-run usage-event contract. BSL Flow must treat its response as untrusted JSONL,
retain the complete stdout/stderr stream, and fail closed on an unknown terminal,
error, or usage shape.

A separately authorized real no-tools call with `deepseek/deepseek-v4-flash`
captured `step_start`, `text` (`part.text`), and `step_finish` with reason `stop`,
consistent session/message IDs, tokens and cost. The scoped transport reader
and its regression fixtures now validate that sequence. Raw evidence is in
`work/benchmark-integration/transport-1`; the measured cost and limits are in
[the integration report](BENCHMARK_INTEGRATIONS_RU.md). This does not validate
multi-step tool calls, failures, or the full managed host. Unknown event shapes
remain blocked; absent usage is not zero.

## What configuration isolation does and does not provide

OpenCode's official configuration documentation says that configuration sources
are merged, and `OPENCODE_CONFIG` is loaded between global and project sources.
The official permissions documentation describes tool approvals, not operating
system sandboxing. Our `debug agent` probe confirmed both boundaries:

- `OPENCODE_CONFIG=<fixture>`, `OPENCODE_DISABLE_PROJECT_CONFIG=1`,
  `OPENCODE_DISABLE_CLAUDE_CODE=1`, and `--pure` still retained global
  external-skill allowances.
- Moving the configuration and state roots to a disposable workspace fixture via
  `XDG_CONFIG_HOME`, `XDG_DATA_HOME`, `XDG_CACHE_HOME`, `TEMP`, and `TMP`, then
  adding `OPENCODE_DISABLE_EXTERNAL_SKILLS=1`, removed those global skill grants
  from the resolved agent. `OPENCODE_CONFIG_DIR` did not do so.

For a future process wrapper, construct a fresh per-attempt directory outside the
source worktree and run a configuration diagnostic first:

```text
XDG_CONFIG_HOME=<attempt>\xdg-config
XDG_DATA_HOME=<attempt>\xdg-data
XDG_CACHE_HOME=<attempt>\xdg-cache
TEMP=<attempt>\tmp
TMP=<attempt>\tmp
OPENCODE_DISABLE_PROJECT_CONFIG=1
OPENCODE_DISABLE_CLAUDE_CODE=1
OPENCODE_DISABLE_EXTERNAL_SKILLS=1
opencode debug agent <packaged-agent> --pure
```

The controller must parse that diagnostic and reject an agent that has an enabled
MCP/plugin path, a non-denied edit/bash/task/web/skill permission, or an allowed
external directory outside the attempt and source roots. This preflight reduces
configuration drift; it does not isolate the child process from the host.

`OPENCODE_DISABLE_EXTERNAL_SKILLS=1` does not suppress explicit `skills.paths`:
a synthetic in-attempt skill path remained in the resolved agent while global
`.agents` paths disappeared. The renderer may use that mechanism only for an
absolute path under the controller-created attempt directory and must validate
the resolved path. It must not point at a project path, shared installation, or
user-profile directory.

## Current route decision

The initial native Codex sandbox probe proved source write and sibling-write denial
with a broad `:root=read` profile, and accepts `network={enabled=true}` in its
profile syntax. That probe did **not** prove restrictive reads: an unelevated profile
without `:root` fails with `Restricted read-only access requires the elevated
Windows sandbox backend`; the elevated backend initially did not produce a usable receipt.

Therefore BSL Flow must not launch OpenCode directly or treat OpenCode
permissions as OS isolation. It must also not expose MCP to this route.

The minimal implementable route is gated rather than enabled now:

1. A host capability sentinel must first prove an elevated profile which allows
   provider networking, source/attempt access, and denies controller/reference
   roots for both read and write, including a child-process sentinel test.
2. Only after that proof may a controller launch OpenCode as a sandboxed child
   with the disposable XDG configuration above, `--pure`, no MCP configuration,
   a packaged hard-deny agent, `run --format json`, bounded stdin/stdout/stderr,
   timeout/cancellation ownership, and immutable raw evidence.
3. A deterministic controller validates the candidate structured object and its
   source/hash gates. OpenCode's final text, claimed permissions, and usage are
   evidence inputs, never acceptance by themselves.

Update after authorized native setup: `windowsSandbox/setupStart` with an
explicit managed profile completed successfully. The synthetic parent/child
PowerShell probe now proves source writes and denied reference/controller reads
and controller writes. Evidence: `work/sandbox-setup/isolation-parent-child`.
Provider networking and real OpenCode/MCP child execution still require their
own checks. The full route remains unverified; the supported managed adapter
remains native Codex as documented in `docs/managed-host-contract.md`.

## Sources

- [OpenCode CLI documentation](https://dev.opencode.ai/docs/cli/)
- [OpenCode configuration documentation](https://dev.opencode.ai/docs/config)
- [OpenCode agent permissions](https://dev.opencode.ai/docs/permissions/)
- [OpenCode configuration loader source](https://github.com/anomalyco/opencode/blob/dev/packages/opencode/src/config/config.ts)
- [Local evidence](../work/benchmark-integration/opencode-spike/EVIDENCE.md)
