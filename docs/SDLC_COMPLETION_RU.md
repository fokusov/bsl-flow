# Завершение управляемого SDLC

Дата: 2026-09-10. Реализация разрешена. Работа выполняется последовательно по этапам C–G плана 0.8; независимые исследования и проверки могут идти параллельно. Срок «сегодня» — целевой, а не основание пропускать приёмку.

## Последовательность и наблюдаемая приёмка

1. **C: штатный native-адаптер расширения и восстановление.** Публичный controller выполняет точную разрешённую сборку и YAxUnit, сохраняет оригинальный JUnit и исходники. Прерывание сохраняет неопределённый результат; новый запуск не повторяет запись. Проверяется реальный bp1-пилот и безопасный сценарий прерывания.
2. **D: достаточность приёмки.** Независимая проверка сопоставляет требования с конкретными наблюдениями и выбранными тестами. Формальная зелёная проверка не закрывает отсутствующее бизнес-наблюдение; изменение требований инвалидирует предыдущую приёмку.
3. **E: эксплуатация очереди.** Перезапуск, несколько владельцев, вопросы и бюджет проверяются на уровне публичного контроллера. Изменения нужны только для выявленных пробелов, существующая очередь сохраняется.
4. **F: управляемая публикация.** Отдельный допуск на конкретный remote/ref, публикация только принятого manifest, журнал попытки и контрольное чтение remote при неизвестном ответе. Установка и откат в production требуют конкретной среды; bp1 не становится production-полигоном.
5. **G: интеграция, документация, выпуск.** Независимое ревью критичных границ, обязательный offline suite, сборка Go CLI, сквозной реальный 1С-пилот, проверка CI после публикации. Корпус проведения, обмена, прав, UI и EPF фиксируется отдельно: unit-пилот не подменяет эти проверки.

## Контракт первого этапа — проект решения

Сохраняется одна state machine PowerShell 7 и Go host. Новый native-адаптер вызывается контроллером, никогда worker. Произвольные shell-команды и Unica durable jobs не используются. Первый маршрут ограничен FILE-базой и расширением с существующим YAxUnit.

Декларация задаёт точный target, executable и версию платформы, source root и имя расширения, модуль и точные JUnit IDs, разрешённые операции и ссылку на допуск пользователя. Credentials поступают отдельно от request и worker; секреты не включаются в hash аргументов, журнал или bundle. Любое изменение декларации требует scope revision.

Проверка secret channel: текущий Codex 0.153.0 разрешает worker чтение `:root`; encrypted CLIXML под тем же пользователем не является изоляцией. Реальный capability probe `filesystem.<path>=none` отклонён unelevated backend: restricted read-only access требует elevated sandbox. Поэтому выбран явный `--runtime-auth stdin`: одна строка JSON `{username,password}` через приватный перенаправленный stdin контроллера, перевод в PSCredential в памяти, без credentials-файлов и секретных environment variables. Worker получает только отдельный поток своего prompt. Следующий вызов controller требует нового ввода. Аргумент `/P` самой платформы остаётся ограничением native 1С; managed worker не выполняется одновременно с native-шагом.

Перед запуском проверяются фактическая платформа, исходники и UUID, FILE marker и состав расширений через разрешённое COM-чтение. Snapshot исходников находится вне writable worker tree. Load работает только с этим snapshot. После load/update выполняется повторное чтение состава; посторонние расширения не должны измениться. Применимость проверяется фактическим обнаружением точного набора тестов, а не только флагом active.

Один владелец держит блокировку базы на всю операцию. До каждого native запуска сохраняется durable intent; пропажа результата после intent блокирует новую запись, включая новую задачу в том же управляемом контуре. Timeout и Cancel не убивают непрерываемое обновление базы. Recovery проверяет завершение процесса и сохраняет контрольное чтение; не создаёт PASS и не повторяет load автоматически. Source-only recovery не может разрешить runtime-неопределённость.

Приёмка проверяет не только счастливый путь: подмена target/source/platform, пропавший и старый JUnit, неполный discovery, другой владелец, Cancel, обрыв между launch и receipt, новый task поверх неопределённой операции и отсутствие утечки credentials. Конкретные interfaces уточняются после исследования существующего engine и пилота; этот раздел не является свидетельством реализации.

## Текущий статус

- A/B и переход на PowerShell 7: ранее реализованы; CI commit `600b6c9` прошёл.
- C: managed native-пилот дошёл до acceptance с оригинальным JUnit 5/5 PASS; после ошибки пути выполнены control-read recovery и test-only продолжение без повторных load/update. Подробности — [native runtime](NATIVE_RUNTIME_RU.md).
- D: trusted mapping и независимый coverage gate проверены контрактными и controller-тестами; модельный пилот завершён acceptance после исправления недостаточного теста. Подробности — [coverage gate](REQUIREMENT_COVERAGE_RU.md).
- E: реализовано восстановление событий из полного журнала и уведомление об ожидании ответа. Прежние 16 проверок, публичный CLI-пилот и финальные 20 регрессий PASS. Независимое review закрыто; проверены quiet snapshot, строгий формат журнала и отсутствие повторного dispatch после durable execution_error.
- F: Git module и controller интегрированы. Helper 18 PASS, controller 39 PASS на реальном локальном bare remote; независимое ревью закрыто. Публичный CLI publish/publish-resume: 9 PASS, один реальный локальный push; общий native CLI smoke: 23 PASS. GitHub HTTPS ещё не прошёл отдельный пилот.
- G: полный offline suite неизменяемого snapshot C PASS. Все обязательные локальные проверки объединённой версии закрыты основным прогоном и адресными продолжениями после двух исправлений тестового harness. Независимое ревью F закрыто, поставляемый Go CLI воспроизводим и прошёл 23 smoke-проверки. Полный чистый CI привязан к публикуемому commit; его результат проверяется отдельно.

