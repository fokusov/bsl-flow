# Technical design

## Решение
Расширить существующую стадию `spec_review` единым controller-owned API Council Engine. Engine не является agent harness: он разрешает конфигурацию providers/models/roles, создаёт отдельные immutable attempts, вызывает узкие structured-completion transports либо fresh-context current-agent adapter, затем передаёт validated member results председателю и публикует `review.json` schema v2 только после deterministic checks.

Логический поток:

```text
task state + original-task + draft spec/design + architecture evidence
                              ↓ freeze/hash/bound
             ┌────────────────┼────────────────┐
             │                │                │
      blind brainstorm   intent critic   architecture critic
             │                │                │
             └──────────── executability critic ────────────┐
                                                            ↓
                                              validated member results
                                                            ↓
                                                chair / reconciler
                                                            ↓
                                               deterministic final gate
                                                            ↓
                                       existing spec_review evidence/route
```

Advisors логически независимы: они не видят ответы друг друга, а brainstorm не видит draft. Chair является единственным модельным fan-in. Controller является единственным writer и authority.

## Затрагиваемые компоненты
- Метаданные: 1С metadata не меняются. Нужны versioned JSON Schemas для provider/model/role config, member results и `review.json` v2; final validator читает v1 и v2.
- Модули: provider-neutral extraction из `Invoke-1CSpecReview.ps1`; общий Council Engine для assisted/managed; transport adapters `openai_responses` и `openai_compatible`; current-agent adapter; role resolver; sanitized receipt builder; council aggregator.
- Регистры/движения: не применимо; используются existing task attempts, budget ledger и ignored report directories.
- Формы: не применимо.
- Интеграции: OpenAI/compatible HTTPS APIs и existing host-agent execution. OpenCode/Qwen/DSH не входят в новый path.
- Фоновые/регламентные механизмы: отсутствуют; один council cycle выполняется внутри `spec_review` с bounded concurrency и cancellation.

Минимальные change seams:

1. `Review.Common.ps1`: отделить transport parse от review normalization; убрать hardcoded `provider=opencode`; добавить v2 validation и сохранить v1 reader.
2. `Invoke-1CSpecReview.ps1`: сохранить lint/snapshot/freshness/publication оболочку, заменить обязательный OpenCode launcher на общий Council Engine; legacy launcher оставить только за явным compatibility routing.
3. `Task.ManagedReview.ps1`: вызвать тот же Council Engine; chair становится единственным модельным reconciliation step и заменяет отдельный вызов worker `spec_reconcile`, а controller сохраняет существующие reconciliation sidecar, final-validation contract и route authority, не маркируя direct/current-agent result как OpenCode.
4. `Task.Contracts.ps1`, request/config schemas и bootstrap `bsl-flow.yaml`: добавить council/provider/model/role config и dependency hashes, не смешивая review transports с implementation `execution_profile`.
5. `review-schema.json`, reconciliation contract и `Test-1CSpecFinal.ps1`: schema v2 council provenance, composite findings и partial decision; historical v1 remains accepted according to its original rules.
6. Metrics/budget: одна council cycle identity, отдельные member/chair reservations/outcomes и privacy-minimized aggregate metrics.

## Конфигурационная модель
Переносимый project config содержит только несекретные значения:

```yaml
llm:
  providers:
    openai:
      protocol: openai_responses
      base_url: https://api.openai.com/v1
      token_env: OPENAI_API_KEY
    deepseek:
      protocol: openai_compatible
      base_url: https://api.deepseek.com
      token_env: DEEPSEEK_API_KEY

  models:
    sol:
      provider: openai
      model: gpt-5.6-sol
      effort: medium
    astra:
      provider: openai
      model: gpt-6-astra
      effort: high
    flash:
      provider: deepseek
      model: deepseek-flash
      effort: 100

review:
  council:
    enabled: true
    max_parallel: 2
    roles:
      brainstorm: { enabled: false, required: false, model: astra, fallback: current_agent }
      intent_critic: { enabled: true, required: true, model: flash, fallback: current_agent }
      architecture_critic: { enabled: true, required: true, model: flash, fallback: current_agent }
      executability_critic: { enabled: true, required: true, model: flash, fallback: current_agent }
      chair: { enabled: true, required: true, model: sol, fallback: current_agent }
```

Ignored local overlay:

```yaml
providers:
  openai:
    token: <local secret>
  deepseek:
    base_url: https://approved-compatible-endpoint.example/v1
    token: <local secret>
```

Merge выполняется только по известным provider IDs и разрешённым полям. Local overlay не добавляет role, не меняет routing и не ослабляет security policy. Controller разрешает token непосредственно перед dispatch и очищает reference после построения Authorization header. Наличие token не копируется в canonical project policy; в provenance остаётся только `credential_source=local|environment|missing`.

`base_url` нормализуется до scheme/host/port/base path; userinfo, fragment и query запрещены. Redirect отключён. Known provider defaults находятся в versioned trusted code. Для loopback HTTP требуются loopback address и отдельный local-development flag; имя, резолвящееся во внутреннюю сеть, не считается loopback автоматически.

