[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)] [string]$SessionPath,
    [Parameter(Mandatory = $true)] [string]$ExpectedSessionId,
    [Parameter(Mandatory = $true)] [string]$ExpectedParentThreadId,
    [Parameter(Mandatory = $true)] [string]$ExpectedAgentPath,
    [Parameter(Mandatory = $true)] [double]$BaselineTotalTokens,
    [string]$OutputPath
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

if (-not (Test-Path -LiteralPath $SessionPath -PathType Leaf)) { throw "Session JSONL was not found: $SessionPath" }
if ($BaselineTotalTokens -lt 0) { throw 'BaselineTotalTokens must be non-negative.' }
if ($BaselineTotalTokens -ne [math]::Truncate($BaselineTotalTokens)) { throw 'BaselineTotalTokens must be an integer count.' }
foreach ($name in @('ExpectedSessionId', 'ExpectedParentThreadId', 'ExpectedAgentPath')) { if ([string]::IsNullOrWhiteSpace((Get-Variable -Name $name -ValueOnly))) { throw "$name must not be empty." } }

function Get-Prop {
    param([AllowNull()][object]$Object, [Parameter(Mandatory)][string]$Name)
    if ($null -eq $Object) { return $null }
    $property = $Object.PSObject.Properties[$Name]
    if ($null -eq $property) { return $null }
    return $property.Value
}

$sessionMeta = $null
$contexts = [System.Collections.Generic.List[object]]::new()
$tokenEvents = [System.Collections.Generic.List[object]]::new()
$lastContextModel = $null; $lastContextEffort = $null
$tokenContextMissing = $false
$ordinal = 0
foreach ($line in @(Get-Content -LiteralPath $SessionPath)) {
    if ([string]::IsNullOrWhiteSpace($line)) { continue }
    $ordinal++
    try { $record = $line | ConvertFrom-Json -ErrorAction Stop } catch { throw "Invalid JSONL at line $ordinal; import refused." }
    $type = if ($null -ne $record.PSObject.Properties['type']) { [string]$record.type } else { '' }
    if ($type -eq 'session_meta') {
        if ($null -ne $sessionMeta) { throw 'More than one session_meta record; import refused.' }
        $sessionMeta = $record
    } elseif ($type -eq 'turn_context') {
        $p = Get-Prop $record 'payload'
        if ($null -ne $p) {
            $lastContextModel = if ($null -ne (Get-Prop $p 'model')) { [string](Get-Prop $p 'model') } else { $null }
            $lastContextEffort = if ($null -ne (Get-Prop $p 'effort')) { [string](Get-Prop $p 'effort') } else { $null }
            $contexts.Add([pscustomobject]@{ ordinal = $ordinal; model = $lastContextModel; effort = $lastContextEffort })
        }
    } elseif ($type -eq 'event_msg' -and $null -ne (Get-Prop $record 'payload') -and [string](Get-Prop (Get-Prop $record 'payload') 'type') -eq 'token_count') {
        $info = Get-Prop (Get-Prop $record 'payload') 'info'; $usage = Get-Prop $info 'total_token_usage'
        if ($null -ne $usage -and $null -ne (Get-Prop $usage 'total_tokens')) {
            $tokenContextMissing = $tokenContextMissing -or ($null -eq $lastContextModel -or $null -eq $lastContextEffort)
            $tokenEvents.Add([pscustomobject]@{ ordinal = $ordinal; total = [double](Get-Prop $usage 'total_tokens'); input = if ($null -ne (Get-Prop $usage 'input_tokens')) { [double](Get-Prop $usage 'input_tokens') } else { $null }; output = if ($null -ne (Get-Prop $usage 'output_tokens')) { [double](Get-Prop $usage 'output_tokens') } else { $null }; model = $lastContextModel; effort = $lastContextEffort })
        }
    }
    # response_item and all other record types are intentionally ignored. Their text is never copied.
}
if ($null -eq $sessionMeta) { throw 'session_meta record was not found; import refused.' }
$payload = Get-Prop $sessionMeta 'payload'
if ($null -eq $payload) { throw 'session_meta.payload is missing; import refused.' }
$actualSessionId = [string](Get-Prop $payload 'id')
$actualParent = [string](Get-Prop $payload 'parent_thread_id')
$source = Get-Prop $payload 'source'; $subagent = Get-Prop $source 'subagent'; $spawn = Get-Prop $subagent 'thread_spawn'
$actualSpawnParent = [string](Get-Prop $spawn 'parent_thread_id')
$actualPath = if ($null -ne (Get-Prop $payload 'agent_path')) { [string](Get-Prop $payload 'agent_path') } elseif ($null -ne (Get-Prop $spawn 'agent_path')) { [string](Get-Prop $spawn 'agent_path') } else { '' }
if ($actualSessionId -ne $ExpectedSessionId) { throw "Session id mismatch; expected '$ExpectedSessionId', observed '$actualSessionId'." }
if ($actualParent -ne $ExpectedParentThreadId -and $actualSpawnParent -ne $ExpectedParentThreadId) { throw "Parent thread id mismatch; expected '$ExpectedParentThreadId'." }
if ($actualPath -ne $ExpectedAgentPath) { throw "Agent path mismatch; expected '$ExpectedAgentPath', observed '$actualPath'." }
if ($tokenEvents.Count -eq 0) { throw 'No usable token_count total_token_usage event was found; usage is unavailable.' }

