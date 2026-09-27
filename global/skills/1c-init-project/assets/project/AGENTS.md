# Project instructions

## Project

- Client: <!-- fill when known -->
- Configuration: <!-- e.g. ERP 2.5 / UT 11 / BP 3 -->
- Platform: <!-- fill when known -->
- Delivery form: <!-- extension / external processor / configuration -->

## Source

- Main source path: `./src`
- Development database: `${ONEC_DEV_IB}`
- Test database: `${ONEC_TEST_IB}`

Adjust these values to the real project. Treat placeholders as unconfirmed until you verify them.

<!-- bsl-flow managed:start -->
## BSL Flow task workflow

- Work in assisted mode by default; use the installed `1c-task` entrypoint for a registered managed task.
- Project bootstrap does not prove host isolation, model availability, 1C runtime readiness, or permission for a database operation; confirm each one before relying on it.
- Keep the established project and global model-routing instructions; BSL Flow configuration adds to them.
<!-- bsl-flow managed:end -->

## Development rules

- Route work through the BSL Flow skills: M/L or high risk `1c-spec` -> `1c-spec-review` -> `1c-implement` -> `1c-verify`; S `1c-implement` -> `1c-verify`; defects `1c-debug`.
- Inspect current metadata and source first; prefer existing project mechanisms and extension points.
- Keep the diff minimal and scoped to the task; leave unrelated vendor objects as they are.
- Treat `review.json` as criticism: decide each finding with evidence, apply only accepted ones, then run the final validation.
- Choose tests by behavior with `1c-verify`: unit for logic, integration for data, saved Vanessa features for client flows; use computer-use only for a stated visual reason or an explicit request.
- Run database operations only on an authorized, verified target (prefer a dedicated FILE test copy); after a failed load or write, inspect the actual state before repeating it.
- Record only evidence actually obtained; a disabled provider or missing required test is a BLOCKED gap, and unknown usage stays unknown in the `1c-init-project` agent-audit journal.

## Project-specific context

<!-- Add concrete conventions, build commands, architecture notes and restrictions as they become known. -->
