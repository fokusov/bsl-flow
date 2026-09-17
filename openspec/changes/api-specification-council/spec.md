# api-specification-council

## Классификация
- Сложность: L
- Риск: high

## Цель
Заменить обязательную зависимость ревью спецификаций от OpenCode на встроенный в BSL Flow конфигурируемый совет LLM-ролей, который через прямые API-вызовы или fresh-context fallback текущего агента независимо исследует исходную задачу и черновую спецификацию, выявляет недостатки, выполняет доказуемую reconciliation и выпускает минимально исправленную, однозначно исполнимую спецификацию без создания второй state machine и без передачи моделям полномочий controller.

## Текущее поведение
- Assisted-review запускается через `global/skills/1c-spec-review/scripts/Invoke-1CSpecReview.ps1`, принимает только `review.reviewer.provider: opencode`, требует executable OpenCode и разбирает его JSONL transport.
- Managed-review в `Task.ManagedReview.ps1` вызывает одну reviewer-модель через `Invoke-BFManagedWorker`, после чего общий код всё равно записывает `reviewer.provider: opencode`.
- `review-schema.json` и `Review.Common.ps1` допускают только один OpenCode reviewer. Проектный шаблон `bsl-flow.yaml` содержит одну секцию `review.reviewer`.
- Controller уже владеет маршрутом `inspect → spec → spec_review → implement`, immutable attempts, dependency hashes, budget admission, reconciliation и final validation. Эти полномочия и последовательность остаются источником истины.
- Reviewer критикует, а отдельный reconciler принимает или отклоняет findings; модельный результат сам по себе не разрешает implementation или acceptance.