$previous = -1.0
foreach ($token in $tokenEvents) {
    if ($token.total -lt $previous) { throw 'Cumulative total_tokens decreased; baseline/attempt mapping is ambiguous.' }
    $previous = $token.total
}
$last = @($tokenEvents | Select-Object -Last 1)[0]
if ($last.total -lt $BaselineTotalTokens) { throw 'BaselineTotalTokens is greater than the observed cumulative total.' }
$models = @($tokenEvents | Where-Object { $null -ne $_.model } | Select-Object -ExpandProperty model -Unique)
$efforts = @($tokenEvents | Where-Object { $null -ne $_.effort } | Select-Object -ExpandProperty effort -Unique)
$componentInput = @($tokenEvents | Select-Object -Last 1)[0].input
$componentOutput = @($tokenEvents | Select-Object -Last 1)[0].output
$partial = ($models.Count -ne 1 -or $efforts.Count -ne 1 -or $contexts.Count -ne 1 -or $tokenContextMissing)
$result = [ordered]@{
    schema_version = 1
    source = [ordered]@{ kind = 'explicit_local_session_file'; session_id = $actualSessionId; parent_thread_id = $ExpectedParentThreadId; agent_path = $actualPath; record_types = @('session_meta', 'turn_context', 'event_msg.token_count') }
    usage = [ordered]@{ mode = 'cumulative'; measurement_id = ('session-' + $actualSessionId); total_tokens = ($last.total - $BaselineTotalTokens); provenance = 'explicit_local_session_file'; completeness = if ($partial) { 'partial' } else { 'complete' }; unavailable_reason = if ($partial) { 'session contains multiple turn_context records, missing preceding context, or ambiguous model/effort; session total is not asserted as one exact attempt' } else { $null } }
    observed_model = if ($models.Count -eq 1) { $models[0] } else { $null }
    observed_effort = if ($efforts.Count -eq 1) { $efforts[0] } else { $null }
    observed_model_unavailable_reason = if ($models.Count -eq 1) { $null } else { 'no single preceding turn_context model' }
    observed_effort_unavailable_reason = if ($efforts.Count -eq 1) { $null } else { 'no single preceding turn_context effort' }
    components = if ($BaselineTotalTokens -eq 0 -and $null -ne $componentInput -and $null -ne $componentOutput) { [ordered]@{ input_tokens = $componentInput; output_tokens = $componentOutput; provenance = 'explicit_local_session_file_baseline_zero' } } else { $null }
    partial = $partial
    limitation = 'Observed private session format; not a stable public API. Baseline was supplied explicitly; rate_limits, last_token_usage and all text records were ignored.'
}
$json = $result | ConvertTo-Json -Depth 12 -Compress
if (-not [string]::IsNullOrWhiteSpace($OutputPath)) {
    $parent = Split-Path -Parent $OutputPath; New-Item -ItemType Directory -Path $parent -Force | Out-Null
    [IO.File]::WriteAllText($OutputPath, $json + "`n", [Text.UTF8Encoding]::new($false))
}
[pscustomobject]$result
