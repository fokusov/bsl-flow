# Живая приёмка BFI-002 (сценарий 5.2): sealed Codex critic

Дата: 2026-09-10. Статус: **PASS** для контракта sealed critic. Source-only синтетический контур; не runtime-приёмка и не бизнес-пилот 1С.

## Проверяемое требование

BFI-002: реальный subscription critic просматривает только attached payload, не получает инструментов (нулевое число tool definitions, включая `additional_tools`), не эмитит tool events, возвращает валидный review, проходящий общий lint/review/invariant gates. Повтор не должен делать нового модельного запроса, изменение catalog bytes должно блокировать cache. Локальный mock capture не считается приёмкой.

## Идентичности окружения

| Компонент | Значение |
| --- | --- |
| Native Codex | `...\codex-win32-x64\vendor\x86_64-pc-windows-msvc\bin\codex.exe`, `codex-cli 0.154.0` |
| Codex SHA-256 | `be96b992178b1e467c225800da0d65f2c86d5eba1ef0b14632f65db381cbdfde` |
| Модель | `gpt-5.6-luna` (reviewer), effort `medium` |
| Pinned runtime (тот же контракт, что BFI-003) | Python `3.12.14`, lxml `6.1.1` |
| Toolset | `cc-1c-skills`, aggregate SHA-256 `efc68ce680ebbdcdde85f13d3d5cde1701403dba915c3aa2d5fe3cce3d310433` |
| Sealed capability | `codex-0.154.0-luna-direct-empty-tools-v1` |

## Маршрут

`Invoke-BFSpecReviewStage` → `Invoke-BFProfileSpecCritic` → `Invoke-BFManagedWorker 'spec_review'` → `Invoke-BFProfiledCodexWorker` (critic) → `Complete-BSLFlowReview`; затем обычная стадия `spec_reconcile` через `Invoke-BFManagedWorker`.

## Наблюдения

- **Zero tools в запросе:** per-attempt `critic-catalog.json` — `shell_type=disabled`, `apply_patch_tool_type=null`, `tool_mode=direct`, `experimental_supported_tools=[]`; binding фиксирует `critic_capability=codex-0.154.0-luna-direct-empty-tools-v1` и catalog hashes.
- **Нет tool events:** поток критика содержит единственный `agent_message` и ноль `command_execution|file_change|mcp_tool_call`.
- **Валидный review:** `spec-lint.passed=true`; `Complete-BSLFlowReview` принял payload — `reviewer_verdict=BLOCK`, `verdict=BLOCK`, вес 3.4, finding `R-001` (`blocker`, отсутствует литерал Example greeting). Инварианты входов (original-task/spec/design sha) сохранены.
- **Replay без модели:** повторный вызов стадии (resume после исправления preflight) прошёл через critic cache — `process.json`/`stdout.txt` критика не изменились (mtime `20:10`), нового модельного запроса не было; новая модель вызывалась только для `spec_reconcile`.
- **Reconciliation gate:** `spec_reconcile` вернул `needs_input` с валидным payload, который принимает `R-001` и требует недостающий литерал; приёмка/final-validation не сфабрикованы. Это корректное поведение gate на намеренно неполной fixture.

## Артефакты

- Каталог: `work/benchmark-integration/live-bfi002-sealed-critic/run-12f536acb636` (ignored), манифест `evidence-manifest.json` со SHA-256 каждого файла.
- Драйвер: `driver.ps1`; резюм после исправления preflight: `continue.ps1`; заморозка: `freeze-evidence.ps1`.
- Тот же живой прогон вскрыл и подтвердил исправление второго интеграционного дефекта preflight (коллизия probe-каталога при двух dispatch в одной задаче); регрессия — `Test-TaskRuntimePin.ps1`.
- Offline-регрессии sealed-контракта (replay cache, изменение catalog bytes, отсутствие tool events): `scripts/Test-BSLFlowProfiledCodex.ps1` (88 checks).

## Границы доказательства

- Ровно одна fixture; review закономерно `BLOCK`, поэтому `review-reconciliation.json` и `final-validation.json` не создавались. Контракт sealed critic (BFI-002) подтверждён; полноценный completed-проход `spec_review` с final validation — отдельная задача с однозначной спецификацией.
- Подтверждено на `gpt-5.6-luna` и закреплённом native executable; другая модель/каталог требуют отдельной проверки.
- Не 1С runtime, не business/EPF/UI приёмка. Стоимость Codex остаётся `unknown`.
