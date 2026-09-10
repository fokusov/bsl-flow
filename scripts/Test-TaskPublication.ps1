#Requires -Version 7.0
[CmdletBinding()]
param([string]$PackageRoot,[string]$PublicationRoot,[string]$Executable,[switch]$KeepFixture)
Set-StrictMode -Version Latest
$ErrorActionPreference='Stop'
if(-not $PackageRoot){$PackageRoot=Split-Path $PSScriptRoot -Parent}
$PackageRoot=[IO.Path]::GetFullPath($PackageRoot)
$core=Join-Path $PackageRoot 'global/skills/1c-task/scripts'
$savedHost=$env:BSL_FLOW_HOST_PATH
if($Executable){
    $Executable=[IO.Path]::GetFullPath($Executable)
    $version=(& $Executable version | ConvertFrom-Json)
    if($LASTEXITCODE -ne 0){throw 'CLI version failed.'}
    & $Executable task status --project $PackageRoot --task ([guid]::NewGuid().ToString()) | Out-Null
    $cache=Join-Path ([Environment]::GetFolderPath([Environment+SpecialFolder]::LocalApplicationData)) ('BSLFlow/bundles/'+$version.version+'-'+$version.bundle_sha256)
    $core=Join-Path $cache 'global/skills/1c-task/scripts'
    if(-not(Test-Path -LiteralPath $core)){throw 'Verified CLI bundle was not extracted.'}
    $env:BSL_FLOW_HOST_PATH=$Executable
}
foreach($name in @('Task.Storage.ps1','Task.Contracts.ps1','Task.Gates.ps1','Task.Process.ps1','Task.Engine.ps1','Task.Stages.ps1','Task.Delivery.ps1')){. (Join-Path $core $name)}
if(-not $PublicationRoot){$PublicationRoot=$core}
foreach($name in @('Task.PublicationGit.ps1','Task.Publication.ps1')){. (Join-Path $PublicationRoot $name)}
$script:checks=0
function Check-P([bool]$Value,[string]$Message){if(-not $Value){throw "ASSERTION FAILED: $Message"};$script:checks++}
function Write-P([string]$Path,[string]$Text){[void][IO.Directory]::CreateDirectory((Split-Path $Path -Parent));[IO.File]::WriteAllText($Path,$Text,[Text.UTF8Encoding]::new($false))}
function Clone-P($Value){return (Get-BFCanonicalJson $Value | ConvertFrom-Json)}
function Block-P($Result,[string]$Pattern){Check-P ($Result.status -eq 'blocked' -and (@($Result.blockers)-join '; ') -match $Pattern) ("Expected blocker $Pattern; got "+(Get-BFCanonicalJson $Result))}
$fixture=Join-Path $PackageRoot ('work/publication-tests-'+[guid]::NewGuid().ToString('N'))
$script:publicationShared=Join-Path $fixture 'shared'
function Get-BFPublicationSharedDirectory([string]$Remote,[string]$Ref){return Join-Path $script:publicationShared (Get-BFHash @{remote=$Remote;ref=$Ref})}
$succeeded=$false
try{
    $project=Join-Path $fixture 'project';[void][IO.Directory]::CreateDirectory($project)
    [void](Invoke-BFGit $project @('init'))
    Write-P (Join-Path $project '.gitignore') ".bsl-flow/`n"
    Write-P (Join-Path $project 'keep.txt') "Before`r`n"
    Write-P (Join-Path $project 'remove.txt') 'Remove this baseline file.'
    [void](Invoke-BFGit $project @('add','.'))
    [void](Invoke-BFGit $project @('-c','user.name=Publication Test','-c','user.email=test@example.invalid','commit','-m','Baseline'))
    $request=[pscustomobject]@{schema_version=1;request_id=[guid]::NewGuid().ToString();prompt='Change the fixture greeting and remove obsolete source.';mode='implement';analysis_goal='analysis';complexity='S';risk='low';impact_flags=@();criteria=@([pscustomobject]@{id='greeting';kind='file_assertion';path='keep.txt';contains='Accepted';observation='The source contains the accepted greeting.'});provenance=[pscustomobject]@{source='user';reference='publication-offline-fixture';text='Implement the bounded source fixture.'};models=[pscustomobject]@{worker='gpt-5.6-terra';worker_effort='medium';reviewer='gpt-6-astra';reviewer_effort='high'}}
    $state=Start-BFTask $project $request
    $executor={param($run)
        switch($run.attempt.stage){
            'inspect'{$payload='{"complexity":"S","risk":"low","impact_flags":[],"rationale":"Source-only fixture."}'}
            'implement'{Write-P (Join-Path $run.state.worker_path 'keep.txt') "Accepted`r`n";Remove-Item -LiteralPath (Join-Path $run.state.worker_path 'remove.txt');[IO.File]::WriteAllBytes((Join-Path $run.state.worker_path 'новый файл.bin'),[byte[]]@(0,255,13,10,42));$payload='{"changed_files":["keep.txt","remove.txt","новый файл.bin"]}'}
            'verify'{return Invoke-BFVerification $run.state (Join-Path $run.directory 'raw') '' $null}
            default{throw "Unexpected worker stage: $($run.attempt.stage)"}
        }
        [ordered]@{schema_version=1;status='completed';summary='Fixture proposal.';payload_json=$payload}
    }
    $state=Invoke-BFRun $project $state.task_id '' $executor
    $state=Accept-BFTask $project $state.task_id
    Check-P ($state.status -eq 'completed' -and $state.acceptances.Count -eq 1) 'Fixture did not obtain a real controller acceptance.'
    $remote=Join-Path $fixture 'remote with spaces.git'
    [void](Invoke-BFGit $fixture @('init','--bare',$remote))
    $publication=[pscustomobject]@{schema_version=1;publication_id=[guid]::NewGuid().ToString();task_id=$state.task_id;acceptance_sha256=$state.acceptances[-1].sha256;remote=$remote;ref='refs/heads/codex/accepted';auth='none';author=[pscustomobject]@{name='Publication Test';email='test@example.invalid'};message='Publish exact accepted source.';provenance=[pscustomobject]@{source='user';reference='publication-offline-fixture';text='Authorize creation of this exact new branch on the local temporary bare remote.'}}
    $remote=(Get-BFPublicationGitDependencies $publication).remote
    $bad=Clone-P $publication;$bad.PSObject.Properties.Remove('provenance')
    Block-P (Publish-BFTask $project $state.task_id $bad) 'BF_INVALID'
    $bad=Clone-P $publication;$bad.acceptance_sha256='0'*64
    Block-P (Publish-BFTask $project $state.task_id $bad) 'current acceptance'
    $publicationFile=Join-Path $fixture 'publication.json'
    Write-P $publicationFile (Get-BFCanonicalJson $publication)
    $first=if($Executable){& $Executable task publish --project $project --task $state.task_id --input $publicationFile | ConvertFrom-Json}else{Publish-BFTask $project $state.task_id $publication}
    Check-P ($first.status -eq 'published') ('Publication failed: '+(Get-BFCanonicalJson $first))
    $operation=Join-Path (Get-BFTaskDirectory $project $state.task_id) ('publications/'+$publication.publication_id)
    $prepared=Read-BFJson (Join-Path $operation 'prepared.json')
    $registration=Read-BFJson (Join-Path $operation 'request.json')
    Check-P ((Invoke-BFGit $remote @('rev-parse',($publication.ref+'^'))) -ceq $state.baseline) 'Publication lost the exact baseline parent.'
    $remotePaths=(Invoke-BFGit $remote @('-c','core.quotePath=false','ls-tree','-r','--name-only',$publication.ref)) -split '\r?\n'
    Check-P ($remotePaths -contains 'новый файл.bin' -and $remotePaths -notcontains 'remove.txt' -and $remotePaths.Count -eq 3) 'Publication tree lost additions/deletions or gained files.'
    $pushProcesses=@(Get-ChildItem (Join-Path $operation 'push') -Filter process.json -File -Recurse | Where-Object { $_.Directory.Name -like 'push-create-only-*' })
    Check-P ($pushProcesses.Count -eq 1) 'Publication did not retain exactly one push process.'
    $second=if($Executable){& $Executable task publish-resume --project $project --task $state.task_id --input $publicationFile | ConvertFrom-Json}else{Publish-BFTask $project $state.task_id $publication}
    Check-P ($second.status -eq 'published' -and $second.idempotent -and $second.commit_oid -ceq $first.commit_oid) 'Identical publication was not idempotent.'
    Check-P (@(Get-ChildItem (Join-Path $operation 'push') -Filter process.json -File -Recurse | Where-Object { $_.Directory.Name -like 'push-create-only-*' }).Count -eq 1) 'Idempotent publication repeated a push.'
    if($Executable){
        Write-Output "Public CLI publication: $script:checks checks PASS; publish and publish-resume, one real local push, model=0, network=0, DB=0."
        $succeeded=$true
        return
    }
    $changed=Clone-P $publication;$changed.message='Different input with the same UUID.'
    Block-P (Publish-BFTask $project $state.task_id $changed) 'different input'
    $existing=Clone-P $publication;$existing.publication_id=[guid]::NewGuid().ToString()
    Block-P (Publish-BFTask $project $state.task_id $existing) 'absent remote ref'
    $receiptPath=Join-Path $operation 'published.json';$receiptBytes=[IO.File]::ReadAllBytes($receiptPath)
    $tampered=Read-BFJson $receiptPath;$tampered.acceptance_sha256='0'*64
    Write-P $receiptPath (Get-BFCanonicalJson $tampered)
    Block-P (Publish-BFTask $project $state.task_id $publication -ResumeOnly $true) 'receipt'
    [IO.File]::WriteAllBytes($receiptPath,$receiptBytes)
    $source=Join-Path $state.worker_path 'keep.txt';$sourceBytes=[IO.File]::ReadAllBytes($source)
    Write-P $source 'Unaccepted source drift.'
    $stale=Clone-P $publication;$stale.publication_id=[guid]::NewGuid().ToString();$stale.ref='refs/heads/codex/stale'
    Block-P (Publish-BFTask $project $state.task_id $stale) 'stale|accepted|acceptance'
    [IO.File]::WriteAllBytes($source,$sourceBytes)

    # A saved preparation before first dispatch cannot substitute a different
    # existing commit for accepted bytes when the next invocation resumes.
    $unprepared=Clone-P $publication;$unprepared.publication_id=[guid]::NewGuid().ToString();$unprepared.ref='refs/heads/codex/prepared-tamper'
    $script:realPublicationRead=${function:Get-BFPublicationRemote}
    function Get-BFPublicationRemote {param($Repository,$Publication,$Directory);throw 'Fixture preflight read unavailable.'}
    try{Block-P (Publish-BFTask $project $state.task_id $unprepared) 'control read failed'}finally{Set-Item Function:Get-BFPublicationRemote $script:realPublicationRead}
    $unpreparedDirectory=Join-Path (Get-BFTaskDirectory $project $state.task_id) ('publications/'+$unprepared.publication_id)
    $unpreparedPath=Join-Path $unpreparedDirectory 'prepared.json'
    $substituted=Read-BFJson $unpreparedPath;$substituted.git.commit_oid=$state.baseline
    Write-P $unpreparedPath (Get-BFCanonicalJson $substituted)
    Block-P (Publish-BFTask $project $state.task_id $unprepared) 'saved prepared commit differs'
    Check-P (-not(Test-Path (Join-Path $unpreparedDirectory 'intent.json'))) 'Tampered prepared commit reached dispatch intent.'

    $configRetry=Clone-P $publication;$configRetry.publication_id=[guid]::NewGuid().ToString();$configRetry.ref='refs/heads/codex/prepared-config'
    function Get-BFPublicationRemote {param($Repository,$Publication,$Directory);throw 'Fixture preflight read unavailable.'}
    try{Block-P (Publish-BFTask $project $state.task_id $configRetry) 'control read failed'}finally{Set-Item Function:Get-BFPublicationRemote $script:realPublicationRead}
    $configDirectory=Join-Path (Get-BFTaskDirectory $project $state.task_id) ('publications/'+$configRetry.publication_id)
    $oldPreparation=Read-BFJson (Join-Path $configDirectory 'prepared.json')
    $configPath=Join-Path $oldPreparation.git.repository 'config'
    [IO.File]::AppendAllText($configPath,"`n[url `"https://example.invalid/unapproved.git`"]`n`t insteadOf = `"$($publication.remote.Replace('\','/'))`"`n",[Text.UTF8Encoding]::new($false))
    $configResult=Publish-BFTask $project $state.task_id $configRetry
    Check-P ($configResult.status -eq 'published') ('Fresh config retry failed: '+(Get-BFCanonicalJson $configResult))
    $newPreparation=Read-BFJson (Join-Path $configDirectory 'prepared.json')
    Check-P ($newPreparation.git.repository -ine $oldPreparation.git.repository -and $newPreparation.git.commit_oid -ceq $oldPreparation.git.commit_oid) 'Retry reused mutable repository config or changed the accepted commit.'

    # Lose only the first post-push control-read response. The actual push and
    # process receipts remain real; resume must observe them without another push.
    $lost=Clone-P $publication;$lost.publication_id=[guid]::NewGuid().ToString();$lost.ref='refs/heads/codex/lost-response'
    $script:realPublicationRead=${function:Get-BFPublicationRemote};$script:publicationReads=0
    function Get-BFPublicationRemote {
        param($Repository,$Publication,$Directory)
        $script:publicationReads++
        if($script:publicationReads -eq 2){throw 'Fixture lost post-push read response.'}
        & $script:realPublicationRead $Repository $Publication $Directory
    }
    try{$lostResult=Publish-BFTask $project $state.task_id $lost}finally{Set-Item Function:Get-BFPublicationRemote $script:realPublicationRead}
    Block-P $lostResult 'control read failed'
    $lostDirectory=Join-Path (Get-BFTaskDirectory $project $state.task_id) ('publications/'+$lost.publication_id)
    $shared=Get-BFPublicationSharedDirectory $remote $lost.ref;$pendingPath=Join-Path $shared 'pending.json'
    Check-P (Test-Path -LiteralPath $pendingPath) 'Unknown publication released its shared pending marker.'
    $pendingBytes=[IO.File]::ReadAllBytes($pendingPath)
    $other=Clone-P $lost;$other.publication_id=[guid]::NewGuid().ToString()
    Block-P (Publish-BFTask $project $state.task_id $other) 'unresolved publication'
    $pushOperation=@(Get-ChildItem (Join-Path $lostDirectory 'push/processes') -Directory -Filter 'push-create-only-*')[0]
    $identityPath=Join-Path $pushOperation.FullName 'process.json';$identityBytes=[IO.File]::ReadAllBytes($identityPath)
    $invalidIdentity=Read-BFJson $identityPath;$invalidIdentity.start_time_utc='corrupt'
    Write-P $identityPath (Get-BFCanonicalJson $invalidIdentity)
    Block-P (Publish-BFTask $project $state.task_id $lost -ResumeOnly $true) 'identity is unknown'
    Check-P (Test-Path -LiteralPath $pendingPath) 'Malformed launch identity cleared pending.'
    [IO.File]::WriteAllBytes($identityPath,$identityBytes)
    $invalidIdentity=Read-BFJson $identityPath;$invalidIdentity.pid=2147483647
    Write-P $identityPath (Get-BFCanonicalJson $invalidIdentity)
    Block-P (Publish-BFTask $project $state.task_id $lost -ResumeOnly $true) 'identity is unknown'
    [IO.File]::WriteAllBytes($identityPath,$identityBytes)
    $dispatchPath=Join-Path $lostDirectory 'push/dispatch.json';$dispatchBytes=[IO.File]::ReadAllBytes($dispatchPath)
    $invalidDispatch=Read-BFJson $dispatchPath;$invalidDispatch.commit_oid=$state.baseline
    Write-P $dispatchPath (Get-BFCanonicalJson $invalidDispatch)
    Block-P (Publish-BFTask $project $state.task_id $lost -ResumeOnly $true) 'identity is unknown'
    Check-P (Test-Path -LiteralPath $pendingPath) 'Changed dispatch binding cleared pending.'
    [IO.File]::WriteAllBytes($dispatchPath,$dispatchBytes)
    $recovered=Publish-BFTask $project $state.task_id $lost -ResumeOnly $true
    Check-P ($recovered.status -eq 'published' -and -not(Test-Path -LiteralPath $pendingPath)) ('Control-read recovery failed: '+(Get-BFCanonicalJson $recovered))
    Check-P (@(Get-ChildItem (Join-Path $lostDirectory 'push') -Filter process.json -File -Recurse | Where-Object { $_.Directory.Name -like 'push-create-only-*' }).Count -eq 1) 'Recovery repeated the original push.'
    # Crash after durable success but before pending removal is idempotent.
    [IO.File]::WriteAllBytes($pendingPath,$pendingBytes)
    $script:offlineReads=0
    function Get-BFPublicationRemote {param($Repository,$Publication,$Directory);$script:offlineReads++;throw 'Fixture remote unavailable after durable success.'}
    try{$repeated=Publish-BFTask $project $state.task_id $lost -ResumeOnly $true}finally{Set-Item Function:Get-BFPublicationRemote $script:realPublicationRead}
    Check-P ($repeated.status -eq 'published' -and -not(Test-Path -LiteralPath $pendingPath)) 'Durable-success recovery did not finish pending removal.'
    Check-P ($script:offlineReads -eq 0) 'Durable-success cleanup depended on an available remote.'
    $settledPreparation=Read-BFJson (Join-Path $lostDirectory 'prepared.json')
    $settledRepository=$settledPreparation.git.repository
    Check-P ($settledRepository -ieq (Get-BFPublicationRepositoryPath $settledPreparation.git.staging_directory)) 'Object repository escaped its deterministic owned path.'
    $ownedObjects=Join-Path ([Environment]::GetFolderPath([Environment+SpecialFolder]::LocalApplicationData)) 'BSLFlow/publication-objects'
    Check-P ([IO.Path]::GetFullPath($settledRepository).StartsWith([IO.Path]::GetFullPath($ownedObjects)+[IO.Path]::DirectorySeparatorChar,[StringComparison]::OrdinalIgnoreCase)) 'Unsafe object repository move target.'
    $heldRepository=$settledRepository+'.retained-test'
    Move-Item -LiteralPath $settledRepository -Destination $heldRepository
    try{Check-P ((Publish-BFTask $project $state.task_id $lost -ResumeOnly $true).status -eq 'published') 'Historical receipt required the old object repository.'}finally{Move-Item -LiteralPath $heldRepository -Destination $settledRepository}

    # An intent without launch proof can never be retried automatically, even
    # when the remote ref is absent and another publication UUID is supplied.
    $unknown=Clone-P $publication;$unknown.publication_id=[guid]::NewGuid().ToString();$unknown.ref='refs/heads/codex/unknown'
    $script:realPublicationSend=${function:Send-BFPublicationCommit};$script:publicationSends=0
    function Send-BFPublicationCommit {param($Repository,$CommitOid,$Publication,$Directory);$script:publicationSends++;throw 'Fixture lost launch proof.'}
    try{
        Block-P (Publish-BFTask $project $state.task_id $unknown) 'absent or unsettled'
        Block-P (Publish-BFTask $project $state.task_id $unknown) 'absent or unsettled'
        Block-P (Publish-BFTask $project $state.task_id $unknown -ResumeOnly $true) 'absent or unsettled'
        Check-P ($script:publicationSends -eq 1) 'Unknown absent-ref publication repeated a dispatch.'
    }finally{Set-Item Function:Send-BFPublicationCommit $script:realPublicationSend}
    Check-P (Test-Path (Join-Path (Get-BFPublicationSharedDirectory $remote $unknown.ref) 'pending.json')) 'Unknown absent-ref publication lost its shared block.'
    Write-Output "Task publication: $script:checks checks PASS; real local Git, model=0, network=0, DB=0."
    $succeeded=$true
}finally{
    $env:BSL_FLOW_HOST_PATH=$savedHost
    if($succeeded -and -not $KeepFixture){
        $work=[IO.Path]::GetFullPath((Join-Path $PackageRoot 'work'));$safe=[IO.Path]::GetFullPath($fixture)
        if(-not $safe.StartsWith($work+[IO.Path]::DirectorySeparatorChar,[StringComparison]::OrdinalIgnoreCase)){throw 'Unsafe publication fixture cleanup.'}
        Remove-Item -LiteralPath $safe -Recurse -Force
    }elseif(-not $succeeded){Write-Host "Publication fixture retained: $fixture"}
}
