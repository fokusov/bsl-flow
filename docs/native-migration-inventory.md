# Карта миграции native-cross-platform-cli

Дата: 2026-09-15 (обновление: волны 7–11). Приоритет владельца: **Windows-first** (macOS/Linux — потом; кросс-платформенные заготовки — build tags, host_other, CI matrix, 4-таргетный release — остаются заделом). Живой трекер замещения PowerShell native Go-интерфейсами по спеке `openspec/changes/native-cross-platform-cli`. Очередь задач для агентов (карточки-брифы W7–W12 + retirement, оценки) — [NATIVE_MIGRATION_ROADMAP_RU.md](NATIVE_MIGRATION_ROADMAP_RU.md). Статусы: **ported** (Go-эквивалент с тестами), **partial** (контрактный слой есть, runtime-исполнение ещё на PS), **pending** (замещение не начато), **retire-candidate** (удаление возможно после green native CI и переноса coverage).

## Волны 7–11 (2026-09-15, commits ffd8f3a…716b55c)

| Срез | Что | Статус |
| --- | --- | --- |
| W7 live council engine | `cli/internal/councilengine` (роли, бюджет/адмиссия, фолбэк-политика, final gate, byte-parity ConvertTo-Json) + `stagehost/council.go`; скилл `1c-spec-review` на бинарнике | **готово** (ffd8f3a, 1ccb5d0) |
| W8 native 1C runtime adapter | verify-ветка исполняет native_1c (COM-inventory, credentials-канал, recovery-ledger d325634); на unix — typed BLOCKED_UNSUPPORTED_PLATFORM | **код готово**; live-приёмка PAUSED владельцем (`work/w8-live-acceptance-20260914/`), integration/ui/external_artifact остаются fail-closed до неё |
| W9a runner loop | `runner run` → нативная петля поверх `internal/runner` (journal replay, lease, liveness, no-blind-retry); default native, `--engine legacy-powershell` сохранён | **готово, в production CLI** (a05dc76) |
| W9b delivery/git | `delivery.NewCLIGitPort` (git subprocess, clean-env, bounded, non-interruptible push watchdog, create-only lease) + `task publish`/`publish-resume` нативно (sealed state, pending-маркер remote/ref) | **готово, в production CLI** (0cb2ed8) |
| W9c bootstrap wiring | `bsl-flow init/upgrade` над `internal/bootstrap` (шаблоны из embedded bundle, fail-closed); скилл `1c-init-project` на бинарнике | **готово** (31470c1, 30e97b8) |
| REQ8 .exe-релаксация | dual-reader: extensionless absolute paths рядом с историческим .exe в execution profiles/pins/attempts; launch-gate отвергает shebang и Windows-extensionless; worker-порт зеркалит stagehost | **готово** (b55212a) |
| W10 parity harness + shadow | `cli/internal/parityharness`: frozen-trace формат с классификацией schema/behavior change, load-bearing аннотации; Shadow() — только read/decision пути по allow-list, без writes/processes/model-calls; 5 PS-трейсов с provenance | **готово** (1007705) |
| W11 no-pwsh audit + smoke | S-lifecycle процесс-аудит (все process/exit/transport receipts = host binary/git/pinned provider; argv-скан), runner self-dispatch аудит; clean-install smoke из release-архива windows/amd64 (extract → help/version/capability/init/task lifecycle) | **готово, Windows-scope** (716b55c); macOS/Linux execution smoke — открыт по таргетам |
| W12 docs | настоящий трекер, ROADMAP-статусы, CHANGELOG Unreleased | **готово** (этот коммит); `verification.md` — решение владельца |

## Волна 6 (2026-09-14, native worker library + native memory helper)

