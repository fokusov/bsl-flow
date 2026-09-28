# План изменений BSL Flow по внешней оценке (2026-09-26)

Статус: черновик для решения владельца. Код не менялся.
Базовая версия: `0.8.0-dev.4`, commit `e0a18d2`.

## 0. Сверка оценки с фактическим кодом

Каждое замечание я проверил по репозиторию. Большинство подтвердилось. Три пункта в оценке неточны, и от этого зависит, как их исправлять.

| # | Утверждение оценки | Факт в репо | Вывод |
|---|---|---|---|
| 1 | ~43k строк, 67 скриптов, 45 сьютов | 132 файла `.ps1`, **30.6k строк** (2.4 MB), около 70 `Test-*.ps1`, **~327 KB** markdown вне `openspec/` | Масштаб подтверждён. Документации даже больше, чем указано |
| 2 | Bootstrap 5.6 KB грузится в каждую сессию Codex | `global/AGENTS.bootstrap.md` = 5653 байт, пишется в `$codexHome\AGENTS.md` (`Install-BSLFlow.ps1:172`) | Подтверждено |
| 3 | «Документация говорит, что OpenCode убран» | Такого в документации нет. На деле OpenCode — **скрытая обязательная зависимость**: M-ревью идёт только через OpenCode (`Invoke-1CSpecReview.ps1:190-228`), установщик падает без `opencode` в PATH (`Install-BSLFlow.ps1:193`), а в `INSTALL.md` в разделе «Требования» OpenCode не указан | Дефект серьёзнее, чем описан: пользователь без OpenCode не может пройти M-маршрут |
| 4 | `1c-task/SKILL.md` называет OpenCode fallback-ревьюером | `1c-task/SKILL.md:18`: «single-reviewer OpenCode spec reviewer remains the fallback». Код это прямо запрещает (`Invoke-1CSpecReview.ps1:166-171`: Council не падает на single reviewer) | Подтверждено: текст противоречит коду |
| 5 | Ссылка на `bsl-flow.ps1` ломается после установки | `1c-task/SKILL.md:24` → `../../../scripts/bsl-flow.ps1`; установщик копирует только `global/skills`. Кроме того, сам `scripts/bsl-flow.ps1:39` ищет контроллер по пути `<repo>/global/skills/...`, поэтому после копирования обёртка всё равно не заработает | Подтверждено, и дефектов два |
| 6 | Имена моделей в шаблоне проекта | `assets/project/bsl-flow.yaml:88-100` (`gpt-5.6-sol`, `gpt-6-astra`, `deepseek-flash`); дополнительно `1c-estimate/SKILL.md:23` (`gpt-6-astra` как «константа скилла») и `Invoke-1CSpecReview.ps1:192` (дефолт `deepseek/deepseek-v4-pro`) | Подтверждено, мест больше, чем в оценке |
| 7 | Council из одной модели | Корневой `bsl-flow.yaml`: все 4 роли → `reviewer` = `deepseek-v4-pro`. В шаблоне три критика сидят на одном `flash` | Подтверждено |
| 8 | Council заблокировал `execution-contract-v01`, но реализацию смержили | `openspec/changes/execution-contract-v01/review-blocked.md`: председатель дважды сгенерировал несуществующий якорь; CHANGELOG фиксирует мерж | Подтверждено. Дополнительный вывод: гейт хрупкий, потому что якоря председатель пишет свободным текстом |
| 9 | `Activate` всегда BLOCKED | `1c-task/SKILL.md:24`: staged `BF_BLOCKED` без записи | Подтверждено |
| 10 | Unica зашита прозой | `1c-init-project/references/test-setup.md:29`, `Task.Toolsets.ps1` | Подтверждено |
| 11 | Нет grounding lint | `Test-1CSpec.ps1` проверяет только структуру разделов, плейсхолдеры и GIVEN/WHEN/THEN | Подтверждено |
| 12 | BSL LS не встроен в гейт | В `global/` и `scripts/` **нет ни одного** упоминания BSL LS в коде. Есть только `verification.static.enabled: true` в YAML | Подтверждено: BSL LS заявлен только в README |
| 13 | CI гоняет OpenCode-сьюты | `offline.yml:34`: `Test-BSLFlowOpenCode*` | Подтверждено. Пока OpenCode — рабочий маршрут M-ревью, это корректно |
| 14 | Только Windows + фиксированный путь pwsh | `task-contract.md:3`, `ARCHITECTURE_RU.md:9` | Подтверждено |

Итог сверки: исправлять OpenCode нужно не удалением хвостов, а выделением **абстракции single-reviewer провайдера**. OpenCode в ней становится одним из опциональных провайдеров.

---

## 1. Целевое состояние

```text
bsl-flow (монорепо, две поставки)
├── core/                        ← bsl-flow-core: ставится за минуту, любая ОС с pwsh 7
│   ├── skills/                  6 скиллов + 1c-estimate (opt-in)
│   ├── openspec/                schema + templates
│   ├── scripts/                 lint, final validation, grounding lint, BSL LS diff,
│   │                            evidence helpers, review-json validator, gate check
│   ├── hosts/
│   │   ├── claude-code/         плагин: .claude-plugin/, skills (ссылка), hooks/, agents/
│   │   ├── codex/               bootstrap ~1 KB, профиль reviewer-а
│   │   └── opencode/            reviewer agents, delegation block
│   └── onec-ops/                порт 1С-операций: schema + адаптеры (native, vrunner,
│                                unica, onec-mcp, внешние скилл-наборы)
├── managed/                     ← bsl-flow-managed: контроллер, council, registry, publication
│   ├── skills/1c-task/
│   ├── council/                 Council.* (переезжает из 1c-spec-review)
│   └── adapters/                codex, claude-code, opencode, current-agent
├── bench/                       доказательная база (набор задач + раннер + отчёт)
├── examples/                    S- и M-задачи end-to-end с артефактами
└── docs/                        quickstart, cookbook, architecture, ADR
```

Матрица поддержки агентов (цель):

| Возможность | Codex | Claude Code | OpenCode | Любой агент с MCP |
|---|---|---|---|---|
| Core-скиллы | ✅ `~/.agents/skills` | ✅ плагин / `~/.claude/skills` | ✅ | — |
| Bootstrap только в 1С-проектах | ✅ ~1 KB + project AGENTS.md | ✅ SessionStart hook (0 байт вне 1С) | ✅ | — |
| Enforcement «нет кода до ревью» | post-hoc check в verify (hooks — P2) | ✅ PreToolUse hook + post-hoc check | post-hoc check | post-hoc check |
| Изолированный M-ревьюер | `codex exec` read-only sandbox | ✅ subagent `bsl-flow-spec-reviewer` (Read/Grep/Glob) | ✅ текущий agent | API-провайдер |
| Council: провайдер моделей | OpenAI / совместимые | ✅ **Anthropic Messages API** (новое) | — | — |
| Managed worker | ✅ (есть) | ✅ новый адаптер `claude -p` (экспериментально) | есть, не верифицирован | режим current-agent |
| Детерминированные проверки как tools | CLI | CLI | CLI | ✅ MCP-сервер `bsl-flow` (P2) |

