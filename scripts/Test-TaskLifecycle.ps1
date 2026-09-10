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
function Assert-Task([bool]$Condition,[string]$Message){if(-not $Condition){throw "ASSERTION FAILED: $Message"};$script:checks++}
function Assert-TaskThrows([scriptblock]$Body,[string]$Pattern){$message='';try{& $Body | Out-Null}catch{$message=$_.Exception.Message};Assert-Task ($message -match $Pattern) "Expected $Pattern, got $message"}
function Write-Fixture([string]$Path,[string]$Text){[void][IO.Directory]::CreateDirectory((Split-Path $Path -Parent));[IO.File]::WriteAllText($Path,$Text,(New-Object Text.UTF8Encoding($false)))}
$testRoot=Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-lifecycle-'+[guid]::NewGuid().ToString('N'))
[void][IO.Directory]::CreateDirectory($testRoot)
$project=Join-Path $testRoot 'project';[void][IO.Directory]::CreateDirectory($project)
[void](Invoke-BFGit $project @('init'))
Write-Fixture (Join-Path $project 'hello.txt') "Initial greeting`n"
Write-Fixture (Join-Path $project '.gitignore') ".bsl-flow/`nopenspec/changes/`n"
[void](Invoke-BFGit $project @('add','.'))
[void](Invoke-BFGit $project @('-c','user.name=BSL Flow Test','-c','user.email=test@example.invalid','commit','-m','Fixture'))
function New-Request([string]$Mode='implement',[string]$Size='S',[string]$Risk='low'){
    return [pscustomobject]@{schema_version=1;request_id=[guid]::NewGuid().ToString();prompt='Add Example greeting to hello.txt.';mode=$Mode;analysis_goal='analysis';complexity=$Size;risk=$Risk;impact_flags=@();criteria=@([pscustomobject]@{id='greeting';observation='The source contains the requested description.';kind='file_assertion';path='hello.txt';contains='Example greeting'});provenance=[pscustomobject]@{source='user';reference='fixture';text='Implement the greeting.'};models=[pscustomobject]@{worker='gpt-6-astra';worker_effort='medium';reviewer='gpt-6-astra';reviewer_effort='high'}}
}
$script:trace=New-Object 'System.Collections.Generic.List[string]'
$executor={param($run)
    $script:trace.Add($run.attempt.stage)
    $payload='{}'
    switch($run.attempt.stage){
        'inspect'{$payload=Get-BFCanonicalJson ([ordered]@{complexity=$run.state.classification.complexity;risk=$run.state.classification.risk;impact_flags=@();rationale='The source fixture only changes a greeting.'})}
        'implement'{Write-Fixture (Join-Path $run.state.worker_path 'hello.txt') "Example greeting`n";$payload='{"changed_files":["hello.txt"]}'}
        'verify'{return Invoke-BFVerification $run.state (Join-Path $run.directory 'raw') '' $null}
        'code_review'{$payload='{"verdict":"PASS","findings":[]}'}
        default{throw "Unexpected fixture stage $($run.attempt.stage)"}
    }
    return [ordered]@{schema_version=1;status='completed';summary='Fixture stage complete.';payload_json=$payload}
}
function New-SpecText([string]$Detail='The greeting behavior is specified.',[string]$Complexity='S',[string]$Risk='medium') {
    return @"
# Greeting change

## Classification
- Complexity: $Complexity
- Risk: $Risk

## Goal
$Detail

## Required behavior
The implementation updates the greeting while retaining the existing file contract.

## 1C context
This filesystem fixture represents a bounded source change without runtime writes.

## Non-goals
No deployment, database mutation, or unrelated source cleanup is included.

## Acceptance criteria
- The tracked greeting file contains the exact requested Example greeting text.

## Required verification
- [x] File assertion checks the exact current greeting source bytes.

## Uncertainties / assumptions
The fixture has no unresolved business mapping or external dependency.
"@
}
$specExecutor={param($run)
    if($run.attempt.stage -eq 'inspect'){
        $payload=Get-BFCanonicalJson ([ordered]@{complexity=$run.state.classification.complexity;risk=$run.state.classification.risk;impact_flags=@();rationale='The fixture requires a specification and has no hidden impact.'})
    }elseif($run.attempt.stage -eq 'spec'){
        $payload=Get-BFCanonicalJson ([ordered]@{spec=New-SpecText -Complexity $run.state.classification.complexity -Risk $run.state.classification.risk;design=$null})
    }else{throw "Unexpected specification fixture stage $($run.attempt.stage)"}
    return [ordered]@{schema_version=1;status='completed';summary='Specification fixture stage complete.';payload_json=$payload}
}

