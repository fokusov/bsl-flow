# Проверка BSL Flow 0.8.0-dev.1

Дата: 2026-09-09. Основа: `0.7.0-dev.1`, commit `0480cdf`; ветка `codex/managed-sdlc`.

В поставке добавлены Go CLI со встроенным пакетом, ограниченная диагностика и исправление source-only ошибок, локальная очередь и выдача принятых исходников. Машина состояний остаётся в PowerShell. Готовый exe не требует Go, но требует Windows PowerShell, Git и настроенные model hosts; полная автономная разработка 1С пока не подтверждена.

## Проверки текущей версии

| Проверка | Наблюдение и граница |
| --- | --- |
| Go host | Go 1.27.1 windows/amd64: unit tests и две одинаковые последовательные сборки. Native smoke: 22 проверки, включая строгие аргументы, Unicode, cache tampering, регистрацию/status/cancel, blocked delivery и cancelled-only queue. Финальный exe SHA-256: `e3bca602f41427e6f3a164c4274dc1478cd0d0721268b9c67eb98204e7367b99`; embedded bundle: `37aba72b584870e6cd79960b9d2d452a3f519a15504a470da904028e415a59b3`, 71 файл. |
| Source repair | 36 проверок PASS в PS5.1/7: оригинальная ошибка, бюджет, отсутствие прогресса, бизнес-вопрос, изменение/подмена тестов, malformed JUnit, cached diagnose recovery и продолжение после уточнения. |
| Queue | 16 проверок PASS в PS5.1/7: две выбранные задачи, restart, отсутствие повтора неизменного controller error, восстановление незавершённого dispatch marker, явный blocker после обрыва изменяющего этапа и при пропавшем acceptance. |
| Delivery | 10 проверок PASS в PS5.1/7: точные принятые файлы/удаления, идемпотентность, source drift, семантические изменения receipt, изменение его raw bytes и copy hash. |
| Полная offline suite | PASS в PS5.1.26100.9168 и PS7.6.5 из неизменяемой копии source ZIP. Также прошли 127 reliability checks, storage, lifecycle 51, hardening 19, resume 16, crash recovery 17, установка/rollback, bootstrap, upgrade, lint/review и test-evidence contracts. Model calls в этих проверках: 0. Предварительные неуспешные прогоны сохранены отдельно и не считаются PASS. |
| Реальный Codex repair | PASS: `FAIL → diagnose → implement → code_review → verify → acceptance`. Синтетический fixture: начальная неверная реализация создана тестовым helper, FAIL получен настоящим file verifier. Диагностика, исправление и независимое review выполнены реальными workers. Это не 1С runtime. |
| CI | Добавлен Windows workflow для PS5.1/7 и Go. Первый remote run отклонён до jobs: `matrix` недоступен в `shell` шага. Исправлено: фиксированный outer `pwsh` запускает выбранную оболочку через matrix env и передаёт её exit code. Полная удалённая проверка после исправления ещё не подтверждена; локальные suite PASS приведены отдельно. |
| 1С native-пилот | PASS для двух unit-сценариев `BFP_ТестыПилота` в БП `3.0.191.38`, FILE `bp1`, платформа `8.3.27.2074`, YAxUnit `25.12`. После исправления привязки языка и конфликта шаблонных UUID Build04 завершил load/update с exit 0; Test03 реально выполнил 2 теста, passed=2, failed/errors/skipped=0. Оригинальный JUnit сохранён, SHA-256 `7aead1b4a8065ddb750235b88442b1a36e1a24086dadcc7a0c9691b3246899c3`. Две предыдущие попытки с 0 тестов сохранились как BLOCKED; current PASS выведен из истории 3 попыток. |
| Managed 1С runtime | Не реализован. Native-пилот — отдельный локальный маршрут, не публичный adapter. Структурная проверка CFE: 14 PASS; BSL diagnostics сохраняет 3 unresolved calls к отсутствующим в fixture исходникам YAxUnit. Recovery после прерывания записи, бизнес-покрытие и сквозная runtime-приёмка controller не подтверждены. |

