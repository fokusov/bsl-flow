# Карта миграции native-cross-platform-cli

Дата: 2026-09-14 (обновление: волна 6). Приоритет владельца: **Windows-first** (macOS/Linux — потом; кросс-платформенные заготовки — build tags, host_other, CI matrix, 4-таргетный release — остаются заделом). Живой трекер замещения PowerShell native Go-интерфейсами по спеке `openspec/changes/native-cross-platform-cli`. Статусы: **ported** (Go-эквивалент с тестами), **partial** (контрактный слой есть, runtime-исполнение ещё на PS), **pending** (замещение не начато), **retire-candidate** (удаление возможно после green native CI и переноса coverage).

## Волна 6 (2026-09-14, native worker library + native memory helper)

| Срез | Что | Статус |
| --- | --- | --- |
| Worker-библиотека `cli/internal/worker` | порт адаптеров: `Invoke-BFManagedWorker`/`Invoke-BFProfiledCodexWorker`/`Invoke-BFOpenCodeWorker` (спавн argv-as-data, дерево-kill, бюджетные хуки в PS-порядке, critic-каталог/контракты, app-server RPC/skills inventory, rollout identity); типизированные `BF_BLOCKED` refusal'ы | **готово как библиотека с тестами** (e2e через fake codex/opencode-бинарник); wiring в stagehost execute — следующий срез |
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
| 4. execution ports | **partial** | см. таблицу ниже |
| 5. operational paths | **partial** | runner/delivery/bootstrap — контракты в Go; исполнение ещё за PS provider |
| 6. build/release | **partial** | Go release builder есть; clean-install smoke на macOS/Linux не выполнялся |
| 7. retirement (runtime PS removal → zero `.ps1`) | **pending** | запрещено до parity + green native CI |

## Пакеты Go (созданы 2026-09-13, волна 1–2)