---

## 2. Фазы и порядок

```text
Ф0 Заморозка и базовая линия ─┐
Ф1 Конкретные дефекты (0.8.1) ┤→ Ф2 Разделение Core/Managed ─→ Ф3 Bootstrap + SKILL.md ─┐
                              │                                                         ├→ Ф6 Бенчмарк ─→ решение о выключении
                              └→ Ф4 Поддержка Claude ─→ Ф5 Порт 1С + grounding + BSL LS ┘
Ф7 Онбординг идёт параллельно с Ф5–Ф6
```

Ориентировочные сроки при работе одного человека с агентом: Ф0+Ф1 — 1 неделя, Ф2+Ф3 — 1.5 недели, Ф4 — 2 недели, Ф5 — 2–3 недели, Ф6 — 2 недели плюс прогоны, Ф7 — 1 неделя.

Версии:
- `0.8.1` — Ф1;
- `0.9.0` — Ф2–Ф4 (Core/Managed, Claude);
- `0.10.0` — Ф5;
- `1.0.0` — только после Ф6, когда польза подтверждена цифрами.

---

## Ф0. Заморозка и базовая линия

**0.1. Заморозка managed.** Записать ADR-11 «Freeze managed surface до бенчмарка».
- Разрешено: исправления дефектов, удаление кода, перенос кода между пакетами.
- Запрещено: новые actions, новые stage-типы, новые опциональные подсистемы.
- Условие снятия заморозки: завершена Ф6 и принято решение по kill-list.

**0.2. Kill-list кандидатов.** Решение по каждому пункту принимается после Ф6. До этого код переносится в `managed/experimental/` и исключается из пакета по умолчанию.

| Кандидат | Размер | Основание |
|---|---|---|
| Experience Ledger (`Task.Memory.ps1` + схемы) | 1186 строк + 3 схемы | выключен по умолчанию, пользы нет в evidence |
| Registry `Activate` | часть `Task.Registry.ps1` (1634) | по дизайну всегда `BF_BLOCKED` |
| Parity harness / frozen traces (след Go-порта) | — | Go-порт откатили |
| Publication (`Task.Publication*.ps1`) | 788 строк | нужен ли без CI/PR-процесса — проверить на пилоте |
| `1c-estimate` | ~530 строк + схема | держать opt-in в Core, если бенчмарк покажет спрос |

- Решение по `Activate`: либо доделать запись ревизии, либо убрать action из CLI, SKILL.md и обёртки. Рекомендация: убрать. Команда, которая всегда отвечает BLOCKED, только сбивает агента.

**0.3. Базовая линия метрик.** Скрипт `scripts/Measure-BSLFlowSurface.ps1` пишет JSON с метриками поверхности:
- строки PS по пакетам;
- байты инструкций, которые грузятся в контекст: bootstrap и каждый SKILL.md;
- число отрицательных конструкций в инструкциях.

CI сравнивает результат с бюджетом (см. Ф3.4) и не даёт поверхности незаметно расти.

**0.4. Governance-дыра с мержем при BLOCKED.**
- Новый CI-чек `scripts/Test-ChangeGovernance.ps1`: для каждого `openspec/changes/*` (кроме `archive/`) с `review-blocked.md` должен существовать `override.md` с полями `owner`, `date`, `reason`, `accepted_risks`. Иначе FAIL.
- Для `execution-contract-v01`: после Ф1.6 (фикс якорей председателя) перезапустить council на разнородных моделях. Если снова BLOCKED, оформить `override.md` явно.

---

## Ф1. Конкретные дефекты → релиз 0.8.1

Каждый пункт: что сломано → правка → как проверить.

**1.1. Скрытая зависимость от OpenCode в M-ревью**
- Сломано: M-маршрут жёстко требует `opencode` (`Invoke-1CSpecReview.ps1:190-228`), установщик падает без него (`Install-BSLFlow.ps1:188-194`), в `INSTALL.md` зависимость не указана.
- Правка, шаг 1 (0.8.1): в `INSTALL.md` честно указать OpenCode как требование M-маршрута. В установщике сделать проверку OpenCode условной: ключ `-SkipOpenCodeReviewer` пропускает `Test-ReviewerConfig`, а M-ревью без OpenCode даёт понятный `BF_BLOCKED` с инструкцией.
- Правка, шаг 2 (0.9.0, Ф4.3): провайдер single-reviewer становится подключаемым (`review.reviewer.provider: claude_subagent | codex_exec | api | opencode`).
- Проверка: новый кейс в `Test-BSLFlowPackage.ps1` — установка с `-SkipOpenCodeReviewer` в изолированный каталог проходит. `Invoke-1CSpecReview` для M без OpenCode возвращает BLOCKED с текстом, где названы способы настройки.

**1.2. Противоречивые тексты про OpenCode-fallback**
- `1c-task/SKILL.md:18`: удалить фразу «remains the fallback». Заменить описанием: M → настроенный single reviewer, L/high → Council, без понижения маршрута.
- `AGENTS.bootstrap.md:31`: «when OpenCode or the configured model is unavailable» → «when the configured reviewer is unavailable».
- `global/OPENCODE.delegation.md:4`: «Preserve the existing OpenCode specification reviewer» оставить, но только в OpenCode-хосте. Этот файл ставится лишь `Install-BSLFlowForOpenCode.ps1`, так что правка не нужна. Нужна только сверка формулировки.
- Проверка: новый тест `Test-InstructionConsistency.ps1`. Его запрещённые фразы: `OpenCode.*fallback`, `OpenCode.*unavailable` в core-инструкциях.

**1.3. Обёртка `bsl-flow.ps1`**
- Перенести в `global/skills/1c-task/scripts/bsl-flow.ps1`. Контроллер искать через `Join-Path $PSScriptRoot 'Invoke-BSLFlowTask.ps1'`.
- В `scripts/bsl-flow.ps1` оставить однострочный прокси для запуска из клона.
- Ссылку в SKILL.md сделать относительной внутри скилла: `scripts/bsl-flow.ps1`.
- Проверка: `Test-BSLFlowPackage` устанавливает пакет в temp и вызывает `bsl-flow.ps1 task list --project <fixture>` из **установленного** пути.
- Общий тест `Test-SkillLinks.ps1`: у каждой относительной ссылки в SKILL.md цель находится внутри установленного набора скиллов. Он ловит весь класс ошибок, а не только этот случай.

