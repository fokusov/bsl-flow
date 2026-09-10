#Requires -Version 7.0
[CmdletBinding()]
param([string]$PackageRoot)

Set-StrictMode -Version Latest
$ErrorActionPreference='Stop'
if(-not $PackageRoot){$PackageRoot=Split-Path $PSScriptRoot -Parent}
$core=Join-Path $PackageRoot 'global/skills/1c-task/scripts'
foreach($name in @('Task.Storage.ps1','Task.Contracts.ps1','Task.Gates.ps1','Task.Process.ps1','Task.Engine.ps1','Task.Stages.ps1')){. (Join-Path $core $name)}
. (Join-Path $PackageRoot 'global/skills/1c-task/adapters/Codex.ps1')

$script:checks=0
function Assert-Coverage([bool]$Condition,[string]$Message){if(-not $Condition){throw "ASSERTION FAILED: $Message"};$script:checks++}
function Assert-CoverageFailure([scriptblock]$Body,[string]$Pattern,[string]$Description){
    $actual=''
    try {& $Body | Out-Null}catch{$actual=$_.Exception.Message}
    Assert-Coverage (-not [string]::IsNullOrWhiteSpace($actual)) "$Description unexpectedly succeeded."
    Assert-Coverage ($actual -match $Pattern) "$Description rejected with unexpected error: $actual"
}
function Write-CoverageFile([string]$Path,[string]$Text){[void][IO.Directory]::CreateDirectory((Split-Path $Path -Parent));[IO.File]::WriteAllText($Path,$Text,(New-Object Text.UTF8Encoding($false)))}
function New-CoverageProject([string]$Root,[string]$Name){
    $project=Join-Path $Root $Name;[void][IO.Directory]::CreateDirectory($project)
    [void](Invoke-BFGit $project @('init'))
    Write-CoverageFile (Join-Path $project '.gitignore') ".bsl-flow/`nopenspec/changes/`n"
    Write-CoverageFile (Join-Path $project 'hello.txt') "Initial greeting`n"
    Write-CoverageFile (Join-Path $project 'tests/protected.ps1') "# Fixture protected input`n"
    [void](Invoke-BFGit $project @('add','.'));[void](Invoke-BFGit $project @('-c','user.name=BF Coverage Test','-c','user.email=test@example.invalid','commit','-m','Fixture'))
    return $project
}
function New-CoverageRequest([bool]$WithRequirements=$true,[bool]$Unit=$false){
    $criterion=if($Unit){
        [pscustomobject]@{id='protected-unit';observation='The protected fixture test remains unchanged.';kind='unit';executable=(Get-Process -Id $PID).Path;arguments=@();report='.bsl-flow-worker/protected.junit.xml';expected_tests=@('Fixture.Protected');protected_paths=@('tests')}
    }else{
        [pscustomobject]@{id='greeting';observation='The tracked greeting contains Example greeting.';kind='file_assertion';path='hello.txt';contains='Example greeting'}
    }
    $request=[pscustomobject]@{schema_version=1;request_id=[guid]::NewGuid().ToString();prompt='Add the Example greeting.';mode='implement';analysis_goal='analysis';complexity='S';risk='low';impact_flags=@();criteria=@($criterion);provenance=[pscustomobject]@{source='user';reference='coverage fixture';text='Implement the requested greeting.'};models=[pscustomobject]@{worker='gpt-6-astra';worker_effort='medium';reviewer='gpt-6-astra';reviewer_effort='high'}}
    if($WithRequirements){$request|Add-Member -NotePropertyName requirements -NotePropertyValue @([pscustomobject]@{id='REQ-1';text='The greeting requirement is observed by its declared criterion.';criterion_ids=@($criterion.id)})}
    return $request
}
function New-CoveragePayload([string]$Verdict='PASS'){
    $sufficient=$Verdict -eq 'PASS'
    $assessmentVerdict=if($sufficient){'SUFFICIENT'}else{'INSUFFICIENT'}
    [string[]]$paths=if($sufficient){@('hello.txt')}else{@()}
    $rationale=if($sufficient){'The file assertion directly observes the requirement.'}else{'The review deliberately records an insufficient fixture.'}
    return [ordered]@{verdict=$Verdict;assessments=@([ordered]@{requirement_id='REQ-1';verdict=$assessmentVerdict;criterion_evidence=@([ordered]@{criterion_id='greeting';test_ids=@();source_paths=$paths;observation='The declared file assertion reads hello.txt.';evidence='The exact asserted source path is present in the worker.'});rationale=$rationale})}
}
function New-CoverageExecutor($Coverage){
    $script:CoverageFixture=$Coverage
    return {
        param($Run)
        switch($Run.attempt.stage){
            'inspect' {$payload='{"complexity":"S","risk":"low","impact_flags":[],"rationale":"The fixture is a bounded source-only change."}'}
            'implement' {Write-CoverageFile (Join-Path $Run.state.worker_path 'hello.txt') "Example greeting`n";$payload='{"changed_files":["hello.txt"]}'}
            'code_review' {$payload=([ordered]@{verdict='PASS';findings=@();coverage_review=$script:CoverageFixture}|ConvertTo-Json -Depth 12 -Compress)}
            'verify' {return Invoke-BFVerification $Run.state (Join-Path $Run.directory 'raw') '' $null}
            default {throw "Unexpected coverage fixture stage $($Run.attempt.stage)"}
        }
        return [ordered]@{schema_version=1;status='completed';summary='Coverage fixture stage complete.';payload_json=$payload}
    }
}
function Invoke-CoverageStages([string]$Project,[string]$TaskId,[scriptblock]$Executor,[int]$Count){
    $task=Read-BFTask $Project $TaskId
    for($i=0;$i -lt $Count;$i++){
        $next=Get-BFNext $task
        if($next.action -ne 'dispatch'){throw "Fixture could not dispatch stage $i ($($next.stage)): $(@($next.blockers) -join '; ')"}
        $task=Invoke-BFStage (New-BFAttempt $Project $TaskId '') '' $Executor $null
    }
    return $task
}
function Get-CoverageReviewAttempt($Task){return @($Task.evidence|Where-Object stage -eq 'code_review')[-1]}

