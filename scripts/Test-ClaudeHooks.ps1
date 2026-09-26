#Requires -Version 7.0
# Offline contract test for the Claude Code plugin hooks: feeds fixture
# stdin JSON to each hook script in a temporary git project and checks exit
# code / stdout. Claude Code itself is not required.
[CmdletBinding()]
param([string]$PackageRoot)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($PackageRoot)) { $PackageRoot = Split-Path -Parent $PSScriptRoot }
$packageRoot = [System.IO.Path]::GetFullPath($PackageRoot)
$hooksRoot = Join-Path $packageRoot 'hosts\claude-code\hooks'
$sessionStart = Join-Path $hooksRoot 'SessionStart.ps1'
$editGate = Join-Path $hooksRoot 'PreToolUse-EditGate.ps1'
$evidenceGuard = Join-Path $hooksRoot 'PreToolUse-EvidenceGuard.ps1'
foreach ($file in @($sessionStart, $editGate, $evidenceGuard, (Join-Path $hooksRoot 'Hooks.Common.ps1'), (Join-Path $hooksRoot 'hooks.json'))) {
    if (-not (Test-Path -LiteralPath $file -PathType Leaf)) { throw "Missing Claude Code hook file: $file" }
}

function Assert-True { param([Parameter(Mandatory)][bool]$Condition, [Parameter(Mandatory)][string]$Message) if (-not $Condition) { throw "ASSERTION FAILED: $Message" } }

function Invoke-BFHook {
    param([Parameter(Mandatory)][string]$Script, [Parameter(Mandatory)][string]$StdinJson)
    $previous = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        $output = $StdinJson | & pwsh -NoProfile -File $Script 2>$null
        $exitCode = $LASTEXITCODE
    }
    finally { $ErrorActionPreference = $previous }
    $text = (@($output) -join "`n").Trim()
    $decision = $null
    if ($text) {
        try { $decision = ($text | ConvertFrom-Json -ErrorAction Stop).hookSpecificOutput.permissionDecision } catch { $decision = $null }
    }
    return [pscustomobject]@{ ExitCode = $exitCode; Output = $text; Decision = $decision }
}

function New-BFFixtureProject {
    $root = Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-claude-hooks-' + [guid]::NewGuid().ToString('N'))
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
    return $root
}

function Set-BFActiveChangeFixture {
    param([Parameter(Mandatory)][string]$Root, [string]$Complexity = 'M', [string]$Risk = 'medium')
    $specPath = Join-Path $Root 'openspec\changes\demo-change\spec.md'
    $specHash = (Get-FileHash -LiteralPath $specPath -Algorithm SHA256).Hash.ToLowerInvariant()
    $active = [ordered]@{ schema_version = 1; change = 'demo-change'; complexity = $Complexity; risk = $Risk; spec_sha256 = $specHash; set_at_utc = [DateTime]::UtcNow.ToString('o') }
    $activeDir = Join-Path $Root '.bsl-flow'
    New-Item -ItemType Directory -Path $activeDir -Force | Out-Null
    ($active | ConvertTo-Json -Compress) | Set-Content -LiteralPath (Join-Path $activeDir 'active-change.json') -Encoding utf8
    return $specHash
}

function Set-BFFinalValidationFixture {
    param([Parameter(Mandatory)][string]$Root, [Parameter(Mandatory)][bool]$Passed, [string]$SpecHash)
    $final = [ordered]@{ schema_version = 1; passed = $Passed; inputs = [ordered]@{ final_spec_sha256 = $SpecHash } }
    ($final | ConvertTo-Json -Compress) | Set-Content -LiteralPath (Join-Path $Root 'openspec\changes\demo-change\final-validation.json') -Encoding utf8
}