Локальные полные логи и результаты хранятся в ignored `outputs/verification-0.8/`; native smoke сохраняет собственный fixture в `work/cli-smoke-*`. Они не входят в публичный ZIP и не подменяются кратким статусом субагента.

Native-пилот завершён `2026-09-09T16:15:16.0953782Z`: `bp1-native-test-20260909-03`, source manifest `461b1f5c4ea8bd42af2187b205c3f2ad989665407ebe7018492696d062877861`. Перед тестом COM inventory подтвердил прежний instance UUID пилота, новый native hash и неизменность семи свойств остальных девяти расширений. Журнал регистрации выявил причину нулевого discovery: конфликт внутренних идентификаторов `BSLFlowPilot`; замена девяти шаблонных UUID позволила обнаружить и выполнить оба теста без изменения режимов безопасности. Native request/result, steps, исходный JUnit и их неизменяемые копии проверены отдельно от нормализованного receipt. Этот результат относится к локальному wrapper и выбранной чистой функции; не закрывает managed adapter, проведение, обмен, UI, recovery после прерывания записи или развёртывание.

Финальные полные receipts: `verified-fixed-ps7.json` и `verified-fixed-ps51.json`. После фиксации тестовой копии в package test уточнено только исключение generated Go cache/binary и Unica cache из текстового поиска. Обновлённая часть вместе с installation/review continuation повторно прошла в обеих оболочках (`package-final-continuation-ps7.log`, `package-final-continuation-ps51.log`). Все 70 файлов `global/` и исходники Go CLI сопоставляются по SHA-256 с проверенной копией перед выдачей; сборка exe из распакованного source ZIP совпала с локальной сборкой.

Первый реальный diagnosis выявил неоднозначность инструкции: модель правильно нашла ошибку, но вернула status `failed` вместо завершённой диагностики. Контроллер остановился, не выдал PASS и не начал исправление. Инструкция уточнена: `completed` описывает завершение диагноза, категория определяет следующий переход. Первый отказ сохранён, повторный пилот использует новую задачу.

Успешная задача: `20127fda-99ab-43f6-917e-ac2f66e1203d`, revision 16, receipt SHA-256 `453dc3922ff87cdbeae19802db195408c4605195050b6a5dbd93f9222e6586ae`. Fixture: `work/live-repair-pilot-5d5826becede45758a0e7a9fbc6e8162`. Реальных model turns: 3; requested `gpt-6-astra` medium для diagnosis/implement, high для review. Host не сообщил observed model/effort, они сохранены как null. Локальный source handoff этой задачи также проверен. После этого пилота изменена только передача длинного пути в native atomic replace; переходы и контракты repair не менялись, платные model turns повторно не выполнялись. Один такой fixture не измеряет качество реальных 1С задач, экономию или стоимость всей разработки. Более ранний успешный пилот `bb747156-362e-4e4c-b291-a2be792faf94` также выдал проверенный source handoff; его receipt сохранён отдельно.

Регрессия обнаружила и устранила отдельный PowerShell-дефект: общий helper перечислял элементы одноэлементного массива, поэтому валидные `protected_paths`, `arguments` и `expected_tests` могли превратиться в скаляр при проверке типа. Исправлена локальная валидация через прямое чтение свойства, без смены семантики общего helper. Независимо проверены 27 сочетаний Hashtable/OrderedDictionary/PSCustomObject и пустых/одноэлементных/скалярных полей на каждой из двух оболочек.

Полный прогон выявил ограничение Win32 `MoveFileEx` в PS7: временный файл при атомарной замене snapshot имел путь 270 символов. В native-вызов теперь передаётся внутренний extended path; входная проверка по-прежнему запрещает device paths. Регрессия с путями 232/270 и обычный storage suite прошли в PS5.1/7; независимое review правки завершено без замечаний. Сетевой UNC runtime отдельно не проверялся. В тесте установки обновлён ожидаемый список команд с учётом `Deliver` и `Serve`.

