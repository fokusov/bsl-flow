# Дефекты и доработки по интеграционным тестам

Реестр на 2026-09-10. Основание — синтетические проверки managed OpenCode/Codex, а не задания 1CLLMBenchTasks или испытания бизнес-логики 1С. Реализация и модельные прогоны приостановлены; этот документ фиксирует дальнейшую работу.

**«Исправлено в рабочем дереве» не означает выпущено или принято полным CI.** Изменения ещё не закоммичены. Сохранённые бинарники отстают от части исходников. Точные пути попыток, состояние задачи и ограничения — в [контрольной точке](BENCHMARK_CHECKPOINT_2026-09-10.md).

## Открытые дефекты и обязательная приёмка

### BFI-001 · P1 · Codex отвергает допустимые MCP-схемы — открыт

**Сценарий:** Unica возвращает schema keys, различающиеся регистром. `ConvertFrom-Json` без `-AsHashtable` отвергает ответ, и связка Codex + Unica блокируется ещё до модельного dispatch.

**Доказательство:** `work/benchmark-integration/codex-managed/synthetic-unica-4/.bsl-flow/tasks/smoke/mcp-rpc/malformed-response.json`: `KeysWithDifferentCasingInJsonString`, method `mcpServerStatus/list`, phase 3. В `Codex.Skills.ps1` на момент фиксации всё ещё используется разбор без `-AsHashtable`.

**Изменение:** сохранять регистр ключей при RPC parse; согласовать обход словарей, особенно `server.tools`, с новым представлением. Не допустить потери полей и не считать допустимым неоднозначный повтор одного и того же ключа. Не писать raw `config/read` или credentials в диагностику.

**Закрытие:** regression на `Name`/`name`, проверки отказа на повреждённом ответе и новый живой source-only Codex + Unica smoke с точным allowlist. Старые partial attempts и receipts не переписывать.

### BFI-002 · P1 · Приёмка критика Luna без инструментов — закрыт 2026-09-10

**Сценарий:** отключение shell или верхнего `tools` не доказывает отсутствие остальных инструментов; Luna сериализует их также в `input.additional_tools`, а часть деклараций приходит из model catalog.

**Решение в рабочем дереве:** отдельный каталог попытки с отключёнными shell/apply_patch и direct tool mode; explicit switches; attached-only prompt без toolset/MCP; контроль hash и отказ при tool events. Обычные worker stages сохраняют свой маршрут.

**Доказательство:** `codex-critic-spike/capture-3/comparison.json`: baseline 3 definitions, custom 0, без inference; source audit pinned host и 88 проверок ProfiledCodex на последней подтверждённой версии.

**Закрытие:** реальный subscription critic должен вернуть валидный review, пройти общие lint/review/invariant gates и не вызвать инструменты. Повтор результата не должен делать нового модельного запроса; изменение каталога должно блокироваться. Локальный mock capture не заменяет эту приёмку.

**Закрыто 2026-09-10:** живой subscription critic (`gpt-5.6-luna`, `codex-cli 0.154.0`) вернул валидный review (`reviewer_verdict=BLOCK`, finding `R-001`), прошёл spec-lint, review schema и инварианты входов, не вызвал инструментов: per-attempt catalog `shell_type=disabled`/`apply_patch=null`/`tool_mode=direct`/`experimental_supported_tools=[]`, в потоке только `agent_message`, ноль tool items. Повтор стадии обслужен из cache без нового модельного запроса; изменение catalog bytes блокируется (offline `Test-BSLFlowProfiledCodex`, 88 checks). Детали и hashes — в [BFI002_LIVE_ACCEPTANCE_RU.md](BFI002_LIVE_ACCEPTANCE_RU.md).

### BFI-003 · P1 · Не закреплена среда исполнения cc-скиллов — закрыт 2026-09-10

**Сценарий:** Python из PATH не содержит `lxml`; Flash начинает искать другие интерпретаторы в сторонних проектах. Такой прогон невоспроизводим и выходит за разрешённый сценарий.

