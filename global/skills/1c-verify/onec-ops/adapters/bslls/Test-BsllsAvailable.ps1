#Requires -Version 7.0
# Mirrors the discovery order of Find-SDBslLsCommand in
# global/skills/1c-verify/scripts/Invoke-1CStaticDiff.ps1 (kept independent here: this script is
# only asked "is a command discoverable", not asked to run analysis).
param([string]$ProjectPath, [string]$AdapterDir)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'SilentlyContinue'

if (-not [string]::IsNullOrWhiteSpace($env:BSL_FLOW_BSLLS) -and (Test-Path -LiteralPath $env:BSL_FLOW_BSLLS -PathType Leaf)) { 'true'; return }
if (Get-Command 'bsl-language-server' -ErrorAction SilentlyContinue) { 'true'; return }
$profilePath = Join-Path ([Environment]::GetFolderPath('UserProfile')) '.bsl-flow/workstation.json'
if (Test-Path -LiteralPath $profilePath -PathType Leaf) {
    try {
        $profileObj = Get-Content -Raw -LiteralPath $profilePath -Encoding UTF8 | ConvertFrom-Json -ErrorAction Stop
        $bsllsPath = $profileObj.bslls_path
        if (-not [string]::IsNullOrWhiteSpace([string]$bsllsPath) -and (Test-Path -LiteralPath ([string]$bsllsPath) -PathType Leaf)) { 'true'; return }
    }
    catch {}
}
'false'