# A generated specification that fails lint remains blocked with actionable diagnostics.
$script:invalidSpecCalls=0
$invalidSpecExecutor={param($run)
    $script:invalidSpecCalls++
    if($run.attempt.stage -eq 'inspect'){
        $payload=Get-BFCanonicalJson ([ordered]@{complexity=$run.state.classification.complexity;risk=$run.state.classification.risk;impact_flags=@();rationale='The fixture deliberately generates an incomplete specification.'})
    }elseif($run.attempt.stage -eq 'spec'){
        $invalidSpec=@('# Invalid generated specification','','## Classification','- Complexity: S','- Risk: medium') -join [Environment]::NewLine
        $payload=Get-BFCanonicalJson ([ordered]@{spec=$invalidSpec;design=$null})
    }else{throw "Unexpected invalid specification fixture stage $($run.attempt.stage)"}
    return [ordered]@{schema_version=1;status='completed';summary='Invalid specification fixture stage complete.';payload_json=$payload}
}
$invalidSpecRequest=New-Request 'implement' 'S' 'medium'
$invalidSpecTask=Start-BFTask $project $invalidSpecRequest
$invalidSpecTask=Invoke-BFRun $project $invalidSpecTask.task_id '' $invalidSpecExecutor
Assert-Task ($invalidSpecTask.status -eq 'blocked' -and $script:invalidSpecCalls -eq 2) 'Invalid generated specification did not block at lint'
$invalidSpecNext=Get-BFNext $invalidSpecTask
$invalidSpecReason=@($invalidSpecNext.blockers) -join '; '
Assert-Task ($invalidSpecNext.action -eq 'blocked' -and $invalidSpecNext.stage -eq 'spec' -and $invalidSpecReason -match 'generated specification failed mandatory lint:' -and $invalidSpecReason -match 'Missing required section: Goal') 'Blocked next action lost the concrete specification lint reason'
$invalidSpecEnvelope=New-BFEnvelope $invalidSpecTask $invalidSpecNext.action $invalidSpecNext.blockers $invalidSpecNext.stage
Assert-Task ($invalidSpecEnvelope.status -eq 'blocked' -and $invalidSpecEnvelope.next_action -eq 'blocked' -and $invalidSpecEnvelope.next_stage -eq 'spec' -and (@($invalidSpecEnvelope.blockers) -join '; ') -match 'Missing required section: Goal') 'Blocked envelope lost the concrete specification lint reason'
$invalidSpecRevision=$invalidSpecTask.revision;$invalidSpecCallsBeforeRepeat=$script:invalidSpecCalls
$invalidSpecTask=Invoke-BFRun $project $invalidSpecTask.task_id '' $invalidSpecExecutor
Assert-Task ($invalidSpecTask.status -eq 'blocked' -and $invalidSpecTask.revision -eq $invalidSpecRevision -and $script:invalidSpecCalls -eq $invalidSpecCallsBeforeRepeat) 'Run repeated the executor for a blocked specification without an explicit update'

# Complete short route and idempotent registration/acceptance.
$request=New-Request;$task=Start-BFTask $project $request
Assert-Task ($task.task_id -eq $request.request_id) 'Start identity'
Assert-Task ((Start-BFTask $project $request).revision -eq 1) 'Start deduplication'
$request.prompt+=' changed';Assert-TaskThrows {Start-BFTask $project $request} 'BF_CONFLICT';$request.prompt='Add Example greeting to hello.txt.'
$task=Invoke-BFRun $project $task.task_id '' $executor
Assert-Task ($task.status -eq 'completed') 'S task completes automatically'
Assert-Task (($script:trace -join ',') -eq 'inspect,implement,verify') 'S route is short and automatic'
Assert-Task ((Accept-BFTask $project $task.task_id).revision -eq $task.revision) 'Acceptance idempotent'
$acceptancePath=$task.acceptances[-1].path
Assert-Task ((Read-BFJson $acceptancePath).source_manifest.files.Count -ge 2) 'Acceptance carries actual manifest'
$last=$task.attempts[-1];Assert-Task ((Record-BFAttempt $project $task.task_id $last).revision -eq $task.revision) 'Record deduplication'

