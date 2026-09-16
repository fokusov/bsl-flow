# Использование BSL Flow непосредственно в OpenCode

Текущий standalone-адаптер BSL Flow использует standalone-адаптер BSL Flow. Он устанавливает skills в общий `%USERPROFILE%\.agents\skills`, доступный Codex и OpenCode, и добавляет два помеченных managed-блока в `%USERPROFILE%\.config\opencode\AGENTS.md`. Копии BSL Flow skills в `%USERPROFILE%\.config\opencode\skills` недопустимы: они имеют более высокий приоритет и могут незаметно затенить общую версию.

Адаптер принципиально не меняет `opencode.json`, providers, credentials, primary agent, subagents и модели. Запуск без `-Apply` печатает детерминированный план. Перед заменой ранее зарегистрированных managed skills и `AGENTS.md` создаётся backup; ошибка проверки запускает rollback. Чужой или изменённый skill с тем же именем блокирует обновление, остальные общие skills сохраняются. Если остались старые BSL Flow-копии в OpenCode-specific каталоге, сначала удали их после проверки/backup либо выполни основной установщик: адаптер не будет молча выбирать между затеняющими версиями.

## Установка

```powershell
Set-ExecutionPolicy -Scope Process Bypass
.\scripts\Install-BSLFlowForOpenCode.ps1 -WhatIf
.\scripts\Install-BSLFlowForOpenCode.ps1
.\scripts\Install-BSLFlowForOpenCode.ps1 -Apply
```

После установки перезапусти OpenCode. Отдельная диагностика не вызывает модель и не запускает 1С:

```powershell
.\scripts\Test-BSLFlowOpenCode.ps1
```

Для нестандартного XDG-каталога передай полный путь к его дочернему каталогу `opencode` через `-OpenCodeConfigRoot`. Последний сегмент обязан называться `opencode`, чтобы CLI и файловая проверка читали один root; внутри поддерживаются штатные `opencode.json` и `opencode.jsonc`.

Она показывает:

- установленные skills и managed rules;
- effective primary/subagents из `opencode debug config`;
- их model и reasoning settings;
- число различных моделей и read-only subagents;
- effective hard-deny права изолированного spec reviewer;
- `heterogeneous_configured` либо `explicit_routing_required`.

`heterogeneous_configured` доказывает только конфигурацию. Он не доказывает, что primary agent действительно выбрал правильного субагента в конкретной задаче. Это оценивается по `.bsl-flow/reports/subagents`, исправлениям и финальной приёмке родителем.

## Два независимых маршрута моделей

Обычная разработка и implementation review используют agents из твоего `opencode.json`. BSL Flow не фиксирует их имена и модели; OpenCode выбирает доступных subagents по описанию, а глобальные/project `AGENTS.md` задают границы делегирования.

Независимый review спецификации — отдельный маршрут. Для M/L/high-risk `1c-spec-review` запускает sealed/read-only OpenCode-процесс и берёт модель из проекта:

```yaml
review:
  reviewer:
    provider: opencode
    agent: bsl-flow-spec-reviewer
    model: deepseek/deepseek-v4-pro
    variant: high
```

Поэтому смена primary/subagent моделей не меняет spec reviewer автоматически, и наоборот.

Для L/high-risk задачи после reconciliation и final validation имеет смысл начать реализацию в новом компактном контексте, если текущая сессия уже большая или долго ожидала внешний review. Передавай только исходную задачу, финальную spec, reconciliation, final-validation, релевантные пути и замысел проверки. Для S/M это не обязательное правило, и BSL Flow сам сессии OpenCode не создаёт и не сбрасывает.

## Проверка оркестрации

Для контролируемого эксперимента выбери небольшую M-задачу без runtime-изменений базы и попроси OpenCode:

1. поручить отдельному read-only subagent исследование текущего кода;
2. самостоятельно собрать короткую OpenSpec-спецификацию;
3. выполнить обязательный `1c-spec-review`;
4. поручить другому read-only subagent review будущей реализации или тестового плана;
5. записать agent/model/effort, проблемы, исправления и parent acceptance через audit helpers.

До запуска зафиксируй ожидаемый routing из `Test-BSLFlowOpenCode.ps1`. После запуска сравни его с фактическим журналом. Если OpenCode не делегировал подходящую независимую часть, хотя агенты настроены, усиливай их descriptions/глобальную routing policy. Если `explicit_routing_required`, сначала добавь primary/subagent definitions в `opencode.json`; BSL Flow не должен угадывать модели за тебя.
