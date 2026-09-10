#Requires -Version 7.0
Set-StrictMode -Version Latest

function Assert-BFPublicationInput {
    param($Publication,[string]$TaskId)
    Assert-BFFields $Publication @('schema_version','publication_id','task_id','acceptance_sha256','remote','ref','auth','author','message','provenance') @() 'publication'
    if($Publication.schema_version -ne 1){throw 'BF_INVALID: unsupported publication schema.'}
    Assert-BFUuid $Publication.publication_id; Assert-BFUuid $Publication.task_id
    if($Publication.task_id -cne $TaskId){throw 'BF_INVALID: publication belongs to another task.'}
    if($Publication.acceptance_sha256 -cnotmatch '^[0-9a-f]{64}$'){throw 'BF_INVALID: exact acceptance SHA-256 is required.'}
    Assert-BFText $Publication.remote 'publication.remote' 4096
    if($Publication.remote -match '[\x00-\x1f]' -or $Publication.remote.StartsWith('-')){throw 'BF_INVALID: invalid publication remote.'}
    if($Publication.ref -cnotmatch '^refs/heads/codex/[A-Za-z0-9][A-Za-z0-9._/-]*$' -or $Publication.ref -match '\.\.|//|/\.|\.lock(?:/|$)|[./]$'){
        throw 'BF_INVALID: publication requires a new full refs/heads/codex/* branch.'
    }
    if($Publication.auth -cnotin @('none','github_cli')){throw 'BF_INVALID: unsupported publication auth profile.'}
    Assert-BFFields $Publication.author @('name','email') @() 'publication.author'
    Assert-BFText $Publication.author.name 'publication.author.name' 256
    if($Publication.author.name -match '[<>\x00-\x1f]' -or $Publication.author.email -cnotmatch '^[^\s<>\x00-\x1f]+@[A-Za-z0-9.-]+$'){
        throw 'BF_INVALID: invalid publication author identity.'
    }
    Assert-BFText $Publication.message 'publication.message' 16384
    if($Publication.message.Contains([char]0)){throw 'BF_INVALID: NUL in publication message.'}
    Assert-BFProvenance $Publication.provenance
}

function Get-BFPublicationSharedDirectory {
    param([string]$Remote,[string]$Ref)
    $key=Get-BFHash ([ordered]@{remote=$Remote;ref=$Ref})
    return Assert-BFSafePath (Join-Path ([Environment]::GetFolderPath([Environment+SpecialFolder]::LocalApplicationData)) ('BSLFlow/publication/'+$key))
}

function Assert-BFPublicationPending {
    param($Pending,$Registration,$Prepared)
    if($Pending.publication_id -cne $Registration.input.publication_id -or $Pending.task_id -cne $Registration.input.task_id -or $Pending.project_path -ine $Registration.project_path -or $Pending.request_sha256 -cne $Registration.request_sha256){
        throw 'BF_BLOCKED: this remote/ref has another unresolved publication; no write is allowed.'
    }
    if($null -ne $Prepared -and $Pending.prepared_sha256 -cne (Get-BFHash $Prepared)){throw 'BF_CONFLICT: pending publication commit differs from prepared evidence.'}
}

function Assert-BFPublicationPrepared {
    param($Prepared,$Registration,[string]$Directory)
    Assert-BFFields $Prepared @('schema_version','request_sha256','registration_sha256','git') @() 'publication.prepared'
    Assert-BFFields $Prepared.git @('repository','staging_directory','commit_oid','tree_oid','parent_oid','object_format','mode_policy','file_count') @() 'publication.commit'
    if($Prepared.schema_version -ne 1 -or $Prepared.request_sha256 -cne $Registration.request_sha256 -or $Prepared.registration_sha256 -cne (Get-BFHash $Registration)){
        throw 'BF_CONFLICT: prepared publication identity changed.'
    }
    foreach($name in @('commit_oid','tree_oid','parent_oid')){if($Prepared.git.$name -cnotmatch '^(?:[0-9a-f]{40}|[0-9a-f]{64})$'){throw 'BF_BLOCKED: invalid prepared Git object identity.'}}
    if($Prepared.git.object_format -cnotin @('sha1','sha256') -or $Prepared.git.file_count -lt 1){throw 'BF_BLOCKED: unsupported prepared Git tree.'}
    $repository=Assert-BFSafePath $Prepared.git.repository
    $staging=Assert-BFSafePath $Prepared.git.staging_directory
    $buildRoot=Join-Path $Directory 'builds'
    if((Split-Path $staging -Parent) -ine $buildRoot -or (Split-Path $staging -Leaf) -cnotmatch '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' -or $repository -ine (Get-BFPublicationRepositoryPath $staging) -or $Prepared.git.parent_oid -cne $Registration.baseline){
        throw 'BF_BLOCKED: publication repository or parent escaped the accepted baseline.'
    }
}