# Freshness catches dirty tracked/untracked files and deletions, never HEAD alone.
Write-Fixture (Join-Path $task.worker_path 'hello.txt') "Example greeting changed after tests`n"
$editedState=Read-BFTask $project $task.task_id;$editedNext=Get-BFNext $editedState
Assert-Task ($editedNext.stage -eq 'implement') 'Post-test edit invalidates acceptance'
$editedEnvelope=New-BFEnvelope $editedState $editedNext.action $editedNext.blockers $editedNext.stage
Assert-Task ($editedEnvelope.status -ne 'completed' -and $null -eq $editedEnvelope.acceptance -and $editedEnvelope.next_action -eq 'dispatch' -and $editedEnvelope.next_stage -eq 'implement') 'External edit left a completed/PASS acceptance envelope or lost its implementation stage'
Assert-TaskThrows {Accept-BFTask $project $task.task_id} 'BF_BLOCKED'
Write-Fixture (Join-Path $task.worker_path 'new.txt') 'untracked'
$manifest=Get-BFSourceManifest $task
Assert-Task (@($manifest.files|Where-Object{$_.path -eq 'new.txt'}).Count -eq 1) 'Untracked sources included'
Remove-Item -LiteralPath (Join-Path $task.worker_path 'hello.txt')
$manifest=Get-BFSourceManifest $task
Assert-Task (@($manifest.files|Where-Object{$_.path -eq 'hello.txt' -and $_.deleted}).Count -eq 1) 'Deletion included'
Write-Fixture (Join-Path $task.worker_path 'hello.txt') "Example greeting`n"

# Analysis accepts only analysis. Authorization reopens with preserved intent evidence.
$analysis=New-Request 'analysis_only';$analysisTask=Start-BFTask $project $analysis
$script:trace.Clear();$analysisTask=Invoke-BFRun $project $analysisTask.task_id '' $executor
Assert-Task ($analysisTask.status -eq 'completed' -and ($script:trace -join ',') -eq 'inspect') 'Analysis never implements/tests'
$event=[pscustomobject]@{schema_version=1;input_event_id=[guid]::NewGuid().ToString();expected_revision=$analysisTask.revision;kind='authorization';mode='implement';provenance=$analysis.provenance}
$updated=Update-BFTask $project $analysisTask.task_id $event
Assert-Task ($updated.intent_hash -eq $analysisTask.intent_hash -and $updated.authorization_revision -eq 2) 'Authorization does not invalidate semantic intent'
Assert-Task ((Get-BFNext $updated).stage -eq 'implement') 'Authorization preserves inspect'
$again=Update-BFTask $project $analysisTask.task_id $event;Assert-Task ($again.revision -eq $updated.revision) 'Update dedup before revision check'
$event.mode='analysis_only';Assert-TaskThrows {Update-BFTask $project $analysisTask.task_id $event} 'BF_CONFLICT'

# Created specifications remain required inputs for implementation and analysis acceptance.
$mediumRequest=New-Request 'implement' 'S' 'medium';$mediumTask=Start-BFTask $project $mediumRequest
$mediumTask=Invoke-BFStage (New-BFAttempt $project $mediumTask.task_id '') '' $specExecutor $null
$mediumTask=Invoke-BFStage (New-BFAttempt $project $mediumTask.task_id '') '' $specExecutor $null
$mediumSpec=Join-Path (Get-BFChangePath $mediumTask) 'spec.md';$mediumText=[IO.File]::ReadAllText($mediumSpec)
Remove-Item -LiteralPath $mediumSpec
Assert-Task ((Get-BFNext (Read-BFTask $project $mediumTask.task_id)).stage -eq 'spec') 'S/medium continued after deleting its accepted spec'
Write-Fixture $mediumSpec $mediumText
Assert-Task ((Get-BFNext (Read-BFTask $project $mediumTask.task_id)).stage -eq 'implement') 'Restored S/medium spec did not restore the next stage'
Write-Fixture $mediumSpec ($mediumText+[Environment]::NewLine+'External substitution.')
Assert-Task ((Get-BFNext (Read-BFTask $project $mediumTask.task_id)).stage -eq 'spec') 'S/medium continued after substituting its accepted spec'