## Требуемое поведение
1. BSL Flow должен сохранить единственную существующую стадию `spec_review`. Внутри неё controller выполняет один bounded council cycle: фиксирует входы, запускает независимые роли, передаёт их результаты председателю, проверяет reconciliation и выполняет deterministic final gate. Council не создаёт собственную очередь, state machine, authorization или acceptance.
2. Новый council path не должен требовать OpenCode, Qwen Code или другой agent harness. Прямые внешние вызовы выполняются узкими transport adapters; fallback выполняется существующим host-agent adapter. Удаление общих OpenCode worker/benchmark adapters не входит в feature, но OpenCode перестаёт быть обязательным и дефолтным provider для review.
3. В переносимом `bsl-flow.yaml` пользователь должен иметь возможность объявить именованные API providers, model profiles и привязку model profile к каждой council role. Model profile задаёт provider, точный model ID и поддерживаемый effort/variant. Provider задаёт protocol, необязательный `base_url` для известного API и ссылку `token_env` на переменную окружения.
4. Литеральный `token` и локальное переопределение `base_url` разрешены только в ignored project-local `.bsl-flow/providers.local.yaml`. Committed `bsl-flow.yaml`, request/state, receipts, diagnostics, metrics и raw metadata не должны содержать значение токена или Authorization header. Локальный token имеет приоритет над `token_env`; token из environment используется при отсутствии локального token.
5. Для известных providers стандартный HTTPS endpoint используется при отсутствии `base_url`. Для custom/OpenAI-compatible provider `base_url` обязателен. Итоговый endpoint должен быть HTTPS; loopback HTTP допускается только отдельным явным local-development флагом. Redirect на другой host запрещён. Credential передаётся только итоговому разрешённому host.
6. Если token для выбранного API не разрешён, отсутствует или пуст, роль по умолчанию должна выполняться моделью текущей задачи через `current_agent` в новом независимом контексте. Это fallback роли, а не имитация запрошенной модели. Источником фактических model/effort служит только доверенный host invocation contract и его controller-observed receipt; значение из project config или model output не является доказательством. Если роль настроена с `fallback: block`, отсутствие token возвращает `BLOCKED` до model dispatch.
7. Fresh-context fallback должен использовать модель текущей Codex/host-задачи без истории основной беседы и только с разрешённым snapshot роли. До dispatch adapter обязан подтвердить capability для отдельного контекста, выбранных model/effort, sealed input/no-tools и terminal receipt. Если host не поддерживает или не может подтвердить любой из этих контрактов, обязательная роль блокируется; inline-продолжение текущего диалога, запуск другой модели и неявная подмена на Luna не являются fallback.
8. Каждая роль конфигурируется полями `enabled`, `required`, `model` и `fallback`. `enabled: false` исключает вызов; `required: true` допустим только при `enabled: true`; ошибка необязательной роли фиксируется и не блокирует chair; отсутствие успешного результата обязательной роли блокирует council. При `council.enabled: true` роли `chair` и deterministic `final_gate` обязательны и не могут быть отключены.
9. Дефолтный council для задач, где review требуется текущей risk-based routing policy, включает обязательные `intent_critic`, `architecture_critic`, `executability_critic` и `chair`. `brainstorm` по умолчанию выключен и необязателен; пользователь может включить его и сделать обязательным. Для S-задач council остаётся необязательным, если project/request policy не требует review.
10. До первого вызова controller должен создать неизменяемый bounded UTF-8 snapshot: `original-task.md`, черновые `spec.md` и optional `design.md`, classification, policy/routing, rubric и компактный hash-bound evidence/architecture bundle из уже проверенных результатов inspect. Советники не получают shell, MCP, web, subagents, произвольный read проекта или write tools.
11. `brainstorm` получает original task, classification и evidence bundle, но не получает черновые spec/design. Он возвращает альтернативы, риски, неизвестные предпосылки и вопросы без verdict и без текста новой спецификации. Critics получают original task, draft spec/design, evidence bundle и свою rubric, но не ответы других ролей.
12. `intent_critic` ищет потерянные требования, intent drift, unsupported assumptions и scope creep. `architecture_critic` проверяет соответствие существующим механизмам, минимальность, данные, транзакции/блокировки, интеграционные и security boundaries. `executability_critic` ищет неоднозначности реализации, отсутствующие решения, нетестируемые критерии и неполную requirement-to-observation трассировку.
13. Каждая critic-role возвращает только versioned role payload с verdict, findings и protected `do_not_change`. Finding должен иметь уникальный в пределах роли ID, severity, category, spec reference, issue, evidence и минимальное correction direction. Общий identity finding — `<role>:<id>`; объединение похожих findings не может терять ссылки на исходные identities. Модель не заполняет и не подтверждает provider/model/effort, status, usage, timestamps, input/payload hashes или execution mode: controller строит этот envelope исключительно из frozen inputs, transport/host observations и terminal receipt; конфликтующие claims внутри model payload отклоняются schema validation.
14. После завершения всех допустимых ролей `chair` получает frozen original/draft/evidence и структурированные результаты членов совета. Chair обязан рассмотреть каждый finding и protected item ровно один раз, вернуть решение `accepted`, `rejected` или `partially_accepted` с reason/evidence/resolution и выдать минимально изменённые полные `spec`/optional `design`. Для partial decision должны быть раздельно названы принятая и отклонённая части; предпочтительны атомарные findings.
15. Chair не может разрешать неотвеченный бизнес-вопрос, придумывать evidence, менять classification или расширять task authority. Материальная неоднозначность должна дать `needs_input`; недоступное доказательство — `BLOCKED`. Согласие большинства и model-reported confidence не являются gate.
16. До dispatch controller должен построить versioned requirement-binding manifest: стабильные material requirement IDs, hashes доверенного исходного текста и явные draft references. Chair обязан вернуть для каждого ID final references и для каждого accepted/partially accepted finding — resolution references. Deterministic final gate проверяет lint, неизменность frozen inputs, совпадение manifest/hash, полноту множеств IDs и reconciliation decisions, валидность references, evidence для rejected частей, сохранность структурных `do_not_change`, classification, отсутствие placeholders и отсутствие явно помеченных unresolved material unknowns. Gate не утверждает семантическую эквивалентность естественного языка: смысловую полноту оценивают critics/chair, а deterministic слой доказывает только структурную связность и может понизить результат, но не повысить model verdict или самостоятельно разрешить implementation.
17. Новый `review.json` schema v2 должен хранить sanitized council provenance: настройки и terminal status каждой роли, requested и observed provider/model/effort, execution mode `direct_api|current_agent_fallback`, fallback reason, hashes, aggregate findings, chair reconciliation reference и diversity status. Raw prompts/responses и provider receipts остаются только в ignored attempt directory. Исторические валидные v1 OpenCode review artifacts должны оставаться читаемыми и не переписываться.
18. Council report должен различать `multi_model`, `multi_role_single_model`, `degraded` и `unknown` на основании observed provenance. Fallback всегда видим. Если несколько ролей выполнены одной моделью, результат нельзя называть multi-model review. Неизвестный observed model не считается доказательством разнообразия.
19. Каждый member/chair dispatch должен иметь отдельный durable attempt, созданный до network/host dispatch, с binding provider/model/effort/protocol, полным нормализованным non-secret endpoint (`scheme`, `host`, effective `port`, normalized base path), versioned transport-capability mapping, prompt/schema versions и input hashes. Token, token hash и Authorization не входят в binding. Завершённая сохранённая попытка переиспользуется без нового вызова только при полном совпадении binding.
20. Вызовы независимых советников могут выполняться параллельно с ограниченной controller-owned concurrency; chair запускается только после terminal classification всех enabled roles. Один writer сохраняет aggregate artifacts. Порядок завершения не должен менять canonical review/reconciliation result.
21. Transport должен иметь bounded connect/overall timeout, cancellation, input/output limits и строгий JSON Schema parse. Pre-dispatch failure можно повторить технически один раз; timeout/обрыв после возможного принятия запроса создаёт unknown cost/effect и запрещает слепой retry. Provider error, malformed/ambiguous response или output overflow не публикуют успешный member result.
22. Budget admission/reservation и outcome reconciliation применяются к каждому платному direct API dispatch и ко всему council cycle. Unknown cost не преобразуется в zero. Provider-reported usage/cost хранится отдельно от billed/observed данных; отсутствие usage не блокирует уже доказанное содержимое review, но влияет на последующий budget admission согласно общей policy.
23. Наличие enabled role, выбранного provider/model и разрешённого token является явным разрешением передать этому endpoint только bounded council snapshot. Другие файлы проекта, credentials, private cache, содержимое базы 1С и raw controller state не отправляются. Delimiters и prompt-injection policy маркируют project/task content как untrusted data.
24. Assisted и managed режимы должны использовать один council contract, одни role/output schemas, provider resolution, final gate и artifact format. Chair является единственным модельным reconciler: в managed path Council Engine заменяет отдельный вызов `spec_reconcile`, сохраняя controller-owned reconciliation sidecar, validation и route transition. Host-specific adapters меняют только transport/current-agent execution и provenance, но не правила review, reconciliation или acceptance.
25. После успешного chair result controller должен сначала сохранить immutable prepared-publication package: expected draft hashes, точные final bytes/hashes `spec.md`/optional `design.md`, canonical review/reconciliation bytes и inputs final validation. Только после durable `prepared` event один writer обновляет live artifacts. Resume без model/API calls допускается, если каждый live file совпадает либо с expected draft, либо с intended final bytes; любое третье содержимое даёт `BLOCKED`. Успех публикуется только после совпадения всех intended final bytes, final validation и durable `completed` event.
26. Новые проекты должны получать portable council defaults без OpenCode dependency. Явно существующая legacy OpenCode configuration не должна молча переосмысливаться: она либо продолжает отдельный явно выбранный compatibility route, либо получает детерминированный migration blocker. Автоматическое удаление OpenCode integration и изменение чужих credentials/configuration не входят в feature.