function Test-BFPublicationPushSettled {
    param([string]$Directory,$Registration,$Prepared)
    $processRoot=Join-Path $Directory 'processes'
    if(-not(Test-Path -LiteralPath $processRoot -PathType Container)){return $false}
    $operations=@(Get-ChildItem -LiteralPath $processRoot -Directory -Filter 'push-create-only-*')
    if($operations.Count -ne 1){return $false}
    $identityPath=Join-Path $operations[0].FullName 'process.json'
    if(-not(Test-Path -LiteralPath $identityPath -PathType Leaf)){return $false}
    try{
        $identity=Read-BFJson $identityPath
        Assert-BFFields $identity @('pid','start_time_utc','executable','executable_sha256','arguments_sha256','dispatch_sha256','non_interruptible','receipt_directory') @() 'publication.push_process'
        $dispatchPath=Join-Path $Directory 'dispatch.json'
        $dispatch=Read-BFJson $dispatchPath
        Assert-BFFields $dispatch @('schema_version','publication_id','task_id','acceptance_sha256','request_sha256','commit_oid','remote','ref','transport','auth','arguments_sha256') @() 'publication.push_dispatch'
        if($identity.dispatch_sha256 -cne (Get-FileHash -LiteralPath $dispatchPath -Algorithm SHA256).Hash.ToLowerInvariant() -or $dispatch.schema_version -ne 1 -or $dispatch.publication_id -cne $Registration.input.publication_id -or $dispatch.task_id -cne $Registration.input.task_id -or $dispatch.acceptance_sha256 -cne $Registration.input.acceptance_sha256 -or $dispatch.request_sha256 -cne $Registration.request_sha256 -or $dispatch.commit_oid -cne $Prepared.git.commit_oid -or $dispatch.remote -cne $Registration.dependencies.remote -or $dispatch.ref -cne $Registration.input.ref -or $dispatch.transport -cne $Registration.dependencies.transport -or $dispatch.auth -cne $Registration.input.auth -or $dispatch.arguments_sha256 -cne $identity.arguments_sha256){return $false}
        if(($identity.pid -isnot [int] -and $identity.pid -isnot [long]) -or $identity.pid -lt 1 -or $identity.pid -gt [int]::MaxValue){return $false}
        $timestamp=[datetime]::MinValue
        if($identity.start_time_utc -isnot [string] -or -not [datetime]::TryParseExact($identity.start_time_utc,'o',[Globalization.CultureInfo]::InvariantCulture,[Globalization.DateTimeStyles]::RoundtripKind,[ref]$timestamp) -or $timestamp.Kind -ne [DateTimeKind]::Utc){return $false}
        if($identity.non_interruptible -isnot [bool] -or -not $identity.non_interruptible -or $identity.arguments_sha256 -cnotmatch '^[0-9a-f]{64}$'){return $false}
        if($identity.executable -ine $Registration.dependencies.git.path -or $identity.executable_sha256 -cne $Registration.dependencies.git.sha256){return $false}
        if((Assert-BFSafePath $identity.receipt_directory) -ine (Assert-BFSafePath $operations[0].FullName)){return $false}
        $result=Read-BFJson (Join-Path $operations[0].FullName 'result.json')
        Assert-BFFields $result @('exit_code','completed','stop_reason','process','stdout_sha256','stderr_sha256') @() 'publication.push_result'
        if((Get-BFHash $result.process) -cne (Get-BFHash $identity) -or $result.completed -isnot [bool]){return $false}
        if($result.completed){
            if($result.exit_code -isnot [int] -and $result.exit_code -isnot [long]){return $false}
        }elseif($null -ne $result.exit_code -or $result.stop_reason -cnotin @('timeout','output_limit')){return $false}
        foreach($name in @('stdout_sha256','stderr_sha256')){if($result.$name -cnotmatch '^[0-9a-f]{64}$'){return $false}}
        return $null -eq (Get-BFOwnedProcess $identity)
    }catch{return $false}
}

