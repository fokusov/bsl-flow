# Локальный проектный task tracker BSL Flow

## Классификация
- Сложность: L
- Риск: high

## Цель
Добавить в BSL Flow единый локальный каталог задач одного Git-клона, доступный из любого worktree, с planned-задачами, сохранением завершённой истории и стабильными human/JSON read-командами, не создавая второй источник истины рядом с controller journals.

## Текущее поведение
- Controller (global/skills/1c-task, Invoke-BSLFlowTask.ps1) хранит каждую зарегистрированную задачу в checkout-local `.bsl-flow/tasks/<uuid>/revisions`; hash-linked revisions являются источником истины, а `current.json` — производной проекцией.
- Действия `Status|Next|Context` требуют заранее известный UUID. Команд перечисления, поиска, overview и истории нет.
- Статусы ограничены исполняемым lifecycle `ready|running|needs_input|blocked|failed|completed|cancelled`; planned-задачи отсутствуют.
- Task store привязан к переданному project root. Задачи, созданные из разных Git worktree одного клона, не образуют общего каталога.
- Trusted request не имеет отдельного короткого title, priority, labels или межзадачных dependencies.

## Требуемое поведение

### Требуемое поведение / 1
Для Git-репозитория BSL Flow разрешает один clone-local repository store относительно проверенного абсолютного `git common dir`. `repository_id` — детерминированный (первые 16 hex SHA-256 нормализованного абсолютного пути git common dir). Все worktree, принадлежащие тому же common dir, используют один store. Сеть, Git remote и внешний сервис для каталога не требуются.

### Требуемое поведение / 2
Истиной каждой новой planned/controller-задачи остаётся один immutable hash-linked journal. Каталог, overview, board-проекции и поисковые индексы являются полностью восстанавливаемыми derived data и не могут разрешать execution, acceptance или publication.

### Требуемое поведение / 3
Новая задача создаётся с UUID и статусом `planned`. Обязательны непустой `title` и schema version; optional поля первой версии: `description`, `priority=low|medium|high|critical`, уникальные `labels` и уникальные `depends_on` UUID того же repository store. Milestones, assignee, comments, due dates и произвольные custom fields не входят в v1.
Константы полей:
- `title`: строка, обрезается, 1–200 символов.
- `description`: строка, опционально, 0–5000 символов.
- `priority`: enum `low|medium|high|critical`, default `medium`.
- `labels`: массив строк; каждый элемент обрезается, 1–50 символов, регистрозависимый, уникальный; максимум 20 элементов.
- `depends_on`: массив UUID строк; элементы уникальны; формат каждого — UUID v4; существование зависимости не проверяется при создании/редактировании.

### Требуемое поведение / 4
Planned-задача не имеет execution authorization, worker path, baseline, active attempt или ложного stage. Её metadata редактируется через optimistic `expected_revision`; удаление journal и переиспользование UUID запрещены.

### Требуемое поведение / 5
`task activate` принимает полный trusted request текущего controller contract, проверяет выбранный worktree/project root и добавляет новую revision того же task ID со статусом `ready`. В v1 PS-реализации активация выполняет полную валидацию trusted request и возвращает staged `BLOCKED` без revision. Включение нативной активации — отдельный controller write slice.
Trusted request schema v1 (обязательные поля):
- `task_id`: UUID string, должен совпадать с целевой задачей.
- `project_root`: absolute path string, должен существовать и быть внутри worktree того же common dir.
- `title`: non-empty string, проходит проверку из требования 3.
- `priority`: enum из требования 3.
- `labels`: массив строк, как в требовании 3.
- `depends_on`: массив UUID, как в требовании 3.
- `controller_contract`: строковая константа `repository-store-aware/v1`.
Validation: обязательные поля присутствуют и имеют корректные типы; `project_root` существует и принадлежит worktree; `controller_contract` объявлен. При ошибке валидации возвращается `BF_INVALID` без создания revision. При успешной валидации, но отсутствии capability нативной активации, возвращается `BF_BLOCKED` без создания revision.

### Требуемое поведение / 6
Для активированной задачи существующий controller lifecycle и authority сохраняются. `archived` является presentation flag, а не execution status и не меняет `next_action`, acceptance, recovery или dependency result.

