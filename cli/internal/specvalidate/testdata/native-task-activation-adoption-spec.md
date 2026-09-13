# native-task-activation-adoption

## Классификация

- Сложность: L
- Риск: high

## Цель

Дать planned-задачам repository store работающий переход в controller lifecycle и добавить явный перенос старых checkout-local задач с сохранением UUID, истории и доказательств. Поддержка Astra fallback остаётся контрактом `api-specification-council` и проверяется отдельным ограниченным live-пилотом.

## Требуемое поведение

1. `task activate` принимает полный trusted request, task UUID, expected revision и точный project/worktree root этого клона. Успешный переход добавляет revision `ready` в существующий journal того же UUID и сохраняет planned metadata. Невалидный request, revision conflict или неподдерживаемый execution contract не создают активацию.
2. Go controller — единственный writer repository task revisions. Legacy PowerShell не пишет repository v2 и не становится автоматическим fallback при ошибке native пути. Переход не создаёт второй authoritative checkout-local journal.
3. Активированная задача проходит существующий маршрут inspect/spec/spec_review/implement/code_review/verify/acceptance согласно mode, classification и policy. Analysis-only не разрешает implementation; inspect не ослабляет trusted classification. Отсутствующий обязательный gate блокирует acceptance.
4. Attempt сохраняется до dispatch и связывается с task, stage, intent/authorization revisions, policy, baseline, актуальными входами и исполняемым provider contract. Worker output не является разрешением, результатом независимого теста или готовым acceptance receipt.
5. `run`, `resume`, `status`, `next`, `context`, `record`, `update`, `cancel`, `accept` маршрутизируются по авторитетной task/store identity. Planned task не запускается. Повреждённая либо конфликтующая canonical задача не падает обратно на legacy copy.
6. Resume использует сохранённый terminal result с проверкой identity/hashes; неопределённый эффект и незавершённый dispatch не повторяются автоматически. Изменение policy/source/criteria инвалидирует зависимые результаты. Cancel не объявляется rollback. Повтор того же подтверждённого действия не добавляет дубликат результата.
7. Acceptance требует текущих обязательных gates и точного source manifest; `implement PASS → verify FAIL` никогда не создаёт acceptance или accepted memory. Сохранённый completed статус не превращает устаревшие evidence в текущий PASS.
8. `depends_on` сохраняет metadata-only semantics текущего согласованного реестра: нет автоматического запуска зависимостей и новой проверки effective-completed перед dispatch. Противоречащая этому последняя фраза требования 8 старой registry spec заменяется ссылкой на отдельный будущий dependency-execution contract.
9. `adopt` имеет явные preview и apply. Preview только читает и фиксирует source identity, terminal revision/hash, зависимости и blockers. Apply использует проверенный preview binding; при изменении источника, конфликте UUID или незавершённом effect не выбирает победителя и не перезаписывает чужие данные.
10. Перенос сохраняет оригинальные revision bytes и hash links, UUID, исторические статусы и происхождение. Копии неизменяемых доказательств имеют manifest с исходным и canonical расположением и SHA-256. Отсутствие live worktree не уничтожает уже сохранённую историю, но не разрешает новое выполнение без валидного execution binding.
11. После переноса одна canonical history продолжает исходный prefix, а одинаковая оставленная legacy копия не становится ложным конфликтом. Расходящийся либо изменённый prefix блокируется. Исходные файлы не удаляются и не переписываются; последующие legacy writes не могут незаметно создать второго владельца задачи.
12. Сбой между копированием и публикацией не показывает неполную задачу как готовую. Повтор с тем же source binding идемпотентен; существующий чужой target не заменяется. Частичный перенос имеет точную диагностируемую точку продолжения.
13. Public list/show/history/context не раскрывают credentials, raw prompts или private evidence. Перенос не отправляет содержимое внешним сервисам и не добавляет registry/evidence в Git.
14. Runtime 1С, deployment и Git publication не получают нового допуска от активации/переноса. Unsupported runtime capabilities остаются явными blockers без пропуска критериев и без запуска базы.

