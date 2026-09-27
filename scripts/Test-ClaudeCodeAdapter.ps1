#Requires -Version 7.0
[CmdletBinding()]param([string]$PackageRoot)
Set-StrictMode -Version Latest
$ErrorActionPreference='Stop'
if(-not $PackageRoot){$PackageRoot=Split-Path $PSScriptRoot -Parent}
$core=Join-Path $PackageRoot 'global/skills/1c-task/scripts'
foreach($name in @('Task.Storage.ps1','Task.Contracts.ps1','Task.Gates.ps1','Task.Process.ps1','Task.Engine.ps1','Task.Stages.ps1')){. (Join-Path $core $name)}
. (Join-Path $PackageRoot 'global/skills/1c-task/adapters/ClaudeCode.ps1')
$script:checks=0
function Assert-C([bool]$Condition,[string]$Message){if(-not $Condition){throw "ASSERTION FAILED: $Message"};$script:checks++}
function Failure-C([scriptblock]$Action){try{& $Action|Out-Null;return ''}catch{return $_.Exception.Message}}
$fixture=Join-Path $PackageRoot 'scripts/fixtures/claude'
$root=Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-claude-'+[guid]::NewGuid().ToString('N'))
[void][IO.Directory]::CreateDirectory($root)
try {
    $adapter=Get-BFWorkerAdapter 'claude-code'
    Assert-C ($adapter.status -eq 'experimental' -and $adapter.isolation -eq 'permission_rules') 'Claude must remain experimental permission_rules.'
    Assert-C ((Failure-C {Get-BFWorkerAdapter '../claude-code'}) -like 'BF_INVALID:*') 'Registry accepted traversal.'
    foreach($stage in $adapter.stages){
        Assert-BFAdapterStagePolicy $adapter $stage
        $tools=Get-BFClaudeCodeTools $adapter $stage
        Assert-C ('Bash' -cnotin $tools) 'Bash was granted.'
        Assert-C (($stage -eq 'implement') -eq ('Write' -cin $tools)) 'Write permissions do not match stage.'
    }
    $args=Get-BFClaudeCodeArguments $adapter 'inspect' 'claude-fixture-1' 'high' (Join-Path $root 'mcp.json')
    Assert-C ($args[0] -eq '-p' -and $args[2] -eq 'stream-json' -and '--strict-mcp-config' -cin $args -and '--tools' -cin $args) 'Required argument missing.'
    Assert-C ($args[([array]::IndexOf($args,'--setting-sources')+1)] -ceq '') 'Settings were enabled.'
    Assert-C ((Failure-C {Assert-BFAdapterVersion $adapter '9.0.0 (Claude Code)'}) -like 'BF_BLOCKED:*') 'Unknown version passed.'
    $readArgs=@{ExitCode=0;AllowedTools=@('Read','Grep','Glob');DeniedTools=@($adapter.tools.denied);WorkerPath=$root}
    $parsed=Read-BFClaudeCodeEvents -Path (Join-Path $fixture 'success.jsonl') @readArgs
    Assert-C ($parsed.result.status -eq 'completed' -and $parsed.metadata.observed_model -eq 'claude-fixture-1' -and $parsed.metadata.reported_cost_usd -eq 0.001) 'Success metadata was not parsed.'
    foreach($name in @('missing-result','duplicate-init','error','bash-exposed','write-exposed')){
        Assert-C ((Failure-C {Read-BFClaudeCodeEvents -Path (Join-Path $fixture ($name+'.jsonl')) @readArgs}) -like 'BF_BLOCKED:*') "Unsafe fixture accepted: $name"
    }
    Assert-C ((Failure-C {Read-BFClaudeCodeEvents -Path (Join-Path $fixture 'success.jsonl') -MaxBytes 10 @readArgs}) -match 'oversized') 'Oversized output accepted.'
    $readArgs.ExitCode=1
    Assert-C ((Failure-C {Read-BFClaudeCodeEvents -Path (Join-Path $fixture 'success.jsonl') @readArgs}) -match 'process failed') 'Nonzero exit accepted.'
    $readArgs.ExitCode=0
    $torn=Join-Path $root 'torn.jsonl';[IO.File]::WriteAllText($torn,[IO.File]::ReadAllText((Join-Path $fixture 'success.jsonl')).TrimEnd())
    Assert-C ((Failure-C {Read-BFClaudeCodeEvents -Path $torn @readArgs}) -match 'torn') 'Torn stream accepted.'
    Assert-C ((Failure-C {Test-BFClaudeCodeToolPath 'Write' ([pscustomobject]@{file_path='../escape.txt'}) $root @()}) -like 'BF_BLOCKED:*') 'Write escaped worker.'
    Assert-C ((Failure-C {Test-BFClaudeCodeToolPath 'Write' ([pscustomobject]@{file_path='.bsl-flow/forged.json'}) $root @()}) -match 'administrative') 'Write entered controller metadata.'
    $outside=Split-Path $root -Parent
    Assert-C ((Failure-C {Test-BFClaudeCodeToolPath 'Read' ([pscustomobject]@{file_path=$outside}) $root @($outside)}) -eq '') 'Permitted read root failed.'
    $protected=Join-Path $root 'protected';[void][IO.Directory]::CreateDirectory($protected)
    $before=Get-BFTreeDigest @($protected)
    [IO.File]::WriteAllText((Join-Path $protected 'state.json'),'{}')
    Assert-C ($before -cne (Get-BFTreeDigest @($protected))) 'Controller state modification went unnoticed.'
    # Capability probe is stubbed in-process: no Claude, model or network call.
    $script:probeCalls=@();$script:omitFlag=$false
    function Invoke-BFProcess {
        param($Executable,$Arguments,$WorkingDirectory,$OutputDirectory,$TimeoutSeconds,[switch]$CleanEnvironment)
        $script:probeCalls+=,$Arguments
        [void][IO.Directory]::CreateDirectory($OutputDirectory)
        $out=Join-Path $OutputDirectory 'stdout.txt'
        $value=if($Arguments[0] -eq '--version'){'2.1.142 (Claude Code)'}elseif($script:omitFlag){'--print'}else{$adapter.required_help_flags -join ' '}
        [IO.File]::WriteAllText($out,$value)
        return @{exit_code=0;stop_reason=$null;stdout=$out}
    }
    $state=@{worker_path=$root;request=@{execution_profile=@{executable='fixture.exe';executable_sha256=('a'*64)}}}
    $probe=Test-BFClaudeCodeCapability $state (Join-Path $root 'probe') $adapter
    Assert-C ($script:probeCalls.Count -eq 2 -and -not $probe.os_sandbox) 'Capability probe did not remain read-only.'
    $script:omitFlag=$true
    Assert-C ((Failure-C {Test-BFClaudeCodeCapability $state (Join-Path $root 'bad-probe') $adapter}) -match 'required flag') 'Missing capability flag accepted.'
    # Exercise the complete adapter with a local process double, including
    # cached dispatch reuse and integrity failure, without launching a CLI.
    function Get-BFExecutionDependencies { param($State) return @{fixture='fixed'} }
    function Get-BFToolsetPrompt { param($State) return '' }
    & git -C $root init -q
    if($LASTEXITCODE -ne 0){throw 'Could not initialize the offline worker fixture.'}
    $script:omitFlag=$false;$script:workerCalls=0;$script:tamper=$false
    function Invoke-BFProcess {
        param($Executable,$Arguments,$WorkingDirectory,$InputText,$OutputDirectory,$TimeoutSeconds,$Cancelled,$Environment,[switch]$CleanEnvironment,$MaxOutputBytes)
        [void][IO.Directory]::CreateDirectory($OutputDirectory)
        $out=Join-Path $OutputDirectory 'stdout.txt'
        if($Arguments[0] -eq '--version'){$value='2.1.142 (Claude Code)'}
        elseif($Arguments[0] -eq '--help'){$value=$adapter.required_help_flags -join ' '}
        else{
            $script:workerCalls++
            $value=[IO.File]::ReadAllText((Join-Path $fixture 'success.jsonl'))
            if($script:tamper){[IO.File]::WriteAllText((Join-Path $state.project_path ('.bsl-flow/tasks/'+$state.task_id+'/forged.json')),'{}')}
        }
        [IO.File]::WriteAllText($out,$value)
        $process=@{exit_code=0;stop_reason=$null;stdout=$out}
        Write-BFJson (Join-Path $OutputDirectory 'exit.json') $process
        return $process
    }
    $state=@{project_path=$root;worker_path=$root;task_id=[guid]::NewGuid().ToString();request=@{models=@{worker='claude-fixture-1';reviewer='claude-fixture-1';worker_effort='high';reviewer_effort='high'};execution_profile=@{provider='claude-code';executable='fixture.exe';executable_sha256=('a'*64);toolset=@{root=$root}}}}
    $dispatchRoot=Join-Path $root ('.bsl-flow/tasks/'+$state.task_id+'/attempts/one/raw/worker')
    $workerArgs=@{State=$state;Stage='inspect';Prompt='Fixture prompt';Directory=$dispatchRoot;CodexPath='';Cancelled={return $false}}
    $result=Invoke-BFClaudeCodeWorker @workerArgs
    Assert-C ($result.status -eq 'completed' -and $script:workerCalls -eq 1) 'Worker did not materialize a result.'
    $cached=Invoke-BFClaudeCodeWorker @workerArgs
    Assert-C ($cached.status -eq 'completed' -and $script:workerCalls -eq 1) 'Worker repeated a retained model call.'
    $workerArgs.Prompt='Different prompt'
    Assert-C ((Failure-C {Invoke-BFClaudeCodeWorker @workerArgs}) -match 'binding differs') 'Cached dispatch ignored changed prompt.'
    $workerArgs.Directory=Join-Path $root ('.bsl-flow/tasks/'+$state.task_id+'/attempts/two/raw/worker')
    $script:tamper=$true
    Assert-C ((Failure-C {Invoke-BFClaudeCodeWorker @workerArgs}) -match 'controller state modified by worker') 'Worker integrity check accepted forged state.'
    Assert-C ($script:workerCalls -eq 2) 'Blocked worker was unexpectedly retried.'
    Write-Output "CLAUDE_CODE_ADAPTER_OK checks=$script:checks; fixture-only; paid calls=0"
} finally {
    if(-not ([IO.Path]::GetFullPath($root)).StartsWith([IO.Path]::GetFullPath([IO.Path]::GetTempPath()),[StringComparison]::OrdinalIgnoreCase)){throw 'Unsafe fixture cleanup path.'}
    Remove-Item -LiteralPath $root -Recurse -Force
}