План и фактические границы развития: [PLAN_0.8_RU.md](docs/PLAN_0.8_RU.md). Подготовка отдельного 1С-пилота: [PILOT_1C_0.8_RU.md](docs/PILOT_1C_0.8_RU.md). Source-only PASS не закрывает runtime, бизнес-покрытие или публикацию в среду.

# История: проверка BSL Flow 0.7.0-dev.1

Дата: 2026-09-09. Основа: `0.6.1`, commit `198da1c`; реализация в ветке `codex/managed-sdlc`.

Реализован управляемый контур задач для исходников: сохранённые требования, вычисляемый маршрут, изолированный worker, независимое review, привязка evidence к текущим входам, восстановление и acceptance receipt. Реальные source-only запуски прошли короткий S-маршрут и полный `inspect → spec → spec_review → implement → code_review → verify → acceptance` без ручного переключения этапов. Это development-поставка: весь план M0–M8 ещё не принят, managed 1С runtime отключён.

## Что проверено в этой реализации

| Проверка | Наблюдение и граница |
| --- | --- |
| Storage | Canonical JSON, strict parse, immutable hash chain, OS lock, atomic publish и восстановление derived pointer; PASS в PowerShell 5.1 и 7. |
| Task lifecycle | 51 проверка: маршруты S/M/L, analysis-only, authorization, вопросы, deduplication, freshness, spec substitution/deletion, registered draft-to-final binding, post-acceptance edit, JUnit, сохранение lint blocker и отсутствие скрытого retry; PASS в обеих оболочках. |
| Hardening | 19 проверок: полный writable source manifest, project review policy, привязка policy к реально исполняемой установке, unknown effects, pre-dispatch cancel, точный UTF-8 stdin, timeout при непрочитанном stdin, отказ до запуска применимого Git filter; PASS в обеих оболочках. |
| Cached Resume | 16 проверок: критик и reconciler восстановлены из сохранённых raw results без второго dispatch, живой controller не дублируется, несовпадающая модель/effort блокируется, spec binding не обходится; PASS в обеих оболочках. Native/model dispatch в этих fixtures: 0. |
| Crash recovery | 17 проверок. Отдельный процесс действительно завершается с exit 42 после одной записи исходника без terminal receipt. Resume не повторяет запись; control read закрывает orphan без фиктивного PASS. Cancel останавливает сохранённого child, live child блокирует recovery, отмена сохраняется до authorization; PASS в обеих оболочках. Это проверка исходников, а не записи в базу. |
| Spec review hardening | 127 reliability assertions: non-PASS без findings, изменение входов во время provider call, исходные snapshots, legacy-invalid evidence, полная reconciliation validation, defaults при отсутствующем project config, точный UTF-8 обмен через stdin/stdout/stderr, чтение BOM-less reconciliation с кириллицей/emoji и единственный возвращаемый результат wrapper без VoidTaskResult; PASS в обеих оболочках. Provider в регрессиях синтетический. Копия реального M2 result также прошла исправленный final validator в PS5.1. |
| Test evidence helpers | Отсутствующий оригинальный JUnit, source-manifest contract, changed inputs и противоречивые noBuild/noLoad evidence блокируются; regression PASS в обеих оболочках. |
| Upgrade и installation | Проверены additive upgrade, сохранение comments/пользовательской policy, общий каталог семи skills, isolated install/reinstall/rollback, inventory и tamper checks. Базы и глобальная установка в этом шаге не изменялись. |
| Реальный worker host | Native Windows `codex-cli 0.153.0`: источник доступен на запись только implement worker, protected controller/installed analogue недоступны на запись; read-only этап не получает source write. Реальный model tool call, structured output, session ID и исходный usage сохранены. |
| Реальный test sandbox | `Test-SandboxedVerification.ps1`: 5 проверок PASS. Native PowerShell test действительно выполняется через Codex sandbox, не может изменить controller sentinel, даёт точный JUnit, повтор сохраняет прежний report, exit 0 без нового JUnit получает BLOCKED. Model calls: 0. |
| Source-only SDLC pilot | `Test-ManagedHost.ps1`: настоящий inspect и implement, детерминированная проверка текста и приёмка актуального source manifest. Requested model: Astra medium; это один ограниченный case, а не оценка качества модели в целом. |
| Установленный CLI, полный M | Публичные Start/Run из изолированной установки в PS5.1 завершили `inspect → spec → spec_review → implement → code_review → verify → acceptance`, revision 14. OpenCode/DeepSeek выдал REVISE с двумя findings; Codex reconciler принял один и обоснованно отклонил другой, final validation прошла. Отдельный code review и verify завершены. Основной агент независимо сравнил точные байты с greeting + CRLF, diff только hello.txt и manifest acceptance с фактическими исходниками. Это малая текстовая fixture с принудительным M и code review, не 1С бизнес-пилот. |

