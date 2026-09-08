[CmdletBinding()]
param([string]$PackageRoot)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($PackageRoot)) { $PackageRoot = Split-Path -Parent $PSScriptRoot }
$packageRoot = [System.IO.Path]::GetFullPath($PackageRoot)
$auditRoot = Join-Path $packageRoot 'global\skills\1c-init-project\scripts'
$writer = Join-Path $auditRoot 'Write-AgentAuditEvent.ps1'
$summary = Join-Path $auditRoot 'Get-AgentAuditSummary.ps1'
$importer = Join-Path $auditRoot 'Import-AgentAuditUsage.ps1'
foreach ($file in @($writer, $summary, $importer, (Join-Path $auditRoot '..\references\agent-audit.md'), (Join-Path $auditRoot '..\references\agent-audit-schema.json'))) {
    if (-not (Test-Path -LiteralPath $file -PathType Leaf)) { throw "Missing agent-audit package file: $file" }
}

function Assert-True { param([Parameter(Mandatory)][bool]$Condition, [Parameter(Mandatory)][string]$Message) if (-not $Condition) { throw "ASSERTION FAILED: $Message" } }
function New-Event {
    param([Parameter(Mandatory)][string]$EventId, [Parameter(Mandatory)][string]$AttemptId, [Parameter(Mandatory)][string]$Type, [hashtable]$Extra)
    $e = [ordered]@{
        schema_version = 1; event_id = $EventId; run_id = 'run-test'; task_id = 'task-test'; attempt_id = $AttemptId; agent_id = 'agent-child'; parent_agent_id = 'agent-parent'
        role = 'implementation'; complexity = 'M'; event_type = $Type; emitted_at_utc = '2026-09-03T12:00:00Z'
        requested_model = 'gpt-test'; requested_effort = 'high'; observed_model = $null; observed_model_unavailable_reason = 'test_runtime_not_exposed'; observed_effort = $null; observed_effort_unavailable_reason = 'test_runtime_not_exposed'
        model_source = 'parent_routing_policy'; routing_rule = 'test AGENTS.md'; routing_reason = 'bounded test'; result_ref = $null; result_ref_unavailable_reason = 'test'
    }
    if ($null -ne $Extra) { foreach ($key in $Extra.Keys) { $e[$key] = $Extra[$key] } }
    return $e
}
function Append-Event {
    param([Parameter(Mandatory)][string]$Root, [Parameter(Mandatory)][object]$Event)
    $json = $Event | ConvertTo-Json -Depth 20 -Compress
    return & $writer -ProjectPath $Root -RunId 'run-test' -ReportRoot (Join-Path $Root 'reports') -EventJson $json
}
function Expect-Failure {
    param([Parameter(Mandatory)][scriptblock]$Action, [Parameter(Mandatory)][string]$Message)
    $failed = $false
    try { & $Action | Out-Null } catch { $failed = $true }
    Assert-True $failed $Message
}

