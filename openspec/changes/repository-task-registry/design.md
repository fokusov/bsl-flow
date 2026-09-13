# Technical design

## Решение

Добавить clone-local `Repository Task Store` под абсолютным Git common dir и использовать его как canonical location для всех новых task journals. Формат остаётся file-based и append-only; новая база данных и background service не нужны.

```text
any repository worktree
        ↓ git rev-parse --git-common-dir / --show-toplevel
verified repository identity
        ↓
<git-common-dir>/bsl-flow/
  repository.json
  tasks/<uuid>/revisions/*.json
  tasks/<uuid>/current.json          # derived
  catalog/index.json                 # derived, rebuildable
  migrations/*.json                 # immutable receipts
        ↓
Go-native task commands / existing execution controller
```

`repository.json` содержит schema version и random clone ID, созданный atomic create под repository lock. Remote URL не является identity: он может отсутствовать или меняться, а два клона одного remote должны иметь разные каталоги.

## Unified task journal

Task schema v2 добавляет стабильную карточку:

- `task_id`, `title`, `description`, `priority`, `labels`, `depends_on`;
- `lifecycle=planned|controller` и `archived`;
- `created_at`, `updated_at`, author/source provenance без PII по умолчанию;
- optional controller state, появляющийся только после activation.

Первая planned revision не содержит фиктивных `stage`, `baseline`, `worker_path` и authorization. Activation append-ит controller payload, сохраняя identity/card. Existing controller functions получают task location через repository resolver, а не конструируют `.bsl-flow/tasks` из текущего worktree.

Для v1 совместимости reader различает schema branch. V1 state не переписывается. После adoption его bytes сохраняются в immutable legacy segment/receipt, migration receipt связывает verified source prefix с canonical continuation, а следующая v2 revision использует документированный legacy-canonical state hash и указывает source location.

## Repository и worktree boundary

Resolver выполняет Git через argv без shell, получает `--git-common-dir` и список worktree в porcelain format, канонизирует абсолютные paths и отклоняет common dir вне ожидаемой Git структуры, symlink/reparse escape и замену identity во время операции.

Task может хранить `origin_worktree` и текущий execution binding, но catalog принадлежит clone ID. Удаление worktree не удаляет task. Activation в другом worktree требует явного выбора, обычной baseline/cleanliness проверки и native controller capability для repository schema/store. Legacy engine не активирует и не пишет такие задачи.

## Команды и read model

- `task create --project ... --input planned.json`
- `task edit --task ... --expected-revision ... --input metadata-patch.json`
- `task activate --task ... --expected-revision ... --input request.json`
- `task list [filters] [--json]`
- `task show --task ... [--json]`
- `task history --task ... [--json]`
- `task overview [filters] [--json]`
- `task archive|unarchive --task ... --expected-revision ...`
- `task registry adopt --source <worktree>|--all --preview|--apply`

JSON schemas разделены на write inputs и read envelopes. Cursor содержит version, repository ID, filter/sort hash и last stable key; изменение filters/repository делает cursor invalid. Default sort: `updated_at desc`, затем UUID ordinal.

List и overview строятся из verified terminal states журналов на каждом чтении. Производный `catalog/index.json` не является обязательным и никогда не читается как источник истины; если он сохраняется, то содержит только безопасные projection fields и source revision/hash и полностью восстанавливается из journals. Corrupt task получает diagnostic projection из безопасно известных directory/source facts.

## Dependencies

Graph ограничен одним repository ID. Любое изменение `depends_on` удерживает repository graph lock от чтения verified graph generation через cycle validation до durable публикации task revision; допустим эквивалентный generation-CAS с повторной проверкой. Per-task lock остаётся нужен для expected revision. `depends_on` — metadata; pre-dispatch freshness gate, привязка dependency revisions/hashes к attempt и dispatch-drift detection в этой версии не реализуются и отложены в спецификацию native controller write slice.

## Concurrency и recovery

Repository lock защищает создание identity, task directory и публикацию projection. Per-task lock плюс `expected_revision` сериализует изменения одной задачи. Атомарная публикация revision выполняется существующим `AtomicWrite` (уникальный temporary file → fsync файла → rename → directory sync); только подтверждённый rename считается authoritative revision. Отдельный протокол `commit_outcome=unknown` и удержание неизвестных temporary paths не вводятся. Повреждённый или torn journal обнаруживается чтением chain (§15) и не создаёт ложную revision; повторный запуск не публикует следующую revision вслепую.

## Legacy discovery

Discovery перечисляет `.bsl-flow/tasks` в verified worktree roots только для чтения; bytes legacy journals не изменяются. Catalog merge использует `(task_id, verified prefix/hash)`: одинаковые histories дедуплицируются, неподтверждённая divergence показывается как conflict, canonical repository journal всегда выигрывает. Adoption (portable artifact manifest, immutable segment, lineage receipts, routing manifest) в эту версию не входит и отложен в отдельную спецификацию; command `adopt` не выполняет запись в canonical store.

## Security и privacy

- Common dir и every task path проходят no-symlink/no-reparse escape checks.
- Index/list не содержат prompt, raw event payload, provider body, credentials или runtime auth.
- `show` использует allowlisted projection, а не generic JSON dump state.
- Labels/title/description считаются untrusted display data и экранируются в terminal output.
- Registry не добавляется в Git index и не отправляется remote.

## Альтернативы

- Markdown-файл на задачу — отклонено: дублирует controller journal и теряет проверяемую recovery semantics.
- SQLite — отклонено: добавляет второй transactional store и migration/locking complexity без доказанной нагрузки.
- Store в каждом worktree — отклонено: не даёт проектного списка и теряет completed history при удалении worktree.
- Глобальный user-level store — отклонено: сложнее переносимость/изоляция и repository identity; clone-local common dir достаточен.
- Автоматический move legacy tasks — отклонено: риск потери/подмены истории и конфликта с работающим controller.

## Риски

| Риск | Снижение |
|---|---|
| Git common dir подменён или содержит unsafe link | Canonical Git discovery, path validation, identity recheck under lock |
| Catalog расходится с task truth | Derived cache с revision/hash; rebuild from journals |
| Удаление worktree теряет старую задачу | New canonical common store и explicit legacy adoption |
| Corrupt task скрывает остальные | Per-task health entries и partial catalog response |
| Dependency меняется при dispatch | Recheck/bind before attempt; no attempt before stable admission |
| Concurrent edges создают цикл | Repository graph lock/generation-CAS covers validation plus revision commit |
| Adoption превращается в ложный conflict | Verified prefix-to-continuation lineage receipt |
| Completed history ссылается на удалённый worktree | Portable artifact manifest; historical status separate from current freshness |
| Rename видим, но durability не подтверждена | Directory sync and explicit unknown commit reconciliation |
| Metadata раскрывает prompt/evidence | Allowlisted projections and negative secret tests |
| V2 ломает v1 recovery | Dual reader, immutable fixtures, no automatic rewrite |

## Стратегия проверки

Golden fixtures покрывают v1/v2 canonical JSON, hash chains и redaction. Temporary Git repositories создают main plus two worktrees и отдельный clone. Fault injection прерывает каждый durability step, включая rename→directory-sync uncertainty. Concurrency tests проверяют expected-revision conflicts, встречные dependency edges и dispatch drift. Adoption tests сохраняют source bytes plus portable artifact manifest, выполняют canonical continuation/repeated discovery, удаляют worktree и различают historical completed от current freshness. JSON schemas и human output сравниваются по membership/order, но human formatting не является API.
