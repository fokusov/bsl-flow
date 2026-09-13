# Аудит реализации планов и спецификаций BSL Flow

Дата: 2026-09-12. Основание: текущее рабочее дерево `C:\DEV\BSL Flow`, HEAD `9ab6710`, включая изменённые и untracked исходники. Это оценка реализации и остатка работ, не новая приёмка продукта и не разрешение на реализацию.

Проверены четыре OpenSpec change, общий Agentic SDLC, план 0.8, план завершения SDLC, архитектурный контекст, исправления интеграций и benchmark prerequisites. Исходники, тестовые сценарии и сохранённые отчёты читались без запуска тестов, внешних provider calls, установки, публикации или операций с базой 1С. Изменён только этот отчёт. Два независимых агента исследовали Council и registry/native; основные выводы сопоставлены с исходниками главным агентом.

## Вывод

У проекта есть существенная действующая Windows-основа: controller, gates, recovery, source-only цикл, ограниченный FILE native-пилот, coverage review, очередь и локальная Git-публикация. Это не проект на стадии одних планов. Однако текущий объединённый increment ещё нельзя считать принятой поставкой.

Из новых спецификаций ближе всего к завершению `repository-task-registry`. Council и память имеют существенный код, но незакрытые поведенческие контракты. Полная миграция на native Go практически впереди: первый registry slice не заменяет PowerShell controller.

Проценты готовности намеренно не вычисляются: критерии имеют разный вес, а неработающий default route или нарушение recovery существенно важнее количества реализованных helpers.

## Оценка по каждой спецификации

Диапазоны — предварительная трудоёмкость одного опытного разработчика, знакомого с проектом и использующего AI-инструменты. Включены исправления, релевантные проверки и ревью; не включено ожидание разрешений, доступов, внешних исправлений и очередей CI. Рабочий день для пересчёта — 6 продуктивных инженерных часов. Это не измеренный прогноз скорости модели.

| Спецификация | Фактическая стадия | Остаток до заявленного результата | Рабочие дни |
|---|---|---:|---:|
| `repository-task-registry` | Основной native planned/read slice реализован; требуется исправление и приёмка | **16–28 ч** | **3–5** |
| `api-specification-council` | Основной цикл реализован; приёмка не пройдена | **24–44 ч** | **4–8** |
| `self-learning-memory` | Реализован механизм и интеграция; важные правила доверия и retrieval не закрыты | **32–56 ч** | **6–10** |
| `native-cross-platform-cli` | Первый registry slice; основная миграция controller не реализована | **600–1000 ч** | **100–167** |

Оценка native имеет наибольшую неопределённость. Это полный scope спеки, включая три ОС, совместимость истории, recovery, поставку и удаление PowerShell из runtime и репозитория. Первый ограниченный native controller lifecycle можно выделить примерно в **120–200 ч** внутри этого диапазона; он не будет завершением всей спеки.

### repository-task-registry

**Реализовано:** Git common-dir/clone identity; repository store; immutable revisions/hash chain; create/edit/archive/unarchive; optimistic revision и сериализация dependency graph; list/show/history/overview, фильтры и cursor; legacy discovery и диагностика conflicts. Registry команды идут по Go path до PowerShell host.

Источники: `cli/internal/repository/repo.go:46`, `task.go:53`, `commands.go:298`, `graph.go:9`, `catalog.go:72`, `legacy.go:19`, `cli/main.go:122`. Существуют Go unit/Git integration fixtures; в этом аудите они не запускались.

**Конкретный остаток:**

1. **Неверное происхождение задачи.** `catalog.go:106–116` присваивает `OriginWorktree = r.Worktree` при чтении, включая legacy rows. Задача, созданная в worktree A и показанная из B, получает B как происхождение. Нужно сохранять provenance при создании и раздельно выводить origin/current binding; покрыть двумя worktree.
2. **Неполная защита metadata/output.** `task.go:393` проверяет тип и длину title/description, а credential-like filtering применяется к provenance. Title/description с `Authorization`/паролем могут попасть в journal и выдачу, хотя требование 19 запрещает credentials в metadata/output. Нужен согласованный ограниченный контракт отбрасывания/очистки и проверки create/edit/show/list.
3. **Согласование спецификации.** Требование 7 откладывает dependency pre-dispatch gate, но последняя фраза требования 8 всё ещё требует drift checking и attempt binding перед dispatch. Нужно явно отнести эту фразу к будущему native controller slice. Read helpers также вызывают `EnsureIdentity`: side effect первого чтения требует явного документирования либо разделения read/open; это вопрос контракта, не доказанная потеря данных.
4. Прогнать существующие Go/Git fixtures, проверить публичный CLI и совместимость legacy на итоговых исходниках; обновить документацию и receipt приёмки.

