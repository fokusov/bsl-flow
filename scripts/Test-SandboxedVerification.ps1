[CmdletBinding()]param([string]$PackageRoot,[string]$CodexPath,[Parameter(Mandatory)][string]$OutputRoot)
Set-StrictMode -Version Latest;$ErrorActionPreference='Stop'
if(-not $PackageRoot){$PackageRoot=Split-Path $PSScriptRoot -Parent}
if(Test-Path -LiteralPath $OutputRoot){throw 'Use a new OutputRoot; retain previous host evidence.'}
$core=Join-Path $PackageRoot 'global/skills/1c-task/scripts'
foreach($name in @('Task.Storage.ps1','Task.Contracts.ps1','Task.Gates.ps1','Task.Process.ps1','Task.Engine.ps1','Task.Stages.ps1')){. (Join-Path $core $name)}
. (Join-Path $PackageRoot 'global/skills/1c-task/adapters/Codex.ps1')
$project=Join-Path $OutputRoot 'project';[void][IO.Directory]::CreateDirectory($project)
$utf8=[Text.UTF8Encoding]::new($false)
[void](Invoke-BFGit $project @('init'))
[IO.File]::WriteAllText((Join-Path $project '.gitignore'),".bsl-flow/`nopenspec/changes/`n",$utf8)
[IO.File]::WriteAllText((Join-Path $project 'hello.txt'),'Example greeting',$utf8)
$check=@'
param([string]$Report,[string]$Sentinel,[switch]$OmitReport)
$ErrorActionPreference='Stop'
if((Get-Content -Raw -LiteralPath 'hello.txt') -cne 'Example greeting'){throw 'Wrong source.'}
$denied=$false
try{Set-Content -LiteralPath $Sentinel -Value 'forbidden' -ErrorAction Stop}catch [UnauthorizedAccessException]{$denied=$true}
if(-not $denied){throw 'Test process could write controller state.'}
if(-not $OmitReport){
    New-Item -ItemType Directory -Path (Split-Path $Report -Parent) -Force|Out-Null
    Set-Content -LiteralPath $Report -Value '<testsuite tests="2" failures="0"><testcase name="source"/><testcase name="controller-write-denied"/></testsuite>' -Encoding UTF8
}
'@
[IO.File]::WriteAllText((Join-Path $project 'check.ps1'),$check,$utf8)
[void](Invoke-BFGit $project @('add','.'));[void](Invoke-BFGit $project @('-c','user.name=BSL Flow Test','-c','user.email=test@example.invalid','commit','-m','Sandbox test fixture'))
$sentinel=Join-Path $OutputRoot 'controller-sentinel.txt';[IO.File]::WriteAllText($sentinel,'controller',$utf8)
$shell=Join-Path $env:SystemRoot 'System32/WindowsPowerShell/v1.0/powershell.exe'
$request=[pscustomobject]@{schema_version=1;request_id=[guid]::NewGuid().ToString();prompt='Check the existing greeting.';mode='implement';analysis_goal='analysis';complexity='S';risk='low';impact_flags=@();criteria=@([pscustomobject]@{id='sandbox';kind='unit';observation='Exact greeting and denied controller write.';executable=$shell;arguments=@('-NoProfile','-File','check.ps1','-Report','.bsl-flow-worker/result.xml','-Sentinel',$sentinel);report='.bsl-flow-worker/result.xml';expected_tests=@('source','controller-write-denied')});provenance=[pscustomobject]@{source='user';reference='framework-source-test-isolation';text='Verify framework test sandbox without database operations.'};models=[pscustomobject]@{worker='gpt-6-astra';worker_effort='medium';reviewer='gpt-6-astra';reviewer_effort='high'}}
$task=Start-BFTask $project $request;$CodexPath=Resolve-BFCodex $CodexPath
$capabilities=Test-BFCodexCapability $task $CodexPath (Join-Path $OutputRoot 'capabilities')
Write-BFJson -Path (Join-Path $OutputRoot 'capability-result.json') -Value $capabilities
$before=Get-BFSourceManifest $task
foreach($attempt in @('first','repeat')){
    $result=Invoke-BFVerification $task (Join-Path $OutputRoot $attempt) $CodexPath $null
    if($result.status -ne 'completed'){throw 'Sandbox verification did not finish.'}
    if([IO.File]::ReadAllText($sentinel) -cne 'controller'){throw 'Protected sentinel changed.'}
}
if(-not(Test-Path -LiteralPath (Join-Path $OutputRoot 'repeat/sandbox/preexisting.junit.xml'))){throw 'Repeat did not preserve the previous report.'}
# Old PASS is removed before the process; zero exit with no new report must block.
$task.request.criteria[0].arguments+=,'-OmitReport'
$failure='';try{Invoke-BFVerification $task (Join-Path $OutputRoot 'missing') $CodexPath $null|Out-Null}catch{$failure=$_.Exception.Message}
if($failure -notmatch 'produced no original JUnit'){throw "Expected missing-report BLOCKED, observed: $failure"}
if((Get-BFSourceManifest $task).sha256 -ne $before.sha256){throw 'Read-only verification changed source bytes.'}
Write-BFJson -Path (Join-Path $OutputRoot 'result.json') -Value ([ordered]@{schema_version=1;verdict='PASS';scope='source-only-test-sandbox';checks=@('fresh exact JUnit','source unchanged','controller write denied','repeat preserved old report','missing new report blocked');model_calls=0;runtime_1c='NOT_RUN'})
Write-Host "Sandboxed verification: 5 checks PASS. Evidence: $OutputRoot"
