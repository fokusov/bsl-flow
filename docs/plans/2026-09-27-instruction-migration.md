# Instruction migration evidence

The bootstrap now routes requests; detailed safety policy is read with the relevant skill.

| Previous location / rule | Current location |
| --- | --- |
| Bootstrap: confirmed root, idempotent initialization, schema conflict | `1c-init-project/SKILL.md` Steps 1–3 and Stop; initializer/upgrade validation |
| Bootstrap: exact database authorization | `1c-init-project/SKILL.md` Inputs and Stop; `1c-verify/SKILL.md` Stop; `references/testing-policy.md` Runtime readiness |
| Bootstrap: unknown extension state is not absence; no blind reinstall | `1c-init-project/SKILL.md` Steps 4 and Stop; `references/test-setup.md` Inspect first and Install only what is missing |
| Bootstrap: computer-use needs an observation/reason | `1c-verify/SKILL.md` Inputs and Stop; `references/testing-policy.md` Computer-use boundary |
| Bootstrap: workstation profile and upgrades | `1c-init-project/SKILL.md` Steps 3–6; `references/test-setup.md` |
| Bootstrap: temporary runtime restriction | `1c-verify/references/testing-policy.md`, `1c-init-project/references/test-setup.md`, `1c-task/SKILL.md` Checks |
| Bootstrap: delegated evidence and unknown telemetry | `1c-init-project/SKILL.md` Step 7; `references/agent-audit.md` |
| Bootstrap: EPF/ERF distinct gates and missing evidence | `1c-verify/SKILL.md` Step 4 and Outputs; `references/external-artifacts.md` |
| Repeated managed paragraphs in assisted skills | `1c-task/references/stage-contract.md`, linked by each Managed mode section |
| `1c-implement` execution graph | `1c-implement/references/execution-graph.md`, loaded when execution.yaml exists |
| `1c-task` repository registry paragraph | `1c-task/references/registry.md` |
| `1c-estimate` detailed estimate rules | `1c-estimate/references/estimate-rules.md` |
| `1c-spec-review` providers and run storage | `1c-spec-review/references/review-providers.md` |

New check integration: `1c-spec` records active change and runs grounding; `1c-spec-review` lints and grounds before review; `1c-verify` runs static diff, code grounding and the post-hoc change gate. FAIL/BLOCKED preserves active change for subsequent gated edits.

Structural budgets and link checks establish packaging integrity, not agent behavior. The requested five-task comparison of old/new instruction adherence requires real agent runs; fake benchmark output cannot establish that result. It remains unverified until those runs are authorized and recorded.
