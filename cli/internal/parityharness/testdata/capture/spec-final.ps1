# Freezes the legacy final validation (Test-1CSpecFinal.ps1) over one
# frozen change-directory copy. The validator writes final-validation.json
# into the temporary change copy and throws when validation fails; a
# failing receipt is a capture result, not a capture error. Read-only
# against the repository.
param([Parameter(Mandatory)][string]$ScriptsRoot,[Parameter(Mandatory)][string]$ProjectPath,[Parameter(Mandatory)][string]$ChangeName,[Parameter(Mandatory)][string]$OutPath)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
try {
    & (Join-Path $ScriptsRoot 'Test-1CSpecFinal.ps1') -ProjectPath $ProjectPath -ChangeName $ChangeName | Out-Null
} catch {
    # The artifact is written before the terminal throw; a missing artifact
    # after a throw is a genuine capture failure and rethrows below.
}
$artifact = Join-Path $ProjectPath ("openspec/changes/" + $ChangeName + "/final-validation.json")
if (-not (Test-Path -LiteralPath $artifact -PathType Leaf)) { throw "final-validation.json was not produced: $artifact" }
Copy-Item -LiteralPath $artifact -Destination $OutPath -Force
