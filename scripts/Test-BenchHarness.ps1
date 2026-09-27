#Requires -Version 7.0
<#
.SYNOPSIS
Offline contract test for the public BSL Flow benchmark harness.

.DESCRIPTION
Runs only the scripted fake agent.  It proves that the runner distinguishes a satisfying
attempt, a claimed-but-failed attempt, and a satisfying attempt with scope drift; it never
starts a model CLI, a network operation, or a 1C runtime.
#>
[CmdletBinding()]
param([string]$PackageRoot = (Split-Path -Parent $PSScriptRoot))

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$PackageRoot = [IO.Path]::GetFullPath($PackageRoot)
$runner = Join-Path $PackageRoot 'bench\Invoke-BSLFlowBench.ps1'
$aggregator = Join-Path $PackageRoot 'bench\Measure-BenchResults.ps1'
foreach ($path in @($runner, $aggregator, (Join-Path $PackageRoot 'bench\agents\fake-agent.ps1'))) {
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { throw "Benchmark harness file missing: $path" }
}

function Assert-True {
    param([bool]$Condition, [string]$Message)
    if (-not $Condition) { throw "ASSERT: $Message" }
}

function Remove-BenchHarnessTestRoot {
    param([Parameter(Mandatory)][string]$Path)
    $fullPath = [IO.Path]::GetFullPath($Path)
    $tempRoot = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\', '/') + [IO.Path]::DirectorySeparatorChar
    if (-not $fullPath.StartsWith($tempRoot, [StringComparison]::OrdinalIgnoreCase) -or
        -not ([IO.Path]::GetFileName($fullPath).StartsWith('bslflow-bench-harness-test-', [StringComparison]::OrdinalIgnoreCase))) {
        throw "Unsafe benchmark test cleanup target: $fullPath"
    }
    if (Test-Path -LiteralPath $fullPath -PathType Container) { Remove-Item -LiteralPath $fullPath -Recurse -Force }
}

