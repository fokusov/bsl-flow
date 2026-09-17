#Requires -Version 7.0
Set-StrictMode -Version Latest
$ErrorActionPreference='Stop'
$root=Split-Path $PSScriptRoot -Parent
. (Join-Path $root 'global/skills/1c-task/scripts/Task.Storage.ps1')
. (Join-Path $root 'global/skills/1c-task/scripts/Task.Contracts.ps1')
. (Join-Path $root 'global/skills/1c-task/adapters/OpenCode.ps1')

$testRoot=Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-opencode-worker-'+[guid]::NewGuid().ToString('N'))
$checks=0;$script:dispatches=0;$script:dependencyMarker='dependency-v1'
function Get-BFExecutionDependencies { param($State) return [ordered]@{fixture=$script:dependencyMarker} }
function Get-BFExecutionPermissionProfile { param($State,$Scratch,$Config,[bool]$Write) return 'permissions.bsl_execution={filesystem={}}' }
function Invoke-BFProcess { $script:dispatches++;throw 'Unexpected OpenCode process dispatch.' }
function Assert-True([bool]$Value,[string]$Message){if(-not $Value){throw $Message};$script:checks++}
function Assert-Blocked([scriptblock]$Action){try{& $Action;throw 'Expected BF_BLOCKED.'}catch{if($_.Exception.Message -notmatch 'BF_BLOCKED'){throw};$script:checks++}}
function New-FixtureState([string]$Project,[string]$Worker,[string]$TaskId,[string]$Executable){
    return [pscustomobject]@{project_path=$Project;worker_path=$Worker;task_id=$TaskId;request=[pscustomobject]@{execution_profile=[pscustomobject]@{provider='opencode';sandbox=[pscustomobject]@{executable=$Executable};toolset=[pscustomobject]@{name='cc-1c-skills';root=$Worker}};models=[pscustomobject]@{worker='deepseek/deepseek-v4-flash';worker_effort=$null;reviewer='deepseek/deepseek-v4-flash';reviewer_effort=$null};timeout_seconds=30}}
}
try {
    $project=Join-Path $testRoot 'project';$worker=Join-Path $project 'worker';$attempt=Join-Path $testRoot 'attempt';$codex=Join-Path $PSHOME 'pwsh.exe';$prompt='fixture prompt';$taskId='fixture-task'
    [void][IO.Directory]::CreateDirectory($worker);[void][IO.Directory]::CreateDirectory($attempt)
    $state=New-FixtureState $project $worker $taskId $codex
    $binding=[ordered]@{dependencies=(Get-BFExecutionDependencies $state);stage='implement';prompt_sha256=Get-BFHash $prompt;worker_path=$worker}
    Write-BFJson (Join-Path $attempt 'binding.json') ([ordered]@{sha256=(Get-BFHash $binding);binding=$binding;permission_sha256='fixture'})
    $rawPath=Join-Path $attempt 'stdout.jsonl';Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'fixtures/opencode/read-write.jsonl') -Destination $rawPath
    Write-BFJson (Join-Path $attempt 'exit.json') ([ordered]@{exit_code=0;stop_reason=$null;stdout=$rawPath})
    $first=Invoke-BFOpenCodeWorker $state 'implement' $prompt $attempt $codex $null
    Assert-True ($first.status -ceq 'completed' -and (Test-Path -LiteralPath (Join-Path $attempt 'host-result.json'))) 'First cached OpenCode parse did not publish host result.'
    $modelPath=Join-Path $attempt 'model-result.json'
    Assert-True ((Test-Path -LiteralPath $modelPath) -and (Get-BFHash (Read-BFJson $modelPath)) -ceq (Get-BFHash $first)) 'Read-only controller recovery result is missing or differs.'
    $second=Invoke-BFOpenCodeWorker $state 'implement' $prompt $attempt $codex $null
    Assert-True ($second.status -ceq 'completed' -and $script:dispatches -eq 0) 'Completed OpenCode cache dispatched a process.'
    Assert-Blocked {Invoke-BFOpenCodeWorker $state 'implement' ($prompt+' changed') $attempt $codex $null|Out-Null}
    $script:dependencyMarker='dependency-v2';Assert-Blocked {Invoke-BFOpenCodeWorker $state 'implement' $prompt $attempt $codex $null|Out-Null};$script:dependencyMarker='dependency-v1'
    $savedModel=$state.request.models.worker;$state.request.models.worker='deepseek/other';Assert-Blocked {Invoke-BFOpenCodeWorker $state 'implement' $prompt $attempt $codex $null|Out-Null};$state.request.models.worker=$savedModel
    $modelText=[IO.File]::ReadAllText($modelPath);[IO.File]::WriteAllText($modelPath,$modelText.Replace('"completed"','"failed"'))
    Assert-Blocked {Invoke-BFOpenCodeWorker $state 'implement' $prompt $attempt $codex $null|Out-Null}
    [IO.File]::WriteAllText($modelPath,$modelText)
    $raw=[IO.File]::ReadAllText($rawPath);[IO.File]::WriteAllText($rawPath,$raw.Replace('"cost":0.3','"cost":0.4'),[Text.UTF8Encoding]::new($false));Assert-Blocked {Invoke-BFOpenCodeWorker $state 'implement' $prompt $attempt $codex $null|Out-Null}
    $partial=Join-Path $testRoot 'partial';[void][IO.Directory]::CreateDirectory($partial);Assert-Blocked {Invoke-BFOpenCodeWorker $state 'implement' $prompt $partial $codex $null|Out-Null}
    Assert-True ($script:dispatches -eq 0) 'A cache/rejection path dispatched a process.'
    Write-Output ('OPEN_CODE_WORKER_OK checks='+$checks)
} finally {if(Test-Path -LiteralPath $testRoot){Remove-Item -LiteralPath $testRoot -Recurse -Force}}
