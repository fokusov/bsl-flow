# Native task activation and adoption

Статус: core/provider design принят после независимого ревью и уточнений (review-advisory.md); migration contract проверяется отдельно до изменения adopt code.

## Выбранный этап

Это следующий writable slice существующего Go CLI. Полный перенос provider adapters, build/install и всех ОС из `native-cross-platform-cli` сюда не включается. Временно сохраняется явный Windows compatibility provider, использующий проверенные source-only stage helpers PowerShell. Его наличие отражается в capabilities и документации; это не zero-PowerShell release.

Go владеет canonical repository revisions и решениями route/dispatch/record/recovery/acceptance. Provider исполняет ограниченную стадию и возвращает наблюдения; он не вызывает legacy controller CLI, не создаёт shadow v1 journal и не записывает canonical revisions/current/input events/acceptance.

Отвергнуты:

- Вызов старого `Run` под новым UUID/store: `Task.Engine` и `Invoke-BFStage` записывают checkout-local v1 state; получаются два владельца.
- Снятие staged guard без execution path: planned-задачи становятся ложными ready-задачами.
- S-only file-assertion workflow как выполнение всего запроса: теряются обязательные маршруты и review gates.
- Полный перенос всего framework одним изменением: превышает необходимый этап и смешивает independent OS/build/runtime migration.

## Store и state

Существующий repository v2 card получает закрытое поле `controller` при переходе lifecycle из planned в controller. Card metadata и внешние revision/hash остаются в одном journal. Вложенный controller payload не имеет собственного журнала; schema1-shaped view для compatibility helpers является переданным снимком, не вторым state store.

До любого append проверяются candidate schema, UUID/request identity, expected revision и lifecycle invariants. Не допускается обнаруживать invalid candidate только после публикации файла. Native registry projections разворачивают controller payload только через allowlist; raw request/evidence не попадают в stdout.

Activation проверяет точный Git worktree root, принадлежность клону, trusted request, capability, clean baseline, project policy и worker binding до ready revision. Смена engine/runtime identity является явной новой execution binding, а не скрытым fallback.

Закрытый controller payload содержит ровно execution fields текущей v1 state schema: `project_path`, `worker_path`, `baseline`, `request`, `request_hash`, `intent_hash`, `policy_hash`, `intent_revision`, `authorization_revision`, `correction_rounds`, `policy_files`, `policy_rules`, `classification`, `status`, `stage`, `active_attempt`, `unresolved_effect`, `attempts`, `evidence`, `events`, `question`, `blockers`, `acceptances` и optional `repair`; дополнительно обязательный `engine`. Остальные поля отклоняются. Вложенные request/criteria/provenance/classification/repair/attempt/evidence/acceptance shapes сохраняют текущие closed contracts и semantic bounds, а не только поверхностную JSON-валидность. Planned state не содержит `controller`; lifecycle=controller требует его.

`engine` имеет закрытые поля `name=native`, `contract_version=1`, `provider=bsl-flow.native-provider.windows-ps.v1`, `host_sha256`, `provider_sha256`, `asset_manifest_sha256`. `engine.provider`, input/output `contract` и `provider_contract.name` должны быть побайтно одинаковы. Hashes — lowercase SHA-256. Переданный provider StateView исключает `engine` и добавляет ровно `schema_version=1`, `task_id`, `revision`, `previous_sha256`, `created_at`, `updated_at` из outer revision. Обратного импорта StateView в journal нет: Go обновляет только поля, выведенные из validated observation/explicit trusted input.

Outer journal сохраняет существующий canonical encoder. `previous_sha256` — SHA-256 canonical representation всей предыдущей outer revision; raw file hash учитывается отдельно. Card/schema timestamps и revision принадлежат outer journal, а не независимому вложенному счётчику. Валидация candidate идёт после вычисления следующей revision/previous hash, но перед atomic publication. Затем создаётся derived current projection. Сбой current projection не отменяет опубликованную revision.

