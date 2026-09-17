# Windows compatibility provider v1

Этот контракт фиксирует интерфейс Go owner ↔ packaged PowerShell provider для текущего increment. Он не предоставляет произвольный command dispatcher. Неизвестные поля/версии/operation отклоняются.

## Input

Один UTF-8 JSON document через redirected stdin, максимум 16 MiB:

```json
{
  "schema_version": 1,
  "contract": "bsl-flow.native-provider.windows-ps.v1",
  "operation": "measure",
  "task_id": "uuid",
  "state_view": {},
  "attempt": null,
  "context_root": "absolute private stage context",
  "artifact_root": "absolute output directory",
  "canonical_store_root": "verified git common dir/bsl-flow",
  "cancel_signal": "absolute attempt-bound signal path",
  "provider_contract": {},
  "prior_artifacts": []
}
```

- `operation`: `measure` или `execute`. Measure не запускает модель/тест/worker и не меняет source/spec; он валидирует request/profile и читает source/policy/dependency observations. Execute требует уже зарегистрированный attempt.
- `state_view`: точная v1-shaped проекция из design.md. `task_id` должен совпадать с view/request. Measure до активации получает provisional view: `worker_path=project_path` (существующий точный Git root), `baseline=HEAD`, `active_attempt=null`, пустые attempts/evidence/acceptances и classification из trusted request. Этот view используется только для чтения baseline/policy/capability и никогда не сохраняется как activated state; execute с таким view запрещён. Ready revision получает отдельный зарегистрированный worker root.
- `attempt`: null для initial measure либо exact start object: `schema_version`, `task_id`, `attempt_id`, `stage`, `intent_revision`, `authorization_revision`, `dependencies`, `source_manifest`, `worker_path`, `executable`, `requested_models`, `started_at`, `operation_id`. Optional closed fields, уже поддержанные legacy stage helpers, передаются только после явной проверки core. Execute с null attempt запрещён.
- `context_root`: временная приватная проекция нужных прошлых artifacts с legacy-compatible relative layout `attempts/<id>/...` и `budget/ledger.json`. Она никогда не содержит revisions/current и не является task journal. Core создаёт её из проверенных immutable artifacts, а provider directory resolver возвращает её только для exact task/project identity.
- `prior_artifacts`: array `{path, sha256, size_bytes, kind}` с тем же закрытым набором kind, что у output artifacts; path относителен context_root. Все перечисленные bytes проверяются до использования. Новые raw files располагаются под artifact_root. Source/spec write scope выводится из stage, а не произвольных input allow roots.
- `canonical_store_root`: независимо сверяется с actual Git common dir. Весь canonical store и context control inputs запрещены worker sandbox. Host provider читает только переданный view/context и разрешённые source/policy/provider inputs; обращения к canonical journals отсутствуют.
- `provider_contract`: closed `{name, version, host_sha256, provider_sha256, asset_manifest_sha256}`; name фиксирован выше, version=1. Core проверяет executable/assets до вызова. В stdin нет credentials; существующая разрешённая provider authentication boundary сохраняется вне журнала.
- `cancel_signal`: core-owned immutable signal с exact task/attempt identity; отсутствие означает отсутствие наблюдаемого cancel. Malformed/mismatched signal блокирует работу. Provider не читает task state для cancellation.

Measure перед запуском новой стадии получает её attempt-shaped binding без dispatch authority; execute разрешён только после Go-published attempt. Нельзя передавать незарегистрированный measurement binding в execute.

## Measure output

Closed document `{schema_version, contract, task_id, operation, request_valid, policy_files, policy_rules, source_manifest, spec_inputs, dependencies, capability, blockers}`. Version/contract фиксированы, operation=measure. `request_valid` — observation strict schema/semantic validation; core также проверяет execution/authorization fields, identity, supported criteria и classification bounds. `policy_files` и source manifest имеют существующие canonical shapes; Go проверяет listed bytes и их hash, core-owned enumeration исключает omission путём сравнения inventory. `spec_inputs` — object с ключами `original-task.md`, `spec.md`, `design.md` и nullable SHA-256 из существующего Get-BFSpecInputs; это не array. `dependencies` содержит только известные stage keys. Capability — наблюдение executable/version/profile/sandbox с точной identity, не утверждение модели. Отрицательный результат не разрешает partial dispatch.

