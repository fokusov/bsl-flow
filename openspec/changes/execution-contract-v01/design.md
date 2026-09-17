# Technical design — execution-contract-v01

## Решение

Три слоя с разделением ответственности (из исходного предложения «Agent Native SDD», согласовано в планировании):

- человек — Markdown: `spec.md` остаётся центральным артефактом и единственным входом ревью;
- агентский контракт — YAML рядом со спекой: `contract.yaml` (ЧТО + инварианты), `execution.yaml` (КАК + DAG), `verification.yaml` (доказательства);
- runtime — генерируемые JSON: коммитируемый `evidence/T-NNN.json` и локальный `state.json`.

Поток: спека → ревью (без изменений) → контракт + граф (агент, до реализации) → скилл-исполнение (1c-implement) → evidence. Компилятор spec→execution и controller-принуждение done_when — будущие отдельные спеки (этапы 2–3 плана), в v0.1 не делаются.

Ключевые контракты:

1. Разрешение ссылок: `R.spec_ref` → якорь «Требуемое поведение / N» (существование проверяет линт; hash-привязка — у будущего компилятора); `T.satisfies` → только R; `T.verify` → только V; `V.requirement` → только R. Имена T/R/V уникальны в каталоге изменения.
2. Парсер YAML — в духе существующего allowlist-парсера council policy (`Get-BSLFlowCouncilPolicy` / Council.Common.ps1), без YAML-библиотеки: явный список ключей, отклонение неизвестных, fail-closed с именем файла и ключа. Линт полностью детерминированный, без LLM.
3. Исполнение v0.1 — скилл-уровень: топологический порядок, kind → разрешения (`explore|research|review|document` — read-only; `implement|migrate|fix|test` — мутации только в `allowed_scope` с учётом `forbidden`), правило BLOCKED по отсутствию V-evidence. Контроллер (global/skills/1c-task) не меняется.
4. `state.json` — восстанавливаемая проекция из `evidence/`, не источник истины, в Git не попадает; источник истины по фактам исполнения — `evidence/` + git diff.

## Черновик схем (schema_version: 1; заморожено при реализации линта)

```yaml
# contract.yaml
schema_version: 1
requirements:
  - id: R-001
    spec_ref: 2           # пункт N раздела «Требуемое поведение» spec.md
    constraints:          # необязательная плоская map скаляров, allowlist
      configuration_changes: forbidden

# execution.yaml
schema_version: 1
tasks:
  - id: T-001
    kind: explore
    goal: "…"
    depends_on: []
    satisfies: [R-001]
    verify: []
    allowed_scope: []
    forbidden: []
    mutation: forbidden

# verification.yaml
schema_version: 1
checks:
  - id: V-001
    requirement: R-001
    type: scenario
    expect: { conducted: false }
```

## Затрагиваемые компоненты

- Метаданные 1С: не затрагиваются.
- PowerShell-скрипты: global/skills/1c-spec-review/scripts/Invoke-1CSpecContractLint.ps1 (новый линт артефактов, рядом с Test-1CSpec.ps1 и его `-ChangePath`-контрактом).
- Скиллы: 1c-spec (шаблоны для M/L), 1c-implement (дисциплина исполнения, deterministic-хелперы scripts/ExecutionGraph.ps1, evidence/state).
- Схемы: global/openspec/schemas/bsl-flow — только если schema требует декларации; артефакты опциональны.
- Совет: не затрагивается (промпты, рубрики, JSON-схемы без изменений).

## Границы выполнения

- Клиент/сервер: неприменимо (PS-скрипты + скиллы).
- Транзакции/блокировки: нет; конкурентных писателей артефактов v0.1 не поддерживает.
- Права: kind-разрешения дисциплины исполнителя (read-only виды задач).
- Производительность: линт — линейный проход по небольшим файлам изменения.

## Совместимость и данные

- Артефакты опциональны: S-изменения и все существующие каталоги изменений не затронуты; `spec.md`-линт не меняется.
- `state.json` игнорируется Git (локальный игнор); `evidence/` — обычные коммиты.
- Миграций нет; при появлении компилятора его hash-привязка станет строгой заменой `spec_ref`-проверки (обратная совместимость сохраняется).

## Альтернативы

- Сразу делать DAG-исполнитель в контроллере — отклонено: runtime-гейты BLOCKED, скилл-уровень даёт результат сейчас и не блокируется платформой.
- Компилировать граф автоматически из спеки в v0.1 — отклонено: без ревью-цикла компилятор закрепляет ошибки спеки; ручной черновик + линт дешевле и безопаснее.
- Расширить JSON-схемы совета, чтобы он ревьюил контракт — отклонено: заморозка промптов/схем, совет проверяет спеку, а не план.

## Риски

| Риск | Как снижаем |
|---|---|
| Дрейф контракта от спеки при ручном редактировании | Линт `spec_ref`; полное решение (source_hash) — этап компилятора |
| Скилл-дисциплина не принуждается кодом (нарушения scope не ловит линт) | Принятое ограничение v0.1: нарушения фиксируются в evidence как violation, задача не может быть done; controller enforcement — следующая спека |
| Ползучесть формата артефактов | `schema_version: 1`, allowlist-парсер, заморозка имён полей до отдельного решения |

## Стратегия проверки

- Unit: линт (циклы, дубликаты, висячие ссылки, glob, обязательные поля V), восстановимость `state.json`.
- Integration: `Invoke-1CSpecContractLint.ps1` end-to-end на fixture-каталогах; сквозной проход 1c-implement по fixture-графу с правилом BLOCKED (без 1С runtime).
- Independent review: обязательна (L) по проектной политике.

## Переанкеровка 2026-09-16 (PowerShell)

Go-модули из исходной версии (`cli/internal/specvalidate`, `cli/speccmd.go`) удалены откатом `native-cross-platform-cli`; линт переанкерован на `Invoke-1CSpecContractLint.ps1` (PowerShell), исполнитель — 1c-implement + `ExecutionGraph.ps1`. Черновик схем выше приведён к замороженному при реализации формату (`checks:` вместо `verifications:`, целочисленный `spec_ref`, per-requirement `constraints`).
