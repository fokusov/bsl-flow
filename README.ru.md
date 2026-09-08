# BSL Flow

**Лёгкий AI-assisted engineering workflow для разработки на BSL.**

BSL Flow — open-source workflow для AI coding agents, работающих с BSL и проектами 1С:Предприятие. Его цель — оставить минимальный инженерный процесс, который реально снижает риск ошибок, но не превращать каждую доработку в тяжёлый SDD-процесс.

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
- **Agent-native orchestration**: Codex или другой coding agent остаётся оркестратором, а BSL Flow не пытается его заменить.

## Планируемый workflow

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

## Статус

**Pre-release.** Репозиторий готовится к первой публичной версии. Полный пакет фреймворка, инструкция по установке и примеры будут опубликованы здесь позже.

## Для кого

BSL Flow ориентирован на разработчиков и команды, которые используют AI-assisted development для BSL / 1С:Предприятие, особенно при работе с большим количеством клиентских проектов, расширениями, интеграциями и автоматизированным тестированием.

## Ключевые слова

BSL, 1С:Предприятие, разработка 1С, AI coding agents, Codex, OpenCode, OpenSpec, spec-driven development, SDD, AI-assisted development, review спецификаций, YAxUnit, Vanessa Automation.

## Товарные знаки

BSL Flow — независимый open-source проект и не связан с фирмой «1С». Обозначения 1С и 1С:Предприятие упоминаются только для описания совместимости и целевой экосистемы разработки.

## Лицензия

Лицензия будет выбрана до первой публичной версии фреймворка.
