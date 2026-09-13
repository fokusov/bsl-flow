# Unica MCP bridge contract

Research snapshot: 2026-09-10, Windows x64. This document specifies a
source-only MCP bridge proposal for managed Codex and OpenCode. It is not a
runtime-1C adapter and does not authorize an infobase operation.

## Verified local identity

The cached marketplace plugin is
`C:\Users\ifokusov\.codex\plugins\cache\unica\unica\0.12.3`.

| Item | SHA-256 |
| --- | --- |
| Plugin version / upstream commit | `0.12.3` / `f6d23068c397cd85c540812de7627b2c3f434d68` |
| `.mcp.json` | `90879814ED8E330873077B95B1C939625BB77361B8C040F3A895D25DF5D0F385` |
| `runtime-manifest.json` | `ED2FDD5FF47880C5402FDCF284F1CEB65F0BA170FC55979CD75BD6D6817EFCBA` |
| `bootstrap/launch.sh` | `E1634FFC245783D7A86EAEBD0BB3696BB1D5A87516FBFA6DDD8F2453896ADD23` |
| `bootstrap/bin/win-x64/unica-bootstrap.exe` | `8810F05A96E2A4CF35484FA2622854446D770DBA6F919A19C4950764E2800F77` |
| Cached `unica.exe` | `1B1935F59EB3A4F6EF5AAE6D10C3A9CDDCBB57DCC38A391AA0C98CE408FAD517` |

The pinned Windows archive is `unica-runtime-win-x64.tar.gz`, SHA-256
`9bc4ff6c39d453d61105d218a926d17490d774deab6ec75463179c72c087f797`.
The manifest lists 66 Windows runtime files. The present cache is
`C:\Users\ifokusov\.codex\unica\runtimes\0.12.3\win-x64`; its `.ready.json`
identifies plugin `0.12.3`, target `win-x64`, and the cached executable hash
matches the pinned manifest entry. Treat the cache directory and every listed
manifest file as required dependencies; do not substitute an unpinned binary.

## Verified public stdio launch

Use this argv vector, with no shell interpolation and no internal adapter:

```text
C:\Users\ifokusov\.codex\plugins\cache\unica\unica\0.12.3\bootstrap\bin\win-x64\unica-bootstrap.exe
  run
  --plugin-root
  C:\Users\ifokusov\.codex\plugins\cache\unica\unica\0.12.3
```

Set the child-only environment variable:

```text
UNICA_RUNTIME_CACHE_DIR=C:\Users\ifokusov\.codex\unica\runtimes
```

This was run against the existing cache only. MCP `initialize` using protocol
`2025-03-26` returned `serverInfo = { name: "unica", version: "0.12.3" }`.
After `notifications/initialized`, `tools/list` returned 74 tools and the
owned process exited 0. No tool call, 1C process, database, durable job, or
download was performed.

The packaged `.mcp.json` instead uses Git's command-scoped POSIX alias to run
`bootstrap/launch.sh`. That launcher selects `win-x64` under Git for Windows
and calls the same bootstrap. A direct Windows bootstrap argv is preferable
for managed hosts because it removes Git Bash, `$PWD`, `$GIT_PREFIX`, and
host-variable expansion from the launch boundary.

## Host configuration candidates

These are concrete configuration shapes, not applied settings. The controller
must pin the plugin root and cache path after verifying the identities above.

Codex TOML:

```toml
[mcp_servers.unica]
command = "C:\\Users\\ifokusov\\.codex\\plugins\\cache\\unica\\unica\\0.12.3\\bootstrap\\bin\\win-x64\\unica-bootstrap.exe"
args = ["run", "--plugin-root", "C:\\Users\\ifokusov\\.codex\\plugins\\cache\\unica\\unica\\0.12.3"]
startup_timeout_sec = 900
enabled_tools = [
  "unica.project.map", "unica.cf.info", "unica.cf.validate",
  "unica.cfe.diff", "unica.cfe.validate", "unica.meta.info",
  "unica.code.search", "unica.code.diagnostics"
]
disabled_tools = [
  "unica.build.dump", "unica.build.load", "unica.build.update", "unica.build.make", "unica.build.run",
  "unica.runtime.execute", "unica.runtime.job.start", "unica.runtime.job.status",
  "unica.runtime.job.wait", "unica.runtime.job.logs", "unica.runtime.job.cancel", "unica.runtime.job.list",
  "unica.support.edit"
]

[mcp_servers.unica.env]
UNICA_RUNTIME_CACHE_DIR = "C:\\Users\\ifokusov\\.codex\\unica\\runtimes"
```

