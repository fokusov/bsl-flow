# onec-ops/v1: 1C operations contract

`global/skills/1c-verify/onec-ops/Invoke-OneCOp.ps1 -Capability <name> -ProjectPath <dir> [-Params <hashtable|json-file>] [-Provider <name>] [-AuthorizationFile <file>] [-ImportResult <file>]`

Prints and writes a `{schema_version, capability, status, mutating, target, evidence[], raw_output_sha256, provider, provider_version, message, agent_tool?}` result to `.bsl-flow/reports/onec-ops/<timestamp>-<capability>.json`. Exit 0 PASS, 1 FAIL, 11 BLOCKED.

## Capabilities

| capability | mutating | purpose |
|---|---|---|
| `metadata.inspect` | no | metadata/requisite/module index from local source roots |
| `syntax.check` | no | module syntax check |
| `static.bslls` | no | BSL LS diagnostics (wraps `Invoke-1CStaticDiff.ps1`) |
| `build.cf` / `build.cfe` / `build.epf` | provider-dependent | local artifact; native and vrunner build routes require authorization because their build process can load a database |
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

Known database-write capabilities always require authorization. In addition, a capability whose manifest entry sets `requires_authorization: true` is refused (`BLOCKED`, provider never invoked) unless `-AuthorizationFile` points at JSON `{capability, target, expires_utc}` whose `capability` and `target` exactly match the request and whose `expires_utc` is a still-future UTC timestamp. `target` comes from `-Params.target`.

## Adapters (`onec-ops/adapters/<name>/provider.json` + scripts)

- `fake` - configurable outcomes for offline tests.
- `bslls` - wraps `1c-verify/scripts/Invoke-1CStaticDiff.ps1` for `static.bslls`.
- `grounding` - calls the actual `Get-1CMetadataIndex.ps1 -SourceRoot ... -CachePath ...` contract. Supply `params.source_root` as a path or array (relative roots resolve from the project); evidence contains the hashed cache with its metadata index.
- `native-1cv8` - `1cv8 DESIGNER`/`ENTERPRISE` batch mode. `/LoadConfigFromFiles`, `/UpdateDBCfg`, `-Extension` and the YAxUnit `RunUnitTests=` ENTERPRISE flow are verified precedent from `Task.Runtime.ps1`; `/DumpCfg`/`/LoadCfg` are marked UNVERIFIED in the adapter scripts. `extension.list` has no verified non-mutating route and always returns `BLOCKED`.
- `vrunner` - vanessa-runner CLI; every subcommand/flag is marked UNVERIFIED pending a check against a pinned `vrunner --help`.
- `unica` - always `BLOCKED` under the temporary runtime recovery restriction, even with valid authorization or `-ImportResult`. It emits no runtime/durable-job instruction and does not declare `extension.list`. Lifting the restriction requires verification of the installed fix and explicit owner authorization.
- `skillset` - generic `onec.skillset.map` mapping to a `.ps1` or a Skill name. Scripts must return structured status; exit 0 alone is insufficient. Imports must identify the same capability/target and PASS must include evidence files. Known Unica runtime mappings are blocked; custom mappings remain trusted project code subject to the same authorization and runtime restrictions.

All adapter entry scripts are internal: invoke them through the dispatcher. An authorization file records an existing owner decision; agents must not fabricate it as permission to execute. Build authorization covers the caller-supplied scratch database; a local artifact classification never authorizes writing an arbitrary database. CLI flag labels distinguish reused source precedent from real execution evidence. This port's contract tests use mock executables only and prove no real platform/runner compatibility. Native `extension.list` remains BLOCKED; a source metadata index cannot prove installed extension state. Native YAxUnit requires an executed JUnit testcase and no reported failures; vrunner runtime-test acceptance remains BLOCKED pending a verified result parser. Neither onec-mcp nor edt-cli is implemented yet; selecting either returns no-provider BLOCKED.

## Adding an adapter

1. `adapters/<name>/provider.json` matching `schemas/provider.schema.json` (`contract: "onec-ops/v1"`, one `entry` script per capability, a `detect` script printing exactly `true`/`false`).
2. Entry scripts accept `-ProjectPath -Params -AdapterDir -Capability -AuthorizationFile -ImportResult` and return a PSCustomObject with at least `status` (`PASS|FAIL|BLOCKED`) and `evidence` (paths, relative to `-ProjectPath` or absolute); the dispatcher hashes evidence and `raw_output` itself.
3. Register in the target project's `onec.providers`/`onec.overrides`.
4. Add the adapter to `scripts/Test-OneCOpsContract.ps1`.