**Не считать дефектом:** `activate` возвращает staged `BLOCKED` без task revision до появления совместимого native controller (`commands.go:163`). Это прямо разрешено текущей спецификацией и не включено в оценку registry. Adoption также отложен; он не должен незаметно попасть в остаток этой версии.

Сохранённый `final-validation.json` соответствует текущим hash spec/design. Это подтверждает согласование документов, а не реализацию.

### api-specification-council

**Реализовано:** роли/рубрики, transport/provider/config, critic/chair fan-in, полный final text, v2 envelopes, provenance direct API, optional failure reporting, dependency bindings, terminal reuse, budget ledger, concurrency, prepared publication и исключение повторного legacy reconciler в Council managed path.

Сохранённый `verification.md:3–53` содержит четвёртую проверку от 12 сентября с итогом **FAIL**: 111 focused assertions прошли, но default fallback и concurrent budget acceptance не пройдены. Исторический DeepSeek smoke подтверждает реальный direct API transport, observed model/usage и структурную проверку. Verdict chair — `REVISE`; это не content PASS всей спеки. Проверки сейчас не повторялись.

**Конкретный остаток:**

1. **Штатный tokenless/current-agent путь блокируется.** `Task.ManagedReview.ps1:46` требует observed model/effort, но `adapters/Codex.ps1:85` и `ProfiledCodex.ps1:208` записывают `null`. Assisted entry point `Invoke-1CSpecReview.ps1:97` не передаёт capability/runner. Нужен реально подтверждённый host contract и положительная проверка обоих публичных маршрутов, а не подстановка requested identity в observed.
2. **Budget admission и reservation не атомарны.** `Invoke-CouncilReview.ps1:303` проверяет лимит, а reservation выполняется отдельно на строке 333. Два mutex-protected helper сами по себе не защищают общий check-and-reserve. Сохранённая независимая проверка воспроизвела 0.08 reservation при лимите 0.05.
3. **Public recovery после частичной публикации не начинает с prepared package.** Публичный вызов сначала создаёт новый snapshot/bindings (`Invoke-CouncilReview.ps1:44–71`), а publication resume вызывает лишь в конце нового cycle (`:690`). Если chair изменила spec и процесс прерван после её записи, новый snapshot отличается от исходного: возможен новый dispatch вместо продолжения публикации. Положительный replay fixture возвращает неизменённый исходный spec, поэтому этот случай не проверяет. Это вывод из control flow; отдельного воспроизведения в этом аудите не было.
4. **Evidence из inspect не подключено к public Council input.** Общий `EvidenceText` по умолчанию пуст (`Invoke-CouncilReview.ps1:23`), assisted и managed callers его не передают. Нужна передача предусмотренного проверенного evidence bundle с hash/freshness и границами содержимого.

Разбивка: host fallback/preflight 8–16 ч; атомарный бюджет 2–4 ч; public recovery 6–10 ч; evidence wiring 2–4 ч; интеграционные негативные проверки и ревью 6–10 ч. Итого 24–44 ч. Если installed host не способен выдать требуемые receipts, это внешний blocker, а не повод фабриковать provenance.

Последний сохранённый package gate — BLOCKED из-за недоступного OpenSpec CLI 1.11.0. Исторические сообщения о publication harness failure не следует смешивать с последней границей проверки.

### self-learning-memory

**Реализовано:** `Task.Memory.ps1`, закрытые классы/риски, append-only события и replay, lifecycle, порог 3 подтверждения/2 задачи, shadow/quarantine/supersede, bounded deterministic bundle, optional context projection, интеграция с attempt и stage prompt. Есть `Test-TaskMemory.ps1`, в том числе controller fixtures и пустая/старая память; suite подключён к package/CI.

Источники интеграции: `Task.Engine.ps1:224,265,314`, `Task.Stages.ps1:37,305`, `Task.Architecture.ps1:522`. Сохранённый final validation совпадает с текущими spec/design; он не является отчётом выполнения memory tests.

**Конкретный остаток:**

