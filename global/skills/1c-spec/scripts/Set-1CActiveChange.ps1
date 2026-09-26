#Requires -Version 7.0
# Records the currently active OpenSpec change for a project so any agent
# (Claude Code hooks, Test-1CChangeGate.ps1, other hosts) can find its
# complexity/risk and spec hash without re-parsing every change directory.
# This file is the only writer of .bsl-flow/active-change.json; agents must
# not write it by hand (see PreToolUse-EvidenceGuard.ps1 in the Claude Code
# host and ADR-13).
[CmdletBinding(DefaultParameterSetName = 'Set')]
param(
    [Parameter(Mandatory, ParameterSetName = 'Set')]
    [Parameter(Mandatory, ParameterSetName = 'Clear')]
    [string]$ProjectPath,

    [Parameter(Mandatory, ParameterSetName = 'Set')]
    [string]$ChangeName,

    [Parameter(Mandatory, ParameterSetName = 'Clear')]
    [switch]$Clear
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot '../../1c-spec-review/scripts/Review.Common.ps1')

$projectRoot = [System.IO.Path]::GetFullPath($ProjectPath).TrimEnd('\', '/')
if (-not (Test-Path -LiteralPath $projectRoot -PathType Container)) { throw "Project path not found: $projectRoot" }
$activeChangePath = Join-Path $projectRoot '.bsl-flow/active-change.json'

if ($PSCmdlet.ParameterSetName -eq 'Clear') {
    if (Test-Path -LiteralPath $activeChangePath -PathType Leaf) { Remove-Item -LiteralPath $activeChangePath -Force }
    return
}

if ($ChangeName -notmatch '^[A-Za-z0-9][A-Za-z0-9._-]*$') { throw "Unsafe OpenSpec change name: $ChangeName" }
$changeRoot = Join-Path $projectRoot "openspec/changes/$ChangeName"
$specPath = Join-Path $changeRoot 'spec.md'
if (-not (Test-Path -LiteralPath $specPath -PathType Leaf)) { throw "spec.md not found for change '$ChangeName': $specPath" }

$text = [IO.File]::ReadAllText($specPath, (New-Object Text.UTF8Encoding($false)))

# Same patterns as Test-1CSpec.ps1's Complexity/Risk lint (kept in sync
# deliberately: both read the one classification line a spec is required to
# have exactly once).
$complexityMatches = [regex]::Matches($text, '(?im)^\s*-\s*(?:Сложность|Complexity):\s*(S|M|L)\s*$')
$riskMatches = [regex]::Matches($text, '(?im)^\s*-\s*(?:Риск|Risk):\s*(low|medium|high)\s*$')
if ($complexityMatches.Count -ne 1) { throw "spec.md must contain exactly one Complexity value (S, M, or L) to activate a change: $specPath" }
if ($riskMatches.Count -ne 1) { throw "spec.md must contain exactly one Risk value (low, medium, or high) to activate a change: $specPath" }

$complexity = $complexityMatches[0].Groups[1].Value.ToUpperInvariant()
$risk = $riskMatches[0].Groups[1].Value.ToLowerInvariant()

$result = [ordered]@{
    schema_version = 1
    change         = $ChangeName
    complexity     = $complexity
    risk           = $risk
    spec_sha256    = Get-BSLFlowSha256 $specPath
    set_at_utc     = [DateTime]::UtcNow.ToString('o')
}
Write-BSLFlowJsonAtomic -Value $result -Path $activeChangePath
return [pscustomobject]$result
