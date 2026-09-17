# Technical design

## Решение
Добавить отдельную controller-owned Learning Plane поверх существующей Execution Plane. Авторитетными остаются task journal, receipts, gates, version-controlled policy и фактическое состояние файлов/внешних систем. Learning Plane хранит только append-only события опыта и воспроизводимые производные проекции: memory index, stage bundle и Resume Capsule.

Поток данных:

`journal/receipt/test evidence` → `experience extractor` → `memory event log` → `shadow + promotion policy` → `derived index` → `bounded stage bundle` → существующие `task context`/`Get-BFNext` и attempt.

Worker может предложить наблюдение, но только controller классифицирует, очищает, дедуплицирует и записывает candidate. Worker читает конкретный bundle и не получает прямого write-доступа к memory store.

## Затрагиваемые компоненты
- Метаданные: versioned JSON Schemas для memory event/record/index/bundle/Resume Capsule; новые поля task context только optional и backward-compatible.
- Модули: extension существующего controller context path, отдельный memory store/extractor/retriever/promotion policy и offline test helpers. Точные имена файлов выбираются после сверки текущей структуры.
- Регистры/движения: не применимо; используется локальный append-only event log и производный индекс.
- Формы: не применимо.
- Интеграции: stage prompt/bundle adapter; внешних memory API нет.
- Фоновые/регламентные механизмы: обязательного daemon/job нет; promotion и rebuild запускаются синхронно controller либо явной локальной maintenance-командой.

Логическая раскладка controller-owned state:

```text
.bsl-flow/memory/
  events/          # immutable canonical events
  index.json       # derived, rebuildable projection
  bundles/         # attempt-bound derived bundles или их receipts
  quarantine/      # derived view, не отдельный источник истины
```

Физическая раскладка может быть скорректирована под существующий store, если сохраняются append-only история, atomic write и rebuildability.

Минимальный memory event содержит: `schema_version`, `event_id`, `project_id`, `timestamp`, `record_id`, `event_type`, `knowledge_class`, `risk_class`, `scope`, `observation`, `recommended_action` или `avoid_action`, `evidence_refs`, `fingerprints`, `source_task_id`, `source_attempt_id`, `reason`, `previous_event_id`, `content_hash`. `knowledge_class` ограничен значениями `procedural|diagnostic|business_rule|authorization|test_or_waiver|architecture|controller_policy|model_routing|runtime_or_external_effect`, `risk_class` — `low|medium|high`; неизвестное значение отклоняется fail-closed. Свободный текст ограничивается размером и проходит redaction; доказательство хранится ссылкой и hash, а не копией артефакта.

Состояние записи вычисляется replay событий. Разрешённые переходы:

- `candidate → shadow → accepted`;
- `candidate|shadow|accepted → quarantined|deprecated`;
- `accepted → superseded` с обязательной ссылкой на новую запись;
- возврат из quarantine только отдельным подтверждённым событием, без удаления причины карантина.

Retriever выполняет точное фильтрование по обязательным fingerprints, затем ранжирует фиксированными правилами: stage, task kind, object/path scope, error signature, recency policy и record ID как стабильный tie-breaker. Он применяет лимиты количества и размера. Bundle содержит только минимальный текст действия/предупреждения, evidence refs, состояние, причины выбора, record IDs и canonical hash.

Promotion policy реализуется как чистая детерминированная функция над replayed record и текущей version-controlled policy. Production-подтверждения группируются по canonical key из project ID, scope, recommended/avoid action и обязательных fingerprints и считаются независимыми только для разных task IDs. Категории, способные менять полномочия, критерии PASS, бизнес-смысл или необратимые эффекты, fail-closed исключаются из автоматического promotion. Replay fixtures проверяют функцию, но не увеличивают production-счётчики.

Компактная Resume Capsule строится как расширение существующего `task context` из уже принятого результата `Get-BFNext`/`Resume-BFAttempt`, рабочего набора и выбранного bundle. Feature не вычисляет повторно класс эффекта, не меняет `side_effects`/`unresolved_effect` и не заменяет существующую сверку source manifest/receipt. Это сохраняет единственный recovery-контракт и отделяет дельту памяти от параллельно реализуемого архитектурного контекста.

## Границы выполнения
- Клиент: существующий CLI/context output при необходимости показывает причины выбора и blockers; отдельная inspection-команда не является требованием feature. CLI не принимает самостоятельных решений о promotion.
- Сервер: не применимо; все операции локальны проекту. Возможная будущая remote storage не входит в scope.
- Транзакции/блокировки: один controller-writer на memory store; append события через temp + atomic rename, затем обновление производного индекса. Lock памяти не должен расширять task lock на длительную работу worker. Torn write игнорируется/карантинируется и не меняет последнюю валидную цепочку.
- Права: запись разрешена только controller; worker и review-процессы получают read-only bundle. Project boundary проверяется до чтения/записи; пути вне root и symlink escape запрещены.
- Производительность: retrieval работает по bounded derived index, не сканирует проект или весь event log на каждый prompt; rebuild допускает линейный replay. Жёсткие лимиты bundle предотвращают неконтролируемый рост контекста.

