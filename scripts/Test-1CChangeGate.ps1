#Requires -Version 7.0
# Offline contract test for global/skills/1c-verify/scripts/Test-1CChangeGate.ps1:
# PASS/FAIL/override cases against temp git repositories. No network, no host.
[CmdletBinding()]
param([string]$PackageRoot)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($PackageRoot)) { $PackageRoot = Split-Path -Parent $PSScriptRoot }
$packageRoot = [System.IO.Path]::GetFullPath($PackageRoot)
$gateScript = Join-Path $packageRoot 'global\skills\1c-verify\scripts\Test-1CChangeGate.ps1'
$setActiveScript = Join-Path $packageRoot 'global\skills\1c-spec\scripts\Set-1CActiveChange.ps1'
foreach ($file in @($gateScript, $setActiveScript)) {
    if (-not (Test-Path -LiteralPath $file -PathType Leaf)) { throw "Missing change-gate package file: $file" }
}

function Assert-True { param([Parameter(Mandatory)][bool]$Condition, [Parameter(Mandatory)][string]$Message) if (-not $Condition) { throw "ASSERTION FAILED: $Message" } }

function New-BFGateFixtureProject {
    $root = Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-change-gate-' + [guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $root -Force | Out-Null
    New-Item -ItemType Directory -Path (Join-Path $root 'src') -Force | Out-Null
    New-Item -ItemType Directory -Path (Join-Path $root 'openspec\changes\demo-change') -Force | Out-Null
    Set-Content -LiteralPath (Join-Path $root 'bsl-flow.yaml') -Value "source:`n  paths:`n    - src`n" -Encoding utf8
    Set-Content -LiteralPath (Join-Path $root 'src\Module.bsl') -Value 'Процедура Тест() КонецПроцедуры' -Encoding utf8
    $specText = @'
## Classification
- Complexity: M
- Risk: medium

## Goal
Do a thing.

## Required behavior
Something happens.

## 1C context
n/a

## Non-goals
n/a

## Acceptance criteria
- GIVEN a WHEN b THEN c

## Required verification
- [x] Static: checks something specific and useful
'@
    Set-Content -LiteralPath (Join-Path $root 'openspec\changes\demo-change\spec.md') -Value $specText -Encoding utf8

    Push-Location $root
    try {
        & git init -q 2>$null
        & git -c core.autocrlf=false add -A 2>$null
        & git -c user.email=test@test.local -c user.name=test commit -q -m init 2>$null
    }
    finally { Pop-Location }
    return $root
}

function Invoke-BFGate {
    param([Parameter(Mandatory)][string]$Root)
    $previous = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        $output = & $gateScript -ProjectPath $Root 2>$null
        $exitCode = $LASTEXITCODE
    }
    finally { $ErrorActionPreference = $previous }
    $json = ($output -join "`n") | ConvertFrom-Json
    return [pscustomobject]@{ ExitCode = $exitCode; Result = $json }
}

