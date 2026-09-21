# BSL Flow: assisted и managed работа

Это руководство описывает интерфейс `1c-task` в BSL Flow `0.8.0-dev.3`. Он нужен, когда одной инструкции агенту недостаточно: маршрут задачи, вопросы, проверки и приёмка должны переживать прерывания и оставаться привязанными к точным требованиям и исходникам.

## Граница Core и Managed

| Контур | Как включается | Кто управляет этапами | Что не происходит автоматически |
|---|---|---|---|
| Core (assisted, по умолчанию) | Bootstrap проекта или прямой вызов отдельного skill | Пользователь и текущий агент | Регистрация managed-задачи, controller journal, публикация и оценка |
| Managed (opt-in) | Явный запрос пользователя и `1c-task` для конкретной зарегистрированной задачи | Controller; один `Run` владеет последовательностью этапов | Merge, push, deploy, новая runtime-авторизация и активация других задач |

Core включает `1c-init-project`, `1c-spec`, `1c-spec-review`, `1c-implement`, `1c-verify` и `1c-debug`. Эти skills можно применять независимо и пропорционально риску. `1c-estimate` остаётся отдельной операцией по явному запросу, а не обязательным этапом любого изменения.

Managed добавляет неизменяемый журнал задачи, controller-owned transitions, recovery и проверку доказательств. Наличие установленного `1c-task`, проектного `bsl-flow.yaml` или `.bsl-flow/project.yaml` означает только доступность контракта и bootstrap-состояние. Оно не запускает задачу, не передаёт controller-у управление и не доказывает готовность Codex provider, 1С runtime или тестовой базы.

Локальный реестр хранит только явно созданные planned-записи; до отдельной активации они не являются managed runs. Машиночитаемые `contract/execution/verification.yaml`, оценка, команды публикации и Experience Ledger также opt-in. Даже принятая managed-задача не разрешает merge, push или deploy без отдельного точного допуска.

Self-learning memory выключен по умолчанию. Для явного включения добавь в проектный `bsl-flow.yaml`:

```yaml
features:
  self_learning_memory:
    enabled: true
```

При `false` controller не читает, не дополняет и не передаёт worker-ам `.bsl-flow/memory`; уже существующий ledger остаётся на диске и снова применяется только после opt-in. Это не отключает обязательный managed journal, task context или recovery state.

## Единый CLI в версии 0.8

Пользовательский вход — PowerShell-контроллер `Invoke-BSLFlowTask.ps1`. Для работы нужны PowerShell 7 из стандартной машинной установки `C:\Program Files\PowerShell\7\pwsh.exe`, Git и выбранный provider. Windows PowerShell 5.1 fallback не поддерживается. Пользовательские команды:

```powershell
& "$env:USERPROFILE\.agents\skills\1c-task\scripts\Invoke-BSLFlowTask.ps1" -Action Start -ProjectPath C:\DEV\Example -InputFile C:\Tasks\request.json
& "$env:USERPROFILE\.agents\skills\1c-task\scripts\Invoke-BSLFlowTask.ps1" -Action Run -ProjectPath C:\DEV\Example -TaskId <uuid>
& "$env:USERPROFILE\.agents\skills\1c-task\scripts\Invoke-BSLFlowTask.ps1" -Action Status -ProjectPath C:\DEV\Example -TaskId <uuid>
& "$env:USERPROFILE\.agents\skills\1c-task\scripts\Invoke-BSLFlowTask.ps1" -Action Deliver -ProjectPath C:\DEV\Example -TaskId <uuid>
& "$env:USERPROFILE\.agents\skills\1c-task\scripts\Invoke-BSLFlowTask.ps1" -Action Serve -ProjectPath C:\DEV\Example -InputFile C:\Tasks\queue.json
```

`deliver` создаёт локальную копию принятых исходников, manifest удалений, receipt и краткий отчёт. Для CFE/EPF по-прежнему нужны отдельно подтверждённые native gates. `serve` обрабатывает только явно перечисленные зарегистрированные задачи; установка пакета не включает автозагрузку службы. По окончании заданного окна polling команду можно запустить снова. Вопрос и неопределённый результат остаются причинами остановки до соответствующего ответа/восстановления.

Для разрешения автоматического исправления source-only ошибок добавь в request `max_source_repairs: 1` (максимум 3). Проверки команд должны иметь `retry_safe: true` и `protected_paths`, перечисляющие все их тестовые исходники/fixtures. После доказанного падения выполняются диагностика, исправление, независимое code review и новый verify. Старая ошибка сохраняется; пропавший отчёт или изменение тестов не превращаются в основание для повторного запуска. Без этого opt-in сохраняется прежняя остановка при ошибке.