## Контекст 1С
- Конфигурация/подсистема: BSL Flow specification workflow и managed controller; метаданные конфигурации 1С не изменяются.
- Затрагиваемые объекты: `1c-spec-review`, managed `spec_review`, review/reconciliation schemas, project bootstrap config, provider resolution, attempts/evidence, budget ledger, metrics и offline fixtures.
- Клиент/сервер: локальный controller вызывает внешний HTTPS LLM API либо существующий fresh-context host adapter; модели получают только attached snapshot.
- Расширение или основная конфигурация: не применимо.
- Существующие точки расширения/механизмы: `Invoke-1CSpecReview.ps1`, `Task.ManagedReview.ps1`, `Invoke-BFManagedWorker`, `Review.Common.ps1`, `review-schema.json`, `Test-1CSpecFinal.ps1`, `bsl-flow.yaml`, immutable attempts и dependency hashes.
- Существенные ограничения: controller остаётся единственным authority; temporary Unica runtime restriction не затрагивается; council не выполняет код, 1С runtime или публикацию.

Карта механизма: project/request review policy → frozen original/spec/design/evidence snapshot → role/model/provider resolution → direct API либо fresh current-agent attempts → sanitized member results → chair reconciliation и минимальная revision → deterministic final validation → существующий `spec_review` evidence/next stage. Внешние side effects ограничены HTTPS model requests и локальной записью ignored raw evidence/controller-owned sidecars.

