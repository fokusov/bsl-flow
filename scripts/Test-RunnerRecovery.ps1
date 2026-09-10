#Requires -Version 7.0
[CmdletBinding()]
param([string]$PackageRoot,[switch]$KeepFixture)
Set-StrictMode -Version Latest
$ErrorActionPreference='Stop'
if(-not $PackageRoot){$PackageRoot=Split-Path $PSScriptRoot -Parent}
$PackageRoot=[IO.Path]::GetFullPath($PackageRoot)
$core=Join-Path $PackageRoot 'global/skills/1c-task/scripts'
foreach($name in @('Task.Storage.ps1','Task.Contracts.ps1','Task.Gates.ps1','Task.Process.ps1','Task.Engine.ps1','Task.Stages.ps1','Task.Runner.ps1')){. (Join-Path $core $name)}
$script:checks=0
$script:calls=[Collections.Generic.List[string]]::new()
function Check([bool]$Value,[string]$Message){if(-not $Value){throw "ASSERTION FAILED: $Message"};$script:checks++}
function Reject([scriptblock]$Action,[string]$Pattern){$message='';try{&$Action|Out-Null}catch{$message=$_.Exception.Message};Check ($message -match $Pattern) "Expected $Pattern, got $message"}
function Write-Text([string]$Path,[string]$Text){[void][IO.Directory]::CreateDirectory((Split-Path $Path -Parent));[IO.File]::WriteAllText($Path,$Text,[Text.UTF8Encoding]::new($false))}
function New-Request([string]$Prompt){[pscustomobject]@{schema_version=1;request_id=[guid]::NewGuid().ToString();prompt=$Prompt;mode='analysis_only';analysis_goal='analysis';complexity='S';risk='low';impact_flags=@();criteria=@();provenance=[pscustomobject]@{source='user';reference='runner-recovery-fixture';text=$Prompt};models=[pscustomobject]@{worker='gpt-6-astra';worker_effort='medium';reviewer='gpt-6-astra';reviewer_effort='high'}}}
function New-Queue([string[]]$TaskIds){[pscustomobject]@{schema_version=1;queue_id=[guid]::NewGuid().ToString();task_ids=$TaskIds;poll_seconds=1;max_cycles=1}}
function New-Event([string]$TaskId,[int]$Revision,[string]$Action,[string]$Status){$key="$TaskId|$Revision|$Action";return [ordered]@{schema_version=1;event_key=$key;at=[DateTime]::UtcNow.ToString('o');task_id=$TaskId;revision=$Revision;action=$Action;status=$Status}}
function Add-Event([string]$Path,$Event){[IO.File]::AppendAllText($Path,(Get-BFCanonicalJson $Event)+[Environment]::NewLine,[Text.UTF8Encoding]::new($false))}
function Read-Events([string]$Path){if(-not(Test-Path -LiteralPath $Path)){return @()};return @(Get-Content -LiteralPath $Path|ForEach-Object{ConvertFrom-Json $_})}
function Save-State([string]$Project,$State){$lock=Enter-BFLock (Get-BFTaskDirectory $Project $State.task_id);try{return Save-BFTask $State $State.revision}finally{$lock.Dispose()}}
function Save-QueueSnapshot([string]$Project,$Queue,$Task){$runner=Get-BFRunnerDirectory $Project;[void][IO.Directory]::CreateDirectory($runner);$snapshot=[ordered]@{schema_version=1;queue_id=$Queue.queue_id;queue_sha256=Get-BFHash $Queue;cycle=0;cursor=0;tasks=[ordered]@{};event_keys=@();updated_at=[DateTime]::UtcNow.ToString('o')};$snapshot.tasks[$Task.task_id]=[ordered]@{last_key="$($Task.task_id)|$($Task.revision)|run";revision=$Task.revision;status='ready';action='run'};Write-BFJson (Join-Path $runner ('queue-'+$Queue.queue_id+'-snapshot.json')) $snapshot}
$executor={param($run);$script:calls.Add($run.state.task_id);[ordered]@{schema_version=1;status='completed';summary='Read-only fixture completed.';payload_json=Get-BFCanonicalJson ([ordered]@{complexity='S';risk='low';impact_flags=@();rationale='Fixture.'})}}
$fixture=Join-Path $PackageRoot ('work/runner-recovery-'+[guid]::NewGuid().ToString('N'))
$succeeded=$false
try{
    $project=Join-Path $fixture 'project';[void][IO.Directory]::CreateDirectory($project);[void](Invoke-BFGit $project @('init'));Write-Text (Join-Path $project 'file.txt') "fixture`n";Write-Text (Join-Path $project '.gitignore') ".bsl-flow/`n";[void](Invoke-BFGit $project @('add','.'));[void](Invoke-BFGit $project @('-c','user.name=BSL Flow Test','-c','user.email=test@example.invalid','commit','-m','Fixture'))
    $runner=Get-BFRunnerDirectory $project;[void][IO.Directory]::CreateDirectory($runner);$journal=Join-Path $runner 'events.jsonl'

    # Journal is ahead of an absent snapshot. Restart must dispatch the still-ready
    # core task, but must not append the already durable run event again.
    $ahead=Start-BFTask $project (New-Request 'Journal ahead of snapshot.');$queue=New-Queue @($ahead.task_id);Add-Event $journal (New-Event $ahead.task_id $ahead.revision 'run' 'ready')
    $beforeCalls=$script:calls.Count;[void](Invoke-BFTaskQueue $project $queue '' $executor);$events=Read-Events $journal
    Check ($script:calls.Count -eq $beforeCalls+1) 'Journal-ahead recovery stranded the still-ready task.'
    Check (@($events|Where-Object{$_.event_key -ceq "$($ahead.task_id)|$($ahead.revision)|run"}).Count -eq 1) 'Journal-ahead restart duplicated the run event.'

    # The durable journal, not the bounded snapshot cache, owns deduplication.
    $long=Start-BFTask $project (New-Request 'Event older than cache.');$oldEvent=New-Event $long.task_id $long.revision 'run' 'ready';Add-Event $journal $oldEvent
    $filler=[Text.StringBuilder]::new()
    for($i=0;$i -lt 257;$i++){[void]$filler.AppendLine((Get-BFCanonicalJson (New-Event ([guid]::NewGuid().ToString()) $i 'observed' 'completed')))}
    [IO.File]::AppendAllText($journal,$filler.ToString(),[Text.UTF8Encoding]::new($false))
    $otherQueue=New-Queue @($long.task_id);$beforeCalls=$script:calls.Count;[void](Invoke-BFTaskQueue $project $otherQueue '' $executor);$events=Read-Events $journal
    Check ($script:calls.Count -eq $beforeCalls+1) 'Old durable run event suppressed the required core dispatch.'
    Check (@($events|Where-Object{$_.event_key -ceq $oldEvent.event_key}).Count -eq 1) 'Event older than 256 keys was duplicated by another queue.'

    # A durable execution_error for the same task/revision suppresses replay even
    # when the persisted queue snapshot still says run.
    $failed=Start-BFTask $project (New-Request 'Failed dispatch before snapshot recovery.');$state=Read-BFTask $project $failed.task_id;$state.request|Add-Member -NotePropertyName max_attempts -NotePropertyValue 1;$state.attempts=@([guid]::NewGuid().ToString());$state=Save-State $project $state;$errorQueue=New-Queue @($failed.task_id);Save-QueueSnapshot $project $errorQueue $state;Add-Event $journal (New-Event $failed.task_id $state.revision 'execution_error' 'blocked')
    $script:originalInvokeBFRun=${function:Invoke-BFRun};$script:invokeBFRunCalls=0
    Set-Item -Path Function:Invoke-BFRun -Value {param([string]$ProjectPath,[string]$TaskId,[string]$CodexPath,[scriptblock]$StageExecutor);$script:invokeBFRunCalls++;& $script:originalInvokeBFRun @PSBoundParameters}
    try{[void](Invoke-BFTaskQueue $project $errorQueue '' $executor)}finally{Set-Item -Path Function:Invoke-BFRun -Value $script:originalInvokeBFRun}
    $errorSnapshot=Read-BFJson (Join-Path $runner ('queue-'+$errorQueue.queue_id+'-snapshot.json'))
    Check ($errorSnapshot.tasks.$($failed.task_id).action -eq 'error' -and $errorSnapshot.tasks.$($failed.task_id).status -eq 'blocked') 'Durable execution_error did not restore the snapshot error state.'
    Check ($script:invokeBFRunCalls -eq 0) 'Durable execution_error allowed Invoke-BFRun to retry.'

    # Every quiet terminal/operator state gets one observed event, including a
    # status change discovered only after restart.
    $quietTasks=[ordered]@{}
    foreach($status in @('needs_input','blocked','cancelled')){
        $task=Start-BFTask $project (New-Request ("Quiet "+$status));$state=Read-BFTask $project $task.task_id;$state.status=$status
        if($status -eq 'needs_input'){$state.question=[ordered]@{question_id=[guid]::NewGuid().ToString();text='Choose mapping.';intent_revision=$state.intent_revision};$state.blockers=@($state.question.text)}
        elseif($status -eq 'blocked'){$state.blockers=@('Blocked fixture.')}
        $state=Save-State $project $state;$quietTasks[$status]=$state
    }
    $completed=Start-BFTask $project (New-Request 'Quiet completed');[void](Invoke-BFRun $project $completed.task_id '' $executor);$quietTasks['completed']=Read-BFTask $project $completed.task_id
    foreach($status in @('needs_input','blocked','cancelled','completed')){
        $task=$quietTasks[$status];$quietQueue=New-Queue @($task.task_id)
        if($status -eq 'needs_input'){
            Save-QueueSnapshot $project $quietQueue $task
            $quietSnapshotPath=Join-Path $runner ('queue-'+$quietQueue.queue_id+'-snapshot.json')
            $quietSnapshot=Read-BFJson $quietSnapshotPath
            $quietSnapshot.tasks.$($task.task_id).last_key="$($task.task_id)|$($task.revision)|quiet"
            $quietSnapshot.tasks.$($task.task_id).revision=$task.revision
            $quietSnapshot.tasks.$($task.task_id).status='needs_input'
            $quietSnapshot.tasks.$($task.task_id).action='quiet'
            Write-BFJson $quietSnapshotPath $quietSnapshot -Replace
        }
        [void](Invoke-BFTaskQueue $project $quietQueue '' $executor);[void](Invoke-BFTaskQueue $project $quietQueue '' $executor)
        $observed=@(Read-Events $journal|Where-Object{$_.task_id -eq $task.task_id -and $_.action -eq 'observed' -and $_.status -eq $status})
        Check ($observed.Count -eq 1) "Quiet $status state did not produce exactly one durable observed event."
    }

    # Strict startup parsing rejects torn and structurally malformed journal rows.
    $goodJournal=if(Test-Path $journal){[IO.File]::ReadAllBytes($journal)}else{[byte[]]@()};$probe=Start-BFTask $project (New-Request 'Malformed journal probe.');$probeQueue=New-Queue @($probe.task_id);$beforeCalls=$script:calls.Count
    [IO.File]::AppendAllText($journal,'{"schema_version":1',[Text.UTF8Encoding]::new($false));Reject {Invoke-BFTaskQueue $project $probeQueue '' $executor} 'BF_BLOCKED';Check ($script:calls.Count -eq $beforeCalls) 'Torn journal dispatched a task before validation.'
    [IO.File]::WriteAllBytes($journal,$goodJournal);Add-Event $journal ([ordered]@{schema_version=1;event_key='incomplete'});Reject {Invoke-BFTaskQueue $project $probeQueue '' $executor} 'BF_BLOCKED';Check ($script:calls.Count -eq $beforeCalls) 'Malformed journal dispatched a task before validation.'
    $invalid=New-Event $probe.task_id $probe.revision 'observed' 'blocked';$invalid.status=$null;[IO.File]::WriteAllBytes($journal,$goodJournal);Add-Event $journal $invalid;Reject {Invoke-BFTaskQueue $project $probeQueue '' $executor} 'BF_BLOCKED'
    $invalid=New-Event $probe.task_id $probe.revision 'observed' 'blocked';$invalid.at=$null;[IO.File]::WriteAllBytes($journal,$goodJournal);Add-Event $journal $invalid;Reject {Invoke-BFTaskQueue $project $probeQueue '' $executor} 'BF_BLOCKED'
    [IO.File]::WriteAllBytes($journal,$goodJournal);[IO.File]::AppendAllText($journal,(Get-BFCanonicalJson @((New-Event $probe.task_id $probe.revision 'observed' 'blocked')))+[Environment]::NewLine,[Text.UTF8Encoding]::new($false));Reject {Invoke-BFTaskQueue $project $probeQueue '' $executor} 'BF_BLOCKED';[IO.File]::WriteAllBytes($journal,$goodJournal)

    # A second PowerShell process cannot own the runner lock; normal owner exit
    # releases it for the next process/controller.
    $ready=Join-Path $fixture 'lock-ready';$release=Join-Path $fixture 'lock-release';$storage=Join-Path $core 'Task.Storage.ps1';$child=@"
. '$($storage.Replace("'","''"))'
`$lock=Enter-BFLock '$($runner.Replace("'","''"))'
[IO.File]::WriteAllText('$($ready.Replace("'","''"))','ready')
try{while(-not(Test-Path -LiteralPath '$($release.Replace("'","''"))')){Start-Sleep -Milliseconds 50}}finally{`$lock.Dispose()}
"@
    $encoded=[Convert]::ToBase64String([Text.Encoding]::Unicode.GetBytes($child));$owner=Start-Process -FilePath (Join-Path $PSHOME 'pwsh.exe') -ArgumentList @('-NoProfile','-EncodedCommand',$encoded) -PassThru -WindowStyle Hidden
    try{$deadline=[DateTime]::UtcNow.AddSeconds(10);while(-not(Test-Path $ready) -and [DateTime]::UtcNow -lt $deadline){Start-Sleep -Milliseconds 50};Check (Test-Path $ready) 'External lock owner did not initialize.';Reject {Enter-BFLock $runner} 'Writer lock';Write-Text $release 'release';if(-not $owner.WaitForExit(10000)){throw 'External lock owner did not exit normally.'};$lock=Enter-BFLock $runner;try{Check $true 'Runner lock was not released after normal owner exit.'}finally{$lock.Dispose()}}finally{if(-not $owner.HasExited){$owner.Kill($true);$owner.WaitForExit()};$owner.Dispose()}

    Write-Output "Runner recovery checks passed: $script:checks"
    $succeeded=$true
}finally{
    if($succeeded -and -not $KeepFixture -and (Test-Path -LiteralPath $fixture)){
        $work=[IO.Path]::GetFullPath((Join-Path $PackageRoot 'work'));$resolved=[IO.Path]::GetFullPath($fixture)
        if(-not $resolved.StartsWith($work+[IO.Path]::DirectorySeparatorChar,[StringComparison]::OrdinalIgnoreCase)){throw 'Unsafe runner recovery fixture cleanup target.'}
        Remove-Item -LiteralPath $resolved -Recurse -Force
    }elseif(-not $succeeded){Write-Host "Fixture retained for inspection: $fixture"}
}
