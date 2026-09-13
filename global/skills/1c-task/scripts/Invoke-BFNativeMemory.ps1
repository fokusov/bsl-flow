#Requires -Version 7.0
[CmdletBinding()]
param()

# The native controller invokes this entrypoint as a private JSON stdin/stdout
# helper.  It has no command-line operation mode: the operation and every other
# input are part of one closed request object.
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

$script:BFNativeMemorySchemaVersion = 1
$script:BFNativeMemoryInputLimit = 16MB
$script:BFNativeMemoryOutputLimit = 1MB
$script:BFNativeMemoryRootFields = @(
    'schema_version', 'operation', 'state', 'stage', 'result', 'result_hash',
    'receipt', 'receipt_hash', 'next', 'memory_root', 'pending_failure_result',
    'pending_failure_result_hash'
)
$script:BFNativeMemoryOperations = @('bind', 'extract-attempt', 'extract-acceptance', 'projection')
$script:BFNativeMemoryStages = @('inspect', 'spec', 'spec_review', 'implement', 'code_review', 'verify', 'diagnose', 'acceptance')
$script:BFNativeMemoryTerminalOutcomes = @('PASS', 'FAIL', 'BLOCKED', 'NEEDS_INPUT', 'REVISE', 'REPAIR')

function Get-BFNativeMemoryObjectKeys {
    param([AllowNull()][object]$Value)
    if ($null -eq $Value) { return @() }
    if ($Value -is [System.Collections.IDictionary]) {
        return @($Value.Keys | ForEach-Object { [string]$_ })
    }
    return @($Value.PSObject.Properties | ForEach-Object { [string]$_.Name })
}

function Test-BFNativeMemoryObjectProperty {
    param([AllowNull()][object]$Value, [Parameter(Mandatory = $true)][string]$Name)
    return ((Get-BFNativeMemoryObjectKeys $Value) -ccontains $Name)
}

function Get-BFNativeMemoryValue {
    param([AllowNull()][object]$Value, [Parameter(Mandatory = $true)][string]$Name)
    if ($null -eq $Value) { return $null }
    if ($Value -is [System.Collections.IDictionary]) {
        foreach ($key in $Value.Keys) {
            if ([string]$key -ceq $Name) { return $Value[$key] }
        }
        return $null
    }
    foreach ($property in @($Value.PSObject.Properties)) {
        if ([string]$property.Name -ceq $Name) { return $property.Value }
    }
    return $null
}

function Assert-BFNativeMemoryObject {
    param([AllowNull()][object]$Value, [Parameter(Mandatory = $true)][string]$Name)
    if ($null -eq $Value -or $Value -is [array] -or ($Value -isnot [System.Collections.IDictionary] -and $Value -isnot [pscustomobject])) {
        throw (New-BFError 'BF_INVALID' ("{0} must be an object." -f $Name))
    }
}

function Assert-BFNativeMemoryString {
    param(
        [AllowNull()][object]$Value,
        [Parameter(Mandatory = $true)][string]$Name,
        [int]$Maximum = 4096,
        [switch]$AllowEmpty
    )
    if ($Value -isnot [string] -or $Value.Length -gt $Maximum -or (-not $AllowEmpty -and [string]::IsNullOrWhiteSpace($Value))) {
        throw (New-BFError 'BF_INVALID' ("{0} must be a bounded string." -f $Name))
    }
    return [string]$Value
}

function Assert-BFNativeMemoryExactFields {
    param(
        [Parameter(Mandatory = $true)][object]$Value,
        [Parameter(Mandatory = $true)][string[]]$Fields,
        [Parameter(Mandatory = $true)][string]$Name
    )
    Assert-BFNativeMemoryObject $Value $Name
    $keys = @(Get-BFNativeMemoryObjectKeys $Value)
    foreach ($field in $Fields) {
        if ($keys -cnotcontains $field) { throw (New-BFError 'BF_INVALID' ("{0}.{1} is required." -f $Name, $field)) }
    }
    foreach ($key in $keys) {
        if ($Fields -cnotcontains $key) { throw (New-BFError 'BF_INVALID' ("Unknown field {0}.{1}." -f $Name, $key)) }
    }
}

function Assert-BFNativeMemoryMaybeObject {
    param([AllowNull()][object]$Value, [Parameter(Mandatory = $true)][string]$Name)
    if ($null -ne $Value) { Assert-BFNativeMemoryObject $Value $Name }
}

