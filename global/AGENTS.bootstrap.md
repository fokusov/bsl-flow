<!-- bsl-flow bootstrap:start -->
## BSL Flow (1C projects only)
Apply only in a 1C:Enterprise project: it has `Configuration.xml`, `*.mdo`, `*.bsl`, `src/cf|cfe|epf`, or the user says so. Otherwise ignore this block.
1. If `bsl-flow.yaml` or `openspec/config.yaml` (schema: bsl-flow) is missing, use `1c-init-project`.
2. Size the task S/M/L and risk. S: `1c-implement` -> `1c-verify`. M/L or high risk: `1c-spec` -> `1c-spec-review` -> `1c-implement` -> `1c-verify`.
3. Bugs: `1c-debug`. A managed task only on explicit request: `1c-task`.
4. Report PASS / PASS_WITH_LIMITATIONS / FAIL / BLOCKED with the evidence you actually observed. A required review stays required when the configured reviewer is unavailable; report that, and a missing required test, as BLOCKED.
<!-- bsl-flow bootstrap:end -->
