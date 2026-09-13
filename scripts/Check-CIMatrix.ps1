# Validates the native OS matrix in .github/workflows/offline.yml (spec req. 19).
# Plain-text parsing only - no YAML module required.
# Asserts: job id inventory, every runs-on value is a known runner,
# native-matrix holds three non-Windows OS entries, and the native-matrix
# job body contains no pwsh/powershell reference. Prints CI_MATRIX_OK on success.
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
if ($jobIds.Count -lt 3) { throw "Expected at least 3 job ids, found $($jobIds.Count): $($jobIds -join ', ')" }
foreach ($expected in @('engine', 'cli', 'native-matrix')) {
    if ($jobIds -notcontains $expected) { throw "Expected job '$expected' missing; jobs: $($jobIds -join ', ')" }
}

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
if ($runsOnCount -lt 3) { throw "Expected at least 3 runs-on entries, found $runsOnCount" }

# --- native-matrix block: header up to the next top-level job or EOF ---
$start = -1
for ($i = 0; $i -lt $lines.Count; $i++) {
    if ($lines[$i] -match '^  native-matrix:\s*$') { $start = $i; break }
}
if ($start -lt 0) { throw 'native-matrix job header not found' }
$end = $lines.Count
for ($i = $start + 1; $i -lt $lines.Count; $i++) {
    if ($lines[$i] -match '^  [A-Za-z0-9][A-Za-z0-9_-]*:') { $end = $i; break }
}
$block = $lines[$start..($end - 1)]

# --- Matrix must carry the three non-Windows OS entries and no Windows one ---
$osLine = @($block | Where-Object { $_ -match '^\s+os:\s*\[' }) | Select-Object -First 1
if (-not $osLine) { throw 'Matrix os: line not found inside native-matrix' }
foreach ($os in @('macos-14', 'macos-13', 'ubuntu-24.04')) {
    if ($osLine -notmatch [regex]::Escape($os)) { throw "Matrix os: missing '$os' (line: $osLine)" }
}
if ($osLine -match 'windows') { throw "native-matrix os: must not list a Windows runner (line: $osLine)" }

# --- native-matrix must not depend on pwsh/powershell anywhere in the job body ---
for ($i = 0; $i -lt $block.Count; $i++) {
    if ($block[$i] -match '(?i)pwsh|powershell') {
        throw "native-matrix references a forbidden shell at line $($start + $i + 1): $($block[$i])"
    }
}

Write-Output "jobs=$($jobIds -join ',') runs-on-entries=$runsOnCount"
Write-Output 'CI_MATRIX_OK'