function Assert-BFNativeMemoryHash {
    param(
        [AllowNull()][object]$Value,
        [Parameter(Mandatory = $true)][string]$Name,
        [switch]$AllowEmpty
    )
    if ($null -eq $Value) { return }
    if ($Value -isnot [string]) { throw (New-BFError 'BF_INVALID' ("{0} must be a SHA-256 string or null." -f $Name)) }
    # The Go caller uses an empty string for the unused hash slots.  Treat it as
    # the wire-compatible null representation, while requiring a real hash for
    # an extraction operation.
    if ($Value.Length -eq 0 -and $AllowEmpty) { return }
    if ($Value -cnotmatch '^[0-9a-f]{64}$') { throw (New-BFError 'BF_INVALID' ("{0} must be a lower-case SHA-256 string." -f $Name)) }
}

function Read-BFNativeMemoryInputBytes {
    $stream = [Console]::OpenStandardInput()
    $buffer = [byte[]]::new(65536)
    $memory = [IO.MemoryStream]::new()
    try {
        while ($true) {
            $read = $stream.Read($buffer, 0, $buffer.Length)
            if ($read -le 0) { break }
            if (($memory.Length + $read) -gt $script:BFNativeMemoryInputLimit) {
                throw (New-BFError 'BF_INVALID' 'Native memory input exceeds the 16 MiB limit.')
            }
            $memory.Write($buffer, 0, $read)
        }
        return $memory.ToArray()
    }
    finally {
        $memory.Dispose()
        $stream.Dispose()
    }
}

function ConvertFrom-BFNativeMemoryInput {
    param([Parameter(Mandatory = $true)][byte[]]$Bytes)
    if ($Bytes.Length -eq 0) { throw (New-BFError 'BF_INVALID' 'Native memory input is empty.') }
    $encoding = [Text.UTF8Encoding]::new($false, $true)
    try { $text = $encoding.GetString($Bytes) }
    catch { throw (New-BFError 'BF_INVALID' 'Native memory input is not valid UTF-8.') }
    if ($text.Length -gt 0 -and $text[0] -eq [char]0xFEFF) { $text = $text.Substring(1) }
    if ([string]::IsNullOrWhiteSpace($text)) { throw (New-BFError 'BF_INVALID' 'Native memory input is empty.') }
    try {
        # The shared scanner rejects duplicate keys and trailing JSON.  Keep all
        # date-looking strings as strings: hashes are computed over the wire
        # representation and must not be changed into DateTime values.
        if ((Test-BFJsonSyntax $text) -cne 'object') { throw 'top-level JSON value must be an object' }
        $convertCommand = Get-Command ConvertFrom-Json -ErrorAction Stop
        if ($convertCommand.Parameters.ContainsKey('DateKind')) {
            $value = ConvertFrom-Json -InputObject $text -Depth 100 -DateKind String -ErrorAction Stop
        }
        else {
            $value = ConvertFrom-Json -InputObject $text -Depth 100 -ErrorAction Stop
        }
    }
    catch { throw (New-BFError 'BF_INVALID' 'Native memory input is not one valid JSON document.') }
    Assert-BFNativeMemoryObject $value 'native memory input'
    return $value
}

