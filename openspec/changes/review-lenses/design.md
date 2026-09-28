# review-lenses: дизайн

Нормативные требования — в [spec.md](spec.md). Этот документ объясняет устройство и причины решений.

## Принципы

- **Линза — данные, не движок.** Угол проверки описан декларативным файлом. Добавление линзы не меняет раннер, агрегатор и reconciliation.
- **Существующие контракты переиспользуются.** Провайдеры из `review-providers.md`, слои конфигурации Council, `reconciliation-contract.md`, принцип ADR-4 (review только критикует) и fail-closed маршрутизация.
- **Цена растёт с риском.** Лестница S/M/L сохраняется: M получает один проход, L/high — независимых критиков.
- **Core, не managed.** Новый скилл живёт в Core; ADR-11 не нарушается.

## Компоненты

| Компонент | Путь | Задача |
|---|---|---|
| Каталог линз | `global/skills/1c-code-review/lenses/<lens>.md` | Фокус, чек-лист, анти-цели, закрытые категории, триггеры |
| Селектор | `global/skills/1c-code-review/scripts/Select-1CReviewLenses.ps1` | Детерминированный `lens-plan.json`, `-DryRun` |
| Раннер | `global/skills/1c-code-review/scripts/Invoke-1CCodeReview.ps1` | Snapshot, вызов провайдеров, `-ImportRaw`, валидация, публикация |
| Агрегатор | модуль раннера | Слияние findings, `duplicate_of`, вердикт |
| Схемы | `global/skills/1c-code-review/references/lens-review-schema.json`, `code-review-schema.json`, `lens-plan-schema.json` | Контракт ответов и результата |
| Промпт | `global/skills/1c-code-review/reviewer/lens-reviewer-prompt.md` | Универсальный read-only reviewer, линза — секция |
| Reconciliation | `global/skills/1c-spec-review/references/reconciliation-contract.md` | Без изменений семантики |
| Gate | `global/skills/1c-verify/scripts/Test-1CChangeGate.ps1` | Требует reconciled `code-review.json` |

Поток:

```
1c-implement
  → Select-1CReviewLenses        → lens-plan.json
  → Invoke-1CCodeReview          → snapshot в .bsl-flow/reports/code-review/<run-id>/
      → provider × lens (или single_pass) → raw/<lens>.json
      → validate → aggregate     → code-review.json
  → reconcile                    → code-review-reconciliation.json
  → 1c-verify (Test-1CChangeGate проверяет hashes)
```

`1c-verify` шаг 5 заменяется ссылкой на результат `1c-code-review`.

## Формат линзы

```markdown
---
id: security
version: 1
applies_to: [code, spec]
id_prefix: SEC
categories: [privileged_mode, access_rights, dynamic_code, query_injection, external_input, secret_exposure]
triggers:
  flags: [permissions]
  diff_patterns: ['УстановитьПривилегированныйРежим', 'Выполнить\s*\(', 'Вычислить\s*\(', 'HTTPСоединение', 'ПолучитьОбщийМакет']
  metadata_kinds: [HTTPService, WebService, Role]
---

## Фокус
...

## Чек-лист
...

## Анти-цели
Не оценивать производительность, стиль и простоту архитектуры — это другие линзы.
```

Анти-цели и закрытые категории удерживают линзы от пересечения: без них каждая линза начинает повторять общие замечания.

## Каталог v1 и триггеры

| Линза | Префикс | Фокус | Включение |
|---|---|---|---|
| `intent` | `INT` | Потерянные требования, дрейф, scope за пределами спеки | Всегда |
| `simplicity` | `SIM` | Спекулятивные слои, опции, абстракции; можно ли проще. Для спеки переиспользует классификацию `required/justified/optional/unjustified` рубрики | Всегда |
| `reuse` | `REU` | Повторная реализация БСП, общих модулей и объектов проекта | Добавлен общий модуль, объект метаданных, экспортный метод или механизм |
| `security` | `SEC` | Привилегированный режим, права и RLS, `Выполнить`/`Вычислить`, склейка текста запроса, внешний ввод, HTTP/web-сервисы, секреты | Флаг `permissions`, сервисы, внешние данные, паттерны в diff |
| `performance` | `PRF` | Запрос в цикле, разыменование через точку, временные таблицы, индексы, лишние чтения объектов | Текст запроса или обращения к БД внутри цикла в diff |
| `data_integrity` | `DAT` | Управляемые блокировки, транзакции, проведение, движения регистров, миграция данных | Флаги `data_migration`/`data_deletion`, модули проведения, запись регистров |