**1.4. Имена моделей вне проектного шаблона**
- `assets/project/bsl-flow.yaml`: секцию `llm.models` с конкретными именами удалить. Роли ссылаются на **символьные профили** `review-fast`, `review-strong`, `review-chair`.
- Привязка профиль → конкретная модель живёт только в `~/.bsl-flow/config.yaml`. Механизм приоритета уже есть (`Council.Profile.ps1`).
- Если профиль не привязан, admission-гейт отвечает `BF_BLOCKED: model profile 'review-strong' is not bound; add llm.models.review-strong to ~/.bsl-flow/config.yaml` и прикладывает пример.
- Добавить `scripts/New-BSLFlowUserConfig.ps1` — интерактивный генератор профиля с 2–3 шаблонами: OpenAI+DeepSeek, Anthropic+DeepSeek, только Anthropic.
- `1c-estimate/SKILL.md:23`: убрать константу `gpt-6-astra`. `ai_basis.model` = модель текущей сессии, если хост её сообщает, иначе `estimate.ai_basis.model` из пользовательского профиля, иначе `unknown`. Валидатор `Test-1CEstimate.ps1` принимает `unknown`.
- `Invoke-1CSpecReview.ps1:192`: убрать дефолт `deepseek/deepseek-v4-pro`. Если модели нет, выдавать явную ошибку конфигурации.
- Проверка: `Test-ProjectUpgrade.ps1` + новый кейс. Upgrade старого проекта **не удаляет** его существующие `llm.models`, потому что пользовательские значения сохраняются, а только печатает рекомендацию перенести их в профиль.

**1.5. Council из одной модели**
- Новый ключ `review.council.independence: distinct_models | distinct_providers | any`, по умолчанию `distinct_models` для критиков относительно председателя и хотя бы двух различных моделей среди критиков.
- Admission проверяет правило до платного вызова.
- При `any` в `review.json` пишется `limitations: ["single_model_council"]`, и вердикт не может быть чистым `PASS`: максимум `PASS_WITH_LIMITATIONS`.
- Собственный `bsl-flow.yaml` репо перевести на две и больше модели разных провайдеров. После Ф4.4 это DeepSeek + Anthropic.
- Проверка: кейсы в `Test-CouncilRouting.ps1` / `Test-CouncilProfile.ps1`.

**1.6. Хрупкие якоря председателя** (причина BLOCKED в execution-contract-v01)
- Lint спецификации (`Test-1CSpec.ps1`) уже разбирает разделы. Добавить в `spec-lint.json` массив `anchors[]`: `{id: "REQ-3", section: "Требуемое поведение", item: 3, text_sha256}`, по одному на каждый пункт требований, критериев и non-goals.
- Схема ответа председателя (`council-review-schema.json`): поле ссылки становится `enum`, который формируется из `anchors[].id` на лету. Так модель физически не может сослаться на несуществующий якорь при structured output. Где structured output недоступен, валидатор отвечает понятной ошибкой вместо BLOCKED всей задачи.
- Проверка: регрессионный кейс на фикстуре с якорем `Форматы файлов / Evidence/T-NNN.json` из `review-blocked.md`.

**1.7. Хвосты в CI**
- CI-шаги OpenCode остаются, пока OpenCode — поддерживаемый провайдер. После Ф2 они переезжают в отдельный job `hosts-opencode`, который не блокирует Core.

Критерий готовности 0.8.1: все новые тесты зелёные на `windows-2025`, `CHANGELOG` дополнен, `INSTALL.md` соответствует фактическим требованиям.

---

## Ф2. Разделение Core / Managed

**2.1. ADR-12 «Две поставки из одного репо».**
- Монорепо проще, чем два репозитория: общие схемы, один CHANGELOG, одна CI.
- `Build-BSLFlowPackage.ps1` получает `-Package core|managed` и собирает два ZIP с отдельными manifest.
- Managed объявляет зависимость от точной версии Core (`requires_core: 0.9.x`).

**2.2. Граница пакетов**

| В Core | В Managed |
|---|---|
| Скиллы `1c-init-project`, `1c-spec`, `1c-spec-review` (lint + reconciliation + final + single reviewer), `1c-implement`, `1c-verify`, `1c-debug`, `1c-estimate` (opt-in) | `1c-task`, controller `Task.*`, registry, publication, native FILE adapter (до выноса в onec-ops) |
| `Test-1CSpec.ps1`, `Test-1CSpecFinal.ps1`, reconciliation contract, review-schema v1 validator | Council engine (`Council.*.ps1`, `Invoke-CouncilReview.ps1`) и review-schema v2 |
| Evidence helpers `1c-verify/scripts/*` | Worker adapters |
| Grounding lint, BSL LS diff (Ф5) | Managed review / coverage / gates |
| Host-интеграции (Claude plugin, Codex bootstrap, OpenCode) | |

- Как быть с L/high-risk в Core без Council: правило «L не понижается» сохраняется. `1c-spec-review` в Core для L отвечает `BLOCKED: L/high-risk requires Council (install bsl-flow-managed) or an owner override recorded in review-reconciliation.json`. Override — явная запись с именем и причиной, и итоговый verify выдаёт не выше `PASS_WITH_LIMITATIONS`.
- Для L допустим альтернативный путь в Core: **два** независимых single-reviewer прогона на разных провайдерах. Это «мини-совет» без председателя: reconciler закрывает оба набора замечаний. Решение за владельцем (см. §5, вопрос 3).

