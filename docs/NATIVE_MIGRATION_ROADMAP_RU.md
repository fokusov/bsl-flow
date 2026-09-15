# Роадмап native-cross-platform-cli: очередь задач для агентов

Дата: 2026-09-14 (после волны 6). **Статус 2026-09-15: W7, W8 (код), W9 (a/b/c), REQ8, W10, W11 и W12 выполнены и закоммичены** (ffd8f3a…716b55c + docs); живой статус — [native-migration-inventory.md](native-migration-inventory.md), карточки ниже сохранены как исторические брифы. Осталось: push + CI-матрица и W8 live-приёмка (решения владельца), macOS/Linux smoke (открыт по таргетам), стадия 7 retirement (запрещена до зелёной native CI). Живой статус-трекер — [native-migration-inventory.md](native-migration-inventory.md); этот документ — очередь исполнения: каждая карточка самодостаточна и копируется в бриф агенту целиком. Оценки — агент-часы (включая тесты, differential и интеграцию).

## Как раздавать задачи

- Волны W7 ∥ W8 ∥ W9 параллелятся (непересекающиеся файлы). Корневая сессия интегрирует, гоняет полный `go test ./...` и коммитит по срезу — этот паттерн отработан в волнах 1–6.
- Каждая карточка ниже = один срез (или несколько коммитов внутри волны). Не начинать волну раньше её блокеров.
- Общие ограничения для ВСЕХ карточек (дублировать в бриф):
  1. Байт-четность персистируемых контрактов и BF_*-сообщений; расхождения — классифицировать явно, не маскировать.
  2. Не править `.ps1`; контракт `bsl-flow.native-provider.windows-ps.v1` не переименовывать (rename — отдельный retirement-шаг).
  3. Никакого авто-fallback на PowerShell; native-ошибка = ошибка.
  4. Аргументы процессов — argv-данные, без shell-строк.
  5. Не ослаблять sandbox/paths/authorization/acceptance/unknown-effect гейты.
  6. Verify: `cd cli && go vet ./... && go build ./... && go test ./... -count=1` (полный ~8–10 мин; в разработке — пакеты среза).
  7. Коммитит только корневая сессия.

## Снимок состояния (отправная точка)

Волны 1–6 закоммичены локально (`22066c0…489e3ea`, push не делался). Native-путь композита не запускает pwsh: measure/verify/worker-стадии (`internal/stagehost` + `internal/worker`), memory (`internal/memoryhost`, `__memory`). Дифференциалы есть: memory (byte-identical против живого pwsh), stage prompt (byte-identical), inspect-execute (byte-identical + классифицированные engine-receipts). Скилл `1c-task` переключён на бинарник; бинарник развёрнут в `~/.local/bin` (ec7446ed). Smoke 52/52, PS-сьюты зелёные.

Типизированные BLOCKED на native-пути (то, что осталось портировать): live council engine; unmanaged codex (без execution_profile — by design, не портировать); 1С runtime adapter. Bootstrap/runner/delivery — контракты в Go, wiring pending.

---

## W7 — Live council engine в Go

**Блокеры:** нет (можно стартовать сразу, параллельно W8/W9).
**Оценка:** 8–14 агент-ч. ~3.5k строк PS.

**Цель:** council spec review (обзор спек через API-council) исполняется нативно; убрать типизированный BLOCKED «managed council spec review is not served by the native stage host yet».

**Источники правды (PS):** `global/skills/1c-spec-review/scripts/`: Council.Engine.ps1 (717), Invoke-CouncilReview.ps1 (829), Invoke-1CSpecReview.ps1 (488), Review.Common.ps1 (525), Council.Fallback.ps1 (123), Council.Validation.ps1 (548, частично уже в Go — controller_spec_review v2). Transport НЕ портировать — он готов.