**Доказательство:** `synthetic-cc-script-smoke-1` не принят; `synthetic-cc-script-smoke-3` вызвал нужный скрипт после явного указания проверенного Python 3.12.14 / lxml 6.1.1 и worker path.

**Изменение:** включить выбранный interpreter и необходимые зависимости в проверяемую конфигурацию benchmark environment; проверять их до модели. При отсутствии зависимости возвращать конкретный environment blocker, без автоматического поиска по чужим проектам или установки. Сравниваемые профили должны получать одинаково подготовленную среду.

**Закрытие:** доступный runtime проходит preflight и source smoke; отсутствующий/изменённый runtime блокирует до оплачиваемого вызова. Идентичность среды сохраняется с результатом. Один удачный путь, указанный только в prompt, недостаточен.

**Закрыто 2026-09-10:** контроллер теперь требует `execution_profile.runtime` (точный executable + SHA-256 + версия + обязательные пакеты, `lxml` обязателен) и запускает `Test-BFRuntimePreflight` до оплачиваемого dispatch. Offline-drift/отсутствие пакета блокируются (`scripts/Test-TaskRuntimePin.ps1`, 23 checks). Живая приёмка сценария 5.3 (Codex + cc-1c-skills, один Luna-вызов) прошла: наблюдаемая команда исполнила закреплённый `python.exe` 3.12.14 / lxml 6.1.1 и выбранный skill, mixed skills/MCP отсутствуют. Детали и hashes — в [BFI003_LIVE_ACCEPTANCE_RU.md](BFI003_LIVE_ACCEPTANCE_RU.md).

### BFI-004 · P2 · Диагностика filesystem capability недостаточно конкретна — открыт

**Сценарий:** при широком deny-каталоге preflight завершился timeout с пустыми потоками; при конкретном разрешённом тестовом target прошёл. Причина разницы окончательно не установлена: нельзя представлять рекурсивную ACL-обработку как доказанный root cause.

**Изменение:** сохранять точный этап и длительность preflight, идентичность собственного процесса и профиль; различать timeout/setup/несовпадение наблюдаемых прав. Не расширять разрешения автоматически и не повторять неопределённую попытку вслепую.

**Закрытие:** диагностические fixtures отличают эти исходы без чтения реальных private данных; успешный probe подтверждает deny для parent/child. Не обещать default-deny всего диска на основании одного synthetic probe.

### BFI-005 · P1 до бенчмарка · Общий бюджет всех этапов — требуется доработка harness

Сборщик стоимости уже учитывает отдельные `step_finish` и не дублирует session. Но итоговый ledger после запуска не равен предварительному ограничению затрат нескольких задач, ревью и повторов.

**Изменение:** проверять общий остаток перед новым оплачиваемым dispatch, учитывать неуспешные попытки, сохранять неизвестную стоимость как unknown, а не ноль. Отдельно учитывать requested и observed model; не придумывать цену Luna по названию модели.

**Закрытие:** replay не увеличивает расход; failed attempt учитывается; исчерпание лимита запрещает следующий вызов; после восстановления сохраняется тот же итог. Реальный счёт провайдера и reported cost остаются разными источниками.

## Исправления, уже внесённые в исходники