**2.3. Кроссплатформенный Core**
- Аудит `core/scripts` на Windows-специфику: `C:\`, `\\`-пути в `Join-Path`, `Program Files`, `LocalApplicationData`, `.cmd`, реестр, `Get-Acl`. Заменить на `[IO.Path]`, `$HOME`, `$IsWindows`-ветки.
- Путь схемы OpenSpec брать через `openspec schema which`, а не через `%LOCALAPPDATA%`.
- Решение 2026-09-16 «не поддерживать Linux/macOS» уточнить в CHANGELOG: оно остаётся в силе для **Managed**. Core поддерживается на Windows, Linux и macOS с pwsh 7 из PATH.
- Проверка: CI-матрица `core` на `windows-2025`, `ubuntu-latest`, `macos-latest`.

**2.4. Установщики**
- `Install-BSLFlowCore.ps1 -Host codex|claude|opencode|agents [-WhatIf]`:
  - копирует скиллы в каталог хоста (`~/.agents/skills`, `~/.claude/skills` или путь плагина);
  - копирует OpenSpec schema;
  - пишет минимальный bootstrap (Ф3) только для хостов, где он нужен;
  - проверяет только Git и OpenSpec; не требует OpenCode, Codex, API-ключей или .NET SDK.
- Для Claude Code основной путь — **плагин из marketplace** (Ф4.1), установщик нужен как офлайн-альтернатива.
- `Install-BSLFlowManaged.ps1` = текущий `Install-BSLFlow.ps1` минус Core-часть плюс проверка версии Core.
- Бюджет: установка Core ≤ 60 секунд на чистой машине, где уже есть pwsh, git и openspec. Замер — в CI.

**2.5. Миграция существующих установок**
- `Install-BSLFlowCore.ps1` распознаёт старую установку 0.8.x по маркеру bootstrap и составу скиллов, делает backup (механизм уже есть) и заменяет большой bootstrap-блок малым.
- Документ `docs/MIGRATION_0.9_RU.md`.

---

## Ф3. Bootstrap и переписывание SKILL.md

**3.1. Bootstrap ≤ 1 KB** (Codex / общий `AGENTS.md`). Черновик, около 750 байт:

```markdown
<!-- bsl-flow bootstrap:start -->
## BSL Flow (1C projects only)
Apply only when the project is a 1C:Enterprise project: it has `Configuration.xml`,
`*.mdo`, `*.bsl`, `src/cf|cfe|epf`, or the user says so. Ignore otherwise.
1. If `bsl-flow.yaml` or `openspec/config.yaml` (schema: bsl-flow) is missing, use `1c-init-project`.
2. Size the task S/M/L and risk. S: `1c-implement` → `1c-verify`.
   M/L or high risk: `1c-spec` → `1c-spec-review` → `1c-implement` → `1c-verify`.
3. Bugs: `1c-debug`. Report results as PASS / PASS_WITH_LIMITATIONS / FAIL / BLOCKED
   with the evidence you actually observed.
<!-- bsl-flow bootstrap:end -->
```

Остальное содержимое нынешнего bootstrap раскладывается так:

| Текущий абзац bootstrap | Куда |
|---|---|
| Managed/`1c-task` | `1c-task/SKILL.md` |
| Критерии и идемпотентность bootstrap | `1c-init-project/SKILL.md` (уже есть, дублирование удалить) |
| Тестовая политика, computer-use | `1c-verify/references/testing-policy.md` (уже есть) |
| Upgrade проекта, workstation profile | `1c-init-project` |
| Delegation audit | `1c-init-project/references/agent-audit.md` |
| EPF/ERF external_artifact | `1c-verify/references/external-artifacts.md` (уже есть) |

- Проектный `assets/project/AGENTS.md` (33 строки) тоже сжать: 10–12 строк позитивных правил и ссылка на скиллы.

**3.2. Шаблон SKILL.md** (единый для всех 7 скиллов):

```markdown
---
name: 1c-xxx
description: <когда применять — одна фраза с триггерами>
---
# 1c-xxx
## When to use            — 2–4 пункта
## Inputs                 — какие файлы читать
## Steps                  — нумерованная позитивная процедура, у каждого шага наблюдаемый результат
## Outputs                — какие файлы/вердикт появляются
## Checks                 — какие скрипты запустить и что считается успехом
## Stop and ask when      — только условия, которые НЕ enforce-ит скрипт
## Managed mode           — одна строка: «Inside a 1c-task stage, follow the stage contract instead.»
```

**3.3. Правила переписывания**
- Каждое отрицание («never / does not prove / remains BLOCKED»):
  - если его enforce-ит скрипт, удалить из текста и оставить шаг «run X; it blocks on Y»;
  - если не enforce-ит, переформулировать в позитивное действие («record the observed counts») или оставить в `Stop and ask when`.
- Managed-абзацы в начале каждого скилла (`1c-spec:8`, `1c-implement:8`, `1c-verify:8`, `1c-debug:8`, `1c-spec-review:8`) заменить одной строкой из шаблона. Детали уйти в `1c-task/references/stage-contract.md`.
- `1c-implement` «Execution graph (v0.1)» вынести в `references/execution-graph.md`. Опциональная фича не должна занимать треть основного скилла.
- `1c-task/SKILL.md` абзац «Repository task registry» (сейчас одна строка на ~1.3 KB) вынести в `references/registry.md`.

**3.4. Бюджеты как тесты** (`Test-InstructionBudget.ps1` в CI Core):
- bootstrap ≤ 1024 байт;
- каждый SKILL.md ≤ 4 KB, без учёта references;
- в SKILL.md не больше 5 вхождений `never|does not|must not|remains BLOCKED`;
- в `description` frontmatter есть триггер применения.

Проверка качества переписывания — не только бюджеты. Перед мержем Ф3 прогнать 5 задач из Ф6 на старой и новой редакции и сравнить соблюдение маршрута. Это ранняя мини-версия бенчмарка.

---

## Ф4. Поддержка Claude

### 4.1. Плагин Claude Code

Структура в `core/hosts/claude-code/`:

```text
.claude-plugin/
  plugin.json            name: bsl-flow, version, description
  marketplace.json       (в корне репо) — marketplace «bsl-flow»
skills/                  → те же 7 Core-скиллов (сборка копирует, не дублирует в git)
agents/
  bsl-flow-spec-reviewer.md   read-only subagent (tools: Read, Grep, Glob)
  bsl-flow-code-reviewer.md   read-only subagent для L code review
hooks/
  hooks.json
  SessionStart.ps1
  PreToolUse-EditGate.ps1
  PreToolUse-EvidenceGuard.ps1