$temp = New-BFFixtureProject
try {
    # --- SessionStart ---
    $nonProject = Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-not-1c-' + [guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $nonProject -Force | Out-Null
    try {
        $stdin = (@{ cwd = $nonProject } | ConvertTo-Json -Compress)
        $result = Invoke-BFHook -Script $sessionStart -StdinJson $stdin
        Assert-True ($result.ExitCode -eq 0 -and [string]::IsNullOrWhiteSpace($result.Output)) 'SessionStart must print nothing outside a 1C project.'
    }
    finally { Remove-Item -LiteralPath $nonProject -Recurse -Force -ErrorAction SilentlyContinue }

    $stdin = (@{ cwd = $temp } | ConvertTo-Json -Compress)
    $result = Invoke-BFHook -Script $sessionStart -StdinJson $stdin
    Assert-True ($result.ExitCode -eq 0 -and $result.Output -match 'BSL Flow' -and (@($result.Output -split "`n")).Count -le 8) 'SessionStart must print a short (<=8 line) context inside a 1C project.'

    $malformed = Invoke-BFHook -Script $sessionStart -StdinJson 'not json'
    Assert-True ($malformed.ExitCode -eq 0) 'SessionStart must fail open on malformed stdin JSON.'

    # --- PreToolUse-EditGate matrix ---
    $srcFile = Join-Path $temp 'src\Module.bsl'
    $outsideFile = Join-Path $temp 'docs\notes.md'
    $editStdin = { param($path) (@{ cwd = $temp; tool_name = 'Edit'; tool_input = @{ file_path = $path } } | ConvertTo-Json -Compress) }

    # Outside source.paths -> allow, regardless of active change.
    $r = Invoke-BFHook -Script $editGate -StdinJson (& $editStdin $outsideFile)
    Assert-True ($r.ExitCode -eq 0 -and -not $r.Decision) 'EditGate must allow edits outside source.paths.'

    # No active change (S-route) -> allow.
    $r = Invoke-BFHook -Script $editGate -StdinJson (& $editStdin $srcFile)
    Assert-True ($r.ExitCode -eq 0 -and -not $r.Decision) 'EditGate must allow source edits with no active change.'

    # Active S change -> allow (not gated).
    Set-BFActiveChangeFixture -Root $temp -Complexity 'S' -Risk 'low' | Out-Null
    $r = Invoke-BFHook -Script $editGate -StdinJson (& $editStdin $srcFile)
    Assert-True ($r.ExitCode -eq 0 -and -not $r.Decision) 'EditGate must allow source edits under an S/low active change.'

    # Active M change, no final-validation.json -> deny.
    $specHash = Set-BFActiveChangeFixture -Root $temp -Complexity 'M' -Risk 'medium'
    $r = Invoke-BFHook -Script $editGate -StdinJson (& $editStdin $srcFile)
    Assert-True ($r.ExitCode -eq 0 -and $r.Decision -eq 'deny') 'EditGate must deny source edits under M without final-validation.json.'

    # Active M change, failing final-validation.json -> deny.
    Set-BFFinalValidationFixture -Root $temp -Passed $false -SpecHash $specHash
    $r = Invoke-BFHook -Script $editGate -StdinJson (& $editStdin $srcFile)
    Assert-True ($r.ExitCode -eq 0 -and $r.Decision -eq 'deny') 'EditGate must deny source edits when final-validation.json has passed=false.'

    # Active M change, passing final-validation.json with stale spec hash -> deny.
    Set-BFFinalValidationFixture -Root $temp -Passed $true -SpecHash 'deadbeef'
    $r = Invoke-BFHook -Script $editGate -StdinJson (& $editStdin $srcFile)
    Assert-True ($r.ExitCode -eq 0 -and $r.Decision -eq 'deny') 'EditGate must deny source edits when final-validation.json spec hash is stale.'

    # Active M change, passing and matching final-validation.json -> allow.
    Set-BFFinalValidationFixture -Root $temp -Passed $true -SpecHash $specHash
    $r = Invoke-BFHook -Script $editGate -StdinJson (& $editStdin $srcFile)
    Assert-True ($r.ExitCode -eq 0 -and -not $r.Decision) 'EditGate must allow source edits once final-validation.json passes with a matching hash.'

    # Env override writes an override record and still allows, even while blocked.
    Remove-Item -LiteralPath (Join-Path $temp 'openspec\changes\demo-change\final-validation.json') -Force
    $overridesPath = Join-Path $temp '.bsl-flow\reports\gate-overrides.jsonl'
    if (Test-Path -LiteralPath $overridesPath) { Remove-Item -LiteralPath $overridesPath -Force }
    $env:BSL_FLOW_GATES = 'off'
    try {
        $r = Invoke-BFHook -Script $editGate -StdinJson (& $editStdin $srcFile)
        Assert-True ($r.ExitCode -eq 0 -and -not $r.Decision) 'EditGate must allow when BSL_FLOW_GATES=off even while otherwise blocked.'
        Assert-True (Test-Path -LiteralPath $overridesPath -PathType Leaf) 'EditGate must record an override in gate-overrides.jsonl when BSL_FLOW_GATES=off.'
        $overrideLine = Get-Content -LiteralPath $overridesPath | Select-Object -Last 1
        $overrideRecord = $overrideLine | ConvertFrom-Json
        Assert-True ($overrideRecord.change -eq 'demo-change') 'Override record must name the active change.'
    }
    finally { Remove-Item Env:\BSL_FLOW_GATES -ErrorAction SilentlyContinue }

    $malformed = Invoke-BFHook -Script $editGate -StdinJson 'not json'
    Assert-True ($malformed.ExitCode -eq 0 -and -not $malformed.Decision) 'EditGate must fail open (allow) on malformed stdin JSON.'

    # --- PreToolUse-EvidenceGuard ---
    $evidenceStdin = { param($path) (@{ cwd = $temp; tool_name = 'Edit'; tool_input = @{ file_path = $path } } | ConvertTo-Json -Compress) }
    foreach ($deniedRelative in @('openspec\changes\demo-change\spec-lint.json', 'openspec\changes\demo-change\review.json', 'openspec\changes\demo-change\final-validation.json', '.bsl-flow\evidence\some.json', '.bsl-flow\active-change.json')) {
        $r = Invoke-BFHook -Script $evidenceGuard -StdinJson (& $evidenceStdin (Join-Path $temp $deniedRelative))
        Assert-True ($r.ExitCode -eq 0 -and $r.Decision -eq 'deny') "EvidenceGuard must deny direct edits to $deniedRelative."
    }
    $r = Invoke-BFHook -Script $evidenceGuard -StdinJson (& $evidenceStdin (Join-Path $temp 'openspec\changes\demo-change\review-reconciliation.json'))
    Assert-True ($r.ExitCode -eq 0 -and -not $r.Decision) 'EvidenceGuard must allow review-reconciliation.json.'
    $r = Invoke-BFHook -Script $evidenceGuard -StdinJson (& $evidenceStdin $srcFile)
    Assert-True ($r.ExitCode -eq 0 -and -not $r.Decision) 'EvidenceGuard must allow ordinary source edits.'

    $malformed = Invoke-BFHook -Script $evidenceGuard -StdinJson 'not json'
    Assert-True ($malformed.ExitCode -eq 0 -and -not $malformed.Decision) 'EvidenceGuard must fail open (allow) on malformed stdin JSON.'

    # --- Optional: claude plugin validate, skipped gracefully when the CLI is absent. ---
    $claudeCli = Get-Command claude -ErrorAction SilentlyContinue
    if ($null -eq $claudeCli) {
        Write-Output 'claude CLI not found; skipping plugin validate check.'
    }
    else {
        $previous = $ErrorActionPreference
        $ErrorActionPreference = 'Continue'
        try {
            $validateOutput = & claude plugin validate $packageRoot 2>&1
            $validateExit = $LASTEXITCODE
        }
        finally { $ErrorActionPreference = $previous }
        Assert-True ($validateExit -eq 0 -and (($validateOutput -join "`n") -match 'Validation passed')) "claude plugin validate failed: $($validateOutput -join ' | ')"
    }

    Write-Output 'CLAUDE_HOOKS_OK'
}
finally {
    Remove-Item -LiteralPath $temp -Recurse -Force -ErrorAction SilentlyContinue
}
