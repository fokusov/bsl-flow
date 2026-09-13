# Контрольная точка интеграций — 2026-09-10

По последующей просьбе пользователя дефекты, приоритеты и критерии закрытия оформлены отдельно: [BENCHMARK_FINDINGS_RU.md](BENCHMARK_FINDINGS_RU.md). Это дополнение документации, не возобновление реализации или тестов.

**Работа приостановлена по просьбе пользователя из-за лимитов Codex.** Не запускать модели, субагентов, тесты или продолжение задач до новой команды пользователя. Это незавершённая интеграция, не релиз и не результат 1CLLMBenchTasks.

## Репозиторий и сохранность

- Корень: `C:\DEV\BSL Flow`.
- Ветка: `codex/managed-sdlc`; HEAD: `9ab671077da71575164479ab067d95c9cf1c509f`.
- Изменения интеграций остаются в рабочем дереве, включая новые untracked файлы. Commit/push этой фазы не выполнялись.
- Основной отчёт: [BENCHMARK_INTEGRATIONS_RU.md](BENCHMARK_INTEGRATIONS_RU.md). Этот checkpoint имеет приоритет над более ранними формулировками статуса в документах.
- Raw evidence, снимки скиллов, синтетические проекты и скрипты: `work/benchmark-integration/` (ignored). Не удалять, не публиковать целиком и не заменять старые попытки.
- Активный субагент `codex_profile_finish` прерван. Последняя работа: подготовка исправления case-sensitive MCP JSON. Это исправление **ещё не подтверждено**; последняя подтверждённая серия ProfiledCodex — 88 offline checks.
- Оставшийся offline runner остановлен по точной идентичности процесса. Receipt: `work/benchmark-integration/pause-owned-processes.json`.

## Цель и ограничения

Матрица: OpenCode + `deepseek/deepseek-v4-flash` и Codex + `gpt-5.6-luna`, каждый с Unica и cc-1c-skills под управлением BSL Flow. Задания ЗУП исключены. Текущая фаза — интеграции на синтетических файлах; задания корпуса ещё не запускались.

Бюджет DeepSeek — желательно до $10, небольшое превышение разрешено; Luna без отдельного денежного лимита. Нельзя публиковать содержимое базы, credentials и private cache. Перед будущим бенчмарком нужен backup `C:\BASES\DEMO\bp1`. В этой фазе база не копировалась и вызовы 1С к ней не выполнялись. Запрет Unica durable runtime jobs остаётся в силе.

## Что уже доказано

| Проверка | Фактический результат |
| --- | --- |
| OpenCode transport | Native 1.18.30, stdin prompt, JSONL, exact Flash model request, effort null |
| OpenCode source write / replay | Синтетический файл создан; cached replay разобран без нового модельного вызова |
| OpenCode + Unica | Модель прочитала выбранный cfe-validate SKILL.md и реально вызвала `unica_unica_cfe_validate` на созданном с нуля неполном XML |
| OpenCode + cc-1c-skills | Третий smoke вызвал выбранный Python cfe-validate.py на синтетическом XML, используя заранее проверенный интерпретатор и правильный worker directory |
| Codex skill inventory | Native skills/list: 20 автоматически найденных скиллов; отключение всех 20 по точным путям подтверждено |
| Codex + Luna | `synthetic-cc-4`: одна реальная модельная сессия прочитала synthetic.txt, вернула value=73; post-inventory прошёл |
| Codex critic tools | Локальный capture-3: baseline 3 tool definitions, custom catalog 0; проверены вложенные AdditionalTools, без inference |
| Filesystem boundary | `published-isolation-probe-1`: parent/child чтение и запись synthetic private запрещены, source write разрешена |
| Полный CLI №1 | Дошёл до spec review/reconcile; остановился на запрете доступа к scratch через OpenCode tool policy |
| Полный CLI №2 | Прошёл spec review/reconcile и implementation; code review правильно заблокировал недостаточное покрытие требования |

Ошибки в неполном XML ожидаемы: 2 errors / 1 warning. Это доказательство вызова валидатора, не валидности расширения и не native 1C runtime PASS.

## Проверки и стоимость

Новые offline suites: ToolsetSnapshot 11; OpenCodeTransport 15; OpenCodeEvents 12; OpenCodeWorker 10; ProcessEnvironment 7; ExecutionProfile 104; ManagedReview 6; ProfiledCodex 88 — **253 проверки**. Они выполнялись на последовательно дорабатываемых версиях файлов; это не единый полный зелёный CI последнего рабочего дерева.

