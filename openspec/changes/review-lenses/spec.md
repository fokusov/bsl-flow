# review-lenses

## Статус

**ЗАПЛАНИРОВАНО** на будущую версию. Реализация не разрешена до явного решения владельца о включении в релиз. Спецификация фиксирует согласованный дизайн; подробности — в [design.md](design.md).

## Классификация

- Сложность: L
- Риск: medium

## Цель

Спецификация и результат реализации проверяются независимыми критиками под разными углами (линзами): соответствие задаче, простота и отсутствие оверинжиниринга, отсутствие «велосипедов», безопасность, производительность, целостность данных. Механизм один для spec review и code review, работает в Codex, Claude Code и OpenCode, сохраняет существующие принципы: reviewer только критикует, reconciliation принимает решения, вердикт считает скрипт, недоступная обязательная проверка даёт `BLOCKED`.

## Текущее поведение

- Spec review (`1c-spec-review`): M — один reviewer по `reviewer-rubric.md` с оценками `minimality`, `architecture_fit` и метриками `overengineering_index`; L/high — Council с фиксированными ролями `intent_critic`, `architecture_critic`, `executability_critic`, `chair` (роли зашиты в `ValidateSet` в `Council.Engine.ps1`).
- Code review в Core не имеет контракта, схемы и gate: `1c-verify` шаг 5 требует для L/high «independent review» в отдельном контексте без формата результата. Для Claude Code есть агент `hosts/claude-code/agents/bsl-flow-code-reviewer.md` со свободным форматом вывода.
- Угол безопасности (привилегированный режим, права, динамический код, склейка текста запроса, секреты), производительности и целостности данных отдельно не проверяется ни на одном этапе.
- Провайдеры одиночного reviewer: `opencode`, `claude_cli`, `codex_exec`, `api`, `claude_subagent` (`review-providers.md`); слои конфигурации моделей: профиль пользователя → проект → `providers.local.yaml`.
- ADR-11 замораживает managed-поверхность: новые stage-типы, actions и опциональные подсистемы managed запрещены.

## Требуемое поведение

### Линзы и выбор

1. Каталог линз поставляется в Core-пакете: `intent`, `simplicity`, `reuse`, `security`, `performance`, `data_integrity`. Каждая линза — файл с frontmatter (`id`, `version`, `applies_to`, `categories`, `triggers`) и телом (фокус, чек-лист, анти-цели). Hash каждой использованной линзы входит в `input_hashes` результата.
2. Категории findings закрыты по линзам. Общая для всех линз категория — `prompt_injection`.
3. Детерминированный селектор `Select-1CReviewLenses.ps1` строит `lens-plan.json` из классификации, флагов инспекции, `original-task.md`, diff (для code review) или spec (для spec review) и `review.<phase>.lenses.*`. Для каждой линзы план содержит: включена ли, `required`, причину включения (правило или сигнал), провайдер, модель, effort и источник каждого значения (`profile`, `project`, `default`). Модель при выборе не вызывается.
4. `intent` и `simplicity` включены всегда, когда code review выполняется. Остальные линзы включаются по сигналам (см. design.md, «Триггеры»). Флаг `permissions` делает `security` обязательной, флаги `data_migration`/`data_deletion` делают `data_integrity` обязательной. Эти флаги уже повышают риск до high, поэтому такие изменения всегда идут маршрутом `per_lens`.
5. Проект может явно включить или выключить линзу. Выключение линзы, обязательной по риску, разрешено и записывается в `limitations` результата.
6. `Select-1CReviewLenses.ps1 -DryRun` печатает итоговый план с источниками значений без вызова моделей.

### Маршрутизация и исполнение

7. Code review: S — не выполняется, кроме явного запроса или `review.routing.s_default: required`; M — режим `single_pass`; L/high — режим `per_lens`.
8. `single_pass`: один изолированный reviewer получает все линзы плана как секции и возвращает массив результатов, по одному на линзу. Пропущенная или лишняя линза делает ответ невалидным.
9. `per_lens`: каждая линза исполняется в отдельном изолированном запуске. Параллелизм ограничен `review.runtime.max_parallel` (по умолчанию 3); хост без параллелизма выполняет линзы последовательно, каждую в свежем контексте.
10. Текущая авторская сессия никогда не является reviewer линзы; значение `fallback: current_agent` для линз запрещено.
11. Если план требует `per_lens`, а изолированный запуск недоступен, результат — `BLOCKED` с шагом настройки; переход на `single_pass` без явной настройки запрещён.
12. Раннер снимает snapshot входов (diff, список изменённых файлов, spec, `original-task.md`, тексты линз) в `.bsl-flow/reports/code-review/<run-id>/` и отказывает в публикации, если live-входы изменились во время прогона.

### Конфигурация моделей по критикам