Соответствие Council на этапе спеки: `intent_critic` ≈ `intent`, `architecture_critic` ≈ `simplicity` + `reuse`, `executability_critic` ≈ testability. Поэтому для L-спеки рядом с Council запускаются только линзы, которых Council не покрывает.

## Маршрутизация

| Изменение | Code review |
|---|---|
| S, low/medium | Нет; при явном запросе или `review.routing.s_default: required` — `single_pass` |
| M, low/medium | `single_pass` |
| L или high | `per_lens` |
| Флаг `permissions` / `data_*` | Риск уже high, значит `per_lens`; соответствующая линза обязательна |

## Схема ответа линзы

```json
{
  "schema_version": 1,
  "lens": "security",
  "lens_version": 1,
  "lens_verdict": "PASS|REVISE|BLOCK",
  "summary": "short evidence-based summary",
  "findings": [
    {
      "id": "SEC-001",
      "severity": "blocker|high|medium|low",
      "category": "privileged_mode",
      "ref": { "file": "src/CommonModules/X/Ext/Module.bsl", "line": 42 },
      "issue": "precise criticism",
      "evidence": "code or task evidence",
      "confidence_kind": "confirmed|hypothesis",
      "suggested_direction": "correction direction, not a rewrite"
    }
  ],
  "checked": ["what was inspected and found correct"],
  "confidence": 0.0
}
```

`single_pass` возвращает `{ "schema_version": 1, "lenses": [ <ответ линзы>, ... ] }`.

Правила валидации: категория из списка своей линзы или `prompt_injection`; ID уникальны, префикс совпадает с `id_prefix`; `PASS` без `checked` невалиден; состав линз `single_pass` равен плану.

## Результат `code-review.json`

Поля: `schema_version`, `change`, `lens_plan` (копия и hash), `inputs` (hashes diff, spec, `original-task.md`, каждой линзы, промпта, эффективной политики), `lenses[]` (`lens`, `version`, `mode`, `provider`, `requested_model`, `observed_model`, `effort`, `isolation: script_owned|host_asserted`, `same_model_as_author`, `lens_verdict`, `attempts`), `findings[]` (с `lens` и `duplicate_of`), `verdict`, `limitations[]`.

Дубли: агрегатор помечает finding как `duplicate_of`, если совпадают файл, пересекаются строки ±3 и совпадает severity-класс. Решение о слиянии по смыслу остаётся reconciliation; агрегатор только группирует.

## Вердикт

| Условие | Вердикт |
|---|---|
| Любой `blocker` или обязательная линза не отработала | `BLOCK` |
| `confirmed` finding `high`/`medium` или любой `lens_verdict: REVISE` | `REVISE` |
| Иначе | `PASS` |

`hypothesis` и `low` уходят в reconciliation, но сами не блокируют. Gate понижает, но не повышает вердикт модели.

## Конфигурация моделей по критикам

```yaml
review:
  code_review:
    enabled: true
    provider: codex_exec
    model: reviewer
    effort: medium
    independence: distinct_from_author
    distinct_lenses: [security, simplicity]
    lenses:
      security:
        required: true
        provider: api
        model: critic-claude
        effort: high
        fallback: block
      simplicity:
        model: reviewer
      reuse:
        enabled: false
```

- Наследование: поле линзы перекрывает `review.code_review.*`.
- Слои: профиль пользователя → проект → `providers.local.yaml`. Профиль задаёт только `model` и `effort` (по умолчанию и по линзам); выбор провайдера, обязательность и включение — решение проекта.
- `fallback`: `block` по умолчанию; `default_model` только для необязательной линзы, с limitation; `current_agent` запрещён.
- Независимость проверяется при admission, до первого платного вызова.
- `-DryRun` показывает для каждой линзы значение и источник (`profile`, `project`, `default`).

Для spec review та же форма под `review.spec_review.lenses`; модели ролей Council остаются в `review.council.roles`.

## Адаптеры хостов

Общее: раннер пишет в run-dir `diff.patch`, список файлов, `lens-plan.json` и тексты линз. Reviewer читает diff из файла, `git` ему не нужен. Промпт один, линза подставляется секцией.

