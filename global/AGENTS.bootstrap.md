<!-- bsl-flow bootstrap:start -->
## 1C project bootstrap

When working in a project that appears to be a 1C development project, check whether it is initialized for the global BSL Flow workflow before development begins.

Treat a directory as a 1C project when at least one of these is true:

- it contains exported 1C configuration sources or BSL files;
- it contains recognized 1C project structures such as `src`, `cf`, `cfe`, or `edt` together with project context;
- the user explicitly says this is a 1C project.

Expected bootstrap:

- Git repository initialized at the project root;
- project `AGENTS.md` exists;
- `bsl-flow.yaml` exists;
- `.bsl-flow/project.yaml` sentinel exists;
- `openspec/config.yaml` exists with `schema: bsl-flow`;
- the global `bsl-flow` OpenSpec schema resolves and validates.

If a new or empty directory is explicitly identified by the user as a 1C project, use the global `1c-init-project` skill before development. If an existing directory has reliable 1C indicators and bootstrap components are missing, use the same skill.

The bootstrap must be idempotent. Create only missing managed files, preserve existing project instructions and configuration, and validate the result. Run `git init` and `openspec init --tools none` only at the confirmed project root.

Stop and ask before changing anything when there is a conflict, including an existing different OpenSpec schema, a nested parent Git repository, unclear project-root scope, or files that would need incompatible edits.

Do not bootstrap arbitrary folders, project-container directories, drive roots, or the user profile.

For an initialized bsl-flow project, lint every created specification. Before implementation, use `1c-spec-review` for M/L or high-risk specifications; external review remains optional for S unless explicitly requested or routed by project configuration. A required review may not be silently skipped when OpenCode or the configured model is unavailable.

For 1C testing, select the smallest sufficient evidence per requirement: unit tests for isolated logic, integration assertions for data/posting, saved Vanessa features for user flows. Follow `1c-verify` and its testing policy even for S changes. Computer-use is not the default test runner: first state the criterion that needs visual/manual observation or the explicit user request. Unconfigured test tools do not waive required evidence or justify an automatic click-through fallback. Do not repeat already sufficient automated checks with GUI actions. Missing required tests remain an explicit limitation/blocker, never a claimed PASS.
On first 1C project/test setup, use `1c-init-project` test-setup guidance: inspect local YAxUnit/Vanessa artifacts and actual installed extension state, then perform only missing, needed, authorized installation. A local file or project sentinel does not prove database readiness. Never use an extension-property mutation as a read-only inventory check or treat unknown installation state as absent.
For an older initialized project, run the packaged comment-preserving project upgrade and inspect its deterministic plan; do not preserve a stale sentinel while silently omitting new managed keys. A workstation profile may authorize an explicitly allowlisted FILE development base as its test target, but never authorizes another base, stores credentials, replaces unknown/newer extensions, or lifts a temporary runtime restriction.

For delegated 1C project work, preserve the applicable AGENTS.md and .ai/model-routing.md policy; bsl-flow does not replace model selection or require delegation. Follow the installed `1c-init-project/references/agent-audit.md` helpers: record delegation when it starts, model/effort as requested versus observed, problems/corrections and parent acceptance in ignored `.bsl-flow/reports/subagents`. Collect usage only from attributable evidence; unknown is null, not zero. Do not log private prompts or reasoning. Subagent completion is not integration acceptance, and a telemetry gap is not a product failure or fabricated success.

Before the selected runtime test, follow `1c-verify/references/test-evidence.md` for a focused preflight and durable attempt result. When an explicitly authorized interactive pilot is the only supported route, save its exact selection/counts through the packaged interactive-pilot helper; keep this evidence distinct from unattended/durable readiness and from a Vanessa TestClient connection. A module filter does not constrain build scope. Preserve history and actual post-failure database state; never repeat a load or business write merely because the receipt failed. Use the test-starters reference for new YAxUnit/Vanessa scaffolds instead of rebuilding runner parameters from memory.
For EPF/ERF deliverables, also apply the `external_artifact` profile: static diagnostics, native build, round-trip, native load/open of the exact SHA-256 artifact, and task-specific behavior are distinct gates. Missing required native or behavior evidence is BLOCKED.
<!-- bsl-flow bootstrap:end -->
