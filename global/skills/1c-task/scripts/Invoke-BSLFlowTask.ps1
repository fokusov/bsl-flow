[CmdletBinding()]
param(
    [Parameter(Mandatory)][ValidateSet('Start','Status','Next','Run','Record','Update','Accept','Resume','Cancel','Deliver','Serve')][string]$Action,
    [Parameter(Mandatory)][string]$ProjectPath,
    [string]$TaskId,
    [string]$InputFile,
    [string]$AttemptId,
    [string]$CodexPath
)

Set-StrictMode -Version Latest
$ErrorActionPreference='Stop'
if (-not [string]::IsNullOrWhiteSpace($env:BSL_FLOW_HOST_PATH)) {
    [Console]::OutputEncoding=New-Object Text.UTF8Encoding($false)
    $OutputEncoding=[Console]::OutputEncoding
}
foreach($module in @('Task.Storage.ps1','Task.Contracts.ps1','Task.Gates.ps1','Task.Process.ps1','Task.Engine.ps1','Task.Stages.ps1','Task.Delivery.ps1','Task.Runner.ps1')){ . (Join-Path $PSScriptRoot $module) }
. (Join-Path (Split-Path $PSScriptRoot -Parent) 'adapters/Codex.ps1')

$code=0;$state=$null;$delivery=$null
try {
    $ProjectPath=Assert-BFSafePath $ProjectPath
    if($Action -eq 'Serve'){
        if(-not $InputFile){throw 'BF_INVALID: Serve requires -InputFile with a trusted queue.'}
        if($TaskId){throw 'BF_INVALID: Serve accepts task IDs only in the queue input.'}
        $snapshot=Invoke-BFTaskQueue -ProjectPath $ProjectPath -Input (Read-BFJson $InputFile) -CodexPath $CodexPath
        $statuses=@($snapshot.tasks.Values | ForEach-Object { $_.status })
        $queueStatus=if(@($statuses|Where-Object{$_ -in @('blocked','failed')}).Count){'blocked'}elseif($statuses -contains 'needs_input'){'needs_input'}elseif(@($statuses|Where-Object{$_ -ne 'completed'}).Count -eq 0){'completed'}else{'waiting'}
        $envelope=[ordered]@{schema_version=1;queue_id=$snapshot.queue_id;status=$queueStatus;snapshot=$snapshot}
        $code=switch($queueStatus){'completed'{0}'needs_input'{10}default{11}}
    } elseif($Action -eq 'Start'){
        if(-not $InputFile){throw 'BF_INVALID: Start requires -InputFile with a trusted request.'}
        $state=Start-BFTask $ProjectPath (Read-BFJson $InputFile)
    } else {
        Assert-BFUuid $TaskId
        switch($Action){
            'Update'{if(-not $InputFile){throw 'BF_INVALID: Update requires -InputFile.'};$state=Update-BFTask $ProjectPath $TaskId (Read-BFJson $InputFile)}
            'Record'{$state=Record-BFAttempt $ProjectPath $TaskId $AttemptId}
            'Cancel'{$state=Cancel-BFTask $ProjectPath $TaskId}
            'Accept'{$state=Accept-BFTask $ProjectPath $TaskId}
            'Resume'{$state=Resume-BFAttempt $ProjectPath $TaskId;$state=Invoke-BFRun $ProjectPath $TaskId $CodexPath}
            'Run'{$state=Invoke-BFRun $ProjectPath $TaskId $CodexPath}
            'Deliver'{$delivery=Export-BFTaskDelivery $ProjectPath $TaskId;$state=Read-BFTask $ProjectPath $TaskId}
            default{$state=Read-BFTask $ProjectPath $TaskId}
        }
    }
    if($Action -ne 'Serve'){
        $next=Get-BFNext $state
        $envelope=New-BFEnvelope $state $next.action @($next.blockers) $next.stage
        if($null -ne $delivery){$envelope.delivery=$delivery}
    }
    if($Action -in @('Run','Resume','Accept')){
        $code=switch($state.status){'completed'{0}'needs_input'{10}'failed'{12}'cancelled'{13}default{11}}
    }
} catch {
    $reason=$_.Exception.Message
    $code=if($reason.StartsWith('BF_INVALID:')){2}elseif($reason.StartsWith('BF_CONFLICT:')){3}elseif($reason.StartsWith('BF_BLOCKED:')){11}elseif($reason.StartsWith('BF_FAIL:')){12}else{4}
    $envelope=[ordered]@{schema_version=1;task_id=$TaskId;revision=$null;status=if($code -eq 12){'failed'}else{'blocked'};stage=$null;next_action='inspect_blocker';blockers=@($reason);evidence_refs=@()}
    # Read-only commands expose corrupted/unavailable state in the envelope, never PASS.
    if($Action -in @('Status','Next') -and $code -eq 11){$code=0}
}
Write-Output ($envelope|ConvertTo-Json -Depth 64 -Compress)
exit $code