PowerShell: 7.6.5 и Windows PowerShell 5.1.26100.9168. Новая skill `1c-task` прошла `skill-creator/quick_validate.py`. Независимый критический review переходов, freshness и recovery выполнен; найденные обходы исправлены и закреплены негативными сценариями.

Сохранённые локальные host evidence: `work/host-probe`, `work/managed-pilot-04`, `work/sandbox-verification-01`, `work/installed-M-pilot-b415df04686c` (task `f953a769-c615-4e97-9485-4055a10feaf1`). Эти каталоги исключены из ZIP: в них находятся machine-specific paths, raw sessions и временные Git projects. Неудачные ранние пилоты сохранены для диагностики и не агрегируются в PASS. Источник воспроизводимых сценариев — соответствующие скрипты в `scripts/`. Итоговый внешний delivery report сопоставляет все установленные skill-файлы живого M-пилота с manifest финального ZIP. После пилота подавлен служебный VoidTaskResult в reviewer-wrapper и введена ordinal-сортировка policy inventory: стандартный Sort-Object давал разные порядки неизменённых файлов в PS5.1/7. Ограниченные diffs, единый hash между оболочками, возврат единственного объекта, hardening и crash recovery проверены отдельно; платный M-пилот повторно не запускался. Изменения документации не подменяют проверенные исходники.

## Состояние относительно плана

| Этап | Состояние |
| --- | --- |
| M0 | Подтверждён source-only host; отдельного подтверждения 1С test runner нет. |
| M1 | Исправления gates и негативные регрессии выполнены. |
| M2–M3 | Ядро, хранение, inputs, gates, binding и acceptance реализованы и проверены deterministic fixtures. |
| M4 | Codex managed loop реализован; короткий S и полный source-only M с внешним spec reviewer, reconciliation и отдельным code review проверены реально. Existing skills интегрированы; OpenCode остаётся assisted adapter и отдельным spec reviewer. L/high routing проверен fixtures, реального L/1С бизнес-пилота нет. |
| M5 | Source-only recovery, сохранение результатов и sandboxed test evidence реализованы. Managed integration/UI/external-artifact adapter 1С не реализован как подтверждённый исполнимый маршрут; соответствующие gates BLOCKED. |
| M6 | Есть воспроизводимый build, внешняя SHA-256 и проверка распакованного ZIP через `-Test`; isolated installer/upgrade regression. Итог конкретного архива фиксируется после его сборки. |
| M7 | Детерминированные сценарии покрывают ключевые переходы и сбои. Live corpus ограничен малыми source-only S/M pilots и host/test isolation, поэтому полная live-оценка процесса не заявляется. |
| M8 | Source-only часть PASS. Реальная небольшая 1С доработка с бизнес-проверкой и безопасным runtime recovery пока BLOCKED. |

Для завершения M5/M8 нужна конкретная доработка, отдельно разрешённая development/test база, точный маршрут native build/load/test и наблюдаемый бизнес-критерий. Историческая база ниже не выбирается автоматически. Действующее ограничение Unica не снимается выбором базы или реализацией framework. До этой проверки нельзя объявлять полную готовность агентского 1С SDLC.

## Поставка и повтор проверки

```powershell
pwsh -NoProfile -File .\scripts\Test-BSLFlowPackage.ps1
powershell -NoProfile -File .\scripts\Test-BSLFlowPackage.ps1
pwsh -NoProfile -File .\scripts\Build-BSLFlowPackage.ps1 -Test
```

