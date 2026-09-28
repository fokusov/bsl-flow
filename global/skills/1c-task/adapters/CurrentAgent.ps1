#Requires -Version 7.0
Set-StrictMode -Version Latest

# Current-agent (assisted-managed) mode, plan Ф4.7 / ADR-11 exception.
# `Next -Format Prompt` registers an attempt dispatched to the current session
# and returns the exact worker prompt; `Submit` feeds the result through the
# same schema, stage gates, source manifest and stale checks as a headless
# worker. There is no worker isolation: receipts record isolation current_agent.

function Get-BFCurrentAgentDispatchRecord {
    param([string]$AttemptDirectory)
    $path=Join-Path $AttemptDirectory 'dispatch.json'
    if(-not(Test-Path -LiteralPath $path -PathType Leaf)){return $null}
    $dispatch=Read-BFJson $path
    Assert-BFFields $dispatch @('schema_version','dispatch_id','adapter','isolation','stage','attempt_id','issued_at_utc') @() 'dispatch'
    if($dispatch.adapter -cne 'current-agent' -or $dispatch.isolation -cne 'current_agent'){throw 'BF_BLOCKED: invalid current-agent dispatch record.'}
    return $dispatch
}

function Find-BFCurrentAgentDispatch {
    param([string]$TaskDirectory,[string]$DispatchId)
    $root=Join-Path $TaskDirectory 'attempts'
    if(-not(Test-Path -LiteralPath $root -PathType Container)){return $null}
    foreach($attempt in @(Get-ChildItem -LiteralPath $root -Directory)){
        $dispatch=Get-BFCurrentAgentDispatchRecord $attempt.FullName
        if($null -ne $dispatch -and $dispatch.dispatch_id -ceq $DispatchId){return $dispatch}
    }
    return $null
}

function Get-BFCurrentAgentIntegrity {
    param([string]$TaskDirectory,[string]$AttemptDirectory)
    return Get-BFTreeDigest @($TaskDirectory) @((Join-Path $AttemptDirectory 'raw'))
}

function New-BFCurrentAgentDispatch {
    param([string]$ProjectPath,[string]$TaskId)
    $adapter=Get-BFWorkerAdapter 'current-agent'
    $directory=Get-BFTaskDirectory $ProjectPath $TaskId
    $state=Read-BFTask $ProjectPath $TaskId
    if($null -ne (Get-BFValue $state.request 'execution_profile')){throw 'BF_BLOCKED: a task with a managed execution profile is dispatched by Run; current-agent mode serves tasks without one.'}
    $next=Get-BFNext $state
    $envelope=New-BFEnvelope $state $next.action @($next.blockers) $next.stage
    $envelope.dispatch=$null
    if($next.action -eq 'recover' -and $state.active_attempt){
        # A lost session may re-read its pending dispatch; no new id is issued.
        $attemptDir=Join-Path $directory ('attempts/'+$state.active_attempt)
        $pending=Get-BFCurrentAgentDispatchRecord $attemptDir
        if($null -ne $pending -and -not(Test-Path -LiteralPath (Join-Path $attemptDir 'submission.json'))){$envelope.pending_dispatch=[ordered]@{dispatch_id=$pending.dispatch_id;attempt_id=$pending.attempt_id;stage=$pending.stage;prompt_path=Join-Path $attemptDir 'raw/worker/prompt.txt'}}
        return $envelope
    }
    if($next.action -ne 'dispatch'){return $envelope}
    if($next.stage -cnotin @($adapter.stages)){
        $envelope.controller_owned=$true
        $envelope.blockers=@("Stage $($next.stage) is controller-owned; run it with -Action Run (file-assertion verification needs no worker host).")
        return $envelope
    }
    Assert-BFAdapterStagePolicy $adapter $next.stage -Submit
    $dispatchId=[guid]::NewGuid().ToString()
    $run=New-BFAttempt $ProjectPath $TaskId '' 'current_agent' ([ordered]@{dispatch_id=$dispatchId;adapter='current-agent';stages=@($adapter.stages)})
    try{
        $prompt=Get-BFWorkerStagePrompt $run.state $run.attempt ''
        $worker=Join-Path $run.directory 'raw/worker';[void][IO.Directory]::CreateDirectory($worker)
        $promptPath=Join-Path $worker 'prompt.txt'
        [IO.File]::WriteAllText($promptPath,$prompt,[Text.UTF8Encoding]::new($false))
        Write-BFJson (Join-Path $worker 'dispatch-integrity.json') ([ordered]@{controller_sha256=Get-BFCurrentAgentIntegrity $directory $run.directory})
    }catch{
        # Close the attempt as a terminal BLOCKED receipt instead of leaving an
        # orphan that would require recovery.
        $reason=$_.Exception.Message
        [void](Invoke-BFStage -Run $run -CodexPath '' -StageExecutor ({throw $reason}.GetNewClosure()))
        throw
    }
    $schema=Join-Path (Split-Path $PSScriptRoot -Parent) 'schemas/worker-result.schema.json'
    $envelope=New-BFEnvelope $run.state 'submit' @() $run.attempt.stage
    $envelope.dispatch=[ordered]@{dispatch_id=$dispatchId;attempt_id=$run.attempt.attempt_id;stage=$run.attempt.stage;adapter='current-agent';isolation='current_agent';prompt=$prompt;prompt_sha256=Get-BFFileHash $promptPath;result_schema=$schema;submit='-Action Submit -TaskId <task> -Stage <stage> -DispatchId <dispatch_id> -ResultFile <file outside the worktree>'}
    return $envelope
}

