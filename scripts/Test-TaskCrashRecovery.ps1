[CmdletBinding()]param([string]$PackageRoot)
Set-StrictMode -Version Latest;$ErrorActionPreference='Stop'
if(-not $PackageRoot){$PackageRoot=Split-Path $PSScriptRoot -Parent}
$core=Join-Path $PackageRoot 'global/skills/1c-task/scripts'
foreach($name in @('Task.Storage.ps1','Task.Contracts.ps1','Task.Gates.ps1','Task.Process.ps1','Task.Engine.ps1','Task.Stages.ps1')){. (Join-Path $core $name)}
$script:checks=0
function Assert-C([bool]$Value,[string]$Message){if(-not $Value){throw "ASSERTION FAILED: $Message"};$script:checks++}
function Failure-C([scriptblock]$Body){try{& $Body|Out-Null;return ''}catch{return $_.Exception.Message}}
function Write-C([string]$Path,[string]$Text){[void][IO.Directory]::CreateDirectory((Split-Path $Path -Parent));[IO.File]::WriteAllText($Path,$Text,[Text.UTF8Encoding]::new($false))}
$testRoot=Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-crash-'+[guid]::NewGuid().ToString('N'))
$project=Join-Path $testRoot 'project';[void][IO.Directory]::CreateDirectory($project)
[void](Invoke-BFGit $project @('init'))
Write-C (Join-Path $project 'hello.txt') 'Before'
Write-C (Join-Path $project '.gitignore') ".bsl-flow/`nopenspec/changes/`n"
[void](Invoke-BFGit $project @('add','.'));[void](Invoke-BFGit $project @('-c','user.name=BSL Flow Test','-c','user.email=test@example.invalid','commit','-m','Fixture'))
$request=[pscustomobject]@{schema_version=1;request_id=[guid]::NewGuid().ToString();prompt='Change the greeting.';mode='implement';analysis_goal='analysis';complexity='S';risk='low';impact_flags=@();criteria=@([pscustomobject]@{id='greeting';kind='file_assertion';observation='Updated source.';path='hello.txt';contains='After'});provenance=[pscustomobject]@{source='user';reference='crash-fixture';text='Implement greeting.'};models=[pscustomobject]@{worker='gpt-6-astra';worker_effort='medium';reviewer='gpt-6-astra';reviewer_effort='high'}}
$task=Start-BFTask $project $request
$inspect=New-BFAttempt $project $task.task_id ''
$executor={param($r)[ordered]@{schema_version=1;status='completed';summary='Greeting inspected.';payload_json='{"complexity":"S","risk":"low","impact_flags":[],"rationale":"Source-only greeting."}'}}
$task=Invoke-BFStage $inspect '' $executor
# A separate controller dies after a source write, before creating any terminal receipt.
$scriptPath=Join-Path $testRoot 'interrupted.ps1'
$body=@'
param($Core,$Project,$Task)
$ErrorActionPreference='Stop'
foreach($name in @('Task.Storage.ps1','Task.Contracts.ps1','Task.Gates.ps1','Task.Process.ps1','Task.Engine.ps1','Task.Stages.ps1')){. (Join-Path $Core $name)}
$run=New-BFAttempt $Project $Task ''
[IO.File]::WriteAllText((Join-Path $run.state.worker_path 'hello.txt'),'After one write',[Text.UTF8Encoding]::new($false))
exit 42
'@
Write-C $scriptPath $body
$shell=Join-Path $env:SystemRoot 'System32/WindowsPowerShell/v1.0/powershell.exe'
$process=Invoke-BFProcess $shell @('-NoProfile','-File',$scriptPath,'-Core',$core,'-Project',$project,'-Task',$task.task_id) $testRoot '' (Join-Path $testRoot 'controller') 30 $null
Assert-C ($process.exit_code -eq 42) 'Fault fixture did not terminate at its intended checkpoint.'
$task=Read-BFTask $project $task.task_id;$attemptId=$task.active_attempt
Assert-C ($null -ne $attemptId -and $task.stage -eq 'implement') 'Interrupted source attempt was not retained.'
Assert-C ((Failure-C {Resume-BFAttempt $project $task.task_id}) -match 'no terminal receipt') 'Missing mutation receipt caused an implicit rerun.'
Assert-C ([IO.File]::ReadAllText((Join-Path $task.worker_path 'hello.txt')) -ceq 'After one write') 'Resume repeated or changed the write.'
$manifest=Get-BFSourceManifest $task
$event=[pscustomobject]@{schema_version=1;input_event_id=[guid]::NewGuid().ToString();expected_revision=$task.revision;kind='recovery';provenance=$request.provenance;resolution=[pscustomobject]@{attempt_id=$attemptId;scope='source_only';source_sha256=$manifest.sha256;observation='Inspected retained source: exactly one greeting update; no external target.'}}
$task=Update-BFTask $project $task.task_id $event
Assert-C ($null -eq $task.active_attempt -and $null -eq $task.unresolved_effect) 'Exact control read did not close the orphan.'
Assert-C (@($task.evidence|Where-Object{$_.attempt_id -eq $attemptId}).Count -eq 0) 'Recovery fabricated a PASS for interrupted work.'
Assert-C ((Update-BFTask $project $task.task_id $event).revision -eq $task.revision) 'Recovery input is not idempotent.'
Assert-C ([IO.File]::ReadAllText((Join-Path $task.worker_path 'hello.txt')) -ceq 'After one write') 'Control read repeated a source mutation.'