```

Установка пользователем:

```text
/plugin marketplace add <github-owner>/bsl-flow
/plugin install bsl-flow@bsl-flow
```

- Скиллы в плагине получают namespace `bsl-flow:1c-spec` и т.п. Проверить, что ссылки внутри SKILL.md не зависят от имени каталога. Тест `Test-SkillLinks.ps1` из Ф1.3 покрывает и это.
- Офлайн-альтернатива: `Install-BSLFlowCore.ps1 -Host claude` копирует скиллы в `~/.claude/skills`, агентов в `~/.claude/agents`, а hooks — в `~/.claude/settings.json`, merge с backup, через JSON без regex.

### 4.2. Hooks: enforcement без контроллера

`hooks/hooks.json`:

```json
{
  "hooks": {
    "SessionStart": [{ "hooks": [{ "type": "command",
      "command": "pwsh -NoProfile -File \"${CLAUDE_PLUGIN_ROOT}/hooks/SessionStart.ps1\"" }] }],
    "PreToolUse": [
      { "matcher": "Edit|Write|MultiEdit|NotebookEdit",
        "hooks": [
          { "type": "command", "command": "pwsh -NoProfile -File \"${CLAUDE_PLUGIN_ROOT}/hooks/PreToolUse-EvidenceGuard.ps1\"" },
          { "type": "command", "command": "pwsh -NoProfile -File \"${CLAUDE_PLUGIN_ROOT}/hooks/PreToolUse-EditGate.ps1\"" }
        ] }
    ]
  }
}
```

**SessionStart.ps1** — вместо глобального bootstrap:
- Если `cwd` не 1С-проект (детектор из 3.1, не глубже 2 уровней, с таймаутом), выход без вывода. Вне 1С в контекст попадает 0 байт. Это закрывает замечание «грузится в каждую сессию».
- Если 1С-проект, печатает 5–8 строк: маршрут S/M/L, активный change и его статус гейтов, либо «проект не инициализирован → 1c-init-project».

**Понятие «активный change».**
- `1c-spec` пишет `.bsl-flow/active-change.json`: `{change, complexity, risk, spec_sha256}` через скрипт `Set-1CActiveChange.ps1`. Руками файл не пишется.
- `1c-verify` после финального вердикта снимает активный change.

**PreToolUse-EditGate.ps1** читает stdin JSON (`tool_input.file_path`):
1. Путь не входит в `source.paths` из `bsl-flow.yaml` → allow.
2. Активного change нет → allow (S-маршрут).
3. Активный change M/L/high-risk, и `final-validation.json` отсутствует, `passed=false` или хеш спеки в нём ≠ текущему `spec.md` → **deny**. Причина уходит модели: «Спека X не прошла final validation: запусти 1c-spec-review». Выход кодом 2 или `permissionDecision: "deny"`.
4. Аварийный выключатель: `BSL_FLOW_GATES=off` или `.bsl-flow/local/gates.yaml`. Сам hook при этом пишет запись в `.bsl-flow/reports/gate-overrides.jsonl`, и verify показывает её как limitation.

**PreToolUse-EvidenceGuard.ps1** — deny для Edit/Write по путям `openspec/changes/*/{spec-lint,review,review-reconciliation,final-validation}.json` и `.bsl-flow/evidence/**`. Эти файлы пишут только скрипты. Исключение: `review-reconciliation.json` пишет агент по контракту, но его схему проверяет `Test-1CSpecFinal.ps1`.

**Честная граница** (записать в ADR-13):
- Hook не перехватывает запись через Bash (`Set-Content`, `echo >`). Поэтому hook поднимает нижнюю планку, а не даёт sandbox.
- Гарантию закрывает **post-hoc проверка** в `1c-verify`: новый скрипт `Test-1CChangeGate.ps1`. Если в diff есть изменения в `source.paths`, а у активного M/L change нет прошедшего final validation, который старше первого изменения исходников (сравнение с mtime/commit), то вердикт — `FAIL: process violation`.
- Проверка работает в любом агенте, поэтому она часть Core, а не плагина.

**Тесты hooks** (`Test-ClaudeHooks.ps1`): hooks вызываются с фикстурными stdin-JSON и проверяются exit code и stdout. Claude Code для этого не нужен. Отдельный ручной пилот в реальном Claude Code — в чек-листе релиза.

### 4.3. Абстракция single-reviewer и Claude как M-ревьюер

- Интерфейс `Invoke-BSLFlowSingleReview -Provider <p> -Bundle <input> -> raw JSON`. Общая часть не меняется: snapshot входов, валидация по `review-schema.json`, пересчёт метрик, атомарная запись `review.json`. Выносится из `Invoke-1CSpecReview.ps1:190-487` в `Review.Providers.ps1`.

| Провайдер | Запуск | Изоляция |
|---|---|---|
| `claude_subagent` | в assisted: текущая сессия Claude Code вызывает subagent `bsl-flow-spec-reviewer`, тот пишет сырой JSON в `.bsl-flow/reports/spec-review/<run>/raw.json`, после чего `Invoke-1CSpecReview.ps1 -ImportRaw` валидирует и публикует | tools: Read, Grep, Glob; отдельный контекст |
| `claude_cli` | `claude -p --output-format json --model <m> --allowedTools "Read,Grep,Glob" --disallowedTools "Edit,Write,Bash,WebFetch,WebSearch,Task" --strict-mcp-config` с prompt из stdin, cwd = проект | процесс, только чтение |
| `codex_exec` | `codex exec --sandbox read-only --output-schema review-schema.json` (переиспользует код `Codex.ps1`) | OS-sandbox |
| `api` | один вызов через `Council.Transport.ps1` (одна роль `reviewer`) | нет доступа к ФС, только приложенные файлы (`attached_only`) |
| `opencode` | текущий код | текущая |

- Точный набор флагов CLI проверяется **capability probe** (`claude --version`, `claude --help`) по той же политике, что у Codex: неподтверждённая версия или флаг → `BLOCKED`, а не догадка. Список поддерживаемых версий хранится в `hosts/claude-code/capability.json`.
- Независимость: subagent по умолчанию наследует модель основной сессии. В `agents/bsl-flow-spec-reviewer.md` указать `model:` явно (настраивается). В `review.json` пишется `reviewer_model` и флаг `same_model_as_author`; при `true` вердикт получает limitation.
- Дефолт `review.reviewer.provider` выбирается в `1c-init-project` по обнаруженному хосту: Claude Code → `claude_subagent`, Codex → `codex_exec`, иначе `api`.

### 4.4. Anthropic как провайдер Council

- В `Council.Transport.ps1` добавить `protocol: anthropic_messages`:
  - `POST {base_url}/v1/messages`, заголовки `x-api-key` из `token_env` (по умолчанию `ANTHROPIC_API_KEY`) и `anthropic-version`;
  - structured output через принудительный tool use: `tools=[{name:"submit_review", input_schema: council-member-schema}]`, `tool_choice={type:"tool", name:"submit_review"}`;
  - usage из `usage.input_tokens/output_tokens` в budget ledger.
- Пример профиля в `New-BSLFlowUserConfig.ps1`: критики на DeepSeek и OpenAI, председатель на Claude (или наоборот). Имена моделей только в профиле пользователя.
- Проверка: `Test-CouncilTransport.ps1` + фикстурный HTTP-ответ Anthropic (локальный mock, по образцу существующих). Отдельный кейс — ответ без tool_use: ошибка схемы, не BLOCKED всей задачи без объяснения.

### 4.5. Интерфейс worker-адаптера (Managed)

- Вынести из `Task.Execution.ps1:24,510,587` ветвление `codex/opencode` в реестр адаптеров. Каждый `managed/adapters/<name>/adapter.json` декларирует:

```json
{
  "name": "claude-code",
  "contract": "worker-adapter/v1",
  "isolation": "permission_rules",
  "stages": ["spec", "spec_review", "code_review", "coverage_review", "implement"],
  "observed_identity": true,
  "entry": "ClaudeCode.ps1"
}
```

- Функции контракта: `Test-<X>Capability`, `Invoke-<X>Worker` → `worker-result.schema.json` плюс host metadata `{session_id, requested_model, observed_model, usage, isolation}`.
- Controller policy связывает stage и минимальный уровень изоляции: `implement` требует `os_sandbox` или `permission_rules+integrity_check`, read-only stages принимают `permission_rules`.

### 4.6. Адаптер Claude Code для managed worker

- Запуск: `claude -p --output-format stream-json --verbose --model <m> --allowedTools <per-stage> --disallowedTools "Bash,WebFetch,WebSearch,Task" --strict-mcp-config --mcp-config <empty.json> --setting-sources <none>`. Prompt идёт в stdin, cwd = worktree.
- Инструменты по стадиям:
  - read-only стадии: `Read,Grep,Glob`;
  - `implement`: `Read,Grep,Glob,Edit,Write`.
  - Bash не даётся ни одной стадии: тесты и сборку запускает controller. Поэтому изоляция держится на правилах разрешений без OS sandbox.
- Разбор событий:
  - `system/init` → `session_id` и **наблюдаемая модель**. Это лучше, чем у Codex, где observed identity приходится доставать из rollout;
  - `result` → `usage`, `total_cost_usd`, итоговый текст;
  - JSON результата валидируется по `worker-result.schema.json`.
- Integrity check: до и после запуска хешируется controller state (`.bsl-flow/tasks/<id>`) и всё вне worktree, что controller считает защищённым. Любое изменение даёт `BF_BLOCKED: controller state modified by worker`. Полный manifest worktree (ADR-3) уже ловит запись вне подсказанных путей.
- Статус: `experimental` до пилота S+M, по образцу пилотов Codex в CHANGELOG 0.7.
- Тесты: `Test-ClaudeCodeAdapter.ps1` с фикстурными stream-json (`scripts/fixtures/claude/*.jsonl`) по образцу `fixtures/opencode`: нормальный прогон, отсутствие `result`, два `init`, `is_error: true`, превышение output.

### 4.7. Режим «текущий агент» (assisted-managed)

- Новые actions контроллера:
  - `-Action Next -Format Prompt` выдаёт stage prompt для текущей сессии;
  - `-Action Submit -TaskId -Stage -ResultFile` принимает результат, прошедший ту же схему и те же gates.
- Receipt фиксирует `isolation: current_agent`. Acceptance scope ограничен, и это видно в выводе.
- Так Claude Code, Cursor и другие агенты получают managed-журнал, recovery и evidence gates без headless-адаптера.
- Действует заморозка Ф0. Этот пункт — единственное исключение: новые actions добавляются, потому что оценка прямо просит сделать контроллер переносимым. Исключение фиксируется в ADR-11.

### 4.8. Документация для Claude

- `docs/hosts/CLAUDE_CODE_RU.md`: установка плагина, что делают hooks, настройка модели ревьюера, ограничения (Bash-обход и post-hoc check).
- В README вынести матрицу поддержки из §1.

---

## Ф5. Порт 1С-операций, grounding lint, BSL LS

### 5.1. Порт `onec-ops`

Схема `core/onec-ops/schemas/`:
- `provider.schema.json` — манифест адаптера;
- `op-request.schema.json`, `op-result.schema.json` — единый конверт результата: `{capability, status: PASS|FAIL|BLOCKED, mutating, target, evidence:[{path, sha256}], raw_output_sha256, provider, provider_version}`.

Возможности:

| capability | mutating | Назначение |
|---|---|---|
| `metadata.inspect` | нет | индекс объектов, реквизитов, модулей (для grounding) |
| `syntax.check` | нет | синтаксический контроль модулей |
| `static.bslls` | нет | диагностики BSL LS (5.3) |
| `build.cf` / `build.cfe` / `build.epf` | нет (локальный файл) | сборка из исходников |
| `extension.list` | нет | фактический состав расширений базы |
| `extension.load` | **да** | загрузка расширения |
| `config.update` | **да** | обновление конфигурации БД |
| `test.yaxunit` | да (тестовые данные) | прогон YAxUnit с исходным JUnit |
| `test.vanessa` | да | прогон feature |

Манифест адаптера:

```json
{
  "name": "vrunner",
  "contract": "onec-ops/v1",
  "capabilities": {
    "syntax.check":   { "entry": "Invoke-VrunnerSyntax.ps1" },
    "build.cfe":      { "entry": "Invoke-VrunnerBuild.ps1" },
    "extension.load": { "entry": "Invoke-VrunnerLoad.ps1", "requires_authorization": true },
    "test.yaxunit":   { "entry": "Invoke-VrunnerYaxunit.ps1" }
  },
  "detect": "Test-VrunnerAvailable.ps1"
}
```

Адаптеры по порядку:

| # | Адаптер | Что делаем | Какие возможности закрывает |
|---|---|---|---|
| 1 | `native-1cv8` | **выносим** из текущего managed native FILE adapter — рефакторинг, не переписывание | `extension.list`, `extension.load`, `config.update`, `test.yaxunit`, `build.*` через `1cv8 DESIGNER` |
| 2 | `unica` | прозу `test-setup.md:29` превращаем в код | `extension.load` (`dryRun` + apply), `test.*`. Важно: `operation=extensions` — мутирующая операция, поэтому как `extension.list` **не** декларируется |
| 3 | `vrunner` | новый адаптер для vanessa-runner | — |
| 4 | `onec-mcp` | вызов MCP-инструментов через CLI-мост или через текущего агента в assisted | — |
| 5 | `skillset` | маппинг на внешние наборы скиллов 1С (`cf-*`, `cfe-*`, `epf-*`, `db-*` и т.п.): адаптер указывает, какой скилл или скрипт выполняет capability; обобщение текущего `Task.Toolsets.ps1` | — |
| 6 | `edt-cli` | `1cedtcli` | `build.*`, `metadata.inspect` для EDT-проектов |

- Конфигурация в `bsl-flow.yaml`: `onec.providers: [native-1cv8, vrunner]` с приоритетом per capability, плюс `onec.overrides: { test.vanessa: vrunner }`.
- Нет адаптера для нужной capability → `BLOCKED: capability X has no configured provider`. Сейчас эта логика размазана прозой по скиллам.
- Скиллы `1c-verify` и `1c-init-project` вызывают `Invoke-OneCOp.ps1 -Capability test.yaxunit ...`, а не описывают Unica текстом.

Тесты:
- контрактный тест, который гоняется для каждого адаптера: манифест, схема результата, отказ мутирующей операции без authorization;
- фейковый адаптер `fake` в фикстурах для офлайн-CI.

### 5.2. Grounding lint по метаданным

Скрипт `core/scripts/Test-1CSpecGrounding.ps1 -ChangePath <dir> [-SourceRoot <src>]`.

**Индекс метаданных** (`Get-1CMetadataIndex.ps1`, кеш `.bsl-flow/cache/metadata-index.json` по хешу списка файлов и mtime):
- Выгрузка конфигуратора: `Configuration.xml` (состав), `<Тип>/<Имя>.xml` (реквизиты, ТЧ, реквизиты ТЧ, измерения и ресурсы регистров, значения перечислений), `Ext/*.bsl` и `Forms/*/Ext/Form/Module.bsl` (процедуры и функции с флагом `Экспорт`).
- EDT: `src/<Тип>/<Имя>/<Имя>.mdo` плюс `.bsl`.
- Расширения (`cfe`): объекты с `ObjectBelonging=Adopted` помечаются как «заимствованные», свои — как «добавленные расширением».
- Производительность: потоковый `XmlReader` только по верхним узлам; на типовой ERP (10k+ объектов) цель ≤ 30 секунд холодного и ≤ 2 секунд тёплого прогона. Замер на публичной демо-конфигурации включается в тест.

**Извлечение ссылок из `spec.md`/`design.md`:**
- Типы RU/EN: `Справочник|Catalog`, `Документ|Document`, `РегистрНакопления|AccumulationRegister`, `РегистрСведений|InformationRegister`, `РегистрБухгалтерии`, `РегистрРасчета`, `ОбщийМодуль|CommonModule`, `Перечисление|Enum`, `ПланВидовХарактеристик`, `ПланСчетов`, `Обработка|DataProcessor`, `Отчет|Report`, `Константа|Constant`, `РегламентноеЗадание`, `HTTPСервис`, `ОбщаяФорма`, `Роль`, `Подсистема`. Также формы множественного числа из кода (`Справочники.X`, `Документы.X`).
- Формы ссылок:
  - `Тип.Имя`;
  - `Тип.Имя.Реквизит`;
  - `Тип.Имя.ТабличнаяЧасть.ТЧ.Реквизит` и сокращённое `Тип.Имя.ТЧ.Реквизит`;
  - `ОбщийМодуль.Имя.Метод()`;
  - `Модуль менеджера/объекта Тип.Имя: Метод`.
- Внутри блоков кода и обратных кавычек ссылки тоже извлекаются.

**Правила:**
- Объект, реквизит или метод не найден:
  - **error**, если ссылка стоит в разделах «Контекст 1С», «Требуемое поведение», «Критерии приёмки»;
  - **warning** в «Неопределённостях».
- Намеренно новые объекты объявляются в новом необязательном разделе шаблона `## Новые объекты метаданных` (или пометкой `(новый)`) и проверяются на **отсутствие** коллизии с существующими.
- Вызов неэкспортного метода общего модуля извне → error.
- Опечатка: подсказка ближайшего имени (Левенштейн ≤ 2 или нормализация регистра и ё/е).
- Исходников нет → `grounding: unavailable` warning, не error. Для S это нормально.
- Результат пишется в `spec-lint.json.grounding` и учитывается `Test-1CSpecFinal.ps1`.