function Assert-BFNativeMemoryStateAndRoot {
    param([Parameter(Mandatory = $true)]$InputObject)
    $state = Get-BFNativeMemoryValue $InputObject 'state'
    Assert-BFNativeMemoryObject $state 'state'
    if (-not (Test-BFNativeMemoryObjectProperty $state 'schema_version') -or (Get-BFNativeMemoryValue $state 'schema_version') -cne 1) {
        throw (New-BFError 'BF_INVALID' 'state.schema_version must be 1.')
    }
    if (-not (Test-BFNativeMemoryObjectProperty $state 'task_id')) { throw (New-BFError 'BF_INVALID' 'state.task_id is required.') }
    $taskId = Assert-BFNativeMemoryString (Get-BFNativeMemoryValue $state 'task_id') 'state.task_id' 64
    Assert-BFUuid $taskId
    if (-not (Test-BFNativeMemoryObjectProperty $state 'project_path')) { throw (New-BFError 'BF_INVALID' 'state.project_path is required.') }
    $projectPath = Assert-BFNativeMemoryString (Get-BFNativeMemoryValue $state 'project_path') 'state.project_path' 4096
    $projectPath = Assert-BFSafePath $projectPath
    $declaredRoot = Assert-BFNativeMemoryString (Get-BFNativeMemoryValue $InputObject 'memory_root') 'memory_root' 4096
    $declaredRoot = Assert-BFSafePath $declaredRoot
    $expectedRoot = Assert-BFSafePath (Join-Path $projectPath '.bsl-flow/memory')
    $normalize = { param([string]$Path) $Path.TrimEnd([IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar) }
    if (-not [string]::Equals((& $normalize $declaredRoot), (& $normalize $expectedRoot), [StringComparison]::OrdinalIgnoreCase)) {
        throw (New-BFError 'BF_INVALID' 'memory_root must resolve to state.project_path/.bsl-flow/memory.')
    }
    if ([IO.File]::Exists($declaredRoot)) { throw (New-BFError 'BF_BLOCKED' 'memory_root is occupied by a file.') }
    return $state
}

function Assert-BFNativeMemoryResult {
    param(
        [Parameter(Mandatory = $true)]$State,
        [Parameter(Mandatory = $true)]$InputObject
    )
    $result = Get-BFNativeMemoryValue $InputObject 'result'
    Assert-BFNativeMemoryObject $result 'result'
    foreach ($field in @('schema_version', 'task_id', 'attempt_id', 'stage', 'outcome')) {
        if (-not (Test-BFNativeMemoryObjectProperty $result $field)) { throw (New-BFError 'BF_INVALID' ("result.{0} is required." -f $field)) }
    }
    if ((Get-BFNativeMemoryValue $result 'schema_version') -cne 1) { throw (New-BFError 'BF_INVALID' 'result.schema_version must be 1.') }
    $stateTaskId = [string](Get-BFNativeMemoryValue $State 'task_id')
    $resultTaskId = Assert-BFNativeMemoryString (Get-BFNativeMemoryValue $result 'task_id') 'result.task_id' 64
    Assert-BFUuid $resultTaskId
    if ($resultTaskId -cne $stateTaskId) { throw (New-BFError 'BF_CONFLICT' 'result.task_id does not match state.task_id.') }
    $attemptId = Assert-BFNativeMemoryString (Get-BFNativeMemoryValue $result 'attempt_id') 'result.attempt_id' 64
    Assert-BFUuid $attemptId
    $resultStage = Assert-BFNativeMemoryString (Get-BFNativeMemoryValue $result 'stage') 'result.stage' 64
    if ($resultStage -notin $script:BFNativeMemoryStages) { throw (New-BFError 'BF_INVALID' 'result.stage is not a controller stage.') }
    $outcome = Assert-BFNativeMemoryString (Get-BFNativeMemoryValue $result 'outcome') 'result.outcome' 32
    if ($outcome -notin $script:BFNativeMemoryTerminalOutcomes) { throw (New-BFError 'BF_INVALID' 'result.outcome is not terminal.') }
    return $result
}

function Assert-BFNativeMemoryReceipt {
    param(
        [Parameter(Mandatory = $true)]$State,
        [Parameter(Mandatory = $true)]$InputObject
    )
    $receipt = Get-BFNativeMemoryValue $InputObject 'receipt'
    Assert-BFNativeMemoryObject $receipt 'receipt'
    foreach ($field in @('schema_version', 'task_id', 'verdict')) {
        if (-not (Test-BFNativeMemoryObjectProperty $receipt $field)) { throw (New-BFError 'BF_INVALID' ("receipt.{0} is required." -f $field)) }
    }
    if ((Get-BFNativeMemoryValue $receipt 'schema_version') -cne 1) { throw (New-BFError 'BF_INVALID' 'receipt.schema_version must be 1.') }
    $stateTaskId = [string](Get-BFNativeMemoryValue $State 'task_id')
    $receiptTaskId = Assert-BFNativeMemoryString (Get-BFNativeMemoryValue $receipt 'task_id') 'receipt.task_id' 64
    Assert-BFUuid $receiptTaskId
    if ($receiptTaskId -cne $stateTaskId) { throw (New-BFError 'BF_CONFLICT' 'receipt.task_id does not match state.task_id.') }
    $verdict = Assert-BFNativeMemoryString (Get-BFNativeMemoryValue $receipt 'verdict') 'receipt.verdict' 32
    if ($verdict -cne 'PASS') { throw (New-BFError 'BF_BLOCKED' 'acceptance receipt verdict must be PASS.') }
    if ([string](Get-BFNativeMemoryValue $State 'status') -cne 'completed') { throw (New-BFError 'BF_BLOCKED' 'acceptance extraction requires completed controller state.') }
    return $receipt
}

function Assert-BFNativeMemoryPendingFailure {
    param(
        [Parameter(Mandatory = $true)]$State,
        [Parameter(Mandatory = $true)]$InputObject
    )
    $failure = Get-BFNativeMemoryValue $InputObject 'pending_failure_result'
    $hash = Get-BFNativeMemoryValue $InputObject 'pending_failure_result_hash'
    if ($null -eq $failure) {
        if ($null -ne $hash -and -not [string]::IsNullOrEmpty([string]$hash)) { throw (New-BFError 'BF_INVALID' 'pending_failure_result_hash requires pending_failure_result.') }
        return $null
    }
    Assert-BFNativeMemoryObject $failure 'pending_failure_result'
    Assert-BFNativeMemoryHash $hash 'pending_failure_result_hash'
    if ([string]::IsNullOrWhiteSpace([string]$hash)) { throw (New-BFError 'BF_INVALID' 'pending_failure_result requires pending_failure_result_hash.') }
    foreach ($field in @('schema_version','task_id','attempt_id','stage','outcome','side_effects')) {
        if (-not (Test-BFNativeMemoryObjectProperty $failure $field)) { throw (New-BFError 'BF_INVALID' ("pending_failure_result.{0} is required." -f $field)) }
    }
    if ((Get-BFNativeMemoryValue $failure 'schema_version') -cne 1) { throw (New-BFError 'BF_INVALID' 'pending_failure_result.schema_version must be 1.') }
    $taskId = Assert-BFNativeMemoryString (Get-BFNativeMemoryValue $failure 'task_id') 'pending_failure_result.task_id' 64
    Assert-BFUuid $taskId
    if ($taskId -cne [string](Get-BFNativeMemoryValue $State 'task_id')) { throw (New-BFError 'BF_CONFLICT' 'pending_failure_result.task_id does not match state.task_id.') }
    $attemptId = Assert-BFNativeMemoryString (Get-BFNativeMemoryValue $failure 'attempt_id') 'pending_failure_result.attempt_id' 64
    Assert-BFUuid $attemptId
    $pendingId = Get-BFNativeMemoryValue (Get-BFNativeMemoryValue $State 'repair') 'pending_failure'
    if ([string]::IsNullOrWhiteSpace([string]$pendingId) -or $attemptId -cne [string]$pendingId) { throw (New-BFError 'BF_CONFLICT' 'pending_failure_result.attempt_id does not match state.repair.pending_failure.') }
    if ([string](Get-BFNativeMemoryValue $failure 'stage') -cne 'verify' -or [string](Get-BFNativeMemoryValue $failure 'outcome') -cne 'FAIL' -or [string](Get-BFNativeMemoryValue $failure 'side_effects') -cne 'none') { throw (New-BFError 'BF_BLOCKED' 'pending_failure_result is not a clean failed verification.') }
    if ((Get-BFHash $failure) -cne [string]$hash) { throw (New-BFError 'BF_CONFLICT' 'pending_failure_result_hash does not match pending_failure_result.') }
    return $failure
}

function Assert-BFNativeMemoryInput {
    param([Parameter(Mandatory = $true)]$InputObject)
    Assert-BFNativeMemoryExactFields $InputObject $script:BFNativeMemoryRootFields 'native memory input'
    if ((Get-BFNativeMemoryValue $InputObject 'schema_version') -cne $script:BFNativeMemorySchemaVersion) { throw (New-BFError 'BF_INVALID' 'Unsupported native memory input schema_version.') }
    $operation = Assert-BFNativeMemoryString (Get-BFNativeMemoryValue $InputObject 'operation') 'operation' 32
    if ($operation -cnotin $script:BFNativeMemoryOperations) { throw (New-BFError 'BF_INVALID' 'Unsupported native memory operation.') }
    $state = Assert-BFNativeMemoryStateAndRoot $InputObject
    $stageValue = Get-BFNativeMemoryValue $InputObject 'stage'
    if ($stageValue -isnot [string] -or $stageValue.Length -gt 64) { throw (New-BFError 'BF_INVALID' 'stage must be a bounded string.') }
    if (-not [string]::IsNullOrWhiteSpace($stageValue) -and $stageValue -notin $script:BFNativeMemoryStages) { throw (New-BFError 'BF_INVALID' 'stage is not a controller stage.') }
    Assert-BFNativeMemoryMaybeObject (Get-BFNativeMemoryValue $InputObject 'result') 'result'
    Assert-BFNativeMemoryMaybeObject (Get-BFNativeMemoryValue $InputObject 'receipt') 'receipt'
    Assert-BFNativeMemoryMaybeObject (Get-BFNativeMemoryValue $InputObject 'next') 'next'
    Assert-BFNativeMemoryHash (Get-BFNativeMemoryValue $InputObject 'result_hash') 'result_hash' -AllowEmpty
    Assert-BFNativeMemoryHash (Get-BFNativeMemoryValue $InputObject 'receipt_hash') 'receipt_hash' -AllowEmpty
    $pendingFailure = Assert-BFNativeMemoryPendingFailure $state $InputObject

    $result = Get-BFNativeMemoryValue $InputObject 'result'
    $receipt = Get-BFNativeMemoryValue $InputObject 'receipt'
    $resultHash = Get-BFNativeMemoryValue $InputObject 'result_hash'
    $receiptHash = Get-BFNativeMemoryValue $InputObject 'receipt_hash'
    switch ($operation) {
        'bind' {
            if ([string]::IsNullOrWhiteSpace([string]$stageValue)) { throw (New-BFError 'BF_INVALID' 'bind requires a non-empty stage.') }
            if ($null -ne $result -or -not [string]::IsNullOrEmpty([string]$resultHash) -or $null -ne $receipt -or -not [string]::IsNullOrEmpty([string]$receiptHash)) { throw (New-BFError 'BF_INVALID' 'bind accepts no result or receipt fields.') }
        }
        'projection' {
            if ($null -ne $result -or -not [string]::IsNullOrEmpty([string]$resultHash) -or $null -ne $receipt -or -not [string]::IsNullOrEmpty([string]$receiptHash)) { throw (New-BFError 'BF_INVALID' 'projection accepts no result or receipt fields.') }
        }
        'extract-attempt' {
            if ($null -ne $pendingFailure) { throw (New-BFError 'BF_INVALID' 'extract-attempt accepts no pending failure result.') }
            if ($null -eq $result -or [string]::IsNullOrWhiteSpace([string]$resultHash)) { throw (New-BFError 'BF_INVALID' 'extract-attempt requires result and result_hash.') }
            if ($null -ne $receipt -or -not [string]::IsNullOrEmpty([string]$receiptHash)) { throw (New-BFError 'BF_INVALID' 'extract-attempt accepts no receipt fields.') }
            $result = Assert-BFNativeMemoryResult $state $InputObject
            if (-not [string]::IsNullOrWhiteSpace($stageValue) -and [string]$stageValue -cne [string](Get-BFNativeMemoryValue $result 'stage')) { throw (New-BFError 'BF_CONFLICT' 'result.stage does not match stage.') }
            if ((Get-BFHash $result) -cne [string]$resultHash) { throw (New-BFError 'BF_CONFLICT' 'result_hash does not match result.') }
        }
        'extract-acceptance' {
            if ($null -ne $pendingFailure) { throw (New-BFError 'BF_INVALID' 'extract-acceptance accepts no pending failure result.') }
            if ($null -eq $receipt -or [string]::IsNullOrWhiteSpace([string]$receiptHash)) { throw (New-BFError 'BF_INVALID' 'extract-acceptance requires receipt and receipt_hash.') }
            if ($null -ne $result -or -not [string]::IsNullOrEmpty([string]$resultHash)) { throw (New-BFError 'BF_INVALID' 'extract-acceptance accepts no result fields.') }
            $receipt = Assert-BFNativeMemoryReceipt $state $InputObject
            if ((Get-BFHash $receipt) -cne [string]$receiptHash) { throw (New-BFError 'BF_CONFLICT' 'receipt_hash does not match receipt.') }
        }
    }
    return [ordered]@{state=$state; pending_failure_result=$pendingFailure; pending_failure_result_hash=[string](Get-BFNativeMemoryValue $InputObject 'pending_failure_result_hash')}
}

function Invoke-BFNativeMemoryOperation {
    param([Parameter(Mandatory = $true)]$InputObject)
    $validated = Assert-BFNativeMemoryInput $InputObject
    $state = $validated.state
    $pendingFailure = $validated.pending_failure_result
    $pendingFailureHash = [string]$validated.pending_failure_result_hash
    $operation = [string](Get-BFNativeMemoryValue $InputObject 'operation')
    switch ($operation) {
        'bind' {
            return Add-BFMemoryAttemptBinding $state ([string](Get-BFNativeMemoryValue $InputObject 'stage')) $pendingFailure $pendingFailureHash
        }
        'extract-attempt' {
            return Add-BFMemoryFromAttempt $state (Get-BFNativeMemoryValue $InputObject 'result') ([string](Get-BFNativeMemoryValue $InputObject 'result_hash'))
        }
        'extract-acceptance' {
            return Add-BFMemoryFromAcceptance $state (Get-BFNativeMemoryValue $InputObject 'receipt') ([string](Get-BFNativeMemoryValue $InputObject 'receipt_hash'))
        }
        'projection' {
            return Get-BFMemoryProjection $state (Get-BFNativeMemoryValue $InputObject 'next') $pendingFailure $pendingFailureHash
        }
    }
    throw (New-BFError 'BF_INVALID' 'Unsupported native memory operation.')
}

function New-BFNativeMemoryEnvelope {
    param(
        [Parameter(Mandatory = $true)][bool]$Available,
        [AllowNull()][object]$Value,
        [AllowNull()][string]$DisabledReason
    )
    return [ordered]@{schema_version=1;available=$Available;value=$Value;disabled_reason=$DisabledReason}
}

function Get-BFNativeMemoryErrorText {
    param([AllowNull()][object]$ErrorRecord)
    $message = if ($null -ne $ErrorRecord) { [string]$ErrorRecord.Exception.Message } else { '' }
    if ([string]::IsNullOrWhiteSpace($message)) { $message = 'BF_BLOCKED: native memory helper failed.' }
    if ($message.Length -gt 256) { $message = $message.Substring(0, 256).TrimEnd() }
    return $message
}

function Write-BFNativeMemoryEnvelope {
    param([Parameter(Mandatory = $true)]$Envelope)
    try {
        $json = Get-BFCanonicalJson $Envelope
        $encoding = [Text.UTF8Encoding]::new($false, $true)
        $bytes = $encoding.GetBytes($json)
        if ($bytes.Length -gt $script:BFNativeMemoryOutputLimit) {
            $json = Get-BFCanonicalJson (New-BFNativeMemoryEnvelope $false $null 'BF_BLOCKED: native memory output exceeds the 1 MiB limit.')
            $bytes = $encoding.GetBytes($json)
        }
        $stdout = [Console]::OpenStandardOutput()
        try {
            $stdout.Write($bytes, 0, $bytes.Length)
            $stdout.Flush()
        }
        finally { $stdout.Dispose() }
    }
    catch {
        # The normal envelope is already bounded and serializable.  Keep a final
        # constant fallback so the caller never receives a process exception.
        try {
            $fallback = '{"available":false,"disabled_reason":"BF_BLOCKED: native memory output failed.","schema_version":1,"value":null}'
            $bytes = [Text.UTF8Encoding]::new($false, $true).GetBytes($fallback)
            $stdout = [Console]::OpenStandardOutput()
            try { $stdout.Write($bytes, 0, $bytes.Length); $stdout.Flush() } finally { $stdout.Dispose() }
        }
        catch { }
    }
}

$envelope = $null
try {
    # Import only the read-only storage, contract, architecture and memory
    # surfaces required by this bridge.  The task controller lifecycle is owned
    # by Go and is deliberately outside this process.
    foreach ($name in @('Task.Storage.ps1', 'Task.Contracts.ps1', 'Task.Architecture.ps1', 'Task.Memory.ps1')) {
        . (Join-Path $PSScriptRoot $name)
    }
    $inputObject = ConvertFrom-BFNativeMemoryInput (Read-BFNativeMemoryInputBytes)
    $value = Invoke-BFNativeMemoryOperation $inputObject
    $envelope = New-BFNativeMemoryEnvelope $true $value $null
}
catch {
    $envelope = New-BFNativeMemoryEnvelope $false $null (Get-BFNativeMemoryErrorText $_)
}

Write-BFNativeMemoryEnvelope $envelope
exit 0
