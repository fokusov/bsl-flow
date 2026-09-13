# План исправлений по результатам интеграционного бенчмарка

Статус: план работ, 2026-09-10. Основание — [реестр дефектов](BENCHMARK_FINDINGS_RU.md) и [контрольная точка](BENCHMARK_CHECKPOINT_2026-09-10.md). Этот документ не возобновляет модельные прогоны, не разрешает действия с информационной базой и не объявляет текущее рабочее дерево принятым.

## Цель

Довести managed-интеграции OpenCode/Codex с Unica и cc-1c-skills до воспроизводимой приёмки на одном зафиксированном пакете, после чего отдельно открыть реальный benchmark.

План закрывает BFI-001–005 и повторно подтверждает BFI-006–014. Исправления BFI-006–014 уже находятся в рабочем дереве: их не нужно реализовывать заново, но нельзя переносить на них старые PASS без проверки итогового combined snapshot.

## Неизменяемые границы

- Сохранять старые attempts, receipts, raw JSONL, журналы и бинарники; не исправлять историю задним числом и не повторять partial attempt в том же каталоге.
- Не публиковать credentials, содержимое базы, `config/read`, private cache и полный `work/benchmark-integration/`.
- Не использовать Unica durable runtime jobs и не выполнять runtime-действия с 1С в рамках закрытия интеграционных дефектов.
- Не считать synthetic exact-source JUnit проверкой бизнес-логики 1С.
- Не считать provider-reported cost фактическим биллингом и не выводить цену модели из её имени.
- Каждый новый запуск получает новый каталог, immutable binding и идентичность исходников, host, toolset, runtime и budget state.

## Изменённый порядок работ

Реестр предлагает сначала живые Codex-проверки, а затем закрепление среды и бюджета. В реализации порядок нужно изменить: BFI-003 и BFI-005 становятся pre-dispatch gates до новых платных smoke. BFI-001 и BFI-004 можно разрабатывать параллельно с этими воротами, но живую приёмку начинать только после их общего offline PASS.

## Этап 0. Зафиксировать исходную точку

Задачи:

1. Снять manifest текущего combined working tree с `core.quotePath=false`, включая tracked, modified и untracked файлы, но исключая private evidence.
2. Сверить фактический diff с BFI-006–014 и отметить для каждого пункта изменённые файлы и существующие регрессии.
3. Проверить отсутствие принадлежащих предыдущему прогону процессов по сохранённому receipt `pause-owned-processes.json`; ничего не завершать по имени процесса.
4. Сохранить отдельную карту `BFI → код → тест → live evidence → статус`.

Результат: однозначный исходный snapshot и список уже внесённых изменений без утверждения об их приёмке.

Критерий выхода: любой последующий результат можно связать с SHA-256 manifest; Unicode-пути и untracked исходники не потеряны.

## Этап 1. Ввести обязательные pre-dispatch gates

Статус на 2026-09-10: offline-реализация 1.1 и 1.2 внесена в рабочее дерево и покрыта регрессиями; живая приёмка (пункты закрытия в 5.3) остаётся открытой и не заменяется этими проверками.

- Контракт `execution_profile.runtime` (`executable`, SHA-256, точная версия, обязательные пакеты с версиями; `lxml` обязателен) и `budget` (`currency`, `limit`, `reservation`) добавлены в `request.schema.json` и в контроллер `Assert-BFRequest`/`Assert-BFExecutionProfile`/`Assert-BFBudget`.
- Общий путь `Invoke-BFManagedWorker` выполняет `Test-BFRuntimePreflight` и `Assert-BFBudgetAdmission` до любого provider dispatch и `Complete-BFBudgetDispatch` после него; логика не дублируется в адаптерах.
- Runtime входит в permission profile и в binding `Get-BFExecutionDependencies`; drift исполнителя или пакета блокирует до модельного вызова.
- Durable ledger `.bsl-flow/tasks/<id>/budget/ledger.json` хранит reservation/outcome раздельно (`reported_cost_usd`, `billed_cost_usd`, `usage`, requested/observed model, `cost_state`). Открытая reservation или unknown cost при заданном лимите блокируют следующий оплачиваемый dispatch; replay идемпотентен по dispatch key.
- Offline-регрессии: `scripts/Test-TaskRuntimePin.ps1` (22 checks) и `scripts/Test-TaskBudgetGate.ps1` (22 checks); обе включены в `.github/workflows/offline.yml`. `Test-TaskExecutionProfile.ps1` расширен до 130 checks (schema, runtime, budget, binding).