## Контракты ролей и артефактов
Общий member envelope содержит `schema_version`, `role`, `attempt_id`, `status`, `summary`, `input_hashes`, `requested`, `observed`, `execution_mode`, `fallback_reason`, `payload_sha256`, `usage`, `cost_state` и sanitized timestamps. Весь envelope строит controller из frozen inputs, transport/host observations и terminal receipt. Модель возвращает только закрытый role payload; identity, status, usage, timestamps, hashes и execution mode в него не входят, а такие claims отклоняются schema validation.

Critic findings получают composite identity при aggregation. Canonical ordering: configured role order, затем finding ID ordinal. Completion order не влияет на hash. Optional failures сохраняются как terminal member envelopes без fabricated payload.

`review.json` v2 содержит:

- frozen input hashes и council policy hash;
- sanitized member envelopes;
- diversity status и объясняющие member identities;
- все исходные findings/protected items;
- chair verdict и decisions со ссылками на composite IDs;
- рассчитанные deterministic scores/overengineering metrics, если rubric их использует;
- gate thresholds и итоговый verdict.

Raw HTTP body, raw provider response, stderr/diagnostic payload и exact input snapshots остаются в ignored attempt directory с hash references. Common artifact не содержит полный prompt, token, headers или произвольные tool/provider metadata.

До model dispatch controller строит versioned requirement-binding manifest: material requirement IDs, hashes trusted source fragments и draft references. Chair payload содержит полные final spec/design, decisions и final/resolution references для всех manifest/findings. Controller проверяет множества IDs и ссылки, но не заявляет детерминированной проверки смысла естественного языка: semantic coverage остаётся модельной review обязанностью.

После schema/dependency validation controller формирует immutable prepared-publication package с expected draft hashes, точными final bytes/hashes всех live OpenSpec files, canonical review/reconciliation bytes и final-validation inputs. Durable `prepared` event предшествует первой замене live file. Resume идемпотентно дописывает package без model/API calls, только если каждый live artifact совпадает с expected draft или intended final; третье содержимое блокирует публикацию. `completed` записывается после установки всех intended bytes и успешной final validation. `partially_accepted` требует `accepted_scope` и `rejected_scope`; structural validator проверяет resolution reference принятой части и evidence отклонённой.

## Границы выполнения
- Клиент: assisted command и managed CLI показывают краткие статусы ролей, fallback/diversity и blockers; секреты и raw response не показываются.
- Сервер: внешние LLM endpoints получают только role-specific bounded snapshot. Возможности provider вне structured text completion не используются.
- Транзакции/блокировки: task/controller lock защищает публикацию; member attempts могут выполняться параллельно, но каждый создаётся до dispatch. Aggregate review/reconciliation пишет один controller writer.
- Права: external roles не получают tools. Current-agent adapter до dispatch получает от доверенного host contract capability/identity receipt для fresh context, фактических model/effort, sealed input/no-tools и terminal observation. Неподдерживаемая комбинация блокируется; inline execution и подмена на иной default, включая Luna, запрещены. Council output не меняет authorization revision.
- Производительность: `max_parallel` ограничен schema; input и output имеют byte limits; одинаковый stable prefix допустим для provider cache, но cache hit не предполагается и не является correctness contract.

## Failure, recovery и budget
Attempt state различает `not_dispatched`, `completed`, `failed_before_acceptance`, `unknown_after_dispatch`, `cancelled` и `invalid_response`. Технический retry допускается только для доказанного `not_dispatched|failed_before_acceptance` и создаёт новый attempt. `unknown_after_dispatch` требует provider reconciliation/control evidence либо остаётся blocker; cancel не является rollback стоимости.

Council resume читает terminal attempts и пересчитывает их binding. Fresh result с теми же dependencies переиспользуется. Изменение original/spec/design/evidence, role config, provider/model/effort, любого компонента нормализованного non-secret endpoint (scheme/host/effective port/base path), версии transport-capability mapping, prompt/schema или policy инвалидирует зависимые attempts. Rotation самого secret не раскрывается и не переписывает историю; новый dispatch использует текущее разрешённое credential source.

Budget reservation создаётся на каждый paid API member/chair call до dispatch. Admission учитывает completed, open и unknown outcomes всего council cycle. Current-agent subscription usage сохраняется только при доступной attributable telemetry; неизвестная стоимость остаётся unknown, а не вычисляется из model name.

## Совместимость и данные
Новый project template включает council defaults и не требует OpenCode. Existing `review.reviewer` config не преобразуется молча. Для первого релиза допустим explicit legacy compatibility adapter; migration/readiness command должен показать old config и требуемое изменение до включения council. Historical v1 `review.json`, reconciliation и final-validation остаются immutable и читаются старым schema branch.

Managed request должен связывать resolved council policy, но не копировать token. `execution_profile` продолжает описывать implementation/host tool execution; review API provider profiles являются отдельной configuration boundary. Это предотвращает случайное распространение сетевых credentials на code worker и сохраняет одинаковые gates для assisted/managed review.