| ID | Дефект / конкретный эффект | Что изменено и чем подтверждено |
| --- | --- | --- |
| BFI-006 | OpenCode не сохранял `model-result.json`, нужный Resume; cached cost менял JSON-представление после decimal → double | Сохранение нормализованного результата перед host receipt; сверка с raw JSONL; стабильный cost type. Worker suite и replay ранее завершённого smoke без новой модели |
| BFI-007 | OpenCode использовал глобальные state locks, пытался создавать файл в read-only config и неоднозначно находил source directory | Отдельные XDG config/data/cache/state/tmp; preseed `.gitignore`; explicit `--dir` и worker path. Подтверждено source smoke и третьим cc smoke |
| BFI-008 | Правила OpenCode блокировали доступ к собственному scratch при согласовании review | Разрешён только scratch данной попытки в external_directory policy. Первый CLI failure сохранён; второй цикл прошёл согласование и дошёл до code review. Изолированную приёмку этого правила включить в окончательные проверки |
| BFI-009 | `mcp_servers={}` не очищал глобальные MCP; quoted dotted keys создавали другое имя; поздняя whole-table registration отменяла ранние overrides | Config/read с сохранением только имён, exact per-name disable, bare keys, повтор deny после Unica registration; same-process checks. Обычный Luna smoke прошёл. Общая Unica приёмка остаётся заблокирована BFI-001 |
| BFI-010 | Одного multi_agent=false недостаточно при catalog v1; автообнаруженные скиллы могли смешаться с выбранным набором | Explicit agents.enabled=false, multi_agent_v2/apps/plugins off; hash inventory и отключение всех 20 обнаруженных скиллов. Native inventories и offline tests; selected cc invocation из Codex ещё требуется |
| BFI-011 | Parser usage отвергал наблюдаемые поля cache_write_input_tokens / reasoning_output_tokens | Optional nonnegative integer fields. Сохранённый реальный ответ Luna value=73 успешно разобран offline; старый binding не менялся |
| BFI-012 | Ошибка закрытия stdin маскировала исходную RPC-ошибку и прерывала cleanup | Bounded stop/drain/dispose с сохранением исходной ошибки; stopped-pipe regression. Безопасная диагностика malformed MCP response позволила установить BFI-001 |
| BFI-013 | Слишком очищенный Windows environment ломал запуск; быстрый процесс мог обойти проверку размера вывода | Разрешён необходимый стандартный Windows environment без произвольных parent secrets; общий stdout/stderr bound проверяется также после drain. ProcessEnvironment tests и живые sandbox probes |
| BFI-014 | Cached payload critic мог расходиться с проверенным model result | Проверка согласованности до публикации review; ManagedReview regression на подменённый payload и сохранение прежнего review |

Эти изменения относятся к интеграциям новой рабочей версии. Они не доказывают, что все перечисленные ошибки присутствуют в уже опубликованном релизе.

## Что не является дефектом ядра BSL Flow

| Наблюдение | Классификация и действие |
| --- | --- |
| Code review отказал в приёмке функции, проверенной лишь substring `Return` | **Правильное срабатывание coverage gate.** Ошибка тестового fixture. Подготовлен защищённый exact-source JUnit test; scope update принят в revision 12, свежий run ещё не начат |
| Unica validator вернул 2 errors / 1 warning на неполном XML | **Ожидаемый результат входного fixture**, а не дефект валидатора или фреймворка |
| Public Unica bootstrap требует `.locks` write при готовом runtime | **Контракт внешнего host.** Разрешён этот служебный каталог, executable cache остаётся read-only; нельзя использовать требование locks как повод разрешить runtime installation |
| Отказ доступа к publication cache при offline suite | **Ограничение окружения данного прогона.** Адресное продолжение начато, затем остановлено по просьбе пользователя. Не выдавать за функциональный дефект публикации без воспроизведения; улучшить тестовую изоляцию cache при необходимости |
| Snapshot потерял три кириллических пути | **Ошибка локального harness:** вывод git ls-files был quoted. Исправлено перечислением с core.quotePath=false; включать сверку manifest перед дальнейшими замороженными прогонами |
| Get-Content synthetic.txt из Luna успешен | **Доказательство транспорта и конкретного read**, не подтверждение выполнения cc-скилла, полного SDLC или качества Luna на задачах 1С |

## Порядок закрытия и граница выпуска

1. Исправить BFI-001, провести живые Codex + Unica и sealed critic проверки (BFI-002).
2. Закрепить среду скиллов и бюджет (BFI-003, BFI-005); уточнить preflight diagnostics (BFI-004).
3. Продолжить сохранённую synthetic CLI task revision 12 и подтвердить полный цикл с защищённым тестом. Подтвердить repair/recovery и четыре профиля без смешивания скиллов.
4. Проверить последний combined diff, закончить обязательные offline checks на идентичном пакете, собрать новый binary и актуализировать статус исправлений. Не переносить PASS старого snapshot на новые изменения автоматически.
5. После этих проверок готовить benchmark, backup базы и очищенную публикацию результатов. До команды пользователя работа остаётся на паузе.
