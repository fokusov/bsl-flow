# Architecture context and the optional project ADR index

BSL Flow exposes a read-only architecture projection over the controller journal:
`bsl-flow task context --project <path> --task <uuid>`. It reports the
authoritative `Get-BFNext` result, blockers, question, unknown effect, evidence
freshness and the hashes it was generated from. It is not authorization,
acceptance, runtime evidence or a transition authority.

## Optional project ADR index

A project may opt in to project-specific architecture decisions by adding:

- `docs/architecture/adr-index.json` — the machine-readable index;
- `docs/architecture/adr-index.schema.json` — the index schema;
- `docs/ARCHITECTURE_RU.md` — the normative ADR text the index links to
  (anchors must match the `## ADR-N: <title>` headings).

When the project index is present and valid, the stage architecture bundle and
`task context` use it. Without it, the previous behavior is unchanged: the
framework package root is used and an absent index is reported as
`missing_context` rather than failing the task. A present but damaged index is
fail-closed: validation rejects unknown subjects, dangling references,
`supersedes` cycles, missing anchors and schema drift.

## Bootstrap boundary

Project bootstrap does not create, overwrite, delete or otherwise manage
`docs/architecture`. The index is an optional project-owned file; a repeat
bootstrap leaves it byte-for-byte unchanged. Do not add architecture files to a
project automatically, and do not treat the index as authorization to skip
controller gates.
