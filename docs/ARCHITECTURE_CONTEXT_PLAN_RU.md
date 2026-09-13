# План архитектурного контекста и ADR-индекса

Статус: 2026-09-10 согласовано; этапы A–E реализованы offline. Замечания двух независимых ревью устранены: полный section hash в identity **всех** применимых ADR (включая исключённые из presentation), containment `source.path`/`refs`, единый architecture resolver, расширенный `task context` (source/package identity, последний terminal attempt с receipts), согласование schema (`active_attempt` строкой), проверяемые subject refs, корректный missing subject, лимит и `excluded` от реального prompt, настоящая package identity, versioned context на ошибке чтения, разделение Git stdout/stderr для стабильного resume-пилота. Документ задаёт отдельный increment после критических исправлений BFI-001–005. Реализация A–E не запускает модели, benchmark и не выполняет действий с базой 1С; `Context` — чистый read-only.

**Что реализовано (A, B, C, D, E).**

- A: `docs/architecture/adr-index.schema.json` (индекс + subject registry), `docs/architecture/adr-index.json` (ADR-1…ADR-10), валидатор `global/skills/1c-task/scripts/Task.Architecture.ps1`, offline `scripts/Test-ADRIndex.ps1` (25 checks: duplicate id, missing anchor, dangling reference, supersedes cycle, unknown subject, schema, containment `source.path`/`refs`, существование subject-файлов и symbol anchors, обязательные `informed_by`/`supersedes`, maxLength title). Канонический hash индекса сохраняется в `package-manifest.json` (`architecture.adr_index_sha256` / `adr_index_canonical_sha256`); повреждённая связь валит build.
- B: read-only action `Context` (`Invoke-BSLFlowTask.ps1`, `Get-BFTaskContext`), версионированная схема `global/skills/1c-task/schemas/context.schema.json`, тонкий relay в Go CLI (`task context`), projection с `next`/blocker/question/unknown-effect/evidence, `generated_from` (state/policy/source/ADR/package identity с `adr_root`/`adr_scope`/`package_source`), последний **terminal** attempt с receipts; `active_attempt` может быть UUID-строкой; ошибка чтения сохраняет versioned context envelope со `status=blocked` и полезным evidence. Offline `scripts/Test-TaskContext.ps1` (22 checks) и end-to-end проверка через `scripts/Test-BSLFlowCli.ps1`. CLI-embedded bundle содержит только `global/` + `VERSION`, поэтому вне репозитория контекст возвращает `missing_context=['adr-index']`, а не тихий fallback.
- C: `Get-BFStagePrompt` добавляет детерминированный, size-bounded presentation bundle; **identity** отдельно хранит все применимые `accepted` ADR с полным `section_sha256`, поэтому изменение любого применимого ADR — включая исключённый из presentation по лимиту — инвалидирует stage evidence. Лимит применяется к реальному отрендеренному тексту, а список `excluded` ограничен отдельно (`excluded` sample + `excluded_count`), без обхода через 64 итерации. Отсутствующий subject попадает в `missing_context`, а не роняет bundle. Subject-набор задан явно по этапам (`Get-BFStageSubjects`); hash входит в `Get-BFDependencies` (`architecture`). Отсутствующий индекс даёт `missing_context`, повреждённый — fail-closed. Offline `scripts/Test-TaskArchitectureBundle.ps1` (38 checks) и prompt regression: bundle помечен как instructional context, не authorization/acceptance.
- D: read-only resume-пилот `scripts/Test-TaskResumePilot.ps1` (64 checks) на трёх сохранённых сценариях реального controller — обычная остановка (accept), question/blocker (`needs_input`/`blocked`), modifying attempt с unknown effect (`recover`). Для каждого проекция сравнивается с journal identity/hashes, receipts и фактическим `Get-BFNext`; подтверждено отсутствие записи (fingerprint), различимость stale/missing и запрет повторного dispatch при unknown effect. Замерены размер bundle и число ручных обращений к источникам ADR (bundle инлайнит выдержки); качество/экономия токенов не заявляются.
- E: решено, что проектный ADR-index нужен как необязательный project-owned файл. Единый `Resolve-BFArchitectureContext`/`Get-BFArchitectureContextRoot` используется и контекстом, и stage prompt, и dependencies: project index > package root > missing. Bootstrap не создаёт и не считает его managed; повторный bootstrap сохраняет файл байт-в-байт. Проверка `scripts/Test-ProjectArchitectureIndex.ps1` (17 checks) покрывает отсутствие автосоздания, `missing_context` fallback, валидный проектный индекс, hash-bound dependencies и fail-closed при повреждении; без OpenSpec dynamic-bootstrap часть пропускается явным `PARTIAL`, а static-boundary и architecture-контракт выполняются всегда. Справка — `global/skills/1c-init-project/references/architecture-context.md`; обновлены `ARCHITECTURE_RU.md` (ADR-10) и `cli/README.md`. Модели не запускались.