### 1.1 BFI-003 — закреплённый runtime cc-1c-skills

Основные области изменения:

- `global/skills/1c-task/schemas/request.schema.json`;
- `global/skills/1c-task/scripts/Task.Execution.ps1`;
- `global/skills/1c-task/adapters/OpenCode.ps1`;
- `global/skills/1c-task/adapters/ProfiledCodex.ps1`;
- `scripts/Test-TaskExecutionProfile.ps1` и профильные adapter tests.

Минимальный контракт:

- для `cc-1c-skills` профиль содержит точный абсолютный путь Python, SHA-256 executable, наблюдаемую версию Python и список обязательных пакетов с версиями; для текущего benchmark обязателен `lxml`;
- для Unica этот блок запрещён, поскольку её runtime уже имеет отдельный контракт;
- до модельного dispatch контроллер запускает только bounded environment preflight, сверяет `sys.executable`, версию и импорт/версию `lxml`, затем сохраняет очищенный результат и его hash;
- runtime добавляется в read-only sandbox inputs и в binding попытки; controller не ищет альтернативный Python, не устанавливает зависимости и не сканирует соседние проекты;
- worker получает точный interpreter через managed prompt/environment. Для приёмочного cc-вызова evidence должно показывать использование именно закреплённого executable.

Регрессии:

- верный runtime проходит preflight без модели;
- отсутствующий файл, иной hash, несовпадающая версия Python, отсутствие/иная версия `lxml` блокируют до model call;
- запрещены PATH fallback, автоматический поиск и установка;
- изменение runtime invalidates binding и не переиспользует cache;
- оба provider-профиля получают один и тот же runtime contract.

### 1.2 BFI-005 — общий ledger и admission control

Основные области изменения:

- trusted request/schema и controller state;
- общий путь перед `Invoke-BFManagedWorker`, а не независимая логика в двух adapters;
- provider receipts и recovery/replay;
- `scripts/Get-BSLFlowOpenCodeCost.ps1` как offline reconciliation tool.

Минимальный контракт:

- trusted request явно задаёт денежный лимит и резерв нового dispatch; значения не вычисляются из названия модели;
- перед каждым оплачиваемым вызовом контроллер пересчитывает cumulative ledger всех завершённых и неуспешных попыток задачи;
- reported cost, billed cost, token usage, requested model и observed model хранятся раздельно;
- отсутствие terminal cost evidence означает `unknown`, а не `0`; unknown блокирует следующий оплачиваемый dispatch до reconciliation;
- replay завершённой попытки не добавляет расход и не вызывает provider;
- reservation создаётся атомарно до вызова, outcome дописывается после него; crash между ними оставляет расход неопределённым и блокирует продолжение;
- admission control гарантирует запрет следующего вызова при недостаточном остатке, но не называется жёстким billing cap внутри уже начатой provider session.

Регрессии:

- несколько stages/tasks, failed attempt, retry/recovery и cached replay;
- session без `step_finish` остаётся unknown;
- duplicate session/part не учитывается дважды;
- исчерпание лимита блокирует до model call;
- восстановление controller state даёт тот же cumulative total;
- Luna без известной денежной цены сохраняет usage и `reported_cost: unknown`, без выдуманного USD.

Критерий выхода этапа 1: cc runtime drift и budget exhaustion/unknown cost останавливают выполнение до оплачиваемого dispatch; offline fixtures подтверждают crash/replay semantics.

## Этап 2. Исправить host-протоколы и диагностику

### 2.1 BFI-001 — case-preserving Codex RPC

Основные области изменения:

