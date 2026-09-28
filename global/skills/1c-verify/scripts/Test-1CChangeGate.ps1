#Requires -Version 7.0
# Post-hoc, agent-agnostic gate: proves that any Claude Code PreToolUse hook
# (or any other host without hooks) was not bypassed. If the diff touches
# source.paths and the active change is M/L or high-risk, a passing,
# up-to-date final-validation.json must predate the source edits. This is
# the authoritative guarantee; hooks are only a lower bar (ADR-13).
[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$ProjectPath,
    [string]$BaseRef = 'HEAD'
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot '../../1c-spec-review/scripts/Review.Common.ps1')

function Invoke-BFGitInDir {
    param([Parameter(Mandatory)][string]$Root, [Parameter(Mandatory)][string[]]$Arguments)
    $previous = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        $output = & git -C $Root @Arguments 2>&1
        $exitCode = $LASTEXITCODE
    }
    finally { $ErrorActionPreference = $previous }
    return [pscustomobject]@{ ExitCode = $exitCode; Lines = @($output | Where-Object { $_ -ne $null -and $_ -ne '' }) }
}

$projectRoot = [System.IO.Path]::GetFullPath($ProjectPath).TrimEnd('\', '/')
if (-not (Test-Path -LiteralPath $projectRoot -PathType Container)) { throw "Project path not found: $projectRoot" }

$reasons = [System.Collections.Generic.List[string]]::new()
$verdict = 'PASS'

# 1. Collect the changed-file set: tracked changes vs BaseRef, plus untracked files.
$changedRelative = [System.Collections.Generic.List[string]]::new()
$gitAvailable = [bool](Get-Command git -ErrorAction SilentlyContinue)
if ($gitAvailable -and (Test-Path -LiteralPath (Join-Path $projectRoot '.git'))) {
    $tracked = Invoke-BFGitInDir -Root $projectRoot -Arguments @('diff', '--name-only', $BaseRef, '--')
    if ($tracked.ExitCode -eq 0) { foreach ($line in $tracked.Lines) { $changedRelative.Add($line) } }
    $untracked = Invoke-BFGitInDir -Root $projectRoot -Arguments @('ls-files', '--others', '--exclude-standard')
    if ($untracked.ExitCode -eq 0) { foreach ($line in $untracked.Lines) { $changedRelative.Add($line) } }
}
$changedRelative = @($changedRelative | Where-Object { $_ } | Select-Object -Unique)

# 2. Filter to files under configured source.paths (default: src).
$sourcePaths = @('src')
$yamlPath = Join-Path $projectRoot 'bsl-flow.yaml'
if (Test-Path -LiteralPath $yamlPath -PathType Leaf) {
    try {
        $yamlText = [IO.File]::ReadAllText($yamlPath, (New-Object Text.UTF8Encoding($false)))
        $match = [regex]::Match($yamlText, '(?ms)^source:\s*\r?\n(?<body>(?:^[ \t]+.*\r?\n?)*)')
        if ($match.Success) {
            $pathsMatch = [regex]::Match($match.Groups['body'].Value, '(?ms)^[ \t]*paths:\s*\r?\n(?<items>(?:^[ \t]*-[ \t]*.+\r?\n?)+)')
            if ($pathsMatch.Success) {
                $items = [regex]::Matches($pathsMatch.Groups['items'].Value, '(?m)^[ \t]*-[ \t]*(?<value>.+?)\s*$') |
                    ForEach-Object { $_.Groups['value'].Value.Trim('"', "'") } | Where-Object { $_ }
                if (@($items).Count -gt 0) { $sourcePaths = @($items) }
            }
        }
    }
    catch { }
}

function Test-BFUnderSourcePath {
    param([string]$Root, [string]$Relative, [string[]]$Paths)
    $fullFile = [IO.Path]::GetFullPath((Join-Path $Root $Relative))
    foreach ($sourcePath in $Paths) {
        $fullSource = [IO.Path]::GetFullPath((Join-Path $Root $sourcePath)).TrimEnd('\', '/')
        $comparison = if ($IsWindows) { [StringComparison]::OrdinalIgnoreCase } else { [StringComparison]::Ordinal }
        if ($fullFile.Equals($fullSource, $comparison)) { return $true }
        if ($fullFile.StartsWith($fullSource + [IO.Path]::DirectorySeparatorChar, $comparison)) { return $true }
        if ($fullFile.StartsWith($fullSource + '/', $comparison)) { return $true }
    }
    return $false
}

$changedSourceFiles = @($changedRelative | Where-Object { Test-BFUnderSourcePath -Root $projectRoot -Relative $_ -Paths $sourcePaths })

# 3. Read overrides log (always reported as limitations, regardless of verdict).
$overrides = [System.Collections.Generic.List[object]]::new()
$overridesPath = Join-Path $projectRoot '.bsl-flow/reports/gate-overrides.jsonl'
if (Test-Path -LiteralPath $overridesPath -PathType Leaf) {
    foreach ($line in @(Get-Content -LiteralPath $overridesPath -ErrorAction SilentlyContinue)) {
        if ([string]::IsNullOrWhiteSpace($line)) { continue }
        try { $overrides.Add(($line | ConvertFrom-Json -ErrorAction Stop)) } catch { }
    }
}

$activeChangeInfo = $null
if (@($changedSourceFiles).Count -gt 0) {
    $activeChangePath = Join-Path $projectRoot '.bsl-flow/active-change.json'
    if (Test-Path -LiteralPath $activeChangePath -PathType Leaf) {
        try { $activeChangeInfo = [IO.File]::ReadAllText($activeChangePath) | ConvertFrom-Json -ErrorAction Stop } catch { $activeChangeInfo = $null }
    }

    if ($null -ne $activeChangeInfo) {
        $changeName = try { [string]$activeChangeInfo.change } catch { $null }
        $complexity = try { [string]$activeChangeInfo.complexity } catch { $null }
        $risk = try { [string]$activeChangeInfo.risk } catch { $null }
        $isGated = ($complexity -in @('M', 'L')) -or ($risk -eq 'high')

        if ($isGated -and -not [string]::IsNullOrWhiteSpace($changeName)) {
            $changeRoot = Join-Path $projectRoot "openspec/changes/$changeName"
            $finalValidationPath = Join-Path $changeRoot 'final-validation.json'
            $final = $null
            if (Test-Path -LiteralPath $finalValidationPath -PathType Leaf) {
                try { $final = [IO.File]::ReadAllText($finalValidationPath) | ConvertFrom-Json -ErrorAction Stop } catch { $final = $null }
            }
            if ($null -eq $final) {
                $verdict = 'FAIL'
                $reasons.Add("process_violation: change '$changeName' is $complexity/$risk and source.paths changed, but final-validation.json is missing or unreadable.")
            }
            else {
                $passed = try { [bool]$final.passed } catch { $false }
                if (-not $passed) {
                    $verdict = 'FAIL'
                    $reasons.Add("process_violation: change '$changeName' final-validation.json has passed=false.")
                }
                else {
                    $specPath = Join-Path $changeRoot 'spec.md'
                    $currentSpecHash = if (Test-Path -LiteralPath $specPath -PathType Leaf) { Get-BSLFlowSha256 $specPath } else { $null }
                    $recordedSpecHash = try { [string]$final.inputs.final_spec_sha256 } catch { $null }
                    if ($null -ne $currentSpecHash -and -not [string]::IsNullOrWhiteSpace($recordedSpecHash) -and $currentSpecHash -ne $recordedSpecHash) {
                        $verdict = 'FAIL'
                        $reasons.Add("process_violation: change '$changeName' spec.md changed after the final validation it was checked against (hash mismatch).")
                    }
                    else {
                        $finalValidationMtime = (Get-Item -LiteralPath $finalValidationPath -Force).LastWriteTimeUtc
                        $earliestChangeMtime = $null
                        foreach ($relative in $changedSourceFiles) {
                            $fullPath = Join-Path $projectRoot $relative
                            if (Test-Path -LiteralPath $fullPath -PathType Leaf) {
                                $mtime = (Get-Item -LiteralPath $fullPath -Force).LastWriteTimeUtc
                                if ($null -eq $earliestChangeMtime -or $mtime -lt $earliestChangeMtime) { $earliestChangeMtime = $mtime }
                            }
                        }
                        if ($null -ne $earliestChangeMtime -and $finalValidationMtime -gt $earliestChangeMtime) {
                            $verdict = 'FAIL'
                            $reasons.Add("process_violation: change '$changeName' final-validation.json is newer than the earliest changed source file; it cannot have preceded the implementation it is meant to gate.")
                        }
                    }
                }
            }
        }
    }
}

$result = [ordered]@{
    schema_version       = 1
    checked_at_utc        = [DateTime]::UtcNow.ToString('o')
    verdict               = $verdict
    base_ref              = $BaseRef
    changed_source_files  = @($changedSourceFiles)
    active_change         = $activeChangeInfo
    reasons               = @($reasons)
    limitations           = @($overrides)
}
Write-Output ($result | ConvertTo-Json -Depth 10)
if ($verdict -eq 'PASS') { exit 0 } else { exit 1 }
