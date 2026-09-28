# Execution graph (v0.1)

Moved from `1c-implement/SKILL.md`. Applies only when the change directory contains `execution.yaml` (the optional artifact triple `contract.yaml` / `execution.yaml` / `verification.yaml`). Changes without these artifacts follow the ordinary `1c-implement` steps.

1. Before implementation, run `Invoke-1CSpecContractLint.ps1 -ChangePath <change dir>` (scripts of `1c-spec-review`) and continue only when it passes. Result: a passing contract lint.
2. Dot-source the deterministic helpers [ExecutionGraph.ps1](../scripts/ExecutionGraph.ps1): artifact reading and validation (through the lint), topological order (sequential, no waves; ties broken by task id), permission checks, the evidence writer and the state projection.
3. Walk tasks one at a time in topological order. Kinds `explore|research|review|document` read only; mutating kinds write only inside `allowed_scope` and outside every `forbidden` path.
4. After each task, write `evidence/T-NNN.json` (id, status, observations, touched_files, verify results, violations) with the evidence writer.
5. Mark a task `done` once every V referenced in its `verify[]` has a recorded observable result; otherwise record `blocked` with the reason. Record a scope or mutation violation in the evidence and mark the task `blocked`.
6. Regenerate `state.json` (`schema_version: 1`) from `evidence/` with the projection helper instead of editing it by hand. Commit `evidence/`; keep `state.json` out of commits.

`Test-ExecutionGraphDiscipline.ps1` exercises these helpers offline.