# Cancellation stops a stored child even without an active original controller.
$childInfo=New-Object Diagnostics.ProcessStartInfo
$childInfo.FileName=$shell;$childInfo.Arguments='-NoProfile -Command "Start-Sleep -Seconds 60"';$childInfo.UseShellExecute=$false;$childInfo.CreateNoWindow=$true
$child=[Diagnostics.Process]::Start($childInfo)
try {
    $run=New-BFAttempt $project $task.task_id ''
    $identity=[ordered]@{pid=$child.Id;start_time_utc=$child.StartTime.ToUniversalTime().ToString('o');executable=$shell;arguments_sha256=('0'*64)}
    Write-BFJson -Path (Join-Path $run.directory 'raw/worker/process.json') -Value $identity
    $task=Cancel-BFTask $project $task.task_id
    Assert-C ($task.status -eq 'cancelled' -and $child.WaitForExit(3000)) 'Cancel left the exact stored child alive.'
    Assert-C ((Cancel-BFTask $project $task.task_id).revision -eq $task.revision) 'Repeated Cancel changed the task unnecessarily.'
    $event.input_event_id=[guid]::NewGuid().ToString();$event.expected_revision=$task.revision;$event.resolution.attempt_id=$task.active_attempt;$event.resolution.source_sha256=(Get-BFSourceManifest $task).sha256
    Assert-C ((Failure-C {Update-BFTask $project $task.task_id $event}) -match 'controller is still running') 'Recovery abandoned a still-live controller.'
    # Fault injection changes only the saved controller identity to a dead PID.
    $startPath=Join-Path $run.directory 'start.json';$start=Read-BFJson $startPath;$start.controller_process.pid=2147483647
    Write-BFJson -Path $startPath -Value $start -Replace
    $task=Update-BFTask $project $task.task_id $event
    Assert-C ($task.status -eq 'cancelled' -and (Get-BFNext $task).action -eq 'cancelled') 'Recovery silently removed user cancellation.'
    $auth=[pscustomobject]@{schema_version=1;input_event_id=[guid]::NewGuid().ToString();expected_revision=$task.revision;kind='authorization';resume=$true;provenance=$request.provenance}
    $task=Update-BFTask $project $task.task_id $auth
    Assert-C ($task.status -eq 'ready' -and (Get-BFNext $task).stage -eq 'implement') 'Explicit resume did not choose the missing implementation gate.'
} finally {if(-not $child.HasExited){$child.Kill();[void]$child.WaitForExit(3000)};$child.Dispose()}
# A blocked terminal receipt does not prove that an unsuccessfully stopped child died.
$child=[Diagnostics.Process]::Start($childInfo)
try {
    $executor={param($r)Write-BFJson -Path (Join-Path $r.directory 'raw/worker/process.json') -Value ([ordered]@{pid=$child.Id;start_time_utc=$child.StartTime.ToUniversalTime().ToString('o')});throw 'BF_BLOCKED: simulated owned process did not terminate'}
    $task=Invoke-BFStage (New-BFAttempt $project $task.task_id '') '' $executor
    Assert-C ($null -eq $task.active_attempt -and $null -ne $task.unresolved_effect) 'Blocked terminal did not retain the unknown effect.'
    $event.input_event_id=[guid]::NewGuid().ToString();$event.expected_revision=$task.revision;$event.resolution.attempt_id=$task.unresolved_effect.attempt_id;$event.resolution.source_sha256=(Get-BFSourceManifest $task).sha256
    Assert-C ((Failure-C {Update-BFTask $project $task.task_id $event}) -match 'owned child is still running') 'Terminal unknown effect bypassed child liveness check.'
    $task=Cancel-BFTask $project $task.task_id
    Assert-C ($child.WaitForExit(3000)) 'Cancel did not stop child of an unresolved terminal effect.'
    $event.expected_revision=$task.revision;$task=Update-BFTask $project $task.task_id $event
    Assert-C ($task.status -eq 'cancelled' -and $null -eq $task.unresolved_effect) 'Stopped terminal effect did not resolve while preserving cancellation.'
} finally {if(-not $child.HasExited){$child.Kill();[void]$child.WaitForExit(3000)};$child.Dispose()}
Write-Host "Task crash recovery: $script:checks checks PASS. Fixture retained at $testRoot"
