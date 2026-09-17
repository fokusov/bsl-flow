#Requires -Version 7.0
[CmdletBinding()]
param(
    [Parameter(Mandatory)][ValidateSet('Start','Status','Next','Context','Run','Record','Update','Accept','Resume','Cancel','Deliver','Serve','Publish','PublishResume','Create','EditRegistry','List','Show','History','Overview','ArchiveTask','UnarchiveTask','Activate')][string]$Action,
    [Parameter(Mandatory)][string]$ProjectPath,
    [string]$TaskId,
    [string]$InputFile,
    [string]$AttemptId,
    [string]$CodexPath,
    [ValidateSet('stdin')][string]$RuntimeAuth,
    [string]$Format='Human',
    [string]$Title,
    [string]$Description,
    [string]$Priority,
    [string[]]$Labels,
    [string[]]$DependsOn,
    [int]$ExpectedRevision=-1,
    [string[]]$Status,
    [string[]]$Stage,
    [string[]]$Label,
    [string]$UpdatedBefore,
    [string]$UpdatedAfter,
    [string]$Archived='false',
    [string]$Sort,
    [string]$Order,
    [int]$Limit=0,
    [string]$Cursor
)

Set-StrictMode -Version Latest
$ErrorActionPreference='Stop'
if (-not [string]::IsNullOrWhiteSpace($env:BSL_FLOW_HOST_PATH)) {
    [Console]::OutputEncoding=New-Object Text.UTF8Encoding($false)
    $OutputEncoding=[Console]::OutputEncoding
}
foreach($module in @('Task.Storage.ps1','Task.Registry.ps1','Task.Contracts.ps1','Task.Memory.ps1','Task.Architecture.ps1','Task.Gates.ps1','Task.Process.ps1','Task.Engine.ps1','Task.Stages.ps1','Task.Delivery.ps1','Task.Runner.ps1','Task.PublicationGit.ps1','Task.Publication.ps1')){ . (Join-Path $PSScriptRoot $module) }
. (Join-Path (Split-Path $PSScriptRoot -Parent) 'adapters/Codex.ps1')
. (Join-Path (Split-Path $PSScriptRoot -Parent) 'adapters/OpenCode.ps1')
. (Join-Path (Split-Path $PSScriptRoot -Parent) 'adapters/ProfiledCodex.ps1')
. (Join-Path $PSScriptRoot 'Task.ManagedReview.ps1')

$code=0;$state=$null;$delivery=$null;$registryHandled=$false
try {
    if($RuntimeAuth){
        if($Action -notin @('Run','Resume','Update','Serve')){throw 'BF_INVALID: runtime auth is only valid for execution or recovery.'}
        if(-not [Console]::IsInputRedirected){throw 'BF_INVALID: runtime auth requires a private redirected stdin pipe.'}
        [Console]::InputEncoding=[Text.UTF8Encoding]::new($false)
        $authLine=[Console]::In.ReadLine()
        if($null -eq $authLine -or $authLine.Length -gt 16384){throw 'BF_INVALID: missing or oversized runtime auth input.'}
        try{$auth=ConvertFrom-Json -InputObject $authLine -ErrorAction Stop}catch{throw 'BF_INVALID: malformed runtime auth input.'}
        Assert-BFFields $auth @('username','password') @() 'runtime_auth'
        Assert-BFText $auth.username 'runtime_auth.username' 1024
        if($auth.password -isnot [string] -or $auth.password.Length -gt 8192){throw 'BF_INVALID: invalid runtime auth password.'}
        $secure=[Security.SecureString]::new()
        foreach($character in $auth.password.ToCharArray()){$secure.AppendChar($character)}
        $secure.MakeReadOnly()
        $script:BFNativeCredential=[pscredential]::new($auth.username,$secure)
        $authLine=$null;$auth=$null
    }
    $ProjectPath=Assert-BFSafePath $ProjectPath
    if($Action -in @('Create','EditRegistry','List','Show','History','Overview','ArchiveTask','UnarchiveTask','Activate')){
        # Repository task registry (schema/store v1). The registry command owns
        # its output contract (one versioned JSON document, or Human text) and
        # its exit codes: 0 success, 2 BF_INVALID, 11 BF_BLOCKED/BF_CONFLICT.
        $registry=Invoke-BFRegistryCommand -Action $Action -ProjectPath $ProjectPath -TaskId $TaskId -Title $Title -Description $Description -Priority $Priority -Labels $Labels -DependsOn $DependsOn -ExpectedRevision $ExpectedRevision -Status $Status -Stage $Stage -Label $Label -UpdatedBefore $UpdatedBefore -UpdatedAfter $UpdatedAfter -Archived $Archived -Sort $Sort -Order $Order -Limit $Limit -Cursor $Cursor -Format $Format -InputFile $InputFile
        if($registry.StdOut){ Write-Output $registry.StdOut }
        if($registry.StdErr){ [Console]::Error.WriteLine($registry.StdErr) }
        $code=$registry.ExitCode
        $registryHandled=$true
    } elseif($Action -in @('Publish','PublishResume')){
        Assert-BFUuid $TaskId
        if(-not $InputFile){throw 'BF_INVALID: publication requires a separate trusted -InputFile.'}
        $envelope=Publish-BFTask -ProjectPath $ProjectPath -TaskId $TaskId -Input (Read-BFJson $InputFile) -ResumeOnly ($Action -eq 'PublishResume')
        $reason=@($envelope.blockers) -join '; '
        $code=if($envelope.status -eq 'published'){0}elseif($reason.StartsWith('BF_INVALID:')){2}elseif($reason.StartsWith('BF_CONFLICT:')){3}else{11}
    } elseif($Action -eq 'Serve'){
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
        if($Action -in @('Run','Resume','Next') -and (Test-BFRegistryPlannedTask -ProjectPath $ProjectPath -TaskId $TaskId)){
            # A planned repository task has no execution authorization: the
            # controller write slice (activation) is not declared yet, so the
            # run path must never start an attempt for it.
            throw ('BF_BLOCKED: task {0} is planned in the repository task registry and activation is not yet declared; the {1} path will not start it.' -f $TaskId,$Action)
        }
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
    if(-not $registryHandled -and $Action -eq 'Context'){
        # Pure read-only projection: it resolves the ADR source and includes
        # Get-BFNext internally; it writes nothing.
        $envelope=Get-BFTaskContext $state $ProjectPath
    } elseif(-not $registryHandled -and $Action -notin @('Serve','Publish','PublishResume')){
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
    $stateVar=Get-Variable -Name state -Scope Local -ErrorAction SilentlyContinue
    if($Action -eq 'Context' -and $null -ne $stateVar -and $null -ne $stateVar.Value){
        # A Context read error keeps the versioned context-shaped envelope.
        $envelope=New-BFContextErrorEnvelope $stateVar.Value $reason $ProjectPath
    } else {
        $envelope=[ordered]@{schema_version=1;task_id=$TaskId;revision=$null;status=if($code -eq 12){'failed'}else{'blocked'};stage=$null;next_action='inspect_blocker';blockers=@($reason);evidence_refs=@()}
    }
    # Read-only commands expose corrupted/unavailable state in the envelope, never PASS.
    if($Action -in @('Status','Next','Context') -and $code -eq 11){$code=0}
}
if(-not $registryHandled){ Write-Output ($envelope|ConvertTo-Json -Depth 64 -Compress) }
if($null -ne (Get-Variable BFNativeCredential -Scope Script -ErrorAction SilentlyContinue)){
    if($null -ne $script:BFNativeCredential){$script:BFNativeCredential.Password.Dispose();$script:BFNativeCredential=$null}
}
exit $code
