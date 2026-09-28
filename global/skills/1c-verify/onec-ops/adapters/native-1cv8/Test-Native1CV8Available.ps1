#Requires -Version 7.0
param([string]$ProjectPath, [string]$AdapterDir)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'SilentlyContinue'
. (Join-Path $AdapterDir 'Native.Common.ps1')

$bin = Resolve-N1PlatformBin -Params ([pscustomobject]@{}) -ProjectPath $ProjectPath
if (-not [string]::IsNullOrWhiteSpace([string]$bin) -and (Test-Path -LiteralPath ([string]$bin) -PathType Leaf)) { 'true' } else { 'false' }