**Строить на:** `cli/internal/counciltransport` (HTTP-транспорт, 18 loopback-тестов), `cli/internal/worker` (budget-хуки, спавн), `cli/internal/stagehost/managed_review.go` (каркас стадии уже портирован — туда встроен profiled critic; council-ветка сейчас BLOCKED), `cli/internal/specvalidate` (final-валидация), openspec/changes/api-specification-council (финальные контракты council v2).

**Состав:** engine-цикл (роли chair/critics, бюджет/адмиссия, фолбэк-политика), адаптация Invoke-1CSpecReview-маршрутизации (lint → council|single-reviewer → final gate) в native-путь, redaction/usage-словари, wired через существующий counciltransport. Single-reviewer OpenCode-режим — через existing worker-механику.

**Приёмка:** юнит+интеграционные тесты с httptest-loopback (как в counciltransport); differential против PS-маршрута на read-only/lint части (без двойных API-вызовов — req 21); после волны переключить скилл `1c-spec-review` на бинарник (SKILL.md + переустановка в ~/.agents/skills) — 0.5–1 ч, отдельным коммитом.

## W8 — 1С runtime adapter + резидуалы

**Блокеры:** нет (параллельно W7/W9). Windows-only capability (req 15): на unix — `BLOCKED_UNSUPPORTED_PLATFORM`.
**Оценка:** 6–10 агент-ч. ~0.8k строк PS.

**Цель:** критерии `native_1c` исполняются native-движком (сейчас verify-ветка отдаёт typed BF_BLOCKED для native_1c/integration/ui).

**Источники правды (PS):** `global/skills/1c-task/scripts/Task.Runtime.ps1` (439), `Task.NativeReuse.ps1` (61), остатки `Task.Architecture.ps1` (524; часть уже в Go: coverage/protected гейты, package identity — при старте волны составить точный остаток). Read-NativeInventory — по факту (упомянут в ранних оценках).

**Строить на:** `cli/internal/stagehost` (verify-ветка, typed BF_BLOCKED-места), `cli/internal/platform` (capability model), существующий Windows FILE-профиль (BFI002/BFI003 acceptance как контракт-образец).

**Приёмка:** Windows-focused native 1С evidence (req: отсутствие на macOS/Linux — ожидаемый BLOCKED, не PASS); differential read-only частей против PS; process-receipts через существующую process-механику stagehost.

## W9 — Operational wiring: runner / delivery / bootstrap

**Блокеры:** нет (параллельно W7/W8).
**Оценка:** 6–10 агент-ч. ~1.2k строк PS wiring (контракты уже портированы).

**Цель:** стадии operational paths (этап 5 спеки) работают через CLI без pwsh.

**Состав:**
1. **Runner-петля:** `Task.Runner.ps1` (251) — процессная петля поверх готового decision/replay-слоя `cli/internal/runner`, подключить к `bsl-flow runner run`.
2. **Delivery/git:** `Task.Publication.ps1` (244) + `Task.PublicationGit.ps1` (583) + `Task.Delivery.ps1` (138) — реальный git-адаптер (git CLI subprocess, clean-env, bounded) над домен-слоем `cli/internal/delivery`; publish/publish-resume wiring в CLI уже частично есть — проверить покрытие.
3. **Bootstrap wiring:** `internal/bootstrap` уже портирован — подключить `bsl-flow init/upgrade`-команды (комментарий-preserving merge по контракту).
4. **Скилл `1c-init-project`** → бинарник (после п.3), отдельный коммит.

**Приёмка:** integration-тесты на temp-репо (journal replay, publish-resume после unknown effect, idempotent bootstrap); без model/API-вызовов.

## REQ8 — `.exe`-релаксация путей (спека п.8)

**Блокеры:** нет; маленький срез, вставить в любой зазор.
**Оценка:** 1–2 агент-ч.

Пути/конфиг-схемы принимают platform-native абсолютные пути без обязательного `.exe`; исторические Windows v1 запросы остаются читаемыми (dual-reader). Места: контракты stagehost/repository, capability-схемы. Приёмка: юнит-тесты на оба мира + фикс daльнейшего отсутствия регресса в существующих сьютах.

