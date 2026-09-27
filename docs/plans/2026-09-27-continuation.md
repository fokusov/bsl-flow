# Продолжение remediation/0.9 — 27 сентября 2026

Основание: [handoff](2026-09-27-handoff.md) и принятый [план](2026-09-26-remediation-plan.md). Работа локальная; push, PR, merge в main, установка в пользовательский профиль и операции с реальными базами не выполнялись.

Итог: интеграция в `remediation/0.9` завершена до офлайн-проверки. Продуктовый снимок — `aacb4e5`; последующие `e6b3c59`, `b1c4b7b`, `fb08483` исправляют только пакетный тест. Офлайн-проверки пройдены поэтапно; единый повторный `Build-BSLFlowPackage -Package full -Test` после последних исправлений не выполнялся. Полная приёмка remediation-плана и готовность релиза остаются **BLOCKED** по перечисленным ниже live/runtime границам.

## Интеграция

| Этап | Результат |
| --- | --- |
| Ф3 инструкции | Bootstrap и восемь SKILL.md сокращены; единый шаблон и бюджеты проверяются в CI. Правила авторизации/инвентаризации/runtime сохранены в references. [Карта переноса](2026-09-27-instruction-migration.md). |
| Ф5.1 onec-ops | Порт и семь адаптеров, authorization в dispatcher и прямых mutating entrypoints, grounding на реальном индексаторе. Unica applied runtime блокируется. Process-success отличается от проверки состояния БД. |
| Ф6 harness | Fake good/bad/drift, агрегация и manual CI; acceptance_complete отделён от частичных проверок. Стартовый набор содержит 6 задач, а не целевые 20–30. |
| Ф4.5–4.7 | Реестр адаптеров, Next/Submit и одноразовый dispatch. Current-agent не выполняет обязательное независимое code review. Claude parsing/probe подготовлены, реальный dispatch блокируется до доказательства превентивной изоляции путей. |
| Ф2 | Core/Managed слиты. SHA-256/inventory проверяются до установки; backup/rollback, сохранение Claude settings/hooks, host-specific reviewer defaults и CI-матрица реализованы. |
| Ф7 | Синтетические S/M примеры с проверяемыми lint/review/final; рецептник, словарь, README RU/EN. |
| Версия | 0.9.0-dev.1 в VERSION, plugin manifest, документации и project upgrade. |

## Ошибки, найденные при продолжении

- Прямой native adapter обходил authorization dispatcher: проверка перенесена в общий helper и повторяется на каждом mutating entrypoint.
- Пустой JUnit и произвольный импорт PASS могли выглядеть успешными: проверяются фактические counts, schema и связь результата с запросом.
- Бенчмарк считал неполную приёмку успешной: пропущенные обязательные проверки теперь исключают effective_pass.
- Current-agent мог сам выполнить required code review: стадия теперь остаётся у независимого исполнителя контроллера.
- Claude path checks выполнялись после чтения/записи: это обнаружение, а не защита данных. Реальный dispatch блокируется до preflight и расхода бюджета.
- `claude plugin validate .` проверял marketplace и не обнаруживал ошибку plugin manifest. Отдельная проверка `.claude-plugin/plugin.json` выявила неподдерживаемый `displayName`; поле удалено.
- Измеритель поверхности заходил в защищённые generated work-каталоги: исключения применяются до рекурсивного обхода.
- Активный change очищался после FAIL/BLOCKED: инструкции теперь сохраняют его до принятого результата.
- Git stderr смешивался с именами файлов, ошибка diff могла дать пустую успешную выборку, а пустой metadata index падал на StrictMode: разделены потоки, ошибки дают BLOCKED, добавлены регрессии.
- Grounding создавал кэш внутри benchmark fixture и портил scope drift: проверка теперь пишет во внешние временные файлы.
- Недоступная статическая проверка могла трактоваться как ноль ошибок: BLOCKED/неизвестный verdict сохраняются, список изменённых BSL разбирается построчно до фильтрации расширений.
- Синтетические сравнения могли выглядеть основанием для kill-list: fake evidence исключено из таких решений; неполные реальные baseline и неопределённые отношения дают `not_evaluable`/`n/a`.

## Проверки

Целевые проверки выполнены в основной копии: instruction budget 81; skill links 101; instruction consistency 11; ADR index 25; governance 20; user config 30; onboarding 28; onec-ops 163. Fake benchmark good/bad/drift и агрегация прошли примерно за 9 секунд. Project upgrade прошёл. Marketplace и plugin manifest отдельно прошли CLI validation.

Первый полный пакетный прогон **не является приёмкой**: пакет был изменён во время выполнения, и controller корректно остановился на policy/package hash mismatch. Первый прогон из ZIP выявил устаревшее ожидание CouncilCycle: final receipt теперь содержит также `grounding_unavailable`. Ожидание исправлено, 58 CouncilCycle checks PASS. Второй прогон из ZIP прошёл Council, но остановился на Windows Git `GIT_DIR too big` в глубокой тестовой распаковке. `Build` теперь использует короткий уникальный TEMP-путь и точный cleanup guard; LegacyFence 21 и Lifecycle 51 PASS.

