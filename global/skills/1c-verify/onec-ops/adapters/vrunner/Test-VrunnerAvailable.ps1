#Requires -Version 7.0
param([string]$ProjectPath, [string]$AdapterDir)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'SilentlyContinue'
. (Join-Path $AdapterDir '..\..\OneCOps.Common.ps1')

if (Get-Command 'vrunner' -ErrorAction SilentlyContinue) { 'true'; return }
$yamlText = Get-OOBslFlowYamlText $ProjectPath
$bin = Get-OOYamlValue $yamlText @('onec', 'vrunner', 'bin') $null
if ($bin -and (Test-Path -LiteralPath $bin -PathType Leaf)) { 'true'; return }
$profilePath = Join-Path ([Environment]::GetFolderPath('UserProfile')) '.bsl-flow/workstation.json'
if (Test-Path -LiteralPath $profilePath -PathType Leaf) {
    try {
        $profileObj = Get-Content -Raw -LiteralPath $profilePath -Encoding UTF8 | ConvertFrom-Json -ErrorAction Stop
        $vBin = $profileObj.vrunner_bin
        if (-not [string]::IsNullOrWhiteSpace([string]$vBin) -and (Test-Path -LiteralPath ([string]$vBin) -PathType Leaf)) { 'true'; return }
    }
    catch {}
}
'false'