13. `review.code_review.{provider, model, effort}` задают значения по умолчанию; `review.code_review.lenses.<lens>.{enabled, required, provider, model, effort, fallback}` перекрывают их для линзы. Без настроек линз все линзы используют одно значение.
14. Разные линзы могут использовать разных провайдеров. Линзы script-owned провайдеров (`codex_exec`, `opencode`, `claude_cli`, `api`) исполняет раннер; линзы `claude_subagent` получают в плане статус `pending_import`; `code-review.json` публикуется только после получения результатов всех линз плана.
15. Модель передаётся хосту штатным способом провайдера (см. design.md, «Адаптеры хостов»). Для `claude_subagent` сессия берёт модель линзы из `lens-plan.json`; если хост не сообщает наблюдаемую модель, результат линзы содержит `observed_model: null` и limitation.
16. Слои конфигурации совпадают с Council: профиль пользователя → проектный `bsl-flow.yaml` → `providers.local.yaml` для `token`/`base_url`. Allow-list профиля расширяется только ключами `review.code_review.{model, effort}` и `review.code_review.lenses.<lens>.{model, effort}`; остальные ключи отклоняются fail-closed с именем файла и ключа.
17. Неизвестный профиль модели, модель, не поддерживаемая провайдером, или недоступный провайдер обнаруживаются до первого вызова и дают ошибку конфигурации с именем линзы и ключа.
18. `fallback: block` — по умолчанию. Для необязательной линзы допустим `fallback: default_model`: линза исполняется на `review.code_review.model`, факт записывается в `limitations`.
19. Независимость: `review.code_review.independence: distinct_from_author` (по умолчанию) требует, чтобы модель каждой линзы отличалась от модели автора; `distinct_lenses: [<lens>, ...]` дополнительно требует попарно различных моделей у перечисленных линз. Нарушение обнаруживается до первого вызова.

### Результат, вердикт, reconciliation, gate

20. Сырой ответ линзы валидируется схемой `lens-review-schema.json` (см. design.md). Каждый finding содержит ID с префиксом линзы, severity, категорию линзы, ссылку `file`/`line` или `module`/`method`, `issue`, `evidence`, `confidence_kind: confirmed|hypothesis`, `suggested_direction`. Ответ с `lens_verdict: PASS` и пустым `checked` невалиден.
21. Раннер публикует `code-review.json` только после валидации всех линз: план и его hash, input hashes, результат, провайдер, модель, признак изоляции (`script_owned` или `host_asserted`) каждой линзы, объединённые findings с `duplicate_of`, вердикт и `limitations`.
22. Вердикт вычисляется детерминированно: `BLOCK` — есть `blocker` или обязательная линза не отработала; `REVISE` — есть `confirmed` finding уровня `high`/`medium` или любая линза вернула `REVISE`; иначе `PASS`. Gate может понизить вердикт модели, но не повышает `REVISE`/`BLOCK`.
23. Ошибка провайдера, timeout или невалидный ответ линзы в `per_lens` дают одну повторную попытку этой линзы; повторная ошибка — `BLOCKED`. Уже валидные результаты других линз сохраняются и повторно не запрашиваются. В `single_pass` повторяется весь проход один раз, затем `BLOCKED`.
24. Findings проходят существующий `reconciliation-contract.md` без изменения семантики: каждое решается ровно один раз, принятые исправляются в одной correction round, затем выполняется свежий code review полного изменённого diff. Результат — `code-review-reconciliation.json`.
25. `Test-1CChangeGate.ps1` требует reconciled `code-review.json`, актуальный по hash текущего diff, когда code review обязателен по маршрутизации; отсутствие или устаревание даёт `FAIL: process_violation`.

### Хосты

26. Один универсальный read-only промпт `lens-reviewer-prompt.md`; линза подставляется секцией. Отдельных агентов на каждую линзу нет.
27. Claude Code: агент `hosts/claude-code/agents/bsl-flow-lens-reviewer.md` с `tools: Read, Grep, Glob` и зафиксированной моделью по умолчанию; сессия вызывает его по одному разу на линзу, передавая модель линзы, сохраняет сырой ответ в `<run-dir>/raw/<lens>.json` и выполняет `Invoke-1CCodeReview.ps1 -ImportRaw <run-dir>`.
28. Codex: раннер запускает `codex exec` в read-only sandbox по одному разу на линзу; для скилла поставляется `agents/openai.yaml`.
29. OpenCode: раннер запускает `opencode run` с упакованным read-only агентом по одному разу на линзу и не изменяет глобальную конфигурацию агентов OpenCode.

### Spec review (второй этап)

30. Линзы с `applies_to: spec` используются в spec review через `review.spec_review.lenses` той же формы: для M — как секции одиночного reviewer; для L/high — как отдельные `per_lens` запуски рядом с Council. Движок, роли и модели Council (`review.council.roles`) не изменяются.

## Контекст 1С

- Конфигурация/подсистема: BSL Flow (PowerShell 7); метаданные 1С не изменяются.
- Затрагиваемые механизмы: новый Core-скилл `global/skills/1c-code-review` (линзы, селектор, раннер, схемы, промпт); `global/skills/1c-verify` (шаг 5 и `Test-1CChangeGate.ps1`); `global/skills/1c-spec-review` (второй этап: секции линз и `per_lens` рядом с Council; `Council.Profile.ps1` — allow-list профиля); `hosts/claude-code/agents`; упаковка Core; `bench/`.
- Evidence path: implement → select lenses → snapshot → provider × lens → validate → aggregate → `code-review.json` → reconcile → verify gate.
- 1С runtime: не запускается; reviewer не исполняет тесты и команды.