**Grounding по diff** (в `1c-verify`): тот же индекс плюс разбор изменённых `.bsl` на обращения `Справочники.X`, `Документы.X.СоздатьДокумент()`, `ОбщийМодуль.Метод(` и `Метаданные.X.Y`. Несуществующее имя — FAIL на уровне static.

Тесты:
- фикстуры `scripts/fixtures/metadata/designer-mini/` и `edt-mini/` (5–10 объектов, расширение с заимствованием);
- кейсы: верная ссылка, опечатка, неэкспортный метод, объявленный новый объект, коллизия нового объекта, отсутствие исходников, EN-синтаксис.

### 5.3. Static-гейт по diff BSL LS

Скрипт `core/scripts/Invoke-1CStaticDiff.ps1` (capability `static.bslls`):
1. Найти BSL LS: `onec.bslls.jar` / `bsl-language-server` в PATH / `~/.bsl-flow/workstation.json`. Не найден: `BLOCKED`, если `verification.static.required: true`, иначе limitation.
2. Baseline: отчёт для merge-base, по умолчанию `HEAD` до изменений. Кешируется в `.bsl-flow/cache/bslls/<commit>.json`. Анализ идёт только по изменённым файлам (`--srcDir` на временную копию набора файлов) — это важно для скорости.
3. Current: тот же набор файлов из рабочего дерева.
4. Сопоставление диагностик по ключу `(file, diagnosticCode, normalized message, sha256 строки ± 1 соседней)`, а не по номеру строки: сдвиг строк не даёт ложных «новых».
5. Новые `Error`/`Blocker`/`Critical` → FAIL. Новые остальные → в отчёт. Legacy-диагностики не расширяют scope (правило из `1c-verify` сохраняется).
6. Конфиг BSL LS проекта (`.bsl-language-server.json`) используется, если есть.