Queue JSON имеет вид:

```json
{
  "schema_version": 1,
  "queue_id": "58a17e56-49a5-42bf-8fbb-7c7a10cd21f8",
  "task_ids": ["e450d763-776a-4786-8dbe-6d71a9e25fb4"],
  "poll_seconds": 5,
  "max_cycles": 12
}
```

Подставь идентификаторы своих зарегистрированных задач и новый UUID очереди. `max_cycles` ограничивает число проходов за один вызов; исполняемая задача использует собственный timeout и общий сохраняемый attempt budget. Повторный запуск очереди не обнуляет бюджет задач. Изменённый список требует нового queue ID. После обновления controller/engine существующая задача требует явного согласования новой policy: это сохраняет проверяемую связь результата с исполняемой версией.

Текущее состояние очереди хранится в `.bsl-flow/runner/queue-<uuid>-snapshot.json`, содержательные события — в общем `events.jsonl`. При перезапуске controller читает весь журнал: последние 256 ключей в snapshot служат только кешем. Сохранённая ошибка dispatch не повторяется на той же revision, даже если snapshot не успел обновиться. Ожидание ответа тоже создаёт событие; следующая проверка без изменения состояния его не дублирует. Оборванная или повреждённая строка журнала блокирует автоматическое продолжение и требует исследования сохранённых task attempts. Журнал не является отправкой уведомления во внешний сервис; интеграция читает его и использует `event_key` для своей дедупликации.

Managed-контур поддерживает исходники и ограниченный [native-маршрут расширения](NATIVE_RUNTIME_RU.md) в явно разрешённой FILE-базе. В 0.7 реальные source-only пилоты прошли S- и M-маршруты; начиная с 0.8.0-dev.2 публичный controller завершил native unit-пилот с пятью тестами и контрольным восстановлением. Новым native-задачам нужен [mapping требований и независимый coverage review](REQUIREMENT_COVERAGE_RU.md). Временное ограничение durable Unica jobs сохраняется. Актуальные решения и ограничения — в [CHANGELOG.md](../CHANGELOG.md).

Точный машинный контракт запросов, обновлений, результатов и exit codes находится в [`1c-task/references/task-contract.md`](../global/skills/1c-task/references/task-contract.md). При расхождении ориентируйся на него и текущий код.

## Когда какой режим применять

Практическая польза managed-режима видна в трёх ситуациях. Если агент пытается перейти к реализации M-задачи без принятой спецификации, контроллер не выдаёт этот этап. Если после проверки изменились исходники, старый PASS больше не допускает приёмку. Если сессия оборвалась, новая сессия получает сохранённые требования, попытки и причины остановки вместо пересказа по памяти.

Это сокращает ручное переключение skills и поиск последнего надёжного результата. Качество постановки и бизнес-критериев остаётся ответственностью оператора. Ускорение, экономия токенов и процент успешно завершённых реальных задач пока не измерены: успешные технические пилоты не дают таких чисел.

В **assisted** режиме ты вызываешь существующие skills отдельно: исследование, спецификацию, review, реализацию и проверку ведёт основной агент. Этот режим удобен для разовой работы, диалога и случаев, где тебе нужен ручной контроль между этапами.

В **managed** режиме `1c-task` регистрирует задачу и сам последовательно выбирает обязательный этап. Он полезен для длинной реализации, восстановления после прерывания и проверяемой передачи результата. Контроллер хранит состояние отдельно от worker, запускает worker в выделенной Git worktree, проверяет свежесть каждого gate и выдаёт acceptance receipt только после всех требуемых этапов.

Managed выполняет разрешённый native FILE-маршрут расширения через controller и предоставляет отдельные publish/publish-resume для принятого результата. Допуск связывает точные target и операции либо remote/ref. Merge, production deploy и глобальная установка остаются за пределами этого профиля. См. [native runtime](NATIVE_RUNTIME_RU.md) и [публикацию](PUBLICATION_RU.md).

Deterministic gates дополняют skills и OpenSpec:

- skills задают рабочий метод конкретного этапа;
- OpenSpec хранит проверяемую спецификацию для маршрутов, где она нужна;
- контроллер связывает результат с intent, policy, baseline, полной версией исходников и raw evidence;
- модель предлагает результат этапа, но не может принять собственную работу или ослабить критерии.

