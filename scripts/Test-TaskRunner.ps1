[CmdletBinding()]
param([string]$PackageRoot,[string]$RunnerPath,[switch]$KeepFixture)
Set-StrictMode -Version Latest
$ErrorActionPreference='Stop'
if(-not $PackageRoot){$PackageRoot=Split-Path $PSScriptRoot -Parent}
if(-not $RunnerPath){$RunnerPath=Join-Path $PackageRoot 'global/skills/1c-task/scripts/Task.Runner.ps1'}
$core=Join-Path $PackageRoot 'global/skills/1c-task/scripts'
foreach($name in @('Task.Storage.ps1','Task.Contracts.ps1','Task.Gates.ps1','Task.Process.ps1','Task.Engine.ps1','Task.Stages.ps1')){. (Join-Path $core $name)}
. $RunnerPath
$script:checks=0;$script:calls=New-Object 'System.Collections.Generic.List[string]'
function Assert-T([bool]$Value,[string]$Message){if(-not $Value){throw "ASSERTION FAILED: $Message"};$script:checks++}
function Throws-T([scriptblock]$Body,[string]$Pattern){$message='';try{& $Body|Out-Null}catch{$message=$_.Exception.Message};Assert-T ($message -match $Pattern) "Expected $Pattern, got $message"}
function Write-T([string]$Path,[string]$Text){[void][IO.Directory]::CreateDirectory((Split-Path $Path -Parent));[IO.File]::WriteAllText($Path,$Text,(New-Object Text.UTF8Encoding($false)))}
function New-Request-T([string]$Prompt){[pscustomobject]@{schema_version=1;request_id=[guid]::NewGuid().ToString();prompt=$Prompt;mode='analysis_only';analysis_goal='analysis';complexity='S';risk='low';impact_flags=@();criteria=@();provenance=[pscustomobject]@{source='user';reference='runner-fixture';text=$Prompt};models=[pscustomobject]@{worker='gpt-6-astra';worker_effort='medium';reviewer='gpt-6-astra';reviewer_effort='high'}}}
function New-Queue-T([string[]]$TaskIds,[int]$Cycles=1,[int]$Poll=1){[pscustomobject]@{schema_version=1;queue_id=[guid]::NewGuid().ToString();task_ids=$TaskIds;poll_seconds=$Poll;max_cycles=$Cycles}}
$testRoot=Join-Path $PackageRoot ('work/task-runner-fixture-'+[guid]::NewGuid().ToString('N'));$succeeded=$false
[void][IO.Directory]::CreateDirectory($testRoot)
try {
    $project=Join-Path $testRoot 'project';[void][IO.Directory]::CreateDirectory($project);[void](Invoke-BFGit $project @('init'))
    Write-T (Join-Path $project 'hello.txt') "fixture`n";Write-T (Join-Path $project '.gitignore') ".bsl-flow/`n"
    [void](Invoke-BFGit $project @('add','.'));[void](Invoke-BFGit $project @('-c','user.name=BSL Flow Test','-c','user.email=test@example.invalid','commit','-m','Fixture'))
    $first=Start-BFTask $project (New-Request-T 'First selected task.')
    $second=Start-BFTask $project (New-Request-T 'Second selected task.')
    $executor={param($run);$script:calls.Add($run.state.task_id);return [ordered]@{schema_version=1;status='completed';summary='Inspection completed.';payload_json=(Get-BFCanonicalJson ([ordered]@{complexity='S';risk='low';impact_flags=@();rationale='Read-only fixture.'}))}}
    $queue=New-Queue-T @($first.task_id,$second.task_id)
    $snapshot=Invoke-BFTaskQueue -ProjectPath $project -Input $queue -CodexPath '' -StageExecutor $executor
    Assert-T ($script:calls.Count -eq 2 -and $script:calls[0] -eq $first.task_id -and $script:calls[1] -eq $second.task_id) 'Runner dispatched two explicit tasks once each in queue order.'
    Assert-T ((Read-BFTask $project $first.task_id).status -eq 'completed') 'Selected task completed through the core controller.'
    Assert-T ((Read-BFTask $project $second.task_id).status -eq 'completed') 'Second selected task was skipped by a cursor mutation.'

    # The persisted cursor/event key makes a restart quiet for the completed task.
    $callsBefore=$script:calls.Count;[void](Invoke-BFTaskQueue $project $queue '' $executor)
    Assert-T ($script:calls.Count -eq $callsBefore) 'Restart duplicated a completed task.'
    $snapshotPath=Join-Path $project ('.bsl-flow/runner/queue-'+$queue.queue_id+'-snapshot.json')
    Assert-T ((Read-BFJson $snapshotPath).tasks.$($first.task_id).action -eq 'quiet') 'Operational snapshot did not retain the completed cursor state.'

    $taskLock=Enter-BFLock (Get-BFTaskDirectory $project $second.task_id)
    try {
        $state=Read-BFTask $project $second.task_id;$state.status='needs_input';$state.question=[ordered]@{question_id=[guid]::NewGuid().ToString();text='Choose the business mapping.';intent_revision=$state.intent_revision};$state.blockers=@($state.question.text)
        [void](Save-BFTask $state $state.revision)
    } finally {$taskLock.Dispose()}
    $questionQueue=New-Queue-T @($second.task_id) 2 1;$callsBefore=$script:calls.Count
    [void](Invoke-BFTaskQueue $project $questionQueue '' $executor)
    Assert-T ($script:calls.Count -eq $callsBefore -and (Read-BFTask $project $second.task_id).status -eq 'needs_input') 'NEEDS_INPUT was dispatched without an operator update.'

    # New-BFAttempt fails before worker execution, but this unchanged controller
    # error must not be retried in later polls or after restarting the runner.
    $exhausted=Start-BFTask $project (New-Request-T 'Exhausted attempt budget.')
    $taskLock=Enter-BFLock (Get-BFTaskDirectory $project $exhausted.task_id)
    try {
        $state=Read-BFTask $project $exhausted.task_id
        $state.request | Add-Member -NotePropertyName max_attempts -NotePropertyValue 1
        $state.attempts=@([guid]::NewGuid().ToString())
        [void](Save-BFTask $state $state.revision)
    } finally {$taskLock.Dispose()}
    $errorQueue=New-Queue-T @($exhausted.task_id) 2 1
    [void](Invoke-BFTaskQueue $project $errorQueue '' $executor)
    [void](Invoke-BFTaskQueue $project $errorQueue '' $executor)
    $errorSnapshotPath=Join-Path $project ('.bsl-flow/runner/queue-'+$errorQueue.queue_id+'-snapshot.json')
    $errorStatus=(Read-BFJson $errorSnapshotPath).tasks.$($exhausted.task_id)
    Assert-T ($errorStatus.action -eq 'error' -and $errorStatus.status -eq 'blocked' -and $errorStatus.error -match 'finite task attempt limit reached') 'Operational snapshot lost the unchanged controller error.'
    $errorEvents=Get-Content -LiteralPath (Join-Path $project '.bsl-flow/runner/events.jsonl') | ForEach-Object {ConvertFrom-Json $_} | Where-Object {$_.task_id -eq $exhausted.task_id}
    Assert-T (@($errorEvents|Where-Object {$_.action -eq 'run'}).Count -eq 1 -and @($errorEvents|Where-Object {$_.action -eq 'execution_error'}).Count -eq 1) 'Unchanged controller error was retried after polling or restart.'

    $undispatched=Start-BFTask $project (New-Request-T 'Crash before attempt registration.')
    $pendingQueue=New-Queue-T @($undispatched.task_id)
    $pendingSnapshot=[ordered]@{schema_version=1;queue_id=$pendingQueue.queue_id;queue_sha256=Get-BFHash $pendingQueue;cycle=1;cursor=0;tasks=[ordered]@{};event_keys=@();updated_at=[DateTime]::UtcNow.ToString('o')}
    $pendingSnapshot.tasks[$undispatched.task_id]=[ordered]@{last_key="$($undispatched.task_id)|$($undispatched.revision)|run";revision=$undispatched.revision;status='ready';action='run'}
    Write-BFJson (Join-Path (Get-BFRunnerDirectory $project) ('queue-'+$pendingQueue.queue_id+'-snapshot.json')) $pendingSnapshot
    $callsBefore=$script:calls.Count;$snapshot=Invoke-BFTaskQueue $project $pendingQueue '' $executor
    Assert-T ($script:calls.Count -eq $callsBefore+1 -and $snapshot.tasks[$undispatched.task_id].status -eq 'completed') 'Crash before attempt creation stranded a ready task.'

    $request=New-Request-T 'Interrupted modifying attempt.';$request.mode='implement';$request.criteria=@([pscustomobject]@{id='text';kind='file_assertion';path='hello.txt';contains='changed';observation='Changed greeting.'})
    $interrupted=Start-BFTask $project $request
    $run=New-BFAttempt $project $interrupted.task_id ''
    [void](Invoke-BFStage $run '' $executor)
    $run=New-BFAttempt $project $interrupted.task_id ''
    $startPath=Join-Path $run.directory 'start.json';$start=Read-BFJson $startPath;$start.controller_process.pid=2147483000;Write-T $startPath (Get-BFCanonicalJson $start)
    $callsBefore=$script:calls.Count;$snapshot=Invoke-BFTaskQueue $project (New-Queue-T @($interrupted.task_id)) '' $executor
    Assert-T ($snapshot.tasks[$interrupted.task_id].status -eq 'blocked' -and $snapshot.tasks[$interrupted.task_id].action -eq 'recovery_required' -and $script:calls.Count -eq $callsBefore) 'Dead modifying attempt was hidden as waiting or replayed.'
    $accepted=Read-BFTask $project $first.task_id
    Remove-Item -LiteralPath $accepted.acceptances[-1].path
    $snapshot=Invoke-BFTaskQueue $project (New-Queue-T @($first.task_id)) '' $executor
    Assert-T ($snapshot.tasks[$first.task_id].status -eq 'blocked' -and $snapshot.tasks[$first.task_id].action -eq 'completed_stale') 'Missing acceptance receipt was shown as completed.'

    $invalid=New-Queue-T @($second.task_id);$invalid.poll_seconds=0;Throws-T {Invoke-BFTaskQueue $project $invalid '' $executor} 'BF_INVALID'
    $invalid=New-Queue-T @($second.task_id);$invalid.max_cycles=10001;Throws-T {Invoke-BFTaskQueue $project $invalid '' $executor} 'BF_INVALID'
    $invalid=New-Queue-T @($second.task_id,$second.task_id);Throws-T {Invoke-BFTaskQueue $project $invalid '' $executor} 'BF_INVALID'
    $invalid=New-Queue-T @([guid]::NewGuid().ToString());Throws-T {Invoke-BFTaskQueue $project $invalid '' $executor} 'task does not exist'

    $lock=Enter-BFLock (Get-BFRunnerDirectory $project)
    try {Throws-T {Invoke-BFTaskQueue $project (New-Queue-T @($second.task_id)) '' $executor} 'Writer lock'} finally {$lock.Dispose()}
    Write-Output "Task runner checks passed: $script:checks"
    $succeeded=$true
} finally {
    if($succeeded -and -not $KeepFixture -and (Test-Path -LiteralPath $testRoot)){
        $workRoot=[IO.Path]::GetFullPath((Join-Path $PackageRoot 'work'))
        $resolved=[IO.Path]::GetFullPath($testRoot)
        if(-not $resolved.StartsWith($workRoot+[IO.Path]::DirectorySeparatorChar,[StringComparison]::OrdinalIgnoreCase)){throw 'Unsafe fixture cleanup target.'}
        Remove-Item -LiteralPath $resolved -Recurse -Force
    }
    elseif(-not $succeeded){Write-Host "Fixture retained for inspection: $testRoot"}
}
