# BSL Flow

[English](README.en.md)

BSL Flow помогает AI-агентам выполнять доработки 1С/BSL: выбрать достаточный маршрут, проверить спецификацию и собрать доказательства результата. Core даёт отдельные skills и проверки; Managed добавляет контроллер этапов для явно запрошенных задач.

## Быстрый старт

Нужны PowerShell 7, Git, Node.js 20.19+ и OpenSpec CLI. Рабочая версия — **0.9.0-dev.1**; это development-версия. Исходники содержат **8 skills**: шесть основных, опциональный `1c-estimate` и Managed-скилл `1c-task`.

Из распакованного Core-пакета или клона репозитория сначала посмотри план установки:

```powershell
# Codex: общий каталог ~/.agents/skills
./scripts/Install-BSLFlowCore.ps1 -Host codex -WhatIf
./scripts/Install-BSLFlowCore.ps1 -Host codex

# Claude Code: skills, read-only reviewers и hooks
./scripts/Install-BSLFlowCore.ps1 -Host claude -WhatIf
./scripts/Install-BSLFlowCore.ps1 -Host claude
```

В Windows общий каталог — `%USERPROFILE%\.agents\skills`. Для OpenCode используй `-Host opencode`, для общего набора skills — `-Host agents`. Установка сохраняет резервную копию изменяемых файлов; переход с 0.8 описан в [миграции](docs/MIGRATION_0.9_RU.md).

Claude Code также может открыть локальный плагин из корня клона; установка и проверка описаны в [руководстве хоста](docs/hosts/CLAUDE_CODE_RU.md). До публикации версии локальная копия точнее отражает development-код, чем marketplace.

В проекте начни с запроса: «Инициализируй этот проект 1С через `1c-init-project`, сохрани мои настройки». Затем опиши доработку и ожидаемый результат. [Полная установка](INSTALL.md) описывает зависимости и провайдеров ревью; конкретные модели задаются в пользовательском профиле, а не в шаблоне проекта.

## Маршрут S / M / L

```text
S, низкий риск:  inspect -> implement -> verify
M:              inspect -> spec -> single review -> reconcile -> implement -> verify
L / высокий:    inspect -> spec + design -> Council -> reconcile
                        -> implement -> independent code review -> verify
```

Для S отдельная спецификация нужна по запросу или политике проекта. M получает одного независимого reviewer, по умолчанию нативного для выбранного хоста. L/high требует Council из Managed либо явно записанный override владельца с принятыми рисками; отсутствие обязательного провайдера означает `BLOCKED`.

Проверки выбираются по поведению: unit для логики, integration для данных и проведения, Vanessa для пользовательского сценария, визуальная проверка для внешнего вида. Grounding проверяет ссылки на реальные метаданные, BSL LS diff — новые статические ошибки. Они не заменяют runtime-проверку.

## Примеры

- [S: печатная форма](examples/s-print-form/) — запрос, diff и границы проверки.
- [M: реквизит и форма](examples/m-attribute-and-form/) — спецификация, review/reconciliation и проверяемые sidecar-файлы.
- [Рецептник](docs/COOKBOOK_RU.md) — восемь типовых задач с критериями и тестами.

Примеры демонстрационные: синтетическое ревью и статическая валидация не доказывают выполнение сценария в базе 1С. [Бенчмарк](docs/BENCHMARK_RU.md) отдельно различает fake-проверку раннера и измерение поведения настоящих агентов.

## Managed

Managed остаётся Windows-only. Он запускается только по явному запросу через `1c-task`; установка и bootstrap **Managed не включают**. Контроллер хранит состояние, связывает результаты с исходниками и авторизацией, управляет восстановлением и принимает задачу по доказательствам. Подробности: [руководство](docs/FRAMEWORK_GUIDE_RU.md), [архитектура](docs/ARCHITECTURE_RU.md), [контракт CLI](global/skills/1c-task/references/task-contract.md), [словарь](docs/GLOSSARY_RU.md).

Managed устанавливается поверх Core совместимой версии через `Install-BSLFlowManaged.ps1`; `Install-BSLFlow.ps1` сохраняет полную установку. `Next -Format Prompt` / `Submit` позволяют работать текущему агенту с одноразовым dispatch ID; такой receipt явно отмечает `isolation: current_agent`. Это другой уровень изоляции, чем отдельный worker.

| Возможность | Codex | Claude Code | OpenCode | Агент с CLI/MCP |
| --- | --- | --- | --- | --- |
| Core skills | Общий каталог | Плагин / локальные skills | Общий каталог | Через доступный CLI |
| Проверка порядка review → код | Post-hoc | Hooks + post-hoc | Post-hoc | Post-hoc |
| M reviewer | `codex_exec` | `claude_subagent` / `claude_cli` | `opencode` | API provider |
| Managed worker | Sandbox capability gate | Экспериментальный adapter, CLI probe | Adapter, нужен host pilot | Current-agent |
| Runtime 1С | Только подтверждённый и авторизованный маршрут | То же | То же | То же |

Core рассчитан на Windows/Linux/macOS с pwsh 7; CI-матрица проверяет переносимость. Наличие workflow не является доказательством выполненного CI-прогона. Реальные host-пилоты и runtime-гейты учитываются отдельно от офлайн-контрактов.

Реестр задач, оценка, execution contracts, публикация и Experience Ledger включаются отдельно. Ledger по умолчанию выключен (`features.self_learning_memory.enabled: false`); он не меняет обязательные журналы и гейты. Приёмка не разрешает push, merge, deploy или новые операции с базой.

## Тестовое окружение 1С

BSL Flow инвентаризирует явно выбранные локальные каталоги YAxUnit и Vanessa, не скачивает их автоматически. Найденный файл не доказывает установку расширения: неизвестное состояние сохраняется как unknown. Отсутствующий provider получает `not_configured`; требующий его критерий остаётся `BLOCKED`. Настройка и отдельные разрешения описаны в [руководстве окружения](docs/TEST_ENVIRONMENT_GUIDE_RU.md).

`onec-ops` задаёт общий контракт операций и авторизации. Его mock-тесты не подтверждают реальные флаги платформы, состояние базы или безопасный повтор load. Временное ограничение Unica runtime сохраняется.

## Разработка и поставка

`scripts/Build-BSLFlowPackage.ps1 -Package full|core|managed` собирает отдельные архивы; `-Test` проверяет собранный артефакт. Core и Managed имеют отдельные CI-наборы, OpenCode — host lane, бенчмарк запускается вручную. [CHANGELOG](CHANGELOG.md) хранит изменения и ограничения.

BSL Flow — независимый проект, не связанный с фирмой «1С»; названия продуктов указывают совместимость. Распространяется по [лицензии MIT](LICENSE).
