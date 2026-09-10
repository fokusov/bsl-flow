#Requires -Version 7.0
Set-StrictMode -Version Latest

# The caller dot-sources the authoritative controller modules first.  This
# runner deliberately delegates every task transition to that controller.

function Assert-BFRunnerQueue {
    param([Parameter(Mandatory)][object]$QueueInput)

    Assert-BFFields $QueueInput @('schema_version','queue_id','task_ids','poll_seconds','max_cycles') @() 'task_queue'
    if ($QueueInput.schema_version -ne 1) { throw 'BF_INVALID: unsupported task_queue schema_version.' }
    Assert-BFUuid $QueueInput.queue_id
    if ($QueueInput.task_ids -isnot [array] -or @($QueueInput.task_ids).Count -eq 0) { throw 'BF_INVALID: task_queue.task_ids must be a non-empty array.' }
    $seen=@{}
    foreach ($taskId in @($QueueInput.task_ids)) {
        if ($taskId -isnot [string]) { throw 'BF_INVALID: task_queue.task_ids must contain strings.' }
        Assert-BFUuid $taskId
        if ($seen.ContainsKey($taskId)) { throw 'BF_INVALID: task_queue.task_ids must be unique.' }
        $seen[$taskId]=$true
    }
    foreach ($name in @('poll_seconds','max_cycles')) {
        $value=Get-BFValue $QueueInput $name
        if ($value -isnot [int] -and $value -isnot [long]) { throw "BF_INVALID: task_queue.$name must be an integer." }
    }
    if ($QueueInput.poll_seconds -lt 1 -or $QueueInput.poll_seconds -gt 60) { throw 'BF_INVALID: task_queue.poll_seconds must be from 1 to 60.' }
    if ($QueueInput.max_cycles -lt 1 -or $QueueInput.max_cycles -gt 10000) { throw 'BF_INVALID: task_queue.max_cycles must be from 1 to 10000.' }
}

function Get-BFRunnerDirectory {
    param([Parameter(Mandatory)][string]$ProjectPath)
    return Assert-BFSafePath (Join-Path $ProjectPath '.bsl-flow/runner')
}

function Save-BFRunnerJson {
    param([string]$Path, $Value)
    Write-BFJson -Path $Path -Value $Value -Replace
}

function Save-BFRunnerEvent {
    param([string]$RunnerDirectory, $Snapshot, [string]$TaskId, [int]$Revision, [string]$Action, [string]$Status, [System.Collections.IDictionary]$JournalIndex)
    $key="$TaskId|$Revision|$Action"
    if ($JournalIndex.Contains($key)) { return }
    $event=[ordered]@{schema_version=1;event_key=$key;at=[DateTime]::UtcNow.ToString('o');task_id=$TaskId;revision=$Revision;action=$Action;status=$Status}
    $line=Get-BFCanonicalJson $event
    $bytes=[Text.UTF8Encoding]::new($false).GetBytes($line+[Environment]::NewLine)
    $stream=[IO.File]::Open((Join-Path $RunnerDirectory 'events.jsonl'),[IO.FileMode]::Append,[IO.FileAccess]::Write,[IO.FileShare]::Read)
    try {$stream.Write($bytes,0,$bytes.Length);$stream.Flush($true)} finally {$stream.Dispose()}
    $JournalIndex[$key]=$event
    $Snapshot.event_keys=@($Snapshot.event_keys+$key | Select-Object -Last 256)
}

