#Requires -Version 7.0
# Governance gate for openspec/changes: a change that reached a BLOCKED review must carry an
# explicit, non-empty owner override before it is treated as mergeable. This closes the
# governance hole from the remediation plan (Ф0.4): execution-contract-v01 was merged while its
# council review was BLOCKED, with no recorded owner decision anywhere in the repository.
#
# override.md format (deliberately simple, no YAML parser dependency — matches the plain
# "key: value" convention already used elsewhere in this repo, e.g. SKILL.md frontmatter checks
# in Test-BSLFlowOpenCode.ps1): a UTF-8 text file with four top-level sections, each introduced
# by a line that starts at column 0 with `<key>:`. A section's value is everything between its
# key line and the next recognised key line (or end of file), so a value may span multiple lines
# and, for accepted_risks, may itself contain further simple `key: value` sub-lines. Example:
#
#   owner: Igor Fokusov
#   date: 2026-09-26
#   reason: >
#     Free-form explanation of why the override is granted.
#   accepted_risks:
#     risk_one: description of the first accepted risk
#     risk_two: description of the second accepted risk
#
# Required keys: owner, date (must match YYYY-MM-DD), reason, accepted_risks. All four must be
# present with non-empty (non-whitespace) bodies, or the change fails governance.
[CmdletBinding()]
param([string]$PackageRoot)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($PackageRoot)) { $PackageRoot = Split-Path -Parent $PSScriptRoot }
$root = [IO.Path]::GetFullPath($PackageRoot).TrimEnd('\', '/')
$changesRoot = Join-Path $root 'openspec/changes'
if (-not (Test-Path -LiteralPath $changesRoot -PathType Container)) { throw "Missing openspec/changes directory: $changesRoot" }

$script:checks = 0
function Assert-G { param([bool]$Condition, [string]$Message) if (-not $Condition) { throw "ASSERTION FAILED: $Message" }; $script:checks++ }

$overrideKeys = @('owner', 'date', 'reason', 'accepted_risks')

function Read-OverrideFields {
    # Splits override.md into sections keyed by the recognised top-level field names and
    # returns a hashtable of trimmed field bodies. Missing fields are simply absent.
    param([string]$Text)
    $pattern = '(?m)^(' + ($overrideKeys -join '|') + '):[ \t]*'
    $matches = [regex]::Matches($Text, $pattern)
    $fields = @{}
    for ($i = 0; $i -lt $matches.Count; $i++) {
        $match = $matches[$i]
        $key = $match.Groups[1].Value
        $startIndex = $match.Index + $match.Length
        $endIndex = if ($i + 1 -lt $matches.Count) { $matches[$i + 1].Index } else { $Text.Length }
        $value = $Text.Substring($startIndex, $endIndex - $startIndex).Trim()
        if (-not $fields.ContainsKey($key)) { $fields[$key] = $value }
    }
    return $fields
}

function Assert-ValidOverride {
    # Throws a message naming the change on any structural or content defect.
    param([string]$ChangeName, [string]$OverridePath)
    if (-not (Test-Path -LiteralPath $OverridePath -PathType Leaf)) {
        throw "Change '$ChangeName' has a BLOCKED review with no override.md: $OverridePath"
    }
    $text = Get-Content -Raw -LiteralPath $OverridePath
    $fields = Read-OverrideFields $text
    foreach ($key in $overrideKeys) {
        if (-not $fields.ContainsKey($key) -or [string]::IsNullOrWhiteSpace([string]$fields[$key])) {
            throw "Change '$ChangeName' override.md is missing a non-empty '$key' field: $OverridePath"
        }
    }
    if ([string]$fields['date'] -notmatch '^\d{4}-\d{2}-\d{2}$') {
        throw "Change '$ChangeName' override.md 'date' field is not YYYY-MM-DD: $OverridePath"
    }
    return $fields
}

function Test-BlockedVerdict {
    # review.json (schema v1 single-reviewer or v2 council) both use a top-level `.verdict`
    # field with enum values including "BLOCK" (see review-schema.json and
    # council-review-schema.json). Any other shape is ignored rather than treated as blocked,
    # since this gate only concerns the recorded verdict, not review.json structural validity
    # (that is covered elsewhere, e.g. Test-1CSpecFinal.ps1).
    param([string]$ReviewJsonPath)
    if (-not (Test-Path -LiteralPath $ReviewJsonPath -PathType Leaf)) { return $false }
    try {
        $review = Get-Content -Raw -LiteralPath $ReviewJsonPath | ConvertFrom-Json
    } catch {
        return $false
    }
    return ([string]$review.verdict -ceq 'BLOCK')
}

$changeDirs = @(Get-ChildItem -LiteralPath $changesRoot -Directory | Where-Object { $_.Name -ne 'archive' } | Sort-Object Name)
Assert-G ($changeDirs.Count -gt 0) "No change directories found under $changesRoot."

foreach ($dir in $changeDirs) {
    $blockedMarker = Join-Path $dir.FullName 'review-blocked.md'
    $reviewJson = Join-Path $dir.FullName 'review.json'
    $overridePath = Join-Path $dir.FullName 'override.md'
    $hasBlockedMarker = Test-Path -LiteralPath $blockedMarker -PathType Leaf
    $hasBlockedVerdict = Test-BlockedVerdict $reviewJson
    if ($hasBlockedMarker -or $hasBlockedVerdict) {
        Assert-ValidOverride $dir.Name $overridePath | Out-Null
    } else {
        $script:checks++
    }
}

# --- Negative fixtures on a throwaway change tree: prove the gate actually fails closed. ---
$temp = Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-change-governance-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $temp -Force | Out-Null
try {
    function New-ChangeDir { param([string]$Name) $path = Join-Path $temp $Name; New-Item -ItemType Directory -Path $path -Force | Out-Null; return $path }
    function Invoke-GovernanceOnRoot {
        param([string]$FixtureRoot)
        $changes = @(Get-ChildItem -LiteralPath (Join-Path $FixtureRoot 'openspec/changes') -Directory | Where-Object { $_.Name -ne 'archive' })
        foreach ($dir in $changes) {
            $blockedMarker = Join-Path $dir.FullName 'review-blocked.md'
            $reviewJson = Join-Path $dir.FullName 'review.json'
            $overridePath = Join-Path $dir.FullName 'override.md'
            if ((Test-Path -LiteralPath $blockedMarker -PathType Leaf) -or (Test-BlockedVerdict $reviewJson)) {
                Assert-ValidOverride $dir.Name $overridePath | Out-Null
            }
        }
    }
    function Expect-GovernanceFailure {
        param([string]$FixtureRoot, [string]$Pattern, [string]$Message)
        $threw = $false
        try { Invoke-GovernanceOnRoot $FixtureRoot } catch { $threw = $true; Assert-G ($_.Exception.Message -match $Pattern) "$Message (wrong error: $($_.Exception.Message))" }
        Assert-G $threw "$Message (governance did not fail)."
    }

    # Fixture 1: review-blocked.md with no override.md at all -> fail.
    $fixture1 = Join-Path $temp 'fixture1'
    New-Item -ItemType Directory -Path (Join-Path $fixture1 'openspec/changes/no-override') -Force | Out-Null
    Set-Content -LiteralPath (Join-Path $fixture1 'openspec/changes/no-override/review-blocked.md') -Value '# blocked' -Encoding utf8
    Expect-GovernanceFailure $fixture1 "no-override.*no override\.md" 'Missing override.md must fail.'

    # Fixture 2: override.md present but missing a required field -> fail.
    $fixture2 = Join-Path $temp 'fixture2'
    New-Item -ItemType Directory -Path (Join-Path $fixture2 'openspec/changes/partial-override') -Force | Out-Null
    Set-Content -LiteralPath (Join-Path $fixture2 'openspec/changes/partial-override/review-blocked.md') -Value '# blocked' -Encoding utf8
    Set-Content -LiteralPath (Join-Path $fixture2 'openspec/changes/partial-override/override.md') -Value @"
owner: Test Owner
date: 2026-09-26
reason: because
"@ -Encoding utf8
    Expect-GovernanceFailure $fixture2 "partial-override.*accepted_risks" 'override.md missing accepted_risks must fail.'

    # Fixture 3: override.md present with an empty field -> fail.
    $fixture3 = Join-Path $temp 'fixture3'
    New-Item -ItemType Directory -Path (Join-Path $fixture3 'openspec/changes/empty-field') -Force | Out-Null
    Set-Content -LiteralPath (Join-Path $fixture3 'openspec/changes/empty-field/review-blocked.md') -Value '# blocked' -Encoding utf8
    Set-Content -LiteralPath (Join-Path $fixture3 'openspec/changes/empty-field/override.md') -Value @"
owner:
date: 2026-09-26
reason: because
accepted_risks: none
"@ -Encoding utf8
    Expect-GovernanceFailure $fixture3 "empty-field.*owner" 'override.md with an empty owner must fail.'

    # Fixture 4: override.md with a malformed date -> fail.
    $fixture4 = Join-Path $temp 'fixture4'
    New-Item -ItemType Directory -Path (Join-Path $fixture4 'openspec/changes/bad-date') -Force | Out-Null
    Set-Content -LiteralPath (Join-Path $fixture4 'openspec/changes/bad-date/review-blocked.md') -Value '# blocked' -Encoding utf8
    Set-Content -LiteralPath (Join-Path $fixture4 'openspec/changes/bad-date/override.md') -Value @"
owner: Test Owner
date: 26/09/2026
reason: because
accepted_risks: none
"@ -Encoding utf8
    Expect-GovernanceFailure $fixture4 "bad-date.*YYYY-MM-DD" 'override.md with a malformed date must fail.'

    # Fixture 5: review.json with verdict BLOCK and no override.md -> fail.
    $fixture5 = Join-Path $temp 'fixture5'
    New-Item -ItemType Directory -Path (Join-Path $fixture5 'openspec/changes/blocked-verdict') -Force | Out-Null
    Set-Content -LiteralPath (Join-Path $fixture5 'openspec/changes/blocked-verdict/review.json') -Value '{"schema_version":2,"verdict":"BLOCK"}' -Encoding utf8
    Expect-GovernanceFailure $fixture5 "blocked-verdict.*no override\.md" 'review.json verdict BLOCK with no override.md must fail.'

    # Fixture 6: complete, valid override.md -> passes.
    $fixture6 = Join-Path $temp 'fixture6'
    New-Item -ItemType Directory -Path (Join-Path $fixture6 'openspec/changes/valid-override') -Force | Out-Null
    Set-Content -LiteralPath (Join-Path $fixture6 'openspec/changes/valid-override/review-blocked.md') -Value '# blocked' -Encoding utf8
    Set-Content -LiteralPath (Join-Path $fixture6 'openspec/changes/valid-override/override.md') -Value @"
owner: Test Owner
date: 2026-09-26
reason: because it was approved
accepted_risks:
  risk_one: some accepted risk
"@ -Encoding utf8
    Invoke-GovernanceOnRoot $fixture6
    $script:checks++

    # Fixture 7: change without review-blocked.md or a BLOCK verdict needs no override.md.
    $fixture7 = Join-Path $temp 'fixture7'
    New-Item -ItemType Directory -Path (Join-Path $fixture7 'openspec/changes/clean-change') -Force | Out-Null
    Set-Content -LiteralPath (Join-Path $fixture7 'openspec/changes/clean-change/review.json') -Value '{"schema_version":2,"verdict":"PASS"}' -Encoding utf8
    Invoke-GovernanceOnRoot $fixture7
    $script:checks++

    # Fixture 8: archive/ is excluded even when it contains a BLOCKED marker with no override.
    $fixture8 = Join-Path $temp 'fixture8'
    New-Item -ItemType Directory -Path (Join-Path $fixture8 'openspec/changes/archive/old-blocked') -Force | Out-Null
    Set-Content -LiteralPath (Join-Path $fixture8 'openspec/changes/archive/old-blocked/review-blocked.md') -Value '# blocked' -Encoding utf8
    Invoke-GovernanceOnRoot $fixture8
    $script:checks++
}
finally {
    $resolved = [IO.Path]::GetFullPath($temp)
    $tempRoot = [IO.Path]::GetFullPath([IO.Path]::GetTempPath())
    if ($resolved.StartsWith($tempRoot, [StringComparison]::OrdinalIgnoreCase) -and (Split-Path -Leaf $resolved) -like 'bsl-flow-change-governance-*') {
        Remove-Item -LiteralPath $resolved -Recurse -Force -ErrorAction SilentlyContinue
    }
}

Write-Output "CHANGE_GOVERNANCE_OK checks=$script:checks"
