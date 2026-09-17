# Оригинальный запрос

## Запрос пользователя (2026-09-15, планирование следующего релиза)

> 2. хочу чтобы спеки были как описано здесь https://chatgpt.com/share/6aa9554e-17cc-83eb-a956-64f575222e7e

Содержимое ссылки сохранено пользователем в `docs/newsdd.txt` (диалог с ChatGPT «Agent Native SDD»): Intent → Contract → Execution Graph → Evidence; исполняемый граф задач (kind, depends_on, allowed_scope, satisfies, verify, mutation); verification как данные (arrange/act/expect); done_when и программный BLOCKED; complexity-driven artifacts; эксперимент «Markdown SDD vs Agent Execution Contract» на существующих метриках.

## Подтверждение плана

Пользователь подтвердил («да, все так») предложенный план:

- v0.1 — рядом с существующим SDD: артефакты + детерминированный линт + скилл-исполнитель; совет, контроллер и runtime-гейты не трогаются;
- этап 2 (done_when/BLOCKED на уровне контроллера) и этап 3 (компилятор spec→execution + эксперимент на `spec metric`) — отдельными спеками после v0.1;
- уже реализованное не дублируется: complexity-driven артефакты (S → нет артефактов, M → spec.md, L/high → +design.md) уже существуют; `spec.md` остаётся человеческим слоем; секции спеки не переименовываются; per-task model routing и ADR — вне v0.1.
