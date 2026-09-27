# onec-ops/v1: 1C operations contract

`global/skills/1c-verify/onec-ops/Invoke-OneCOp.ps1 -Capability <name> -ProjectPath <dir> [-Params <hashtable|json-file>] [-Provider <name>] [-AuthorizationFile <file>] [-ImportResult <file>]`

Prints and writes a `{schema_version, capability, status, mutating, target, evidence[], raw_output_sha256, provider, provider_version, message, agent_tool?}` result to `.bsl-flow/reports/onec-ops/<timestamp>-<capability>.json`. Exit 0 PASS, 1 FAIL, 11 BLOCKED.

## Capabilities

| capability | mutating | purpose |
|---|---|---|
| `metadata.inspect` | no | metadata/requisite/module index for grounding (needs Ф5.2's `Get-1CMetadataIndex.ps1`) |
| `syntax.check` | no | module syntax check |
| `static.bslls` | no | BSL LS diagnostics (wraps `Invoke-1CStaticDiff.ps1`) |
| `build.cf` / `build.cfe` / `build.epf` | no (local file) | build from sources |
| `extension.list` | no | actual installed-extension composition |
| `extension.load` | **yes** | load an extension into a target base |
| `config.update` | **yes** | update a target base's DB configuration |
| `test.yaxunit` | yes (test data) | run YAxUnit, produce JUnit |
| `test.vanessa` | yes | run a Vanessa feature |

## Provider selection

In `bsl-flow.yaml`:

```yaml
onec:
  providers:
    - native-1cv8
    - vrunner
  overrides:
    test.vanessa: vrunner
  skillset:
    map:
      build.cf: cf-init
```

Order: `-Provider` (explicit) > `onec.overrides[capability]` > `onec.providers` (first candidate whose manifest declares the capability and whose `detect` script prints `true`). No match -> `BLOCKED: capability X has no configured provider` (exit 11).

## Authorization for mutating capabilities

A capability whose manifest entry sets `requires_authorization: true` is refused (`BLOCKED`, provider never invoked) unless `-AuthorizationFile` points at JSON `{capability, target, expires_utc}` whose `capability` and `target` exactly match the request and whose `expires_utc` is a still-future UTC timestamp. `target` comes from `-Params.target`.

## Adapters (`onec-ops/adapters/<name>/provider.json` + scripts)

- `fake` - configurable outcomes for offline tests.
- `bslls` - wraps `1c-verify/scripts/Invoke-1CStaticDiff.ps1` for `static.bslls`.
- `grounding` - wraps `Get-1CMetadataIndex.ps1` for `metadata.inspect`; fails closed (`detect` -> false) until Ф5.2 lands.
- `native-1cv8` - `1cv8 DESIGNER`/`ENTERPRISE` batch mode. `/LoadConfigFromFiles`, `/UpdateDBCfg`, `-Extension` and the YAxUnit `RunUnitTests=` ENTERPRISE flow are verified precedent from `Task.Runtime.ps1`; `/DumpCfg`/`/LoadCfg` are marked UNVERIFIED in the adapter scripts. `extension.list` has no verified non-mutating route and always returns `BLOCKED`.
- `vrunner` - vanessa-runner CLI; every subcommand/flag is marked UNVERIFIED pending a check against a pinned `vrunner --help`.
- `unica` - `mode: agent_tool`. Never calls anything itself: returns `BLOCKED` with an `agent_tool: {agent_tool, arguments}` payload naming the MCP call (`unica.runtime.execute`, `dryRun:true`) the calling agent must make, then relays the agent's later outcome via `-ImportResult`. Does **not** declare `extension.list` - Unica's `operation=extensions` mutates (`references/test-setup.md:16`).
- `skillset` - generic `onec.skillset.map` mapping to either a `.ps1` (run as a script) or a Skill name (`agent_tool`, relayed via `-ImportResult` like `unica`).

## Adding an adapter

1. `adapters/<name>/provider.json` matching `schemas/provider.schema.json` (`contract: "onec-ops/v1"`, one `entry` script per capability, a `detect` script printing exactly `true`/`false`).
2. Entry scripts accept `-ProjectPath -Params -AdapterDir -Capability -AuthorizationFile -ImportResult` and return a PSCustomObject with at least `status` (`PASS|FAIL|BLOCKED`) and `evidence` (paths, relative to `-ProjectPath` or absolute); the dispatcher hashes evidence and `raw_output` itself.
3. Register in the target project's `onec.providers`/`onec.overrides`.
4. Add the adapter to `scripts/Test-OneCOpsContract.ps1`.
