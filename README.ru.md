# BSL Flow

**Лёгкий инженерный процесс и контроллер задач для разработки на BSL.**

BSL Flow — публичный workflow-проект для AI coding agents, работающих с BSL и проектами 1С:Предприятие. Его цель — оставить минимальный инженерный процесс, который реально снижает риск ошибок, но не превращать каждую доработку в тяжёлый SDD-процесс.

Базовая схема:

```text
задача
  -> минимально достаточная спецификация
  -> независимое ревью спецификации
  -> реализация
  -> проверка и тесты
  -> накопление знаний проекта
```

## Зачем нужен BSL Flow

AI coding agents хорошо помогают в разработке на BSL, но часто:

- переусложняют спецификации;
- добавляют лишние архитектурные сущности;
- расширяют scope задачи;
- создают implementation plans, которые сложнее самой задачи;
- делают вывод о корректности кода без фактической проверки.

BSL Flow строится на нескольких принципах:

- **Minimal Sufficient Design** вместо архитектуры «на будущее».
- **Risk-based workflow** вместо обязательного процесса для каждой задачи.
- **Независимое review спецификаций** для поиска scope drift, assumptions и overengineering.
- **Evidence-driven verification** вместо «код выглядит корректно».
- **BSL/1С-специфика**: объекты метаданных, управляемые формы, клиент/сервер, регистры, проведение документов, интеграции и тестовые базы.
- **Два режима работы**: существующие skills помогают в assisted-режиме; в managed-режиме `1c-task` передаёт последовательность этапов и проверку доказательств детерминированному контроллеру.

## Маршруты по размеру и риску

```text
S / low-risk
inspect -> implement -> verify

M
inspect -> spec -> independent review -> targeted revision -> implement -> verify

L / high-risk
inspect -> spec/design -> independent review -> targeted revision
        -> implement -> independent code review -> verify
```

Фреймворк рассчитан на совместную работу с:

- OpenAI Codex и другими coding agents;
- OpenSpec для lightweight specification artifacts;
- OpenCode и независимой reviewer-моделью;
- BSL Language Server для статического анализа;
- YAxUnit для unit/integration тестов;
- Vanessa Automation / TestClient для UI и end-to-end сценариев.

## Актуальная версия

Рабочая версия — **BSL Flow 0.8.0-dev.2**. В ней семь skills, включая новый `1c-task`: контроллер сохраняет историю задачи, выбирает обязательные этапы, запускает изолированных Codex workers, проверяет актуальность исходников и доказательств и останавливается при неопределённом результате. Шесть assisted-skills и отдельный OpenCode reviewer спецификаций сохранены.

Версия включает managed native FILE-адаптер расширения, независимую проверку достаточности тестов и восстановление очереди по durable журналу. Реальный native-пилот завершён с оригинальным JUnit 5/5 PASS; это не подтверждает произвольные бизнес/UI-сценарии. Текущая приёмка публикации и итогового выпуска отражена в [плане завершения](docs/SDLC_COMPLETION_RU.md).

Новая версия добавляет Go CLI `bsl-flow.exe` со встроенными инструкциями и движком, ограниченный цикл исправления проверяемых ошибок, локальную очередь зарегистрированных задач и передачу принятых исходников. Тестовые входы при автоматическом исправлении защищены от изменения. Бинарник вызывает тот же авторитетный PowerShell 7 controller через стандартную машинную установку `C:\Program Files\PowerShell\7\pwsh.exe`; fallback на Windows PowerShell 5.1 не поддерживается. Подробности и оставшиеся runtime-gates описаны в [плане 0.8](docs/PLAN_0.8_RU.md) и [руководстве по установке](INSTALL.md).

Практическая польза: тебе не нужно напоминать агенту о следующем этапе зарегистрированной задачи. Изменение кода после теста отменяет применимость старого PASS; потерянный ответ после записи не становится поводом повторить действие. Короткая S/low задача сохраняет короткий маршрут.

Это development-версия с проверенным managed native FILE-адаптером расширения. Публичный controller довёл BSLFlowPilot до приёмки; этот узкий маршрут не означает готовность полного автономного 1С SDLC.

Начни с [руководства по фреймворку](docs/FRAMEWORK_GUIDE_RU.md). В [описании архитектуры](docs/ARCHITECTURE_RU.md) разобраны выбранные решения и компромиссы, а в [контракте CLI](global/skills/1c-task/references/task-contract.md) — входные JSON, команды и восстановление.