Тесты: фикстурные JSON-отчёты BSL LS (baseline/current) без запуска Java. Запуск Java — отдельный opt-in тест в CI с кешированным jar.

---

## Ф6. Доказательная база

**6.1. Формат задачи** `bench/tasks/<id>/task.yaml`:

```yaml
id: print-form-invoice-qr
size: S            # S | M | L
risk: low
fixture: demo-bp-mini@<commit>          # публичная конфигурация-фикстура
request: original-task.md               # формулировка как от заказчика
expected_scope: [src/Documents/РеализацияТоваровУслуг/**]
acceptance:                             # скрытые от агента проверки
  - kind: yaxunit
    tests: [bench/tasks/.../tests/*.bsl]
  - kind: grounding
  - kind: bslls_new_errors_max
    value: 0
```

**6.2. Набор.** 20–30 задач:
- печатные формы;
- реквизит + форма;
- движения / проведение;
- обмен / HTTP-сервис;
- регламентное задание;
- EPF-отчёт;
- 2–3 заведомо неоднозначные задачи, где правильное поведение — задать вопрос.

Публичная часть идёт на открытых конфигурациях/фикстурах. Приватная часть — на клиентских проектах: запускается локально, в репо публикуются **только агрегаты**.

**6.3. Раннер** `bench/Invoke-BSLFlowBench.ps1 -Agent claude|codex -Mode bare|core|managed -Tasks <glob> -Repeat 3`:
- каждый прогон в чистом worktree фикстуры;
- агент headless (`claude -p` / `codex exec`), в `bare` без скиллов и bootstrap;
- после прогона — скрытые acceptance тем же контроллерным кодом, что и в продукте.