$temp = New-BFGateFixtureProject
try {
    # No active change, no source edits -> PASS.
    $r = Invoke-BFGate -Root $temp
    Assert-True ($r.ExitCode -eq 0 -and $r.Result.verdict -eq 'PASS') 'Gate must PASS with no changed source files.'

    # Source file changed but no active change at all -> PASS (nothing to gate).
    Add-Content -LiteralPath (Join-Path $temp 'src\Module.bsl') -Value 'ещё'
    $r = Invoke-BFGate -Root $temp
    Assert-True ($r.ExitCode -eq 0 -and $r.Result.verdict -eq 'PASS' -and @($r.Result.changed_source_files).Count -eq 1) 'Gate must PASS for a changed source file with no active change.'

    # Active M change, no final-validation.json -> FAIL: process_violation.
    & $setActiveScript -ProjectPath $temp -ChangeName demo-change | Out-Null
    $r = Invoke-BFGate -Root $temp
    Assert-True ($r.ExitCode -eq 1 -and $r.Result.verdict -eq 'FAIL' -and ($r.Result.reasons -join '|') -match 'process_violation') 'Gate must FAIL for an M change with source edits and no final-validation.json.'

    # Active M change, failing final-validation.json -> FAIL.
    $specHash = (Get-FileHash -LiteralPath (Join-Path $temp 'openspec\changes\demo-change\spec.md') -Algorithm SHA256).Hash.ToLowerInvariant()
    $finalPath = Join-Path $temp 'openspec\changes\demo-change\final-validation.json'
    (@{ schema_version = 1; passed = $false; inputs = @{ final_spec_sha256 = $specHash } } | ConvertTo-Json -Compress) | Set-Content -LiteralPath $finalPath -Encoding utf8
    $r = Invoke-BFGate -Root $temp
    Assert-True ($r.ExitCode -eq 1 -and $r.Result.verdict -eq 'FAIL') 'Gate must FAIL when final-validation.json has passed=false.'

    # Active M change, passing final-validation.json but stale spec hash -> FAIL.
    (@{ schema_version = 1; passed = $true; inputs = @{ final_spec_sha256 = 'deadbeef' } } | ConvertTo-Json -Compress) | Set-Content -LiteralPath $finalPath -Encoding utf8
    $r = Invoke-BFGate -Root $temp
    Assert-True ($r.ExitCode -eq 1 -and $r.Result.verdict -eq 'FAIL') 'Gate must FAIL when final-validation.json spec hash is stale.'

    # Active M change, passing + matching final-validation.json but NEWER than the source edit -> FAIL
    # (final validation cannot have preceded implementation it is meant to gate).
    (@{ schema_version = 1; passed = $true; inputs = @{ final_spec_sha256 = $specHash } } | ConvertTo-Json -Compress) | Set-Content -LiteralPath $finalPath -Encoding utf8
    $sourceFile = Join-Path $temp 'src\Module.bsl'
    (Get-Item -LiteralPath $sourceFile).LastWriteTime = (Get-Date).AddMinutes(-30)
    (Get-Item -LiteralPath $finalPath).LastWriteTime = (Get-Date)
    $r = Invoke-BFGate -Root $temp
    Assert-True ($r.ExitCode -eq 1 -and $r.Result.verdict -eq 'FAIL') 'Gate must FAIL when final-validation.json is newer than the source edit it should have preceded.'

    # Same passing/matching final-validation.json, but older than the source edit -> PASS.
    (Get-Item -LiteralPath $finalPath).LastWriteTime = (Get-Date).AddMinutes(-60)
    $r = Invoke-BFGate -Root $temp
    Assert-True ($r.ExitCode -eq 0 -and $r.Result.verdict -eq 'PASS') 'Gate must PASS when final-validation.json passes, matches the spec, and precedes the source edit.'

    # Override log is surfaced as a limitation regardless of verdict.
    $reportsDir = Join-Path $temp '.bsl-flow\reports'
    New-Item -ItemType Directory -Path $reportsDir -Force | Out-Null
    $overrideRecord = @{ recorded_at_utc = [DateTime]::UtcNow.ToString('o'); hook = 'PreToolUse-EditGate'; file = 'src/Module.bsl'; change = 'demo-change'; reason = 'BSL_FLOW_GATES=off' }
    ($overrideRecord | ConvertTo-Json -Compress) | Set-Content -LiteralPath (Join-Path $reportsDir 'gate-overrides.jsonl') -Encoding utf8
    $r = Invoke-BFGate -Root $temp
    Assert-True (@($r.Result.limitations).Count -eq 1 -and $r.Result.limitations[0].reason -eq 'BSL_FLOW_GATES=off') 'Gate must report recorded overrides as limitations.'

    # Active S/low change is never gated, even with source edits and no final-validation.json.
    Remove-Item -LiteralPath $finalPath -Force
    & $setActiveScript -ProjectPath $temp -ChangeName demo-change -ErrorAction SilentlyContinue | Out-Null
    $activePath = Join-Path $temp '.bsl-flow\active-change.json'
    $active = Get-Content -LiteralPath $activePath -Raw | ConvertFrom-Json
    $active.complexity = 'S'
    $active.risk = 'low'
    ($active | ConvertTo-Json -Compress) | Set-Content -LiteralPath $activePath -Encoding utf8
    $r = Invoke-BFGate -Root $temp
    Assert-True ($r.ExitCode -eq 0 -and $r.Result.verdict -eq 'PASS') 'Gate must not gate an S/low active change.'

    Write-Output 'CHANGE_GATE_OK'
}
finally {
    Remove-Item -LiteralPath $temp -Recurse -Force -ErrorAction SilentlyContinue
}