$analysisSpecRequest=New-Request 'analysis_only' 'S' 'low';$analysisSpecRequest.analysis_goal='specification'
$analysisSpecTask=Start-BFTask $project $analysisSpecRequest
$analysisSpecTask=Invoke-BFStage (New-BFAttempt $project $analysisSpecTask.task_id '') '' $specExecutor $null
$analysisSpecTask=Invoke-BFStage (New-BFAttempt $project $analysisSpecTask.task_id '') '' $specExecutor $null
$analysisSpecPath=Join-Path (Get-BFChangePath $analysisSpecTask) 'spec.md';$analysisSpecText=[IO.File]::ReadAllText($analysisSpecPath)
Remove-Item -LiteralPath $analysisSpecPath
Assert-Task ((Get-BFNext (Read-BFTask $project $analysisSpecTask.task_id)).stage -eq 'spec') 'Analysis specification accepted after deleting its spec'
Write-Fixture $analysisSpecPath ($analysisSpecText+[Environment]::NewLine+'External substitution.')
Assert-Task ((Get-BFNext (Read-BFTask $project $analysisSpecTask.task_id)).stage -eq 'spec') 'Analysis specification accepted after substituting its spec'

# Registered review evidence binds a reviewed draft to a linted final spec.
$reviewRequest=New-Request 'implement' 'S' 'medium'
$reviewRequest|Add-Member -NotePropertyName require_spec_review -NotePropertyValue $true
$reviewTask=Start-BFTask $project $reviewRequest
$reviewTask=Invoke-BFStage (New-BFAttempt $project $reviewTask.task_id '') '' $specExecutor $null
$reviewTask=Invoke-BFStage (New-BFAttempt $project $reviewTask.task_id '') '' $specExecutor $null
$reviewRun=New-BFAttempt $project $reviewTask.task_id ''
$trustedReview={param($run)
    $raw=Join-Path $run.directory 'raw\trusted-reconciliation';[void][IO.Directory]::CreateDirectory($raw)
    Save-BFSpec $run.state ([pscustomobject]@{spec=New-SpecText 'The reviewed final greeting behavior is specified.';design=$null}) $raw
    $change=Get-BFChangePath $run.state
    Write-BFJson -Path (Join-Path $change 'review.json') -Value ([ordered]@{schema_version=1;verdict='reviewed'})
    Write-BFJson -Path (Join-Path $change 'review-reconciliation.json') -Value ([ordered]@{schema_version=1;result='reconciled'})
    Write-BFJson -Path (Join-Path $change 'final-validation.json') -Value ([ordered]@{schema_version=1;passed=$true})
    return [ordered]@{schema_version=1;status='completed';summary='Trusted reconciliation fixture complete.';payload_json='{}';bound_dependencies=Get-BFDependencies $run.state 'spec_review' $null}
}
$reviewTask=Invoke-BFStage $reviewRun '' $trustedReview $null
$specEvidence=@($reviewTask.evidence|Where-Object{$_.stage -eq 'spec'})[-1]
Assert-Task (Test-BFEvidenceFresh $reviewTask $specEvidence (Get-BFSourceManifest $reviewTask)) 'Registered reconciliation did not preserve reviewed draft freshness'
Assert-Task ((Get-BFNext $reviewTask).stage -eq 'implement') 'Registered reconciliation caused an infinite re-spec/review loop'

# Required question identity, stale-event rejection and no self-authorization.
$questionRequest=New-Request;$questionTask=Start-BFTask $project $questionRequest
$ask={param($run) [ordered]@{schema_version=1;status='needs_input';summary='Which greeting must be used?';payload_json='{}'}}
$questionTask=Invoke-BFRun $project $questionTask.task_id '' $ask
Assert-Task ($questionTask.status -eq 'needs_input') 'Question pauses loop'
$answer=[pscustomobject]@{schema_version=1;input_event_id=[guid]::NewGuid().ToString();expected_revision=$questionTask.revision;kind='clarification';text='Use Example greeting.';question_id=[guid]::NewGuid().ToString();provenance=$questionRequest.provenance}
Assert-TaskThrows {Update-BFTask $project $questionTask.task_id $answer} 'BF_CONFLICT'
$answer.question_id=$questionTask.question.question_id;$answered=Update-BFTask $project $questionTask.task_id $answer
Assert-Task ($null -eq $answered.question -and $answered.intent_revision -eq 2) 'Answer closes exact question and updates intent'
$bad=New-Request;$bad.provenance.source='worker';Assert-TaskThrows {Start-BFTask $project $bad} 'BF_INVALID'

