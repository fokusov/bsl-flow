#Requires -Version 7.0
# Enforces the surface-area budget from docs/plans/2026-09-26-remediation-plan.md (Ф0.1/Ф0.3/
# Ф3.4): the managed package must not silently grow past its frozen line-count baseline (the
# ADR-11 freeze), and no bootstrap/SKILL.md must silently grow past its recorded byte/negation
# baseline. See scripts/surface-budget.json for the recorded thresholds and how to move them on
# purpose.
[CmdletBinding()]
param([string]$PackageRoot)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($PackageRoot)) { $PackageRoot = Split-Path -Parent $PSScriptRoot }
$root = [IO.Path]::GetFullPath($PackageRoot).TrimEnd('\', '/')
$measureScript = Join-Path $root 'scripts/Measure-BSLFlowSurface.ps1'
$budgetPath = Join-Path $root 'scripts/surface-budget.json'
if (-not (Test-Path -LiteralPath $measureScript -PathType Leaf)) { throw "Missing measurement script: $measureScript" }
if (-not (Test-Path -LiteralPath $budgetPath -PathType Leaf)) { throw "Missing surface budget: $budgetPath" }

$script:checks = 0
function Assert-B { param([bool]$Condition, [string]$Message) if (-not $Condition) { throw "ASSERTION FAILED: $Message" }; $script:checks++ }
$updateHint = "See scripts/surface-budget.json ('how_to_update_the_baseline') to move this deliberately."

function Assert-BFSurfaceBudget {
    # Pure comparison of a measurement (as produced by Measure-BSLFlowSurface.ps1, already
    # ConvertFrom-Json'd) against a budget (already ConvertFrom-Json'd). Throws naming the first
    # violation. Kept side-effect free so both the real package tree and synthetic negative
    # fixtures below exercise exactly this logic.
    param($Budget, $Measured)

    $managedLines = [int64]$Measured.areas.managed.lines
    $baselineLines = [int64]$Budget.freeze.managed_ps_lines_baseline
    $growthPct = [double]$Budget.freeze.managed_ps_lines_growth_limit_pct
    $limitLines = [math]::Floor($baselineLines * (1 + $growthPct / 100.0))
    if ($managedLines -gt $limitLines) {
        throw "Managed PowerShell surface grew from a baseline of $baselineLines to $managedLines lines, past the +$growthPct% freeze limit of $limitLines lines. This is expected only for the ADR-11 controller-portability exception (plan Ф4.5-4.7); otherwise it means a forbidden new managed feature slipped in. $updateHint"
    }

    $bootstrapBytes = [int64]$Measured.bootstrap.bytes
    $bootstrapMax = [int64]$Budget.current.bootstrap_bytes_max
    if ($bootstrapBytes -gt $bootstrapMax) {
        throw "global/AGENTS.bootstrap.md grew from a recorded baseline of $bootstrapMax bytes to $bootstrapBytes bytes. $updateHint"
    }

    $budgetSkills = $Budget.current.skills
    foreach ($skillName in $budgetSkills.PSObject.Properties.Name) {
        $entry = $budgetSkills.$skillName
        if ($Measured.skills.PSObject.Properties.Name -notcontains $skillName) {
            throw "Skill '$skillName' is recorded in surface-budget.json but Measure-BSLFlowSurface.ps1 did not find it: it may have been removed or renamed. $updateHint"
        }
        $measuredSkill = $Measured.skills.$skillName
        $bytesMax = [int64]$entry.bytes_max
        if ([int64]$measuredSkill.bytes -gt $bytesMax) {
            throw "global/skills/$skillName/SKILL.md grew from a recorded baseline of $bytesMax bytes to $($measuredSkill.bytes) bytes. $updateHint"
        }
        $negationMax = [int64]$entry.negation_max
        if ([int64]$measuredSkill.negation_count -gt $negationMax) {
            throw "global/skills/$skillName/SKILL.md grew from a recorded baseline of $negationMax negation phrases to $($measuredSkill.negation_count). $updateHint"
        }
    }

    # Every skill the measurement finds must also be recorded, so a new skill cannot silently
    # ship without a budget entry.
    foreach ($skillName in $Measured.skills.PSObject.Properties.Name) {
        if ($budgetSkills.PSObject.Properties.Name -notcontains $skillName) {
            throw "SKILL.md '$skillName' has no entry in scripts/surface-budget.json 'current.skills'. Add one at its measured size before it can ship. $updateHint"
        }
    }
}

function New-FixtureMeasured {
    # Minimal synthetic measurement matching the shape Measure-BSLFlowSurface.ps1 produces,
    # for the negative fixtures below.
    param([int64]$ManagedLines = 100, [int64]$BootstrapBytes = 500, [hashtable]$Skills = @{ demo = @{ bytes = 1000; negation_count = 1 } })
    $skillsObj = [ordered]@{}
    foreach ($name in $Skills.Keys) { $skillsObj[$name] = [pscustomobject]@{ bytes = $Skills[$name].bytes; negation_count = $Skills[$name].negation_count } }
    [pscustomobject]@{
        areas     = [pscustomobject]@{ managed = [pscustomobject]@{ lines = $ManagedLines } }
        bootstrap = [pscustomobject]@{ bytes = $BootstrapBytes }
        skills    = [pscustomobject]$skillsObj
    }
}

function New-FixtureBudget {
    param([int64]$BaselineLines = 100, [double]$GrowthPct = 2, [int64]$BootstrapMax = 500, [hashtable]$Skills = @{ demo = @{ bytes_max = 1000; negation_max = 1 } })
    $skillsObj = [ordered]@{}
    foreach ($name in $Skills.Keys) { $skillsObj[$name] = [pscustomobject]@{ bytes_max = $Skills[$name].bytes_max; negation_max = $Skills[$name].negation_max } }
    [pscustomobject]@{
        freeze  = [pscustomobject]@{ managed_ps_lines_baseline = $BaselineLines; managed_ps_lines_growth_limit_pct = $GrowthPct }
        current = [pscustomobject]@{ bootstrap_bytes_max = $BootstrapMax; skills = [pscustomobject]$skillsObj }
    }
}

function Expect-BudgetFailure {
    param([scriptblock]$Mutate, [string]$Pattern, [string]$Message)
    $budget = New-FixtureBudget
    $measured = New-FixtureMeasured
    & $Mutate $budget $measured
    $threw = $false
    try { Assert-BFSurfaceBudget $budget $measured } catch { $threw = $true; Assert-B ($_.Exception.Message -match $Pattern) "$Message (wrong error: $($_.Exception.Message))" }
    Assert-B $threw "$Message (budget check did not fail)."
}

# --- Negative fixtures: prove the gate fails closed before trusting it on the real tree. ---
Expect-BudgetFailure { param($b, $m) $m.areas.managed.lines = 103 } 'Managed PowerShell surface grew' 'Managed lines past the +2% freeze limit must fail.'
Expect-BudgetFailure { param($b, $m) $m.bootstrap.bytes = 501 } 'AGENTS\.bootstrap\.md grew' 'Bootstrap bytes past baseline must fail.'
Expect-BudgetFailure { param($b, $m) $m.skills.demo.bytes = 1001 } "SKILL\.md grew from a recorded baseline of 1000 bytes" 'Skill bytes past baseline must fail.'
Expect-BudgetFailure { param($b, $m) $m.skills.demo.negation_count = 2 } 'negation phrases' 'Skill negation count past baseline must fail.'
Expect-BudgetFailure { param($b, $m) $m.skills | Add-Member -NotePropertyName extra -NotePropertyValue ([pscustomobject]@{ bytes = 1; negation_count = 0 }) } 'has no entry in scripts/surface-budget.json' 'An unrecorded skill must fail.'

# A budget exactly at every ceiling passes.
$budgetAtLimit = New-FixtureBudget
$measuredAtLimit = New-FixtureMeasured -ManagedLines 102
Assert-BFSurfaceBudget $budgetAtLimit $measuredAtLimit
$script:checks++

# --- Real measurement against the recorded baseline. ---
$budget = Get-Content -Raw -LiteralPath $budgetPath | ConvertFrom-Json
$measured = & $measureScript -PackageRoot $root | ConvertFrom-Json
Assert-B ($measured.schema_version -eq 1) 'Measure-BSLFlowSurface.ps1 returned an unexpected schema_version.'
Assert-BFSurfaceBudget $budget $measured
$script:checks++

Write-Output ("SURFACE_BUDGET_OK checks=$script:checks managed_lines={0} bootstrap_bytes={1}" -f $measured.areas.managed.lines, $measured.bootstrap.bytes)
