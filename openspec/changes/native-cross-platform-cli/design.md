# Technical design

## Решение

Переносить controller в существующий Go module по bounded vertical slices, сохраняя один domain core и отделяя OS/process/integration adapters. Финальная архитектура:

```text
cmd/bsl-flow
  ↓ strict CLI / versioned JSON
internal/controller     single state machine and gates
  ├─ taskstore          journals, canonical JSON, locks, repository registry
  ├─ policy             authorization, dependencies, freshness, routing
  ├─ attempts           dispatch/recovery/unknown-effect
  ├─ review             council and reconciliation contracts
  ├─ runner             local queue/supervision
  ├─ delivery           handoff/publication
  └─ ports
       ├─ git
       ├─ worker
       ├─ verification
       └─ native1c (Windows capability only)
platform adapters: windows | darwin | linux
```

Domain packages не импортируют OS-specific process/path implementations. Controller создаёт decisions и authoritative writes; adapters возвращают typed observations/receipts и не меняют state напрямую.

## Compatibility baseline

Перед первым writable port создаётся immutable compatibility corpus:

- representative v1 task revision chains and current projections;
- every status/stage/next-action branch;
- request, review, coverage, runtime, delivery/publication schemas;
- corrupt/torn/stale/unknown-effect fixtures;
- Unicode, large integer/decimal/null, key-order and timestamp cases;
- runner journals and recovery snapshots.

Golden contract фиксирует exact canonical bytes для новых native writes и отдельно legacy v1 chain algorithm. В v1 `previous_sha256` вычисляется из legacy-canonical representation уже parsed state (`Get-BFCanonicalJson`), а raw stored file hash хранится/проверяется как отдельная величина и не подменяет chain hash. Fixture с допустимыми иными whitespace/key order обязан читаться так же, как legacy controller. Остальные public JSON сравниваются по schema/semantics. Любое намеренное отличие требует schema/ADR и dual-reader.

## Migration stages

### 1. Native read slice

Реализовать Git repository resolver, v1 journal reader/verifier и `repository-task-registry` read commands. Никаких legacy writes. Go tests работают на всех OS.

### 2. Native storage slice

Реализовать canonical encoder, atomic/durable revision publication, expected revision, per-task/repository/graph locks и fault injection. Planned metadata writes являются первым production write path. Activation остаётся staged `BLOCKED` до controller core; legacy engine не пишет новый repository store.

### 3. Controller core

Портировать pure transitions/gates из `Task.Engine/Process/Stages/Gates/Contracts` в typed Go domain. Shadow mode сравнивает decisions на frozen state. Затем explicit native engine получает write authority по одному action group; первая compatible group включает planned activation и `create→activate→run→resume` на одном repository UUID/journal.

### 4. Execution ports

Портировать Git worktrees, worker adapters, structured events, review council, verification, attempts, budget и recovery. Side-effecting parity доказывается receipts/replay, не повторным запуском.

### 5. Operational paths

Портировать runner, native Windows 1С adapter, delivery/publication, project bootstrap/upgrade, audit/evidence helpers. На Darwin/Linux native1c port всегда возвращает typed unsupported capability.

### 6. Build/release

Перенести package manifest/ZIP/checksum generation в Go tooling, создать OS matrix, clean-install smokes и native docs. Switch default to native после acceptance matrix; optional legacy Windows compatibility является отдельным artifact и не попадает в default package.

### 7. Retirement

Сначала default installation перестаёт требовать/неявно запускать PowerShell, сохраняя при необходимости отдельный explicit compatibility artifact. Следующий runtime-removal milestone удаляет PowerShell и из compatibility artifact. После этого переносятся оставшиеся repository test/build/install scripts либо заменяются `go test`/Go commands; zero `.ps1` inventory становится финальным gate.

## Engine selection

Transition config содержит closed enum и engine version/hash. Compatibility matrix связывает `(task store/schema, action group, engine)`: legacy PowerShell пишет только checkout-local v1, native читает v1/v2 и один владеет repository v2. Windows legacy mode требует явного opt-in и compatible PowerShell; native failure возвращает typed error. Darwin/Linux reject legacy before state access. Engine identity входит в policy/attempt binding, а switch существующей активной задачи требует scope reconciliation; read-only native inspection разрешено всегда.

## Canonical data и schemas