Основание: в Cairn полезны три связанные идеи — выборка применимых архитектурных решений для конкретной работы, компактный контекст возобновления и машинно-читаемый индекс связей между ADR. BSL Flow принимает эти идеи как локальную read-only проекцию над существующим controller, OpenSpec и project policy, а не как новый фреймворк или источник истины.

## Цель

После паузы или перед новым этапом давать агенту и оператору короткий проверяемый ответ на четыре вопроса:

1. Где именно остановилась задача и каков авторитетный следующий шаг?
2. Какие принятые архитектурные решения относятся к этому шагу?
3. На какие исходники, контракты и evidence они опираются?
4. Что отсутствует или устарело и поэтому не может считаться разрешением либо PASS?

## Принятые решения

### 1. Один источник истины

Авторитетными остаются последовательный controller state, trusted input events, OpenSpec, project policy, manifests и receipts. Архитектурный контекст всегда:

- вычисляется только чтением этих источников;
- может быть полностью пересобран;
- не создаёт transition, authorization, acceptance или publication authority;
- не заменяет `Get-BFNext`, а включает его результат без альтернативной state machine;
- явно разделяет факты controller и справочные архитектурные пояснения.

Полная интеграция Cairn, отдельная база графа и новый DSL не нужны: они создали бы второй жизненный цикл рядом с уже существующими OpenSpec и controller state.

### 2. ADR-index индексирует, но не дублирует решения

Предлагаемый машинный артефакт: `docs/architecture/adr-index.json`. Это версионируемый индекс над текстом ADR в `docs/ARCHITECTURE_RU.md` и будущих специализированных ADR-файлах.

Минимальная запись:

```json
{
  "id": "ADR-5",
  "title": "Неизвестный эффект требует control read",
  "status": "accepted",
  "applies_to": ["controller.recovery", "controller.execution"],
  "source": {
    "path": "docs/ARCHITECTURE_RU.md",
    "anchor": "adr-5-неизвестный-эффект-требует-control-read"
  },
  "informed_by": ["ADR-1", "ADR-2"],
  "supersedes": [],
  "revisit_triggers": [
    "Появился проверяемый idempotency contract внешней операции"
  ]
}
```

Допустимые статусы: `proposed`, `accepted`, `superseded`, `deprecated`. Нормативным для bundle является только `accepted`; `superseded` может показываться лишь как lineage. Полный текст решения и его цена остаются в source ADR, поэтому индекс не превращается в расходящуюся копию документации.

Валидатор должен проверять:

- версию schema и уникальность `id`;
- существование source-файла и точного anchor;
- существование всех ссылок `informed_by` и `supersedes`;
- отсутствие циклов `supersedes`;
- согласованность статуса и lineage;
- известные subject ID;
- канонический JSON и детерминированный SHA-256 индекса.

### 3. Небольшой реестр архитектурных субъектов вместо общего графа

Для первого increment достаточно закрытого набора устойчивых subject ID:

