# Advisory independent review

> Независимое criticism-only ревью выполнено native Codex subagent (`gpt-6-astra`, requested effort `high`). Это не legacy `review.json` и не подменяет OpenCode-only gate.

Вердикт до reconciliation: `REVISE`.

## Findings

- `NATIVE-01` P1: design ошибочно описывал legacy chain hash как raw-file hash; текущий controller хеширует parsed canonical state.
- `CROSS-01` P1: transition matrix не согласовывала repository v2 activation с запретом legacy writes.
- `REG-04` P1: cross-platform durability contract должен отличать visibility rename от confirmed durable commit.
- `NATIVE-02` P2: native-default и runtime-PowerShell-removal acceptance были слиты, несмотря на explicit compatibility window.

## Do not change

- Поэтапная migration до нуля PowerShell; один writable controller.
- Explicit engine selection без fallback и повторных side effects.
- Windows/macOS/Linux native evidence и Windows-only 1С capability.
- Reconciliation с финальными API-council/architecture-context contracts до implementation.
- Specification-only scope; без `tasks.md` и big-bang rewrite.