Не использовать обычный `encoding/json` map output как неявный hash contract. Canonical encoder задаёт UTF-8, escaping, legacy-compatible key ordering/value rules, integer/decimal semantics, timestamp normalization и newline policy. Decoder отвергает duplicate keys, trailing data, invalid UTF-8 и unexpected fields по closed schemas.

Schema evolution идёт через explicit versions and adapters. Historical revision bytes никогда не переписываются, но v1 chain verification намеренно parses state и вычисляет legacy canonical state hash; raw byte hash используется отдельно для immutability/provenance. New schema явно определяет, какой hash связывает revisions, и не меняет правило молча.

## Filesystem и locks

Общий interface включает canonicalize, lstat/no-follow traversal, atomic create/replace, sync file/directory, exclusive lock и filesystem capability probe.

- Windows: existing reparse-point and file-handle semantics.
- Darwin/Linux: `lstat/openat`-style no-follow checks, advisory lock with owner metadata and atomic rename on same filesystem.
- Unsupported/remote filesystem: write blocker unless exact semantics proven by capability test.

Recovery distinguishes unpublished temporary files from authoritative published revisions. Cleanup only removes paths created by the exact operation under validated parent.

## Process и cancellation

Process adapter принимает executable + argv + allowlisted environment + stdin source; shell отсутствует. Он создаёт attempt before start, bounded asynchronous drains, terminal status and OS process identity. Interrupt records requested cancellation and observed termination separately. For potentially effectful processes, kill/timeout cannot imply rollback; reconcile path remains controller-owned.

## Platform capability model

CLI exposes machine-readable capabilities: OS/arch, filesystem/lock support, Git, worker providers, sandbox, native1c, browser/UI and available verification adapters. Routing consumes observed capabilities; model text/config claims cannot enable them.

Source-only flow is mandatory on all target OS. Windows native1c capability additionally requires exact executable/target/auth contracts. macOS/Linux reports unsupported without dispatch.

## Packaging

Binary embeds only required versioned schemas/instructions/templates and manifest. Extraction is needed only for assets that external tools must read; pure controller code remains compiled. Cache root follows trusted OS APIs/defaults and validates every file before use.

Release builder consumes a clean source revision and produces deterministic archives for each target with SHA-256 manifest. Cross-compilation is followed by native CI smoke. Users install binary/assets only; Go toolchain is build-time.

## Testing and acceptance

Layers:

1. Pure domain unit tests for transitions/gates.
2. Golden raw-byte/hash/schema tests.
3. OS filesystem/process fault tests.
4. Temporary Git repository/worktree integration.
5. Fake worker/API/test providers for attempts/review/recovery.
6. Differential legacy/native read and decision traces on Windows.
7. Native clean-machine lifecycle on every target.
8. Focused Windows 1C acceptance retained separately.

Every migrated PowerShell contract is mapped to a native test/command before its script is removed. Coverage is behavior-oriented; filename count alone is not parity.

## Альтернативы

- Install PowerShell on macOS — отклонено пользователем и сохраняет лишнюю runtime dependency.
- Big-bang rewrite — отклонено: невозможно локализовать divergence в state/recovery contracts.
- Keep Go as wrapper forever — отклонено: не решает portability.
- Reimplement in another language — отклонено: existing Go host/toolchain already provides suitable cross-platform binary.
- Automatic native→PowerShell fallback — отклонено: скрывает defects and can duplicate effects.
- Embed a general shell — отклонено: расширяет attack surface и возвращает platform-specific scripts.

## Риски

| Риск | Снижение |
|---|---|
| Canonical JSON differs and breaks history | Raw-byte golden corpus, explicit canonical encoder, dual-reader |
| Two engines diverge | One writable authority per task/action, shadow read only, differential gate |
| Portability weakens security | Per-OS path/lock/process adapters and malicious fixtures |
| Retry duplicates external effect | Attempt-before-dispatch, receipt reconciliation, no automatic fallback |
| macOS marked supported without real run | Native hosted CI and clean-install smoke for arm64/amd64 |
| 1C limits hidden | Typed Windows-only capability and expected blockers elsewhere |
| Migration never reaches zero PS | Explicit runtime-removal and repository-zero milestones with inventory gates |
| Parallel features change contracts | Reinspect/reconcile final merged ADR/schema before implementation |

## Sequencing dependencies

`repository-task-registry` provides the first storage/read vertical slice. `api-specification-council` provides the future review transport contract. `ARCHITECTURE_CONTEXT_PLAN_RU.md` work may change context bundle/dependency bindings. Native implementation must consume their accepted final contracts, not current transient dirty-tree shapes.