## Не делать

- Не изменять managed-контур и `1c-task` до снятия заморозки ADR-11; не добавлять stage-типы и actions.
- Не изменять движок, роли, промпты и вердикты Council.
- Не открывать проектные линзы в первом релизе.
- Не давать reviewer права на запись, запуск команд, тестов или сети.
- Не позволять модели вычислять итоговый вердикт, hashes или объединение findings.
- Не создавать отдельный агент хоста на каждую линзу.
- Не допускать тихого перехода с `per_lens` на `single_pass` и с обязательной линзы на её пропуск.

## Критерии приёмки

- GIVEN M-изменение без флагов риска и diff без запросов и новых объектов
  WHEN строится план
  THEN план содержит только `intent` и `simplicity` в режиме `single_pass`.
- GIVEN S-изменение с флагом `permissions` (риск повышен до high)
  WHEN строится план
  THEN режим `per_lens`, `security` обязательна.
- GIVEN diff добавляет `УстановитьПривилегированныйРежим(Истина)`
  WHEN строится план
  THEN `security` включена с причиной-сигналом.
- GIVEN проект выключает обязательную по риску линзу
  WHEN публикуется результат
  THEN `limitations` содержит запись о выключенной линзе.
- GIVEN `review.code_review.lenses.security.model: critic-claude` в проекте и `review.code_review.lenses.security.model: personal` в профиле
  WHEN выполняется `-DryRun`
  THEN модель `security` — `critic-claude` с источником `project`.
- GIVEN профиль содержит `review.code_review.lenses.security.provider`
  WHEN читается конфигурация
  THEN запуск завершается ошибкой с именем файла и ключа.
- GIVEN модель линзы совпадает с моделью автора при `distinct_from_author`
  WHEN выполняется admission
  THEN запуск блокируется до первого вызова модели.
- GIVEN `single_pass` ответ без одной из линз плана
  WHEN выполняется валидация
  THEN ответ невалиден, `code-review.json` не публикуется.
- GIVEN finding линзы `simplicity` с категорией `query_injection`
  WHEN выполняется валидация
  THEN ответ невалиден.
- GIVEN линза вернула `PASS` с пустым `checked`
  WHEN выполняется валидация
  THEN ответ невалиден.
- GIVEN `per_lens`, одна линза дважды вернула невалидный JSON
  WHEN прогон завершается
  THEN вердикт `BLOCKED`, результаты остальных линз сохранены, повторно не запрашивались.
- GIVEN смешанные провайдеры: `security` через `api`, остальные через `claude_subagent`
  WHEN раннер завершил script-owned линзы, а импорт ещё не выполнен
  THEN `code-review.json` не опубликован, план показывает `pending_import`.
- GIVEN L-изменение и хост без изолированного запуска
  WHEN запускается code review
  THEN результат `BLOCKED` с шагом настройки, `single_pass` не используется.
- GIVEN diff изменился во время прогона
  WHEN раннер готов публиковать
  THEN публикация отклонена.
- GIVEN code review обязателен, а `code-review.json` отсутствует или устарел по hash diff
  WHEN выполняется `Test-1CChangeGate.ps1`
  THEN результат `FAIL: process_violation`.
- GIVEN бенчмарк с посеянными дефектами (см. design.md)
  WHEN сравниваются линзы и одиночный reviewer
  THEN критерий пользы, зафиксированный до выпуска, выполнен; иначе функциональность не выпускается.

## Требуемые проверки

- [x] Static — схемы линз и результата, закрытые категории, allow-list профиля, контракт агента Claude (`Read, Grep, Glob`, зафиксированная модель), состав Core-пакета.
- [x] Unit — селектор (таблица сигналов, слои конфигурации, источники значений, limitations), валидация ответов, агрегатор и вердикт, независимость моделей.
- [x] Integration — раннер с fake-провайдерами `codex_exec`/`opencode`/`claude_cli`/`api`, `-ImportRaw` для `claude_subagent`, смешанные провайдеры, retry и `BLOCKED`, дрейф входов, gate в `1c-verify`; без сети.
- [x] Benchmark — кейсы с посеянными дефектами в `bench/`, recall по линзам, доля отклонённых findings, стоимость против одиночного reviewer.
- [ ] UI — не требуется.
- [x] Independent review — L: Council spec review обязателен перед реализацией.

## Неопределённости / допущения

- Критерий пользы бенчмарка (пороги recall и шума) устанавливается владельцем до начала реализации этапа 1 и записывается в эту спецификацию.
- Сигнатуры триггеров в diff — эвристика; ложное срабатывание стоит одного лишнего вызова и допустимо, пропуск закрывается явным включением линзы в проекте.
- Способ, которым Claude Code сообщает наблюдаемую модель субагента, уточняется при реализации адаптера; до этого действует требование 15.
- Отдельная настройка моделей линз для spec и code review задаётся раздельно (`review.spec_review.lenses` и `review.code_review.lenses`); общий блок не вводится.
