#Requires -Version 7.0
# PreToolUse hook (Edit|Write|MultiEdit): denies editing source.paths files
# while an active M/L/high-risk change has no passing, up-to-date final
# validation. This is a lower bar, not a sandbox: it does not intercept Bash
# writes. The authoritative, agent-agnostic guarantee is the post-hoc
# Test-1CChangeGate.ps1 run from 1c-verify. See ADR-13.
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'Hooks.Common.ps1')

try {
    $input_ = Read-BFHookStdin
    if ($null -eq $input_) { exit 0 }

    $projectRoot = $null
    try { $projectRoot = [string]$input_.cwd } catch { $projectRoot = $null }
    if ([string]::IsNullOrWhiteSpace($projectRoot)) { exit 0 }
    if (-not (Test-Path -LiteralPath $projectRoot -PathType Container)) { exit 0 }

    $filePath = Get-BFHookFilePath -ToolInput $input_.tool_input
    if ([string]::IsNullOrWhiteSpace($filePath)) { exit 0 }

    $sourcePaths = Get-BFHookSourcePaths -ProjectRoot $projectRoot
    if (-not (Test-BFPathUnderSource -ProjectRoot $projectRoot -FilePath $filePath -SourcePaths $sourcePaths)) { exit 0 }

    $active = Get-BFHookActiveChange -ProjectRoot $projectRoot
    if ($null -eq $active) { exit 0 }

    $changeName = $null
    try { $changeName = [string]$active.change } catch { $changeName = $null }
    if ([string]::IsNullOrWhiteSpace($changeName)) { exit 0 }
    $complexity = try { [string]$active.complexity } catch { $null }
    $risk = try { [string]$active.risk } catch { $null }
    $isGated = ($complexity -in @('M', 'L')) -or ($risk -eq 'high')
    if (-not $isGated) { exit 0 }

    # Emergency escape hatch: allow, but record the override so verify can
    # report it as a limitation.
    if ($env:BSL_FLOW_GATES -eq 'off') {
        Add-BFHookGateOverrideRecord -ProjectRoot $projectRoot -FilePath $filePath -Change $changeName
        exit 0
    }

    $changeRoot = Join-Path $projectRoot "openspec/changes/$changeName"
    $finalValidationPath = Join-Path $changeRoot 'final-validation.json'
    if (-not (Test-Path -LiteralPath $finalValidationPath -PathType Leaf)) {
        Write-BFHookDeny -Reason "Change '$changeName' is $complexity/$risk and has no final-validation.json yet. Run 1c-spec-review (spec lint + independent review + Test-1CSpecFinal.ps1) before editing files under source.paths. Escape hatch: set BSL_FLOW_GATES=off (recorded as a limitation)."
    }

    $final = $null
    try { $final = [IO.File]::ReadAllText($finalValidationPath) | ConvertFrom-Json -ErrorAction Stop }
    catch {
        Write-BFHookDeny -Reason "Change '$changeName': final-validation.json could not be read. Re-run 1c-spec-review's final validation before editing source. Escape hatch: BSL_FLOW_GATES=off."
    }

    $passed = $false
    try { $passed = [bool]$final.passed } catch { $passed = $false }
    if (-not $passed) {
        Write-BFHookDeny -Reason "Change '$changeName' did not pass final validation (final-validation.json: passed=false). Run 1c-spec-review to resolve findings before editing source. Escape hatch: BSL_FLOW_GATES=off."
    }

    $specPath = Join-Path $changeRoot 'spec.md'
    $currentSpecHash = Get-BFHookFileSha256 -Path $specPath
    $recordedSpecHash = $null
    try { $recordedSpecHash = [string]$final.inputs.final_spec_sha256 } catch { $recordedSpecHash = $null }
    if ($null -ne $currentSpecHash -and -not [string]::IsNullOrWhiteSpace($recordedSpecHash) -and $currentSpecHash -ne $recordedSpecHash) {
        Write-BFHookDeny -Reason "Change '$changeName': spec.md changed after final validation (hash mismatch). Re-run 1c-spec-review's final validation for the current spec before editing source. Escape hatch: BSL_FLOW_GATES=off."
    }

    exit 0
}
catch {
    # Fail open: a broken hook must not silently block all edits.
    [Console]::Error.WriteLine("bsl-flow PreToolUse-EditGate: unexpected error, allowing: $($_.Exception.Message)")
    exit 0
}
