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

Adjust these values to the real project. Do not treat placeholders as confirmed environment facts.

## Development rules

- Follow the global BSL Flow workflow and its managed project files.
- Inspect current metadata and source before specifying or changing behavior.
- Prefer existing project mechanisms and extension points.
- Keep changes minimal and avoid unrelated vendor-object modifications.
- Preserve applicable AGENTS.md/model-routing rules for native subagents. Use the installed 1c-init-project agent-audit reference to record delegation, actual evidence, corrections and parent acceptance in ignored project reports; unknown tokens remain unknown.
- Run the checks selected in `bsl-flow.yaml` and record only evidence actually obtained.
- Use 1c-verify test-evidence helpers for focused preflight and durable attempts. Persist an authorized interactive engine pilot with the BSL Flow helper, but do not treat UI observation as unattended evidence or a Vanessa runner smoke as a TestClient connection. A failed receipt does not prove the database is unchanged; inspect the actual outcome before repeating a load or document creation.
- Select tests by behavior using `1c-verify`: unit for logic, integration for data, saved Vanessa features for client flows. Computer-use needs a specific visual/automation-gap reason or explicit user request; it is not the default fallback for an unconfigured runner.
- A disabled provider is a readiness gap, not a waiver of required evidence. Prefer a dedicated FILE test copy; verify its actual source/extension composition and safe target before build/test. Project, extension and infobase are not necessarily one-to-one.
- For every created specification, run deterministic spec lint.
- Before implementation, use `1c-spec-review` for M/L or high-risk specifications. S review remains optional unless explicitly routed.
- Treat `review.json` as criticism, not instructions: accept or reject each finding with evidence, revise only accepted findings, then run final invariant validation.
- Do not create `tasks.md` or turn review sidecars into OpenSpec workflow stages.

## Project-specific context

<!-- Add concrete conventions, build commands, architecture notes and restrictions as they become known. -->