function Read-BFRunnerJournal {
    param([string]$RunnerDirectory)
    $index=[ordered]@{}
    $path=Join-Path $RunnerDirectory 'events.jsonl'
    if(-not(Test-Path -LiteralPath $path -PathType Leaf)){return ,$index}
    [void](Assert-BFSafePath $path)
    try{$text=[Text.UTF8Encoding]::new($false,$true).GetString([IO.File]::ReadAllBytes($path))}
    catch{throw 'BF_BLOCKED: runner journal cannot be read as strict UTF-8; inspect before resuming.'}
    if($text.Length -gt 0 -and -not $text.EndsWith("`n",[StringComparison]::Ordinal)){throw 'BF_BLOCKED: runner journal has an incomplete final record; inspect before resuming.'}
    foreach($line in ($text -split '\r?\n')){
        if($line.Length -eq 0){continue}
        try {
            if((Test-BFJsonSyntax $line) -ne 'object'){throw 'Journal record must be an object.'}
            $event=ConvertFrom-Json -InputObject $line -ErrorAction Stop
            # Keep the wire type and value regardless of PowerShell's automatic
            # ISO date conversion (DateKind is not available in every PS7).
            $document=[System.Text.Json.JsonDocument]::Parse($line)
            try{
                $at=$document.RootElement.GetProperty('at')
                if($at.ValueKind -ne [System.Text.Json.JsonValueKind]::String){throw 'Invalid timestamp JSON type.'}
                $event.at=$at.GetString()
            }finally{$document.Dispose()}
            Assert-BFFields $event @('schema_version','event_key','at','task_id','revision','action','status') @() 'runner_event'
            Assert-BFUuid $event.task_id
            if($event.schema_version -ne 1 -or ($event.revision -isnot [int] -and $event.revision -isnot [long]) -or $event.revision -lt -1 -or $event.event_key -cne "$($event.task_id)|$($event.revision)|$($event.action)"){throw 'Invalid event identity.'}
            if($event.action -notin @('run','resume_readonly','observed','completed_stale','recovery_required','execution_error','error')){throw 'Invalid event action.'}
            if($event.status -isnot [string] -or $event.status -cnotin @('ready','running','needs_input','blocked','failed','completed','cancelled')){throw 'Invalid event status.'}
            $timestamp=[datetime]::MinValue
            if($event.at -isnot [string] -or -not [datetime]::TryParseExact($event.at,'o',[Globalization.CultureInfo]::InvariantCulture,[Globalization.DateTimeStyles]::RoundtripKind,[ref]$timestamp) -or $timestamp.Kind -ne [DateTimeKind]::Utc){throw 'Invalid event timestamp.'}
        } catch {throw 'BF_BLOCKED: runner journal contains an invalid record; inspect before resuming.'}
        # Older versions could append the same event before saving their cursor.
        # Existing duplicates remain historical; the journal is never rewritten.
        $index[$event.event_key]=$event
    }
    return ,$index
}

function Get-BFRunnerErrorSummary {
    param([string]$Message)
    $text=($Message -replace '[\r\n]+',' ').Trim()
    if($text.Length -gt 1024){$text=$text.Substring(0,1024)}
    return $text
}

function Get-BFRunnerSnapshot {
    param([string]$Path, $Queue, [string]$QueueHash)
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        return [ordered]@{schema_version=1;queue_id=$Queue.queue_id;queue_sha256=$QueueHash;cycle=0;cursor=0;tasks=[ordered]@{};event_keys=@();updated_at=$null}
    }
    $snapshot=Read-BFJson $Path
    Assert-BFFields $snapshot @('schema_version','queue_id','queue_sha256','cycle','cursor','tasks','event_keys','updated_at') @() 'runner_snapshot'
    if ($snapshot.schema_version -ne 1 -or $snapshot.queue_id -ne $Queue.queue_id -or $snapshot.queue_sha256 -ne $QueueHash) { throw 'BF_CONFLICT: queue_id belongs to a different immutable queue input.' }
    # JSON is read as PSCustomObject; make this dynamically keyed map writable
    # without enumerating singleton arrays.
    if ($snapshot.tasks -isnot [System.Collections.IDictionary]) {
        $tasks=[ordered]@{}
        foreach($property in @($snapshot.tasks.PSObject.Properties)){$tasks[$property.Name]=$property.Value}
        $snapshot.tasks=$tasks
    }
    return $snapshot
}

function Test-BFRunnerAttemptIsDead {
    param([string]$ProjectPath,[string]$TaskId,$State)
    if (-not $State.active_attempt) { return $false }
    $attemptDir=Join-Path (Get-BFTaskDirectory $ProjectPath $TaskId) ('attempts/'+$State.active_attempt)
    $start=Read-BFJson (Join-Path $attemptDir 'start.json')
    $owner=Get-BFValue $start 'controller_process'
    if ($null -ne $owner -and $null -ne (Get-BFOwnedProcess $owner)) { return $false }
    foreach($file in @(Get-ChildItem -LiteralPath $attemptDir -Filter process.json -File -Recurse)) {
        $identity=Read-BFJson $file.FullName
        $process=Get-Process -Id $identity.pid -ErrorAction SilentlyContinue
        if($null -ne $process -and $process.StartTime.ToUniversalTime().ToString('o') -eq $identity.start_time_utc){return $false}
    }
    return $true
}