$temp = Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-agent-audit-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $temp -Force | Out-Null
try {
    $delegated = New-Event 'evt-delegated' 'attempt-success' 'delegated' $null
    $started = New-Event 'evt-started' 'attempt-success' 'started' @{ emitted_at_utc = '2026-09-03T12:00:01Z' }
    $completed = New-Event 'evt-completed' 'attempt-success' 'completed' @{ emitted_at_utc = '2026-09-03T12:00:02Z' }
    Append-Event $temp $delegated | Out-Null; Append-Event $temp $started | Out-Null; Append-Event $temp $completed | Out-Null
    $same = Append-Event $temp $completed
    Assert-True ($same.outcome -eq 'deduplicated') 'Identical event was not deduplicated.'
    $conflict = New-Event 'evt-completed' 'attempt-success' 'completed' @{ emitted_at_utc = '2026-09-03T12:02:00Z' }
    Expect-Failure { Append-Event $temp $conflict } 'Conflicting event_id was accepted.'
    Expect-Failure { Append-Event $temp (New-Event 'evt-sensitive' 'attempt-sensitive' 'delegated' @{ prompt = 'must not be persisted' }) } 'Sensitive prompt property was accepted.'
    Expect-Failure { Append-Event $temp (New-Event 'evt-sensitive-array' 'attempt-sensitive-array' 'delegated' @{ evidence = @(@{ message = 'must not be persisted' }) }) } 'Sensitive property nested in an array was accepted.'
    Expect-Failure { Append-Event $temp (New-Event 'evt-unknown-usage' 'attempt-unknown-usage' 'delegated' @{ usage = @{ mode = 'cumulative'; measurement_id = 'm1'; total_tokens = $null; provenance = 'test' } }) } 'Unavailable usage without reason was accepted.'
    Expect-Failure { Append-Event $temp (New-Event 'evt-fractional-usage' 'attempt-fractional-usage' 'delegated' @{ usage = @{ mode = 'delta'; measurement_id = 'm1'; total_tokens = 1.5; provenance = 'test' } }) } 'Fractional token count was accepted.'
    Expect-Failure { Append-Event $temp (New-Event 'evt-delta-id' 'attempt-delta-id' 'delegated' @{ usage = @{ mode = 'delta'; total_tokens = 1; provenance = 'test' } }) } 'Delta usage without measurement id was accepted.'
    Expect-Failure { Append-Event $temp (New-Event 'evt-failed-no-class' 'attempt-failed-no-class' 'failed' $null) } 'Failed event without classification was accepted.'
    Expect-Failure { Append-Event $temp (New-Event 'evt-early' 'attempt-early' 'started' $null) } 'Lifecycle allowed started without delegated.'

    $corrBase = New-Event 'evt-corr-delegated' 'attempt-correction' 'delegated' $null
    $corrDone = New-Event 'evt-corr-completed' 'attempt-correction' 'completed' $null
    $corr = New-Event 'evt-corr-fixed' 'attempt-correction' 'corrected' @{ correction_of = 'evt-corr-completed'; correction_actor = 'parent'; correction_action = 'narrowed_scope'; correction_result = 'ready_for_recheck' }
    $corrReject = New-Event 'evt-corr-reject' 'attempt-correction' 'acceptance' @{ acceptance_state = 'rejected'; acceptance_actor = 'parent' }
    $corrAccept = New-Event 'evt-corr-accept' 'attempt-correction' 'acceptance' @{ acceptance_state = 'accepted'; acceptance_actor = 'parent' }
    Append-Event $temp $corrBase | Out-Null; Append-Event $temp $corrDone | Out-Null; Append-Event $temp $corrReject | Out-Null; Append-Event $temp $corr | Out-Null; Append-Event $temp $corrAccept | Out-Null

    foreach ($case in @(
        @{ attempt = 'attempt-escalation'; events = @(
            (New-Event 'evt-esc-delegated' 'attempt-escalation' 'delegated' $null),
            (New-Event 'evt-esc' 'attempt-escalation' 'escalated' @{ from_model = 'gpt-test'; to_model = 'gpt-other'; escalation_reason = 'tool failure' }),
            (New-Event 'evt-esc-done' 'attempt-escalation' 'completed' $null)
        ) },
        @{ attempt = 'attempt-cancel'; events = @(
            (New-Event 'evt-can-delegated' 'attempt-cancel' 'delegated' $null),
            (New-Event 'evt-can' 'attempt-cancel' 'cancelled' $null),
            (New-Event 'evt-can-accept' 'attempt-cancel' 'acceptance' @{ acceptance_state = 'cancelled'; acceptance_actor = 'parent' })
        ) },
        @{ attempt = 'attempt-unknown'; events = @(
            (New-Event 'evt-unk-delegated' 'attempt-unknown' 'delegated' $null),
            (New-Event 'evt-unk' 'attempt-unknown' 'unknown' @{ unknown_reason = 'runtime metadata absent' })
        ) },
        @{ attempt = 'attempt-task-change'; events = @(
            (New-Event 'evt-chg-delegated' 'attempt-task-change' 'delegated' $null),
            (New-Event 'evt-chg' 'attempt-task-change' 'task_changed' @{ task_change_reason = 'parent clarified acceptance criterion' })
        ) },
        @{ attempt = 'attempt-error'; events = @(
            (New-Event 'evt-err-delegated' 'attempt-error' 'delegated' $null),
            (New-Event 'evt-err-failed' 'attempt-error' 'failed' @{ error_class = 'tool'; error_evidence_ref = 'evidence/tool-error.txt' }),
            (New-Event 'evt-err-accept' 'attempt-error' 'acceptance' @{ acceptance_state = 'rejected'; acceptance_actor = 'parent' })
        ) }
    )) { foreach ($event in $case.events) { Append-Event $temp $event | Out-Null } }

    $parallelA = New-Event 'evt-par-a' 'attempt-par-a' 'delegated' @{ role = 'review'; complexity = 'S' }
    $parallelB = New-Event 'evt-par-b' 'attempt-par-b' 'delegated' @{ role = 'review'; complexity = 'S' }
    Append-Event $temp $parallelB | Out-Null; Append-Event $temp $parallelA | Out-Null
    $summaryResult = & $summary -ReportRoot (Join-Path $temp 'reports')
    $summaryPath = Join-Path $temp 'reports\summary.md'
    Assert-True (Test-Path -LiteralPath $summaryPath -PathType Leaf) 'Summary was not regenerated.'
    $summaryText = Get-Content -Raw -LiteralPath $summaryPath
    Assert-True ($summaryText -match '\(n\)') 'Summary has no attempt denominator.'
    Assert-True ($summaryText -match 'accepted_after_correction=1') 'Summary has no correction metric.'
    Assert-True ($summaryText -match 'leaderboard') 'Summary lacks no-leaderboard boundary.'
    Assert-True ($summaryText -notmatch 'gpt-other.*leaderboard') 'Summary contains a model leaderboard.'

    $session = Join-Path $temp 'session.jsonl'
    $records = @(
        ([ordered]@{ timestamp = '2026-09-03T12:00:00Z'; ordinal = 0; type = 'session_meta'; payload = [ordered]@{ id = 'child-1'; parent_thread_id = 'parent-1'; agent_path = '/root/child-1'; source = [ordered]@{ subagent = [ordered]@{ thread_spawn = [ordered]@{ parent_thread_id = 'parent-1'; agent_path = '/root/child-1' } } } } }),
        ([ordered]@{ timestamp = '2026-09-03T12:00:01Z'; ordinal = 1; type = 'turn_context'; payload = [ordered]@{ model = 'gpt-test'; effort = 'high' } }),
        ([ordered]@{ timestamp = '2026-09-03T12:00:02Z'; ordinal = 2; type = 'event_msg'; payload = [ordered]@{ type = 'token_count'; info = [ordered]@{ total_token_usage = [ordered]@{ total_tokens = 100; input_tokens = 90; output_tokens = 10; cached_input_tokens = 80 } } } }),
        ([ordered]@{ timestamp = '2026-09-03T12:00:03Z'; ordinal = 3; type = 'response_item'; payload = [ordered]@{ role = 'assistant'; content = 'must not be imported' } }),
        ([ordered]@{ timestamp = '2026-09-03T12:00:04Z'; ordinal = 4; type = 'event_msg'; payload = [ordered]@{ type = 'token_count'; info = [ordered]@{ total_token_usage = [ordered]@{ total_tokens = 130; input_tokens = 110; output_tokens = 20; cached_input_tokens = 100 } } } })
    )
    $records | ForEach-Object { ($_ | ConvertTo-Json -Depth 12 -Compress) } | Set-Content -LiteralPath $session -Encoding utf8
    $usage = & $importer -SessionPath $session -ExpectedSessionId 'child-1' -ExpectedParentThreadId 'parent-1' -ExpectedAgentPath '/root/child-1' -BaselineTotalTokens 100
    Assert-True ([math]::Abs($usage.usage.total_tokens - 30) -lt 0.001) 'Importer did not apply explicit baseline.'
    Assert-True ($usage.partial -eq $false) 'Importer marked one-turn usage partial.'
    Assert-True ($usage.observed_model -eq 'gpt-test' -and $usage.observed_effort -eq 'high') 'Importer did not map preceding turn context.'
    Assert-True ($usage.source.record_types -notcontains 'response_item') 'Importer exposed disallowed record type.'
    $partialSession = Join-Path $temp 'session-partial.jsonl'
    @($records[0], $records[2]) | ForEach-Object { ($_ | ConvertTo-Json -Depth 12 -Compress) } | Set-Content -LiteralPath $partialSession -Encoding utf8
    $partialUsage = & $importer -SessionPath $partialSession -ExpectedSessionId 'child-1' -ExpectedParentThreadId 'parent-1' -ExpectedAgentPath '/root/child-1' -BaselineTotalTokens 0
    Assert-True ($partialUsage.partial -eq $true -and $partialUsage.usage.completeness -eq 'partial') 'Importer claimed complete usage without preceding turn context.'
    Expect-Failure { & $importer -SessionPath $session -ExpectedSessionId 'child-1' -ExpectedParentThreadId 'parent-1' -ExpectedAgentPath '/root/child-1' -BaselineTotalTokens 0.5 | Out-Null } 'Importer accepted fractional token baseline.'
    Write-Host 'All agent audit tests passed.'
}
finally {
    $resolved = [IO.Path]::GetFullPath($temp); $tempRoot = [IO.Path]::GetFullPath([IO.Path]::GetTempPath())
    if ($resolved.StartsWith($tempRoot, [StringComparison]::OrdinalIgnoreCase) -and (Split-Path -Leaf $resolved) -like 'bsl-flow-agent-audit-*') { Remove-Item -LiteralPath $resolved -Recurse -Force -ErrorAction SilentlyContinue }
}