$testRoot=Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-coverage-'+[guid]::NewGuid().ToString('N'))
[void][IO.Directory]::CreateDirectory($testRoot)
$succeeded=$false
try {
    # A trusted requirement forces independent review, while legacy requests retain the short route.
    $routeState=[pscustomobject]@{request=(New-CoverageRequest $true $false);classification=[pscustomobject]@{complexity='S';risk='low'}}
    Assert-Coverage ((@(Get-BFRoute $routeState)-join ',') -eq 'inspect,implement,code_review,verify,acceptance') 'Requirements did not force code_review on the S/low route.'
    $legacyState=[pscustomobject]@{request=(New-CoverageRequest $false $false);classification=[pscustomobject]@{complexity='S';risk='low'}}
    Assert-Coverage ((@(Get-BFRoute $legacyState)-join ',') -eq 'inspect,implement,verify,acceptance') 'Request without requirements did not retain the legacy route.'

    # A green file assertion cannot bypass an independently recorded coverage gap.
    $blockedProject=New-CoverageProject $testRoot 'blocked';$blockedRequest=New-CoverageRequest
    $blocked=Start-BFTask $blockedProject $blockedRequest
    $blocked=Invoke-CoverageStages $blockedProject $blocked.task_id (New-CoverageExecutor (New-CoveragePayload 'BLOCK')) 3
    Assert-Coverage ($blocked.status -eq 'blocked') 'Coverage BLOCK did not block the controller.'
    Assert-Coverage ((Get-BFNext $blocked).action -eq 'blocked') 'Coverage BLOCK still allowed verification dispatch.'
    Assert-Coverage (([IO.File]::ReadAllText((Join-Path $blocked.worker_path 'hello.txt'))).Contains('Example greeting')) 'Fixture file assertion was not green before coverage block.'

    # Missing coverage data is a controller failure, never a silently accepted PASS review.
    $missingProject=New-CoverageProject $testRoot 'missing';$missing=Start-BFTask $missingProject (New-CoverageRequest)
    $missingExecutor=New-CoverageExecutor $null
    $missing=Invoke-CoverageStages $missingProject $missing.task_id $missingExecutor 3
    $missingReason=@($missing.blockers) -join '; '
    Assert-Coverage ($missing.status -eq 'blocked' -and $missingReason -match 'coverage_review') "Missing coverage payload did not expose its actual controller error: $missingReason"

    # Full coverage produces a binding.  Damaged retained evidence prohibits both
    # the real verifier and acceptance; restore each byte before testing success.
    $project=New-CoverageProject $testRoot 'pass';$request=New-CoverageRequest;$task=Start-BFTask $project $request
    $executor=New-CoverageExecutor (New-CoveragePayload 'PASS')
    $task=Invoke-CoverageStages $project $task.task_id $executor 3
    $review=Get-CoverageReviewAttempt $task;$reviewDir=Join-Path (Get-BFTaskDirectory $project $task.task_id) ('attempts/'+$review.attempt_id)
    $binding=Join-Path $reviewDir 'raw/coverage-review-binding.json'
    Assert-Coverage (Test-Path -LiteralPath $binding -PathType Leaf) 'Passing coverage review did not retain its binding.'
    $task=Invoke-CoverageStages $project $task.task_id $executor 1
    Assert-Coverage ((Get-BFNext $task).action -eq 'accept') 'Full coverage did not satisfy every controller gate.'
    $accepted=Accept-BFTask $project $task.task_id
    Assert-Coverage ($accepted.acceptances.Count -eq 1) 'Full coverage did not permit acceptance.'
    $bindingBytes=[IO.File]::ReadAllBytes($binding);$resultPath=Join-Path $reviewDir 'result.json';$resultBytes=[IO.File]::ReadAllBytes($resultPath)
    Write-CoverageFile $binding '{"tampered":true}'
    Assert-CoverageFailure {Invoke-BFVerification (Read-BFTask $project $task.task_id) (Join-Path $reviewDir 'raw/verify-probe') '' $null} 'BF_BLOCKED|BF_CONFLICT|BF_INVALID' 'Tampered binding verification'
    Assert-CoverageFailure {Accept-BFTask $project $task.task_id} 'BF_BLOCKED|BF_CONFLICT|BF_INVALID' 'Tampered binding acceptance'
    [IO.File]::WriteAllBytes($binding,$bindingBytes)
    Remove-Item -LiteralPath $binding -Force
    Assert-CoverageFailure {Invoke-BFVerification (Read-BFTask $project $task.task_id) (Join-Path $reviewDir 'raw/verify-probe') '' $null} 'fresh independent code review|registered coverage binding is missing' 'Missing binding verification'
    Assert-CoverageFailure {Accept-BFTask $project $task.task_id} 'fresh independent code review|registered coverage binding is missing|acceptance requires code_review' 'Missing binding acceptance'
    [IO.File]::WriteAllBytes($binding,$bindingBytes)
    Write-CoverageFile $resultPath '{"tampered":true}'
    Assert-CoverageFailure {Invoke-BFVerification (Read-BFTask $project $task.task_id) (Join-Path $reviewDir 'raw/verify-probe') '' $null} 'BF_BLOCKED' 'Tampered review result verification'
    Assert-CoverageFailure {Accept-BFTask $project $task.task_id} 'BF_BLOCKED' 'Tampered review result acceptance'
    [IO.File]::WriteAllBytes($resultPath,$resultBytes)
    # A requirement text change changes intent and invalidates the old acceptance.
    $changed=New-CoverageRequest;$changed.request_id=$accepted.task_id;$changed.requirements[0].text='The changed requirement has new trusted scope.'
    $event=[ordered]@{schema_version=1;input_event_id=[guid]::NewGuid().ToString();expected_revision=$accepted.revision;kind='scope_change';request=$changed;provenance=$request.provenance}
    $scoped=Update-BFTask $project $accepted.task_id ([pscustomobject]$event)
    Assert-Coverage ($scoped.intent_hash -ne $accepted.intent_hash) 'Requirement scope change did not alter intent hash.'
    Assert-Coverage ((Get-BFNext $scoped).action -ne 'accept') 'Old acceptance remained valid after requirement scope change.'

    # The initial implementation cannot change declared protected test inputs when requirements exist.
    $protectedProject=New-CoverageProject $testRoot 'protected';$protectedRequest=New-CoverageRequest $true $true;$protected=Start-BFTask $protectedProject $protectedRequest
    $protectedExecutor={param($run)
        if($run.attempt.stage -eq 'inspect'){$payload='{"complexity":"S","risk":"low","impact_flags":[],"rationale":"Fixture inspection."}'}
        elseif($run.attempt.stage -eq 'implement'){Write-CoverageFile (Join-Path $run.state.worker_path 'tests/protected.ps1') '# changed by implementation';$payload='{"changed_files":["tests/protected.ps1"]}'}
        else{throw "Unexpected protected fixture stage $($run.attempt.stage)"}
        [ordered]@{schema_version=1;status='completed';summary='Protected fixture stage complete.';payload_json=$payload}
    }
    $protected=Invoke-CoverageStages $protectedProject $protected.task_id $protectedExecutor 2
    $protectedReason=@($protected.blockers) -join '; '
    Assert-Coverage ($protected.status -eq 'blocked' -and $protectedReason -match 'protected native test inputs') "Initial protected-input edit did not block with its actual error: $protectedReason"

    $succeeded=$true
    Write-Host "Coverage controller: $script:checks checks PASS. Isolated fixture will be removed at $testRoot"
} finally {
    $absolute=[IO.Path]::GetFullPath($testRoot);$temp=[IO.Path]::GetFullPath([IO.Path]::GetTempPath())
    if($succeeded -and $absolute.StartsWith($temp,[StringComparison]::OrdinalIgnoreCase) -and (Split-Path $absolute -Leaf).StartsWith('bsl-flow-coverage-',[StringComparison]::Ordinal)){
        Remove-Item -LiteralPath $absolute -Recurse -Force -ErrorAction Stop
    }elseif(-not $succeeded){
        Write-Host "Coverage controller fixture retained after failure at $testRoot"
    }
}
