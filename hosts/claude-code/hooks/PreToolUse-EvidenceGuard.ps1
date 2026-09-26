#Requires -Version 7.0
# PreToolUse hook (Edit|Write|MultiEdit): denies direct edits to files that
# only BSL Flow scripts should write (lint/review/final-validation evidence
# and the active-change pointer). review-reconciliation.json is explicitly
# allowed: the agent writes it by contract, and Test-1CSpecFinal.ps1 checks
# its schema and hashes.
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'Hooks.Common.ps1')

try {
    $input_ = Read-BFHookStdin
    if ($null -eq $input_) { exit 0 }

    $projectRoot = $null
    try { $projectRoot = [string]$input_.cwd } catch { $projectRoot = $null }
    if ([string]::IsNullOrWhiteSpace($projectRoot)) { exit 0 }

    $filePath = Get-BFHookFilePath -ToolInput $input_.tool_input
    if ([string]::IsNullOrWhiteSpace($filePath)) { exit 0 }

    $fullPath = try {
        if ([IO.Path]::IsPathRooted($filePath)) { [IO.Path]::GetFullPath($filePath) } else { [IO.Path]::GetFullPath((Join-Path $projectRoot $filePath)) }
    }
    catch { $null }
    if ($null -eq $fullPath) { exit 0 }

    $rootFull = [IO.Path]::GetFullPath($projectRoot).TrimEnd('\', '/')
    $relative = $fullPath.Substring([Math]::Min($rootFull.Length, $fullPath.Length)).TrimStart('\', '/')
    $normalized = $relative.Replace('\', '/')

    if ($normalized -match '(?i)^openspec/changes/[^/]+/review-reconciliation\.json$') { exit 0 }

    $deniedPatterns = @(
        '(?i)^openspec/changes/[^/]+/spec-lint\.json$',
        '(?i)^openspec/changes/[^/]+/review\.json$',
        '(?i)^openspec/changes/[^/]+/final-validation\.json$',
        '(?i)^\.bsl-flow/evidence/',
        '(?i)^\.bsl-flow/active-change\.json$'
    )
    foreach ($pattern in $deniedPatterns) {
        if ($normalized -match $pattern) {
            Write-BFHookDeny -Reason "'$normalized' is BSL Flow evidence written only by its own scripts (lint/review/final-validation/active-change tracking), not directly by an agent. Run the corresponding skill script instead."
        }
    }

    exit 0
}
catch {
    [Console]::Error.WriteLine("bsl-flow PreToolUse-EvidenceGuard: unexpected error, allowing: $($_.Exception.Message)")
    exit 0
}