Go tests и воспроизводимая сборка прошли. Отдельный CLI smoke — 23 PASS. Сохранены бинарники:

- `work/benchmark-integration/bin/bsl-flow-opencode.exe`, SHA-256 `86b1210a327ad04af24e3a240b5abe200358f0d92b542e65d020ba3c332cb1d5`.
- `work/benchmark-integration/bin/bsl-flow-opencode-2.exe`, SHA-256 `92e4aee0ae8cb87dcff63dd3fddca66d1af7fb31c948f08e7d4ff71ca9554ff8`.

Бинарники содержат более ранние снимки пакета; они не включают все последние изменения Codex critic/RPC. Старый `cli/bin/bsl-flow.exe` не заменялся.

**19 завершённых OpenCode-сессий сообщили суммарно 0.0493475808 USD**, включая неудачные попытки и оба CLI-прогона. Это provider-reported cost, не сверка с биллингом. Реальных Luna-сессий — одна (`synthetic-cc-4`); probes inventory/capture не являются модельными сессиями. Ledger: `work/benchmark-integration/integration-cost-20260910.json`; сборщик: `scripts/Get-BSLFlowOpenCodeCost.ps1`.

Полный offline package run на `offline-package-snapshot` прошёл lifecycle 51, hardening 24, resume 16, crash recovery 17, repair 36, delivery 10, review reliability 127 и последующие runner/native/coverage suites. Остановился на filesystem permission к локальному publication cache. Логи: `offline-package-check-2.log`. Продолжение не повторяло уже прошедшие suites: `scripts/Continue-PackageCheck.ps1` в ignored snapshot, оставшиеся PublicationGit/TaskPublication и финальная часть package checks. PublicationGit успел завершиться, оставшееся продолжение остановлено по просьбе пользователя; **общий package PASS не заявляется**. Лог: `offline-package-continuation.log`. Ранний `offline-package-check.log` отражает ошибку копирования трёх Unicode-файлов snapshot; она исправлена перед run-2.

## Выявленные контракты и исправления