## Execute output

Closed document:

```json
{
  "schema_version": 1,
  "contract": "bsl-flow.native-provider.windows-ps.v1",
  "task_id": "uuid",
  "attempt_id": "uuid",
  "stage": "inspect",
  "status": "completed",
  "summary": "Bounded stage summary",
  "proposal": {},
  "side_effects": "none",
  "dependencies": {},
  "source_manifest": {},
  "artifacts": [],
  "process_receipt": {},
  "provider_contract": {}
}
```

- `status`: completed/failed/blocked/needs_input. Он описывает исполнение стадии, не является task status/outcome/acceptance.
- `proposal`: соответствующий существующему stage contract объект или null. Для code_review сохраняются raw review и reconciliation, для diagnose — category/reason; Go выводит REVISE/REPAIR. Для verify передаются реальные criterion observations/report refs; `passed=true` без доказательств не принимается.
- `side_effects`: none/source_changed/unknown. Go независимо проверяет permitted source changes. Missing/ambiguous terminal receipt не означает none.
- `artifacts`: `{path, sha256, size_bytes, kind}`; path относителен exact artifact_root. Closed kind: raw, process, model, verification, review, budget, failure. Никакие artifacts не имеют kind acceptance/state/authorization. Unknown kinds отклоняются.
- `process_receipt`: closed object `{processes: [...]}` со ссылками на exact `process.json`/`exit.json` и stdout/stderr hashes существующего native runner. Entry имеет ровно `process_path`, `process_sha256`, `exit_path`, `exit_sha256`, `stdout_path`, `stdout_sha256`, `stderr_path`, `stderr_sha256`, `exit_code`, `stop_reason`; paths относительны artifact_root, terminal значения совпадают с прочитанным exit.json. Provider не сочиняет exit code будущего завершения. Список может быть пустым для deterministic file-only verify. Go отдельно фиксирует outer provider executable/hash, actual exit code, stream hashes и stop_reason. Unknown/missing terminal native receipt блокирует record; stop_reason timeout/cancel/size-limit не преобразуется в successful terminal.
- `dependencies`/`source_manifest`: observations после стадии. Core сверяет их с актуальными измерениями и допускает изменение только outputs соответствующей стадии. Ни `next_action`, ни новая revision, ни готовый state не возвращаются.

Core сохраняет provider stdout/stderr/exit и observation неизменяемыми, проверяет manifests, затем строит свой result/evidence/record. Process crash с уже сохранённым observation требует проверки retained exit/identity перед record; отсутствие надёжного terminal доказательства оставляет unknown.

Внутренний Go interface сохраняет outer transport отдельно от JSON provider output: `ProviderTransportEvidence { Receipt map[string]any; Stdout []byte; Stderr []byte }`. Measure/Execute observations имеют `Transport *ProviderTransportEvidence` с `json:"-"`; ошибка после возможного dispatch предоставляет `TransportEvidence() ProviderTransportEvidence`. Поле `process_receipt` wire output содержит только данные внутренних процессов provider. Ошибка decoding/validation после фактического завершения тоже сохраняет outer transport. Provider JSON не может подменить это поле Go или объявить собственный outer exit.

## Budget

Initial context содержит проверенный предыдущий ledger. Новые keys включают registered attempt_id и относительный nested dispatch. Core разрешает только этот attempt; provider atomic reservation/veto сохраняет existing per-call limit semantics. Ledger delta входит в artifact manifest, core валидирует parent identity, uniqueness и соответствие receipt перед следующей стадией. Потерянный outcome не закрывается запуском новой модели. Role/Council ledger — artifact под parent attempt, не самостоятельный task dispatcher.

## Extraction constraints

Выделяется `Invoke-BFStageObservation`, завершающийся observation, без записи controller `result.json` и вызова Record. Legacy `Invoke-BFStage` оборачивает общий calculation helper и сохраняет прежнюю запись/record. Provider cancellation/evidence paths параметризованы через validated context. Provider call graph не вызывает legacy Start/Read/Save/Record/Accept/Update/GetNext; pure validators могут быть перенесены в общий helper без копирования бизнес-логики. Отдельный процесс проверяет отсутствие v1/canonical journal writes и необходимость всех M/L/high-risk gates.