- `controller.state`;
- `controller.gates`;
- `controller.execution`;
- `controller.recovery`;
- `adapter.codex`;
- `adapter.opencode`;
- `runtime.native-1c`;
- `publication.git`;
- `toolset.snapshot`.

Каждый subject связывается с конкретными путями, публичными командами или controller-функциями. Неизвестный subject не угадывается по embedding, имени файла или свободному тексту: он остаётся `missing_context` до явного уточнения индекса.

### 4. Две read-only проекции

#### Контекст возобновления

Предлагаемая публичная команда:

```text
bsl-flow task context --project <path> --task <uuid>
```

Go CLI только передаёт действие `Context` существующему controller и отображает его versioned JSON. Собственной логики переходов в CLI нет.

Минимальный результат:

- task ID, revision, status и stage;
- intent, policy и source hashes;
- авторитетный результат `Get-BFNext`;
- активный blocker, question либо unknown effect;
- свежие и устаревшие обязательные evidence;
- последний terminal attempt и связанные receipts;
- следующий разрешённый шаг и причина запрета остальных;
- `generated_from` с hashes state, policy, ADR-index и package.

Команда ничего не запускает и ничего не записывает. При повреждении или неполноте она по возможности возвращает структурированный `blocked`/`missing`, но не маскирует ошибку чтения как пустой успешный контекст.

#### Architecture bundle этапа

Перед формированием stage prompt controller сможет собрать ограниченный bundle:

- применимые `accepted` ADR с ID, заголовком и ссылкой на source;
- короткие нормативные выдержки, извлечённые из source ADR, а не скопированные в индекс;
- относящиеся к этапу schemas, contracts и ключевые symbols;
- hashes всех входов bundle;
- явный список `missing_context` и `stale_context`;
- границу: bundle является инструктивным контекстом, но не пользовательским допуском.

Bundle выбирается по явным subjects этапа и trusted task metadata. Он ограничен размером и детерминированно сортируется. Его hash включается в dependencies попытки, чтобы изменение принятого решения инвалидировало старый результат, а не незаметно меняло prompt.

## Порядок реализации

### Этап A. Schema и ручной индекс для самого BSL Flow (реализовано)

1. Зафиксировать JSON Schema индекса и subject registry.
2. Проиндексировать существующие ADR-1–ADR-10 без переноса их текста.
3. Добавить offline validator и негативные fixtures: duplicate ID, missing anchor, invalid reference, supersedes cycle, unknown subject.
4. Сохранить hash индекса в package manifest либо другом уже авторитетном package identity; не вводить отдельный mutable registry.

Критерий выхода: один и тот же snapshot даёт byte-identical валидный индекс; повреждённая связь блокирует package validation.

### Этап B. `task context` (реализовано)

1. Добавить read-only controller action `Context` поверх текущих `Status`, dependency calculation и `Get-BFNext`.
2. Определить versioned response schema и canonical JSON.
3. Добавить тонкий relay в Go CLI без локальных решений о state.
4. Покрыть completed, blocked, needs-input, failed и unknown-effect fixtures.

Критерий выхода: команда точно повторяет авторитетный next action, не создаёт файлов и не вызывает worker/provider/runtime.

### Этап C. Architecture bundle в stage prompt (реализовано)

1. Связать каждый stage с минимальным явным набором subjects.
2. Разрешать ADR через валидный индекс и фактические source sections.
3. Включить bundle hash в stage dependencies и attempt binding.
4. Ограничить размер и сохранить диагностический список исключённых/недостающих данных.
5. Добавить prompt regressions, запрещающие трактовать bundle как authorization или acceptance.

Критерий выхода: изменение применимого ADR меняет дополнительный bundle hash и делает старое stage evidence stale; неприменимое решение не раздувает prompt и само по себе не меняет bundle hash. Существующая полная инвалидизация по package identity, policy и source manifest при этом не ослабляется и может потребовать новую попытку независимо от subject-фильтра.

### Этап D. Проверка на реальном возобновлении (реализовано)