OpenCode JSON:

```json
{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "unica": {
      "type": "local",
      "command": [
        "C:\\Users\\ifokusov\\.codex\\plugins\\cache\\unica\\unica\\0.12.3\\bootstrap\\bin\\win-x64\\unica-bootstrap.exe",
        "run",
        "--plugin-root",
        "C:\\Users\\ifokusov\\.codex\\plugins\\cache\\unica\\unica\\0.12.3"
      ],
      "environment": {
        "UNICA_RUNTIME_CACHE_DIR": "C:\\Users\\ifokusov\\.codex\\unica\\runtimes"
      },
      "timeout": 900000,
      "enabled": true
    }
  },
  "tools": {
    "unica_*": false
  },
  "agent": {
    "unica-source-read": {
      "tools": {
        "unica_unica_project_map": true,
        "unica_unica_cf_info": true,
        "unica_unica_cf_validate": true,
        "unica_unica_cfe_diff": true,
        "unica_unica_cfe_validate": true,
        "unica_unica_meta_info": true,
        "unica_unica_code_search": true,
        "unica_unica_code_diagnostics": true
      }
    }
  }
}
```

OpenCode documents local MCP servers as `type: "local"`, an argv `command`,
an `environment`, and a tool-fetch `timeout` measured in milliseconds. Codex
CLI 0.153.0 also exposes `codex mcp add NAME --env KEY=VALUE -- COMMAND...`,
which confirms its stdio argv/environment registration path. Codex's installed
plugin declares the equivalent stdio server in `.mcp.json`; the direct TOML
shape is still a proposed explicit desktop registration, not an observed
desktop registration.

OpenCode 1.18.30 did live-connect this exact local configuration through
`opencode mcp list`. That check did not call a Unica tool, a model, or a 1C
process. Its disposable cache nevertheless received a 4.5 MB `models.json`;
therefore OpenCode MCP discovery must not be described as a no-network probe.

## Exact host tool identifiers

Codex `enabled_tools` and `disabled_tools` use the raw tool names returned by
the Unica MCP server. The official filter applies those sets to `tool.tool.name`
before it constructs model-visible names. The installed `codex-cli 0.153.0`
accepted temporary `mcp_servers.unica.enabled_tools` and `disabled_tools`
values through `codex mcp get unica --json`; the command echoed the exact lists
without starting the server.

OpenCode 1.18.30 uses this exact transformation:

```text
openCodeToolId = sanitize(serverName) + "_" + sanitize(rawMcpToolName)
sanitize(x) = x with every character outside [a-zA-Z0-9_-] replaced by "_"
```

The official immutable release is commit `3104c14`. The official OpenCode
`dev` source currently exports this exact `toolName` function in
`packages/opencode/src/mcp/catalog.ts`; the release commit pin and that source
file were inspected separately because the browser could not retrieve the
historical blob by its SHA. For the `unica` server, the initial bridge plans to
use these identifiers:

| Raw Unica tool | OpenCode ID |
| --- | --- |
| `unica.project.map` | `unica_unica_project_map` |
| `unica.cf.info` | `unica_unica_cf_info` |
| `unica.cf.validate` | `unica_unica_cf_validate` |
| `unica.cfe.diff` | `unica_unica_cfe_diff` |
| `unica.cfe.validate` | `unica_unica_cfe_validate` |
| `unica.meta.info` | `unica_unica_meta_info` |
| `unica.code.search` | `unica_unica_code_search` |
| `unica.code.diagnostics` | `unica_unica_code_diagnostics` |
| `unica.cf.edit` | `unica_unica_cf_edit` |
| `unica.cfe.borrow` | `unica_unica_cfe_borrow` |
| `unica.cfe.patch_method` | `unica_unica_cfe_patch_method` |
| `unica.meta.add` | `unica_unica_meta_add` |
| `unica.meta.edit` | `unica_unica_meta_edit` |
| `unica.meta.remove` | `unica_unica_meta_remove` |
| `unica.code.patch` | `unica_unica_code_patch` |

## Source-only allowlist

The live server exposes runtime and build tools alongside source tools. Both
hosts have an exact host-side allowlist, so a proxy is not required merely to
translate tool names. Never enable the OpenCode wildcard `unica_*` for a
worker; use it only as the global deny before enabling named tools per agent.