Activation publish order: lock → validate current chain/request/revision/root/capability → register immutable activation intent и точный baseline/worker target → create или reconcile только этот worker → stage initial request/policy evidence → append ready revision → derived projection. Восстановление того же activation intent проверяет target/HEAD/clean state и не создаёт второго worktree. Неизвестный или изменённый уже существующий target блокирует продолжение.

Все execution actions сначала разрешают canonical identity. Существующая canonical task directory резервирует UUID даже при corrupt/unsupported journal: возвращается native error, legacy dispatch запрещён. Legacy mode на Windows допустим только для checkout-local v1, никогда для canonical UUID; explicit `--engine legacy-powershell` не обходит это правило. Registry reads остаются native независимо от engine selection.

Версионированная compatibility policy сохраняет прежние Windows v1 вызовы без нового обязательного флага: initial routing по подтверждённому checkout-local v1 выбирает legacy; canonical v2 выбирает native. `--engine native|legacy-powershell` делает выбор явным, но не расширяет matrix. Это начальный выбор по identity/schema, а не попытка другого engine после ошибки. Existing legacy start/runner CLI не ломаются ради включения native registry.

## Core/provider boundary

| Операция | Владелец |
|---|---|
| Resolve repository/UUID, journal verification, locks/CAS | Go |
| Request validation, route, stage eligibility, finite attempt bound | Go |
| Filesystem/policy/provider capability observations | Узкий trusted host/provider; не worker prose |
| Attempt before dispatch, intent/authorization/policy/source binding | Go |
| Source-only worker, existing review/verification tools, raw artifacts | Explicit compatibility provider |
| Stage outcome interpretation, freshness/identity checks, evidence revision | Go |
| Recovery decision, no-blind-retry, cancel state | Go |
| Accepted-source receipt и completed revision | Go |

Provider принимает versioned immutable input с task/attempt/stage identity, request/classification view, baseline/worker roots, разрешёнными artifact roots, current source manifest, dependency hashes и provider contract identity. Он не получает action `accept`, `next` либо произвольный controller command.

Task store под Git common dir явно входит в deny roots worker sandbox. Старый запрет только `<project>/.bsl-flow/tasks` недостаточен. Provider host получает переданный immutable view и только необходимые предыдущие artifacts; worker не получает доступа к canonical revisions/current/inputs. Legacy `Read-BFTask` не используется для cancellation: отдельный наблюдаемый cancel signal связывается с attempt. Correction/diagnose не читают скрытый checkout-local journal; core передаёт проверенные evidence references через узкий context.

Для native profile canonical store, task-control snapshot и прочие task-control roots запрещены worker read/write. Worker write разрешён только для implement в exact worker source root и для нужного scratch. Read-only stages имеют source read-only. Явное широкое переоткрытие project `.git` не должно пересекать canonical deny-root; нужные Git metadata остаются read-only без разрешения читать `<common>/bsl-flow`. Невыразимый набор permissions блокирует provider до dispatch. Отдельный тест настоящего sandbox без model call должен доказать отказ читать/писать canonical revisions/current/inputs, наряду с разрешённой записью в scratch.

Матрица ownership/permissions: native Go host владеет canonical store; native compatibility host читает только immutable context и создаёт stage artifacts, его worker всегда имеет canonical deny; legacy controller host владеет только exact checkout-local v1 task directory, а его worker также имеет canonical deny. Наличие старого engine не разрешает открывать canonical store. Native context/artifact allowlist не переносится на legacy task автоматически; legacy task-control root не переоткрывается для worker. Широкий `.git` allow не может отменить nested canonical deny. Старые непатченные binaries не становятся совместимыми только от появления новой metadata; их writes обнаруживаются как source drift/conflict.

Provider возвращает closed observation: identity, terminal status, summary, proposal, side-effect observation, relative artifact manifest и native process receipt. Go проверяет фактические bytes, containment, declared identity, текущие зависимости и допустимые для стадии изменения. Изменения source допустимы только для implement; spec/reconciliation меняют только зарегистрированные spec artifacts. Read-only этапы и verification сохраняют source manifest.

