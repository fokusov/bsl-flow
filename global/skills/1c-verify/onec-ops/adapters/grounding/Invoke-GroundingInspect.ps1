#Requires -Version 7.0
<#
onec-ops metadata.inspect adapter: wraps Get-1CMetadataIndex.ps1 (Ф5.2). The dispatcher only
reaches this script when Test-GroundingAvailable.ps1 found that script, so the guard below is a
defensive fail-closed check, not the primary gate.

Required -Params: source_root (path or array of paths). Reports use a unique cache path.
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$ProjectPath,
    [object]$Params,
    [string]$AdapterDir,
    [string]$Capability,
    [string]$AuthorizationFile,
    [string]$ImportResult
)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Get-GIParam { param($Params, [string]$Name, $Default) $p = $Params.PSObject.Properties[$Name]; if ($null -ne $p -and $null -ne $p.Value) { return $p.Value }; return $Default }

$script = Join-Path $AdapterDir '..\..\..\..\1c-spec-review\scripts\Get-1CMetadataIndex.ps1'
if (-not (Test-Path -LiteralPath $script -PathType Leaf)) { throw 'BF_BLOCKED: Get-1CMetadataIndex.ps1 is not available; metadata.inspect should not have been dispatched to this provider.' }
$script = (Resolve-Path -LiteralPath $script).Path

$sourceRoot = @(Get-GIParam $Params 'source_root' @())
if ($sourceRoot.Count -eq 0) {
    return [pscustomobject]@{ status = 'BLOCKED'; evidence = @(); message = 'BF_BLOCKED: metadata.inspect requires source_root'; target = $null }
}
$outputPath = Join-Path $ProjectPath (".bsl-flow/reports/onec-ops-tmp/grounding-" + [guid]::NewGuid().ToString('N') + '.json')

$sourceRoot = @($sourceRoot | ForEach-Object { if ([IO.Path]::IsPathRooted($_)) { $_ } else { Join-Path $ProjectPath $_ } })
try {
    $rawOutput = & $script -SourceRoot $sourceRoot -CachePath $outputPath
    $exit = 0
} catch {
    return [pscustomobject]@{ status = 'BLOCKED'; evidence = @(); message = "BF_BLOCKED: metadata index failed: $($_.Exception.Message)"; target = ($sourceRoot -join ';') }
}

if (-not (Test-Path -LiteralPath $outputPath -PathType Leaf)) {
    [pscustomobject]@{ status = 'BLOCKED'; evidence = @(); message = 'BF_BLOCKED: metadata index script did not produce an output file'; raw_output = $rawOutput; target = $sourceRoot }
    return
}

[pscustomobject]@{
    status     = if ($exit -eq 0) { 'PASS' } else { 'FAIL' }
    evidence   = @($outputPath.Substring($ProjectPath.Length).TrimStart('\', '/'))
    message    = "Get-1CMetadataIndex exit=$exit"
    raw_output = (Get-Content -Raw -LiteralPath $outputPath -Encoding UTF8)
    target     = ($sourceRoot -join ';')
}