| Срез | Что | Статус |
| --- | --- | --- |
| Worker-библиотека `cli/internal/worker` | порт адаптеров: `Invoke-BFManagedWorker`/`Invoke-BFProfiledCodexWorker`/`Invoke-BFOpenCodeWorker` (спавн argv-as-data, дерево-kill, бюджетные хуки в PS-порядке, critic-каталог/контракты, app-server RPC/skills inventory, rollout identity); типизированные `BF_BLOCKED` refusal'ы | **готово, wired в stagehost (см. следующий срез)** |
| Native memory helper `cli/internal/memoryhost` | порт `Invoke-BFNativeMemory.ps1` + достижимое подмножество `Task.Memory.ps1` (bind / extract-attempt / extract-acceptance / projection; fingerprints, event journal/replay, bundle selection, writer-lock); live differential против реального pwsh: конверты/события/индекс byte-identical | **готово, в production CLI** |
| Memory wiring | скрытая субкоманда `__memory` (self-host ре-экзекьют собственного бинарника, как `__provider`); композит маршрутизирует InvokeMemory на native — memory больше не требует pwsh и работает на всех платформах (degrade остаётся только при ошибке хелпера) | **готово** |

## Волна 5 (2026-09-14, native stage host: measure/verify)

| Срез | Что | Статус |
| --- | --- | --- |
| Native stage host `cli/internal/stagehost` | порт `Invoke-BFNativeProvider.ps1`: контракты, measure, verify, process-квитанции, capability/permission-профили | **готово, в production CLI** |
| Self-host транспорт | скрытые субкоманды `__provider`/`__fs-probe`: CLI ре-экзекьютит собственный доверенный бинарник вместо pwsh (in-sandbox fs-probe — тоже host-бинарник); контракт `windows-ps.v1` сохранён намеренно | **готово** |
| Композит-провайдер | `cli/composite_provider.go`: measure+verify → native stage host; worker-стадии → packaged Windows PS provider (на macOS/Linux — typed blocked envelope, без pwsh fallback); memory → native `__memory` (волна 6) | **готово** |
| strictjson + экспортные швы | пакет `cli/internal/strictjson`; экспортные швы `cli/internal/repository/export_stagehost.go` | **готово** |
| CI-матрица native | `.github/workflows/native-cli.yml`: windows-2025/macos-14/ubuntu-24.04, `go vet` + `go test ./... -timeout 20m` | **добавлена; удалённый прогон ожидает push (решение владельца)** |

## Волна 4 (2026-09-13, Windows-first runtime wiring)

| Срез | Что | Статус |
| --- | --- | --- |
| Нативные spec-команды | `bsl-flow spec lint --project <path> [--change <id>] [--json]` и `bsl-flow spec final ...` — PowerShell-free, артефакт spec-lint.json байт-совместим с PS; differential на всех 5 реальных change'ах: native == PS (ошибки/предупреждения); Go final-валидатор поймал реальный дрейф binding-final-spec в repository-task-registry (правка спеки после reconciliation), подтверждён pwsh | **готово, в production CLI** |
| Native spec-stage провайдер | `nativeSpecStageProvider` (композиция поверх PS-провайдера): spec-стадия исполняется в Go (specvalidate, byte-parity артефактов, та же blocked-семантика lint-ошибок, source-manifest gate); текст спеки приходит через инжектируемый `nativeSpecWorker` seam (fail-closed, без тихого fallback) | **готово, активация в production-маршрут — следующий слайс** (нужен controller-level differential до переключения) |

## Стадии спеки (requirement 2)

