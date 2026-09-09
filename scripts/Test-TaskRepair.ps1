[CmdletBinding()]
param([string]$PackageRoot)
Set-StrictMode -Version Latest
$ErrorActionPreference='Stop'
if(-not $PackageRoot){$PackageRoot=Split-Path $PSScriptRoot -Parent}
foreach($file in @('Task.Storage','Task.Contracts','Task.Gates','Task.Process','Task.Engine','Task.Stages')){. (Join-Path $PackageRoot "global/skills/1c-task/scripts/$file.ps1")}
. (Join-Path $PackageRoot 'global/skills/1c-task/adapters/Codex.ps1')
$script:checks=0
function Check([bool]$Value,[string]$Reason){if(-not $Value){throw "ASSERTION FAILED: $Reason"};$script:checks++}
function Throws([scriptblock]$Body,[string]$Pattern){$message='';try{& $Body|Out-Null}catch{$message=$_.Exception.Message};Check ($message -match $Pattern) "Expected $Pattern, observed $message"}
function Put([string]$Path,[string]$Content){[void][IO.Directory]::CreateDirectory((Split-Path $Path -Parent));[IO.File]::WriteAllText($Path,$Content,(New-Object Text.UTF8Encoding($false)))}
$root=Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-repair-'+[guid]::NewGuid().ToString('N'))
$project=Join-Path $root 'project';[void][IO.Directory]::CreateDirectory($project)
try{
    $single=[pscustomobject]@{id='single';kind='unit';observation='One command argument, selected test and protected directory.';executable=(Get-Process -Id $PID).Path;arguments=@('--version');report='.bsl-flow-worker/test.xml';expected_tests=@('one');retry_safe=$true;protected_paths=@('tests')}
    Assert-BFCriteria @($single);Check $true 'Singleton command/test/protected arrays stay arrays'
    $single.arguments=@();Assert-BFCriteria @($single);Check $true 'Empty argv is a valid array'
    $single.protected_paths='tests';Throws {Assert-BFCriteria @($single)} 'protected_paths'
    $single.protected_paths=@();Throws {Assert-BFCriteria @($single)} 'protected_paths'
    $single.protected_paths=@('tests');$single.expected_tests='one';Throws {Assert-BFCriteria @($single)} 'expected test names'
    [void](Invoke-BFGit $project @('init'))
    Put (Join-Path $project 'hello.txt') 'Initial'
    Put (Join-Path $project 'tests/check.ps1') 'Original independent test assertion'
    Put (Join-Path $project '.gitignore') ".bsl-flow/`nopenspec/changes/`n"
    [void](Invoke-BFGit $project @('add','.'))
    [void](Invoke-BFGit $project @('-c','user.name=BSL Flow Test','-c','user.email=test@example.invalid','commit','-m','Fixture'))
    function Request([int]$Repairs=1){return [pscustomobject]@{schema_version=1;request_id=[guid]::NewGuid().ToString();prompt='Write the final greeting.';mode='implement';analysis_goal='analysis';complexity='S';risk='low';impact_flags=@();criteria=@([pscustomobject]@{id='greeting';observation='The final greeting is present.';kind='file_assertion';path='hello.txt';contains='Final greeting'});provenance=[pscustomobject]@{source='user';reference='repair-fixture';text='Implement and fix failed source assertions.'};models=[pscustomobject]@{worker='gpt-6-astra';worker_effort='medium';reviewer='gpt-6-astra';reviewer_effort='high'};max_source_repairs=$Repairs}}
    $script:behavior='fix';$script:trace=New-Object 'System.Collections.Generic.List[string]'
    $executor={param($run)
        $stage=$run.attempt.stage;$script:trace.Add($stage)
        $payload=@{}
        switch($stage){
            'inspect'{$payload=@{complexity='S';risk='low';impact_flags=@();rationale='Pure source fixture.'}}
            'implement'{
                $text=if($run.state.repair.rounds -gt 0 -and $script:behavior -eq 'fix'){'Final greeting'}elseif($script:behavior -eq 'exhaust'){'Wrong '+$run.state.repair.rounds}else{'Wrong greeting'}
                Put (Join-Path $run.state.worker_path 'hello.txt') $text
                if($run.state.repair.rounds -gt 0 -and $script:behavior -eq 'weaken_test'){Put (Join-Path $run.state.worker_path 'tests/check.ps1') 'Always emit passing JUnit'}
                $payload=@{changed_files=@('hello.txt')}
            }
            'verify'{
                if($script:behavior -eq 'weaken_test'){Stop-BFVerificationFailure $run.state $run.state.request.criteria[0] 'BF_FAIL: independent assertion failed.'}
                if($script:behavior -eq 'unknown'){throw 'BF_FAIL: worker text does not prove a completed verifier.'}
                if($script:behavior -eq 'tamper'){Put (Join-Path $run.state.worker_path 'unexpected.txt') 'changed by test'}
                return Invoke-BFVerification $run.state (Join-Path $run.directory 'raw') '' $null
            }
            'diagnose'{
                $category=if($script:behavior -in @('environment','business_rule','test_contract')){$script:behavior}else{'implementation'}
                $payload=@{failure_attempt_id=$run.state.repair.pending_failure;category=$category;reason='The required greeting is absent; restore its defined text.';evidence='Retained file assertion and current hello.txt.';fix_instructions='Write Final greeting without changing the criterion.'}
                if($script:behavior -eq 'wrong_identity'){$payload.failure_attempt_id=[guid]::NewGuid().ToString()}
            }
            'code_review'{$payload=@{verdict='PASS';findings=@()}}
            default{throw "Unexpected stage $stage"}
        }
        return [ordered]@{schema_version=1;status='completed';summary='Fixture result';payload_json=Get-BFCanonicalJson $payload}
    }
    function RunCase([string]$Behavior,[int]$Repairs=1){
        $script:behavior=$Behavior;$script:trace.Clear()
        $state=Start-BFTask $project (Request $Repairs)
        return Invoke-BFRun $project $state.task_id '' $executor
    }
    $state=RunCase 'fix'
    Check ($state.status -eq 'completed') 'Failed criterion repaired to real acceptance'
    Check (($script:trace -join ',') -eq 'inspect,implement,verify,diagnose,implement,code_review,verify') 'Independent review and fresh verification after source repair'
    Check ($state.repair.rounds -eq 1 -and $null -eq $state.unresolved_effect) 'Only one safe repair consumed'
    Check (@($state.evidence|Where-Object{$_.outcome -eq 'FAIL'}).Count -eq 1) 'Original failure retained'
    $receipt=Read-BFJson $state.acceptances[-1].path
    Check ($receipt.source_manifest.sha256 -eq (Get-BFSourceManifest $state).sha256) 'Acceptance binds corrected source'
    $unchanged=Invoke-BFRun $project $state.task_id '' $executor
    Check ($unchanged.revision -eq $state.revision) 'Completed repaired task not rerun'
    $clarification=[pscustomobject]@{schema_version=1;input_event_id=[guid]::NewGuid().ToString();expected_revision=$state.revision;kind='clarification';text='Keep the final greeting and inspect the revised intent.';provenance=[pscustomobject]@{source='user';reference='after-repair';text='Inspect the revised intent.'}}
    $state=Update-BFTask $project $state.task_id $clarification
    $run=New-BFAttempt $project $state.task_id ''
    Check ($run.attempt.stage -eq 'inspect' -and $run.state.repair.rounds -eq 1 -and $null -eq $run.state.repair.diagnosis_attempt) 'Clarification after repair dispatches with spent budget retained'
    [void](Invoke-BFStage $run '' $executor)
    Write-Host 'Task repair: happy path, fresh gates and idempotent acceptance PASS.'

    $state=RunCase 'fix' 0
    Check ($state.status -eq 'failed' -and $script:trace -notcontains 'diagnose') 'Legacy no-repair default stops'
    $state=RunCase 'exhaust' 2
    Check ($state.status -eq 'failed' -and $state.repair.rounds -eq 2) 'Repair budget terminates changing wrong implementations'
    Check (@($script:trace|Where-Object{$_ -eq 'diagnose'}).Count -eq 2) 'No extra diagnosis beyond repair budget'
    $state=RunCase 'no_progress' 3
    Check ($state.status -eq 'failed' -and $state.repair.rounds -eq 1) 'Unchanged failed source stops before repeated diagnosis'
    $state=RunCase 'environment'
    Check ($state.status -eq 'blocked' -and $state.repair.rounds -eq 0) 'Environment diagnosis does not launch implementation'
    $state=RunCase 'test_contract'
    Check ($state.status -eq 'blocked') 'Model cannot weaken test contract'
    $state=RunCase 'business_rule'
    Check ($state.status -eq 'needs_input' -and $null -ne $state.question) 'Business diagnosis asks a durable question'
    $before=$state.revision
    $state=Invoke-BFRun $project $state.task_id '' $executor
    Check ($state.revision -eq $before) 'Pending question does not repeat diagnosis'
    $event=[pscustomobject]@{schema_version=1;input_event_id=[guid]::NewGuid().ToString();expected_revision=$state.revision;kind='clarification';question_id=$state.question.question_id;text='Keep the original final greeting criterion.';provenance=[pscustomobject]@{source='user';reference='reply';text='Keep the criterion.'}}
    $state=Update-BFTask $project $state.task_id $event
    Check ($null -eq $state.repair.pending_failure -and (Get-BFNext $state).stage -eq 'inspect') 'Clarification re-inspects new intent instead of diagnosing stale failure'
    $state=RunCase 'wrong_identity'
    Check ($state.status -eq 'blocked' -and $state.repair.rounds -eq 0) 'Diagnosis cannot refer to another attempt'
    $state=RunCase 'unknown'
    Check ($null -ne $state.unresolved_effect -and $script:trace -notcontains 'diagnose') 'Plain FAIL text cannot authorize repair'
    $state=RunCase 'tamper'
    Check ($state.status -eq 'blocked' -and $null -ne $state.unresolved_effect) 'Failed verifier source mutation blocks repair'

    # Stop at a registered failure, then alter its retained result and ensure diagnosis is denied.
    $script:behavior='fix';$state=Start-BFTask $project (Request)
    foreach($stage in @('inspect','implement','verify')){$run=New-BFAttempt $project $state.task_id '';$state=Invoke-BFStage $run '' $executor}
    Check ((Get-BFNext $state).stage -eq 'diagnose') 'Safe failure schedules diagnosis'
    $failurePath=Join-Path (Get-BFTaskDirectory $project $state.task_id) ('attempts/'+$state.repair.pending_failure+'/result.json')
    $failure=Read-BFJson $failurePath;$failure.summary='Tampered';Put $failurePath (Get-BFCanonicalJson $failure)
    Throws {Get-BFNext $state} 'trusted safe repair'

    $script:behavior='weaken_test';$script:trace.Clear();$request=Request
    $request.criteria=@([pscustomobject]@{id='unit';kind='unit';observation='Independent test checks the greeting.';executable=(Get-Process -Id $PID).Path;arguments=@('-File','tests/check.ps1');report='.bsl-flow-worker/result.xml';expected_tests=@('exact');retry_safe=$true;protected_paths=@('tests')})
    $state=Start-BFTask $project $request
    $state=Invoke-BFRun $project $state.task_id '' $executor
    Check ($state.status -eq 'blocked' -and $null -ne $state.unresolved_effect -and ($state.blockers -join ' ') -match 'protected test inputs') 'Repair cannot replace the test with a green report emitter'
    Check (@($script:trace|Where-Object{$_ -eq 'verify'}).Count -eq 1) 'Weakened test is never executed a second time'

    # Saved read-only diagnosis can be recovered without a second worker call.
    $script:behavior='fix';$state=Start-BFTask $project (Request)
    foreach($stage in @('inspect','implement','verify')){$run=New-BFAttempt $project $state.task_id '';$state=Invoke-BFStage $run '' $executor}
    $run=New-BFAttempt $project $state.task_id ''
    $raw=Join-Path $run.directory 'raw/worker';[void][IO.Directory]::CreateDirectory($raw)
    Write-BFJson (Join-Path $raw 'exit.json') ([ordered]@{exit_code=0;stop_reason=$null})
    Write-BFJson (Join-Path $raw 'host-result.json') ([ordered]@{session_id='saved-diagnosis'})
    Write-BFJson (Join-Path $raw 'model-result.json') (& $executor $run)
    $start=Read-BFJson (Join-Path $run.directory 'start.json');$start.controller_process.pid=2147483000
    Put (Join-Path $run.directory 'start.json') (Get-BFCanonicalJson $start)
    $state=Resume-BFAttempt $project $state.task_id
    Check ($state.status -eq 'ready' -and $state.repair.rounds -eq 1 -and $null -eq $state.active_attempt) 'Completed diagnosis resumes from retained raw result'

    $junit=Join-Path $root 'tests.xml'
    Put $junit '<testsuite tests="1" failures="1"><testcase name="exact"><failure message="wrong"/></testcase></testsuite>'
    Check ((Test-BFJUnit $junit @('exact') -AllowFailure).outcome -eq 'FAIL') 'Exact original failed JUnit can be diagnosed'
    Throws {Test-BFJUnit $junit @('exact')} 'BF_FAIL'
    Put $junit '<testsuite tests="2"><testcase name="exact"><failure/></testcase><testcase name="other"><skipped/></testcase></testsuite>'
    Throws {Test-BFJUnit $junit @('exact','other') -AllowFailure} 'BF_BLOCKED'
    Put $junit '<testsuite errors="garbage"><testcase name="exact"><failure/></testcase></testsuite>'
    Throws {Test-BFJUnit $junit @('exact') -AllowFailure} 'BF_BLOCKED'
    Put $junit '<testsuite tests="1" failures="0"><testcase name="exact"><failure/></testcase></testsuite>'
    Throws {Test-BFJUnit $junit @('exact') -AllowFailure} 'BF_BLOCKED'
    $request=Request 4;Throws {Assert-BFRequest $request} 'max_source_repairs'
    $request=Request;$request.criteria[0]|Add-Member retry_safe 'yes';Throws {Assert-BFRequest $request} 'retry_safe'
    Write-Output "Task repair: $script:checks checks PASS; model calls: 0."
}finally{
    $resolved=[IO.Path]::GetFullPath($root)
    $temp=[IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\','/')
    if($resolved.StartsWith($temp+[IO.Path]::DirectorySeparatorChar,[StringComparison]::OrdinalIgnoreCase) -and (Split-Path $resolved -Leaf) -like 'bsl-flow-repair-*'){Remove-Item -LiteralPath $resolved -Recurse -Force}
}
