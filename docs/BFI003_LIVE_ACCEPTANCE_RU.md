# Живая приёмка BFI-003 (сценарий 5.3): Codex + cc-1c-skills

Дата: 2026-09-10. Статус: **PASS** для source-only синтетического контура. Это не runtime-приёмка и не бизнес-пилот 1С.

## Проверяемое требование

BFI-003 `pre-dispatch gate`: контроллер до оплачиваемого модельного вызова закрепляет точный интерпретатор Python и обязательные пакеты, запускает bounded preflight и блокирует dispatch при drift/отсутствии, а фактический cc-вызов использует именно закреплённый executable. Сценарий 5.3 дополнительно проверяет, что модель применяет ровно выбранный skill и не смешивает native skills/MCP с выбранным toolset (BFI-010).

## Идентичности окружения

| Компонент | Значение |
| --- | --- |
| Интерпретатор | `C:\Users\ifokusov\.cache\codex-runtimes\codex-primary-runtime\dependencies\python\python.exe` |
| Python SHA-256 | `372c2eae555b344520bf147be0096e009069aeca4e7f78d6aecea6d53158056a` |
| Версия Python | `3.12.14` |
| lxml | `6.1.1` |
| Native Codex | `...\codex-win32-x64\vendor\x86_64-pc-windows-msvc\bin\codex.exe`, `codex-cli 0.154.0` |
| Codex SHA-256 | `be96b992178b1e467c225800da0d65f2c86d5eba1ef0b14632f65db381cbdfde` |
| Toolset `cc-1c-skills` | `work/benchmark-integration/cc-1c-skills`, aggregate SHA-256 `efc68ce680ebbdcdde85f13d3d5cde1701403dba915c3aa2d5fe3cce3d310433` |
| Закреплённый inventory (override-набор адаптера) | 21 skill, SHA-256 `3e6c5c845021e0d53e425b2ec29a613e42077032f404cfa763bee4bf3d717aff` |

## Маршрут и наблюдения

Вызов шёл через общий `Invoke-BFManagedWorker`: `Test-BFRuntimePreflight` → `Assert-BFBudgetAdmission` + reservation → `Invoke-BFProfiledCodexWorker` → `Complete-BFBudgetDispatch`, один dispatch `inspect` с моделью `gpt-5.6-luna`.

- Preflight (контроллер): declared и observed совпали — `python.exe`, `3.12.14`, `lxml 6.1.1`.
- Sandbox capability: `source_write=denied`, `controller_read/write=denied`, `config_read=allowed`, sandbox/native SHA-256 равны закреплённым.
- Model read the selected skill and executed the pinned interpreter (raw `command_execution`), например:
  `"C:\Users\ifokusov\.cache\codex-runtimes\codex-primary-runtime\dependencies\python\python.exe" "...\cc-1c-skills\cfe-validate\scripts\cfe-validate.py" "...\synthetic\Configuration.xml"`
- Итог модели: `status=completed`, «Скрипт фактически выполнен закреплённым Python; exit code: 1. Валидация XML не началась: cfe-validate.py сообщил об отсутствии обязательного аргумента -ExtensionPath/-Path.» — ожидаемый fixture outcome для синтетического неполного XML, а не ошибка framework.
- Skills не смешаны: `inventory=21`, `disabled=21`, MCP-инструментов воркеру не выставлено (`mcp-inventory.tools=[]`); глобальные MCP-имена спроецированы и запрещены.
- Ledger: reservation до вызова и outcome после, `cost_state=unknown`, `reported_cost_usd=null`, usage сохранён (input 62890 / output 1717 / reasoning 501 / cached 47104). Для Codex без денежной цены это ожидаемо: лимит не enforced, USD не выдуман.

## Артефакты

- Каталог попытки: `work/benchmark-integration/live-bfi003-codex-cc/run-3ef74a933758` (ignored), манифест `evidence-manifest.json` со SHA-256 каждого файла.
- Драйвер: `work/benchmark-integration/live-bfi003-codex-cc/driver.ps1` (`-Phase probe|live`, `-SkillsHash`).
- Заморозка: `work/benchmark-integration/live-bfi003-codex-cc/freeze-evidence.ps1`.
- Offline-регрессии gate: `scripts/Test-TaskRuntimePin.ps1` (23 checks), `scripts/Test-TaskBudgetGate.ps1` (22 checks).

## Границы доказательства

- Синтетический source-only прогон; 1С runtime, база, бизнес-логика и EPF/ERF не проверялись.
- «Использование закреплённого executable» подтверждено сохранённым потоком команд и ревьюируется вручную; автоматического перехвата/запрета произвольных shell-команд в модели нет.
- Один реальный Luna-вызов; стоимость Codex остаётся `unknown`. Это не benchmark и не сравнение моделей.
- Дополнительно живой прогон выявил и исправил интеграционный дефект: preflight писал в dispatch-каталог адаптера и ложно трактовался как partial dispatch. Теперь controller-side evidence content-addressed под задачей; регрессия покрыта `Test-TaskRuntimePin.ps1`.