**6.4. Метрики** (`bench/results/<date>.json`):
- доля пройденных acceptance;
- scope drift: строки diff вне `expected_scope` и новые объекты метаданных вне спеки;
- время wall-clock и токены/стоимость (из usage хостов, без оценок);
- число ложных PASS: агент заявил PASS, скрытые проверки FAIL. **Главная метрика для 1С**;
- число правок человеком (для приватного набора, вручную);
- для council: доля BLOCK, которую reconciler отклонил как ложную, и доля реальных дефектов, найденных только council-ом.

**6.5. Правила решения фиксируются до прогона** (pre-registration в `bench/DECISION_RULES.md`):
- компонент остаётся, если снижает ложные PASS или scope drift минимум на 20% относительно режима без него при росте времени не больше чем на 50%;
- council для M не включается, пока не найдёт дефекты, которые пропускает single reviewer, минимум в 15% M-задач;
- kill-list из Ф0.2 закрывается по этим данным.

**6.6. Публикация.** `docs/BENCHMARK_RU.md` с методикой, агрегатами и ограничениями. Ссылка из README.

---

## Ф7. Онбординг

**7.1. `examples/`**
- `s-print-form/`: `original-task.md`, итоговый diff, `verification` (что запускалось), сокращённый транскрипт агента (Claude Code), вердикт.
- `m-attribute-and-form/`: полный набор артефактов: `spec.md`, `spec-lint.json`, `review.json`, `review-reconciliation.json`, `final-validation.json`, diff, `verification.md`, транскрипт.
- Примеры проверяются в CI: lint и final validation на артефактах примера должны проходить, иначе пример устарел.

**7.2. Рецептник** `docs/COOKBOOK_RU.md` — по 1 странице на рецепт: печатная форма, реквизит+форма, движения по регистру, изменение проведения, HTTP-сервис, регламентное задание, EPF-отчёт, обмен. Для каждого рецепта:
- типичный класс S/M/L и риск;
- скелет «Требуемого поведения»;
- обязательные проверки (unit / integration / Vanessa / build);
- частые ошибки агентов;
- какие grounding-ошибки обычно ловятся.

**7.3. README.** Порядок разделов:
1. что это, в двух фразах;
2. quickstart за 5 минут для Claude Code (плагин) и Codex;
3. маршрут S/M/L с картинкой;
4. пример;
5. только потом Managed.

Термины (sentinel, receipt, provenance, managed host) — в `docs/GLOSSARY_RU.md`, в README не раньше раздела Managed.

---

## 3. Сквозные изменения в тестах и CI

| Workflow | ОС | Что гоняет | Блокирует релиз Core |
|---|---|---|---|
| `core.yml` | win / ubuntu / macos | lint, final, grounding, BSL LS diff (фикстуры), hooks, install Core, instruction budget, skill links, governance, примеры | да |
| `managed.yml` | windows-2025 | текущие managed + council сьюты | да для Managed |
| `hosts-opencode.yml` | windows-2025 | OpenCode сьюты | нет |
| `hosts-claude.yml` | win / ubuntu | Claude hooks, Claude adapter на фикстурах, Anthropic transport mock | да |
| `bench.yml` | manual | публичный бенчмарк (платные вызовы, secrets) | нет |

Новые тестовые сьюты:
- `Test-InstructionConsistency`
- `Test-InstructionBudget`
- `Test-SkillLinks`
- `Test-ChangeGovernance`
- `Test-ClaudeHooks`
- `Test-ClaudeCodeAdapter`
- `Test-SingleReviewProviders`
- `Test-1CSpecGrounding`
- `Test-1CMetadataIndex`
- `Test-1CStaticDiff`
- `Test-OneCOpsContract`
- `Test-1CChangeGate`

---

## 4. Риски

| Риск | Митигация |
|---|---|
| Разделение пакетов сломает существующие установки 0.8 | Миграционный путь с backup (механизм уже есть), `-WhatIf`, документ миграции, тест апгрейда 0.8.0-dev.4 → 0.9 |
| Флаги `claude` CLI меняются между версиями | Capability probe и список проверенных версий, неизвестная версия → BLOCKED (политика как у Codex) |
| Hooks дают ложное чувство гарантии | ADR-13 с честной границей, post-hoc `Test-1CChangeGate` в verify для всех агентов |
| Grounding lint даёт ложные ошибки на нестандартных ссылках | Error только в нормативных разделах; раздел «Новые объекты»; порог и режим `warn` для первых релизов; бенчмарк меряет долю ложных срабатываний |
| Переписывание SKILL.md ухудшит соблюдение процесса | Мини-бенчмарк из 5 задач до и после (Ф3.4) |
| Заморозка managed тормозит пилоты native | Фиксы разрешены; расширение native — через `onec-ops`, это Core-работа |
| Работа Ф4–Ф5 сама раздувает проект | Бюджеты Ф0.3/Ф3.4 в CI; kill-list по данным Ф6; всё новое в managed — только через ADR-исключение |

---

## 5. Решения, нужные от владельца до старта

1. **Монорепо с двумя поставками** (рекомендация) или два репозитория.
2. **Core на Linux/macOS.** Рекомендация: да, для Core. Managed остаётся Windows-only, как решено 2026-09-16.
3. **L/high-risk в Core без Council:** BLOCKED + owner override (рекомендация), или разрешить «два single-reviewer на разных провайдерах».
4. **Дефолтный M-ревьюер в Core:** нативный для хоста (`claude_subagent` / `codex_exec`, рекомендация) или API.
5. **`Activate`:** удалить (рекомендация) или доделать.
6. **Kill-list Ф0.2:** согласовать перенос в `managed/experimental/` до бенчмарка.
7. **Приватный набор бенчмарка:** какие клиентские проекты можно использовать локально и что разрешено публиковать в агрегатах.

---

## 6. Первые шаги (после одобрения)

1. Ф0.1–0.4: ADR-11, `Measure-BSLFlowSurface.ps1`, `Test-ChangeGovernance.ps1`, override или перезапуск для `execution-contract-v01`.
2. Ф1.3 + `Test-SkillLinks` (самый дешёвый видимый баг).
3. Ф1.1 шаг 1 + Ф1.2 → релиз 0.8.1.
4. Ф1.4–1.6, затем старт Ф2 и Ф4.1–4.2 параллельно: плагин можно собрать поверх текущих скиллов ещё до разделения каталогов.