### Требуемое поведение / 7
`depends_on` является metadata planned-задачи: незавершённые зависимости не запрещают создание или активацию, межзадачной транзакции и автоматического запуска dependency нет. Controller-owned pre-dispatch gate и понятия `fresh effective completed`/`stale-completed` в этой версии не определены и отложены в отдельную спецификацию native controller write slice. `run` в этой версии не использует `depends_on`.

### Требуемое поведение / 8
Создание/редактирование dependency должно отклонять self-reference, duplicate ID и цикл по текущим валидным journals. Проверка графа и публикация dependency revision сериализуются общим repository graph lock либо эквивалентным generation-CAS, поэтому встречные concurrent edges не могут обе пройти на одном snapshot. Проверка результатов dependency перед dispatch не входит в metadata-only contract требования 7 и требует отдельной спецификации.

### Требуемое поведение / 9
`bsl-flow task list --project <любая-worktree>` по умолчанию возвращает все неархивированные задачи repository store, включая `planned`, `completed` и `cancelled`.
Поддерживаются точные фильтры:
- `--status`: comma-separated список из enum `planned|ready|running|needs_input|blocked|failed|completed|cancelled`.
- `--stage`: comma-separated список stage строк.
- `--priority`: comma-separated список из `low|medium|high|critical`.
- `--label`: comma-separated список; задача соответствует, если содержит хотя бы одну из указанных меток (OR).
- `--updated-before`: RFC3339 UTC timestamp; включительно.
- `--updated-after`: RFC3339 UTC timestamp; включительно.
- `--archived`: `false|true|all`, default `false`; `false` — только неархивированные, `true` — только архивированные, `all` — обе.
- `--sort`: `created_at|updated_at|priority|status|title`, default `updated_at`.
- `--order`: `asc|desc`, default: для `updated_at|created_at` — `desc`, для прочих — `asc`.
- `--limit`: integer 1–200, default 50.
- `--cursor`: opaque string из предыдущего ответа; при изменении фильтров или repository cursor невалиден.
Приоритет сортировки (asc): `low`, `medium`, `high`, `critical`; статус (asc): `planned`, `ready`, `running`, `needs_input`, `blocked`, `failed`, `completed`, `cancelled`; title — лексикографический. Tie-breaker всегда UUID ascending. Невалидное значение любого параметра приводит к `BF_INVALID`.

### Требуемое поведение / 10
Строка списка не содержит полный prompt, raw events/evidence или secrets. Она содержит task ID, title, lifecycle status, stage/next action при наличии, priority, labels, dependency summary, created/updated time, originating/current worktree identity и diagnostic state.

### Требуемое поведение / 11
`task show` возвращает карточку и безопасную controller projection: request summary, criteria IDs/observations, status/stage/next action, blockers/question, dependency graph, attempts summary, acceptance summary и evidence references. Raw provider bodies, credentials и private runtime input не выводятся.

### Требуемое поведение / 12
`task history` строит deterministic chronological timeline из проверенных revisions/events: metadata changes, activation, stage/attempt terminals, user updates, blockers, acceptance, cancellation и archive changes. По умолчанию payload/evidence content не разворачивается; ссылки и hashes сохраняются.

### Требуемое поведение / 13
`task overview` возвращает totals по lifecycle status/stage/priority, counts `needs_input|blocked|running|completed|planned`, число archived, corrupt и orphaned задач, а также время построения и scope identity. Counts `dependency-blocked` и `stale-completed` в v1 не возвращаются, так как соответствующие понятия отложены.

### Требуемое поведение / 14
Все read-команды имеют один versioned noninteractive JSON document (`schema_version: 1`) на stdout и отдельный стабильный human-readable table/detail output. Write-команды возвращают поля `schema_version`, `task_id`, `revision`, `status`, `archived`, `priority`, `title`, `next_action`; read-команды — `schema_version`, `repository_id` и allowlisted проекции задач. Exit-контракт зафиксирован: `0` — успех, `2` — `BF_INVALID`, `11` — `BF_BLOCKED`/`BF_CONFLICT`. Ошибка CLI пишет controller-shaped JSON в JSON mode либо concise stderr в human mode; первое поле blocker-сообщения — класс (`BF_INVALID`, `BF_BLOCKED`, `BF_CONFLICT`).

