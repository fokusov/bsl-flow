#Requires -Version 7.0
[CmdletBinding()]
param([string]$PackageRoot)
Set-StrictMode -Version Latest
$ErrorActionPreference='Stop'
if(-not $PackageRoot){$PackageRoot=Split-Path $PSScriptRoot -Parent}
# Offline regression for current-agent mode (plan Ф4.7): Next -Format Prompt /
# Submit through the public CLI. No model, sandbox or 1C process is started.
$cli=Join-Path $PackageRoot 'global/skills/1c-task/scripts/Invoke-BSLFlowTask.ps1'
$script:checks=0
function Assert-Mode([bool]$Condition,[string]$Message){if(-not $Condition){throw "ASSERTION FAILED: $Message"};$script:checks++}
function Write-Text([string]$Path,[string]$Text){[void][IO.Directory]::CreateDirectory((Split-Path $Path -Parent));[IO.File]::WriteAllText($Path,$Text,[Text.UTF8Encoding]::new($false))}
function Invoke-Cli([string[]]$Arguments){
    $output=@(& pwsh -NoProfile -File $cli @Arguments 2>&1)
    $code=$LASTEXITCODE
    $line=@($output|ForEach-Object{[string]$_}|Where-Object{$_.StartsWith('{')})[-1]
    return [pscustomobject]@{code=$code;envelope=(ConvertFrom-Json $line -Depth 64);text=($output -join "`n")}
}
function Write-Result([string]$Name,[string]$Payload,[string]$Status='completed',[hashtable]$Extra=@{}){
    $value=[ordered]@{schema_version=1;status=$Status;summary="Current agent $Name result.";payload_json=$Payload}
    foreach($key in $Extra.Keys){$value[$key]=$Extra[$key]}
    $path=Join-Path $testRoot ("results/$Name-"+[guid]::NewGuid().ToString('N')+'.json')
    Write-Text $path (ConvertTo-Json $value -Depth 10 -Compress)
    return $path
}
function New-Task{
    $request=[ordered]@{schema_version=1;request_id=[guid]::NewGuid().ToString();prompt='Add Example greeting to hello.txt.';mode='implement';analysis_goal='analysis';complexity='S';risk='low';impact_flags=@();criteria=@([ordered]@{id='greeting';observation='The source contains the requested greeting.';kind='file_assertion';path='hello.txt';contains='Example greeting'});provenance=[ordered]@{source='user';reference='fixture';text='Implement the greeting.'};models=[ordered]@{worker='current-session';worker_effort='medium';reviewer='current-session';reviewer_effort='high'}}
    $path=Join-Path $testRoot ('requests/'+$request.request_id+'.json');Write-Text $path (ConvertTo-Json $request -Depth 10)
    $start=Invoke-Cli @('-Action','Start','-ProjectPath',$project,'-InputFile',$path)
    Assert-Mode ($start.code -eq 0 -and $start.envelope.next_stage -eq 'inspect') "Start failed: $($start.text)"
    return $request.request_id
}
$inspectPayload='{"complexity":"S","risk":"low","impact_flags":[],"rationale":"Only a greeting line changes."}'
$testRoot=Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-current-agent-'+[guid]::NewGuid().ToString('N'))
try {
    $project=Join-Path $testRoot 'project';[void][IO.Directory]::CreateDirectory($project)
    & git -C $project init -q;Write-Text (Join-Path $project 'hello.txt') "Initial greeting`n";Write-Text (Join-Path $project '.gitignore') ".bsl-flow/`nopenspec/changes/`n"
    & git -C $project add .;& git -C $project -c user.name=Test -c user.email=test@example.invalid commit -q -m Fixture
    $task=New-Task
    $base=@('-ProjectPath',$project,'-TaskId',$task)

    # inspect: dispatch, then rejected submissions leave the dispatch usable.
    $next=Invoke-Cli (@('-Action','Next','-Format','Prompt')+$base)
    $dispatch=$next.envelope.dispatch
    Assert-Mode ($next.code -eq 0 -and $next.envelope.next_action -eq 'submit' -and $dispatch.stage -eq 'inspect') "Next did not dispatch inspect: $($next.text)"
    Assert-Mode ($dispatch.isolation -eq 'current_agent' -and $dispatch.prompt -match 'BSL Flow worker for stage inspect' -and (Test-Path -LiteralPath $dispatch.result_schema)) 'Dispatch lacks prompt, isolation or schema path.'
    Assert-Mode ($dispatch.dispatch_id -cmatch '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$') 'Dispatch id is not a UUID.'
    $again=Invoke-Cli (@('-Action','Next','-Format','Prompt')+$base)
    Assert-Mode ($again.code -eq 0 -and $null -eq $again.envelope.dispatch -and $again.envelope.pending_dispatch.dispatch_id -eq $dispatch.dispatch_id) 'A second Next issued a new dispatch or lost the pending one.'
    $good=Write-Result 'inspect' $inspectPayload
    $wrongStage=Invoke-Cli (@('-Action','Submit','-Stage','implement','-DispatchId',$dispatch.dispatch_id,'-ResultFile',$good)+$base)
    Assert-Mode ($wrongStage.code -eq 2 -and $wrongStage.text -match 'differs from the dispatched stage') "Wrong stage was not rejected: $($wrongStage.text)"
    $invalid=Write-Result 'inspect-invalid' $inspectPayload 'completed' @{verdict='PASS'}
    $schema=Invoke-Cli (@('-Action','Submit','-Stage','inspect','-DispatchId',$dispatch.dispatch_id,'-ResultFile',$invalid)+$base)
    Assert-Mode ($schema.code -eq 2 -and $schema.text -match 'unknown field submitted_result.verdict') "Schema-invalid result was not rejected: $($schema.text)"
    $badPayload=Write-Result 'inspect-payload' 'not json'
    $schema=Invoke-Cli (@('-Action','Submit','-Stage','inspect','-DispatchId',$dispatch.dispatch_id,'-ResultFile',$badPayload)+$base)
    Assert-Mode ($schema.code -eq 2 -and $schema.text -match 'payload_json') 'Non-JSON payload_json was not rejected.'
    $mismatch=Invoke-Cli (@('-Action','Submit','-Stage','inspect','-DispatchId',[guid]::NewGuid().ToString(),'-ResultFile',$good)+$base)
    Assert-Mode ($mismatch.code -eq 3 -and $mismatch.text -match 'does not match the active dispatch') "Mismatched dispatch id was not rejected: $($mismatch.text)"
    $submit=Invoke-Cli (@('-Action','Submit','-Stage','inspect','-DispatchId',$dispatch.dispatch_id,'-ResultFile',$good)+$base)
    Assert-Mode ($submit.code -eq 0 -and $submit.envelope.submitted.outcome -eq 'PASS' -and $submit.envelope.submitted.isolation -eq 'current_agent' -and $submit.envelope.next_stage -eq 'implement') "Inspect submission failed: $($submit.text)"
    $reused=Invoke-Cli (@('-Action','Submit','-Stage','inspect','-DispatchId',$dispatch.dispatch_id,'-ResultFile',$good)+$base)
    Assert-Mode ($reused.code -eq 3 -and $reused.text -match 'already consumed') "Reused dispatch id was not rejected: $($reused.text)"
    $taskDir=Join-Path $project ".bsl-flow/tasks/$task"

    # implement: the current session edits the worker worktree itself.
    $next=Invoke-Cli (@('-Action','Next','-Format','Prompt')+$base);$dispatch=$next.envelope.dispatch
    Assert-Mode ($dispatch.stage -eq 'implement' -and $dispatch.prompt -match 'Implement the authorized request') 'Implement was not dispatched.'
    $inWorktree=Join-Path $next.envelope.worker_path 'result.json';Write-Text $inWorktree '{}'
    $inside=Invoke-Cli (@('-Action','Submit','-Stage','implement','-DispatchId',$dispatch.dispatch_id,'-ResultFile',$inWorktree)+$base)
    Assert-Mode ($inside.code -eq 2 -and $inside.text -match 'outside the worker worktree') 'A result file inside the worktree was accepted.'
    Remove-Item -LiteralPath $inWorktree -Force
    Write-Text (Join-Path $next.envelope.worker_path 'hello.txt') "Example greeting`n"
    $submit=Invoke-Cli (@('-Action','Submit','-Stage','implement','-DispatchId',$dispatch.dispatch_id,'-ResultFile',(Write-Result 'implement' '{"changed_files":["hello.txt"]}'))+$base)
    Assert-Mode ($submit.code -eq 0 -and $submit.envelope.submitted.outcome -eq 'PASS' -and $submit.envelope.next_stage -eq 'verify') "Implement submission failed: $($submit.text)"

    # verify is controller-owned; a file-assertion verify needs no worker host.
    $next=Invoke-Cli (@('-Action','Next','-Format','Prompt')+$base)
    Assert-Mode ($next.code -eq 0 -and $next.envelope.controller_owned -eq $true -and $null -eq $next.envelope.dispatch) 'Verify was offered to the current agent.'
    $run=Invoke-Cli (@('-Action','Run')+$base)
    Assert-Mode ($run.code -eq 0 -and $run.envelope.status -eq 'completed' -and $run.envelope.acceptance.isolation -eq 'current_agent') "Run did not accept the current-agent task: $($run.text)"
    $receipt=Get-Content -Raw -LiteralPath $run.envelope.acceptance.path|ConvertFrom-Json
    Assert-Mode ($receipt.isolation -eq 'current_agent' -and (@($receipt.isolation_limited_stages) -join ',') -eq 'inspect,implement' -and $receipt.verdict -eq 'PASS') 'Acceptance receipt does not record current_agent isolation.'
    $hostResult=Get-Content -Raw -LiteralPath (Join-Path $taskDir ("attempts/$($dispatch.attempt_id)/raw/worker/host-result.json"))|ConvertFrom-Json
    Assert-Mode ($hostResult.isolation -eq 'current_agent' -and $hostResult.adapter -eq 'current-agent') 'Host metadata lacks current-agent isolation.'

    # Integrity: a change to controller state between Next and Submit blocks.
    $task2=New-Task;$base2=@('-ProjectPath',$project,'-TaskId',$task2)
    $next=Invoke-Cli (@('-Action','Next','-Format','Prompt')+$base2)
    $crossTask=Invoke-Cli (@('-Action','Submit','-Stage','inspect','-DispatchId',$dispatch.dispatch_id,'-ResultFile',$good)+$base2)
    Assert-Mode ($crossTask.code -eq 3 -and $crossTask.text -match 'does not match') 'A dispatch from another task was accepted.'
    Write-Text (Join-Path $project ".bsl-flow/tasks/$task2/inputs/forged.json") '{}'
    $tampered=Invoke-Cli (@('-Action','Submit','-Stage','inspect','-DispatchId',$next.envelope.dispatch.dispatch_id,'-ResultFile',(Write-Result 'inspect' $inspectPayload))+$base2)
    Assert-Mode ($tampered.code -eq 11 -and $tampered.text -match 'controller state modified by worker') "Controller tampering was not detected: $($tampered.text)"

    # A source write during a read-only current-agent stage is an unknown effect.
    $task3=New-Task;$base3=@('-ProjectPath',$project,'-TaskId',$task3)
    $next=Invoke-Cli (@('-Action','Next','-Format','Prompt')+$base3)
    Write-Text (Join-Path $next.envelope.worker_path 'hello.txt') "Changed during inspect`n"
    $readOnly=Invoke-Cli (@('-Action','Submit','-Stage','inspect','-DispatchId',$next.envelope.dispatch.dispatch_id,'-ResultFile',(Write-Result 'inspect' $inspectPayload))+$base3)
    Assert-Mode ($readOnly.code -eq 11 -and $readOnly.envelope.submitted.outcome -eq 'BLOCKED' -and $null -ne $readOnly.envelope.unresolved_effect) "Read-only source change was not an unresolved effect: $($readOnly.text)"
    $task4=New-Task;$base4=@('-ProjectPath',$project,'-TaskId',$task4)
    $next=Invoke-Cli (@('-Action','Next','-Format','Prompt')+$base4)
    $cancel=Invoke-Cli (@('-Action','Cancel')+$base4)
    Assert-Mode ($cancel.envelope.status -eq 'cancelled') 'Cancellation failed.'
    $cancelledSubmit=Invoke-Cli (@('-Action','Submit','-Stage','inspect','-DispatchId',$next.envelope.dispatch.dispatch_id,'-ResultFile',$good)+$base4)
    Assert-Mode ($cancelledSubmit.code -ne 0) 'A revoked dispatch authorization was accepted.'
    # Required code review must not be fulfilled by the authoring session.
    # Isolate the dispatch decision for L/high/explicit-review routes: all
    # reach code_review, but none may create a current-agent attempt there.
    $core=Join-Path $PackageRoot 'global/skills/1c-task/scripts'
    foreach($name in @('Task.Storage.ps1','Task.Contracts.ps1','Task.Gates.ps1','Task.Engine.ps1')){. (Join-Path $core $name)}
    . (Join-Path $PackageRoot 'global/skills/1c-task/adapters/CurrentAgent.ps1')
    $script:reviewDispatches=0
    function Read-BFTask { param($ProjectPath,$TaskId) return $script:reviewState }
    function Get-BFNext { param($State) return @{action='dispatch';stage='code_review';blockers=@()} }
    function New-BFEnvelope { param($State,$Action,$Blockers,$Stage) return @{next_action=$Action;next_stage=$Stage} }
    function New-BFAttempt { $script:reviewDispatches++;throw 'Current-agent self-review was dispatched.' }
    foreach($route in @(@{complexity='L';risk='low'},@{complexity='S';risk='high'},@{complexity='S';risk='low';require_code_review=$true})){
        $script:reviewState=@{request=$route}
        $decision=New-BFCurrentAgentDispatch $project $task4
        Assert-Mode ($decision.controller_owned -eq $true -and $null -eq $decision.dispatch) 'Required independent review was offered to the current agent.'
    }
    $reviewFailure='';try{Assert-BFAdapterStagePolicy (Get-BFWorkerAdapter 'current-agent') 'code_review' -Submit}catch{$reviewFailure=$_.Exception.Message}
    Assert-Mode ($reviewFailure -like 'BF_BLOCKED:*' -and $script:reviewDispatches -eq 0) 'Submit or Next allowed current-agent self-review.'
    Write-Output "CURRENT_AGENT_MODE_OK checks=$script:checks; model/sandbox processes=0"
} finally {
    if(-not ([IO.Path]::GetFullPath($testRoot)).StartsWith([IO.Path]::GetFullPath([IO.Path]::GetTempPath()),[StringComparison]::OrdinalIgnoreCase)){throw 'Unsafe fixture cleanup path.'}
    if(Test-Path -LiteralPath $testRoot){& git -C (Join-Path $testRoot 'project') worktree prune 2>$null;Remove-Item -LiteralPath $testRoot -Recurse -Force -ErrorAction SilentlyContinue}
}
