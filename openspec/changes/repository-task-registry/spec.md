# repository-task-registry

## Классификация

- Сложность: L
- Риск: high

## Цель

Добавить в BSL Flow единый локальный каталог задач одного Git-клона, доступный из любого worktree, с planned-задачами, сохранением завершённой истории и стабильными human/JSON read-командами, не создавая второй источник истины рядом с controller journals.

## Текущее поведение

- Controller хранит каждую зарегистрированную задачу в `.bsl-flow/tasks/<uuid>/revisions`; hash-linked revisions являются источником истины, а `current.json` — производной проекцией.
- `bsl-flow task status|next|context` требует заранее известный UUID. Команд перечисления, поиска, overview и истории нет.
- Статусы ограничены исполняемым lifecycle `ready|running|needs_input|blocked|failed|completed|cancelled`; planned-задачи отсутствуют.
- Task store привязан к переданному project root. Задачи, созданные из разных Git worktree одного клона, не образуют общего каталога.
- Trusted request не имеет отдельного короткого title, priority, labels или межзадачных dependencies.

## Требуемое поведение

1. Для Git-репозитория BSL Flow должен разрешать один clone-local repository store относительно проверенного абсолютного `git common dir`. Все worktree, принадлежащие тому же common dir, используют один store. Сеть, Git remote и внешний сервис для каталога не требуются.
2. Истиной каждой новой planned/controller-задачи остаётся один immutable hash-linked journal. Каталог, overview, board-проекции и поисковые индексы являются полностью восстанавливаемыми derived data и не могут разрешать execution, acceptance или publication.
3. Новая задача создаётся с UUID и статусом `planned`. Обязательны непустой `title` и schema version; optional поля первой версии: `description`, `priority=low|medium|high|critical`, уникальные `labels` и уникальные `depends_on` UUID того же repository store. Milestones, assignee, comments, due dates и произвольные custom fields не входят в v1.
4. Planned-задача не имеет execution authorization, worker path, baseline, active attempt или ложного stage. Её metadata редактируется через optimistic `expected_revision`; удаление journal и переиспользование UUID запрещены.
5. `task activate` принимает полный trusted request текущего controller contract, проверяет выбранный worktree/project root и добавляет новую revision того же task ID со статусом `ready`. Активация не создаёт вторую задачу и необратима без отдельного cancel; planned metadata сохраняется. Команда доступна только при native controller capability, который умеет продолжить этот schema/store; до этого этапа planned-задачи остаются читаемыми/редактируемыми, а activation возвращает staged `BLOCKED` без revision.
6. Для активированной задачи существующий controller lifecycle и authority сохраняются. `archived` является presentation flag, а не execution status и не меняет `next_action`, acceptance, recovery или dependency result.
7. `depends_on` является metadata planned-задачи: незавершённые зависимости не запрещают создание или активацию, межзадачной транзакции и автоматического запуска dependency нет. Controller-owned pre-dispatch gate (проверка эффективного `completed`, классификация `stale-completed`, привязка revisions/hashes dependency к attempt, dispatch-drift detection) и понятия `fresh effective completed`/`stale-completed` в этой версии не определены и отложены в отдельную спецификацию native controller write slice. `run` в этой версии не использует `depends_on`.
8. Создание/редактирование dependency должно отклонять self-reference, duplicate ID и цикл по текущим валидным journals. Проверка графа и публикация dependency revision сериализуются общим repository graph lock либо эквивалентным generation-CAS, поэтому встречные concurrent edges не могут обе пройти на одном snapshot. Проверка результатов dependency перед dispatch не входит в metadata-only contract требования 7 и требует отдельной спецификации.
9. `bsl-flow task list --project <любая-worktree>` по умолчанию возвращает все неархивированные задачи repository store, включая `planned`, `completed` и `cancelled`. Поддерживаются точные фильтры `status`, `stage`, `priority`, `label`, `updated-before|after`, `archived`, deterministic sort, bounded `limit` и opaque cursor.
10. Строка списка не содержит полный prompt, raw events/evidence или secrets. Она содержит task ID, title, lifecycle status, stage/next action при наличии, priority, labels, dependency summary, created/updated time, originating/current worktree identity и diagnostic state.
11. `task show` возвращает карточку и безопасную controller projection: request summary, criteria IDs/observations, status/stage/next action, blockers/question, dependency graph, attempts summary, acceptance summary и evidence references. Raw provider bodies, credentials и private runtime input не выводятся.
12. `task history` строит deterministic chronological timeline из проверенных revisions/events: metadata changes, activation, stage/attempt terminals, user updates, blockers, acceptance, cancellation и archive changes. По умолчанию payload/evidence content не разворачивается; ссылки и hashes сохраняются.
13. `task overview` возвращает totals по lifecycle status/stage/priority, counts `needs_input|blocked|running|completed|planned`, число archived, dependency-blocked, corrupt и stale-completed задач, а также время построения и scope identity.
14. Все read-команды имеют один versioned noninteractive JSON document (`schema_version: 1`) на stdout и отдельный стабильный human-readable table/detail output. Write-команды возвращают поля `schema_version`, `task_id`, `revision`, `status`, `archived`, `priority`, `title`, `next_action`; read-команды — `schema_version`, `repository_id` и allowlisted проекции задач. Exit-контракт зафиксирован: `0` — успех, `2` — `BF_INVALID` (некорректные аргументы/ввод/схема), `11` — `BF_BLOCKED`/`BF_CONFLICT` (blocker либо конфликт revision/цикла). Ошибка CLI пишет controller-shaped JSON в JSON mode либо concise stderr в human mode; первое поле blocker-сообщения — класс (`BF_INVALID`, `BF_BLOCKED`, `BF_CONFLICT`).
15. Повреждённый, torn или конфликтующий journal не должен молча исчезать и не должен делать невидимыми остальные задачи. List/overview возвращают diagnostic entry с известным task/source identity и `health=corrupt|conflict|orphaned`; `show/history` конкретной повреждённой задачи завершаются `BLOCKED` с точной причиной.
16. Read-команды строят результат непосредственно из авторитетных journals. Производный `catalog/index.json` не является обязательным артефактом, никогда не читается как источник истины и, если сохраняется, содержит source revision/hash и полностью восстанавливается из journals. Отсутствие или повреждение производного артефакта не влияет на read-результат и не отменяет подтверждённую revision.
17. Existing checkout-local `.bsl-flow/tasks` остаются читаемыми через legacy discovery (read-only), их bytes не переписываются. Совпадающий UUID с одинаковой verified history дедуплицируется; различающаяся неподтверждённая history показывается как conflict и никогда не выполняется молча. Automatic destructive move запрещён. Полный adoption (portable artifact manifest, immutable segment, source-prefix→canonical-continuation lineage receipts) отложен в отдельную спецификацию; в этой версии явный `adopt` не выполняет запись в canonical store.
18. Repository store должен переживать удаление рабочего worktree: worktree path является provenance/execution binding, но не местом хранения canonical task history. Историческое `completed`/`accepted` состояние сохраняется независимо от живых входов. Наблюдаемый предикат freshness ограничен диагностикой: если запись ссылается на абсолютный live input (baseline/worker/evidence), которого больше нет, read-проекция помечает её `stale|orphaned`; этот diagnostic не меняет исторический статус и не служит execution/acceptance gate в этой версии.
19. Registry metadata и output не должны содержать runtime credentials, Authorization headers или raw private evidence. Clone-local хранение не означает разрешение на передачу данных внешней модели либо Git remote.
20. Реализация должна быть первым Go-native read/write vertical slice и не должна требовать PowerShell для `create|edit|list|show|history|overview|archive|unarchive`. `activate` включается вместе с первым совместимым native controller write slice. Legacy PowerShell может продолжать только existing checkout-local v1 tasks и никогда не пишет repository v2 store; automatic conversion/fallback запрещены.