## Альтернативы
- Оставить OpenCode как обязательный reviewer runtime — не выбрано: наблюдались зависания, установка и provider configuration затрудняют использование коллегами, а attached-only review не требует полноценного agent CLI.
- Перейти на Qwen Code/DeepSeek Harness — исключено пользователем и добавляет неподтверждённый runtime вместо решения review contract.
- Создать универсальный `AgentRuntime` — не выбрано: дублирует controller lifecycle и расширяет feature до implementation agents, tools и state management.
- Один универсальный reviewer prompt — не выбрано: дешевле, но смешивает intent, architecture и executability и не даёт независимых views разных моделей.
- Голосование/средний confidence — не выбрано: согласованная ошибка моделей не становится доказательством; findings требуют evidence и reconciliation.
- Продолжать текущий диалог при fallback — не выбрано: история создаёт anchoring и не даёт воспроизводимого input snapshot; используется fresh host context.
- Разрешить literal token в committed `bsl-flow.yaml` — не выбрано: высокий риск утечки через Git, diagnostics и task evidence; literal допускается только в ignored local overlay.
- Дать reviewers read/search tools — не выбрано для v1: это требует sandbox/tool policy и возвращает harness complexity. Достаточный evidence должен подготовить inspect/controller.

## Риски
| Риск | Как снижаем |
|---|---|
| Несколько ролей повторяют одну слепую зону модели | Независимые input views, разные configurable models, evidence-based findings, chair reconciliation и deterministic gate; diversity не объявляется по configured names |
| Fallback создаёт видимость multi-model review | Requested/observed provenance, explicit fallback reason и закрытые diversity states |
| Токен попадает в Git или отчёт | Literal только ignored local overlay, env reference, redacted transport, negative secret-leak tests и запрет token hash/raw headers |
| Custom BASE_URL крадёт credential через redirect/SSRF | HTTPS/loopback policy, full normalized endpoint binding, redirect deny, no userinfo/query/fragment, credential sent only after endpoint validation |
| Direct API также зависает или теряет ответ | Connect/overall timeout, cancellation, durable pre-dispatch attempt, bounded drain/output и unknown-after-dispatch без blind retry |
| Chair слепо применяет overengineering reviewer | Полная decisions matrix, evidence для reject/partial, protected items, minimal revision и final invariant gate |
| Parallel completion делает результат невоспроизводимым | Canonical role/finding ordering и hash независимо от completion order; один aggregate writer |
| Сбой оставляет смесь draft/final sidecars | Immutable prepared-publication package, expected/final byte hashes, interruption fixtures и fail-closed resume при третьем содержимом |
| Модель подделывает identity/diversity provenance | Role payload не содержит envelope fields; controller строит provenance только из trusted transport/host receipt |
| Host fallback не умеет requested current model/effort | Pre-dispatch capability receipt и BLOCKED без inline/Luna substitution |
| API меняет alias/effort semantics | Versioned provider capabilities, requested/observed separation, capability fixtures и no claimed identity при unknown observation |
| Review отправляет лишний проектный контекст | Attached-only role views, bounded evidence bundle, allowlisted fields и prompt-injection framing |
| v2 ломает исторические задачи | Dual v1/v2 reader, immutable legacy sidecars и migration blocker вместо silent rewrite |
| Feature разрастается в общий orchestration framework | Ограничение только стадией spec_review; Qwen, tools, implementation agents и новая state machine являются non-goals |

## Стратегия проверки
1. Static/schema: validate project/local config separation, closed enums and combinations, known/custom endpoint rules, schema v2, v1 compatibility, packaging и secret-pattern scan committed fixtures.
2. Unit: deterministic provider/model/role resolution, token source precedence/redaction, full endpoint/capability binding, trusted current-agent capability/identity, rejection model-authored provenance, requirement ID/reference integrity, per-role input projection, role failure matrix, aggregation/composite IDs, partial decisions, diversity и canonical hashes.
3. Transport integration: local fake servers для Responses и OpenAI-compatible contracts покрывают UTF-8, structured output, success, 4xx/5xx, malformed/ambiguous response, timeout, cancellation, output overflow, unknown dispatch и отсутствие успешной publication.
4. Council integration: mixed direct API/current-agent capability stubs, required/optional roles, disabled/mandatory brainstorm, bounded concurrency, chair as sole model reconciler, full reconciliation и final validation.
5. Managed lifecycle: route остаётся `spec_review`, attempts/dependencies/budget/recovery работают без нового state; prepared publication прерывается после каждой file write и безопасно возобновляется, а input/policy/model/endpoint/capability drift и cached replay проверяются offline.
6. Compatibility: historical v1 fixtures проходят read/final-status; old explicit OpenCode config не переинтерпретируется; new template works without OpenCode executable.
7. Security negatives: token/header/raw response не появляются в common artifacts; redirect/custom URL/path escape и unauthorized project-file inclusion rejected before credential transmission.
8. Live smoke выполняется только после offline PASS и отдельного допуска: синтетический original/spec, минимальный budget, один configured external API member и current-agent fallback. Это доказывает transport/provenance, а не качество модели или runtime 1С.
9. Независимое criticism-only review сверяет feature со state/reconciliation/security contracts. Каждый finding получает evidence-backed reconciliation до implementation.
