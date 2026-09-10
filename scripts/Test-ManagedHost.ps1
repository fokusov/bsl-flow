#Requires -Version 7.0
[CmdletBinding()]
param([string]$PackageRoot,[string]$Model='gpt-6-astra',[string]$CodexPath,[string]$OutputRoot,[switch]$FullReview)
Set-StrictMode -Version Latest
$ErrorActionPreference='Stop'
if(-not $PackageRoot){$PackageRoot=Split-Path $PSScriptRoot -Parent}
if(-not $OutputRoot){$OutputRoot=Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-host-'+[guid]::NewGuid().ToString('N'))}
$core=Join-Path $PackageRoot 'global/skills/1c-task/scripts'
foreach($name in @('Task.Storage.ps1','Task.Contracts.ps1','Task.Gates.ps1','Task.Process.ps1','Task.Engine.ps1','Task.Stages.ps1')){. (Join-Path $core $name)}
. (Join-Path $PackageRoot 'global/skills/1c-task/adapters/Codex.ps1')
$project=Join-Path $OutputRoot 'project'
if(Test-Path -LiteralPath $OutputRoot){throw 'Use a new OutputRoot; previous host evidence is immutable.'}
[void][IO.Directory]::CreateDirectory($project)
[void](Invoke-BFGit $project @('init'))
[IO.File]::WriteAllText((Join-Path $project 'hello.txt'),"Example`n",(New-Object Text.UTF8Encoding($false)))
[IO.File]::WriteAllText((Join-Path $project '.gitignore'),".bsl-flow/`nopenspec/changes/`n",(New-Object Text.UTF8Encoding($false)))
[void](Invoke-BFGit $project @('add','.'))
[void](Invoke-BFGit $project @('-c','user.name=BSL Flow Host Test','-c','user.email=test@example.invalid','commit','-m','Source-only pilot fixture'))
$request=[ordered]@{schema_version=1;request_id=[guid]::NewGuid().ToString();prompt='In hello.txt replace Example with exactly BSL Flow managed greeting, followed by a newline. This is a source-only text-file task. Do not change any other file, write or run tests, initialize a 1C project, call databases, install tools or use networks. The controller will directly verify the exact text.';mode='implement';analysis_goal='analysis';complexity='S';risk='low';impact_flags=@();criteria=@([ordered]@{id='greeting';observation='hello.txt contains the exact requested greeting.';kind='file_assertion';path='hello.txt';contains='BSL Flow managed greeting'});provenance=[ordered]@{source='user';reference='authorized-framework-implementation-host-pilot';text='Implement and verify the SDLC framework on a harmless source-only fixture.'};models=[ordered]@{worker=$Model;worker_effort='medium';reviewer=$Model;reviewer_effort='high'};timeout_seconds=600}
if($FullReview){
    $request.complexity='M'
    $request.require_code_review=$true
    $request.timeout_seconds=1800
    $request.prompt='This fixture deliberately requires the full M route and independent code review to verify the framework. Preserve M classification even though the edit is tiny. '+$request.prompt
}
Write-BFJson -Path (Join-Path $OutputRoot 'request.json') -Value $request
$task=Start-BFTask $project $request
$task=Invoke-BFRun $project $task.task_id $CodexPath
$next=Get-BFNext $task
$envelope=New-BFEnvelope $task $next.action @($next.blockers) $next.stage
Write-BFJson -Path (Join-Path $OutputRoot 'result.json') -Value $envelope
if($task.status -ne 'completed'){throw ('Host pilot did not pass: '+(Get-BFCanonicalJson $envelope))}
$stages=@($task.evidence|ForEach-Object{$_.stage})
$expectedRoute=if($FullReview){'inspect,spec,spec_review,implement,code_review,verify'}else{'inspect,implement,verify'}
if(($stages -join ',') -ne $expectedRoute){throw "Host pilot did not follow the expected route: $expectedRoute"}
if([IO.File]::ReadAllText((Join-Path $task.worker_path 'hello.txt')).Trim() -cne 'BSL Flow managed greeting'){throw 'Host pilot final bytes failed independent assertion.'}
$receipt=Read-BFJson $envelope.acceptance.path
if($receipt.source_manifest.sha256 -ne (Get-BFSourceManifest $task).sha256){throw 'Host pilot acceptance differs from the actual final source manifest.'}
Write-Host "Managed host source-only pilot PASS. Evidence: $OutputRoot"
