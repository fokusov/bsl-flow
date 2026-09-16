#Requires -Version 7.0
# Validates the PowerShell-only engine workflow in .github/workflows/offline.yml.
# Plain-text parsing only - no YAML module required.
# Asserts: the engine job is the only job, every runs-on value is a known runner,
# the engine job runs the packaged PowerShell suites, and the Go/native CLI
# pipeline (setup-go, go-version, native-cli.yml) is fully absent.
# Prints CI_MATRIX_OK on success.
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$repoRoot = Split-Path -Parent $PSScriptRoot
$workflowPath = Join-Path $repoRoot '.github\workflows\offline.yml'
if (-not (Test-Path -LiteralPath $workflowPath)) { throw "Workflow not found: $workflowPath" }
$lines = @(Get-Content -LiteralPath $workflowPath)

# --- Job ids: 2-space indented keys inside the jobs: section ---
$jobsIndex = -1
for ($i = 0; $i -lt $lines.Count; $i++) {
    if ($lines[$i] -match '^jobs:\s*$') { $jobsIndex = $i; break }
}
if ($jobsIndex -lt 0) { throw 'jobs: section not found in offline.yml' }

$jobIds = @()
for ($i = $jobsIndex + 1; $i -lt $lines.Count; $i++) {
    if ($lines[$i] -match '^  ([A-Za-z0-9][A-Za-z0-9_-]*):(?:\s|$)') { $jobIds += $Matches[1] }
}
if ($jobIds.Count -ne 1 -or $jobIds[0] -cne 'engine') { throw "Expected exactly the 'engine' job, found $($jobIds.Count): $($jobIds -join ', ')" }

# --- Every runs-on value must be a known runner label ---
$allowed = @('windows-2025', 'macos-14', 'macos-13', 'ubuntu-24.04', '${{ matrix.os }}')
$runsOnCount = 0
for ($i = 0; $i -lt $lines.Count; $i++) {
    if ($lines[$i] -match '^\s+runs-on:\s*(\S.*)$') {
        $value = ($Matches[1] -replace '\s*#.*$', '').Trim()
        if ($allowed -notcontains $value) { throw "Line $($i + 1): unexpected runs-on value '$value'" }
        $runsOnCount++
    }
}
if ($runsOnCount -lt 1) { throw 'Expected at least 1 runs-on entry, found none' }

# --- The Go/native CLI pipeline must be fully absent from the workflow ---
$forbiddenPatterns = @(
    @{ Pattern = '(?i)\bsetup-go\b'; Reason = 'setup-go action' },
    @{ Pattern = '(?i)\bgo-version\s*:'; Reason = 'go-version key' },
    @{ Pattern = '(?m)^\s*(?:-\s+)?name:\s*Go\b'; Reason = 'Go step' },
    @{ Pattern = '(?i)\bgo\s+(?:vet|build|test)\b'; Reason = 'Go command' },
    @{ Pattern = '(?i)native-cli\.yml'; Reason = 'native-cli.yml reference' },
    @{ Pattern = '(?i)Build-BSLFlowCli|Test-BSLFlowCli'; Reason = 'native CLI build/smoke script' }
)
for ($i = 0; $i -lt $lines.Count; $i++) {
    foreach ($forbidden in $forbiddenPatterns) {
        if ($lines[$i] -match $forbidden.Pattern) { throw "Line $($i + 1): workflow references the removed Go pipeline ($($forbidden.Reason)): $($lines[$i].Trim())" }
    }
}
$nativeWorkflowPath = Join-Path $repoRoot '.github\workflows\native-cli.yml'
if (Test-Path -LiteralPath $nativeWorkflowPath) { throw "Removed Go matrix workflow still exists: $nativeWorkflowPath" }

# --- The engine job body must run the packaged PowerShell suites ---
$engineStart = -1
for ($i = $jobsIndex + 1; $i -lt $lines.Count; $i++) {
    if ($lines[$i] -match '^  engine:\s*$') { $engineStart = $i; break }
}
if ($engineStart -lt 0) { throw 'engine job header not found' }
$engineEnd = $lines.Count
for ($i = $engineStart + 1; $i -lt $lines.Count; $i++) {
    if ($lines[$i] -match '^  [A-Za-z0-9][A-Za-z0-9_-]*:') { $engineEnd = $i; break }
}
$engineBlock = $lines[$engineStart..($engineEnd - 1)]
foreach ($required in @('Test-BSLFlowPackage\.ps1', '(?i)pwsh', 'scripts/\$suite\.ps1')) {
    $hit = @($engineBlock | Where-Object { $_ -match $required })
    if ($hit.Count -eq 0) { throw "engine job body does not run the PowerShell suites (missing: $required)" }
}

Write-Output "jobs=$($jobIds -join ',') runs-on-entries=$runsOnCount"
Write-Output 'CI_MATRIX_OK'