function Assert-BFPublishedReceipt {
    param($Receipt,$Registration,$Prepared,$Intent,[string]$Directory)
    Assert-BFFields $Receipt @('schema_version','publication_id','task_id','request_sha256','acceptance_sha256','intent_sha256','commit_oid','remote','ref','observation','published_at_utc','status') @() 'publication.receipt'
    $declaration=$Registration.input
    if($Receipt.schema_version -ne 1 -or $Receipt.publication_id -cne $declaration.publication_id -or $Receipt.task_id -cne $declaration.task_id -or $Receipt.request_sha256 -cne $Registration.request_sha256 -or $Receipt.acceptance_sha256 -cne $declaration.acceptance_sha256 -or $Receipt.intent_sha256 -cne (Get-BFHash $Intent) -or $Receipt.commit_oid -cne $Prepared.git.commit_oid -or $Receipt.remote -cne $Registration.dependencies.remote -or $Receipt.ref -cne $declaration.ref -or $Receipt.status -cne 'published'){
        throw 'BF_CONFLICT: saved publication receipt identity changed.'
    }
    try{$timestamp=[DateTime]::Parse($Receipt.published_at_utc,[Globalization.CultureInfo]::InvariantCulture,[Globalization.DateTimeStyles]::RoundtripKind)}catch{throw 'BF_BLOCKED: invalid publication receipt timestamp.'}
    if($timestamp.ToUniversalTime().ToString('o') -cne $Receipt.published_at_utc){throw 'BF_BLOCKED: noncanonical publication receipt timestamp.'}
    Assert-BFFields $Receipt.observation @('oid','path','sha256') @() 'publication.receipt.observation'
    $path=Assert-BFSafePath $Receipt.observation.path
    $observationRoot=Join-Path $Directory 'observations'
    if(-not $path.StartsWith($observationRoot+[IO.Path]::DirectorySeparatorChar,[StringComparison]::OrdinalIgnoreCase) -or $Receipt.observation.oid -cne $Receipt.commit_oid){throw 'BF_BLOCKED: publication receipt observation escaped its evidence directory.'}
    $observation=Read-BFJson $path
    Assert-BFFields $observation @('schema_version','observed_at_utc','remote','ref','oid','result') @() 'publication.observation'
    if((Get-BFHash $observation) -cne $Receipt.observation.sha256 -or $observation.schema_version -ne 1 -or $observation.remote -cne $declaration.remote -or $observation.ref -cne $declaration.ref -or $observation.oid -cne $Receipt.commit_oid){throw 'BF_BLOCKED: recorded publication observation changed.'}
}

function Get-BFPublicationObservation {
    param($Prepared,$Publication,[string]$Directory)
    $readDirectory=Join-Path $Directory ('observations/'+[guid]::NewGuid().ToString())
    [void][IO.Directory]::CreateDirectory($readDirectory)
    try{
        $observed=Get-BFPublicationRemote $Prepared.git.repository $Publication $readDirectory
        $oid=Get-BFValue $observed 'oid'
        $completed=(Get-BFValue $observed 'completed') -eq $true -and $null -eq (Get-BFValue $observed 'stop_reason')
        $found=$completed -and (Get-BFValue $observed 'exit_code') -eq 0 -and $null -ne $oid
        # ls-remote --exit-code distinguishes an absent ref from a transport failure.
        $missing=$completed -and (Get-BFValue $observed 'exit_code') -eq 2 -and $null -eq $oid -and $observed.stdout -is [string] -and $observed.stdout.Length -eq 0
        if(-not($found -or $missing)){throw 'BF_BLOCKED: remote query did not return a verified present or absent ref.'}
        if($null -ne $oid -and $oid -cnotmatch '^(?:[0-9a-f]{40}|[0-9a-f]{64})$'){throw 'BF_BLOCKED: invalid remote object identity.'}
        $receipt=[ordered]@{schema_version=1;observed_at_utc=[DateTime]::UtcNow.ToString('o');remote=$Publication.remote;ref=$Publication.ref;oid=$oid;result=$observed}
        Write-BFJson (Join-Path $readDirectory 'observation.json') $receipt
        return [ordered]@{oid=$oid;path=Join-Path $readDirectory 'observation.json';sha256=Get-BFHash $receipt}
    }catch{
        Write-BFJson (Join-Path $readDirectory 'read-failure.json') @{schema_version=1;observed_at_utc=[DateTime]::UtcNow.ToString('o');status='unknown';error_type=$_.Exception.GetType().FullName}
        throw 'BF_BLOCKED: remote control read failed; publication outcome remains unknown.'
    }
}