## Не делать
- Не включать Qwen Code, DeepSeek Harness или выбор нового agent CLI.
- Не создавать общий `AgentRuntime` со своими `spawn/continue/status` и не переносить stage authority из controller.
- Не удалять OpenCode worker/benchmark integration, если она используется вне specification council.
- Не превращать OpenSpec в orchestrator и не создавать `tasks.md`.
- Не использовать majority vote, один числовой confidence score или самооценку модели как разрешающий gate.
- Не давать council roles инструменты чтения проекта, shell, MCP, web, subagents или запись файлов.
- Не сохранять literal token в version-controlled config, state, receipts, hashes, diagnostics или metrics.
- Не передавать raw project tree, базу 1С, private cache или controller journal внешнему API.
- Не запускать implementation, тесты 1С, native runtime, публикацию или реальные платные smoke-вызовы в рамках подготовки/статической проверки этой спецификации.
- Не рефакторить unrelated worker/runtime adapters и соседние controller stages.

## Критерии приёмки
- GIVEN новый проект с дефолтным конфигом, без внешних tokens и host adapter с подтверждённой capability fresh context для текущих model/effort
  WHEN обязательный M/L/high-risk `spec_review` запускает council
  THEN `intent_critic`, `architecture_critic`, `executability_critic` и `chair` выполняются текущей моделью в отдельных fresh contexts, `brainstorm` не запускается, а report честно имеет `multi_role_single_model` либо `unknown`, но не `multi_model`.
- GIVEN token отсутствует, а host не подтверждает отдельный fresh context, фактические model/effort, no-tools или terminal receipt
  WHEN обязательная роль разрешается через `current_agent`
  THEN council возвращает `BLOCKED` до dispatch, не продолжает inline и не подменяет модель на Luna либо иной host default.
- GIVEN `brainstorm.enabled: true`, `brainstorm.required: true` и доступный model profile
  WHEN council запускается
  THEN brainstorm получает original/evidence без draft spec/design; отсутствие его terminal result блокирует chair и не публикует успешный review.
- GIVEN разные роли с доступными OpenAI и DeepSeek tokens
  WHEN council выполняется
  THEN каждая роль вызывается через свой provider/model/effort и получает независимый input view, а observed provenance и fallback status записаны без секретов.
- GIVEN provider имеет literal token только в `.bsl-flow/providers.local.yaml`
  WHEN config разрешается и выполняется API request
  THEN local token имеет приоритет над `token_env`, используется только в Authorization transport и отсутствует во всех project sidecars, logs, diagnostics, hashes и metrics.
- GIVEN известный provider без `base_url` и custom provider без `base_url`
  WHEN config проходит validation
  THEN известный provider получает packaged standard HTTPS endpoint, а custom provider отклоняется до dispatch с конкретной ошибкой.
- GIVEN endpoint отвечает redirect на другой host или настроен HTTP не-loopback без явного допуска
  WHEN transport готовит/получает request
  THEN вызов блокируется и credential не передаётся новому host.
- GIVEN выбран внешний model profile, но token отсутствует и `fallback: current_agent`
  WHEN роль запускается
  THEN используется модель текущей задачи в fresh context, а result содержит requested model, actual execution mode, observed model при наличии и `credential_missing` без имитации выбранной модели.
- GIVEN token отсутствует и `fallback: block`
  WHEN обязательная роль разрешается
  THEN council возвращает `BLOCKED` до model dispatch и не вызывает другой provider.
- GIVEN одна необязательная role завершилась timeout/provider error
  WHEN все обязательные роли успешны
  THEN failure остаётся в council provenance, chair может продолжить, diversity имеет `degraded`, а ошибочная role не получает сфабрикованный result.
- GIVEN обязательная role завершилась timeout, malformed JSON или unknown dispatch
  WHEN controller классифицирует попытку
  THEN chair не запускается, успешный `review.json` не публикуется и слепого платного retry нет.
- GIVEN critics вернули пересекающиеся либо конфликтующие findings
  WHEN chair выполняет reconciliation
  THEN каждый composite finding identity рассмотрен ровно один раз, объединение сохраняет source identities, а accepted/rejected/partial части имеют evidence и разрешение в финальном тексте.
- GIVEN chair пропускает requirement ID, возвращает неизвестный final reference, оставляет accepted finding без resolution reference либо отвергает finding без evidence
  WHEN final gate проверяет результат
  THEN structural validation fails, старый PASS не сохраняется и implementation остаётся запрещённой; обнаружение семантического перефразирования с потерей смысла остаётся обязанностью criticism/chair и не заявляется как детерминированное доказательство.
