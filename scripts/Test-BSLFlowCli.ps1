#Requires -Version 7.0
[CmdletBinding()]
param([string]$PackageRoot, [string]$Executable)

Set-StrictMode -Version Latest
$ErrorActionPreference='Stop'
if (-not $PackageRoot) { $PackageRoot=Split-Path -Parent $PSScriptRoot }
$PackageRoot=[IO.Path]::GetFullPath($PackageRoot)
if (-not $Executable) { $Executable=Join-Path $PackageRoot 'cli\bin\bsl-flow.exe' }
$Executable=[IO.Path]::GetFullPath($Executable)
if (-not (Test-Path -LiteralPath $Executable -PathType Leaf)) { throw 'Build the Go CLI before this test.' }
$testRoot=Join-Path $PackageRoot ('work\cli-smoke-'+[guid]::NewGuid().ToString('N'))
[void][IO.Directory]::CreateDirectory($testRoot)
$cache=Join-Path $testRoot 'local-cache'
[void][IO.Directory]::CreateDirectory($cache)
$script:checks=0
$utf8=[Text.UTF8Encoding]::new($false)
function Assert-Cli([bool]$Condition,[string]$Message) { if (-not $Condition) { throw "ASSERTION FAILED: $Message" }; $script:checks++ }
function Invoke-CliProcess([string]$File,[string[]]$Arguments) {
    $start=[Diagnostics.ProcessStartInfo]::new()
    $start.FileName=$File
    foreach($argument in $Arguments) { $start.ArgumentList.Add($argument) }
    $start.WorkingDirectory=$testRoot
    $start.UseShellExecute=$false
    $start.CreateNoWindow=$true
    $start.RedirectStandardOutput=$true
    $start.RedirectStandardError=$true
    $start.StandardOutputEncoding=$utf8
    $start.StandardErrorEncoding=$utf8
    $start.Environment['LOCALAPPDATA']=$cache
    $start.Environment['BSL_FLOW_HOST_PATH']=$Executable
    $process=[Diagnostics.Process]::Start($start)
    $stdout=$process.StandardOutput.ReadToEndAsync()
    $stderr=$process.StandardError.ReadToEndAsync()
    if (-not $process.WaitForExit(60000)) { $process.Kill($true); throw 'CLI fixture exceeded 60 seconds.' }
    $result=[pscustomobject]@{code=$process.ExitCode;stdout=$stdout.GetAwaiter().GetResult();stderr=$stderr.GetAwaiter().GetResult()}
    $process.Dispose()
    return $result
}
function Invoke-Cli([string[]]$Arguments) { return Invoke-CliProcess $Executable $Arguments }
function Invoke-Git([string[]]$Arguments) {
    $git=(Get-Command git -ErrorAction Stop).Source
    $result=Invoke-CliProcess $git $Arguments
    if($result.code -ne 0){throw "Git fixture failed: $($result.stderr)"}
}

$version=Invoke-Cli @('version')
Assert-Cli ($version.code -eq 0) 'version exit'
$identity=$version.stdout | ConvertFrom-Json
Assert-Cli ($identity.version -eq [IO.File]::ReadAllText((Join-Path $PackageRoot 'VERSION')).Trim()) 'version matches checkout'
$invalid=Invoke-Cli @('task','force-pass')
Assert-Cli ($invalid.code -eq 2 -and ($invalid.stdout|ConvertFrom-Json).blockers[0] -like 'BF_INVALID:*') 'closed commands and JSON error'
$duplicate=Invoke-Cli @('task','status','--project','.','--project','other','--task',([guid]::NewGuid().ToString()))
Assert-Cli ($duplicate.code -eq 2) 'duplicate option rejection'

