#Requires -Version 7.0
<#
onec-ops static.bslls adapter: wraps global/skills/1c-verify/scripts/Invoke-1CStaticDiff.ps1
(rather than reimplementing the BSL LS baseline/current diff) and maps its verdict onto the
onec-ops/v1 result contract:
  PASS               -> PASS
  FAIL               -> FAIL
  BLOCKED            -> BLOCKED
  NOT_RUN            -> BLOCKED (no analysis is never reported as PASS)

The wrapped script calls `exit`, so it is always run out-of-process (pwsh -File) and its result is
read back from its own JSON report file, never from this process's exit code alone.

Recognised -Params properties (all optional): base_ref, source_path, bsl_ls_command, required
(bool), target.
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

function Get-BASParam { param($Params, [string]$Name, $Default) $p = $Params.PSObject.Properties[$Name]; if ($null -ne $p -and $null -ne $p.Value) { return $p.Value }; return $Default }

$staticDiffScript = Join-Path $AdapterDir '..\..\..\scripts\Invoke-1CStaticDiff.ps1'
if (-not (Test-Path -LiteralPath $staticDiffScript -PathType Leaf)) { throw "BF_INVALID: static-diff gate script is missing: $staticDiffScript" }
$staticDiffScript = (Resolve-Path -LiteralPath $staticDiffScript).Path

$required = [bool](Get-BASParam $Params 'required' $false)
$outputPath = Join-Path $ProjectPath (".bsl-flow/reports/onec-ops-tmp/bslls-" + [guid]::NewGuid().ToString('N') + '.json')

$argv = @('-NoProfile', '-NonInteractive', '-File', $staticDiffScript, '-ProjectPath', $ProjectPath, '-OutputPath', $outputPath)
$baseRef = Get-BASParam $Params 'base_ref' $null
if (-not [string]::IsNullOrWhiteSpace([string]$baseRef)) { $argv += @('-BaseRef', [string]$baseRef) }
$sourcePath = Get-BASParam $Params 'source_path' $null
if (-not [string]::IsNullOrWhiteSpace([string]$sourcePath)) { $argv += @('-SourcePath', [string]$sourcePath) }
$bslLsCommand = Get-BASParam $Params 'bsl_ls_command' $null
if (-not [string]::IsNullOrWhiteSpace([string]$bslLsCommand)) { $argv += @('-BslLsCommand', [string]$bslLsCommand) }
foreach ($pair in @(@('baseline_report', '-BaselineReport'), @('current_report', '-CurrentReport'))) {
    $value = Get-BASParam $Params $pair[0] $null
    if ($value) { $argv += @($pair[1], [string]$value) }
}
if ($required) { $argv += '-Required' }

$pwsh = (Get-Process -Id $PID).Path
$rawOutput = & $pwsh @argv 2>&1 | ForEach-Object { $_.ToString() } | Out-String

if (-not (Test-Path -LiteralPath $outputPath -PathType Leaf)) {
    [pscustomobject]@{
        status     = 'BLOCKED'
        evidence   = @()
        message    = 'BF_BLOCKED: static.bslls wrapper did not produce a report (bslls invocation failed before writing output)'
        raw_output = $rawOutput
        target     = Get-BASParam $Params 'target' $null
    }
    return
}

$verdict = Get-Content -Raw -LiteralPath $outputPath -Encoding UTF8 | ConvertFrom-Json -ErrorAction Stop
$status = switch ($verdict.verdict) {
    'PASS' { 'PASS' }
    'FAIL' { 'FAIL' }
    'BLOCKED' { 'BLOCKED' }
    'NOT_RUN' { 'BLOCKED' }
    default { 'BLOCKED' }
}

[pscustomobject]@{
    status           = $status
    evidence         = @($outputPath.Substring($ProjectPath.Length).TrimStart('\', '/'))
    message          = "bslls verdict=$($verdict.verdict) reason=$($verdict.reason)"
    raw_output       = (Get-Content -Raw -LiteralPath $outputPath -Encoding UTF8)
    target           = Get-BASParam $Params 'target' $null
    provider_version = $verdict.tool.version
}