Прогон ZIP снимка `aacb4e5` прошёл все основные контроллерные наборы, включая Repair 36, Delivery 10, Runner 16, RunnerRecovery 20, NativeController 13, NativeRecovery 31, NativeReuse 18, RequirementCoverage 19, CoverageController 24 и TaskPublication 39. Затем прошли grounding 38, static diff 37, benchmark, CorePackage и CurrentAgent 30. Прогон остановился на `Test-InstallCore`: ранее запущенные тесты сохраняли evidence в `work/` распаковки, а установщик корректно отвергал лишние файлы. Проверка установки перенесена до создания evidence; строгая проверка inventory сохранена.

На свежей распаковке исправленного ZIP установка дала 73 PASS; все оставшиеся зарегистрированные наборы также прошли. Финальная bootstrap/review-фикстура выявила ещё два устаревших ожидания: наличие конкретных Council/OpenCode моделей в portable defaults. Тест теперь проверяет отказ без привязки и явно использует синтетические модели только в изолированном проекте. Завершающий сценарий прошёл с exit 0. Сравнение манифестов подтвердило неизменность продуктовых файлов и ранее прошедших тестов; изменялись только `Test-BSLFlowPackage.ps1` и этот отчёт.

Локальные доказательства сохранены в `outputs/verification/`: `full-package-acceptance.log` (остановленный общий прогон, **FAIL**), `package-completion-reviewer-fixture.log` (успешные установка и оставшиеся наборы, затем устаревшая review-фикстура), `package-completion.log` / `.status` (**PASS** завершающего сценария), `complete-package-checks.ps1` и `remaining-package-checks.ps1` (точный способ поэтапного завершения). Это совокупное покрытие набора, а не утверждение об успешном непрерывном запуске исходной команды.

### Интегрированные Core / Managed

- `Test-CorePackage`: PASS; `Test-InstallCore`: 73 проверки PASS. Проверены изолированные профили четырёх хостов, backup/rollback, migration, tamper rejection, Markdown closure и Core review без Council engine.
- `Build-BSLFlowPackage -Package core -Test`: PASS, 163 файла, SHA-256 `3c7fbd8c21f5e73eceabe75109f0676c9089524ffa78e51180e82e9e3d6699c5`.
- `Build-BSLFlowPackage -Package managed -Test`: PASS, 77 файлов, SHA-256 `73ed71483968e12a3e3729c29bc36f815804d17dda99ffe3777d3c1076303b16`.
- Полный ZIP: 465 файлов; повторная сборка воспроизводима. Актуальные SHA-256 и состав записаны в `outputs/verification/full-package-final.json` и соседнем с ZIP `.sha256`. Финальный отчёт добавлен после проверки кода; повторная упаковка документации не означает повторный монолитный тест.
- После последнего grounding fix: `Test-1CSpecGrounding` 38 PASS, harness good/bad/drift PASS; fake bare/core по 6 задач: false PASS=0, drift=0, effective pass=1/6 в каждом режиме; NOT_RUN=8, SKIPPED_RUNTIME=5. Это проверка раннера, не качества модели.
- Managed adapter: 21 targeted suite PASS, включая CurrentAgent 30, Claude 39 и TaskRepair 36. Полный интеграционный прогон из ZIP учитывается отдельно.
- Managed surface: 13 906 строк, bootstrap: 803 байта; baseline обновлён после ADR-11 portability и ADR-12 переноса общих API-функций в Core.

## Открытые границы приёмки

- Claude headless Managed: BLOCKED до подтверждённой превентивной защиты read-denied и write-allowed путей. Офлайн-fixtures подтверждают отказ и разбор протокола, не живую изоляцию.
- Реальный Codex sandbox-пилот: установленная 0.158.0-alpha.2.1 отвергнута stable-version gate. Это ограничение host-среды, не PASS.
- Heterogeneous Council re-review `execution-contract-v01`: в окружении отсутствует `ANTHROPIC_API_KEY`; override и review-blocked сохранены. Секреты не выводились.
- Ручной Claude plugin pilot и сравнение поведения старых/новых инструкций на пяти реальных задачах не выполнены. Fake agent этого не доказывает.
- Расширение benchmark-набора до 20–30 задач, Managed benchmark mode и реальные модельные результаты остаются незавершёнными. Kill-list не закрыт, перенос в experimental по выдуманным метрикам не выполнен.
- Реальная EDT-выгрузка, native/vrunner CLI и 1С/YAxUnit/Vanessa поведение не проверялись; действующее временное runtime-ограничение сохранено.
- Linux/macOS требуют результатов реальной CI-матрицы; локальные Windows-тесты не заменяют их.

До закрытия обязательных live/runtime границ development-артефакты не означают готовность релиза или полную приёмку всего remediation-плана.