## W10 — Консолидированный parity-харнес + shadow mode (req 20–21)

**Блокеры:** после W7–W9 (чтобы покрытие было полным).
**Оценка:** 4–8 агент-ч.

Собрать разрозненные дифференциалы (memory, prompt, inspect-execute, council — из W7) в один frozen-trace харнес: одинаковые trusted inputs → сравнение envelopes/journals/hashes; расхождения классифицируются как schema/behavior change. Shadow mode: только read/decision-computation, без writes/model-вызовов/тестов; без повторного исполнения side-effecting действий (req 21 — сравнение по frozen receipt).

## W11 — Default-native switch + процесс-аудит + clean-install smoke (Windows)

**Блокеры:** W10 зелёный; желательно push + зелёная CI-матрица (решение владельца).
**Оценка:** 4–6 агент-ч.

No-pwsh процесс-аудит полного lifecycle на чистой Windows-машине (clean-install release archive, help/version/bootstrap/task lifecycle; process audit без pwsh), PS-free упаковка. Это requirement 22 для Windows-scope; macOS/Linux smoke — отдельное решение владельца.

## W12 — Docs / verification.md / CHANGELOG

**Блокеры:** W11.
**Оценка:** 2–4 агент-ч.

Финализация: inventory/CHANGELOG, решение по `verification.md` (спека запрещает создавать до evidence на всех target OS — либо Windows-scoped с явной границей, либо change остаётся открытым до multi-OS фазы; решение владельца).

## CI — Push + первый прогон матрицы mac/linux

**Блокеры:** решение владельца (push).
**Оценка:** 2–6 агент-ч фиксинга (неопределённо — зависит от того, что покажет первый прогон `.github/workflows/native-cli.yml` на macos-14/ubuntu-24.04; ожидаемы падения по путям/скипам).

## Стадия 7 — Retirement (отдельный milestone)

**Блокеры:** зелёная native CI-матрица (спека п.23: запрещено раньше parity + green CI).
**Оценка:** 25–40 агент-ч (~1.5–2 дня оркестровки).

Runtime-PS removal (default и compatibility поставки без `.ps1`/legacy-exe, help/docs без pwsh) → затем выкидывание всех `.ps1` из репо после переноса coverage 64 test/build-скриптов `scripts/`. Контрактный rename `windows-ps.v1 → go.v1` + dual-reader — здесь же.

---

## Сводная таблица

| Этап | Оценка, агент-ч | Блокеры | Статус 2026-09-15 |
| --- | --- | --- | --- |
| W7 council engine | 8–14 | — | **готово** (ffd8f3a) |
| W8 1С runtime adapter | 6–10 | — | **код готово** (daf4205, d325634); live-приёмка PAUSED владельцем |
| W9 runner/delivery/bootstrap | 6–10 | — | **готово** (a05dc76, 0cb2ed8, 31470c1) |
| REQ8 .exe-пути | 1–2 | — | **готово** (b55212a) |
| W10 parity+shadow | 4–8 | W7–W9 | **готово** (1007705) |
| W11 default-native+smoke | 4–6 | W10, (push) | **готово, Windows-scope** (716b55c); macOS/Linux smoke открыт |
| W12 docs/verification.md | 2–4 | W11 | **docs готовы**; verification.md — решение владельца |
| CI push+фикс матрицы | 2–6 | решение владельца | открыто |
| **Итого до Windows default-native** | **33–60** | | **достигнуто (Windows-scope)** |
| Стадия 7 retirement | 25–40 | зелёная CI | заблокирована до push+CI |

Wall-clock при параллельной оркестровке (W7∥W8∥W9): ~2–4 рабочих дня до default-native; retirement ещё ~1.5–2 дня. Ограничители: usage-лимиты агентов, решения владельца (push, macOS/Linux smoke, форма verification.md).