1. **Подтверждение знания опережает доказательство результата.** `Add-BFMemoryFromAttempt` считает `implement/PASS` источником `accepted-result` (`Task.Memory.ps1:455–465`); вызывается при Record до task verification/acceptance (`Task.Engine.ps1:314`). Worker observation проходит shape/class checks, но его утверждение не становится доказанным от успешного завершения implementation stage. Несколько задач с одинаковым observation могут накопить promotion, даже если последующая проверка результата не пройдёт. Нужна привязка подтверждения к действительно доказанному outcome/evidence.
2. **Класс и риск фактически выбирает worker.** `ConvertTo-BFMemoryItem` проверяет enum и копирует worker labels (`Task.Memory.ps1:124–130`). Текст бизнес-правила с меткой `procedural/low` может попасть в автоматическое продвижение. Контракт «только подтверждённое низкорисковое процедурное знание» требует controller-owned классификации/разрешённого источника; один enum этого не доказывает. Память не меняет gates напрямую, но передаёт такое знание следующим workers.
3. **Retrieval уже заявленного контракта.** В `Get-BFMemoryBundleFromReplay:511` есть stage, paths и fingerprints, но нет task kind/error signature. На строке 540 используется точное равенство path: request scope `src` не выбирает запись `src/a.bsl`. Fingerprints на строке 78 содержат policy/controller/version; изменение pinned toolchain в trusted request не представлено отдельно. Нужно закрыть предусмотренные selection/invalidation сценарии.
4. Довести доказательства crash/replay, полной очистки запрещённого содержимого и лимита фактического bundle. Текущий расчёт размера не учитывает весь rendered scope/excluded; это требует отдельной отрицательной проверки, особенно с длинными paths и большим ledger. Проверить не только pause→next attempt, но и реальные опубликованные dispositions Resume/control-read/BLOCKED без изменения их смысла.

Разбивка: подтверждение/классификация 12–20 ч; retrieval/fingerprints/размер 8–12 ч; crash/negative/public integration и ревью 12–24 ч. Итого 32–56 ч. База 1С для этой приёмки не требуется.

### native-cross-platform-cli

**Реализовано:** Go host и первый repository slice, часть portable filesystem/locking helpers. Это полезное начало, но основная цель спеки ещё не достигнута.

**Не реализовано в целевом виде:** native state machine/gates, process/adapters/review, runner/recovery/publication/delivery, bootstrap/upgrade/package/install, engine migration compatibility, полная OS matrix, differential/golden/fault tests, clean-install без PowerShell, retirement `.ps1`.

Прямое основание: `cli/main.go:90` строит аргументы PowerShell, `:167–175` получает `systemPowerShell()` и запускает engine; `cli/host_windows.go` содержит Windows host implementation. Полноценного non-Windows host нет. `.github/workflows/offline.yml` использует Windows и `pwsh`. Build/package/install остаются PowerShell. В `global/` сейчас 58 `.ps1`, около 13 тысяч непустых строк; repository-level test/build/install scripts увеличивают объём. Объём приведён для масштаба, не как формула оценки.

Предварительная разбивка остатка:

| Часть миграции | Часы |
|---|---:|
| Зафиксировать совместимость, golden fixtures, storage/canonical JSON/locks | 80–140 |
| Native controller transitions, gates, evidence и acceptance | 120–200 |
| Process/worker adapters, Council transport, identity/usage/sandbox contracts | 120–220 |
| Runner/recovery/delivery/publication | 80–140 |
| Bootstrap/upgrade/build/install/package | 80–120 |
| Три ОС, fault/parity/clean-install и удаление PowerShell | 120–180 |
| **Всего** | **600–1000** |

Работы частично пересекаются внутри этапов; диапазоны — укрупнённый остаток, не календарный график. Отдельная реализация уже существующего registry сюда повторно не включена. Наиболее дорогая часть — сохранение контрактов и их доказательство на разных ОС, а не синтаксический перевод PowerShell в Go. До старта стоит завершить/зафиксировать Council, registry и architecture contracts и провести короткий parity spike. После первого вертикального slice оценку нужно пересчитать по измеренной скорости.

## Остальные планы: что выполнено и что осталось