Существующие trusted stage validators/review reconciliation/verification assertions могут использоваться внутри provider. Они не создают task transitions/acceptance. Provider не может утвердить готовый next_action либо принудить PASS. Admission veto существующего provider budget сохраняется; отсутствие подтверждённого допуска не трактуется как нулевая стоимость.

Go публикует attempt-bound dispatch admission до вызова provider. Snapshot включает лимиты request, предыдущие reservation/outcome observations, policy binding и разрешённый stage; provider не расширяет их. Nested calls (Council роли/reconciliation) получают keys под этим attempt. Provider budget ledger в scratch — переносимый artifact исполнения выделенного attempt, не параллельный task journal. Каждый фактический nested dispatch обязан иметь reservation и один terminal outcome или open/unknown marker. Core проверяет uniqueness/parent binding, переносит ledger в immutable canonical attempt artifacts и учитывает его перед следующим dispatch. Open reservation/unknown cost при monetary limit блокируют следующий paid dispatch. Resume не запускает provider повторно для получения потерянной стоимости. Дополнительный veto provider допустим; положительный ответ сам по себе не разрешает dispatch/acceptance.

Closed observation envelope: `schema_version=1`, `task_id`, `attempt_id`, `stage`, `status=completed|failed|blocked|needs_input`, `summary`, `proposal` (stage-specific object или null), `side_effects=none|source_changed|unknown`, `artifacts`, `process_receipt`, `provider_contract`. Каждый artifact имеет relative `path`, lowercase `sha256`, nonnegative `size_bytes`, closed `kind`; paths проверяются относительно exact artifact root. Core создаёт собственные terminal result/evidence/record envelopes только после проверки наблюдений. Observation не содержит нового state, revision, next_action или acceptance.

Extracted provider entrypoint получает команды только измерения входов/capability и исполнения зарегистрированной стадии. Он не импортирует writable Engine как исполняемый dispatcher и не вызывает `Start-BFTask`, `Read-BFTask`, `Save-BFTask`, `Record-BFAttempt`, `Accept-BFTask`, `Update-BFTask`, `Get-BFNext`. Общая calculation часть `Invoke-BFStage` выделяется до её final record; legacy wrapper сохраняет прежний record path. Запрет проверяется call-graph/static assertion и отдельным process integration trace, включая cancellation и correction/diagnose branches.

## Resume и ошибки

До внешнего эффекта публикуется immutable attempt и revision с active_attempt. Повтор команды читает retained attempt: подтверждённый terminal observation проверяется один раз, а отсутствующий/неоднозначный результат остаётся recovery blocker. Timeout/cancel не означает rollback. Разрешение процесса не берётся из сохранённого worker текста.

Core snapshots/receipts и provider observations версионируются и связываются хешами. Незавершённая публикация receipt/revision восстанавливается с тем же binding; несовпадающие файлы сохраняются и блокируют продолжение. Git hooks/filters и inherited Git environment не получают новый путь выполнения.

## Migration

Конкретный manifest/lineage/fencing contract уточняется по существующим artifact references до реализации adopt. Исторические revision bytes сохраняются. Нельзя незаметно переписать абсолютные paths внутри подписанной истории либо выдать старый acceptance за свежий после смены engine/worktree. Незавершённый внешний эффект сначала требует reconciliation у существующего владельца.

## Проверка границы

Обязателен public native CLI lifecycle с реальной файловой системой, отдельным процессом compatibility provider и детерминированным worker/test seam; в нём отсутствуют checkout-local task revisions и вызовы legacy controller actions. Это offline integration, не live model proof. Сравнение pure routing/state decisions с frozen legacy traces выполняется без повторного side effect.

Отрицательные сценарии: invalid activation before write; inspect-strengthened route; analysis-only; M/L review failure; implement PASS/verify FAIL; stale source/policy; changed terminal bytes; cancel/unknown dispatch; same-ID migration conflict; interrupted apply; legacy writer after adoption. После интеграции — independent review, targeted suites, exact executable smoke и reproducible artifacts.
