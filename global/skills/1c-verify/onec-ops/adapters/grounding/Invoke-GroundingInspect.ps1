#Requires -Version 7.0
<#
onec-ops metadata.inspect adapter: wraps Get-1CMetadataIndex.ps1 (Ф5.2). The dispatcher only
reaches this script when Test-GroundingAvailable.ps1 found that script, so the guard below is a
defensive fail-closed check, not the primary gate.

Recognised -Params properties (all optional): source_root, output_path.
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

$sourceRoot = Get-GIParam $Params 'source_root' $null
$outputPath = Join-Path $ProjectPath (".bsl-flow/reports/onec-ops-tmp/grounding-" + [guid]::NewGuid().ToString('N') + '.json')

$argv = @('-NoProfile', '-NonInteractive', '-File', $script, '-ProjectPath', $ProjectPath, '-OutputPath', $outputPath)
if (-not [string]::IsNullOrWhiteSpace([string]$sourceRoot)) { $argv += @('-SourceRoot', [string]$sourceRoot) }

$pwsh = (Get-Process -Id $PID).Path
$rawOutput = & $pwsh @argv 2>&1 | ForEach-Object { $_.ToString() } | Out-String
$exit = $LASTEXITCODE

if (-not (Test-Path -LiteralPath $outputPath -PathType Leaf)) {
    [pscustomobject]@{ status = 'BLOCKED'; evidence = @(); message = 'BF_BLOCKED: metadata index script did not produce an output file'; raw_output = $rawOutput; target = $sourceRoot }
    return
}

[pscustomobject]@{
    status     = if ($exit -eq 0) { 'PASS' } else { 'FAIL' }
    evidence   = @($outputPath.Substring($ProjectPath.Length).TrimStart('\', '/'))
    message    = "Get-1CMetadataIndex exit=$exit"
    raw_output = (Get-Content -Raw -LiteralPath $outputPath -Encoding UTF8)
    target     = $sourceRoot
}
