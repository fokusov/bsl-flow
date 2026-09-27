#Requires -Version 7.0
# metadata.inspect wraps global/skills/1c-spec-review/scripts/Get-1CMetadataIndex.ps1 (Ф5.2 in the
# remediation plan). At the time this adapter was written that script did not exist yet in this
# worktree (it is built by a separate parallel agent), so detect fails closed until it lands -
# there is no code path here that fabricates a metadata index. Re-run the onec-ops contract suite
# after Ф5.2 merges; no change to this adapter should be needed once the script exists at the path
# below with a compatible CLI (-ProjectPath, -SourceRoot, JSON to stdout or -OutputPath).
param([string]$ProjectPath, [string]$AdapterDir)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'SilentlyContinue'

$script = Join-Path $AdapterDir '..\..\..\..\1c-spec-review\scripts\Get-1CMetadataIndex.ps1'
if (Test-Path -LiteralPath $script -PathType Leaf) { 'true'; return }
[Console]::Error.WriteLine('onec-ops: grounding adapter has no Get-1CMetadataIndex.ps1 to wrap yet (Ф5.2 not landed); metadata.inspect is unavailable.')
'false'