- `global/skills/1c-task/adapters/Codex.Skills.ps1`;
- `global/skills/1c-task/adapters/ProfiledCodex.ps1`;
- `scripts/Test-BSLFlowProfiledCodex.ps1` и отдельные bounded RPC fixtures.

Изменение:

1. Ограничить новый parser только Codex RPC: проверять bounded JSON и exact duplicate keys ordinal-сравнением, затем разбирать с `ConvertFrom-Json -AsHashtable`.
2. Разрешить разные ключи `Name` и `name`, но отвергать точный повтор одного ключа на любом уровне.
3. Перевести чтение RPC-объектов и перечисление `server.tools` на `IDictionary`-safe helpers. Не менять глобальную canonical JSON policy, которая намеренно строже для trusted state.
4. Сохранить текущую безопасную диагностику: только method, phase, размеры, hash и тип ошибки; без raw line, exception message, config и credentials.

Регрессии:

- fixture с `Name`/`name` успешно разбирается без потери обоих полей;
- exact duplicate, malformed JSON, неправильный id/phase и неожиданный MCP tool блокируются;
- dictionary/object traversal даёт одинаковый нормализованный allowlist;
- cleanup сохраняет первичную ошибку при сломанном stdin.

### 2.2 BFI-004 — точная диагностика filesystem capability

Основные области изменения:

- `Test-BFExecutionCapability` в `Task.Execution.ps1`;
- `Task.Process.ps1` и process receipt, только если текущих полей недостаточно;
- `scripts/Test-BSLFlowBenchmarkIsolation.ps1` и synthetic fixtures.

Изменение:

- сохранять результат каждого шага preflight: version checks, setup, sandbox start, parent/child observation, parse/compare;
- фиксировать elapsed, stop reason/exit code, process identity, backend, hashes permission profile и executables;
- различать как минимум `setup_failed`, `timeout`, `process_failed`, `observation_mismatch` и `pass`;
- при неопределённом исходе не расширять permissions автоматически и не повторять попытку вслепую.

Регрессии:

- synthetic fixtures отдельно воспроизводят timeout, setup failure и mismatch без чтения private данных;
- успешный parent/child probe подтверждает конкретный deny target;
- результат не утверждает default-deny диска и не объявляет ACL-рекурсию root cause без отдельного доказательства.

Критерий выхода этапа 2: targeted suites BFI-001/004 проходят на одном snapshot; diagnostic artifacts не содержат секретных данных.

## Этап 3. Offline-интеграция итогового diff

1. Запустить targeted suites для BFI-001–005.
2. Повторно прогнать suites, покрывающие BFI-006–014: ToolsetSnapshot, OpenCodeTransport, OpenCodeEvents, OpenCodeWorker, ProcessEnvironment, ExecutionProfile, ManagedReview и ProfiledCodex.
3. Добавить все новые suites в `.github/workflows/offline.yml` и в package validation, если они проверяют публичный контракт поставки.
4. Проверить request/state schema migration и старые fixtures: существующие задачи должны либо читаться по явному совместимому пути, либо блокироваться понятной версионной ошибкой; silent defaults для runtime/budget недопустимы в benchmark-профиле.
5. Провести независимое read-only review требований и combined diff. Исправлять только подтверждённые findings с конкретным failure scenario.

Критерий выхода: единый clean offline run на одном manifest. Сумма ранее разрозненных проверок не заменяет этот gate.

## Этап 4. Заморозить package и host identity

1. Собрать новый воспроизводимый CLI/package из принятого snapshot, не заменяя старые бинарники.
2. Сохранить SHA-256 binary, package manifest, source commit/diff identity и версии Codex/OpenCode/toolsets/runtime.
3. Выполнить native CLI smoke и обязательные package checks на этом же bundle.
4. Решить судьбу сохранённой task revision 12 без переписывания её binding:
   - если policy разрешает безопасную смену host identity — использовать штатный update/recovery contract;
   - иначе завершить её старым закреплённым binary только как исторический lifecycle evidence, а финальную package acceptance провести отдельной новой задачей.

Критерий выхода: нет смешивания старого `bsl-flow-opencode-2.exe` с утверждением о приёмке нового package.

## Этап 5. Живая source-only приёмка

