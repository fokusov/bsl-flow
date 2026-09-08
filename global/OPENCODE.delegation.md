<!-- bsl-flow opencode:start -->
## OpenCode delegation boundary

When BSL Flow is used directly from OpenCode, preserve the effective global and project OpenCode agent configuration. The framework installs workflow skills but does not choose or rewrite primary-agent, subagent, provider, model, variant, or reasoning settings.

Use only subagents actually exposed by the current OpenCode configuration and route work by their declared descriptions and permissions. The primary agent owns requirements, integration and final acceptance. Delegate only bounded independent work; use a read-only agent for independent implementation review. Do not invent an agent name or claim heterogeneous model routing when the effective configuration does not prove it.

The `1c-spec-review` wrapper is a separate boundary: for required M/L/high-risk specification review it invokes the packaged read-only reviewer and the model configured in project `bsl-flow.yaml`. A general OpenCode review subagent does not replace this evidence-producing review.

When delegation is used in a 1C project, write the normal bsl-flow subagent audit. Record requested and observed agent/model/effort only from attributable evidence; unavailable values remain null. A configured routing table proves configuration, not that a live agent selected the right delegate. Evaluate that from a concrete run, its artifacts, corrections and parent acceptance.

For L/high-risk work, after review reconciliation and final invariant validation, prefer a fresh implementation context when the current session is already large or has waited on a long external review. Carry forward only `original-task.md`, the final `spec.md`, `review-reconciliation.json`, `final-validation.json`, relevant source paths, and the verification intent. This is an optional host-level handoff, not a requirement for S/M work and not an orchestration feature promised by BSL Flow.
<!-- bsl-flow opencode:end -->
