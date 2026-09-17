# Advisory review reconciliation

> Это evidence согласования независимого ревью, а не официальный `review-reconciliation.json` legacy gate.

Итог: все шесть findings приняты; protected direction сохранён.

| Finding | Решение | Отражение в итоговой спецификации |
|---|---|---|
| `ACR-001` | accepted | Добавлен versioned requirement-binding manifest; deterministic gate ограничен структурной доказуемостью, semantic coverage оставлен critics/chair; добавлены negative criteria по IDs/references. |
| `ACR-002` | accepted | Chair определён единственным модельным reconciler и заменяет отдельный managed worker `spec_reconcile`, сохраняя controller sidecar/final gate/route authority. |
| `ACR-003` | accepted | Добавлен immutable prepared-publication package, `prepared/completed` journal protocol, idempotent resume и fail-closed правило для третьего содержимого; требуются interruption fixtures между всеми writes. |
| `ACR-004` | accepted | `current_agent` требует trusted host capability/identity receipt для текущих model/effort, fresh context, no-tools и terminal result; unsupported host даёт `BLOCKED`, Luna substitution запрещена. |
| `ACR-005` | accepted | Model payload отделён от controller envelope; provenance, hashes, usage, status и execution mode строятся только из trusted observations, model claims запрещены schema. |
| `ACR-006` | accepted | Attempt binding расширен до full normalized endpoint и versioned transport-capability mapping; добавлены drift fixtures для scheme/port/base path/version. |

После reconciliation повторное полное модельное ревью не выполнялось: проектная политика требует один независимый criticism pass и точечную reconciliation, а не цикл до согласия. Детерминированный lint запускается на итоговом тексте отдельно.