# Metacharacters are literal filenames. No shell expression is evaluated.
$project=Join-Path $testRoot 'проект & $literal'
[void][IO.Directory]::CreateDirectory($project)
[IO.File]::WriteAllText((Join-Path $project 'hello.txt'), "Исходное приветствие`n", $utf8)
[IO.File]::WriteAllText((Join-Path $project '.gitignore'), ".bsl-flow/`nopenspec/changes/`n", $utf8)
Invoke-Git @('-C',$project,'init')
Invoke-Git @('-C',$project,'add','.')
Invoke-Git @('-C',$project,'-c','user.name=BSL Flow CLI Test','-c','user.email=test@example.invalid','commit','-m','Fixture')
$task=[guid]::NewGuid().ToString()
$request=[ordered]@{schema_version=1;request_id=$task;prompt='Проверь исходное приветствие.';mode='analysis_only';analysis_goal='analysis';complexity='S';risk='low';impact_flags=@();criteria=@([ordered]@{id='greeting';observation='В исходнике содержится приветствие.';kind='file_assertion';path='hello.txt';contains='приветствие'});provenance=[ordered]@{source='user';reference='CLI fixture';text='Проверь приветствие.'};models=[ordered]@{worker='gpt-6-astra';worker_effort='medium';reviewer='gpt-6-astra';reviewer_effort='high'}}
$inputPath=Join-Path $testRoot 'запрос;$(Write-Output injected).json'
[IO.File]::WriteAllText($inputPath,($request|ConvertTo-Json -Depth 16),$utf8)
$start=Invoke-Cli @('task','start','--project',$project,'--input',$inputPath)
Assert-Cli ($start.code -eq 0) "native task start: $($start.stdout) $($start.stderr)"
$started=$start.stdout|ConvertFrom-Json
Assert-Cli ($started.task_id -eq $task -and $started.status -eq 'ready') 'real registered task'
$status=Invoke-Cli @('task','status','--project',$project,'--task',$task)
Assert-Cli ($status.code -eq 0 -and ($status.stdout|ConvertFrom-Json).task_id -eq $task) 'native status'
$bundleRoot=Join-Path $cache ('BSLFlow\bundles\'+$identity.version+'-'+$identity.bundle_sha256)
$entry=Join-Path $bundleRoot 'global\skills\1c-task\scripts\Invoke-BSLFlowTask.ps1'
$shell=Join-Path ([Environment]::GetFolderPath('System')) 'WindowsPowerShell\v1.0\powershell.exe'
$direct=Invoke-CliProcess $shell @('-NoProfile','-NonInteractive','-File',$entry,'-Action','Status','-ProjectPath',$project,'-TaskId',$task)
Assert-Cli ($direct.code -eq $status.code -and $direct.stdout.Trim() -eq $status.stdout.Trim()) 'direct embedded engine status parity'
$receipt=Get-ChildItem -LiteralPath (Join-Path $project ('.bsl-flow\tasks\'+$task+'\revisions')) -Filter '*.json' | Sort-Object Name | Select-Object -Last 1
$state=[IO.File]::ReadAllText($receipt.FullName)|ConvertFrom-Json
Assert-Cli ($state.request.prompt -eq $request.prompt -and $state.project_path -eq $project) 'UTF-8 request and literal path survive native argv'
Assert-Cli (@($state.policy_files|Where-Object{$_.path -eq $Executable}).Count -eq 1) 'host binary is included in policy identity'
$accept=Invoke-Cli @('task','accept','--project',$project,'--task',$task)
Assert-Cli ($accept.code -eq 11 -and ($accept.stdout|ConvertFrom-Json).status -ne 'completed') 'incomplete gates cannot accept'
$deliver=Invoke-Cli @('task','deliver','--project',$project,'--task',$task)
Assert-Cli ($deliver.code -eq 11 -and ($deliver.stdout|ConvertFrom-Json).status -eq 'blocked') 'unfinished task cannot deliver through native CLI'
$cancel=Invoke-Cli @('task','cancel','--project',$project,'--task',$task)
Assert-Cli ($cancel.code -eq 0 -and ($cancel.stdout|ConvertFrom-Json).status -eq 'cancelled') 'cancel delegates to engine'
$cancelled=$cancel.stdout|ConvertFrom-Json
$queueId=[guid]::NewGuid().ToString()
$queue=[ordered]@{schema_version=1;queue_id=$queueId;task_ids=@($task);poll_seconds=1;max_cycles=1}
$queuePath=Join-Path $testRoot 'очередь.json'
[IO.File]::WriteAllText($queuePath,($queue|ConvertTo-Json -Depth 8),$utf8)
# An unavailable explicit provider also prevents a regression from making a paid
# worker call. A cancelled-only queue must not attempt to resolve or launch it.
$runner=Invoke-Cli @('runner','run','--project',$project,'--input',$queuePath,'--codex',(Join-Path $testRoot 'no-provider.exe'))
Assert-Cli ($runner.code -eq 11) "cancelled-only runner exit: $($runner.stdout) $($runner.stderr)"
$queueResult=$runner.stdout|ConvertFrom-Json
Assert-Cli ($queueResult.status -eq 'waiting' -and $queueResult.queue_id -eq $queueId -and $queueResult.snapshot.cycle -eq 1) 'runner produces one-cycle waiting snapshot'
$queueTask=$queueResult.snapshot.tasks.PSObject.Properties[$task].Value
Assert-Cli ($queueTask.status -eq 'cancelled' -and $queueTask.action -eq 'quiet' -and $queueTask.revision -eq $cancelled.revision) 'cancelled task stays quiet with unchanged revision'
$snapshotPath=Join-Path $project ('.bsl-flow\runner\queue-'+$queueId+'-snapshot.json')
$savedSnapshot=[IO.File]::ReadAllText($snapshotPath)|ConvertFrom-Json
Assert-Cli ($savedSnapshot.queue_id -eq $queueId -and $savedSnapshot.cycle -eq 1 -and $savedSnapshot.tasks.PSObject.Properties[$task].Value.status -eq 'cancelled') 'native runner persists its one-cycle snapshot'
$afterRunner=Invoke-Cli @('task','status','--project',$project,'--task',$task)
$afterRunnerState=$afterRunner.stdout|ConvertFrom-Json
Assert-Cli ($afterRunner.code -eq 0 -and $afterRunnerState.status -eq 'cancelled' -and $afterRunnerState.revision -eq $cancelled.revision) 'runner preserves authoritative cancelled task'
$missing=Join-Path $testRoot 'несуществующий запрос.json'
$unicode=Invoke-Cli @('task','start','--project',$project,'--input',$missing)
Assert-Cli ($unicode.code -ne 0 -and $unicode.stdout.Contains('несуществующий запрос.json')) 'UTF-8 error from PowerShell'

$tamper=Join-Path $bundleRoot 'VERSION'
[IO.File]::WriteAllText($tamper,'tampered',$utf8)
$blocked=Invoke-Cli @('task','status','--project',$project,'--task',$task)
Assert-Cli ($blocked.code -eq 11 -and ($blocked.stdout|ConvertFrom-Json).blockers[0] -match 'tampered bundle') 'tamper blocks before engine launch'
[IO.File]::WriteAllText($tamper,[IO.File]::ReadAllText((Join-Path $PackageRoot 'VERSION')),$utf8)
$extra=Join-Path $bundleRoot 'unexpected'
[void][IO.Directory]::CreateDirectory($extra)
$blocked=Invoke-Cli @('task','status','--project',$project,'--task',$task)
Assert-Cli ($blocked.code -eq 11 -and ($blocked.stdout|ConvertFrom-Json).blockers[0] -match 'unexpected bundle') 'unexpected directory rejection'
[IO.Directory]::Delete($extra)
$junction=Join-Path $bundleRoot 'junction'
[void](New-Item -ItemType Junction -Path $junction -Target $project)
$blocked=Invoke-Cli @('task','status','--project',$project,'--task',$task)
Assert-Cli ($blocked.code -eq 11 -and ($blocked.stdout|ConvertFrom-Json).blockers[0] -match 'reparse point') 'Windows junction rejection'
# Remove only the link, never recurse into its target.
[IO.Directory]::Delete($junction)
$summary=[ordered]@{schema_version=1;status='PASS';checks=$script:checks;executable=$Executable;sha256=(Get-FileHash -LiteralPath $Executable -Algorithm SHA256).Hash.ToLowerInvariant();bundle_sha256=$identity.bundle_sha256;evidence_root=$testRoot;runtime_1c='not_run';model_calls=0}
[IO.File]::WriteAllText((Join-Path $testRoot 'result.json'),($summary|ConvertTo-Json -Depth 8),$utf8)
$summary|ConvertTo-Json -Depth 8