## Обновление 2026-09-13: native activation/adoption инкремент

После статуса выше принят и заморожен отдельный инкремент managed SDLC (Go controller + stateless Windows PowerShell provider: activate/adopt/rebind, маршруты S/M/repair, Council v2, memory bridge). Offline-приёмка зафиксирована в `openspec/changes/native-task-activation-adoption/verification.md`; freeze r6 — lanes package/extras/cli exit=0, воспроизводимые ZIP и exe с SHA-256.

Не изменилось: GitHub HTTPS без отдельного живого пилота (F); полный чистый CI по-прежнему привязан к публикуемому коммиту — commit/push рабочего diff не выполнялись, результат удалённого CI проверяется после решения о публикации (G); live Astra council — 0/4, preflight требует запуска из Codex-хоста (fail-closed из внешней среды подтверждён 2026-09-13); настоящий Codex sandbox denial — environmental BLOCKED.

## Обновление 2026-09-14: закрытие четырёх спек, переход к задачам владельца

Четыре спеки изменений (все, кроме продолжающейся `native-cross-platform-cli`) закрыты в согласованных срезах; перечень и границы — в PLAN_0.8, раздел «Закрытие спек изменений, 2026-09-14». Формальная цепочка repository-task-registry восстановлена ревью без codex (opencode/`deepseek-v4-pro`) на финальных байтах: R-001 принят (границы `adopt` переданы спецификации native-task-activation-adoption), R-002/R-003 отклонены; final-validation passed. api-specification-council закрыта по verification (решение владельца: advisory + verification достаточны, council live-приёмка не заявляется). Установленный CLI переведён на freeze r6 exe (`f031fc3d…`). `native-cross-platform-cli` продолжает миграцию (волны 1–4 в `22066c0…71cd4a1`) и не блокирует эксплуатацию. Внешние границы прежние: push/удалённый CI, живой Astra 0/4, Codex sandbox denial, GitHub HTTPS пилот, production-публикация.

## Обновление 2026-09-16: откат native-cross-platform-cli

По решению владельца изменение `native-cross-platform-cli` откачено: Go-бинарник не выпускается, версии под Linux/macOS в ближайших релизах не планируются; PowerShell 7 — снова единственный движок, skills маршрутизируют к PS-скриптам (`Invoke-BSLFlowTask.ps1`). Объём отката: удалены `cli/` и бинарные помощники, из CI удалены Go-дорожки, в `Invoke-CouncilReview.ps1` выполнен обратный порт council admission. Упомянутый выше установленный freeze r6 `bsl-flow.exe` больше не является пользовательским входом; пункты G про «сборку Go CLI» утратили силу.

Последствия для спек: `native-task-activation-adoption` — PS-часть приёмки в силе, Go-контроллер и repository-store v2 записи недоступны (дополнение в её verification.md); `repository-task-registry` — ON HOLD, Go-хранилище удалено, модель данных остаётся концепцией; `user-profile-council-config` — ON HOLD до переанкеровки гейтов на PS-совет (`Council.Common.ps1`); `execution-contract-v01` — ON HOLD до переанкеровки линта на PS-реализацию. Этапы C–G (native FILE-адаптер, coverage, очередь, публикация) остаются в PowerShell-контроллере. Запись отката — `openspec/changes/native-cross-platform-cli/rollback.md`; незакоммиченные Go-исправления сохранены в `work/rollback-native-cli-20260916/uncommitted-go-fixes-backup.patch` (ignored path).

## Обновление 2026-09-16: реализация трёх переанкерованных спек

Три ON HOLD-спеки переанкерованы на PowerShell и реализованы (субагентами, disjoint-file оркестрация):

- `user-profile-council-config` — реализована (Council.Profile.ps1, field-level merge, канонический JSON-hash при вкладе профиля/оверлея, fail-closed allowlist, `BSL_FLOW_USER_CONFIG`). Независимое ревью (council, deepseek-v4-pro): REVISE, опубликованный финал спеки потребовал пополевое слияние вместо сущностного — реализация приведена к опубликованному финалу; final-validation passed=True; reconciliation — `review-reconciliation.json`.
- `repository-task-registry` — реализована PS-native vertical slice (Task.Registry.ps1 + действия Invoke-BSLFlowTask.ps1 + обёртка scripts/bsl-flow.ps1; store `<git common dir>/bsl-flow/tasks/<uuid>/revisions` v1, planned-задачи, graph-lock, staged-BLOCKED activate, legacy read-only discovery). Ревью: REVISE, опубликованный финал с нормативными JSON-схемами — реализация выровнена (289+34 проверок); final-validation passed=True; reconciliation — `review-reconciliation.json`.
- `execution-contract-v01` — реализована (Invoke-1CSpecContractLint.ps1 + 1c-implement/scripts/ExecutionGraph.ps1; артефакты опциональны, BLOCKED-правило в коде). Обязательное независимое ревью — **BLOCKED** на детерминированном chair-гейте после двух прогонов (модель-председатель воспроизводит неразрешимый якорь; конвейер исправен, fail-closed) — `review-blocked.md`. Гейт остаётся открытым.

Проверки: полный offline-прогон пакета `Test-BSLFlowPackage.ps1` (теперь 49 внешних наборов) зелёный; лог `work/package-suite-specs-20260916-rerun.log` (обновляется финальным прогоном этой даты). Внешние границы прежние: push/удалённый CI, живой Astra council, Codex sandbox, GitHub HTTPS пилот, production-публикация.