# Routing table and hazard coverage are controlled by code.
foreach($case in @(@('S','low','inspect,implement,verify,acceptance'),@('S','medium','inspect,spec,implement,verify,acceptance'),@('M','low','inspect,spec,spec_review,implement,verify,acceptance'),@('L','low','inspect,spec,spec_review,implement,code_review,verify,acceptance'),@('S','high','inspect,spec,spec_review,implement,code_review,verify,acceptance'))){
    $routeState=[pscustomobject]@{request=New-Request 'implement' $case[0] $case[1];classification=[pscustomobject]@{complexity=$case[0];risk=$case[1]}}
    Assert-Task ((@(Get-BFRoute $routeState)-join ',') -eq $case[2]) "Route $($case[0])/$($case[1])"
}
$hazard=Read-BFTask $project $questionTask.task_id
Set-BFClassification $hazard ([pscustomobject]@{complexity='S';risk='low';impact_flags=@('permissions');rationale='Permission change is security-sensitive.'})
Assert-Task ($hazard.classification.risk -eq 'high') 'Hazard strengthens risk'
Assert-TaskThrows {Assert-BFVerificationCoverage $hazard} 'requires integration'
Assert-TaskThrows {Assert-BFCodeReview ([pscustomobject]@{verdict='BLOCK';findings=@()})} 'BF_INVALID'

# No duplicate worker; terminal receipt recovers without repeating the action.
$recoveryRequest=New-Request;$recovery=Start-BFTask $project $recoveryRequest
$run=New-BFAttempt $project $recovery.task_id ''
Assert-TaskThrows {New-BFAttempt $project $recovery.task_id ''} 'BF_CONFLICT'
Assert-TaskThrows {Resume-BFAttempt $project $recovery.task_id} 'controller is still running'
$cancelled=Cancel-BFTask $project $recovery.task_id
Assert-Task ($cancelled.status -eq 'cancelled' -and $cancelled.active_attempt -eq $run.attempt.attempt_id) 'Cancel preserves unresolved attempt'
Assert-Task ((Get-BFNext $cancelled).action -eq 'cancelled') 'Cancel prevents dispatch'

# JUnit must prove exact executed selection, not zero exit or a placeholder.
$junit=Join-Path $testRoot 'original.xml'
Write-Fixture $junit '<testsuite tests="1" failures="0"><testcase name="expected"/></testsuite>'
Assert-Task ((Test-BFJUnit $junit @('expected')).tests.Count -eq 1) 'JUnit exact selection passes'
Assert-TaskThrows {Test-BFJUnit $junit @('other')} 'BF_BLOCKED'
Write-Fixture $junit '<testsuite><testcase name="expected"><skipped/></testcase></testsuite>'
Assert-TaskThrows {Test-BFJUnit $junit @('expected')} 'BF_BLOCKED'
Write-Fixture $junit '<testsuite><testcase name="expected"><failure/></testcase></testsuite>'
Assert-TaskThrows {Test-BFJUnit $junit @('expected')} 'BF_FAIL'
Write-Fixture $junit '<!DOCTYPE a [<!ENTITY b SYSTEM "file:///C:/Windows/win.ini">]><testsuite><testcase name="expected">&b;</testcase></testsuite>'
Assert-TaskThrows {Test-BFJUnit $junit @('expected')} 'BF_BLOCKED'

# Read-only state commands can recover from a lost derived current pointer.
$taskDirectory=Get-BFTaskDirectory $project $analysisTask.task_id
Remove-Item -LiteralPath (Join-Path $taskDirectory 'current.json')
Assert-Task ((Read-BFTask $project $analysisTask.task_id).revision -eq $updated.revision) 'Missing current pointer recovered from journal'
Write-Host "Task lifecycle: $script:checks checks PASS. Isolated fixture retained at $testRoot"