| Phase | Exact tools | Purpose |
| --- | --- | --- |
| R0 read | `unica.project.map`, `unica.cf.info`, `unica.cf.validate`, `unica.cfe.diff`, `unica.cfe.validate`, `unica.meta.info`, `unica.code.search`, `unica.code.diagnostics` | Map and inspect exported source; structural/static checks only. |
| W1 source edit, controller-authorized | `unica.cf.edit`, `unica.cfe.borrow`, `unica.cfe.patch_method`, `unica.meta.add`, `unica.meta.edit`, `unica.meta.remove`, `unica.code.patch` | Mutate only the selected exported source worktree. Start with `dryRun: true`; the controller permits `dryRun: false` only for an explicit source-change request and verifies the resulting diff. |

All other live tools remain denied, including `unica.build.*`,
`unica.runtime.execute`, `unica.runtime.job.start`,
`unica.runtime.job.cancel`, the other durable-job methods, and `unica.support.edit`.
The temporary Unica runtime restriction therefore remains intact.

`opencode debug agent` does not enumerate its dynamic MCP catalog, explaining
why it could not find individual MCP IDs in an earlier diagnostic. The mapping
is source-derived and pinned to the requested release identity, but remains
pending a planned bounded source-only OpenCode tool call. Before enabling a new
Unica or OpenCode release, repeat `tools/list`, compare raw names, and reject
an unexpected name or a sanitization collision.

## OpenCode stdin prompt contract

The official OpenCode `run` command declares an optional positional
`run [message..]`. Its handler builds the message from those arguments and,
when stdin is not a TTY, appends `await Bun.stdin.text()` before rejecting an
empty message. Therefore the managed argv must contain no task prompt:

```text
opencode run --pure --format json --model <provider/model> --agent <agent> --dir <source>
```

The controller writes the complete UTF-8 task text to stdin, then closes stdin.
It must not place the task in argv, use a literal `-`, or rely on shell quoting.
This is source-derived from the official current `run.ts`; a future bounded
real-tool pilot must verify the behavior against OpenCode `1.18.30` before a
managed adapter relies on it.

The live schemas prove these useful address contracts: `unica.cfe.diff` requires
`ExtensionPath` and `ConfigPath`; `unica.cfe.validate` requires `ExtensionPath`;
`unica.meta.info` requires `sourceSet` and `metadataPath`; `unica.meta.edit`
requires `sourceSet`, `metadataPath`, and `operations`; `unica.meta.add`
requires `sourceSet`, `kind`, and `name`; and `unica.code.patch` requires
`sourceSet`, `metadataPath`, `operation`, and `content`. `sourceSet` must come
from `unica.project.map`, not a hard-coded `main` value.

## Boundaries and remaining checks

- The 74-tool count, names above, server identity, and protocol are live
  `initialize`/`tools/list` evidence. The list is a server capability inventory,
  not an authorization decision.
- Codex desktop's loading of an explicit MCP entry while marketplace plugins
  are disabled is unverified. The installed CLI accepts exact raw-name
  allow/deny lists. OpenCode's per-tool IDs and stdin behavior are source-derived
  pending a bounded real-tool pilot; neither fact proves a desktop model
  session's effective tool set.
- The controller must make an owned immutable tool snapshot from `tools/list`,
  compare it to this allowlist on every startup, and reject an unexpected
  server version, tool removal, tool addition selected by wildcard, or hash
  mismatch.
- A source-only bridge does not prove native build, load, update, syntax,
  runtime behavior, UI, or a specific infobase state.

## Source links

- OpenCode v1.18.30 release commit: https://github.com/anomalyco/opencode/releases/tag/v1.18.30
- OpenCode catalog normalization: https://github.com/anomalyco/opencode/blob/3104c14/packages/opencode/src/mcp/catalog.ts
- OpenCode run command: https://github.com/anomalyco/opencode/blob/3104c14/packages/opencode/src/cli/cmd/run.ts
- OpenCode MCP configuration and per-agent tools: https://dev.opencode.ai/docs/mcp-servers/
- Codex MCP filter source: https://github.com/openai/codex/blob/main/codex-rs/codex-mcp/src/tools.rs
- Codex configuration reference: https://learn.chatgpt.com/docs/config-file/config-reference