$testRoot = Join-Path ([IO.Path]::GetTempPath()) "bslflow-bench-harness-test-$([guid]::NewGuid().ToString('N'))"
$oldVariant = $env:BENCH_FAKE_VARIANT
try {
    New-Item -ItemType Directory -Path $testRoot -Force | Out-Null
    $runs = @{}
    foreach ($variant in @('good', 'bad', 'drift')) {
        $runDir = Join-Path $testRoot $variant
        $env:BENCH_FAKE_VARIANT = $variant
        & pwsh -NoProfile -File $runner -Agent fake -Mode bare -Tasks 'bench/tasks/s-print-form' -Repeat 1 -OutputDir $runDir -TimeoutSeconds 30 -RepoRoot $PackageRoot
        Assert-True ($LASTEXITCODE -eq 0) "Runner failed for fake variant '$variant'."
        $results = @(Get-ChildItem -LiteralPath $runDir -Filter '*.json' -File | ForEach-Object { Get-Content -Raw -LiteralPath $_.FullName -Encoding UTF8 | ConvertFrom-Json })
        Assert-True ($results.Count -eq 1) "Variant '$variant' did not emit one result for the selected offline fixture task."
        $runs[$variant] = $results
    }
    $coreRunDir = Join-Path $testRoot 'core-good'
    $env:BENCH_FAKE_VARIANT = 'good'
    & pwsh -NoProfile -File $runner -Agent fake -Mode core -Tasks 'bench/tasks/s-print-form' -Repeat 1 -OutputDir $coreRunDir -TimeoutSeconds 30 -RepoRoot $PackageRoot
    Assert-True ($LASTEXITCODE -eq 0) 'Runner failed for fake core variant.'

    $good = @($runs['good'])
    Assert-True -Condition (@($good | Where-Object { -not $_.acceptance_pass }).Count -eq 0) -Message 'Good fake variant did not satisfy all executed offline checks.'
    Assert-True -Condition (@($good | Where-Object { $_.acceptance_complete }).Count -eq 0) -Message 'Offline fixture unexpectedly treated a required runtime check as complete.'
    Assert-True -Condition (@($good | Where-Object { $_.effective_pass }).Count -eq 0) -Message 'Incomplete offline acceptance was reported as an effective pass.'
    Assert-True -Condition (@($good | Where-Object { $_.false_pass }).Count -eq 0) -Message 'Good fake variant was classified as false PASS.'
    Assert-True -Condition (@($good | Where-Object { $_.skipped_runtime_count -gt 0 }).Count -gt 0) -Message 'Runtime checks were not explicitly recorded as skipped.'

    $bad = @($runs['bad'])
    Assert-True -Condition (@($bad | Where-Object { $_.false_pass }).Count -eq 1) -Message 'Bad fake variant did not expose a claimed false PASS result.'
    Assert-True -Condition (@($bad | Where-Object { -not $_.effective_pass }).Count -eq 1) -Message 'Bad fake variant unexpectedly passed hidden acceptance.'

    $drift = @($runs['drift'])
    Assert-True -Condition (@($drift | Where-Object { $_.acceptance_pass -and $_.drift.LineCount -gt 0 }).Count -eq 1) -Message 'Drift fake variant did not preserve executed-check success while recording out-of-scope lines.'
    Assert-True -Condition (@($drift | Where-Object { $_.drift.FilesOutsideScope.Count -gt 0 }).Count -eq 1) -Message 'Drift fake variant did not record changed files outside expected_scope.'

    # A real-agent-shaped pair with no false-PASS or drift baseline must not fabricate a
    # percentage or a Rule 1 decision.
    $zeroBaselineDir = Join-Path $testRoot 'zero-baseline'
    New-Item -ItemType Directory -Path $zeroBaselineDir -Force | Out-Null
    foreach ($mode in @('bare', 'core')) {
        [ordered]@{
            task_id = 'zero-baseline'; agent = 'codex'; mode = $mode; status = 'ok'; wall_seconds = 1.0
            usage = $null; cost_usd = $null; effective_pass = $true; false_pass = $false
            not_run_count = 0; skipped_runtime_count = 0; drift = [ordered]@{ LineCount = 0; NewMetadataCount = 0; FilesOutsideScope = @() }
        } | ConvertTo-Json -Depth 6 | Set-Content -LiteralPath (Join-Path $zeroBaselineDir "$mode.json") -Encoding utf8
        [ordered]@{
            task_id = 'missing-time-baseline'; agent = 'claude'; mode = $mode; status = 'ok'; wall_seconds = 0
            usage = $null; cost_usd = $null; effective_pass = ($mode -eq 'core'); false_pass = ($mode -eq 'bare')
            not_run_count = 0; skipped_runtime_count = 0; drift = [ordered]@{ LineCount = 0; NewMetadataCount = 0; FilesOutsideScope = @() }
        } | ConvertTo-Json -Depth 6 | Set-Content -LiteralPath (Join-Path $zeroBaselineDir "time-$mode.json") -Encoding utf8
    }

    $aggregateDir = Join-Path $testRoot 'aggregate'
    & $aggregator -RunDir @((Join-Path $testRoot 'good'), (Join-Path $testRoot 'bad'), (Join-Path $testRoot 'drift'), $coreRunDir, $zeroBaselineDir) -OutputDir $aggregateDir -Date '2099-01-01'
    Assert-True $? 'Benchmark aggregation failed.'
    $report = Get-Content -Raw -LiteralPath (Join-Path $aggregateDir '2099-01-01.json') -Encoding UTF8 | ConvertFrom-Json
    Assert-True -Condition ($report.total_attempts -eq 8) -Message 'Aggregate did not preserve all attempt results.'
    $group = @($report.groups | Where-Object { $_.agent -eq 'fake' -and $_.mode -eq 'bare' }) | Select-Object -First 1
    Assert-True -Condition ($null -ne $group -and $group.false_pass_rate -gt 0) -Message 'Aggregate did not expose the false-PASS metric.'
    Assert-True -Condition ($group.mean_drift_lines -gt 0) -Message 'Aggregate did not expose scope-drift lines.'
    Assert-True -Condition ($group.skipped_runtime_total -gt 0) -Message 'Aggregate did not expose skipped runtime evidence.'
    $fakeComparison = @($report.bare_vs_core | Where-Object { $_.agent -eq 'fake' }) | Select-Object -First 1
    Assert-True -Condition ($fakeComparison.applicability -eq 'synthetic_not_applicable' -and $fakeComparison.decision -eq 'synthetic_not_applicable' -and $null -eq $fakeComparison.keep_per_rule1) -Message 'Synthetic fake comparison was treated as a Rule 1 decision.'
    Assert-True -Condition ($null -eq $fakeComparison.false_pass_rate_cut_pct -and $null -eq $fakeComparison.drift_cut_pct) -Message 'Synthetic fake comparison fabricated quality-cut percentages.'
    $zeroComparison = @($report.bare_vs_core | Where-Object { $_.agent -eq 'codex' }) | Select-Object -First 1
    Assert-True -Condition ($zeroComparison.decision -eq 'not_evaluable' -and $null -eq $zeroComparison.keep_per_rule1) -Message 'Zero-baseline real comparison fabricated a Rule 1 decision.'
    Assert-True -Condition ($null -eq $zeroComparison.false_pass_rate_cut_pct -and $null -eq $zeroComparison.drift_cut_pct) -Message 'Zero-baseline real comparison fabricated percentages.'
    $missingTime = @($report.bare_vs_core | Where-Object { $_.agent -eq 'claude' }) | Select-Object -First 1
    Assert-True ($missingTime.decision -eq 'not_evaluable' -and $null -eq $missingTime.keep_per_rule1 -and $null -eq $missingTime.time_increase_pct) 'A quality improvement without a time baseline was accepted by Rule 1.'
    $markdown = Get-Content -Raw -LiteralPath (Join-Path $aggregateDir '2099-01-01.md') -Encoding UTF8
    Assert-True -Condition ($markdown -match '\| fake \| synthetic_not_applicable \| n/a \| n/a \| n/a \| synthetic_not_applicable \|  \|') -Message 'Markdown did not mark fake comparison as synthetic and n/a.'
    Assert-True -Condition ($markdown -match '\| codex \| real \| n/a \| n/a \| 0% \| not_evaluable \|  \|') -Message 'Markdown did not render zero-baseline cuts as n/a.'
    Write-Host 'Test-BenchHarness: PASS (fake good/bad/drift, synthetic comparison, and zero-baseline aggregation verified).'
}
finally {
    if ($null -eq $oldVariant) { Remove-Item Env:BENCH_FAKE_VARIANT -ErrorAction SilentlyContinue } else { $env:BENCH_FAKE_VARIANT = $oldVariant }
    Remove-BenchHarnessTestRoot -Path $testRoot
}
