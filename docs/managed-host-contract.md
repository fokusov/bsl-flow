# Managed host contract: Windows Codex CLI

M0 spike, 2026-09-09. The filesystem and structured-output checks below passed on this host. This is a bounded source-only capability result, not 1C runtime acceptance or an assurance about every external tool.

## Observed host

- Native executable: `C:\Users\ifokusov\AppData\Roaming\npm\node_modules\@openai\codex\node_modules\@openai\codex-win32-x64\vendor\x86_64-pc-windows-msvc\bin\codex.exe`.
- Version: `codex-cli 0.153.0`.
- SHA-256: `0F8ED9678BCA539AA6517ADB0C8D50AD9A94FF5DF4D21E149AE700D219FFF69D`.
- The native executable avoids npm `.ps1`/`.cmd` launcher quoting and execution-policy differences.
- The outer desktop sandbox denied the CLI's own global service/temp files. The authorized probes ran with outer escalation, while their worker commands remained in the independently selected Codex sandbox. No global settings, packages, or databases were changed by the probe scripts.

## Measured checks

Raw files and reproducible scripts are in `work/host-probe/` (local ignored evidence).

| Check | Observation | Evidence |
| --- | --- | --- |
| Source-write profile | Source write allowed; controller and installed-code sibling sentinels denied | `write-direct.json` |
| Read-only profile | Source, controller, installed-code writes denied | `read-direct.json` |
| Child test process | A nested Windows PowerShell process could not overwrite either protected sentinel in both profiles | Same direct receipts |
| Real model tool call | Astra invoked PowerShell; raw command event contained allowed source write and denied protected/child writes | `exec-events.jsonl` |
| Structured final output | Final JSON matched the required three-key enum schema; native process exited 0 | `probe-schema.json`, `exec-final.json`, `exec-exit.txt` |
| Identity and usage | Thread ID and terminal usage were present in raw events | `exec-events.jsonl` |
| Cancellation | `taskkill /PID <owned-pid> /T /F` terminated the sandbox process and its three child processes; no delayed child write after 14 seconds | `cancel-direct.json` |

The protected sibling contents remained `controller-state` and `installed-original`. The installed-code check uses a disposable analogue outside the write root, not an attempt to damage a real installation. No sibling `.git` mutation was attempted. No network request was attempted: disabled networking is configuration evidence only.

Observed model execution: requested `gpt-6-astra`, effort `medium`; thread `01a0854c-1b62-7a01-8b70-6381620586e7`. Terminal usage: input 35006, cached input 17280, cache-write input 0, output 89, reasoning output 0. These values are CLI event measurements, not model-name cost estimates. The event does not independently attest the backend's actual model identity. An earlier Sol probe returned `unverified` without running a tool and is not isolation evidence.

## Exact process contract

Pass arguments as an argv array; pass UTF-8 task text to stdin and close stdin. Do not construct shell code from a worker response. The following is argv notation, not a PowerShell command string:

```text
<native-codex.exe> exec
  --ignore-user-config --ignore-rules --ephemeral --skip-git-repo-check
  --model <requested-model>
  -c model_reasoning_effort="<requested-effort>"
  -c approval_policy="never"
  -c default_permissions="bsl_worker"
  -c permissions.bsl_worker={filesystem={":root"="read","<absolute-source>"="write"},network={enabled=false}}
  -c windows.sandbox="unelevated"
  --disable plugins --disable multi_agent --disable memories --disable shell_snapshot
  --json --output-schema <trusted-schema-path>
  --output-last-message <controller-owned-final-path>
  --cd <absolute-source> -
```

For an analysis worker, set source access to `read`. Do not use inherited `:workspace_roots`, `--add-dir`, `--sandbox`, `sandbox_mode`, or `sandbox_workspace_write` with this standalone profile. The source path must be canonical, distinct from controller/install/state directories, and serialized safely as a TOML key. The schema and final-output destination are controller-owned and outside source writes. The trusted host executable writes its own final output; worker commands cannot overwrite that destination.

Execute source/test code with the same profile through the measured non-model path:

