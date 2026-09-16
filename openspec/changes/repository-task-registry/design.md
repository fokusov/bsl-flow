# Technical design (PS-native v1)

## Store layout
```
<git-common-dir>/bsl-flow/
  tasks/<uuid>/revisions/*.json
  bsl-flow-graph.lock
```
`repository_id` = первые 16 hex SHA-256 нормализованного абсолютного пути git common dir. Файлы `repository.json`, `current.json`, `catalog/index.json`, `migrations/` не создаются в v1.

## Journal format
Каждая задача — immutable hash-linked journal: каждая revision содержит `task_id`, `revision` (hash), `parent_hash`, `payload`. Hash вычисляется как SHA-256 канонической сериализации полей. Первая planned revision содержит metadata: `title`, `description`, `priority`, `labels`, `depends_on`, `archived=false`. Controller state добавляется только после активации, но в v1 активация всегда возвращает staged BLOCKED без записи.

## Commands
Команды доступны как действия контроллера `Invoke-BSLFlowTask.ps1` (`Create, EditRegistry, List, Show, History, Overview, ArchiveTask, UnarchiveTask, Activate`). Предоставляется тонкая обёртка `bsl-flow task <subcommand>` для вызова действий с параметрами, например `bsl-flow task list --project . --status planned,completed`.

## Reads
Все read-команды строят результат напрямую из journals; производные артефакты не используются и не создаются. При отсутствии/повреждении какого-либо journal остальные задачи остаются видимыми; повреждённая задача выводится диагностически.

## Concurrency
- Per-task lock + `expected_revision` для изменений одной задачи.
- Repository graph lock `<store>/bsl-flow-graph.lock` для валидации и публикации `depends_on` изменений.
- Атомарная публикация revision: temporary file → fsync → rename → directory sync; только подтверждённый rename считается authoritative. Отдельный протокол unknown commit reconciliation не вводится; torn journal обнаруживается по hash chain и не создаёт ложную revision.

## Legacy discovery
Legacy `.bsl-flow/tasks` в verified worktree roots обрабатываются read-only. Verified history hash = SHA-256 конкатенации 64-hex revision hashes в порядке возрастания. Совпадение hash дедуплицируется; расхождение показывается как `health=conflict` с `source=legacy`. Запись в canonical store из legacy discovery не выполняется.

## Security
- Пути common dir и task directories проходят no-symlink/no-reparse escape checks.
- List/overview не содержат prompt, raw payload, credentials, provider bodies.
- Show/history используют allowlisted проекции.
- Labels/title/description экранируются в terminal output.
