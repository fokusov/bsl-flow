#Requires -Version 7.0
<#
.SYNOPSIS
Aggregates per-attempt result JSONs written by bench/Invoke-BSLFlowBench.ps1 into a dated
report: bench/results/<date>.json plus a markdown summary, grouped by agent/mode, with a
bare-vs-core comparison table (docs/plans/2026-09-26-remediation-plan.md Ф6.4/Ф6.5).

.PARAMETER RunDir
One or more directories to scan recursively for *.json attempt results (as written by
Invoke-BSLFlowBench.ps1's -OutputDir).

.PARAMETER OutputDir
Where to write <date>.json / <date>.md. Defaults to bench/results next to this script.
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [string[]]$RunDir,
    [string]$OutputDir,
    [string]$Date
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

if ([string]::IsNullOrWhiteSpace($OutputDir)) { $OutputDir = Join-Path $PSScriptRoot 'results' }
if ([string]::IsNullOrWhiteSpace($Date)) { $Date = (Get-Date).ToString('yyyy-MM-dd') }
. (Join-Path $PSScriptRoot 'BenchTask.Common.ps1')

function Get-BenchMean {
    param([Collections.Generic.List[double]]$Values)
    if ($Values.Count -eq 0) { return $null }
    $sum = 0.0
    foreach ($v in $Values) { $sum += $v }
    return [math]::Round($sum / $Values.Count, 4)
}

function Format-BenchPercent {
    param($Value)
    if ($null -eq $Value) { return 'n/a' }
    return "$Value%"
}

$attempts = New-Object Collections.Generic.List[object]
foreach ($dir in $RunDir) {
    if (-not (Test-Path -LiteralPath $dir -PathType Container)) { throw "RunDir not found: $dir" }
    foreach ($file in Get-ChildItem -LiteralPath $dir -Filter '*.json' -File -Recurse) {
        $parsed = Get-Content -Raw -LiteralPath $file.FullName -Encoding UTF8 | ConvertFrom-Json
        $attempts.Add($parsed)
    }
}
if ($attempts.Count -eq 0) { throw "No attempt result JSON files found under: $($RunDir -join ', ')" }

$groups = $attempts | Group-Object -Property agent, mode
$groupSummaries = New-Object Collections.Generic.List[object]

foreach ($group in $groups) {
    $groupAttempts = @($group.Group)
    $agent = $groupAttempts[0].agent
    $mode = $groupAttempts[0].mode
    $statusCounts = @{}
    foreach ($a in $groupAttempts) {
        $status = [string]$a.status
        if ($statusCounts.ContainsKey($status)) { $statusCounts[$status]++ } else { $statusCounts[$status] = 1 }
    }
    $ok = @($groupAttempts | Where-Object { $_.status -eq 'ok' })

    $effPass = New-Object Collections.Generic.List[double]
    $falsePass = New-Object Collections.Generic.List[double]
    $driftLines = New-Object Collections.Generic.List[double]
    $driftMeta = New-Object Collections.Generic.List[double]
    $wallSeconds = New-Object Collections.Generic.List[double]
    $inputTokens = New-Object Collections.Generic.List[double]
    $outputTokens = New-Object Collections.Generic.List[double]
    $costUsd = New-Object Collections.Generic.List[double]
    $notRunTotal = 0
    $skippedRuntimeTotal = 0

    foreach ($a in $ok) {
        $effPass.Add([double][int][bool]$a.effective_pass)
        $falsePass.Add([double][int][bool]$a.false_pass)
        if ($a.drift) {
            $driftLines.Add([double]$a.drift.LineCount)
            $driftMeta.Add([double]$a.drift.NewMetadataCount)
        }
        $wallSeconds.Add([double]$a.wall_seconds)
        if ($a.usage -and $null -ne $a.usage.input_tokens) { $inputTokens.Add([double]$a.usage.input_tokens) }
        if ($a.usage -and $null -ne $a.usage.output_tokens) { $outputTokens.Add([double]$a.usage.output_tokens) }
        if ($null -ne $a.cost_usd) { $costUsd.Add([double]$a.cost_usd) }
        if ($null -ne $a.not_run_count) { $notRunTotal += [int]$a.not_run_count }
        if ($null -ne $a.skipped_runtime_count) { $skippedRuntimeTotal += [int]$a.skipped_runtime_count }
    }

    $groupSummaries.Add([pscustomobject][ordered]@{
            agent                    = $agent
            mode                     = $mode
            total_attempts           = $groupAttempts.Count
            status_counts            = $statusCounts
            ok_attempts              = $ok.Count
            effective_pass_rate      = Get-BenchMean $effPass
            false_pass_rate          = Get-BenchMean $falsePass
            mean_drift_lines         = Get-BenchMean $driftLines
            mean_drift_new_metadata  = Get-BenchMean $driftMeta
            mean_wall_seconds        = Get-BenchMean $wallSeconds
            mean_input_tokens        = Get-BenchMean $inputTokens
            mean_output_tokens       = Get-BenchMean $outputTokens
            mean_cost_usd            = Get-BenchMean $costUsd
            not_run_total            = $notRunTotal
            skipped_runtime_total    = $skippedRuntimeTotal
        })
}

# Rule 1 comparison (bench/DECISION_RULES.md): bare vs core, per agent.
$comparisons = New-Object Collections.Generic.List[object]
$byAgent = $groupSummaries | Group-Object -Property agent
foreach ($agentGroup in $byAgent) {
    $bare = $agentGroup.Group | Where-Object { $_.mode -eq 'bare' } | Select-Object -First 1
    $core = $agentGroup.Group | Where-Object { $_.mode -eq 'core' } | Select-Object -First 1
    if (-not $bare -or -not $core) { continue }
    $isSynthetic = $agentGroup.Name -eq 'fake'
    $evidenceComplete = $bare.ok_attempts -eq $bare.total_attempts -and $core.ok_attempts -eq $core.total_attempts -and
        $bare.not_run_total -eq 0 -and $core.not_run_total -eq 0 -and $bare.skipped_runtime_total -eq 0 -and $core.skipped_runtime_total -eq 0
    $hasFalsePassBaseline = $null -ne $bare.false_pass_rate -and $bare.false_pass_rate -gt 0
    $hasDriftBaseline = $null -ne $bare.mean_drift_lines -and $bare.mean_drift_lines -gt 0
    $hasDecisionBaseline = $hasFalsePassBaseline -or $hasDriftBaseline
    $falsePassCut = if (-not $isSynthetic -and $hasFalsePassBaseline) { [math]::Round((($bare.false_pass_rate - $core.false_pass_rate) / $bare.false_pass_rate) * 100, 1) } else { $null }
    $driftCut = if (-not $isSynthetic -and $hasDriftBaseline) { [math]::Round((($bare.mean_drift_lines - $core.mean_drift_lines) / $bare.mean_drift_lines) * 100, 1) } else { $null }
    $timeIncrease = if (-not $isSynthetic -and $null -ne $bare.mean_wall_seconds -and $bare.mean_wall_seconds -gt 0) { [math]::Round((($core.mean_wall_seconds - $bare.mean_wall_seconds) / $bare.mean_wall_seconds) * 100, 1) } else { $null }
    $decision = if ($isSynthetic) { 'synthetic_not_applicable' } elseif (-not $evidenceComplete -or -not $hasDecisionBaseline -or $null -eq $timeIncrease) { 'not_evaluable' } else { 'evaluated' }
    $keepRule1 = $null
    if ($decision -eq 'evaluated') {
        $keepRule1 = (($null -ne $falsePassCut -and $falsePassCut -ge 20) -or ($null -ne $driftCut -and $driftCut -ge 20)) -and
            ($timeIncrease -le 50)
    }
    $comparisons.Add([pscustomobject][ordered]@{
            agent                        = $agentGroup.Name
            applicability                 = $(if ($isSynthetic) { 'synthetic_not_applicable' } else { 'real' })
            evidence_complete             = $evidenceComplete
            decision                      = $decision
            false_pass_rate_cut_pct      = $falsePassCut
            drift_cut_pct                = $driftCut
            time_increase_pct            = $timeIncrease
            keep_per_rule1                = $keepRule1
        })
}

$report = [pscustomobject][ordered]@{
    schema_version = 1
    date           = $Date
    run_dirs       = $RunDir
    total_attempts = $attempts.Count
    groups         = $groupSummaries.ToArray()
    bare_vs_core   = $comparisons.ToArray()
}

$jsonPath = Join-Path $OutputDir "$Date.json"
Write-BTJsonAtomic $report $jsonPath

$md = New-Object Collections.Generic.List[string]
$md.Add("# Bench results: $Date")
$md.Add('')
$md.Add("Aggregated from: $($RunDir -join ', ') ($($attempts.Count) attempt(s) total).")
$md.Add('')
$md.Add('| Agent | Mode | Attempts | Effective pass | False PASS | Mean drift (lines) | Mean wall (s) | Not run | Skipped (runtime) |')
$md.Add('|---|---|---|---|---|---|---|---|---|')
foreach ($g in $groupSummaries) {
    $md.Add("| $($g.agent) | $($g.mode) | $($g.total_attempts) | $($g.effective_pass_rate) | $($g.false_pass_rate) | $($g.mean_drift_lines) | $($g.mean_wall_seconds) | $($g.not_run_total) | $($g.skipped_runtime_total) |")
}
$md.Add('')
$md.Add('## Bare vs core (Rule 1, bench/DECISION_RULES.md)')
$md.Add('')
if ($comparisons.Count -eq 0) {
    $md.Add('No agent has both a `bare` and a `core` run in this aggregate yet.')
}
else {
    $md.Add('| Agent | Applicability | False-PASS cut | Drift cut | Time increase | Rule 1 decision | Keep per Rule 1 |')
    $md.Add('|---|---|---|---|---|---|---|')
    foreach ($c in $comparisons) {
        $md.Add("| $($c.agent) | $($c.applicability) | $(Format-BenchPercent $c.false_pass_rate_cut_pct) | $(Format-BenchPercent $c.drift_cut_pct) | $(Format-BenchPercent $c.time_increase_pct) | $($c.decision) | $($c.keep_per_rule1) |")
    }
}
$md.Add('')
$md.Add('Tokens/cost means are omitted from this table when the underlying agent runs did not report usage (see bench/README.md); check the JSON report for the raw means where available.')

$mdPath = Join-Path $OutputDir "$Date.md"
$dir = Split-Path -Parent $mdPath
if ($dir -and -not (Test-Path -LiteralPath $dir)) { New-Item -ItemType Directory -Path $dir -Force | Out-Null }
[IO.File]::WriteAllText($mdPath, ($md -join [Environment]::NewLine) + [Environment]::NewLine, [Text.UTF8Encoding]::new($false))

Write-Host "Wrote $jsonPath and $mdPath"