function Publish-BFTask {
    [CmdletBinding()]
    param([Parameter(Mandatory)][string]$ProjectPath,[Parameter(Mandatory)][string]$TaskId,[Parameter(Mandatory)][Alias('Input')]$Publication,[bool]$ResumeOnly=$false)
    $taskLock=$null; $remoteLock=$null; $prepared=$null; $receiptPath=$null; $idempotent=$false; $safeRemote=$null; $requestHash=$null
    try{
        Assert-BFPublicationInput $Publication $TaskId
        $project=Assert-BFSafePath $ProjectPath
        $taskDirectory=Get-BFTaskDirectory $project $TaskId
        $directory=Assert-BFSafePath (Join-Path $taskDirectory ('publications/'+$Publication.publication_id))
        $requestPath=Join-Path $directory 'request.json'; $preparedPath=Join-Path $directory 'prepared.json'; $intentPath=Join-Path $directory 'intent.json'; $receiptPath=Join-Path $directory 'published.json'
        # Delivery takes its own task lock. Recheck under our lock before any dispatch.
        $delivery=$null
        if(-not $ResumeOnly -and -not(Test-Path -LiteralPath $intentPath) -and -not(Test-Path -LiteralPath $receiptPath)){$delivery=Export-BFTaskDelivery $project $TaskId}
        $taskLock=Enter-BFLock $taskDirectory
        $state=Read-BFJournal $taskDirectory
        if($null -eq $state -or $state.task_id -cne $TaskId -or $state.project_path -ine $project){throw 'BF_BLOCKED: publication task identity is unavailable.'}
        # A durable published receipt completes local cleanup even while the
        # remote or its client tools are unavailable. It is historical evidence.
        $dependencies=if((Test-Path -LiteralPath $receiptPath) -and (Test-Path -LiteralPath $requestPath)){
            (Read-BFJson $requestPath).dependencies
        }else{Get-BFPublicationGitDependencies $Publication}
        Assert-BFText (Get-BFValue $dependencies 'remote') 'publication canonical remote' 4096
        $safeRemote=$dependencies.remote
        $requestHash=Get-BFHash $Publication
        $shared=Get-BFPublicationSharedDirectory $dependencies.remote $Publication.ref
        $remoteLock=Enter-BFLock $shared
        $pendingPath=Join-Path $shared 'pending.json'
        if(Test-Path -LiteralPath $requestPath){
            $idempotent=$true
            $registration=Read-BFJson $requestPath
            Assert-BFFields $registration @('schema_version','input','request_sha256','project_path','dependencies','metadata','baseline','delivery_path','manifest_sha256','acceptance_raw_sha256') @() 'publication.registration'
            if($registration.schema_version -ne 1 -or $registration.request_sha256 -cne $requestHash -or (Get-BFHash $registration.input) -cne $requestHash -or $registration.project_path -ine $project){throw 'BF_CONFLICT: publication UUID was used with different input.'}
            Assert-BFFields $registration.metadata @('name','email','message','timestamp') @() 'publication.metadata'
            if($registration.metadata.name -cne $Publication.author.name -or $registration.metadata.email -cne $Publication.author.email -or $registration.metadata.message -cne $Publication.message){throw 'BF_CONFLICT: saved commit metadata differs from the trusted input.'}
            if((Get-BFHash $registration.dependencies) -cne (Get-BFHash $dependencies)){throw 'BF_BLOCKED: publication Git dependencies changed; retained intent cannot be redispatched.'}
        }else{
            if($ResumeOnly){throw 'BF_BLOCKED: publication has not been prepared; resume cannot dispatch.'}
            if(Test-Path -LiteralPath $pendingPath){throw 'BF_BLOCKED: remote/ref has an unresolved publication.'}
            $accepted=Get-BFDeliveryReceipt $state
            if($accepted.identity -cne $Publication.acceptance_sha256){throw 'BF_BLOCKED: publication does not name the current acceptance.'}
            $manifest=Assert-BFDeliveryCurrent $state $accepted
            Assert-BFCoverageAccepted $state
            if($null -eq $delivery -or -not(Test-BFDeliveryExisting $delivery.path $state $accepted $manifest)){throw 'BF_BLOCKED: exact accepted handoff is unavailable.'}
            $registration=[ordered]@{schema_version=1;input=$Publication;request_sha256=$requestHash;project_path=$project;dependencies=$dependencies;metadata=[ordered]@{name=$Publication.author.name;email=$Publication.author.email;message=$Publication.message;timestamp=[DateTime]::UtcNow.ToString('o')};baseline=$accepted.receipt.baseline;delivery_path=$delivery.path;manifest_sha256=$manifest.sha256;acceptance_raw_sha256=$accepted.raw_sha256}
            Write-BFJson $requestPath $registration
        }
        if(Test-Path -LiteralPath $preparedPath){$prepared=Read-BFJson $preparedPath; Assert-BFPublicationPrepared $prepared $registration $directory}
        if(Test-Path -LiteralPath $pendingPath){Assert-BFPublicationPending (Read-BFJson $pendingPath) $registration $prepared}
        $hasIntent=Test-Path -LiteralPath $intentPath
        if((Test-Path -LiteralPath $receiptPath) -and -not $hasIntent){throw 'BF_BLOCKED: published receipt has no durable intent.'}
        if(-not $hasIntent){
            if($ResumeOnly){throw 'BF_BLOCKED: no push intent exists; resume performs no dispatch.'}
            $accepted=Get-BFDeliveryReceipt $state
            $manifest=Assert-BFDeliveryCurrent $state $accepted
            Assert-BFCoverageAccepted $state
            if($accepted.identity -cne $Publication.acceptance_sha256 -or $accepted.raw_sha256 -cne $registration.acceptance_raw_sha256 -or $manifest.sha256 -cne $registration.manifest_sha256 -or -not(Test-BFDeliveryExisting $registration.delivery_path $state $accepted $manifest)){
                throw 'BF_BLOCKED: accepted publication inputs changed before dispatch.'
            }
            # A pre-dispatch restart never trusts a saved object database or its
            # config. Rebuild from accepted bytes and compare the stable commit.
            $buildDirectory=Join-Path $directory ('builds/'+[guid]::NewGuid().ToString())
            $commit=New-BFPublicationCommit $state $registration.delivery_path $manifest $buildDirectory $registration.metadata $Publication
            if($null -ne $prepared){
                foreach($name in @('commit_oid','tree_oid','parent_oid','object_format','mode_policy','file_count')){
                    if($prepared.git.$name -cne $commit.$name){throw 'BF_CONFLICT: saved prepared commit differs from the exact accepted source.'}
                }
                $historyPath=Join-Path $directory ('preparations/'+(Get-BFHash $prepared)+'.json')
                if(-not(Test-Path -LiteralPath $historyPath)){Write-BFJson $historyPath $prepared}
                if(Test-Path -LiteralPath $pendingPath){
                    # No intent proves that this controller never dispatched.
                    $oldPending=Read-BFJson $pendingPath
                    Assert-BFPublicationPending $oldPending $registration $prepared
                    Write-BFJson (Join-Path $directory ('preparations/pending-'+[guid]::NewGuid().ToString()+'.json')) $oldPending
                    Remove-Item -LiteralPath $pendingPath -Force
                }
            }
            $prepared=[ordered]@{schema_version=1;request_sha256=$requestHash;registration_sha256=Get-BFHash $registration;git=$commit}
            Assert-BFPublicationPrepared $prepared $registration $directory
            Write-BFJson $preparedPath $prepared -Replace
            $before=Get-BFPublicationObservation $prepared $Publication $directory
            if($null -ne $before.oid){throw 'BF_CONFLICT: a new publication requires an absent remote ref, even when its OID matches.'}
            [void](Assert-BFDeliveryCurrent $state $accepted)
            Assert-BFCoverageAccepted $state
            if((Get-BFHash (Get-BFPublicationGitDependencies $Publication)) -cne (Get-BFHash $dependencies)){throw 'BF_BLOCKED: Git dependencies changed before push.'}
            if((Get-BFHash (Read-BFJson $preparedPath)) -cne (Get-BFHash $prepared) -or (Get-BFHash (Read-BFJson $requestPath)) -cne (Get-BFHash $registration)){throw 'BF_BLOCKED: publication preparation changed before push.'}
            $pending=[ordered]@{schema_version=1;publication_id=$Publication.publication_id;task_id=$TaskId;project_path=$project;request_sha256=$requestHash;prepared_sha256=Get-BFHash $prepared;created_at_utc=[DateTime]::UtcNow.ToString('o')}
            if(Test-Path -LiteralPath $pendingPath){Assert-BFPublicationPending (Read-BFJson $pendingPath) $registration $prepared}else{Write-BFJson $pendingPath $pending}
            $intent=[ordered]@{schema_version=1;publication_id=$Publication.publication_id;request_sha256=$requestHash;registration_sha256=Get-BFHash $registration;prepared_sha256=Get-BFHash $prepared;commit_oid=$prepared.git.commit_oid;remote=$dependencies.remote;ref=$Publication.ref;created_at_utc=[DateTime]::UtcNow.ToString('o')}
            Write-BFJson $intentPath $intent
            try{[void](Send-BFPublicationCommit $prepared.git.repository $prepared.git.commit_oid $Publication (Join-Path $directory 'push'))}
            catch{Write-BFJson (Join-Path $directory 'push-call-failure.json') @{schema_version=1;error_type=$_.Exception.GetType().FullName;status='unknown'}}
        }
        # Any durable intent makes every subsequent path read-only against the remote.
        if($null -eq $prepared){throw 'BF_BLOCKED: push intent is missing its prepared commit.'}
        $intent=Read-BFJson $intentPath
        Assert-BFFields $intent @('schema_version','publication_id','request_sha256','registration_sha256','prepared_sha256','commit_oid','remote','ref','created_at_utc') @() 'publication.intent'
        if($intent.schema_version -ne 1 -or $intent.publication_id -cne $Publication.publication_id -or $intent.request_sha256 -cne $requestHash -or $intent.registration_sha256 -cne (Get-BFHash $registration) -or $intent.prepared_sha256 -cne (Get-BFHash $prepared) -or $intent.commit_oid -cne $prepared.git.commit_oid -or $intent.remote -cne $dependencies.remote -or $intent.ref -cne $Publication.ref){throw 'BF_CONFLICT: durable publication intent changed.'}
        if(Test-Path -LiteralPath $receiptPath){
            $receipt=Read-BFJson $receiptPath
            Assert-BFPublishedReceipt $receipt $registration $prepared $intent $directory
        }else{
            $observed=Get-BFPublicationObservation $prepared $Publication $directory
            if($null -eq $observed.oid){throw 'BF_BLOCKED: dispatched publication is absent or unsettled; automatic push replay is forbidden.'}
            if($observed.oid -cne $prepared.git.commit_oid){throw 'BF_CONFLICT: remote ref has another OID; publication will not overwrite it.'}
            if(-not(Test-BFPublicationPushSettled (Join-Path $directory 'push') $registration $prepared)){throw 'BF_BLOCKED: original push process is alive or its identity is unknown; pending remains held.'}
            $receipt=[ordered]@{schema_version=1;publication_id=$Publication.publication_id;task_id=$TaskId;request_sha256=$requestHash;acceptance_sha256=$Publication.acceptance_sha256;intent_sha256=Get-BFHash $intent;commit_oid=$prepared.git.commit_oid;remote=$dependencies.remote;ref=$Publication.ref;observation=$observed;published_at_utc=[DateTime]::UtcNow.ToString('o');status='published'}
            Write-BFJson $receiptPath $receipt
        }
        if(Test-Path -LiteralPath $pendingPath){Assert-BFPublicationPending (Read-BFJson $pendingPath) $registration $prepared;Remove-Item -LiteralPath $pendingPath -Force}
        return [ordered]@{schema_version=1;publication_id=$Publication.publication_id;task_id=$TaskId;status='published';commit_oid=$prepared.git.commit_oid;remote=$Publication.remote;ref=$Publication.ref;acceptance_sha256=$Publication.acceptance_sha256;request_sha256=$requestHash;receipt_path=$receiptPath;idempotent=$idempotent;blockers=@()}
    }catch{
        $message=if($_.Exception.Message -match '^BF_(INVALID|BLOCKED|CONFLICT):'){$_.Exception.Message}else{'BF_BLOCKED: publication could not complete; retain its controller evidence.'}
        return [ordered]@{schema_version=1;publication_id=Get-BFValue $Publication 'publication_id';task_id=$TaskId;status='blocked';commit_oid=if($null -ne $prepared){Get-BFValue $prepared.git 'commit_oid'}else{$null};remote=$safeRemote;ref=Get-BFValue $Publication 'ref';acceptance_sha256=Get-BFValue $Publication 'acceptance_sha256';request_sha256=$requestHash;receipt_path=$receiptPath;idempotent=$idempotent;blockers=@($message)}
    }finally{if($null -ne $remoteLock){$remoteLock.Dispose()};if($null -ne $taskLock){$taskLock.Dispose()}}
}
