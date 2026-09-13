# Benchmark toolset snapshots

`New-BSLFlowToolsetSnapshot.ps1` makes an offline, immutable-by-check snapshot of an explicitly selected local skill root. It exists to make benchmark inputs reviewable; it neither installs skills nor starts MCP tools, models, network calls, databases, or 1C processes.

Use PowerShell 7 and select the source deliberately. The locations below are observed workstation examples, not a version-selection rule or an assertion that a plugin is installed:

```powershell
pwsh .\scripts\New-BSLFlowToolsetSnapshot.ps1 `
  -ToolsetName cc-1c-skills `
  -SourceRoot 'C:\Users\ifokusov\.config\opencode\skills' `
  -OutputDirectory 'D:\benchmark-inputs\cc-1c-skills-001'

pwsh .\scripts\New-BSLFlowToolsetSnapshot.ps1 `
  -ToolsetName unica `
  -SourceRoot 'C:\Users\ifokusov\.codex\plugins\cache\unica\unica\0.12.3\skills' `
  -OutputDirectory 'D:\benchmark-inputs\unica-001'
```

The output directory must be new, its parent must already exist, and it cannot be inside the source root. The command discovers only direct child directories containing `SKILL.md`, then copies every ordinary file beneath each selected skill, including `scripts/`. It rejects reparse points, unsafe paths, and clearly named `.env`, authentication, credential, secret, and private-key assets. A snapshot contains `toolset-manifest.json` with sorted relative file paths, per-file SHA-256 values, each skill hash, and an aggregate SHA-256.

Verify an existing snapshot before using it:

```powershell
pwsh .\scripts\New-BSLFlowToolsetSnapshot.ps1 `
  -ToolsetName unica `
  -SourceRoot 'C:\Users\ifokusov\.codex\plugins\cache\unica\unica\0.12.3\skills' `
  -OutputDirectory 'D:\benchmark-inputs\unica-001' `
  -Verify
```

Verification uses the manifest as a strict schema: it rejects changed, missing, extra, unsafe, or reparse-point files and rechecks all hashes. The manifest records the original local path as private provenance. Do not attach it to a public benchmark export without first removing or replacing that field through a separately reviewed export process.

The manifest also inventories literal `mcp__...` and `unica.*` names found in text files and lists the files that mention them. This is evidence of documented dependency references only. It does not prove an MCP server is installed, configured, executable, reachable, or authorized for a host integration.

This is a source-input snapshot utility, not a sandbox, a secret scanner, an integration test, or a complete benchmark runner. It does not copy arbitrary configuration from outside selected skill trees and it does not make a snapshot safe to execute without the benchmark's separate environment and authorization controls.