Offline-проверки пакета исторически прошли в PowerShell 5.1 и 7; для версии 0.8.0-dev.2 поддерживается только PowerShell 7. Реальный Codex-пилот подтвердил исправление ошибки и приёмку исходников. Для native-пилота выдано точное разрешение, временное ограничение durable Unica jobs сохраняется. Фактические результаты и оставшиеся проверки приведены в [VERIFICATION.md](VERIFICATION.md).

## Установка

Требования и полный порядок описаны в [INSTALL.md](INSTALL.md). На Windows сначала посмотри план, затем выполни установку:

```powershell
Set-ExecutionPolicy -Scope Process Bypass
.\scripts\Install-BSLFlow.ps1 -WhatIf
.\scripts\Install-BSLFlow.ps1
```

Для самостоятельной работы из OpenCode:

```powershell
.\scripts\Install-BSLFlowForOpenCode.ps1
.\scripts\Install-BSLFlowForOpenCode.ps1 -Apply
.\scripts\Test-BSLFlowOpenCode.ps1
```

OpenCode-установщик не изменяет `opencode.json`, model routing, providers и credentials.

## Как находятся YAxUnit и Vanessa

BSL Flow не сканирует диски рекурсивно и не скачивает тестовые инструменты молча. Однократный workstation profile хранит точные общие каталоги для всех явно зарегистрированных файловых баз разработки. Если пути не указаны, используются `C:\YAxUnit` и `C:\vanessa-automation`. Другое расположение нужно зарегистрировать явно:

```powershell
& "$env:USERPROFILE\.agents\skills\1c-init-project\scripts\Enable-BSLFlowWorkstationProfile.ps1" `
  -DevelopmentDatabasePath "C:\BASES\DEMO\bp1" `
  -PlatformBin "C:\Program Files\1cv8\8.3.27.2074\bin" `
  -YaxunitDirectory "D:\1c-tools\YAxUnit" `
  -VanessaDirectory "D:\1c-tools\vanessa-automation"
```

Инвентаризация проверяет только верхний уровень этих каталогов по точным маскам: `YAxUnit*.cfe`, `vanessa-automation*.epf`, `VAExtension*.cfe` и `client_mcp.cfe`. Отсутствующий каталог или файл получает состояние `not_configured`; несколько кандидатов или пустой файл — `blocked`. Найденный файл означает только `files_found`: установку в базе, совместимость и готовность ещё нужно подтвердить инвентаризацией базы и минимальным runtime-пилотом. Подробности — в [руководстве по тестовому окружению](TEST_ENVIRONMENT_GUIDE_RU.md).

На машине без этих инструментов BSL Flow всё равно устанавливается, но проверки, которым нужен отсутствующий provider, остаются `BLOCKED`. Framework не скачивает сторонние релизы молча. Vanessa Automation — внешний EPF-runner; YAxUnit и необязательный `VAExtension` имеют отдельные требования к установке в базу.

## Структура репозитория

- `global/skills/` — устанавливаемые agent skills;
- `global/openspec/` — OpenSpec schema и шаблоны;
- `scripts/` — установщики и offline regression checks;
- [OPENCODE_SETUP_RU.md](OPENCODE_SETUP_RU.md) — настройка standalone OpenCode;
- [TEST_ENVIRONMENT_GUIDE_RU.md](TEST_ENVIRONMENT_GUIDE_RU.md) — постоянное окружение YAxUnit/Vanessa;
- [VERIFICATION.md](VERIFICATION.md) — подтверждённые и заблокированные границы проверки.

## Для кого

BSL Flow ориентирован на разработчиков и команды, которые используют AI-assisted development для BSL / 1С:Предприятие, особенно при работе с большим количеством клиентских проектов, расширениями, интеграциями и автоматизированным тестированием.

## Ключевые слова

BSL, 1С:Предприятие, разработка 1С, AI coding agents, Codex, OpenCode, OpenSpec, spec-driven development, SDD, AI-assisted development, review спецификаций, YAxUnit, Vanessa Automation.

## Товарные знаки

BSL Flow — независимый проект и не связан с фирмой «1С». Обозначения 1С и 1С:Предприятие упоминаются только для описания совместимости и целевой экосистемы разработки.

## Лицензия

Проект распространяется по [лицензии MIT](LICENSE).