Провести read-only пилот минимум на трёх сохранённых сценариях:

1. обычная остановка между этапами;
2. задача с вопросом или blocker;
3. modifying attempt с unknown effect.

Для каждого сравнить проекцию с исходным journal, receipts и фактическим `Get-BFNext`. Измерить размер bundle и число ручных обращений к документации. Не объявлять экономию токенов или рост качества без наблюдаемого сравнения.

Критерий выхода: ни один сценарий не предлагает повторный dispatch при unknown effect, не переносит stale PASS и не скрывает недостающий контекст.

### Этап E. Решение о распространении на проекты (реализовано)

Решение после пилота: проектный ADR-index **нужен** как необязательный project-owned файл. Проект сам добавляет `docs/architecture/adr-index.json` (+ schema и нормативный source); контроллер предпочитает его package-root fallback, а при отсутствии сохраняет `missing_context`. Bootstrap не создаёт, не перезаписывает и не удаляет `docs/architecture`: повторный bootstrap оставляет индекс байт-в-байт. Повреждённый проектный индекс fail-closed, а не тихо игнорируется. Регрессия — `scripts/Test-ProjectArchitectureIndex.ps1`.

## Области будущих изменений

Предварительная карта, не обязательная структура реализации:

- `docs/architecture/adr-index.json` и его schema;
- новый небольшой controller-модуль чтения/проекции контекста;
- `Invoke-BSLFlowTask.ps1` для действия `Context`;
- `Task.Stages.ps1` для подключения bundle;
- Go CLI для relay команды;
- offline tests controller, CLI, package и prompt dependencies;
- `FRAMEWORK_GUIDE_RU.md`, `cli/README.md` и package docs.

Перед реализацией нужно уточнить фактические точки расширения и не дробить текущие controller-модули без необходимости.

## Риски и ограничения

| Риск | Защита |
| --- | --- |
| Индекс расходится с ADR | В индексе нет полного текста; source/anchor и hash обязательны |
| Bundle становится вторым policy engine | Он включает `Get-BFNext`, но не вычисляет альтернативный переход |
| Resume скрывает unknown effect | Unknown effect и control-read показываются как первичный blocker |
| Prompt разрастается | Явные subjects, лимит размера и детерминированный отбор |
| Изменение ADR не инвалидирует результат | Hash применимого bundle входит в attempt dependencies |
| Автоматическое сопоставление ошибается | Нет неявного semantic matching; неизвестное остаётся missing |
| Проектам навязывается новая структура | Первый increment ограничен архитектурой самого BSL Flow |

## Не входит в scope

- зависимость от Cairn или перенос его хранилища;
- новый графовый сервис, UI или дерево symbols всего репозитория;
- автоматическая генерация или принятие ADR моделью;
- изменение lifecycle, recovery, acceptance и publication semantics;
- запуск моделей, runtime 1С или benchmark;
- автоматическое добавление ADR-файлов во все BSL Flow проекты.

## Definition of Done

Increment завершён, когда:

- ADR-index валидируется, детерминирован и не дублирует нормативный текст;
- `task context` является чистым read-only представлением текущего journal и `Get-BFNext`;
- применимый architecture bundle ограничен, hash-bound и fail-closed при повреждённых ссылках;
- proposed/superseded ADR не становятся нормативными незаметно;
- stale evidence, missing context и unknown effect остаются различимыми;
- CLI не содержит второй state machine;
- offline tests и три resume-пилота подтверждают контракт;
- документация явно отделяет архитектурный контекст от authorization, acceptance и runtime evidence.

## Источник идеи и граница заимствования

Заимствуется форма решения, а не реализация Cairn: его bundle собирает связанные решения и зависимости для работы, а query API предоставляет компактные контекстные представления. Для BSL Flow это адаптируется к существующим PowerShell controller contracts и Go CLI. Совместимость с BSL/1С, текущие gates и recovery должны быть доказаны собственными tests; наличие похожего механизма в Cairn таким доказательством не является.