Обычный package suite не требует сети, глобальной установки или платного provider call. `-HostChecks` отдельно включает конфигурационные проверки реально установленного OpenCode. `Test-ManagedHost.ps1` явно вызывает модель; `Test-SandboxedVerification.ps1` вызывает только native sandbox. Оба требуют нового `-OutputRoot` вне дерева пользовательской `.codex` конфигурации. Их результаты не подменяются offline fixtures.

Для отдельного живого M-сценария используй `Test-ManagedHost.ps1 -FullReview -OutputRoot C:\DEV\bsl-flow-M-pilot-<unique-id>`. Он требует доступных Codex и настроенного OpenCode reviewer, вызывает платные модели и добавляет обязательные `spec_review` и `code_review`. Размер текстовой доработки намеренно мал: сценарий проверяет связность этапов, а не сложность разработки. Без `-FullReview` остаётся короткий S-маршрут.

Build требует PowerShell 7 и помещает manifest файлов внутрь ZIP, а SHA-256 архива рядом с ним. Воспроизводимость проверяется при той же PowerShell/.NET toolchain (в этом прогоне PowerShell 7.6.5), включая пересборку распакованной поставки. .NET Framework в PS5 и современный .NET по-разному реализуют ZIP NoCompression; побайтовая одинаковость архивов между ними не обещается, PS5 entrypoint сборки отклоняется явно. Установка, controller и package suite поддерживают PS5.1/7. `-Test` проверяет inventory и каждый file hash после распаковки, затем исполняет package suite из полученного дерева. ZIP не содержит локального task state и host secrets. Глобальная установка, commit/merge, push и публикация не выполнялись.

## Известные ограничения

- Поддержанная версия Windows adapter закреплена на `codex-cli 0.153.0`; новая версия требует capability проверки.
- Preflight консервативно отвергает `.codex/config.toml`, `.codex/config.json` и hooks по всей цепочке предков. Пользовательская `.codex` над Documents/TEMP тоже может блокировать такой проект; рабочие пилоты находятся в `C:\DEV`. Конфигурация пользователя ради обхода не удаляется.
- Корневая файловая permission — read, поэтому это граница записи, а не обещание конфиденциальности всех читаемых файлов хоста. Отключение сети подтверждено config, отдельный сетевой probe не выполнялся.
- Process-tree cancellation не доказывает остановку произвольного detached service или откат внешнего действия. Неизвестный эффект требует проверки.
- Сбой первого `Start` после создания worktree, но до регистрации журнала, сохраняет orphan и блокирует повтор до отдельной проверки. Controller не удаляет потенциально полезные исходники ради автоматического старта.
- Worker proposal и структурно валидная spec не доказывают полноту бизнес-смысла. Критерии должен задать trusted оператор; обязательное review остаётся отдельным gate.

Практическое использование: [руководство](docs/FRAMEWORK_GUIDE_RU.md). Решения и компромиссы: [архитектура](docs/ARCHITECTURE_RU.md). Наблюдаемый host contract: [Windows Codex adapter](docs/managed-host-contract.md).

---

Следующий раздел — исторические результаты версии 0.6.1. Они не доказывают runtime readiness новой managed-поставки и сохранены как история.

# Проверка BSL Flow v0.6.1

Дата исходной живой проверки: 2026-09-03. Обновление policy/package и интерактивный пилот: 2026-09-08. Один выбранный тест YAxUnit и один локальный сценарий Vanessa прошли в разрешённой FILE-базе. Полная unattended/runtime-приёмка **не завершена**: свежий JUnit/receipt и TestClient не получены, а временное ограничение Unica сохраняется.

BSL Flow v0.6.1 использует единый namespace `bsl-flow`: `bsl-flow.yaml`, `.bsl-flow/`, OpenSpec schema `bsl-flow`, новые имена установочных скриптов и OpenCode adapter. Адаптер поддерживает plan/`-Apply`, manifest/hash chain-of-custody для шести skills, managed global rules, сохранение `opencode.json` и диагностику heterogeneous routing без model-run. Patch добавляет immutable history интерактивных engine-пилотов и атомарное обновление test-setup state без ложного повышения unattended readiness.