| Провайдер | Кто запускает | Как передаётся модель | Изоляция |
|---|---|---|---|
| `codex_exec` | Раннер, `codex exec` в read-only sandbox, на каждую линзу | `-m <model>` | `script_owned` |
| `opencode` | Раннер, `opencode run` с упакованным read-only агентом | `--model <provider/model>` | `script_owned` |
| `claude_cli` | Раннер, `claude -p` в read-only режиме | `--model <model>` | `script_owned` |
| `api` | Раннер, существующий Council transport | профиль `llm.models` | `script_owned` |
| `claude_subagent` | Сессия Claude Code вызывает `bsl-flow-lens-reviewer` на каждую линзу, затем `-ImportRaw` | параметр `model` вызова субагента, из `lens-plan.json` | `host_asserted` |

Для `claude_subagent` раннер не доказывает изоляцию сам; для L/high это limitation, как сейчас у spec review. Смешанные провайдеры поддерживаются: script-owned линзы исполняются сразу, `claude_subagent` линзы ждут импорта со статусом `pending_import`.

OpenCode: глобальная конфигурация агентов не изменяется (`OPENCODE.delegation.md`). Codex: `agents/openai.yaml` разрешает implicit invocation после `1c-implement`.

## Ошибки

| Ситуация | Поведение |
|---|---|
| Timeout, ошибка провайдера, невалидный JSON в `per_lens` | Одна повторная попытка линзы, затем `BLOCKED`; остальные линзы сохраняются |
| То же в `single_pass` | Одна повторная попытка всего прохода, затем `BLOCKED` |
| `per_lens` требуется, изоляция недоступна | `BLOCKED` с шагом настройки |
| Входы изменились во время прогона | Публикации нет |
| Ошибка конфигурации модели или независимости | Ошибка до первого вызова с именем линзы и ключа |
| Инструкции для reviewer в коде | Finding `prompt_injection` в любой линзе |

## Managed и ADR-11

Первый релиз не меняет managed. Стадия `code_review` в `1c-task` работает как прежде. После снятия заморозки managed может вызывать `Invoke-1CCodeReview.ps1` внутри существующей стадии `code_review`; новый stage-тип и action не нужны. Это отложенное решение, не обязательство.

## Тестирование

- Селектор: таблица «сигнал → линзы», слои конфигурации и источники, limitations при выключении обязательной линзы, `-DryRun`.
- Схемы: чужая категория, неверный префикс ID, пустой `checked` при `PASS`, неполный `single_pass`.
- Агрегатор: таблица вердикта, `duplicate_of`, запрет повышения.
- Раннер: fake-провайдеры, `-ImportRaw`, смешанные провайдеры, retry, `BLOCKED`, дрейф входов, запрет `current_agent`, независимость.
- Gate: отсутствующий и устаревший `code-review.json`.
- Хосты: контракт агента Claude, состав Core-пакета.

## Доказательство пользы

ADR-11 появился из-за подсистем без подтверждённой пользы; линзы не должны стать ещё одной такой. В `bench/` добавляются кейсы с посеянными дефектами:

- `УстановитьПривилегированныйРежим(Истина)` без необходимости;
- склейка пользовательского ввода в текст запроса;
- собственная реализация функции, которая уже есть в БСП;
- запрос внутри цикла;
- лишний слой абстракции или настройка «на будущее»;
- запись регистра без управляемой блокировки.

Метрики: recall по линзам, доля findings, отклонённых при reconciliation, стоимость в вызовах и токенах против одиночного reviewer. Пороги задаются владельцем до реализации этапа 1. Если линзы не проходят порог, функциональность не выпускается.

## Этапы

1. Каталог, селектор, схемы, раннер, агрегатор, `1c-code-review` для `codex_exec` и `api`, gate в `1c-verify`, бенчмарк.
2. Адаптеры `claude_subagent`, `claude_cli`, `opencode`; конфигурация моделей по линзам и профиль пользователя.
3. Линзы для spec review: секции у M-reviewer, `per_lens` рядом с Council для L/high.
4. После снятия ADR-11: вызов из managed-стадии `code_review`.
5. Позже: проектные линзы (`.bsl-flow/lenses/*.md`), project-owned по образцу ADR-10, с отдельным анализом prompt injection.

## Отклонённые варианты

- **Каждая линза всегда отдельным агентом.** Слишком дорого для M, противоречит лестнице S/M/L.
- **Все линзы всегда одним проходом.** Линзы «слипаются», последние проходятся поверхностно; для L/high недостаточно независимости.
- **Расширить Council новыми ролями.** Требует изменения движка Council и затрагивает managed; Council остаётся для спеки, линзы добавляются рядом.
- **Отдельный агент хоста на каждую линзу.** N файлов на три хоста быстро расходятся; линза — данные, агент один.
