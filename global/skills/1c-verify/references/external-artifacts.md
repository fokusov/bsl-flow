# EPF/ERF evidence profile

Use `external_artifact` when the deliverable is an EPF/ERF or the change affects its modules, forms, schemas or packaging.

Keep these gates distinct:

1. `static`: supported BSL diagnostics. `not_supported` needs an explicit reason and is not native compilation.
2. `native_build`: the platform built the selected XML sources into the exact EPF/ERF.
3. `roundtrip`: the exact artifact was dumped again and the selected descriptor, forms and modules match under documented normalization.
4. `native_load`: the exact SHA-256 artifact loaded/opened in the selected platform and authorized target without native errors.
5. `behavior`: required when the task changes business behavior or migration; loading alone does not prove it.

`/LoadExternalDataProcessorOrReportFromFiles` is native build evidence, not a complete syntax or behavior test. BSL LS coverage depends on its version/rules/source format and does not guarantee detection of undefined output variables. A narrow `Структура.Свойство()` heuristic may warn after a reproduced false-negative, but never replaces `native_load`.

Save one JSON evidence envelope and validate it with:

```powershell
& "<skill-dir>\scripts\Test-ExternalArtifactEvidence.ps1" -EvidencePath .bsl-flow\evidence\external-artifact.json -OutputPath .bsl-flow\reports\external-artifact-current.json
```

Minimal envelope:

```json
{
  "schema_version": 1,
  "artifact": {"type":"epf","path":"C:\\work\\result.epf","sha256":"...","bytes":123},
  "source": {"manifest_sha256":"..."},
  "gates": {
    "static":{"status":"PASS","evidence_path":"..."},
    "native_build":{"status":"PASS","evidence_path":"...","artifact_sha256":"..."},
    "roundtrip":{"status":"PASS","evidence_path":"...","artifact_sha256":"...","source_manifest_sha256":"..."},
    "native_load":{"status":"PASS","evidence_path":"...","artifact_sha256":"...","platform_version":"8.3.x","target":"authorized FILE base"},
    "behavior":{"required":true,"status":"PASS","evidence_path":"...","artifact_sha256":"..."}
  }
}
```

The validator never starts 1C. Missing native build, round-trip or native load is `BLOCKED`; missing required behavior evidence is also `BLOCKED`. Bind any manual observation to the exact artifact hash.