- GIVEN material business ambiguity обнаружена любой ролью и не разрешена trusted input
  WHEN chair формирует итог
  THEN задача получает `needs_input` с конкретным вопросом, а council не угадывает правило и не выдаёт финальный PASS.
- GIVEN несколько ролей фактически выполнила одна модель либо observed model неизвестна
  WHEN строится council report
  THEN diversity status не равен `multi_model`, даже если configured model profiles различаются.
- GIVEN завершённые member attempts и неизменные dependency hashes
  WHEN `spec_review` возобновляется после сбоя
  THEN готовые результаты переиспользуются без API/agent calls, незавершённые unknown attempts не повторяются автоматически, а chair запускается только после полной terminal classification.
- GIVEN durable prepared-publication package записан, а процесс прерван между записями `spec.md`, `design.md`, review, reconciliation или final-validation
  WHEN `spec_review` возобновляется
  THEN controller без model/API calls завершает публикацию, если каждый live artifact равен expected draft либо intended final bytes; при любом третьем содержимом возвращает `BLOCKED` и ничего не перезаписывает.
- GIVEN сохранённый completed attempt был привязан к одному endpoint
  WHEN изменился scheme, effective port, normalized base path или версия transport-capability mapping при том же host
  THEN cached result не переиспользуется, а зависимая попытка считается stale до нового разрешённого dispatch.
- GIVEN исторический валидный `review.json` schema v1 с OpenCode provenance
  WHEN его читает обновлённый final validator/status
  THEN artifact остаётся читаемым и не переписывается; новый council output создаётся только как schema v2.
- GIVEN один и тот же frozen input и одинаковые validated member/chair payloads завершились в другом порядке
  WHEN controller собирает итог
  THEN canonical aggregate, hashes и final verdict совпадают.
- GIVEN пользователь пишет draft моделью Sol, назначает Flash critics и Astra brainstorm
  WHEN соответствующие credentials доступны
  THEN author model не подменяет role models, каждый provider получает только разрешённый view, а chair выпускает одну minimally revised spec/design с полной reconciliation.

## Требуемые проверки
- [x] Static — schema v2 council/member/reconciliation, config schema и defaults, запрет неизвестных полей/опасных комбинаций, legacy v1 read compatibility, ссылки/packaging и отсутствие literal secrets в committed fixtures.
- [x] Unit — provider/model/role resolution; token precedence и redaction; full endpoint/capability binding; input views; role required/optional/fallback matrix; trusted host capability/receipt; rejection of model provenance claims; requirement IDs/references; composite finding identities; partial reconciliation; diversity classification; canonical ordering; timeout/error/ambiguous JSON/output-limit handling.
- [x] Integration — fake OpenAI Responses и OpenAI-compatible HTTP servers: mixed providers, current-agent capability stub, bounded parallel fan-out, chair fan-in, budget reservation/outcome, prepared publication with interruption after each file write, cached replay, input/endpoint/capability drift rejection, v1/v2 final validation и отсутствие успешной публикации при required failure.
- [ ] UI — отдельного UI нет.
- [x] Smoke — packaged offline lifecycle без tokens проходит через fresh current-agent fixtures; отдельный explicitly authorized live smoke на синтетической спецификации доказывает один вызов каждого реально включённого API и sanitised receipts, но не является 1С runtime acceptance.
- [x] Independent review — criticism-only review этой spec/design с reconciliation обязателен до implementation; старый OpenCode-only gate не может считаться пройденным через подмену provider.

Runtime 1С, YAxUnit, Vanessa и Computer Use не требуются: feature изменяет только controller/provider review path. Live paid API smoke требует отдельного явного допуска и бюджета после полного offline PASS.

## Неопределённости / допущения
- Точное имя ignored local secrets file фиксируется как `.bsl-flow/providers.local.yaml`; изменение имени потребует обновления spec до implementation.
- `current_agent` означает модель текущей host-задачи в новом контексте. Авторитетны только model/effort и capability, наблюдаемые доверенным host invocation contract/receipt; конкретный adapter обязан доказать fresh context, no-tools и terminal result, иначе fallback блокируется без подстановки другой модели.
- Packaged standard endpoints и поддерживаемые protocol/effort mappings должны быть versioned и проверены отдельными capability fixtures; произвольный model ID не доказывает поддержку effort.
- Явный legacy OpenCode compatibility route может сохраниться вне нового default council, но не должен быть транзитивной зависимостью установки или исполнения API council.
