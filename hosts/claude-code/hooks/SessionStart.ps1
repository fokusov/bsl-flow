#Requires -Version 7.0
# SessionStart hook: plain stdout becomes session context. Outside a 1C
# project this prints nothing (0 bytes into context). Detection is bounded
# (see Test-BF1CProjectMarker) to stay within a ~1s budget.
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'Hooks.Common.ps1')

try {
    $stopwatch = [Diagnostics.Stopwatch]::StartNew()
    $input_ = Read-BFHookStdin
    $projectRoot = $null
    if ($null -ne $input_) { try { $projectRoot = [string]$input_.cwd } catch { $projectRoot = $null } }
    if ([string]::IsNullOrWhiteSpace($projectRoot)) { $projectRoot = (Get-Location).Path }

    if (-not (Test-BF1CProjectMarker -ProjectRoot $projectRoot)) { exit 0 }

    $lines = [System.Collections.Generic.List[string]]::new()
    $lines.Add('BSL Flow: 1C project detected.')

    $bslFlowYaml = Join-Path $projectRoot 'bsl-flow.yaml'
    $initialized = (Test-Path -LiteralPath $bslFlowYaml -PathType Leaf)
    if (-not $initialized) {
        $lines.Add('Not initialized: run 1c-init-project before other BSL Flow skills.')
    }
    else {
        $lines.Add('Route: S -> 1c-implement -> 1c-verify; M/L or high risk -> 1c-spec -> 1c-spec-review -> 1c-implement -> 1c-verify.')
        $active = Get-BFHookActiveChange -ProjectRoot $projectRoot
        if ($null -eq $active) {
            $lines.Add('Active change: none (S-route, or 1c-spec has not run Set-1CActiveChange yet).')
        }
        else {
            $changeName = try { [string]$active.change } catch { 'unknown' }
            $complexity = try { [string]$active.complexity } catch { 'unknown' }
            $risk = try { [string]$active.risk } catch { 'unknown' }
            $finalValidationPath = Join-Path $projectRoot "openspec/changes/$changeName/final-validation.json"
            $gateStatus = 'no final-validation.json yet'
            if (Test-Path -LiteralPath $finalValidationPath -PathType Leaf) {
                try {
                    $final = [IO.File]::ReadAllText($finalValidationPath) | ConvertFrom-Json -ErrorAction Stop
                    $gateStatus = if ([bool]$final.passed) { 'final validation PASSED' } else { 'final validation FAILED' }
                }
                catch { $gateStatus = 'final-validation.json unreadable' }
            }
            $lines.Add("Active change: $changeName ($complexity/$risk) - $gateStatus.")
        }
    }

    if ($stopwatch.ElapsedMilliseconds -lt 1000) {
        $lines | Select-Object -First 8 | ForEach-Object { Write-Output $_ }
    }
}
catch {
    # Fail open and quiet: a SessionStart hook must never block session start.
    exit 0
}
