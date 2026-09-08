[CmdletBinding(DefaultParameterSetName = 'Json')]
param(
    [Parameter(Mandatory = $true)] [string]$ProjectPath,
    [Parameter(Mandatory = $true)] [string]$RunId,
    [Parameter(Mandatory = $true, ParameterSetName = 'Json')] [string]$EventJson,
    [Parameter(Mandatory = $true, ParameterSetName = 'Path')] [string]$EventPath,
    [string]$ReportRoot
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Get-CanonicalValue {
    param([AllowNull()][object]$Value)
    if ($null -eq $Value) { return $null }
    if ($Value -is [System.Collections.IDictionary]) {
        $result = [ordered]@{}
        foreach ($key in @($Value.Keys | Sort-Object)) { $result[[string]$key] = Get-CanonicalValue $Value[$key] }
        return $result
    }
    if ($Value -is [pscustomobject]) {
        $result = [ordered]@{}
        foreach ($property in @($Value.PSObject.Properties | Sort-Object Name)) {
            $result[$property.Name] = Get-CanonicalValue $property.Value
        }
        return $result
    }
    if (($Value -is [System.Collections.IEnumerable]) -and -not ($Value -is [string])) {
        return @($Value | ForEach-Object { Get-CanonicalValue $_ })
    }
    return $Value
}

function Get-CanonicalJson {
    param([AllowNull()][object]$Value)
    return (Get-CanonicalValue $Value | ConvertTo-Json -Depth 30 -Compress)
}

function Get-TextProperty {
    param([Parameter(Mandatory)][object]$Object, [Parameter(Mandatory)][string]$Name)
    $property = $Object.PSObject.Properties[$Name]
    if ($null -eq $property -or $null -eq $property.Value) { return $null }
    return ([string]$property.Value).Trim()
}

function Require-Text {
    param([Parameter(Mandatory)][object]$Object, [Parameter(Mandatory)][string]$Name)
    $value = Get-TextProperty $Object $Name
    if ([string]::IsNullOrWhiteSpace($value)) { throw "Event property '$Name' is required." }
    return $value
}

function Get-PropertyOrNull {
    param([Parameter(Mandatory)][object]$Object, [Parameter(Mandatory)][string]$Name)
    $property = $Object.PSObject.Properties[$Name]
    if ($null -eq $property) { return $null }
    return $property.Value
}

function Test-NullWithReason {
    param([Parameter(Mandatory)][object]$Object, [Parameter(Mandatory)][string]$ValueName, [Parameter(Mandatory)][string]$ReasonName)
    $value = Get-PropertyOrNull $Object $ValueName
    if ($null -eq $value -or ([string]::IsNullOrWhiteSpace([string]$value) -and $value -is [string])) {
        $reason = Get-TextProperty $Object $ReasonName
        if ([string]::IsNullOrWhiteSpace($reason)) { throw "'$ValueName' is null/unavailable; '$ReasonName' must explain why." }
    }
}

function Assert-NoSensitiveProperties {
    param([AllowNull()][object]$Object, [string]$Path = 'event')
    if ($null -eq $Object -or $Object -is [string] -or $Object.GetType().IsValueType) { return }
    if (($Object -is [System.Collections.IEnumerable]) -and -not ($Object -is [System.Collections.IDictionary]) -and -not ($Object -is [pscustomobject])) {
        foreach ($item in $Object) { Assert-NoSensitiveProperties -Object $item -Path ($Path + '[]') }
        return
    }
    $properties = if ($Object -is [System.Collections.IDictionary]) { @($Object.Keys | ForEach-Object { [pscustomobject]@{ Name = [string]$_; Value = $Object[$_] } }) } elseif ($Object -is [pscustomobject]) { @($Object.PSObject.Properties | ForEach-Object { [pscustomobject]@{ Name = $_.Name; Value = $_.Value } }) } else { @() }
    foreach ($property in $properties) {
        if ($property.Name -match '(?i)(prompt|message|conversation|(^|_)log($|_)|chain.?of.?thought|(^|_)cot($|_)|rate.?limit|account.?limit|password|secret|credential)') { throw "Sensitive property is not allowed in audit event: $Path.$($property.Name)" }
        Assert-NoSensitiveProperties -Object $property.Value -Path ($Path + '.' + $property.Name)
    }
}

function Assert-AuditEvent {
    param([Parameter(Mandatory)][object]$Event, [Parameter(Mandatory)][string]$ExpectedRunId)
    Assert-NoSensitiveProperties $Event
    $schema = Get-PropertyOrNull $Event 'schema_version'
    if ($null -eq $schema -or $schema -isnot [int] -and $schema -isnot [long] -and $schema -isnot [double]) { throw 'schema_version must be a JSON number.' }
    if ([double]$schema -ne [math]::Truncate([double]$schema) -or [double]$schema -ne 1) { throw "Unsupported agent audit schema_version: $schema" }
    $eventId = Require-Text $Event 'event_id'
    if ($eventId -notmatch '^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$') { throw "Invalid event_id: $eventId" }
    $run = Require-Text $Event 'run_id'
    if ($run -ne $ExpectedRunId) { throw "Event run_id '$run' differs from -RunId '$ExpectedRunId'." }
    foreach ($idName in @('task_id', 'attempt_id', 'agent_id', 'parent_agent_id')) {
        $id = Require-Text $Event $idName
        if ($id -notmatch '^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$') { throw "Invalid ${idName}: $id" }
    }
    foreach ($name in @('role', 'complexity', 'event_type', 'emitted_at_utc')) { [void](Require-Text $Event $name) }
    $complexity = Get-TextProperty $Event 'complexity'
    if ($complexity -notin @('S', 'M', 'L')) { throw "complexity must be S, M or L." }
    $eventType = Get-TextProperty $Event 'event_type'
    $allowed = @('delegated', 'started', 'completed', 'failed', 'interrupted', 'cancelled', 'corrected', 'escalated', 'acceptance', 'task_changed', 'unknown')
    if ($eventType -notin $allowed) { throw "Unsupported event_type '$eventType'." }
    try { [DateTime]::Parse($Event.emitted_at_utc, [Globalization.CultureInfo]::InvariantCulture, [Globalization.DateTimeStyles]::RoundtripKind) | Out-Null }
    catch { throw 'emitted_at_utc must be an RFC 3339/ISO-8601 timestamp.' }

    foreach ($name in @('requested_model', 'requested_effort', 'observed_model', 'observed_effort', 'routing_rule', 'routing_reason', 'model_source')) {
        Test-NullWithReason $Event $name ($name + '_unavailable_reason')
    }
    if ($eventType -eq 'escalated') {
        foreach ($name in @('from_model', 'to_model', 'escalation_reason')) { [void](Require-Text $Event $name) }
    }
    $errorClass = Get-TextProperty $Event 'error_class'
    if ($eventType -eq 'failed' -and $null -eq $errorClass) { throw "Failed event requires error_class." }
    if ($null -ne $errorClass) {
        if ($errorClass -notin @('model', 'tool', 'environment', 'decomposition', 'unknown')) { throw "Invalid error_class '$errorClass'." }
        Test-NullWithReason $Event 'error_evidence_ref' 'error_evidence_unavailable_reason'
    }
    if ($eventType -eq 'corrected') {
        foreach ($name in @('correction_of', 'correction_actor', 'correction_action', 'correction_result')) { [void](Require-Text $Event $name) }
    }
    if ($eventType -eq 'task_changed') { [void](Require-Text $Event 'task_change_reason') }
    if ($eventType -eq 'unknown') { [void](Require-Text $Event 'unknown_reason') }
    if ($eventType -eq 'acceptance') {
        $state = Require-Text $Event 'acceptance_state'
        if ($state -notin @('accepted', 'rejected', 'blocked', 'cancelled')) { throw "Invalid acceptance_state '$state'." }
        [void](Require-Text $Event 'acceptance_actor')
    }
    $usage = Get-PropertyOrNull $Event 'usage'
    if ($null -ne $usage) {
        $mode = Require-Text $usage 'mode'
        if ($mode -notin @('delta', 'cumulative', 'unknown')) { throw "usage.mode must be delta, cumulative or unknown." }
        [void](Require-Text $usage 'provenance')
        if ($mode -eq 'unknown') {
            if ($null -ne (Get-PropertyOrNull $usage 'total_tokens')) { throw 'usage.total_tokens must be null when usage.mode is unknown.' }
            [void](Require-Text $usage 'unavailable_reason')
        }
        if ($mode -ne 'unknown') {
            $total = Get-PropertyOrNull $usage 'total_tokens'
            if ($null -ne $total -and ($total -isnot [int] -and $total -isnot [long] -and $total -isnot [double])) { throw 'usage.total_tokens must be a JSON number or null.' }
            if ($null -ne $total -and [double]$total -ne [math]::Truncate([double]$total)) { throw 'usage.total_tokens must be an integer count.' }
            if ($null -ne $total -and [double]$total -lt 0) { throw 'usage.total_tokens cannot be negative.' }
            if ($null -eq $total) { [void](Require-Text $usage 'unavailable_reason') }
            [void](Require-Text $usage 'measurement_id')
        }
    }
    $timing = Get-PropertyOrNull $Event 'timing'
    if ($null -ne $timing) {
        [void](Require-Text $timing 'provenance')
        $duration = Get-PropertyOrNull $timing 'duration_ms'
        if ($null -eq $duration) { [void](Require-Text $timing 'unavailable_reason') }
        elseif ($duration -isnot [int] -and $duration -isnot [long] -and $duration -isnot [double]) { throw 'timing.duration_ms must be a JSON number or null.' }
        elseif ([double]$duration -lt 0) { throw 'timing.duration_ms cannot be negative.' }
    }
}

function Assert-Lifecycle {
    param([Parameter(Mandatory)][AllowEmptyCollection()][object[]]$Events, [Parameter(Mandatory)][object]$Candidate)
    $attemptEvents = @($Events | Where-Object { (Get-TextProperty $_ 'task_id') -eq (Get-TextProperty $Candidate 'task_id') -and (Get-TextProperty $_ 'attempt_id') -eq (Get-TextProperty $Candidate 'attempt_id') })
    $candidateType = Get-TextProperty $Candidate 'event_type'
    $types = @($attemptEvents | ForEach-Object { Get-TextProperty $_ 'event_type' })
    if ($types.Count -eq 0 -and $candidateType -ne 'delegated') { throw "The first event for an attempt must be delegated." }
    if ($candidateType -eq 'delegated' -and $types.Count -gt 0) { throw 'An attempt may have only one delegated event.' }
    foreach ($name in @('agent_id', 'parent_agent_id', 'role', 'complexity')) {
        $previous = @($attemptEvents | Select-Object -First 1 | ForEach-Object { Get-TextProperty $_ $name })
        if ($previous.Count -gt 0 -and $previous[0] -ne (Get-TextProperty $Candidate $name)) { throw "Attempt identity property '$name' changed within the attempt." }
    }
    if ($candidateType -in @('started','completed','failed','interrupted','cancelled','escalated','corrected','acceptance','task_changed','unknown') -and $types -notcontains 'delegated') { throw "Event '$candidateType' requires a prior delegated event." }
    if ($candidateType -eq 'acceptance') {
        if ($types -notcontains 'completed' -and $types -notcontains 'failed' -and $types -notcontains 'interrupted' -and $types -notcontains 'cancelled') { throw 'Parent acceptance requires a terminal child event first.' }
    }
    if ($candidateType -eq 'corrected') {
        $target = Get-TextProperty $Candidate 'correction_of'
        if (@($attemptEvents | Where-Object { (Get-TextProperty $_ 'event_id') -eq $target }).Count -eq 0) { throw "correction_of does not reference an earlier event in this attempt: $target" }
    }
}

function Open-AppendStream {
    param([Parameter(Mandatory)][string]$Path)
    $directory = Split-Path -Parent $Path
    New-Item -ItemType Directory -Path $directory -Force | Out-Null
    for ($try = 1; $try -le 50; $try++) {
        try { return [System.IO.File]::Open($Path, [IO.FileMode]::OpenOrCreate, [IO.FileAccess]::ReadWrite, [IO.FileShare]::Read) }
        catch [System.IO.IOException] { Start-Sleep -Milliseconds 20 }
    }
    throw "Could not acquire audit log lock: $Path"
}

if ([string]::IsNullOrWhiteSpace($ReportRoot)) { $ReportRoot = Join-Path (Join-Path (Join-Path $ProjectPath '.bsl-flow') 'reports') 'subagents' }
$root = [System.IO.Path]::GetFullPath($ReportRoot)
if ($RunId -notmatch '^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$') { throw "Invalid run id: $RunId" }
$path = Join-Path $root ($RunId + '.jsonl')
$raw = if ($PSCmdlet.ParameterSetName -eq 'Path') { Get-Content -Raw -LiteralPath $EventPath } else { $EventJson }
try { $event = $raw | ConvertFrom-Json -ErrorAction Stop } catch { throw "EventJson is not valid JSON: $($_.Exception.Message)" }
Assert-AuditEvent $event $RunId
$canonical = Get-CanonicalJson $event

$stream = Open-AppendStream $path
$outcome = 'appended'
try {
    $stream.Seek(0, [IO.SeekOrigin]::Begin) | Out-Null
    $reader = [IO.StreamReader]::new($stream, [Text.UTF8Encoding]::new($false), $true, 1024, $true)
    $existingText = $reader.ReadToEnd(); $reader.Dispose()
    $existing = @()
    foreach ($line in @($existingText -split "`r?`n")) {
        if ([string]::IsNullOrWhiteSpace($line)) { continue }
        try { $item = $line | ConvertFrom-Json -ErrorAction Stop } catch { throw "Audit log contains invalid JSONL; no event was appended: $path" }
        Assert-AuditEvent $item $RunId
        $existing += $item
        if ((Get-TextProperty $item 'event_id') -eq (Get-TextProperty $event 'event_id')) {
            if ((Get-CanonicalJson $item) -eq $canonical) { $outcome = 'deduplicated' }
            else { throw "Conflicting event_id already exists: $($event.event_id)" }
        }
    }
    if ($outcome -eq 'appended') { Assert-Lifecycle -Events ([object[]]$existing) -Candidate $event }
    if ($outcome -eq 'appended') {
        $stream.Seek(0, [IO.SeekOrigin]::End) | Out-Null
        $bytes = [Text.UTF8Encoding]::new($false).GetBytes($canonical + "`n")
        $stream.Write($bytes, 0, $bytes.Length); $stream.Flush($true)
    }
}
finally { $stream.Dispose() }

$summaryScript = Join-Path $PSScriptRoot 'Get-AgentAuditSummary.ps1'
if (Test-Path -LiteralPath $summaryScript -PathType Leaf) { $null = & $summaryScript -ReportRoot $root }
[pscustomobject]@{ outcome = $outcome; run_id = $RunId; event_id = $event.event_id; log_path = $path; summary_path = (Join-Path $root 'summary.md') }
