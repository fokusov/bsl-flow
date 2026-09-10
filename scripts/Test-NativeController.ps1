#Requires -Version 7.0
[CmdletBinding()]
param([string]$PackageRoot)
Set-StrictMode -Version Latest
$ErrorActionPreference='Stop'
if(-not $PackageRoot){$PackageRoot=Split-Path $PSScriptRoot -Parent}
foreach($name in @('Storage','Contracts','Gates','Process','Engine','Stages')){. (Join-Path $PackageRoot "global/skills/1c-task/scripts/Task.$name.ps1")}
$script:checks=0
function Check([bool]$Value,[string]$Message){if(-not $Value){throw "ASSERTION FAILED: $Message"};$script:checks++}
function Throws([scriptblock]$Body,[string]$Pattern){$message='';try{& $Body|Out-Null}catch{$message=$_.Exception.Message};Check ($message -match $Pattern) "Expected $Pattern, got $message"}
function Put([string]$Path,[string]$Value){[void][IO.Directory]::CreateDirectory((Split-Path $Path -Parent));[IO.File]::WriteAllText($Path,$Value)}
# This suite exercises controller transitions, with no platform/database access.
# Adapter validation and dispatch are tested separately by Test-TaskRuntime.
function Assert-BFNativeCriterion($Criterion){}
function Get-BFNativeDependencies($Criterion){return @{executable_sha256='fixture-platform'}}
function Resolve-BFNativeRecovery($State,$Resolution,$AttemptDir){$script:recoveryCalls++;return @{fixture='control-read';target=$Resolution.target}}
function Complete-BFNativeRecovery($State,$Resolution){
    $saved=Read-BFTask $State.project_path $State.task_id
    Check ($saved.revision -eq $State.revision -and $null -eq $saved.unresolved_effect) 'Target latch released only after task recovery revision is durable.'
}
$root=Join-Path $PackageRoot ('work/native-controller-'+[guid]::NewGuid().ToString('N'))
$project=Join-Path $root 'project'
[void][IO.Directory]::CreateDirectory($project)
try {
    [void](Invoke-BFGit $project @('init'))
    Put (Join-Path $project 'hello.txt') 'before'
    Put (Join-Path $project 'tests/contract.txt') 'Independent expected observation.'
    Put (Join-Path $project '.gitignore') ".bsl-flow/`nopenspec/changes/`n"
    [void](Invoke-BFGit $project @('add','.'))
    [void](Invoke-BFGit $project @('-c','user.name=BSL Flow Test','-c','user.email=test@example.invalid','commit','-m','Fixture'))
    $request=[pscustomobject]@{schema_version=1;request_id=[guid]::NewGuid().ToString();prompt='Native controller fixture.';mode='implement';analysis_goal='analysis';complexity='S';risk='low';impact_flags=@();criteria=@([pscustomobject]@{id='native';kind='integration';observation='Exact native cases pass.';executable=(Get-Process -Id $PID).Path;arguments=@();report='.bsl-flow-worker/junit.xml';expected_tests=@('fixture.case');target=(Join-Path $root 'never-opened-base');native_1c=@{fixture=$true}});provenance=@{source='user';reference='offline';text='No database execution.'};models=@{worker='gpt-5.6-sol';worker_effort='medium';reviewer='gpt-6-astra';reviewer_effort='high'}}
    $script:verifyCalls=0;$script:recoveryCalls=0
    $request.criteria[0] | Add-Member -NotePropertyName protected_paths -NotePropertyValue @('tests')
    $request | Add-Member requirements @(@{id='native-result';text='Exact native cases pass.';criterion_ids=@('native')})
    $script:tamper=$false
    $executor={param($run)
        $payload=@{}
        switch($run.attempt.stage){
            'inspect'{$payload=@{complexity='S';risk='low';impact_flags=@();rationale='Controller transition fixture.'}}
            'implement'{Put (Join-Path $run.state.worker_path 'hello.txt') 'after';if($script:tamper){Put (Join-Path $run.state.worker_path 'tests/contract.txt') 'Always pass'};$payload=@{changed_files=@('hello.txt')}}
            'code_review'{$payload=@{verdict='PASS';findings=@();coverage_review=@{verdict='PASS';assessments=@(@{requirement_id='native-result';verdict='SUFFICIENT';rationale='The independent fixture declares the exact case.';criterion_evidence=@(@{criterion_id='native';test_ids=@('fixture.case');source_paths=@('tests/contract.txt');observation='The expected fixture case executes.';evidence='tests/contract.txt retains the independent expected observation.'})})}}}
            'verify'{$script:verifyCalls++;throw 'BF_BLOCKED: simulated uncertain native result.'}
            default{throw 'Unexpected fixture stage.'}
        }
        return @{schema_version=1;status='completed';summary='Fixture stage.';payload_json=(Get-BFCanonicalJson $payload)}
    }
    $state=Start-BFTask $project $request
    $state=Invoke-BFRun $project $state.task_id '' $executor
    Check ($state.unresolved_effect.scope -eq 'native_1c') 'Unknown native result has runtime scope.'
    Check ($script:verifyCalls -eq 1) 'Native verification dispatched once.'
    $again=Invoke-BFRun $project $state.task_id '' $executor
    Check ($script:verifyCalls -eq 1 -and $again.status -eq 'blocked') 'Unknown result does not repeat dispatch.'
    $event=@{schema_version=1;input_event_id=[guid]::NewGuid().ToString();expected_revision=$state.revision;kind='recovery';provenance=@{source='user';reference='fixture';text='Reconcile fixture only.'};resolution=@{attempt_id=$state.unresolved_effect.attempt_id;scope='source_only';source_sha256=(Get-BFSourceManifest $state).sha256;observation='Source inspection cannot resolve a database write.'}}
    Throws {Update-BFTask $project $state.task_id $event} 'requires native_1c'
    Check ($script:recoveryCalls -eq 0) 'Source-only recovery did not call runtime reader.'
    $state=Cancel-BFTask $project $state.task_id
    $event.expected_revision=$state.revision;$event.resolution.scope='native_1c'
    $event.resolution.target=$request.criteria[0].target;$event.resolution.inventory_sha256=('a'*64);$event.resolution.retry_authorized=$true
    $state=Update-BFTask $project $state.task_id $event
    Check ($state.status -eq 'cancelled' -and $null -eq $state.unresolved_effect) 'Control read preserves cancellation without manufacturing PASS.'
    Check ($script:recoveryCalls -eq 1 -and $state.acceptances.Count -eq 0) 'Recovery observed once and created no acceptance.'
    $again=Update-BFTask $project $state.task_id $event
    Check ($again.revision -eq $state.revision -and $script:recoveryCalls -eq 1) 'Duplicate recovery input is idempotent.'
    $request.request_id=[guid]::NewGuid().ToString();$script:tamper=$true
    $tampered=Start-BFTask $project $request
    $tampered=Invoke-BFRun $project $tampered.task_id '' $executor
    Check ($tampered.status -eq 'blocked' -and $tampered.blockers[0] -match 'protected native test inputs') 'Worker cannot weaken native tests during initial implementation.'
    Check ($script:verifyCalls -eq 1) 'Weakened test inputs prevent native dispatch.'
    # If this calls Get-BFOwnedProcess, it would be an attempted native kill.
    function Get-BFOwnedProcess($Identity){throw 'Native cancellation reached process termination.'}
    Stop-BFOwnedProcess @{pid=$PID;start_time_utc='fixture';non_interruptible=$true}
    Check $true 'Non-interruptible native process is never killed by generic Cancel.'
    Write-Output "Native controller checks: $script:checks PASS; database/model calls: 0."
} finally {
    # Keep failures and successful fixtures inspectable in ignored work/.
}
