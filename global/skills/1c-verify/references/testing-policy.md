# Proportionate 1C verification

Read before choosing tests, including for S changes. This is agent decision guidance, not an executable dispatcher or authority to change a base.

## Select by observable behavior

| Behavior to prove | Preferred evidence | What it does not prove |
| --- | --- | --- |
| Calculation, validation, transformation, branching | Existing unit framework, normally YaXUnit; positive and relevant boundary/negative inputs | Forms, actual database lifecycle, external systems |
| Queries, writes, posting, register movements, permissions, transactions | Integration test with minimal known fixtures and assertions on actual state; YaXUnit may run these too | GUI layout or an untested server/DBMS deployment |
| Form command, field interaction, user journey | Existing Vanessa `.feature`, otherwise a minimal saved scenario | Pure logic need not be retested by clicking every combination |
| Layout, clipping, rendered print appearance or an interaction not observable through available automation | Targeted visual/manual or computer-use check with stated observation and scope | Screenshots alone do not prove persisted state or all business rules |
| Syntax and static defects | Configured static checks on changed code | Runtime correctness |

Choose the lowest sufficient level for each criterion, not a mandatory sequence of every row. A change can need several levels for different criteria. Reuse existing tests and stable fixtures before adding a framework or test-only abstraction. Keep test expectations derived from the original task and confirmed rules, not copied from observed implementation output. Preserve tests with source for future regressions.

## Computer-use boundary

Do not use computer-use as the default 1C test runner. Before using it, state the exact criterion and why unit/integration assertions or Vanessa cannot sufficiently observe it. Valid reasons include visual appearance, a demonstrated unsupported interaction, focused exploratory diagnosis, or the user's explicit request. Keep that check bounded and record its limitation; do not manually replay an entire regression suite already covered by sufficient automated evidence.

An unavailable or unconfigured runner is not itself a reason to start an extensive computer-use substitute. Report the missing capability and propose scoped setup or, when useful, an explicitly agreed limited manual check. It remains manual evidence, not an automated test PASS. Respect explicit user tool choices and project constraints; surface conflicting requirements instead of silently changing them. This instruction is not a technical prohibition enforced by tool permissions.

## Runtime readiness and safety

`verification.*.enabled: false` in `bsl-flow.yaml` describes a provider not yet enabled; it does not waive required acceptance evidence or select computer-use. The policy file does not configure Unica or expand environment variables for a runner. Inspect the actual installed public tool contract and project runtime config.

On first use or missing provider, follow the sibling `1c-init-project` [test-setup reference](../../1c-init-project/references/test-setup.md): inspect local catalogs and actual extension state, then install only needed missing components when authorized. Do not rerun a workspace initializer merely to configure a provider. An unknown installed state cannot justify blind installation.

Where Unica is present, use its public `unica.runtime.execute` for preview/applied tests, or its public job route for explicitly asynchronous work. Read the available test-authoring/runtime skill for the requested operation. Do not call internal `unica.build.*`, shell wrappers or a second runner to bypass a refused public operation. Without Unica, use an already configured and authorized project test command, not an invented equivalent.

Preview (`dryRun: true`) is not execution. A real run requires an approved exact test target and understanding of the planned effects: test may build/load/update configuration as well as change data. Reject development/production or baseline targets unless the user has explicitly placed that exact target and operation in scope. Never assume a project path or `${ONEC_TEST_IB}` placeholder proves a safe connection.

Default to a dedicated FILE working copy with a known version and extension composition. Keep baselines untouched; do not reset, restore DT, delete extensions/data or close other sessions without specific authority. Isolate external effects, users, fixtures and reports. Separate work directories or Git branches do not isolate a shared database. Sequential reuse requires known state; concurrent conflicting builds need independent copies. Server testing is required only for relevant server-specific acceptance, not merely because a module runs in server context.

If another build/session already owns the target, do not start a conflicting run or terminate that work without authority. Report the conflict and coordinate serialization or choose an approved independent copy.

## Vanessa: author once, repeat deterministically

Use available Vanessa tools to inspect forms, discover supported steps and debug a narrow scenario. Save the resulting `.feature` in the project's existing test layout, with stable selectors/steps and fixtures. Repeat it using the configured test route; exploratory actions are not the durable regression suite. Inspect actual tool names and versions: do not invent MCP methods or assume last-results belongs to this run. Do not enable blanket `alwaysAllow` or expose runtime tools to the independent spec reviewer.

## Evidence and verdict

For an EPF/ERF deliverable apply the additional [external-artifact profile](external-artifacts.md). Do not collapse native build, round-trip, native load/open and behavior into one green result. A missing required native gate is `BLOCKED`, even when BSL diagnostics are green.

Use the local helpers described in [test-evidence.md](test-evidence.md) to preserve attempts and derive a current result. Never replace runtime evidence with manually asserted readiness. For new test files/profile generation, use [test-starters.md](test-starters.md). A failed post-load/report step needs actual state inspection; continue with a read-only result check after an already completed write rather than repeating the business action.

For each relevant requirement record the selected level, test/command or feature, expected result, actual result, and durable evidence location. For executed suites record the run identity (or unambiguous start/time and target), source/extension versions, expected selection, actual test counts, failures/skips and significant warnings. Do not store secrets or real client data in shared reports or global spec metrics. Copy necessary reports from temporary locations before cleanup when permitted.

Missing, stale, empty or malformed reports, a scope different from the requested tests, or skipped required cases cannot yield PASS. A zero process exit, successful build or a transport-success response alone is insufficient. An unexpected count requires reconciliation before acceptance; an optional explained skip is not automatically a product failure. Distinguish product failure, test/fixture defect, and environment failure.

Use `PASS` only when all required evidence exists; `PASS_WITH_LIMITATIONS` only for explicit non-critical residual risks that do not negate a required criterion; `FAIL` for demonstrated wrong behavior; `BLOCKED` when required evidence cannot be obtained. For M/L keep the compact coverage in the existing change `verification.md`, not another mandatory sidecar. For S the response or existing project report is enough. End once sufficient evidence is obtained; do not add a GUI run merely for reassurance.