function Get-BFRunnerDecision {
    param([string]$ProjectPath,[string]$TaskId)
    $state=Read-BFTask $ProjectPath $TaskId
    $next=Get-BFNext $state
    if ($state.status -eq 'cancelled' -or $null -ne $state.unresolved_effect) { return [ordered]@{state=$state;action='quiet'} }
    if ($state.active_attempt) {
        $start=Read-BFJson (Join-Path (Get-BFTaskDirectory $ProjectPath $TaskId) ('attempts/'+$state.active_attempt+'/start.json'))
        if (Test-BFRunnerAttemptIsDead $ProjectPath $TaskId $state) {
            if($start.stage -in @('inspect','spec','code_review','diagnose')){return [ordered]@{state=$state;action='resume_readonly'}}
            return [ordered]@{state=$state;action='recovery_required'}
        }
        return [ordered]@{state=$state;action='quiet'}
    }
    if($state.status -eq 'completed'){
        $effective=New-BFEnvelope $state $next.action @($next.blockers) $next.stage
        if($effective.status -ne 'completed'){return [ordered]@{state=$state;action='completed_stale'}}
    }
    if ($next.action -in @('dispatch','accept') -and $state.status -eq 'ready') { return [ordered]@{state=$state;action='run'} }
    return [ordered]@{state=$state;action='quiet'}
}