## Контекст 1С

- Объект изменений: Go CLI/controller/store, Windows provider adapters, task schemas, read projections и migration fencing. Метаданные и базы 1С не изменяются.
- Evidence path: trusted CLI input → verified clone/task identity → controller gates → immutable attempt → provider observation → verified evidence → one revision/acceptance.
- Проверяемый рабочий сценарий: локальные временные Git repositories/worktrees, source-only задачи и сохранённые legacy journals. Живой Astra-пилот использует синтетическую спецификацию и максимум четыре модельных вызова.

## Не делать

- Не создавать второй task store, очередь, универсальный plugin framework или `tasks.md`.
- Не заменять требуемый controller lifecycle игрушечным S-only сценарием без явно согласованной границы.
- Не принимать переданный пользователем/worker `passed=true` как проверку.
- Не переписывать исходные legacy journals, не удалять originals/worktrees и не публиковать repository changes.
- Не заявлять multi-OS/zero-PowerShell completion по Windows offline trace.
- Не снимать временный запрет durable Unica runtime и не запускать 1С.

## Критерии приёмки

- GIVEN planned task и валидный trusted request WHEN выполняются activate и controller lifecycle THEN тот же UUID достигает корректного terminal состояния в одном canonical journal, metadata сохраняется, checkout-local journal не создаётся.
- GIVEN invalid request, wrong worktree или stale revision WHEN activate вызывается THEN нет ready revision и worker dispatch.
- GIVEN analysis-only либо обязательные M/L/high-risk reviews WHEN вычисляется/выполняется маршрут THEN нет неразрешённой реализации и пропущенных review/verification gates.
- GIVEN implementation завершена, а проверка провалилась WHEN record/accept/resume вызываются THEN нет acceptance, promotion памяти или скрытого повторного dispatch.
- GIVEN сохранённый terminal receipt либо unknown dispatch WHEN resume вызывается THEN первый проверяется и переиспользуется, второй требует reconciliation без повторного эффекта.
- GIVEN одинаковые legacy copies, verified prefix и canonical continuation WHEN list/show/history выполняются THEN одна задача показывает всю историю; divergent/corrupt copy даёт диагностируемый конфликт.
- GIVEN preview, изменённый source, partial copy либо existing different target WHEN adopt apply повторяется THEN нет overwrite/двойной истории; unchanged binding восстанавливает только незавершённую публикацию.
- GIVEN migrated completed task и удалённый исходный worktree WHEN читается история THEN скопированные доказательства доступны, а новый запуск требует свежего worktree/input binding.
- GIVEN legacy writer после adoption WHEN запрашивается запись THEN он не меняет оставленную legacy history и указывает canonical owner.

## Требуемые проверки

- [x] Static — закрытые request/state/migration contracts, path/redaction и engine routing.
- [x] Unit — transitions, canonical hashes, freshness, idempotence, malformed inputs и lineage.
- [x] Integration — настоящий public CLI в временных клонах/worktrees, lifecycle и adoption без модели/базы, process boundary и один canonical writer.
- [x] Differential — read-only/golden comparison с текущими legacy route/gate/recovery решениями; внешние эффекты не повторяются ради сравнения.
- [x] Fault injection — authoritative publication/attempt boundaries и interrupted migration.
- [x] Packaging — Go tests/vet, воспроизводимый CLI, focused package suites и проверка exact artifact.
- [ ] Runtime 1С — не требуется и не разрешён.

## Неопределённости / допущения

Контракты после spike и независимого design review зафиксированы в `design.md`, `provider-contract.md` и `migration-contract.md`; результаты review — в `review-advisory.md`. После adoption пользователь выбрал явный rebind того же UUID с новой проверкой актуальности. Статусы проверок выше обозначают требуемые виды evidence, а фактический результат реализации и приёмки фиксируется отдельно.