1. Native Codex sandbox требует elevated backend; штатная UAC-настройка уже выполнена. `:root=none` сам по себе не доказал запрет чтения. Явные deny-каталоги работают. Запрет родителя с reopening вложенного worker не работает. Разделены source, controller tasks, config и scratch.
2. Deny всего `C:\BASES` привёл к timeout preflight; профиль с конкретным `C:\BASES\DEMO\bp1` прошёл. Применять только обоснованные конкретные private roots. Это не default-deny всего диска и не доказанная защита от exfiltration.
3. Чистому Windows environment нужны стандартные системные переменные. Произвольные секреты родительского процесса не наследуются OpenCode worker. DeepSeek key передаётся через environment, не argv/config; сам host имеет к нему доступ.
4. OpenCode требует XDG_CONFIG/DATA/CACHE/**STATE**, заранее созданный config/opencode/.gitignore, explicit `--dir` и точную рабочую директорию в prompt. Skill snapshot и scratch имеют explicit external_directory permissions.
5. cc Python по умолчанию не имел lxml. Первый smoke вышел за сценарий, разыскивая другой Python, и не принят. Второй сделал запрещённый glob выше worker и не принят. Третий прошёл с Python `C:\Users\ifokusov\.cache\codex-runtimes\codex-primary-runtime\dependencies\python\python.exe`, Python 3.12.14, lxml 6.1.1. Полноценная привязка этой зависимости к воспроизводимому benchmark environment ещё нужна.
6. Публичный Unica bootstrap требует write только в runtime_cache/.locks даже при готовом runtime. Runtime executable tree остаётся read-only; установка/замена runtime и durable jobs не разрешались.
7. OpenCode receipt cost должен иметь стабильное JSON-представление при replay. Добавлены сохранение model-result.json для Resume и сверка cached result с raw JSONL. Cached critic payload также сверяется с проверенным результатом.
8. В Codex app-server `mcp_servers={}` не очищает глобальную конфигурацию. Нужны config/read, проекция только имён и per-name enabled=false до MCP inventory. Dotted override keys должны быть bare `[A-Za-z0-9_-]+`; quoted key создавал другое имя. После whole-table регистрации Unica запреты необходимо применить снова. Worker exec использует --ignore-user-config и не получает частичные глобальные definitions.
9. `features.multi_agent=false` недостаточно при model metadata v1: добавлены `agents.enabled=false` и multi_agent_v2=false. Apps/plugins отключены.
10. Native Luna usage дополнительно содержит cache_write_input_tokens и reasoning_output_tokens; это optional nonnegative integer fields. `synthetic-cc-4` первоначально остановился на этом parser mismatch, затем raw stream разобран offline. Старые binding/receipts не переписывались.
11. RPC cleanup сохраняет исходную ошибку и выполняет bounded stop/dispose даже при сломанном stdin pipe. Добавлена безопасная диагностика malformed MCP response, без raw config и exception message.
12. Для sealed Luna critic создан per-attempt catalog: shell_type disabled, apply_patch_tool_type null, tool_mode direct; остальная metadata сохранена. Explicit tool switches и no-tools event guard. Catalog source/bytes привязаны hashes, global cache refresh не меняет сохранённую попытку. Prompt только attached, без выбранных скиллов/MCP даже при Unica-профиле. Локальный request capture и source audit выполнены; **реальный subscription critic ещё не проверен**.

## Главный незакрытый дефект Codex + Unica

`codex-managed/synthetic-unica-4/.bsl-flow/tasks/smoke/mcp-rpc/malformed-response.json` доказал:

```text
error_id = KeysWithDifferentCasingInJsonString
exception_type = System.InvalidOperationException
method = mcpServerStatus/list
phase = 3
line_characters = 32996
```

Это валидный JSON со schema keys, различающимися регистром, который PowerShell ConvertFrom-Json без AsHashtable отвергает. Следующее исправление: case-preserving RPC parse и dictionary-safe enumeration `server.tools`; регрессия Name/name. Не сохранять raw config/read или credentials для диагностики. Последний агент был прерван при этой задаче: проверить фактический diff перед продолжением, не считать исправление готовым. Unica5 не запускался.

## Точная точка восстановления CLI-задачи

- Project: `work/benchmark-integration/cli-source-2`.
- Task: `7bc18343-1f9c-447a-9f64-e920d8845483`.
- Последний подтверждённый update принят: **revision 12**, status ready, next_stage inspect. После update `task run` не запускался.
- `current.json` — pointer `{revision,sha256}`, не полный state. State читается через journal/revisions, а не напрямую из pointer.
- Сохранённый update: `cli-fixture/coverage-update-2-protected.json`. Ранее rejected `coverage-update-2.json` и `coverage-update-2-encoded.json` не изменяли state.
- В worker добавлен оператором защищённый `tests/Test-SyntheticModule.ps1`, baseline commit не заменён. Проверяет strict UTF-8, полные три строки без отступов, final newline; JUnit `.bsl-flow-worker/exact-source.junit.xml`, test id exact_synthetic_source. Негативные offline cases missingdecl/missingExport/extra-tail прошли. Existing source сейчас соответствует исходной функции без отступов; модель ещё должна пройти свежие gates.
- Full requirement coverage связывает новый static criterion с исходным требованием. Обновление инвалидировало старые evidence; нельзя переносить старые PASS в новую intent revision.
- Не перезапускать `Invoke-CliLifecycle.ps1` для этой задачи: он создаёт новую task. Продолжение существующей — `bsl-flow-opencode-2.exe task run --project <cli-source-2> --task 7bc18343-1f9c-447a-9f64-e920d8845483` после команды пользователя, через разрешённый outer context. Затем отдельно проверить journal, acceptance receipt и свежие evidence.
- Fixture generator/README ещё требуют сверки с последними поправками root к тесту (убран необоснованный отступ Return) и сохранённому protected update.

## Порядок следующей работы после разрешения продолжить

1. Прочитать checkpoint и фактический Git diff; проверить, что нет активных старых процессов. Не возобновлять автоматически.
2. Завершить AsHashtable/dictionary fix и тесты, затем новый synthetic Codex + Unica smoke. Не повторять старые partial attempts и не исправлять их receipts задним числом.
3. Проверить реальный Luna sealed critic и применение cc-скилла из Codex. Локальный capture-3 — полезное доказательство, но не модельная приёмка.
4. Продолжить указанную CLI task revision 12; доказать весь lifecycle с полноценным JUnit-критерием. Сохранить оба прежних неуспешных прогона в учёте.
5. Провести одинаковые lifecycle/repair/recovery проверки четырёх профилей; при необходимости построить новый binary после freeze, сохраняя старые.
6. Закрепить Python/runtime dependency и полный учёт бюджета всех этапов; обновить docs/status, выполнить оставшиеся package checks и targeted checks последних изменений. Перед release требуется независимое ревью последнего combined diff.
7. Только затем backup bp1 и реальные benchmark tasks. Не публиковать raw каталоги. Не объявлять framework/benchmark готовыми до соответствующих runtime доказательств.
