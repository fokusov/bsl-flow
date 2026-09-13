# Advisory independent review

> Независимое criticism-only ревью выполнено native Codex subagent (`gpt-6-astra`, requested effort `high`). Это не legacy `review.json` и не подменяет OpenCode-only gate.

Вердикт до reconciliation: `REVISE`.

## Findings

- `CROSS-01` P1: repository v2 activation была разрешена раньше native controller, хотя legacy engine не умеет/не должен писать новый store.
- `REG-01` P1: per-task locks допускали concurrent cycle `A→B` + `B→A`.
- `REG-02` P1: adopted source после canonical continuation ошибочно становился конфликтующей history.
- `REG-03` P1: adoption journals без immutable artifacts/resolve rules не сохранял пригодность history после удаления worktree.
- `REG-04` P1: atomic rename был ошибочно приравнен к подтверждённой crash durability без directory sync/unknown outcome.

## Do not change

- Clone-local Git common-dir scope и независимость разных clones.
- Один immutable journal и derived projections.
- Planned lifecycle без фиктивной authorization; archive только presentation flag.
- Explicit adoption без удаления originals; completed history не очищается автоматически.
- Allowlisted outputs без raw prompts/evidence; specification-only scope.