function Invoke-BFTaskQueue {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][string]$ProjectPath,
        [Parameter(Mandatory)][Alias('Input')][object]$QueueInput,
        [string]$CodexPath,
        [scriptblock]$StageExecutor
    )
    Assert-BFRunnerQueue $QueueInput
    $project=Assert-BFSafePath $ProjectPath
    if (-not (Test-Path -LiteralPath $project -PathType Container)) { throw 'BF_INVALID: project directory missing.' }
    if ((Assert-BFSafePath (Invoke-BFGit $project @('rev-parse','--show-toplevel'))) -ne $project) { throw 'BF_INVALID: supervisor requires the exact Git project root.' }
    foreach($taskId in @($QueueInput.task_ids)) { [void](Read-BFTask $project $taskId) }

    $runner=Get-BFRunnerDirectory $project
    $lock=Enter-BFLock $runner
    try {
        $queueHash=Get-BFHash $QueueInput
        $queueDirectory=Join-Path $runner 'queues'
        [void][IO.Directory]::CreateDirectory($queueDirectory)
        $queuePath=Join-Path $queueDirectory ($QueueInput.queue_id+'.json')
        if(Test-Path -LiteralPath $queuePath -PathType Leaf){
            if((Get-BFHash (Read-BFJson $queuePath)) -ne $queueHash){throw 'BF_CONFLICT: queue_id belongs to a different immutable queue input.'}
        } else { Write-BFJson -Path $queuePath -Value $QueueInput }
        $snapshotPath=Join-Path $runner ('queue-'+$QueueInput.queue_id+'-snapshot.json')
        $snapshot=Get-BFRunnerSnapshot $snapshotPath $QueueInput $queueHash
        $journal=Read-BFRunnerJournal $runner
        for($cycle=0;$cycle -lt $QueueInput.max_cycles;$cycle++) {
            $snapshot.cycle=[int]$snapshot.cycle+1
            $count=@($QueueInput.task_ids).Count
            $cycleStartCursor=[int]$snapshot.cursor
            for($offset=0;$offset -lt $count;$offset++) {
                $index=($cycleStartCursor+$offset)%$count
                $taskId=$QueueInput.task_ids[$index]
                $attemptKey=$null;$state=$null
                try {
                    $decision=Get-BFRunnerDecision $project $taskId
                    $state=$decision.state;$action=$decision.action
                    $key="$taskId|$($state.revision)|$action"
                    $previous=Get-BFValue $snapshot.tasks $taskId
                    # An older runner may have saved a quiet snapshot without its
                    # notification. Reconcile against the journal on every poll.
                    if($action -eq 'quiet' -and $state.status -in @('needs_input','blocked','cancelled','completed')){
                        Save-BFRunnerEvent $runner $snapshot $taskId $state.revision 'observed' $state.status $journal
                    }
                    # The durable error event may be newer than the last snapshot.
                    # Do not repeat that same failed dispatch after a crash.
                    if($action -in @('run','resume_readonly') -and $journal.Contains("$taskId|$($state.revision)|execution_error")){
                        if($null -eq $previous -or $previous.last_key -ne $key -or $previous.action -ne 'error'){
                            $snapshot.tasks[$taskId]=[ordered]@{last_key=$key;revision=$state.revision;status='blocked';action='error';error='Recorded controller execution error requires an explicit task update.'}
                        }
                        continue
                    }
                    # An unfinished dispatch marker can precede attempt creation.
                    # Reconcile it with the fresh core decision; an actual error
                    # has action=error and must remain suppressed at this key.
                    if($action -in @('run','resume_readonly') -and ($null -eq $previous -or $previous.last_key -ne $key -or $previous.action -in @('run','resume_readonly'))) {
                        $attemptKey=$key
                        $snapshot.tasks[$taskId]=[ordered]@{last_key=$key;revision=$state.revision;status=$state.status;action=$action}
                        Save-BFRunnerEvent $runner $snapshot $taskId $state.revision $action $state.status $journal
                        $snapshot.cursor=($index+1)%$count;$snapshot.updated_at=[DateTime]::UtcNow.ToString('o');Save-BFRunnerJson $snapshotPath $snapshot
                        if($action -eq 'resume_readonly'){[void](Resume-BFAttempt $project $taskId)}else{[void](Invoke-BFRun $project $taskId $CodexPath $StageExecutor)}
                        # The action itself may have advanced several core stages.
                        # Save the observed terminal/current revision before sleeping;
                        # a new eligible action still has a different revision/action key.
                        $after=Read-BFTask $project $taskId
                        $snapshot.tasks[$taskId]=[ordered]@{last_key="$taskId|$($after.revision)|quiet";revision=$after.revision;status=$after.status;action='quiet'}
                        Save-BFRunnerEvent $runner $snapshot $taskId $after.revision 'observed' $after.status $journal
                    } elseif($null -eq $previous) {
                        $snapshot.tasks[$taskId]=[ordered]@{last_key=$key;revision=$state.revision;status=$state.status;action=$action}
                        if($action -in @('completed_stale','recovery_required')){
                            $snapshot.tasks[$taskId].status='blocked';$snapshot.tasks[$taskId].error=if($action -eq 'completed_stale'){'Completed task has no fresh acceptance; operator review is required.'}else{'Interrupted modifying attempt requires recovery; automatic replay is forbidden.'}
                            Save-BFRunnerEvent $runner $snapshot $taskId $state.revision $action 'blocked' $journal
                        }
                    } elseif($previous.last_key -ne $key -and ($previous.revision -ne $state.revision -or $previous.status -ne $state.status -or $previous.action -ne $action)) {
                        $snapshot.tasks[$taskId]=[ordered]@{last_key=$key;revision=$state.revision;status=$state.status;action=$action}
                        if($action -in @('completed_stale','recovery_required')){
                            $snapshot.tasks[$taskId].status='blocked';$snapshot.tasks[$taskId].error=if($action -eq 'completed_stale'){'Completed task has no fresh acceptance; operator review is required.'}else{'Interrupted modifying attempt requires recovery; automatic replay is forbidden.'}
                            Save-BFRunnerEvent $runner $snapshot $taskId $state.revision $action 'blocked' $journal
                        }
                    }
                } catch {
                    # A failed dispatch was already persisted before calling the
                    # controller. Keep that revision/action cursor so polling or
                    # restart cannot retry the identical blocker.
                    if($null -ne $attemptKey){
                        $revision=[int]$state.revision
                        $snapshot.tasks[$taskId]=[ordered]@{last_key=$attemptKey;revision=$revision;status='blocked';action='error';error=(Get-BFRunnerErrorSummary $_.Exception.Message)}
                        Save-BFRunnerEvent $runner $snapshot $taskId $revision 'execution_error' 'blocked' $journal
                    } else {
                        $revision=if($null -ne $state){[int]$state.revision}else{-1}
                        $snapshot.tasks[$taskId]=[ordered]@{last_key="$taskId|$revision|error";revision=$revision;status='blocked';action='error';error=(Get-BFRunnerErrorSummary $_.Exception.Message)}
                        Save-BFRunnerEvent $runner $snapshot $taskId $revision 'error' 'blocked' $journal
                    }
                }
            }
            $snapshot.updated_at=[DateTime]::UtcNow.ToString('o');Save-BFRunnerJson $snapshotPath $snapshot
            if($cycle -lt ($QueueInput.max_cycles-1)){Start-Sleep -Seconds $QueueInput.poll_seconds}
        }
        return $snapshot
    } finally { $lock.Dispose() }
}
