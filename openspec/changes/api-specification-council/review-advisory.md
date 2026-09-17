# Advisory independent review

> Этот файл сохраняет независимое criticism-only ревью, выполненное вне legacy OpenCode-only gate. Он не является `review.json` и не подменяет официальный review artifact.

- Reviewer route: native Codex subagent
- Requested model/effort: `gpt-6-astra` / `high`
- Observed model/effort: unavailable from a trusted machine-readable receipt
- Verdict before reconciliation: `REVISE`

## Findings

1. `ACR-001` — high, testability. Deterministic gate был заявлен как способ доказать семантическую сохранность естественно-языковых требований, хотя текущие/предлагаемые структурные проверки способны доказать только IDs, hashes, references и полноту множеств.
2. `ACR-002` — high, architecture fit. Council chair и существующий downstream `spec_reconcile` создавали две модельные reconciliation стадии с конфликтующей ответственностью.
3. `ACR-003` — high, missing requirement. Не был определён recoverable protocol публикации нескольких связанных файлов при сбое между записями.
4. `ACR-004` — high, unsupported assumption. Fallback в произвольную модель текущего агента нельзя считать доступным без доверенного host capability/identity contract; скрытая подмена другой моделью недопустима.
5. `ACR-005` — high, clarity/security. Model payload не должен быть источником requested/observed identity, hashes, usage или status, иначе diversity/provenance можно сфабриковать.
6. `ACR-006` — medium, missing requirement. Attempt binding только по endpoint host не обнаруживает drift scheme, port, base path и transport capability mapping.

## Protected direction

- Прямые API providers/models/roles в конфиге; OpenCode не требуется новому default path.
- Fallback — та же текущая host model в отдельном fresh context, иначе `BLOCKED`.
- `brainstorm` выключен по умолчанию, но может быть включён и сделан обязательным.
- Независимые critics, один chair, reconciliation без голосования.
- Одна controller-owned стадия/state machine; без `tasks.md` и без превращения OpenSpec в orchestrator.
- Tokens только через environment или ignored local overlay; advisors не получают tools.
- Unknown cost/effect не равен zero и не разрешает blind retry.
- Legacy v1 остаётся читаемым; Qwen Code и 1С runtime вне scope.