function Submit-BFCurrentAgentResult {
    param([string]$ProjectPath,[string]$TaskId,[string]$Stage,[string]$DispatchId,[string]$ResultFile)
    Assert-BFUuid $DispatchId
    if([string]::IsNullOrWhiteSpace($ResultFile)){throw 'BF_INVALID: Submit requires -ResultFile.'}
    $adapter=Get-BFWorkerAdapter 'current-agent'
    $directory=Get-BFTaskDirectory $ProjectPath $TaskId
    $state=Read-BFTask $ProjectPath $TaskId
    $dispatch=$null;$attemptDir=$null
    if($state.active_attempt){$attemptDir=Join-Path $directory ('attempts/'+$state.active_attempt);$dispatch=Get-BFCurrentAgentDispatchRecord $attemptDir}
    if($null -eq $dispatch -or $dispatch.dispatch_id -cne $DispatchId){
        if($null -ne (Find-BFCurrentAgentDispatch $directory $DispatchId)){throw 'BF_CONFLICT: dispatch id was already consumed.'}
        if($null -eq $dispatch){throw 'BF_CONFLICT: no current-agent dispatch is active for this task.'}
        throw 'BF_CONFLICT: dispatch id does not match the active dispatch.'
    }
    if($Stage -cne $dispatch.stage){throw 'BF_INVALID: submitted stage differs from the dispatched stage.'}
    Assert-BFAdapterStagePolicy $adapter $Stage -Submit
    foreach($name in @('submission.json','result.json')){if(Test-Path -LiteralPath (Join-Path $attemptDir $name)){throw 'BF_CONFLICT: dispatch id was already consumed.'}}
    $file=Assert-BFSafePath $ResultFile
    $worktree=(Assert-BFSafePath $state.worker_path).TrimEnd('\','/')
    if($file.StartsWith($worktree+'\',[StringComparison]::OrdinalIgnoreCase)){throw 'BF_INVALID: the result file must be outside the worker worktree.'}
    if(-not(Test-Path -LiteralPath $file -PathType Leaf) -or (Get-Item -LiteralPath $file).Length -gt 1048576){throw 'BF_INVALID: result file is missing or larger than 1 MiB.'}
    try{$result=Read-BFJson $file}catch{throw 'BF_INVALID: result file is not a JSON object.'}
    Assert-BFWorkerResult $result 'submitted_result'
    $worker=Join-Path $attemptDir 'raw/worker'
    $baseline=Read-BFJson (Join-Path $worker 'dispatch-integrity.json')
    if($baseline.controller_sha256 -cne (Get-BFCurrentAgentIntegrity $directory $attemptDir)){throw 'BF_BLOCKED: controller state modified by worker.'}
    # One-time consumption: the marker is created exclusively before any gate.
    try{$stream=[IO.File]::Open((Join-Path $attemptDir 'submission.json'),[IO.FileMode]::CreateNew,[IO.FileAccess]::Write,[IO.FileShare]::None)}catch [IO.IOException]{throw 'BF_CONFLICT: dispatch id was already consumed.'}
    try{$bytes=[Text.UTF8Encoding]::new($false).GetBytes((Get-BFCanonicalJson ([ordered]@{dispatch_id=$DispatchId;stage=$Stage;result_file_sha256=Get-BFFileHash $file;submitted_at_utc=[DateTime]::UtcNow.ToString('o')})));$stream.Write($bytes,0,$bytes.Length)}finally{$stream.Dispose()}
    Write-BFJson (Join-Path $worker 'model-result.json') $result
    Write-BFJson (Join-Path $worker 'host-result.json') ([ordered]@{adapter='current-agent';adapter_status=$adapter.status;isolation='current_agent';dispatch_id=$DispatchId;session_id=$null;requested_model=$null;observed_model=$null;usage=$null;reported_cost_usd=$null;prompt_sha256=Get-BFFileHash (Join-Path $worker 'prompt.txt');result_sha256=Get-BFFileHash $file})
    $run=[ordered]@{state=(Read-BFTask $ProjectPath $TaskId);attempt=(Read-BFJson (Join-Path $attemptDir 'start.json'));directory=$attemptDir}
    $submitted=$result
    return Invoke-BFStage -Run $run -CodexPath '' -StageExecutor ({param($Run) $submitted}.GetNewClosure()) -ExternalDispatch
}
