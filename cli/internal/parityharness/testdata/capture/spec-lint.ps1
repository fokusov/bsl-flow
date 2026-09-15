# Freezes the legacy PowerShell spec lint (Test-1CSpec.ps1) over one frozen
# spec.md. Read-only against the repository: the change directory and the
# lint artifact live in caller-provided temporary paths. Provenance of the
# committed traces: pwsh -File spec-lint.ps1 -ScriptsRoot <repo>/global/
# skills/1c-spec-review/scripts -ChangePath <temp change> -OutPath <temp>.
param([Parameter(Mandatory)][string]$ScriptsRoot,[Parameter(Mandatory)][string]$ChangePath,[Parameter(Mandatory)][string]$OutPath)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
& (Join-Path $ScriptsRoot 'Test-1CSpec.ps1') -ChangePath $ChangePath -OutputPath $OutPath -NoThrow | Out-Null