Все попытки создаются заново после успешных environment/budget gates.

### 5.1 Codex + Unica — BFI-001

- выполнить новый synthetic source-only smoke с точным Unica allowlist;
- подтвердить case-sensitive MCP schema, точный набор tools, отсутствие глобальных MCP и сохранность pre/post inventories;
- не повторять `synthetic-unica-4` и не менять его receipt.

### 5.2 Sealed Codex critic — BFI-002

- реальный subscription critic получает только attached payload;
- request evidence показывает ноль tool definitions, включая `additional_tools`;
- stream не содержит tool events;
- результат проходит общий review schema, lint, reconciliation и invariant gates;
- replay не вызывает модель; изменение catalog bytes или binding блокирует cache.

Статус 2026-09-10: **PASS для sealed critic**. Реальный `gpt-5.6-luna` вернул валидный review (`BLOCK`, `R-001`), прошёл spec-lint/review schema/invariants, инструментов не вызвал (catalog `shell_type=disabled`, `tool_mode=direct`, `additional_tools=0`; в потоке только `agent_message`, 0 tool items). Повтор стадии обслужен из cache без нового модельного запроса. Downstream `spec_reconcile` корректно вернул `needs_input` на намеренно неполной fixture (без сфабрикованного acceptance/final-validation). Evidence — в [BFI002_LIVE_ACCEPTANCE_RU.md](BFI002_LIVE_ACCEPTANCE_RU.md). Полный completed-проход `spec_review` с `final-validation.json` в этот допуск не входит.

### 5.3 Codex + cc-1c-skills — BFI-003/BFI-010

- модель читает и применяет ровно выбранный skill из snapshot;
- фактическая команда использует закреплённый Python/lxml и worker path;
- native skills/apps/plugins/MCP не смешиваются с выбранным toolset;
- ожидаемый результат неполного XML классифицируется как fixture outcome, а не ошибка framework.

Статус 2026-09-10: **5.3 PASS**. Один Luna-вызов `inspect` через `Invoke-BFManagedWorker`: preflight принял закреплённые Python 3.12.14 / lxml 6.1.1, наблюдаемая команда исполнила именно закреплённый `python.exe` с выбранным `cfe-validate` skill, `inventory=21`/`disabled=21`, MCP-инструментов воркеру не выставлено, ledger записал reservation+outcome (`cost_state=unknown`, без выдуманного USD). Evidence и hashes — в [BFI003_LIVE_ACCEPTANCE_RU.md](BFI003_LIVE_ACCEPTANCE_RU.md).

Критерий закрытия этапа «для трёх сценариев» **ещё не выполнен**: 5.1 (Codex + Unica) блокирован BFI-001, 5.2 закрыт для sealed-critic контракта (см. выше), но полный completed-проход `spec_review` с final validation не проводился. Source-only PASS не является runtime 1С PASS.

Критерий выхода: для трёх сценариев есть fresh immutable evidence, связанное с одним package manifest. Source-only PASS не является runtime 1С PASS.

## Этап 6. Lifecycle и recovery

1. Продолжить сохранённую CLI task `7bc18343-1f9c-447a-9f64-e920d8845483` строго с revision 12, не создавая новую task и не перенося старые evidence после intent update.
2. Подтвердить protected `exact_synthetic_source` JUnit, полный requirement coverage, review/reconciliation и acceptance receipt.
3. Сверить fixture generator/README с фактическим exact-source тестом.
4. Выполнить одинаковую матрицу lifecycle, failure, repair и recovery для четырёх профилей:
   - OpenCode + Flash + Unica;
   - OpenCode + Flash + cc-1c-skills;
   - Codex + Luna + Unica;
   - Codex + Luna + cc-1c-skills.
5. Для каждого профиля проверить отсутствие skill/toolset mixing, неизменность обязательных gates, budget accounting и idempotent resume.

Критерий выхода: четыре профиля прошли один и тот же публичный controller contract либо имеют честный профильный BLOCKED с сохранённым evidence; partial matrix не объявляется готовой.

## Этап 7. Финальная приёмка интеграций