## Совместимость и данные
Новые контракты versioned и additive. Существующие задачи без memory metadata трактуются как имеющие пустой store. Изменение формата текущего task journal не требуется; если attempt получает memory bundle, в journal/receipt сохраняются только bundle ID/hash и версия policy.

`index.json`, quarantine view и Capsule не мигрируются как авторитетные данные: они перестраиваются. Immutable events мигрируются только отдельной versioned командой с сохранением исходных событий и receipt. При неизвестной новой schema controller читает историю задачи, но отключает применение памяти и выдаёт blocker/warning согласно совместимости, не угадывая поля.

Normative memory остаётся в существующих version-controlled источниках: AGENTS.md, `bsl-flow.yaml`, schema, skills, ADR, tests и controller policy. Experience memory никогда не перезаписывает их автоматически. Подтверждённое процедурное знание может влиять только через attempt bundle; перенос в нормативный источник является отдельным reviewable change.

## Альтернативы
- Встроить feature в текущий план архитектурного контекста — не выбрано: план уже реализуется другим агентом, а независимый change снижает конфликт и позволяет отдельно принять границы обучения.
- Хранить всё в ADR — не выбрано: ADR нормативны и рассчитаны на человеческое архитектурное решение, а не на большое число операционных наблюдений и ошибок.
- Разрешить модели самостоятельно редактировать skills/controller — не выбрано: это смешивает опыт и полномочия, делает gates самоменяющимися и ухудшает аудит.
- Сразу использовать embeddings/vector DB/graph — не выбрано: для проектного controller state достаточно детерминированных ключей; внешний индекс добавляет недоказанную сложность, приватность и невоспроизводимость.
- Создать глобальную память между проектами — не выбрано: велик риск утечки контекста и ложного переноса правил между конфигурациями и заказчиками.
- Хранить snapshot всего проекта в Capsule — не выбрано: он быстро устаревает, дублирует source of truth и не решает проверку побочных эффектов.

## Риски
| Риск | Как снижаем |
|---|---|
| Ошибочное знание закрепится и будет повторять дефект | Shadow phase, независимые подтверждения, evidence refs, contradiction counter, quarantine и fail-closed категории |
| Память обойдёт gate или расширит права | Capsule/bundle только советуют; controller journal и policy остаются единственными авторитетными разрешениями |
| Утечка секретов, кода или данных между проектами | Project-local store, path boundary, redaction, allowlisted поля, refs+hash вместо копий, запрет cross-project retrieval |
| Сбой во время записи повредит историю | Immutable canonical events, atomic rename, hash chain, single writer, rebuild derived index |
| Устаревшая запись применяется к новой версии | Обязательные fingerprints controller/schema/toolchain/policy и автоматическая invalidation/quarantine |
| Контекст снова разрастается до размера проекта | Детерминированный retrieval, лимиты bytes/items, рабочий набор вместо snapshot, reasons и stable ordering |
| Два механизма восстановления расходятся | Capsule расширяет готовый результат `task context`/`Get-BFNext`/`Resume-BFAttempt`; повторная recovery-классификация и отдельная task state machine запрещены |
| Автоматизация повторит неизвестный внешний эффект | Матрица эффектов, control-read, idempotency/read-back и `BLOCKED` по умолчанию |
| Формат событий станет трудно менять | Versioned schemas, additive references, replay tests и отдельная миграция с receipt |

## Стратегия проверки
1. Schema/static: validate закрытые контракты, canonical serialization/hash, path boundaries, version compatibility и отсутствие запрещённых полей.
2. Unit: replay lifecycle, deduplication, independent-confirmation counting, promotion matrix, contradiction/quarantine, stable retrieval/tie-breaks, bundle limits, redaction и fingerprint invalidation.
3. Offline integration: прогнать цепочку из нескольких task fixtures от evidence до accepted memory и нового attempt; затем воспроизвести crash на durable boundaries memory store, torn event, удалённый индекс и несовпадающий workspace hash.
4. Recovery integration: подать feature готовые результаты существующего `Resume-BFAttempt` для resume/retry/control-read/BLOCKED и доказать, что bundle не меняет disposition, `side_effects` или `unresolved_effect`.
5. Compatibility smoke: существующие задачи и suites проходят с feature disabled и с пустым store; старый journal читается без миграции.
6. Security negative tests: secret-like values не попадают в event/bundle, worker не пишет в store, попытка path escape отклоняется, запись другого project ID не извлекается.
7. Independent criticism-only review сверяет spec и design с текущими controller contracts; все findings получают явную reconciliation до начала реализации.

Runtime 1С, UI и внешние публикации не запускаются: достаточным доказательством для этой feature являются static/unit/offline controller integration проверки.