| Стадия | Статус | Что закрыто |
| --- | --- | --- |
| 1. native read/catalog | done (принято ранее) | `repository-task-registry`: read-команды, v1 journal reader |
| 2. storage/canonicalization/locks | done (принято ранее) | canonical encoder, atomic publication, expected revision, graph locks |
| 3. controller core | done (принято 2026-09-13) | transitions/gates/budget/attempts/memory в Go; S/M/repair через реальный dispatch |
| 4. execution ports | **done (Windows)** | native stage host (measure/verify/worker/memory), live council engine (W7), native 1C adapter (W8; integration/ui/external_artifact остаются fail-closed до live-приёмки) |
| 5. operational paths | **done** | runner-петля (`runner run`), delivery git-адаптер + publish/publish-resume, bootstrap init/upgrade — все в production CLI (W9) |
| 6. build/release | **done (Windows-scope)** | PS-free packaging + clean-install smoke из release-архива (W11); macOS/Linux execution smoke не выполнялся (нужны runner'ы — push) |
| 7. retirement (runtime PS removal → zero `.ps1`) | **pending** | запрещено до parity + green native CI |

## Пакеты Go (созданы 2026-09-13, волна 1–2)

| Пакет | Замещаемый PS-контракт | Статус |
| --- | --- | --- |
| `cli/internal/platform` | host-слой путей/блокировок; engine selection; capability model | ported (тесты + 4 кросс-таргета); wiring: `bsl-flow capability`, unix-гейт legacy |
| `cli/internal/runner` | `Task.Runner.ps1` (journal replay, dedup, cursor, ownership, no-blind-retry) | done (W9a): decision/replay-слой + нативная петля `bsl-flow runner run` (self-dispatch, lease, liveness) |
| `cli/internal/delivery` | `Task.Publication.ps1`/`Task.PublicationGit.ps1`/`Task.Delivery.ps1` (target/authorization, publish-resume, unknown-effect, handoff) | done (W9b): домен + реальный git-адаптер CLIGitPort; publish/publish-resume в CLI |
| `cli/internal/bootstrap` | `Initialize-BSLFlowProject.ps1`/`Update-BSLFlowProject.ps1` (create/merge, comment-preserving) | done (W9c): `bsl-flow init/upgrade`, шаблоны из embedded bundle |
| `cli/internal/release` | `Build-BSLFlowCli.ps1`/`Build-BSLFlowPackage.ps1` (сборка/архивы/чексуммы) | done (`cmd/bslflow-release`, без PowerShell; W11 добавил `-targets` и clean-install smoke); интеграция в lanes pending |
| `cli/internal/worker` | Codex/ProfiledCodex адаптеры: JSONL-события, rollout-identity (`Get-BFObservedModelEffort`), host-result.json, capability gate, sealing | done (process spawn + e2e fake-codex тесты; byte-parity host-result) |
| `cli/internal/counciltransport` | `Council.Transport.ps1` (openai_compatible HTTP: 1 MiB bounds, 60–900s, redirect-отказ, redaction, usage-словарь, observed identity) | done (18 httptest-loopback тестов; wired в council engine W7) |
| `cli/internal/specvalidate` | `Test-1CSpec.ps1` (14 lint-правил) + `Test-1CSpecFinal.ps1` v1/v2 (schema/hash/reconciliation проверки) | ported с byte-parity против реального pwsh (20/20 lint фикстур); 3 правила честно не портированы (ConvertTo-Json digest, policy hash вне скоупа, banker's rounding recompute) — см. doc-comment `ValidateFinal` |
| `cli/cmd/bslflow-release` | — | новый PowerShell-free сборщик (4 таргета, детерминированные архивы) |

## Runtime `.ps1` (61 в global/, 63 в scripts/) — замещение по группам

| Группа | Файлы | Статус |
| --- | --- | --- |
| Task engine/process/stages/gates/contracts | Task.Engine/Process/Stages/Gates/Contracts.ps1 | partial: transitions/gates в Go (repository); measure/verify стадии — нативно в `cli/internal/stagehost`; worker-исполнение стадий через windows-ps provider |
| Provider/worker adapters | Task.Provider.ps1, Invoke-BFNativeProvider.ps1, adapters/Codex*.ps1, OpenCode*.ps1 | partial: measure/verify портированы в `cli/internal/stagehost` (native stage host, волна 5); вся execute-цепочка (measure + inspect/spec/spec_review/implement/code_review/diagnose/verify) портирована в `cli/internal/stagehost`+`cli/internal/worker` (волна 6, differential есть); memory-хелпер портирован (`internal/memoryhost`, волна 6); на native-пути остаются типизированными BLOCKED: live council engine, unmanaged codex (без execution_profile); 1С runtime adapter — pending (Windows-only capability) |
| Review/council | Invoke-CouncilReview.ps1, Council.*.ps1, Invoke-1CSpecReview.ps1, Review.Common.ps1, Test-1CSpec*.ps1 | partial: полный council-цикл в Go (`internal/councilengine` + `stagehost/council.go`, W7); транспорт/валидация/lint/final в Go; PS-маршрут остаётся compatibility-артефактом |
| Memory | Task.Memory.ps1, Invoke-BFNativeMemory.ps1, Task.NativeReuse.ps1 | helper ported (`cli/internal/memoryhost`, волна 6, live differential byte-identical); Task.NativeReuse.ps1 — pending (reader reuse-контрактов) |
| Publication/delivery | Task.Publication*.ps1, Task.Delivery.ps1 | done: `internal/delivery` + CLIGitPort; `task publish`/`publish-resume` нативно (W9b, 0cb2ed8); evidence-цепочка уже, чем PS (sealed state + immutable request вместо registration/prepared/intent/receipt — классифицировано в коде) |
| Runner/queue | Task.Runner.ps1, Invoke-BSLFlowTask.ps1 | done: `runner run` — нативная петля (W9a, a05dc76); Invoke-BSLFlowTask.ps1 остаётся entrypoint'ом legacy-движка |
| Runtime 1С/native adapter | Task.Runtime.ps1 | done (Windows): нативный адаптер + recovery ledger (daf4205, d325634); live-приёмка PAUSED владельцем; на unix BLOCKED_UNSUPPORTED_PLATFORM |
| Bootstrap/init | 1c-init-project/scripts/*.ps1 | done: `internal/bootstrap` + `bsl-flow init/upgrade` (W9c, 31470c1); скилл на бинарнике; PS-скрипты — compatibility |
| Coverage/protected/architecture | Task.Coverage.ps1, Task.Architecture.ps1 | partial: coverage/protected gates уже в Go (controller) |
| Test/build scripts (scripts/*.ps1) | 63 файла suite/build/CI | pending до стадии 7 (retirement): переносятся последними, после green native CI |

## CI

`offline.yml`: job `native-matrix` (macos-14/macos-13/ubuntu-24.04, bash, без pwsh: vet + build + кросс-compile + OS-neutral suites). Проверка: `scripts/Check-CIMatrix.ps1` → `CI_MATRIX_OK`. Windows jobs не менялись. **Не запускался** — удалённый CI требует push (решение владельца).

`native-cli.yml` (волна 5): матрица windows-2025/macos-14/ubuntu-24.04, `go vet` + `go test ./... -timeout 20m`. **Не запускался** на удалённых runner'ах — ожидает push (решение владельца).

## Открытые границы

- Обновлено 2026-09-15: native-путь (композит) не запускает pwsh ни на одной стадии — measure/verify/worker/memory (волны 5–6), live council (W7), native 1C adapter (W8), runner/delivery/bootstrap (W9), REQ8-пути; Windows-scope no-pwsh процесс-аудит и clean-install smoke из release-архива — зелёные (W11, req 22 Windows-scope). Остатки typed BLOCKED: unmanaged codex (без execution_profile — by design, не портировать); integration/ui/external_artifact критерии 1C — до live-приёмки (PAUSED владельцем).
- Дифференциальная parity консолидирована (W10): frozen-trace harness с классификацией schema/behavior change + shadow mode по allow-list read/decision путей; live-pwsh дифференциалы за существующими build tags.
- `verification.md` не создаётся: спека запрещает до evidence на всех target OS; решение владельца — Windows-scoped с явной границей ИЛИ оставить change открытым до multi-OS фазы.
- Осталось до retirement (стадия 7): push + зелёная remote CI-матрица (native-cli.yml ещё не прогонялся — решение владельца), macOS/Linux smoke, перенос 63 coverage test/build-скриптов `scripts/`.