```text
<native-codex.exe> sandbox -P bsl_worker
  -c permissions.bsl_worker={filesystem={":root"="read","<absolute-source>"="write"},network={enabled=false}}
  -c windows.sandbox="unelevated"
  -C <absolute-source> <trusted-runner.exe> <runner-argv...>
```

`codex sandbox` has no observed `--ignore-user-config` switch. Its named standalone filesystem profile was measured with the current host configuration. Managed adapter preflight must therefore reject unexpected applicable execution configuration rather than assume that the model-path flags apply to this command.

The adapter owns stdout JSONL, stderr, native PID/start time, timeout, and process exit. Persist attempt start before launch. Capture raw streams concurrently to avoid pipe deadlock; persist raw evidence even on failure. Normalize `thread.started.thread_id`, `item.completed` and `turn.completed.usage`. Missing usage stays null. A valid final JSON is only a proposal: the controller must check source hashes and gate evidence itself. Missing terminal events, malformed JSON, provider error and process failure cannot be translated to PASS from the last agent message.

On cancellation, verify the saved process identity before terminating its owned tree. The observed tree cancellation is not proof of rollback, safety of detached processes, or completion of a previously sent business mutation. Unknown business results require a control read. The production argument quoting, UTF-8 stdin, bounded unread stdin, cancellation and recovery tests passed in PowerShell 5.1 and 7. The production runner uses explicit UTF-8 bytes, not the Windows console code page.

## Configuration and privileged extension boundary

The official [advanced configuration documentation](https://learn.chatgpt.com/docs/config-file/config-advanced) says trusted projects can load `.codex/config.toml` layers between repository root and current directory, plus local `hooks.json`. Therefore `--ignore-user-config` must not be treated as suppression of project settings. The [hooks documentation](https://learn.chatgpt.com/docs/hooks) also describes user hooks separately from project trust.

For the first source-only adapter, reject discovered `.codex/config.toml`, `.codex/config.json` and `.codex/hooks.json` on the ancestor chain before each dispatch. The initial implementation conservatively scans to the drive root, including the user profile: projects under Documents or the default TEMP can therefore be blocked by a profile `.codex/config.toml`. The measured pilots use an independent `C:\DEV` tree. This compatibility limitation must not be worked around by deleting the user's configuration. Do not trust a one-time preflight across worker edits. Retain `--ignore-rules`, and additionally disable `hooks`, `browser_use`, `computer_use`, `in_app_browser`, and `skill_mcp_dependency_install`. Their complete hardened combination passed the production source-only model pilot. Do not grant arbitrary MCP tools to this source-only worker.

Production capability probes use `Set-Content` and accept only `UnauthorizedAccessException` as a denied write. A first prototype incorrectly tried `IO.File` inside ConstrainedLanguage PowerShell; a language restriction is not filesystem-isolation evidence. The corrected probe and a real sandboxed JUnit runner proved the expected write boundary. The latter also preserved an old report on repeat and rejected a successful process that failed to create a new JUnit. Local evidence: `work/sandbox-verification-01`.

The [configuration reference](https://learn.chatgpt.com/docs/config-file/config-reference) defines the named filesystem profile, `default_permissions`, and network fields. The explicit root-read/source-write profile avoids granting additional configured workspace roots. It does not promise confidentiality of readable host files. Applicable trusted host/system configuration is part of the host trust boundary and must be inventoried separately from source-controlled configuration.

Git hooks, filters, test scripts and other source-controlled executables remain untrusted execution even when invoked by a nominally read-only operation. Run them through the sandbox path. A controller-side source manifest reader should read bytes directly without invoking source-configurable code. This spike does not validate an external MCP server, 1C runtime runner, reparse-point escape, detached service, or a new CLI version; those capabilities remain unverified until separately checked.

## Decision

Use the native Codex CLI and explicit per-invocation filesystem profile for the first bounded source-only managed adapter. Keep model output subordinate to deterministic gates. The safe pilot scope is controlled source writes plus trusted file assertions or sandboxed test runners, with no infobase mutation. Do not generalize this M0 result to global installation, runtime 1C acceptance, or lifting the temporary Unica restriction.