### Требуемое поведение / 15
Повреждённый, torn или конфликтующий journal не должен молча исчезать и не должен делать невидимыми остальные задачи.
Детекция:
- `corrupt`: JSON parse failure, отсутствие обязательных полей (`task_id`, `revision`, `parent_hash`, `payload`), или hash chain не сходится (вычисленный SHA-256 канонической сериализации не совпадает с заявленным).
- `conflict`: один и тот же `task_id` имеет различающуюся verified history из разных источников (canonical store против legacy discovery).
- `orphaned`: каталог задачи или legacy path обнаружен, но не удаётся установить verified chain root (например, нет ни одной валидной revision с известным `task_id`).
List/overview возвращают diagnostic entry с известным task/source identity и `health=corrupt|conflict|orphaned`; `show/history` конкретной повреждённой задачи завершаются `BF_BLOCKED` с точной причиной.

### Требуемое поведение / 16
Read-команды строят результат непосредственно из авторитетных journals. Производный `catalog/index.json` не является обязательным артефактом, никогда не читается как источник истины и, если сохраняется, содержит source revision/hash и полностью восстанавливается из journals. Отсутствие или повреждение производного артефакта не влияет на read-результат и не отменяет подтверждённую revision.

### Требуемое поведение / 17
Existing checkout-local `.bsl-flow/tasks` остаются читаемыми через legacy discovery (read-only), их bytes не переписываются. Verified history hash определяется как SHA-256 от конкатенации 64-hex revision hashes в хронологическом порядке. Совпадающий UUID с одинаковой verified history дедуплицируется; различающаяся неподтверждённая history показывается как conflict и никогда не выполняется молча. Automatic destructive move запрещён. Полный adoption принадлежит отдельной спецификации `native-task-activation-adoption`; команды этого контракта (включая legacy discovery) не выполняют запись в canonical store.

### Требуемое поведение / 18
Repository store переживает удаление рабочего worktree: worktree path является provenance/execution binding, но не местом хранения canonical task history. Историческое `completed`/`accepted` состояние сохраняется независимо от живых входов. Для controller-задач проверяются абсолютные live inputs: `baseline_path`, `worker_path`, `evidence_path` (если присутствуют). Если хотя бы один из перечисленных путей отсутствует или недоступен, read-проекция помечает запись `freshness=stale`; diagnostic не меняет исторический статус и не служит execution/acceptance gate в этой версии.

### Требуемое поведение / 19
Registry metadata и output не должны содержать runtime credentials, Authorization headers или raw private evidence. Clone-local хранение не означает разрешение на передачу данных внешней модели либо Git remote.

### Требуемое поведение / 20
Реализация — PS-native vertical slice: `create|edit|list|show|history|overview|archive|unarchive` реализуются в PowerShell (global/skills/1c-task, новый Task.Registry.ps1 + действия Invoke-BSLFlowTask.ps1) и не требуют иного движка. Команды доступны как действия `Create, EditRegistry, List, Show, History, Overview, ArchiveTask, UnarchiveTask, Activate` контроллера. Дополнительно предоставляется тонкая обёртка `bsl-flow task <subcommand>`, которая маппит subcommands на соответствующие действия контроллера: `list` → `Invoke-BSLFlowTask -Action List`, `show` → `Show`, `history` → `History`, `overview` → `Overview`, `create` → `Create`, `edit` → `EditRegistry`, `archive` → `ArchiveTask`, `unarchive` → `UnarchiveTask`, `activate` → `Activate`. Automatic conversion существующих checkout-local задач в repository store запрещён.

## Нормативные JSON-схемы (v1)

### Общий read envelope
| Поле | Тип | Обязательность | Описание |
|---|---|---|---|
| `schema_version` | integer | да | Всегда `1` |
| `repository_id` | string | да | 16 hex символов |

### List response
| Поле | Тип | Обязательность | Описание |
|---|---|---|---|
| `tasks` | array of task list item | да | Отсортированные задачи согласно фильтрам |
| `next_cursor` | string\|null | да | Opaque cursor для следующей страницы; `null` если нет |