| Пакет | Замещаемый PS-контракт | Статус |
| --- | --- | --- |
| `cli/internal/platform` | host-слой путей/блокировок; engine selection; capability model | ported (тесты + 4 кросс-таргета); wiring: `bsl-flow capability`, unix-гейт legacy |
| `cli/internal/runner` | `Task.Runner.ps1` (journal replay, dedup, cursor, ownership, no-blind-retry) | ported (decision/replay-слой); процессная петля ещё не подключена к CLI |
| `cli/internal/delivery` | `Task.Publication.ps1`/`Task.PublicationGit.ps1`/`Task.Delivery.ps1` (target/authorization, publish-resume, unknown-effect, handoff) | ported (domain над GitPort); реальный git-адаптер и CLI wiring pending |
| `cli/internal/bootstrap` | `Initialize-BSLFlowProject.ps1`/`Update-BSLFlowProject.ps1` (create/merge, comment-preserving) | ported; CLI wiring pending |
| `cli/internal/release` | `Build-BSLFlowCli.ps1`/`Build-BSLFlowPackage.ps1` (сборка/архивы/чексуммы) | ported (`cmd/bslflow-release`, без PowerShell); интеграция в lanes pending |
| `cli/internal/worker` | Codex/ProfiledCodex адаптеры: JSONL-события, rollout-identity (`Get-BFObservedModelEffort`), host-result.json, capability gate, sealing | ported (parsing/classification, процессный спавн pending); byte-parity host-result |
| `cli/internal/counciltransport` | `Council.Transport.ps1` (openai_compatible HTTP: 1 MiB bounds, 60–900s, redirect-отказ, redaction, usage-словарь, observed identity) | ported (18 httptest-loopback тестов); wiring в контроллер pending |
| `cli/internal/specvalidate` | `Test-1CSpec.ps1` (14 lint-правил) + `Test-1CSpecFinal.ps1` v1/v2 (schema/hash/reconciliation проверки) | ported с byte-parity против реального pwsh (20/20 lint фикстур); 3 правила честно не портированы (ConvertTo-Json digest, policy hash вне скоупа, banker's rounding recompute) — см. doc-comment `ValidateFinal` |
| `cli/cmd/bslflow-release` | — | новый PowerShell-free сборщик (4 таргета, детерминированные архивы) |

## Runtime `.ps1` (61 в global/, 63 в scripts/) — замещение по группам

| Группа | Файлы | Статус |
| --- | --- | --- |
| Task engine/process/stages/gates/contracts | Task.Engine/Process/Stages/Gates/Contracts.ps1 | partial: transitions/gates в Go (repository); measure/verify стадии — нативно в `cli/internal/stagehost`; worker-исполнение стадий через windows-ps provider |
| Provider/worker adapters | Task.Provider.ps1, Invoke-BFNativeProvider.ps1, adapters/Codex*.ps1, OpenCode*.ps1 | partial: measure/verify портированы в `cli/internal/stagehost` (native stage host, волна 5); worker-dispatch (inspect/spec/implement/code_review/diagnose/spec_review) остаётся на PS; парсинг/identity/usage/capability/спавн/бюджет/critic в Go (`internal/worker`, волна 6 — библиотека готова, wiring pending); memory-хелпер портирован (`internal/memoryhost`, волна 6) |
| Review/council | Invoke-CouncilReview.ps1, Council.*.ps1, Invoke-1CSpecReview.ps1, Review.Common.ps1, Test-1CSpec*.ps1 | partial: council v2 валидация в Go (controller_spec_review); транспорт в Go (`internal/counciltransport`); lint/final правила в Go (`internal/specvalidate`, byte-parity); цикл/budget/admission в контроллере пока за PS |
| Memory | Task.Memory.ps1, Invoke-BFNativeMemory.ps1, Task.NativeReuse.ps1 | helper ported (`cli/internal/memoryhost`, волна 6, live differential byte-identical); Task.NativeReuse.ps1 — pending (reader reuse-контрактов) |
| Publication/delivery | Task.Publication*.ps1, Task.Delivery.ps1 | partial: контракты в Go (`internal/delivery`); runtime-исполнение pending |
| Runner/queue | Task.Runner.ps1, Invoke-BSLFlowTask.ps1 | partial: replay/decisions в Go (`internal/runner`); петля pending |
| Runtime 1С/native adapter | Task.Runtime.ps1 | pending (Windows-only capability; на unix — BLOCKED_UNSUPPORTED_PLATFORM) |
| Bootstrap/init | 1c-init-project/scripts/*.ps1 | partial: контракты в Go (`internal/bootstrap`); CLI wiring pending |
| Coverage/protected/architecture | Task.Coverage.ps1, Task.Architecture.ps1 | partial: coverage/protected gates уже в Go (controller) |
| Test/build scripts (scripts/*.ps1) | 63 файла suite/build/CI | pending до стадии 7 (retirement): переносятся последними, после green native CI |

## CI

`offline.yml`: job `native-matrix` (macos-14/macos-13/ubuntu-24.04, bash, без pwsh: vet + build + кросс-compile + OS-neutral suites). Проверка: `scripts/Check-CIMatrix.ps1` → `CI_MATRIX_OK`. Windows jobs не менялись. **Не запускался** — удалённый CI требует push (решение владельца).

`native-cli.yml` (волна 5): матрица windows-2025/macos-14/ubuntu-24.04, `go vet` + `go test ./... -timeout 20m`. **Не запускался** на удалённых runner'ах — ожидает push (решение владельца).

## Открытые границы

- Clean-install smoke и native hosted CI на macOS/Linux не выполнялись (требуют реальных runner'ов — push); `native-cli.yml` на удалённых runner'ах тоже ещё не прогонялся.
- Стадия 7 (удаление runtime PowerShell и `.ps1` из репозитория) всё ещё запрещена до differential parity (req 20–21) и green native CI.
- Следующий срез: wiring worker-библиотеки в stagehost execute (worker-ветка `Invoke-BFStageObservation` + расчёт стадий `Task.Stages.ps1` + `Task.ManagedReview.ps1`) с differential-доказательством до переключения роутинга композита.
- `native-cross-platform-cli/verification.md` не создаётся до реального evidence на всех target OS.