## Маршруты S, M и L

Инспекция может только усилить исходную классификацию. Ослабить её можно через явный `scope_change` от пользователя.

| Класс и риск | Обычный маршрут `implement` |
|---|---|
| S, low | `inspect → implement → verify → acceptance` |
| S, medium | `inspect → spec → implement → verify → acceptance` |
| M, low/medium | `inspect → spec → single spec_review → implement → verify → acceptance` |
| L или high | `inspect → spec → Council spec_review → implement → code_review → verify → acceptance` |

Для S проектная политика `review.routing.s_default: required` или `require_spec_review: true` добавляет `spec` и один независимый `spec_review`; по умолчанию модель не вызывается. `review.council.enabled` делает Council доступным для L/high-risk, но не повышает M до Council. Недоступный Council блокирует L/high-risk вместо тихого отката к одному reviewer. `require_code_review: true` добавляет code review. Флаги `permissions`, `data_migration` и `data_deletion`, найденные при инспекции, повышают риск до high. Влияния на права, данные, проведение и обмен требуют integration evidence, `form_flow` требует UI evidence, а `external_artifact` — соответствующий профиль. В 0.8 native FILE-профиль выполняет объявленные YAxUnit-тесты; integration/UI/external artifact критерии без соответствующих доказательств остаются `BLOCKED`.

Для `analysis_only` с `analysis_goal: analysis` маршрут состоит из `inspect` и `acceptance`. Значение `specification` добавляет `spec`, а необходимость review определяется классификацией и policy.

## Быстрый старт

Нужны точный корень чистого Git-проекта и установленный entrypoint. Не помещай request JSON в worker worktree. Ниже приведён полный валидный запрос для безопасной source-only задачи; замени пути и модели согласно проектной policy и доступности хоста.

```powershell
$projectRoot = 'C:\work\demo-project'
$taskCli = 'C:\path\to\installed\1c-task\scripts\Invoke-BSLFlowTask.ps1'
$taskId = 'e450d763-776a-4786-8dbe-6d71a9e25fb4'
$requestFile = Join-Path $env:TEMP ("bsl-flow-request-$taskId.json")

@'
{
  "schema_version": 1,
  "request_id": "e450d763-776a-4786-8dbe-6d71a9e25fb4",
  "prompt": "Add the exact line Example greeting to hello.txt to describe its purpose.",
  "mode": "implement",
  "analysis_goal": "analysis",
  "complexity": "S",
  "risk": "low",
  "impact_flags": [],
  "criteria": [
    {
      "id": "description",
      "kind": "file_assertion",
      "observation": "hello.txt describes its purpose.",
      "path": "hello.txt",
      "contains": "Example greeting"
    }
  ],
  "provenance": {
    "source": "user",
    "reference": "current-user-request",
    "text": "Add the description."
  },
  "models": {
    "worker": "gpt-6-astra",
    "worker_effort": "medium",
    "reviewer": "gpt-6-astra",
    "reviewer_effort": "high"
  },
  "source_paths": ["."],
  "require_spec_review": false,
  "require_code_review": false,
  "max_attempts": 16,
  "timeout_seconds": 1800
}
'@ | Set-Content -LiteralPath $requestFile -Encoding utf8

& $taskCli -Action Start -ProjectPath $projectRoot -InputFile $requestFile
& $taskCli -Action Run -ProjectPath $projectRoot -TaskId $taskId
```

`request_id` должен быть новым canonical lower-case UUID. Повторный `Start` с тем же UUID и эквивалентным каноническим JSON запроса идемпотентен; другой payload под тем же UUID даёт conflict. `Start` требует чистый committed baseline и точный Git root. Контроллер не делает stash и не удаляет пользовательские изменения.

Всегда разбирай JSON envelope. В нём есть `status`, `stage`, `next_stage`, `next_action`, `blockers`, `evidence_refs`, `worker_path` и, после приёмки, `acceptance`. Exit code уточняет класс результата, но для `Status` и `Next` код 0 сам по себе не означает PASS.

### Status

`Status` читает журнал и не запускает модель или тесты:

```powershell
& $taskCli -Action Status -ProjectPath $projectRoot -TaskId $taskId
```

### Update

Ответ на вопрос передаётся отдельным trusted operator event. Возьми актуальные `revision` и `question_id` из envelope:

```powershell
$updateFile = Join-Path $env:TEMP ("bsl-flow-update-$taskId.json")
@'
{
  "schema_version": 1,
  "input_event_id": "6b46f4a5-29db-4424-8844-1461bf2bb177",
  "expected_revision": 4,
  "kind": "clarification",
  "question_id": "04fa4d38-a29e-43f9-958d-e065768f208d",
  "text": "The description belongs at the beginning of hello.txt.",
  "provenance": {
    "source": "user",
    "reference": "answer-to-managed-question",
    "text": "Put it at the beginning."
  }
}
'@ | Set-Content -LiteralPath $updateFile -Encoding utf8

& $taskCli -Action Update -ProjectPath $projectRoot -TaskId $taskId -InputFile $updateFile
```

Для изменения границ задачи используй `kind: scope_change` и передай полный новый `request` с прежним task ID. Для разрешения перехода из анализа к реализации используй `authorization` с `mode: implement`. Worker output не является пользовательским разрешением.

### Resume

После прерывания сначала прочитай status, затем возобнови ту же задачу:

```powershell
& $taskCli -Action Status -ProjectPath $projectRoot -TaskId $taskId
& $taskCli -Action Resume -ProjectPath $projectRoot -TaskId $taskId
```

`Resume` сначала пытается зарегистрировать уже сохранённый terminal result. Если точный процесс ещё работает, второй worker не стартует. Если terminal receipt отсутствует и последствия неизвестны, автоматический retry запрещён: требуется проверить сохранённые raw outputs и фактическое состояние. Для source-only recovery используется `Update` с `kind: recovery`, точным `attempt_id` и свежим `source_sha256`, полученным контрольным чтением полного manifest.

Если законченный этап получил `BLOCKED`, ответ сохраняет конкретную причину в `blockers`; повторный `Run` сам его не повторяет. После устранения причины нужен trusted `Update`. Поле `stage` показывает последний зарегистрированный этап, а `next_stage` — вычисленный следующий этап, например `implement` после изменения уже принятого исходника.

### Cancel

```powershell
& $taskCli -Action Cancel -ProjectPath $projectRoot -TaskId $taskId
```

Cancel запрещает новый dispatch. Прерываемый worker может быть остановлен; непрерываемый native-процесс 1С не завершается принудительно, его неизвестный результат требует control read. Cancel не откатывает уже применившееся внешнее действие. Для продолжения после Cancel нужен явный пользовательский `Update` с `kind: authorization` и `resume: true`, затем `Resume`.

## Локальный реестр задач и настройки окружения

Помимо управляемых задач контроллер ведёт clone-local реестр `planned`-задач: store `<git common dir>/bsl-flow/tasks/<uuid>/revisions` (schema v1) общий для всех worktree клона и не коммитится в Git. Задача создаётся с непустым `-Title` и опциональными `-Description -Priority -Labels -DependsOn`; редактирование — через оптимистичный `-ExpectedRevision`; циклы, self-reference и дубликаты зависимостей отклоняются. Чтение: `-Action List|Show|History|Overview` с фильтрами (`-Status -Priority -Label -Archived false|true|all -Sort -Order -Limit -Cursor`) и форматом `-Format Human|Json` (exit: 0 успех, 2 `BF_INVALID`, 11 `BF_BLOCKED`/`BF_CONFLICT`). Тонкая обёртка `scripts/bsl-flow.ps1` отображает `bsl-flow task <create|edit|list|show|history|overview|archive|unarchive|activate>` на те же действия. `-Action Activate` выполняет полную валидацию trusted request и возвращает staged `BLOCKED` без записи revision: включение активации — отдельный controller write slice.

Настройки совета ревью можно вынести в профиль пользователя `%USERPROFILE%\.bsl-flow\config.yaml` (override `BSL_FLOW_USER_CONFIG`): профиль — база, проект вытесняет его по полям внутри именованных провайдеров, профилей моделей и ролей; незакоммиченный `.bsl-flow/providers.local.yaml` сохраняет высший приоритет для `token`/`base_url`. Профиль допускает только `llm.providers`, `llm.models` и привязки `review.council.roles.<роль>.model`; иной ключ отклоняется fail-closed с именем файла и ключа. Файл не создаётся автоматически.

Описание recovery после финально невалидного chair payload, границы retry и состав dry-run routing preview см. в [описании Council review recovery](COUNCIL_REVIEW_RECOVERY_KNOWN_ISSUES_RU.md).

Для M/L-изменений допустима опциональная тройка машиночитаемых артефактов рядом со `spec.md`: `contract.yaml`, `execution.yaml`, `verification.yaml`. Их проверяет детерминированный линт `Invoke-1CSpecContractLint.ps1` (тот же `-ChangePath`, что у линта `spec.md`); отсутствие артефактов — валидное штатное состояние.