Task list item:
| Поле | Тип | Обязательность | Описание |
|---|---|---|---|
| `task_id` | string (UUID) | да | |
| `title` | string | да | |
| `status` | string | да | lifecycle status |
| `stage` | string\|null | да | stage или `null` для planned |
| `next_action` | string\|null | да | next action или `null` |
| `priority` | string | да | enum |
| `labels` | array of string | да | |
| `dependency_summary` | object | да | `{total: integer, by_status: object}` |
| `created_at` | string RFC3339 | да | |
| `updated_at` | string RFC3339 | да | |
| `origin_worktree` | string | да | |
| `current_worktree` | string\|null | да | |
| `diagnostic_state` | string\|null | да | `corrupt\|conflict\|orphaned` или `null` |

### Show response
В дополнение к полям task list item:
| Поле | Тип | Обязательность | Описание |
|---|---|---|---|
| `request_summary` | object | да | безопасная сводка trusted request |
| `criteria_ids` | array of string | да | |
| `observations` | array of string | да | |
| `blockers` | array of string | да | |
| `question` | string\|null | да | |
| `dependency_graph` | object | да | `{nodes: array of UUID, edges: array of {from,to}}` |
| `attempts_summary` | object | да | `{total, terminal, active}` |
| `acceptance_summary` | object | да | `{status, criteria_passed, criteria_total}` |
| `evidence_references` | array of object | да | `[{path, hash}]` |

### History response
| Поле | Тип | Обязательность | Описание |
|---|---|---|---|
| `timeline` | array of event | да | |

Event:
| Поле | Тип | Обязательность | Описание |
|---|---|---|---|
| `timestamp` | string RFC3339 | да | |
| `type` | string | да | `metadata_change\|activation\|stage_terminal\|attempt_terminal\|user_update\|blocker\|acceptance\|cancellation\|archive_change` |
| `revision_hash` | string | да | |
| `summary` | string | да | человекочитаемое описание |
| `evidence_reference` | object\|null | да | `{path, hash}` или `null` |

### Overview response
| Поле | Тип | Обязательность | Описание |
|---|---|---|---|
| `totals_by_status` | object | да | ключи — status enum, значения integer |
| `totals_by_stage` | object | да | |
| `totals_by_priority` | object | да | |
| `counts` | object | да | `{needs_input, blocked, running, completed, planned, archived, corrupt, orphaned}` |
| `generated_at` | string RFC3339 | да | |
| `scope_identity` | string | да | repository_id |

### Write response
| Поле | Тип | Обязательность | Описание |
|---|---|---|---|
| `schema_version` | integer | да | `1` |
| `task_id` | string (UUID) | да | |
| `revision` | string | да | revision hash |
| `status` | string | да | текущий lifecycle status |
| `archived` | boolean | да | |
| `priority` | string | да | |
| `title` | string | да | |
| `next_action` | string\|null | да | |

### Error response
| Поле | Тип | Обязательность | Описание |
|---|---|---|---|
| `error` | object | да | |
`error` object:
| Поле | Тип | Обязательность | Описание |
|---|---|---|---|
| `class` | string | да | `BF_INVALID\|BF_BLOCKED\|BF_CONFLICT` |
| `message` | string | да | человекочитаемое сообщение без secrets |

## Контекст 1С
- Конфигурация/подсистема: BSL Flow (PowerShell 7); метаданные 1С не изменяются.
- Затрагиваемые механизмы: действия Invoke-BSLFlowTask.ps1 (реестровые команды + JSON/human output), Task.Storage.ps1 (checkout-local v1, legacy read-only discovery), новый Task.Registry.ps1 (repository store, revisions, graph lock), output schemas, документация.
- Evidence path: CLI → resolve Git common dir/repository identity → validate journal(s) → derive catalog/detail/overview либо append one revision → existing controller gate. Внешних side effects, кроме локальных файлов и Git metadata reads, нет.
- 1С runtime: не запускается; target/credentials не читаются task catalog commands.