## Контекст 1С

- Конфигурация/подсистема: BSL Flow CLI/controller; метаданные 1С не изменяются.
- Затрагиваемые механизмы: CLI parser/output, task/state schemas, immutable storage, dependency gate, runner decision, Git worktree/common-dir discovery, migrations и package assets.
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

## Критерии приёмки

- GIVEN два worktree одного клона создают разные planned-задачи
  WHEN `task list` выполняется из любого из них
  THEN обе задачи видны в одном repository scope с теми же UUID и canonical store identity.
- GIVEN два независимых клона одного remote
  WHEN в одном создана задача
  THEN второй клон её не видит и Git status не содержит registry files.
- GIVEN planned-задача активируется valid trusted request после объявления совместимой native controller capability
  WHEN controller читает её по прежнему UUID
  THEN появляется `ready` revision с сохранённой metadata, а второго ID/journal нет.
- GIVEN native controller capability ещё не готова либо выбран legacy engine
  WHEN вызывается `task activate` для repository planned-задачи
  THEN команда возвращает staged `BLOCKED`, не создаёт revision и не переносит задачу в checkout-local v1 store.
- GIVEN planned-задача без execution authorization
  WHEN вызывается `run`
  THEN attempt не создаётся и возвращается явный activation blocker.
- GIVEN planned-задача ссылается на существующую и на отсутствующую dependency
  WHEN metadata валидируется и сохраняется
  THEN edges сохранены; отсутствие dependency не блокирует создание/редактирование, а execution gating не применяется в этой версии.
