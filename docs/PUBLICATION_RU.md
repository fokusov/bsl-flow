# Управляемая публикация принятых исходников

`task deliver` создаёт локальный handoff. `task publish` отдельно разрешает создание одной новой Git-ветки из точного принятого manifest. Worker не получает права commit/push и не выбирает место публикации. Обычный commit сохраняет родителя `acceptance.baseline`; копирование файлов через `git add` не применяется, поэтому filters и преобразование окончаний строк не меняют принятые байты.

## Явный допуск

Публикация требует отдельного JSON. Его `task_id` и `acceptance_sha256` берутся из принятой задачи; `publication_id` — новый UUID. Пример профиля GitHub:

```json
{
  "schema_version": 1,
  "publication_id": "8e6d5780-9a76-43aa-9454-e62e080ab329",
  "task_id": "01234567-89ab-4cde-8123-0123456789ab",
  "acceptance_sha256": "REPLACE_WITH_EXACT_ACCEPTANCE_SHA256",
  "remote": "https://github.com/OWNER/REPOSITORY.git",
  "ref": "refs/heads/codex/accepted-change",
  "auth": "github_cli",
  "author": {"name": "YOUR_NAME", "email": "YOUR_EMAIL"},
  "message": "Implement the accepted change",
  "provenance": {
    "source": "user",
    "reference": "REFERENCE_TO_YOUR_AUTHORIZATION",
    "text": "Authorize creation of this exact new branch from the named acceptance."
  }
}
```

Замени placeholders фактическими значениями. Другой поддержанный профиль — абсолютный путь к существующему локальному bare repository с `auth: "none"`. Первый сетевой профиль ограничен точным `https://github.com/OWNER/REPOSITORY.git`, без URL credentials, query, redirects и произвольных transport/helper-команд. Авторизация операции и наличие GitHub credentials — разные условия: профиль использует существующую машинную установку GitHub CLI; controller не устанавливает её и не выполняет интерактивный login.

```powershell
& "$env:USERPROFILE\.agents\skills\1c-task\scripts\Invoke-BSLFlowTask.ps1" -Action Publish -ProjectPath C:\DEV\Example -TaskId <task-uuid> -InputFile C:\Tasks\publication.json
& "$env:USERPROFILE\.agents\skills\1c-task\scripts\Invoke-BSLFlowTask.ps1" -Action PublishResume -ProjectPath C:\DEV\Example -TaskId <task-uuid> -InputFile C:\Tasks\publication.json
```

Второе действие выполняет только контрольное чтение сохранённой публикации. Изменение JSON при том же UUID отклоняется. Другая приёмка, remote, ветка или сообщение требуют нового допуска и UUID; это не снимает уже существующую блокировку неизвестной операции.

## Что проверяет controller

Перед первой отправкой нужны текущий implementation PASS, неизменённые исходники, все свежие gates и coverage binding, если задача использует requirements. Controller повторно проверяет локальный handoff. Он импортирует точный baseline в собственное Git-хранилище, создаёт blobs из raw bytes, проверяет конечное дерево и сохраняет commit OID до отправки.

Журналы и receipts остаются внутри задачи. Из-за фактически проверенного ограничения Git for Windows на длинный путь repository его objects хранятся в коротком controller-owned каталоге `%LOCALAPPDATA%\BSLFlow\publication-objects\<hash>`. Hash связывает точный staging-каталог задачи; произвольный путь repository не принимается. Повтор до dispatch создаёт новое хранилище и заново проверяет commit по принятым байтам.

Обычные baseline-файлы сохраняют mode `100644`/`100755`, новые получают `100644`. Symlinks и submodules не входят в этот профиль. Manifest исходников доказывает байты и набор файлов; mode policy фиксируется отдельно в подготовленной публикации. Исключённые из source manifest административные каталоги не добавляются в новый commit.

У Git свой ограниченный process runner с проверяемой машинной установкой и отдельным cwd. Наследуемые Git-конфигурация, credentials helpers, hooks и environment overrides не являются разрешением. Для GitHub используется фиксированный helper GitHub CLI; пароль или token не входят в JSON, argv controller, worker prompt и publication receipts. Версии и hashes исполняемых зависимостей фиксируются отдельно.

Новая ветка должна отсутствовать. Проверка выполняется до push и повторно на стороне Git через create-only lease. Наличие ветки отклоняется даже при совпадении OID. Операция не обновляет существующие branches/tags, не создаёт PR и не выполняет merge.

## Прерывание и восстановление

Журнал публикации хранится в `.bsl-flow/tasks/<task>/publications/<publication>/`. Общая блокировка пары remote/ref действует в управляемом контуре одного пользователя и не позволяет другой задаче обойти неизвестную операцию новым UUID.

До запуска Git сохраняются pending и intent. После появления intent повторный вход не отправляет commit заново. Controller читает точный remote ref: ожидаемый OID и подтверждённое завершение исходного процесса позволяют сохранить `published.json`, затем снять pending. Другой OID означает конфликт; отсутствие ref, ошибка чтения или неизвестная identity процесса сохраняют BLOCKED. Timeout не равен откату удалённого push.

Сначала записывается durable receipt, затем снимается pending. Сбой между этими действиями завершается идемпотентным восстановлением. Предыдущие ошибки и наблюдения сохраняются. Recovery не требует повторного выполнения тестов или model worker и не превращает изменённые позже исходники в новую acceptance.

## Границы результата

`published` подтверждает появление точного commit в указанной ветке. Это не результат CI, одобрение PR или установка в 1С. Production rollout, резервное копирование, обновление базы и rollback требуют отдельного контракта конкретной среды. Отправка в Git не снимает временное ограничение Unica runtime jobs.

Состояние реализации и фактически выполненные проверки этого development increment указаны в [плане завершения](SDLC_COMPLETION_RU.md) и [отчёте проверки](../VERIFICATION.md).