## Где лежат результаты

| Данные | Путь в project root |
|---|---|
| Авторитетный журнал | `.bsl-flow/tasks/<task-id>/revisions/*.json` |
| Производный указатель | `.bsl-flow/tasks/<task-id>/current.json` |
| Входы пользователя | `.bsl-flow/tasks/<task-id>/inputs/` |
| Attempt, raw output и evidence | `.bsl-flow/tasks/<task-id>/attempts/<attempt-id>/` |
| Acceptance receipt | `.bsl-flow/tasks/<task-id>/acceptance/<sha256>.json` |
| Worker checkout | `.bsl-flow/worktrees/<task-id>/` |
| OpenSpec change | `openspec/changes/bsl-flow-<task-id>/` |
| Сырые test reports внутри worker | `.bsl-flow-worker/`; оригинал каждой попытки сохраняется отдельно у контроллера |

`current.json` можно восстановить из hash-linked revisions; он не меняет историю. Acceptance receipt включает exact source manifest, baseline, intent/policy hashes и ссылки на gates. `source_paths` помогает worker искать нужные места, но manifest охватывает весь checkout, который worker мог изменить, включая untracked-файлы и удаления относительно baseline.

## Freshness и типичные отказы

Контроллер повторно вычисляет зависимости перед dispatch, при записи результата и при acceptance. Старый PASS не подходит, если изменились требования, classification, policy, spec, criteria, correction round, исходники или raw evidence.

Примеры:

- после verify вручную изменён файл в worktree — `stale verify evidence at acceptance`;
- изменились `AGENTS.md`, `.ai/model-routing.md`, `bsl-flow.yaml`, OpenSpec config или установленная skill — `policy/package changed`; нужен явный scope update;
- перед разрешённым повтором теста старый report сохраняется у контроллера и удаляется из generated-пути; если новый отчёт не появился, gate получает `BLOCKED` даже при exit 0;
- JUnit отсутствует, malformed, содержит skip/error/failure или другой набор test names — `BLOCKED` либо `FAIL`, без receipt-only PASS;
- read-only/review stage изменил исходники — stage блокируется;
- worker HEAD ушёл с baseline — требуется явное согласование scope;
- classification потребовала integration/UI/external artifact evidence — managed runtime остаётся `BLOCKED` в этой версии.

## Ошибки и диагностика

| Префикс / код | Значение | Что проверить |
|---|---|---|
| `BF_INVALID`, exit 2 | Невалидный контракт или путь | Неизвестные поля, UUID, абсолютный executable, относительные source/report paths, полный request |
| `BF_CONFLICT`, exit 3 | Identity, revision или lock conflict | Актуальный `expected_revision`, уникальный event UUID, активный attempt, другой controller writer |
| `BF_BLOCKED`, exit 11 | Не хватает подтверждения или состояние небезопасно продолжать | `blockers`, policy freshness, raw receipt, host version, runtime boundary |
| `BF_FAIL`, exit 12 | Проверка доказала неверное поведение | Конкретный criterion и сохранённый report |
| exit 13 | Задача отменена | Явный authorization update перед resume |
| exit 4 | Внутренняя ошибка controller | Сохрани envelope и task journal; не создавай дубликат задачи |

Дополнительные частые причины:

- адаптер принимает только подтверждённый native `codex-cli 0.153.0`; другая версия требует отдельной capability-проверки;
- `.codex/config.toml`, `.codex/config.json` или `.codex/hooks.json` на цепочке worker блокируют dispatch;
- static/unit command должен быть существующим native `.exe`, выполняться в sandbox и создать новый оригинальный JUnit по заявленному пути;
- `max_attempts` и общий `timeout_seconds` конечны; их исчерпание не превращается в PASS.

## Выбор модели и стоимость

Поля `models.worker*` и `models.reviewer*` заполняет основной оператор из явного выбора пользователя, проектного `AGENTS.md`/`.ai/model-routing.md` и реально доступных моделей. Они не переписывают глобальную routing policy. Spec review выполняет API-совет (`review.council`); поля reviewer managed-задачи относятся к code review.

Host receipt сохраняет requested model/effort, session ID и usage из завершённого JSONL turn, если провайдер его сообщил. Наблюдавшиеся model/effort могут остаться `null`. Стоимость неизвестна без attributable usage: её нельзя считать нулевой или выводить из названия модели.