- GIVEN попытка создать dependency cycle либо self-reference
  WHEN metadata validation выполняется
  THEN write отклоняется без новой revision.
- GIVEN два concurrent writers пытаются добавить edges `A→B` и `B→A`
  WHEN graph mutations публикуются
  THEN не более одной revision проходит repository graph critical section, вторая получает cycle/conflict без revision.
- GIVEN repository содержит planned, running, completed, cancelled и archived задачи
  WHEN выполняется default `task list`
  THEN возвращены все неархивированные статусы, включая completed/cancelled; archived доступны только явным фильтром.
- GIVEN один journal повреждён, а остальные валидны
  WHEN выполняются list и overview
  THEN валидные задачи присутствуют, повреждённая отображается диагностически и не считается выполненной.
- GIVEN index отсутствует, устарел или оборван
  WHEN выполняется read command
  THEN результат пересобирается из authoritative journals и совпадает с результатом чистой полной rebuild.
- GIVEN legacy task обнаружена в checkout-local `.bsl-flow/tasks`
  WHEN выполняется read-команда из любого worktree
  THEN задача видна с `source=legacy`, исходные bytes journal не изменены, а конфликтующий UUID показан диагностикой.
- GIVEN JSON и human modes получают один набор задач
  WHEN применены одинаковые filters/sort/cursor
  THEN membership/order совпадают, JSON проходит schema v1, secrets/raw evidence отсутствуют.

## Требуемые проверки

- [x] Static — closed schemas, CLI help/docs, package inventory и запрет secrets/raw evidence в catalog outputs.
- [x] Unit — repository identity/common-dir resolution, metadata validation, dependency cycles, filters/sort/cursors, redaction, status/archive separation и deterministic timeline.
- [x] Integration — несколько реальных temporary Git worktree, concurrent writers/встречные dependency edges, completed retention, corrupt/torn journals, read-derivation без производного index, legacy read-only discovery и worktree deletion.
- [x] Compatibility — historical v1 task fixtures читаются без изменения bytes/hashes; UUID conflicts fail closed.
- [ ] UI — отдельный UI не требуется.
- [ ] 1С runtime — не требуется для этой feature.

## Неопределённости / допущения

- Clone-local repository store размещается под verified Git common dir; точное имя каталога фиксируется design и schema version.
- Fuzzy full-text search и board view могут быть добавлены поверх того же read model, но не входят в первую реализацию.
- Adoption старых journals отложен в отдельную спецификацию; automatic destructive move запрещён.
- Эта feature зависит от стабильного Git CLI и является первым этапом `native-cross-platform-cli`.