## Локальная проверка

Общий регрессионный набор v0.6.1 прошёл в PowerShell 7.6.5 и Windows PowerShell 5.1.26100.9168. Reliability: 50 проверок в каждой оболочке. Шесть skills, YAML/JSON, локальные ссылки, два upstream-schema примера конфигурации и три public test-request примера прошли проверку. Новая регрессия интерактивного pilot evidence проверяет PASS promotion, idempotent replay, immutable run ID, target mismatch, противоречивые counts и отдельные Vanessa runner/TestClient capabilities. Оба изменённых skill прошли `quick_validate`.

Проверяются:

- идемпотентный bootstrap, Git exclusions, OpenSpec schema;
- S/M/L/high-risk routing, read-only reviewer, JSON/schema/reconciliation, invariant hashes и нормализованные метрики;
- одиночный JSON-блок, streamed events, таймаут, сохранение ошибочного ответа;
- тестовая инвентаризация, генерация из установленного skill, явный TestClient профиль и отдельный статус engine/profile readiness;
- интерактивный YAxUnit/Vanessa observation contract, immutable setup history и атомарное обновление `current.json` без unattended promotion;
- точный выбор тестов, согласованность receipt/оригинала, нулевые/устаревшие/пропавшие отчёты и сохранение ошибочных попыток;
- UUID собственных метаданных, неполный выбор источников и пустой набор;
- журнал субагентов, модели/effort, исправления/решения родителя, неизвестные и частичные usage, cumulative snapshots;
- PowerShell 5.1/7, шесть skills, YAML/JSON, локальные Markdown-ссылки, примеры конфигурации по зафиксированной upstream schema.

Имитатор reviewer работает локально, без платного вызова DeepSeek. Fake provider не доказывает доступность конкретной внешней модели.

Архив `BSL-Flow-v0.6.1.zip` содержит отдельный корневой каталог версии; `VERSION`, новый helper и его regression присутствуют. Финальный SHA-256 фиксируется рядом с готовым артефактом, а не внутри самого архива.

## Reviewer efficiency hardening

После анализа одного дорогого OpenCode-прогона default timeout `deepseek-v4-pro/high` увеличен с 300 до 600 секунд; variant не снижен без сравнительных данных о качестве. Validator теперь сообщает одновременно ошибочное значение category и finding ID. Regression связывает enum validator, JSON schema и prompt и отдельно воспроизводит ошибку `category: completeness`.

Read/search reviewer по-прежнему может проверять архитектурные утверждения по исходникам, но whole-tree glob `**/*` запрещён. Permissions блокируют чтение и listing `.git`, `.bsl-flow` и бинарных 1С-артефактов; prompt требует точечных extension-specific glob. `attached_only` остаётся sealed cost/privacy mode. Для L/high-risk документирован необязательный fresh-context handoff после reconciliation; BSL Flow не создаёт и не сбрасывает OpenCode sessions.

Полный offline package test после этих изменений прошёл. Проверка не вызывала платную reviewer-модель и не доказывает процент экономии, качество `medium` или provider cache lifetime.

## Общий каталог skills и отсутствующие тестовые инструменты

Codex- и OpenCode-установщики теперь используют одну копию шести skills в `%USERPROFILE%\.agents\skills`. Основной установщик показывает миграцию в `-WhatIf`, создаёт backup управляемых старых копий из `%CODEX_HOME%\skills`, отказывается удалять reparse-point/junction и восстанавливает прежнее состояние при ошибке. OpenCode-specific каталог хранит только `AGENTS.md`, manifest и backups; дубликат skill в его собственном `skills` блокирует установку, поскольку может затенить общую версию.

Отсутствие каталогов или точных артефактов YAxUnit/Vanessa не блокирует установку framework. Workstation setup сохраняет `not_configured`, не выполняет download/install и выдаёт следующий шаг; требующая provider проверка остаётся `BLOCKED`. Vanessa EPF отмечается как внешний runner (`installed_in_database: not_applicable_external_runner`), а не как расширение базы. Загрузка YAxUnit/необязательных Vanessa-компонентов в ИБ не выполнялась и остаётся отдельным разрешённым runtime-действием после фактической read-only инвентаризации.