| План | Оценка реализации | Остаток и граница |
|---|---|---|
| `AGENTIC_SDLC_PLAN_RU.md`, M0–M8 | Ядро M1–M3 реализовано; source-only host/lifecycle/recovery и package имеются. Ограниченные реальные pilots расширены в 0.8 | Широкий L/high и 1С бизнес-корпус не доказаны. Старые строки M5/M8 в VERIFICATION нельзя читать без более новых C5/D2 результатов |
| `docs/PLAN_0.8_RU.md`, A/B | Windows Go host, source repair, защищённые тесты и handoff реализованы | Текущий combined diff требует повторной приёмки exact candidate; это не native cross-platform migration |
| `PLAN_0.8`, C/D | Managed FILE native extension route и coverage gate реализованы; сохранены C5 5/5 JUnit и D2 2/2, control-read и test-only continuation | Это ограниченные pilots. Проведение, обмен, права, UI, EPF и server/production не получают PASS от этих результатов |
| `PLAN_0.8`, E/F | Queue ownership/recovery и Git publication реализованы; есть локальный bare-remote pilot | Долговременная unattended эксплуатация и отдельный GitHub HTTPS pilot ещё требуют доказательств. Production rollout/rollback зависит от конкретной среды |
| `PLAN_0.8`, G и `SDLC_COMPLETION_RU.md` | Есть воспроизводимая историческая поставка и сохранённый CI success для HEAD `9ab6710` | Старый CI не покрывает нынешние dirty/untracked изменения. Нужны единый candidate, полный обязательный CI и свежая поставка |
| `ARCHITECTURE_CONTEXT_PLAN_RU.md`, A–E | Основная реализация прослеживается: ADR index/containment, read-only context, полный applicable ADR hash, bounded presentation, optional project index; соответствующие suites имеются | **4–8 ч** на подтверждение combined integration и актуальную evidence/documentation; существенной повторной реализации по inspected contracts не требуется |
| `BENCHMARK_FIX_PLAN_RU.md` | BFI-002 sealed critic и BFI-003 pinned Python/lxml имеют отдельную сохранённую live-приёмку; BFI-006–014 изменения есть | **32–56 ч** на остаток harness и одинаковую public lifecycle/recovery matrix четырёх профилей; paid waits/доступы вне оценки |
| `BENCHMARK_INTEGRATIONS_RU.md` / checkpoint | Интеграционные исследования и часть source smoke выполнены | Полный benchmark ещё не принят. Полную стоимость корпуса без замороженного состава задач/повторов считать преждевременно |
| Отложенное собственное расширение-адаптер BSL Flow внутри 1С (`PLAN_0.8:117`) | Идея будущего развития, не реализованная замена COM и не отдельная законченная spec | Только initial transport/capability spike можно оценить в **8–16 ч**; реализацию — после его результатов и согласования scope |

По benchmark остаток конкретен:

- **BFI-001 открыт в коде:** `Codex.Skills.ps1:67` всё ещё использует `ConvertFrom-Json` без case-preserving parse. Нужны dictionary-safe traversal, duplicate-key negatives и новый Codex+Unica source smoke.
- **BFI-004 открыт:** `Task.Execution.ps1:386` объединяет exit/stop failures в общее `managed filesystem capability did not finish`; нужной поэтапной диагностики причины и длительности недостаточно.
- **BFI-005 частичен:** есть per-task ledger/reservation/admission (`Task.Execution.ps1:143–290`), но это не доказанный единый бюджет всего benchmark с несколькими задачами и Council. Нужен общий scope учёта и матрица failure/unknown/cache/recovery. Не реализовывать существующий ledger заново.
- Закрытие BFI-002/003 не закрывает весь public lifecycle. Четыре одинаково проверенных профиля и финальный combined candidate остаются обязательными условиями плана.

## Как читать сроки без двойного счёта

Для стабилизации текущего Windows-контура, трёх новых прикладных specs (registry, Council, memory), архитектурной интеграции и harness: ориентир **120–210 инженерных часов**, примерно **4–7 недель** одного разработчика при 30 продуктивных часах в неделю. Включены 12–20 ч общей финальной интеграции/выпуска; повторные package/CI работы не считаются отдельно в каждом историческом плане.

Это не включает полную native migration, весь бизнес-бенчмарк 1С, новую 1С extension architecture и production deployment. Полный native scope добавляет ориентировочно **20–34 недели** одного разработчика; общий срок нельзя обещать до parity spike и стабильного контракта основных подсистем. Два разработчика могут сократить календарь, но не вдвое: storage/controller/shared schemas/recovery требуют последовательной интеграции.

Если цель — доказать дополнительные конкретные 1С сценарии для 0.8, сначала нужен фиксированный небольшой corpus. Для ограниченного набора проведения/обмена/прав/UI/EPF предварительный резерв **40–80 ч**, с низкой уверенностью и отдельно разрешёнными targets/runtime routes. Это не оценка полного произвольного SDLC и не разрешение на такой запуск.

## Рекомендуемая последовательность

1. Закрыть Council default fallback, бюджет и public recovery; одновременно можно довести registry в отдельных Go-файлах.
2. Закрыть правила подтверждения/классификации памяти до её приёмки как автоматически продвигаемой памяти.
3. Довести BFI-001/004/005, выполнить одинаковые source-only acceptance scenarios и один общий package/CI на зафиксированном candidate. Актуализировать сводные статусы по этим доказательствам.
4. Зафиксировать native compatibility matrix и выполнить первый вертикальный Go slice. Пересчитать большую миграцию по фактическому parity результату.

Ни галочки в разделе «Требуемые проверки», ни spec `final-validation.json`, ни старые suite counts сами по себе не подтверждают реализацию текущего дерева. Следующая приёмка должна фиксировать exact source/package identity, выполненные сценарии и отдельные BLOCKED/NOT RUN.