## Не делать
- Не копировать Markdown-per-task storage, browser UI, MCP server, milestones и document/decision management Backlog.md.
- Не создавать SQL/daemon/cloud service или второй authoritative task database.
- Не коммитить clone-local registry и execution evidence в Git.
- Не удалять completed/cancelled tasks автоматически.
- Не выводить raw prompts/evidence в list/overview.
- Не считать archived новым execution status.
- Не запускать dependency автоматически и не строить distributed scheduler.
- Не переписывать legacy journals при чтении или adoption.
- Не создавать `tasks.md`.
- Не использовать Go-движок или автоматическую конвертацию checkout-local → repository store.

## Критерии приёмки
- GIVEN два worktree одного клона создают разные planned-задачи WHEN `task list` выполняется из любого из них THEN обе задачи видны в одном repository scope с теми же UUID и canonical store identity.
- GIVEN два независимых клона одного remote WHEN в одном создана задача THEN второй клон её не видит и Git status не содержит registry files.
- GIVEN planned-задача активируется valid trusted request после объявления совместимой native controller capability WHEN controller читает её по прежнему UUID THEN появляется `ready` revision с сохранённой metadata, а второго ID/journal нет.
- GIVEN native controller capability ещё не готова либо выбран legacy engine WHEN вызывается `task activate` для repository planned-задачи THEN команда возвращает staged `BLOCKED`, не создаёт revision и не переносит задачу в checkout-local v1 store.
- GIVEN planned-задача без execution authorization WHEN вызывается `run` THEN attempt не создаётся и возвращается явный activation blocker.
- GIVEN planned-задача ссылается на существующую и на отсутствующую dependency WHEN metadata валидируется и сохраняется THEN edges сохранены; отсутствие dependency не блокирует создание/редактирование, а execution gating не применяется в этой версии.
- GIVEN попытка создать dependency cycle либо self-reference WHEN metadata validation выполняется THEN write отклоняется без новой revision.
- GIVEN два concurrent writers пытаются добавить edges `A→B` и `B→A` WHEN graph mutations публикуются THEN не более одной revision проходит repository graph critical section, вторая получает cycle/conflict без revision.
- GIVEN repository содержит planned, running, completed, cancelled и archived задачи WHEN выполняется default `task list` THEN возвращены все неархивированные статусы, включая completed/cancelled; archived доступны только явным фильтром.
- GIVEN один journal повреждён, а остальные валидны WHEN выполняются list и overview THEN валидные задачи присутствуют, повреждённая отображается диагностически и не считается выполненной.
- GIVEN index отсутствует, устарел или оборван WHEN выполняется read command THEN результат пересобирается из authoritative journals и совпадает с результатом чистой полной rebuild.
- GIVEN legacy task обнаружена в checkout-local `.bsl-flow/tasks` WHEN выполняется read-команда из любого worktree THEN задача видна с `source=legacy`, исходные bytes journal не изменены, а конфликтующий UUID показан диагностикой.
- GIVEN JSON и human modes получают один набор задач WHEN применены одинаковые filters/sort/cursor THEN membership/order совпадают, JSON проходит schema v1, secrets/raw evidence отсутствуют.

## Требуемые проверки
- [x] Static — closed schemas, CLI help/docs, package inventory и запрет secrets/raw evidence в catalog outputs.
- [x] Unit — repository identity/common-dir resolution, metadata validation, dependency cycles, filters/sort/cursors, redaction, status/archive separation и deterministic timeline.
- [x] Integration — несколько реальных temporary Git worktree, concurrent writers/встречные dependency edges, completed retention, corrupt/torn journals, read-derivation без производного index, legacy read-only discovery и worktree deletion.
- [x] Compatibility — historical v1 task fixtures читаются без изменения bytes/hashes; UUID conflicts fail closed.
- [ ] UI — отдельный UI не требуется.
- [ ] 1С runtime — не требуется для этой feature.

## Неопределённости / допущения
- Clone-local repository store размещается под verified Git common dir; путь заморожен: `<git common dir>/bsl-flow/tasks/<uuid>/revisions`, schema/store version v1.
- Fuzzy full-text search и board view могут быть добавлены поверх того же read model, но не входят в первую реализацию.
- Adoption старых journals отложен в отдельную спецификацию; automatic destructive move запрещён.
- Эта feature зависит от стабильного Git CLI; реализуется как PS-native vertical slice.