Эти контракты и общий layout прошли полный package suite в PowerShell 7.6.5 и Windows PowerShell 5.1.26100.9168. Для предшествующей v0.6.0 основной установщик был применён глобально: шесть installed-tree hashes совпали с тем package, host-specific дубликаты отсутствовали, старая schema и старые managed rules были удалены с backup. OpenCode diagnostic 1.18.23 вернул PASS и обнаружил все skills в общем каталоге. Patch v0.6.1 пока проверен из source и финального ZIP, но глобально не установлен.

## OpenCode adapter: PASS в изолированном тестовом контуре

Standalone adapter проверен в изолированном OpenCode config. Устанавливаются ровно шесть managed skills в общий `<user-profile>\.agents\skills` и два маркированных блока `AGENTS.md`; manifest указывает shared skills root, версию 0.6.1 и timestamped backup внутри `<opencode-config>\.bsl-flow\backups`. Повторный план возвращает `up_to_date` и пустой список действий. Дубликаты BSL Flow skills в OpenCode-specific каталоге блокируются.

Регрессионная проверка подтверждает, что `opencode.json`, providers, credentials и чужие skills не изменяются.

Диагностика OpenCode 1.18.23 проверяет возможность делегирования, наличие строгого read-only subagent и configured reviewer model. Это доказательство effective configuration, но не фактического выбора агента в живой задаче; платный model-run намеренно не выполняется.

## Независимая проверка

Финальный read-only review проверил миграцию/bootstrap, workstation/test setup и EPF/ERF evidence gate. До выпуска исправлены найденные им дефекты: машинные Vanessa-параметры перенесены из tracked `tests` в ignored local state; managed-блок `.gitignore` обновляется без потери следующей пользовательской строки; evidence-файлы обязаны существовать; отсутствие Vanessa даёт `not_configured`, а не crash; TestClient-порты резервируются межпроектно и занятый закреплённый порт блокирует настройку; нестабильный размер `1Cv8.1CD` не используется как идентичность базы. Повторная проверка дала PASS без blocking/major findings. Это локальная приёмка package policy, а не живая приёмка базы.

Отдельный release review чистого namespace v0.6.0 обнаружил и помог закрыть три дефекта упаковки: корневое ignore-правило скрывало вложенный sentinel-шаблон, документация неточно допускала compiler без .NET SDK, а тест schema resolver не изолировал `XDG_DATA_HOME`. После исправлений reviewer подтвердил PASS: старое имя отсутствует в содержимом и путях, обязательные assets доступны Git, MIT и версия согласованы, security/read-only invariants сохранены. Для patch v0.6.1 добавлена отдельная регрессия интерактивного pilot evidence; новый внешний reviewer не запускался.

Контракт поиска YAxUnit/Vanessa также прошёл отдельный read-only review. По его результатам исправлены сохранение корня диска как абсолютного пути, fail-closed формирование `vanessa_epf` при неоднозначном или пустом artifact и устаревшее утверждение о проверке identity файла базы. Повторный review дал PASS; тесты покрывают отсутствующий/пустой каталог, нестандартные абсолютные пути, top-level-only поиск, несколько кандидатов и отсутствие runnable Vanessa path в состоянии `blocked`.

## Новый BSL Flow pilot на bp1: engine smoke PASS, unattended BLOCKED

Создан чистый `C:\PRJ\temp\bsl-flow-v060-pilot`, а `C:\BASES\DEMO\bp1` явно зарегистрирована как FILE development/test target в локальном workstation profile без credentials. Bootstrap создал Git/OpenSpec/BSL Flow scaffolding, выделил TestClient-порт 48721 и не запустил 1С.

Read-only локальная инвентаризация нашла по одному точному артефакту: YAxUnit 25.12 CFE, Vanessa Automation EPF, VAExtension 1.29 CFE и `client_mcp.cfe`. Первичный setup законно вернул `BLOCKED`, `runtime_mutation_performed: false`; локальные файлы не выдавались за установленное состояние.

