#Requires -Version 7.0
[CmdletBinding()]
param([string]$PackageRoot)

# This is an offline contract test.  It starts the bridge as a separate pwsh
# process for every request, so parser/date/exit/stdio behavior is tested at the
# same boundary used by the native controller.  No model, runtime or controller
# lifecycle code is loaded here.
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

if ([string]::IsNullOrWhiteSpace($PackageRoot)) {
    $PackageRoot = Split-Path $PSScriptRoot -Parent
}
$PackageRoot = [IO.Path]::GetFullPath($PackageRoot).TrimEnd('\', '/')
$core = Join-Path $PackageRoot 'global/skills/1c-task/scripts'
$bridge = Join-Path $core 'Invoke-BFNativeMemory.ps1'
if (-not [IO.File]::Exists($bridge)) { throw "Native memory bridge is missing: $bridge" }

foreach ($module in @('Task.Storage.ps1', 'Task.Contracts.ps1', 'Task.Architecture.ps1', 'Task.Memory.ps1')) {
    . (Join-Path $core $module)
}

$script:Checks = 0
$script:Utf8 = [Text.UTF8Encoding]::new($false, $true)

function Assert-NativeMemory {
    param([bool]$Condition, [Parameter(Mandatory = $true)][string]$Message)
    if (-not $Condition) { throw ("ASSERTION FAILED: " + $Message) }
    $script:Checks++
}

function Get-NativeMemoryKeys {
    param([AllowNull()][object]$Value)
    if ($null -eq $Value) { return @() }
    if ($Value -is [System.Collections.IDictionary]) { return @($Value.Keys | ForEach-Object { [string]$_ }) }
    return @($Value.PSObject.Properties | ForEach-Object { [string]$_.Name })
}

function Get-NativeMemoryValue {
    param([AllowNull()][object]$Value, [Parameter(Mandatory = $true)][string]$Name)
    if ($null -eq $Value) { return $null }
    if ($Value -is [System.Collections.IDictionary]) {
        foreach ($key in $Value.Keys) { if ([string]$key -ceq $Name) { return $Value[$key] } }
        return $null
    }
    foreach ($property in @($Value.PSObject.Properties)) { if ([string]$property.Name -ceq $Name) { return $property.Value } }
    return $null
}

function New-NativeMemoryProject {
    $path = Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-native-memory-' + [guid]::NewGuid().ToString('N'))
    [void][IO.Directory]::CreateDirectory($path)
    return [IO.Path]::GetFullPath($path)
}

function New-NativeMemoryState {
    param(
        [Parameter(Mandatory = $true)][string]$Project,
        [Parameter(Mandatory = $true)][string]$TaskId,
        [string]$Status = 'ready',
        [string]$Stage = 'implement',
        [string]$PolicyHash = ('p' * 64)
    )
    return [ordered]@{
        schema_version = 1
        task_id = $TaskId
        revision = 1
        project_path = $Project
        worker_path = $Project
        baseline = ''
        request_hash = ('r' * 64)
        intent_hash = ('i' * 64)
        policy_hash = $PolicyHash
        created_at = '2026-09-12T20:00:00.0000000Z'
        updated_at = '2026-09-12T20:00:01.0000000Z'
        status = $Status
        stage = $Stage
        active_attempt = $null
        unresolved_effect = $null
        question = $null
        request = [ordered]@{
            mode = 'implement'
            prompt = 'Apply the bounded native memory fixture.'
            analysis_goal = 'analysis'
            complexity = 'S'
            risk = 'low'
            impact_flags = @()
            source_paths = @('src/hello.txt')
            criteria = @([ordered]@{
                id = 'fixture'
                observation = 'The fixture file contains the expected text.'
                kind = 'file_assertion'
                path = 'src/hello.txt'
                contains = 'fixture'
            })
        }
        classification = [ordered]@{complexity = 'S'; risk = 'low'; impact_flags = @(); rationale = 'Trusted native memory fixture.'}
        policy_files = @([ordered]@{path = 'Invoke-BSLFlowTask.ps1'; sha256 = ('h' * 64)})
        attempts = @()
        evidence = @()
        events = @()
        blockers = @()
        acceptances = @()
        repair = $null
    }
}

function New-NativeMemoryResult {
    param(
        [Parameter(Mandatory = $true)][string]$TaskId,
        [Parameter(Mandatory = $true)][string]$Stage,
        [Parameter(Mandatory = $true)][string]$Outcome,
        [AllowNull()][object]$Proposal = $null,
        [string]$SideEffects = 'none'
    )
    return [ordered]@{
        schema_version = 1
        task_id = $TaskId
        attempt_id = [guid]::NewGuid().ToString()
        stage = $Stage
        outcome = $Outcome
        finished_at = '2026-09-12T20:00:02.0000000Z'
        summary = 'Native memory fixture result.'
        dependencies = [ordered]@{}
        raw_hashes = @()
        proposal = $Proposal
        side_effects = $SideEffects
    }
}

function New-NativeMemoryReceipt {
    param([Parameter(Mandatory = $true)][string]$TaskId)
    return [ordered]@{
        schema_version = 1
        task_id = $TaskId
        intent_revision = 1
        authorization_revision = 1
        mode = 'implement'
        intent_hash = ('i' * 64)
        policy_hash = ('p' * 64)
        baseline = ''
        source_manifest = [ordered]@{sha256 = ('s' * 64); files = @()}
        gates = @(
            [ordered]@{stage = 'implement'; attempt_id = [guid]::NewGuid().ToString(); result_sha256 = ('a' * 64)},
            [ordered]@{stage = 'verify'; attempt_id = [guid]::NewGuid().ToString(); result_sha256 = ('b' * 64)}
        )
        verdict = 'PASS'
        scope = 'source-and-declared-checks'
    }
}

function New-NativeMemoryRequest {
    param(
        [Parameter(Mandatory = $true)]$State,
        [Parameter(Mandatory = $true)][string]$Operation,
        [string]$Stage = '',
        [AllowNull()][object]$Result = $null,
        [AllowNull()][string]$ResultHash = '',
        [AllowNull()][object]$Receipt = $null,
        [AllowNull()][string]$ReceiptHash = '',
        [AllowNull()][object]$Next = $null,
        [AllowNull()][object]$PendingFailureResult = $null,
        [AllowNull()][string]$PendingFailureResultHash = ''
    )
    return [ordered]@{
        schema_version = 1
        operation = $Operation
        state = $State
        stage = $Stage
        result = $Result
        result_hash = $ResultHash
        receipt = $Receipt
        receipt_hash = $ReceiptHash
        next = $Next
        memory_root = Join-Path ([string]$State.project_path) '.bsl-flow/memory'
        pending_failure_result = $PendingFailureResult
        pending_failure_result_hash = $PendingFailureResultHash
    }
}

function Assert-NativeMemoryEnvelope {
    param([Parameter(Mandatory = $true)]$Envelope, [string]$Name = 'memory envelope')
    $expected = @('schema_version', 'available', 'value', 'disabled_reason')
    $keys = @(Get-NativeMemoryKeys $Envelope)
    Assert-NativeMemory ($keys.Count -eq $expected.Count -and @($expected | Where-Object { $keys -cnotcontains $_ }).Count -eq 0 -and @($keys | Where-Object { $expected -cnotcontains $_ }).Count -eq 0) "$Name has the closed envelope shape"
    Assert-NativeMemory ((Get-NativeMemoryValue $Envelope 'schema_version') -eq 1) "$Name schema version"
    Assert-NativeMemory ((Get-NativeMemoryValue $Envelope 'available') -is [bool]) "$Name availability is boolean"
    Assert-NativeMemory (Test-NativeMemoryProperty $Envelope 'disabled_reason') "$Name has disabled_reason"
}

function Test-NativeMemoryProperty {
    param([AllowNull()][object]$Value, [Parameter(Mandatory = $true)][string]$Name)
    return ((Get-NativeMemoryKeys $Value) -ccontains $Name)
}

function Invoke-NativeMemoryBridge {
    param([Parameter(Mandatory = $true)]$InputObject)
    $json = Get-BFCanonicalJson $InputObject
    $pwsh = Join-Path ${env:ProgramFiles} 'PowerShell/7/pwsh.exe'
    if (-not [IO.File]::Exists($pwsh)) {
        $command = Get-Command pwsh -CommandType Application -ErrorAction Stop
        $pwsh = [string]$command.Source
    }
    $start = [Diagnostics.ProcessStartInfo]::new()
    $start.FileName = $pwsh
    foreach ($argument in @('-NoLogo', '-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass', '-File', $script:BridgePath)) {
        [void]$start.ArgumentList.Add($argument)
    }
    $start.UseShellExecute = $false
    $start.CreateNoWindow = $true
    $start.RedirectStandardInput = $true
    $start.RedirectStandardOutput = $true
    $start.RedirectStandardError = $true
    $start.StandardInputEncoding = $script:Utf8
    $start.StandardOutputEncoding = $script:Utf8
    $start.StandardErrorEncoding = $script:Utf8
    $process = [Diagnostics.Process]::new()
    $process.StartInfo = $start
    try {
        if (-not $process.Start()) { throw 'native bridge process did not start' }
        $stdoutTask = $process.StandardOutput.ReadToEndAsync()
        $stderrTask = $process.StandardError.ReadToEndAsync()
        $bytes = $script:Utf8.GetBytes($json)
        $process.StandardInput.BaseStream.Write($bytes, 0, $bytes.Length)
        $process.StandardInput.BaseStream.Flush()
        $process.StandardInput.Close()
        if (-not $process.WaitForExit(30000)) {
            try { $process.Kill($true) } catch { $process.Kill() }
            throw 'native bridge process timed out'
        }
        $stdout = $stdoutTask.GetAwaiter().GetResult()
        $stderr = $stderrTask.GetAwaiter().GetResult()
        if ($process.ExitCode -ne 0) { throw ("native bridge exited {0}: {1}" -f $process.ExitCode, $stderr) }
        if ([string]::IsNullOrWhiteSpace($stdout)) { throw ("native bridge returned empty stdout: " + $stderr) }
        $convert = Get-Command ConvertFrom-Json -ErrorAction Stop
        if ($convert.Parameters.ContainsKey('DateKind')) {
            return ConvertFrom-Json -InputObject $stdout -Depth 100 -DateKind String -ErrorAction Stop
        }
        return ConvertFrom-Json -InputObject $stdout -Depth 100 -ErrorAction Stop
    }
    finally { $process.Dispose() }
}

function Get-NativeMemoryTreeSnapshot {
    param([Parameter(Mandatory = $true)][string]$Project)
    $root = Join-Path $Project '.bsl-flow/memory'
    if (-not [IO.Directory]::Exists($root)) { return @() }
    return @(Get-ChildItem -LiteralPath $root -File -Recurse | Sort-Object FullName | ForEach-Object {
        [ordered]@{path = [IO.Path]::GetRelativePath($root, $_.FullName); length = $_.Length; sha256 = (Get-BFFileHash $_.FullName)}
    })
}

function Get-NativeMemoryRecordValues {
    param($Replay)
    if ($Replay.records -is [System.Collections.IDictionary]) { return @($Replay.records.Values) }
    return @($Replay.records.PSObject.Properties | ForEach-Object { $_.Value })
}

$script:BridgePath = $bridge
$projects = [System.Collections.Generic.List[string]]::new()
try {
    # --- 1. Start/bind and worker-result rejection -----------------------------
    $project = New-NativeMemoryProject; $projects.Add($project)
    $task = New-NativeMemoryState $project ([guid]::NewGuid().ToString())
    $bindInput = New-NativeMemoryRequest $task 'bind' 'implement'
    $bind = Invoke-NativeMemoryBridge $bindInput
    Assert-NativeMemoryEnvelope $bind 'bind'
    Assert-NativeMemory ((Get-NativeMemoryValue $bind 'available') -eq $true) 'bind process is available'
    Assert-NativeMemory ((Get-NativeMemoryValue (Get-NativeMemoryValue $bind 'value') 'available') -eq $true) 'empty bind memory is available'
    Assert-NativeMemory (0 -eq @(Get-NativeMemoryTreeSnapshot $project).Count) 'empty bind does not create an unnecessary ledger'

    $workerObservation = [ordered]@{
        scope = @('src/hello.txt')
        observation = 'The worker suggested a bounded implementation procedure.'
        action_type = 'recommended'
        action = 'Run the bounded procedure.'
        knowledge_class = 'procedural'
        risk_class = 'low'
    }
    $workerResult = New-NativeMemoryResult $task.task_id 'implement' 'PASS' ([ordered]@{changed_files = @('src/hello.txt'); observations = @($workerObservation)})
    $workerHash = Get-BFHash $workerResult
    $workerInput = New-NativeMemoryRequest $task 'extract-attempt' '' $workerResult $workerHash
    $workerExtract = Invoke-NativeMemoryBridge $workerInput
    Assert-NativeMemoryEnvelope $workerExtract 'worker extraction'
    Assert-NativeMemory ((Get-NativeMemoryValue $workerExtract 'available') -eq $true -and (Get-NativeMemoryValue $workerExtract 'value') -eq $true) 'worker extraction completes'
    $replay = Get-BFMemoryReplay $project
    Assert-NativeMemory (1 -eq @($replay.events).Count -and [string]$replay.events[0].event_type -ceq 'rejected' -and [string]$replay.events[0].reason -ceq 'worker-prose-untrusted') 'worker PASS prose is audited and rejected'
    Assert-NativeMemory (0 -eq @(Get-NativeMemoryRecordValues $replay).Count) 'worker PASS prose creates no record'

    $beforeBadResult = Get-NativeMemoryTreeSnapshot $project
    $badResultInput = New-NativeMemoryRequest $task 'extract-attempt' '' $workerResult ('0' * 64)
    $badResult = Invoke-NativeMemoryBridge $badResultInput
    Assert-NativeMemoryEnvelope $badResult 'bad result hash'
    Assert-NativeMemory ((Get-NativeMemoryValue $badResult 'available') -eq $false) 'bad result hash is disabled'
    Assert-NativeMemory ((Get-BFHash $workerResult) -cne ('0' * 64)) 'bad result fixture hash differs'
    Assert-NativeMemory ((Get-NativeMemoryTreeSnapshot $project | ConvertTo-Json -Compress) -ceq ($beforeBadResult | ConvertTo-Json -Compress)) 'bad result hash does not mutate memory'

    # --- 2. Three trusted receipts promote one record; bind retrieves it ------
    $acceptedTasks = @()
    foreach ($number in 1..3) {
        $acceptedTask = New-NativeMemoryState $project ([guid]::NewGuid().ToString()) 'completed' 'acceptance'
        $acceptedTasks += $acceptedTask
        $receipt = New-NativeMemoryReceipt $acceptedTask.task_id
        $receiptHash = Get-BFHash $receipt
        $acceptInput = New-NativeMemoryRequest $acceptedTask 'extract-acceptance' '' $null '' $receipt $receiptHash
        $accept = Invoke-NativeMemoryBridge $acceptInput
        Assert-NativeMemoryEnvelope $accept ("acceptance {0}" -f $number)
        Assert-NativeMemory ((Get-NativeMemoryValue $accept 'available') -eq $true -and (Get-NativeMemoryValue $accept 'value') -eq $true) ("trusted acceptance {0} completes" -f $number)
        $acceptReplay = Get-BFMemoryReplay $project
        $acceptRecords = @(Get-NativeMemoryRecordValues $acceptReplay)
        if ($number -eq 1) { Assert-NativeMemory (1 -eq $acceptRecords.Count -and [string]$acceptRecords[0].state -ceq 'candidate') 'first trusted receipt creates a candidate' }
        if ($number -eq 2) { Assert-NativeMemory (1 -eq $acceptRecords.Count -and [string]$acceptRecords[0].state -ceq 'shadow') 'second trusted receipt creates a cross-task shadow' }
        if ($number -eq 3) { Assert-NativeMemory (1 -eq $acceptRecords.Count -and [string]$acceptRecords[0].state -ceq 'accepted') 'third trusted receipt satisfies promotion threshold' }
    }
    $replay = Get-BFMemoryReplay $project
    $records = @(Get-NativeMemoryRecordValues $replay)
    Assert-NativeMemory (1 -eq $records.Count) 'trusted receipts deduplicate into one record'
    Assert-NativeMemory ([string]$records[0].state -ceq 'accepted') 'three trusted receipts promote the record'
    Assert-NativeMemory (1 -eq @($replay.events | Where-Object { [string]$_.event_type -ceq 'promoted' }).Count) 'promotion is recorded in the append-only history'

    $bindTask = New-NativeMemoryState $project ([guid]::NewGuid().ToString()) 'ready' 'implement'
    $selected = Invoke-NativeMemoryBridge (New-NativeMemoryRequest $bindTask 'bind' 'implement')
    Assert-NativeMemoryEnvelope $selected 'promoted bind'
    $selectedValue = Get-NativeMemoryValue $selected 'value'
    Assert-NativeMemory ((Get-NativeMemoryValue $selected 'available') -eq $true -and (Get-NativeMemoryValue $selectedValue 'available') -eq $true) 'promoted bind is available'
    Assert-NativeMemory (1 -eq @(Get-NativeMemoryValue $selectedValue 'records').Count -and [string](Get-NativeMemoryValue (Get-NativeMemoryValue $selectedValue 'records')[0] 'state') -ceq 'accepted') 'bind selects the promoted record'

    $beforeBadReceipt = Get-NativeMemoryTreeSnapshot $project
    $badReceipt = New-NativeMemoryReceipt $acceptedTasks[0].task_id
    $badReceiptInput = New-NativeMemoryRequest $acceptedTasks[0] 'extract-acceptance' '' $null '' $badReceipt ('f' * 64)
    $badReceiptResult = Invoke-NativeMemoryBridge $badReceiptInput
    Assert-NativeMemoryEnvelope $badReceiptResult 'bad receipt hash'
    Assert-NativeMemory ((Get-NativeMemoryValue $badReceiptResult 'available') -eq $false) 'bad receipt hash is disabled'
    Assert-NativeMemory ((Get-NativeMemoryTreeSnapshot $project | ConvertTo-Json -Compress) -ceq ($beforeBadReceipt | ConvertTo-Json -Compress)) 'bad receipt hash does not mutate memory'

    # --- 3. Controller diagnostic failure never becomes accepted knowledge -----
    $failureProject = New-NativeMemoryProject; $projects.Add($failureProject)
    $failureTask = New-NativeMemoryState $failureProject ([guid]::NewGuid().ToString()) 'failed' 'verify'
    $failureProposal = [ordered]@{repair_eligible = $true; criterion_id = 'fixture'; kind = 'file_assertion'; observation = 'The fixture criterion failed.'}
    $failureResult = New-NativeMemoryResult $failureTask.task_id 'verify' 'FAIL' $failureProposal
    $failureHash = Get-BFHash $failureResult
    $failureExtract = Invoke-NativeMemoryBridge (New-NativeMemoryRequest $failureTask 'extract-attempt' '' $failureResult $failureHash)
    Assert-NativeMemoryEnvelope $failureExtract 'verification failure extraction'
    Assert-NativeMemory ((Get-NativeMemoryValue $failureExtract 'available') -eq $true -and (Get-NativeMemoryValue $failureExtract 'value') -eq $true) 'verification failure extraction completes'
    $failureReplay = Get-BFMemoryReplay $failureProject
    $failureRecords = @(Get-NativeMemoryRecordValues $failureReplay)
    Assert-NativeMemory (1 -eq $failureRecords.Count -and [string]$failureRecords[0].knowledge_class -ceq 'diagnostic') 'verification failure creates only a diagnostic record'
    Assert-NativeMemory ([string]$failureRecords[0].state -ne 'accepted' -and 0 -eq @($failureReplay.events | Where-Object { [string]$_.event_type -ceq 'promoted' }).Count) 'verification failure never promotes memory'

    # Diagnose must consume the controller-owned canonical terminal result. A
    # hash-valid result for another attempt is rejected before the legacy
    # worktree fallback can expose an unrelated diagnostic.
    $failureTask.repair = [ordered]@{pending_failure = $failureResult.attempt_id}
    $diagnoseNext = [ordered]@{action = 'dispatch'; stage = 'diagnose'; blockers = @()}
    $diagnose = Invoke-NativeMemoryBridge (New-NativeMemoryRequest $failureTask 'projection' '' $null '' $null '' $diagnoseNext $failureResult $failureHash)
    Assert-NativeMemoryEnvelope $diagnose 'trusted diagnosis projection'
    $diagnoseValue = Get-NativeMemoryValue $diagnose 'value'
    Assert-NativeMemory ((Get-NativeMemoryValue $diagnose 'available') -eq $true -and (Get-NativeMemoryValue $diagnoseValue 'available') -eq $true -and 1 -eq @((Get-NativeMemoryValue $diagnoseValue 'records') | Where-Object { [string]$_.knowledge_class -ceq 'diagnostic' }).Count) 'trusted pending failure selects its diagnostic'
    $otherFailure = New-NativeMemoryResult $failureTask.task_id 'verify' 'FAIL' ([ordered]@{repair_eligible = $true; criterion_id = 'other'; kind = 'file_assertion'; observation = 'Another criterion failed.'})
    $mismatch = Invoke-NativeMemoryBridge (New-NativeMemoryRequest $failureTask 'projection' '' $null '' $null '' $diagnoseNext $otherFailure (Get-BFHash $otherFailure))
    Assert-NativeMemoryEnvelope $mismatch 'mismatched diagnosis projection'
    Assert-NativeMemory ((Get-NativeMemoryValue $mismatch 'available') -eq $false) 'mismatched pending failure is disabled'

    # --- 4. Damaged memory degrades the inner value without throwing -----------
    $damagedProject = New-NativeMemoryProject; $projects.Add($damagedProject)
    $damagedEvents = Join-Path $damagedProject '.bsl-flow/memory/events'
    [void][IO.Directory]::CreateDirectory($damagedEvents)
    [IO.File]::WriteAllText((Join-Path $damagedEvents '000001.json'), '{"schema_version":1}', $script:Utf8)
    $damagedTask = New-NativeMemoryState $damagedProject ([guid]::NewGuid().ToString()) 'ready' 'implement'
    $damaged = Invoke-NativeMemoryBridge (New-NativeMemoryRequest $damagedTask 'bind' 'implement')
    Assert-NativeMemoryEnvelope $damaged 'damaged bind'
    $damagedValue = Get-NativeMemoryValue $damaged 'value'
    Assert-NativeMemory ((Get-NativeMemoryValue $damaged 'available') -eq $true -and (Get-NativeMemoryValue $damagedValue 'available') -eq $false) 'damaged memory degrades through the advisory value'
    Assert-NativeMemory (-not [string]::IsNullOrWhiteSpace([string](Get-NativeMemoryValue $damagedValue 'disabled_reason'))) 'damaged memory exposes a bounded reason'

    # --- 5. Projection is read-only --------------------------------------------
    $beforeProjection = Get-NativeMemoryTreeSnapshot $project
    $projectionTask = New-NativeMemoryState $project ([guid]::NewGuid().ToString()) 'ready' 'implement'
    $projectionNext = [ordered]@{action = 'dispatch'; stage = 'implement'; blockers = @()}
    $projection = Invoke-NativeMemoryBridge (New-NativeMemoryRequest $projectionTask 'projection' '' $null '' $null '' $projectionNext)
    Assert-NativeMemoryEnvelope $projection 'projection'
    Assert-NativeMemory ((Get-NativeMemoryValue $projection 'available') -eq $true -and (Get-NativeMemoryValue (Get-NativeMemoryValue $projection 'value') 'available') -eq $true) 'projection is available'
    Assert-NativeMemory ((Get-NativeMemoryTreeSnapshot $project | ConvertTo-Json -Compress) -ceq ($beforeProjection | ConvertTo-Json -Compress)) 'projection writes nothing'

    # --- 6. Contract rejects a root outside project/.bsl-flow/memory -----------
    $badRootInput = New-NativeMemoryRequest $projectionTask 'projection' '' $null '' $null '' $projectionNext
    $badRootInput.memory_root = Join-Path $project '.bsl-flow/other'
    $badRoot = Invoke-NativeMemoryBridge $badRootInput
    Assert-NativeMemoryEnvelope $badRoot 'bad memory root'
    Assert-NativeMemory ((Get-NativeMemoryValue $badRoot 'available') -eq $false) 'wrong memory root is rejected'

    Write-Output ("PASS: {0} native memory bridge checks" -f $script:Checks)
}
finally {
    foreach ($path in @($projects)) {
        if ([IO.Directory]::Exists($path)) { Remove-Item -LiteralPath $path -Recurse -Force }
    }
}

