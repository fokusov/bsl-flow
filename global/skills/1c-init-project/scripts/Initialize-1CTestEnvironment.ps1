#Requires -Version 7.0
[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$ProjectPath,
    [Parameter(Mandatory)][string]$DevelopmentDatabasePath,
    [string]$ProfilePath = (Join-Path ([Environment]::GetFolderPath('UserProfile')) '.bsl-flow\workstation.json'),
    [switch]$ConfigureUi,
    [string]$TestClientUser = 'TestClient',
    [string[]]$SourcePath
)

Set-StrictMode -Version Latest
$ErrorActionPreference='Stop'
function Resolve-Absolute([string]$Path,[string]$Name) {
    if ($Path -notmatch '^(?:[A-Za-z]:[\\/]|[\\/]{2}[^\\/]+[\\/][^\\/]+(?:[\\/]|$))') { throw "$Name must be an absolute filesystem path." }
    $full=[IO.Path]::GetFullPath($Path);$root=[IO.Path]::GetPathRoot($full)
    if($full.TrimEnd([char]'\',[char]'/') -eq $root.TrimEnd([char]'\',[char]'/')){$root}else{$full.TrimEnd([char]'\',[char]'/')}
}
function Get-StableId([string]$Text) {
    $sha=[Security.Cryptography.SHA256]::Create(); try {$bytes=$sha.ComputeHash([Text.Encoding]::UTF8.GetBytes($Text.ToLowerInvariant())); ([BitConverter]::ToString($bytes).Replace('-','').Substring(0,12).ToLowerInvariant())} finally {$sha.Dispose()}
}
function Test-FreeTcpPort([int]$Port) {
    $listener=$null
    try {$listener=[Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback,$Port);$listener.Start();$true}catch{$false}finally{if($listener){$listener.Stop()}}
}
function Reserve-ProjectPort([string]$ProjectId,[string]$ProjectPath,[string]$RegistryPath) {
    $mutex=New-Object Threading.Mutex($false,'Local\bsl-flow-testclient-ports')
    if(-not$mutex.WaitOne([TimeSpan]::FromSeconds(10))){$mutex.Dispose();throw 'Timed out waiting for the TestClient port registry.'}
    try{
        $entries=@()
        if(Test-Path -LiteralPath $RegistryPath -PathType Leaf){try{$entries=@((Get-Content -LiteralPath $RegistryPath -Raw -Encoding UTF8|ConvertFrom-Json).entries)}catch{throw 'TestClient port registry is invalid.'}}
        $existing=@($entries|Where-Object{[string]$_.project_id-eq$ProjectId})|Select-Object -First 1
        if ($existing) {
            if (Test-FreeTcpPort ([int]$existing.port)) {return [int]$existing.port}
            throw "Reserved TestClient port $($existing.port) for project $ProjectId is currently in use; refusing to rewrite local runner state."
        }
        $used=@($entries|Where-Object{[string]$_.project_id-ne$ProjectId}|ForEach-Object{[int]$_.port})
        $start=[Convert]::ToInt32($ProjectId.Substring(0,4),16)%1000;$port=$null
        for($i=0;$i -lt 1000;$i++){$candidate=48000+(($start+$i)%1000);if(($used -notcontains $candidate) -and (Test-FreeTcpPort $candidate)){$port=$candidate;break}}
        if(-not$port){throw 'No unreserved free TestClient port was found in 48000..48999.'}
        $entries=@($entries|Where-Object{[string]$_.project_id-ne$ProjectId})+@([ordered]@{project_id=$ProjectId;project_path=$ProjectPath;port=$port})
        $value=[ordered]@{schema_version=1;entries=@($entries|Sort-Object project_id)}
        [IO.Directory]::CreateDirectory((Split-Path -Parent $RegistryPath))|Out-Null
        [IO.File]::WriteAllText($RegistryPath,($value|ConvertTo-Json -Depth 8)+[Environment]::NewLine,[Text.UTF8Encoding]::new($false))
        return $port
    }finally{$mutex.ReleaseMutex();$mutex.Dispose()}
}
$project=Resolve-Absolute $ProjectPath 'ProjectPath'; $db=Resolve-Absolute $DevelopmentDatabasePath 'DevelopmentDatabasePath'; $profileFile=Resolve-Absolute $ProfilePath 'ProfilePath'
if (-not (Test-Path -LiteralPath $project -PathType Container)) { throw "ProjectPath does not exist: $project" }
if (-not (Test-Path -LiteralPath (Join-Path $db '1Cv8.1CD') -PathType Leaf)) { throw "DevelopmentDatabasePath is not a FILE infobase: $db" }
if (-not (Test-Path -LiteralPath $profileFile -PathType Leaf)) { throw "Workstation profile is not configured: $profileFile" }
$profile=Get-Content -LiteralPath $profileFile -Raw -Encoding UTF8|ConvertFrom-Json
if ($profile.schema_version -ne 1 -or -not $profile.enabled -or $profile.profile -ne 'development_database_is_test_target') { throw 'Unsupported or disabled workstation profile.' }
$allowed=@($profile.development_databases|ForEach-Object{Resolve-Absolute ([string]$_.path) 'profile database path'})
if ($allowed -notcontains $db) { throw "Development database is not trusted by the workstation profile: $db" }
$dbItem=Get-Item -LiteralPath $db -Force; $markerItem=Get-Item -LiteralPath (Join-Path $db '1Cv8.1CD') -Force
if (($dbItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0 -or ($markerItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw 'Development database path or marker is a reparse point.' }
$profileDb=@($profile.development_databases|Where-Object{(Resolve-Absolute ([string]$_.path) 'profile database path') -eq $db})[0]
$inventoryScript=Join-Path $PSScriptRoot 'Get-1CTestTooling.ps1'
$inventory=& $inventoryScript -YaxunitDirectory ([string]$profile.catalogs.yaxunit) -VanessaDirectory ([string]$profile.catalogs.vanessa) -TestDatabasePath $db
$projectId=Get-StableId ($project+'|'+$db)
$portRegistry=Join-Path (Split-Path -Parent $profileFile) 'testclient-ports.json'
$port=Reserve-ProjectPort $projectId $project $portRegistry
$states=[ordered]@{}
foreach($artifact in $inventory.artifacts){
    $state=switch($artifact.local_status){'file_found_unverified'{'files_found'}'missing'{'not_configured'}default{'blocked'}}
    $installedState=if($artifact.component -eq 'vanessa'){'not_applicable_external_runner'}else{'unknown'}
    $blockedReason=switch($state){
        'not_configured' {'local_catalog_or_required_artifact_missing'}
        'blocked' {'local_artifact_ambiguous_or_invalid'}
        default {'database_inventory_and_runtime_pilot_required'}
    }
    $nextAction=switch($state){
        'not_configured' {'Configure an explicit local catalog for this workstation; BSL Flow does not silently download third-party test tools.'}
        'blocked' {'Select and verify one exact non-empty compatible artifact; never guess the newest filename.'}
        default {if($artifact.component -eq 'vanessa'){'Verify the external runner and TestClient with a focused pilot.'}else{'Inspect the real extension state in the allowlisted database, then install only a proven-missing required component through an authorized route.'}}
    }
    $states[$artifact.component]=[ordered]@{state=$state;enabled=$false;local_status=$artifact.local_status;installed_in_database=$installedState;pilot='not_run';blocked_reason=$blockedReason;next_action=$nextAction}
}
$localDir=Join-Path $project '.bsl-flow\local';$reportDir=Join-Path $project '.bsl-flow\reports\test-setup';[IO.Directory]::CreateDirectory($localDir)|Out-Null;[IO.Directory]::CreateDirectory($reportDir)|Out-Null
$resolvedSources=@()
if($SourcePath){foreach($path in $SourcePath){$full=Resolve-Absolute $path 'SourcePath';if(-not(Test-Path -LiteralPath $full -PathType Container)){throw "SourcePath does not exist: $full"};$resolvedSources+=$full}}
else{foreach($name in @('src','cf','cfe','edt')){$candidate=Join-Path $project $name;if(Test-Path -LiteralPath $candidate -PathType Container){$resolvedSources+=$candidate}}}
$vanessaArtifact=@($inventory.artifacts|Where-Object component -eq 'vanessa')|Select-Object -First 1
$vanessaCandidate=if($vanessaArtifact -and $vanessaArtifact.local_status -eq 'file_found_unverified'){@($vanessaArtifact.candidates)|Select-Object -First 1}else{$null}
$runtime=[ordered]@{schema_version=1;project_id=$projectId;platform_bin=[string]$profileDb.platform_bin;development_db=$db;test_db=$db;source_paths=$resolvedSources;testclient=[ordered]@{name=('bsl-flow-'+$projectId);port=$port;user=$TestClientUser;password='not_stored'};vanessa_epf=if($vanessaCandidate){[string]$vanessaCandidate.path}else{$null}}
$runtimePath=Join-Path $localDir 'runtime.json';[IO.File]::WriteAllText($runtimePath,($runtime|ConvertTo-Json -Depth 10)+[Environment]::NewLine,[Text.UTF8Encoding]::new($false))
if($ConfigureUi -and $states.vanessa.state -eq 'files_found'){
    $starter=Join-Path (Split-Path -Parent $PSScriptRoot) '..\1c-verify\scripts\New-1CTestStarter.ps1'
    & $starter -ProjectPath $project -TestClientFileDatabasePath $db -TestClientUser $TestClientUser -TestClientPort $port -TestClientName $runtime.testclient.name -VanessaEpfPath $runtime.vanessa_epf -ReportPath (Join-Path $reportDir 'vanessa-junit')|Out-Null
}
$report=[ordered]@{schema_version=1;checked_at_utc=[DateTime]::UtcNow.ToString('o');project_id=$projectId;target=[ordered]@{development_db=$db;test_db=$db;profile_path=$profileFile;marker_length=$markerItem.Length};inventory=$inventory;providers=$states;dependency_policy=[ordered]@{automatic_download_performed=$false;automatic_install_performed=$false;missing_required_provider_result='BLOCKED';installation_requires='explicit catalog, allowlisted database, verified installed state, authorization, and supported runtime route'};runtime_config=$runtimePath;runtime_actions='not_run';status='blocked';blocked_reason='Installed extension inventory and engine/TestClient pilots require an authorized supported runtime route. No provider was enabled.'}
$reportPath=Join-Path $reportDir 'current.json';[IO.File]::WriteAllText($reportPath,($report|ConvertTo-Json -Depth 16)+[Environment]::NewLine,[Text.UTF8Encoding]::new($false))
[pscustomobject]@{status='BLOCKED';project_id=$projectId;development_db=$db;test_db=$db;runtime_config=$runtimePath;report=$reportPath;testclient_port=$port;providers=$states;runtime_mutation_performed=$false}