1. Повторить полный offline CI/package run на exact release candidate.
2. Выполнить финальное независимое review source, schemas, tests, docs и release manifest.
3. Обновить `BENCHMARK_FINDINGS_RU.md`, checkpoint, integration report и changelog по фактическим результатам:
   - `PASS` — только при выполненном критерии закрытия;
   - `BLOCKED` — при отсутствующей обязательной live/runtime evidence;
   - `NOT RUN` — если проверка не запускалась;
   - `unknown` — для неизмеренной стоимости/identity.
4. Подготовить новый release artifact и SHA-256; старые ZIP/binaries сохранить как историю, но не предлагать как актуальные.

Критерий выхода: BFI-001–005 закрыты свежими доказательствами, BFI-006–014 повторно подтверждены на том же snapshot, полный CI зелёный, документация не расширяет границы evidence.

## Согласованный follow-up: архитектурный контекст

После успешного завершения offline-этапа 3, при сохранении открытыми всех обязательных live gates BFI-001–005, можно начать отдельный increment из [плана архитектурного контекста и ADR-индекса](ARCHITECTURE_CONTEXT_PLAN_RU.md). Он добавляет read-only `task context`, индекс ADR и hash-bound выборку применимых решений для stage prompt. Альтернативно increment можно отложить до полного закрытия BFI на этапе 7; это решение о последовательности работ, а не допуск к model/runtime-запускам.

Этот follow-up не входит в критерии закрытия BFI, не разрешает model/runtime/benchmark-запуски и не должен задерживать исправление harness. Его первый этап ограничен самим BSL Flow: project bootstrap не меняется до отдельного пилота возобновления.

## Этап 8. Отдельный допуск к benchmark

Этот этап не входит в исправление harness и требует отдельного разрешения пользователя.

1. Сделать и проверить backup `C:\BASES\DEMO\bp1` разрешённым маршрутом.
2. Заморозить corpus, исключение ЗУП, четыре execution profiles, toolset/runtime manifests и budget policy.
3. Провести реальные задания с разделением ошибок environment, framework, model и 1С runtime.
4. Опубликовать только очищенные результаты; raw evidence и данные базы не публиковать.

## Сводная трассировка

| ID | Реализация | Обязательное доказательство закрытия | Этап |
| --- | --- | --- | --- |
| BFI-001 | Case-preserving RPC + dictionary-safe traversal | Offline Name/name и новый Codex + Unica smoke | 2, 5 |
| BFI-002 | Код не менять без нового finding; проверить sealed contract (живой PASS 2026-09-10) | Реальный critic, zero tools/events, schema/gates, cache replay | 5 |
| BFI-003 | Pinned Python/lxml profile и preflight (offline 23 checks + живой 5.3 PASS) | Drift/missing dependency блокируются до dispatch; fresh cc smoke | 1, 5 |
| BFI-004 | Структурированная поэтапная диагностика capability | Различимые timeout/setup/mismatch fixtures и конкретный deny probe | 2 |
| BFI-005 | Shared ledger, reservation и admission control (offline готово, `Test-TaskBudgetGate` 22 checks) | Failed/unknown/replay/recovery/exhaustion regressions | 1, 6 |
| BFI-006–014 | Не реализовывать повторно; проверить combined diff | Targeted suites + единый offline CI + live пункты BFI-002/010 | 3–7 |

## Definition of Done

Интеграционные исправления считаются завершёнными только если одновременно выполнены все условия:

- новый оплачиваемый dispatch невозможен при неподтверждённом cc runtime, unknown prior cost или недостаточном остатке;
- Codex принимает допустимый MCP response с различающимися регистром schema keys и fail-closed обрабатывает повреждённый/неоднозначный ответ;
- filesystem failure классифицируется по сохранённому наблюдаемому этапу, а не по предположению;
- sealed critic доказан живым запуском без инструментов;
- все четыре профиля проверены одинаковым lifecycle/recovery contract;
- один immutable package прошёл targeted tests, полный offline CI, native smoke и независимое review;
- документация, ledger и публикация сохраняют границы `reported`, `observed`, `unknown`, source-only и runtime evidence.