После отдельного точного разрешения состав был осмотрен в Конфигураторе: активны `YAXUNIT 25.12`, `VAExtension 1.29` и собственное `BP1Tests 1.0.0.1`; переустановка не потребовалась. В 1С:Предприятии выбран только `BP1T_Пилот.СложениеДвухЧисел`: `1/18`, passed `1`, broken/failed `0`, время `0.010` сек. Затем Vanessa Automation `1.2.043.28` выполнила один локальный feature `pilot_arithmetic.feature`: один сценарий, два зелёных шага, сообщение «Ошибок не было». Основная конфигурация, защита и бизнес-данные не менялись.

Свежий JUnit/runner receipt интерактивные прогоны не создали; найденный Vanessa JUnit от 2026-09-03 отвергнут как stale. Арифметический feature не использовал TestClient. Поэтому engine-smoke имеет `PASS`, но unattended/durable readiness и Vanessa UI capability остаются `BLOCKED`.

Patch v0.6.1 добавляет `Save-1CInteractiveTestPilot.ps1`. Он принимает закрытый observation JSON, сверяет точный FILE target, selection, counts, installation kind и evidence boundary; сохраняет immutable `history/<run-id>.json`; повтор с тем же evidence идемпотентен, а конфликтующий run ID отклоняется; `current.json` обновляется атомарно. YAxUnit и Vanessa runner могут получить `pilot_passed`, но helper всегда оставляет `automation_readiness: blocked` и не приравнивает Vanessa runner к TestClient. Скрипт не запускает 1С и не выполняет runtime mutation.

## Предыдущий живой пилот: BLOCKED

С разрешения пользователя был создан изолированный pilot-проект и выбрана локальная FILE demo base. В эффективной конфигурации использовалось только собственное тестовое расширение, без main и прикладного расширения задачи.

Preview build/test успешно нормализовали параметры. Единственный applied test BP1T_Пилот вернул exit code 2 до запуска тестов:

```text
config validation failed: EXTENSION source-set requires at least one CONFIGURATION source-set
```

Следовательно, preview не доказал исполнимость безопасного маршрута. YAxUnit и Vanessa в новом проекте не прошли живую приёмку. Профиль TestClient 48123 сгенерирован и структурно проверен, но фактическое подключение и порт не подтверждены. Арифметический starter не выдаётся за бизнес/UI-тест.

Основная конфигурация, защита и установленные расширения не менялись; постоянные документы не создавались, база не копировалась, сеансы не закрывались. CLI/no-build обход публичного контракта не применялся.

Первоначальный файл результата пилота был производным кратким отчётом. Родитель восстановил **исходный tool output** из ровно одного локального события дочернего сеанса, сохранил исходную оболочку и извлечённый receipt отдельно от реконструкции. Повторного runtime-вызова не было. Новый Save-helper сохранил его как immutable attempt: BLOCKED, failure_state=unknown, started_at_utc=null. Отсутствующие поля не додуманы. Первоначальное отсутствие немедленно сохранённого оригинала не скрывается.

В самой сборке использован новый журнал субагентов: реальные настройки получены из метаданных привязанных дочерних сеансов. Многоходовые usage остаются partial, неизмеренное время — null. Исправления и решение родителя записаны отдельно; журнал не включён в публичный ZIP.

## Следующий разрешённый шаг

Для полной unattended-приёмки нужен подтверждённый публичный маршрут без загрузки main либо отдельно согласованный контур с полноценным CONFIGURATION source-set, а также свежий machine report. Для UI readiness нужен отдельный сохранённый Vanessa-сценарий с фактическим TestClient connection. Интерактивный smoke эти границы не расширяет.

Глобально продолжает работать v0.6.0 из общего `%USERPROFILE%\.agents\skills`; Codex/OpenCode-specific копий шести skills нет. Готовая v0.6.1 не устанавливалась, поэтому текущий Codex ещё не использует новый helper из глобального каталога. Репозиторий не закоммичен и не опубликован.
