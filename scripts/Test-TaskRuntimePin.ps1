#Requires -Version 7.0
# BFI-003 offline contract: the pinned cc-1c-skills interpreter is the only
# runtime launched before dispatch. No model, sandbox or database process runs.
[CmdletBinding()]param([string]$PackageRoot)
Set-StrictMode -Version Latest
$ErrorActionPreference='Stop'
if(-not $PackageRoot){$PackageRoot=Split-Path -Parent $PSScriptRoot}
$core=Join-Path $PackageRoot 'global/skills/1c-task/scripts'
foreach($name in @('Task.Storage.ps1','Task.Contracts.ps1','Task.Process.ps1','Task.Gates.ps1','Task.Engine.ps1','Task.Stages.ps1')){. (Join-Path $core $name)}
$script:checks=0
function Assert-R([bool]$Condition,[string]$Message){if(-not $Condition){throw "ASSERTION FAILED: $Message"};$script:checks++}
function Failure-R([scriptblock]$Action){try{& $Action|Out-Null;return ''}catch{return $_.Exception.Message}}
function Write-R([string]$Path,[string]$Text){[void][IO.Directory]::CreateDirectory((Split-Path $Path -Parent));[IO.File]::WriteAllText($Path,$Text,[Text.UTF8Encoding]::new($false))}
$testRoot=Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-runtime-pin-'+[guid]::NewGuid().ToString('N'))
[void][IO.Directory]::CreateDirectory($testRoot)
$script:probeExe=$null;$script:probeArgs=$null;$script:probeJson=''
function Invoke-BFProcess {
    param($Executable,$Arguments,$WorkingDirectory,$InputText,$OutputDirectory,$TimeoutSeconds,$Cancelled,$Environment,[switch]$CleanEnvironment,$MaxOutputBytes)
    $script:probeExe=$Executable;$script:probeArgs=$Arguments
    $stdout=Join-Path $OutputDirectory 'stdout.txt'
    [IO.File]::WriteAllText($stdout,$script:probeJson,[Text.UTF8Encoding]::new($false))
    [ordered]@{exit_code=0;stop_reason=$null;stdout=$stdout;stderr=(Join-Path $OutputDirectory 'stderr.txt');executable=$Executable}
}
function New-ProbeJson([string]$Exe,[string]$Version,[hashtable]$Packages){
    [ordered]@{sys_executable=$Exe;version=$Version;packages=$Packages}|ConvertTo-Json -Compress
}
function New-PinState([string]$Worker,[string]$Exe,[string]$Sha,[string]$Provider){
    $profile=[pscustomobject]@{provider=$Provider;executable=$script:providerExe;executable_sha256=(Get-BFFileHash $script:providerExe);sandbox=[pscustomobject]@{executable=$script:providerExe;sha256=(Get-BFFileHash $script:providerExe)};toolset=[pscustomobject]@{name='cc-1c-skills';root=(Join-Path $testRoot 'toolset');sha256=('a'*64)};runtime=[pscustomobject]@{executable=$Exe;sha256=$Sha;version='3.12.14';packages=@([pscustomobject]@{name='lxml';version='6.1.1'})};denied_read_roots=@((Join-Path $testRoot 'private'))}
    [pscustomobject]@{project_path=(Join-Path $testRoot 'project');worker_path=$Worker;task_id=([guid]::NewGuid().ToString());request=[pscustomobject]@{execution_profile=$profile;models=[pscustomobject]@{worker='w';reviewer='r'}}}
}
try {
    $worker=Join-Path $testRoot 'worker';[void][IO.Directory]::CreateDirectory($worker)
    # Runtime evidence is written below the legacy task path, and the storage
    # fence verifies that path lives under a Git worktree root; give the
    # fixture project a repository like every real task project has.
    [void][IO.Directory]::CreateDirectory((Join-Path $testRoot 'project'))
    $null=& git -C (Join-Path $testRoot 'project') init 2>&1
    if($LASTEXITCODE -ne 0){throw 'runtime pin fixture repository was not created.'}
    $script:providerExe=Join-Path $testRoot 'provider.exe';Write-R $script:providerExe 'provider fixture'
    $python=Join-Path $testRoot 'python.exe';Write-R $python 'python fixture'
    $pythonSha=Get-BFFileHash $python
    $decoy=Join-Path $testRoot 'decoy-python.exe';Write-R $decoy 'decoy fixture'

    # 1. Correct pinned runtime passes and the exact interpreter is launched.
    $state=New-PinState $worker $python $pythonSha 'opencode'
    $script:probeJson=New-ProbeJson $python '3.12.14' @{lxml='6.1.1'}
    $evidence=Test-BFRuntimePreflight $state (Join-Path $testRoot 'opencode-run')
    Assert-R ($null -ne $evidence -and $evidence.observed.version -ceq '3.12.14') 'Correct runtime preflight did not pass.'
    Assert-R ($script:probeExe -ceq $python -and $script:probeArgs[0] -eq '-I') 'Pinned interpreter was not the launch target.'
    $runtimeDir=Join-Path $state.project_path ('.bsl-flow/tasks/'+$state.task_id+'/runtime')
    $evidencePath=@(Get-ChildItem -LiteralPath $runtimeDir -Filter 'preflight-*.json' -File | Select-Object -First 1).FullName
    Assert-R (-not [string]::IsNullOrEmpty($evidencePath)) 'Runtime preflight evidence was not retained.'
    Assert-R (-not (Test-Path -LiteralPath (Join-Path $state.project_path ('.bsl-flow/tasks/'+$state.task_id+'/attempts')))) 'Runtime preflight created provider dispatch state.'
    Assert-R ([IO.File]::ReadAllText($evidencePath).Contains($pythonSha)) 'Declared runtime identity missing from evidence.'
    Assert-R ((Get-BFHash (Read-BFJson $evidencePath)) -ceq (Get-BFHash $evidence)) 'Retained runtime evidence differs from the result.'
    $repeated=Test-BFRuntimePreflight $state (Join-Path $testRoot 'opencode-run-2')
    Assert-R ((Get-BFHash $repeated.declared) -ceq (Get-BFHash $evidence.declared)) 'A second preflight in one task was not repeatable.'

    # 2. Same contract for the Codex provider on the same pinned runtime.
    $codexState=New-PinState $worker $python $pythonSha 'codex'
    $script:probeJson=New-ProbeJson $python '3.12.14' @{lxml='6.1.1'}
    $codexEvidence=Test-BFRuntimePreflight $codexState (Join-Path $testRoot 'codex-run')
    Assert-R ($null -ne $codexEvidence -and $script:probeExe -ceq $python) 'Codex provider received a different runtime contract.'

    # 3. Missing runtime file blocks before any launch.
    $missing=Join-Path $testRoot 'absent-python.exe'
    $missingState=New-PinState $worker $missing ('a'*64) 'opencode'
    Assert-R ((Failure-R {Test-BFRuntimePreflight $missingState (Join-Path $testRoot 'missing')}) -match 'does not exist|BFSafePath|File') 'Missing runtime file was not blocked.'
    Assert-R ($script:probeExe -ceq $python) 'Missing runtime still launched a process.'

    # 4. Executable byte drift blocks even when a decoy interpreter is present.
    $driftState=New-PinState $worker $python ('b'*64) 'opencode'
    Assert-R ((Failure-R {Test-BFRuntimePreflight $driftState (Join-Path $testRoot 'drift')}) -match 'changed before preflight') 'Runtime byte drift was not blocked.'
    Assert-R ($script:probeExe -ceq $python) 'Runtime drift launched a process.'
    Assert-R (Test-Path -LiteralPath $decoy) 'Decoy fixture missing.'

    # 5. Version and package drift are fail-closed.
    $script:probeJson=New-ProbeJson $python '3.12.15' @{lxml='6.1.1'}
    Assert-R ((Failure-R {Test-BFRuntimePreflight $state (Join-Path $testRoot 'version-drift')}) -match 'version differs') 'Wrong Python version accepted.'
    $script:probeJson=New-ProbeJson $python '3.12.14' @{requests='2.0'}
    Assert-R ((Failure-R {Test-BFRuntimePreflight $state (Join-Path $testRoot 'missing-lxml')}) -match 'package lxml') 'Missing lxml accepted.'
    $script:probeJson=New-ProbeJson $python '3.12.14' @{lxml='6.0.0'}
    Assert-R ((Failure-R {Test-BFRuntimePreflight $state (Join-Path $testRoot 'lxml-drift')}) -match 'package lxml') 'Wrong lxml version accepted.'
    $script:probeJson=New-ProbeJson (Join-Path $testRoot 'other.exe') '3.12.14' @{lxml='6.1.1'}
    Assert-R ((Failure-R {Test-BFRuntimePreflight $state (Join-Path $testRoot 'wrong-exe')}) -match 'different interpreter') 'Substituted interpreter accepted.'

    # 6. Runtime byte change invalidates the execution binding.
    & {
        function Test-BFToolsetSnapshot {param($Root,$ExpectedToolset);[pscustomobject]@{aggregate_sha256=('a'*64)}}
        $deps=Get-BFExecutionDependencies $state
        Assert-R ($null -ne $deps.profile.runtime) 'Runtime identity omitted from execution dependencies.'
        Write-R $python 'python fixture changed'
        Assert-R ((Failure-R {Get-BFExecutionDependencies $state}) -match 'runtime executable changed') 'Runtime drift accepted by execution dependencies.'
    }

    # 7. Pin validation rejects forbidden and incomplete definitions.
    Assert-R ((Failure-R {Assert-BFRuntimePin ([pscustomobject]@{executable=$python;sha256=$pythonSha;version='3.12';packages=@([pscustomobject]@{name='lxml';version='6.1.1'})})}) -match 'three-part version') 'Non-exact version accepted.'
    Assert-R ((Failure-R {Assert-BFRuntimePin ([pscustomobject]@{executable=$python;sha256=$pythonSha;version='3.12.14';packages=@([pscustomobject]@{name='requests';version='1.0'})})}) -match 'lxml') 'Runtime without lxml accepted.'
    Assert-R ((Failure-R {Assert-BFRuntimePin ([pscustomobject]@{executable=$python;sha256=$pythonSha;version='3.12.14';packages=@([pscustomobject]@{name='lxml';version='6.1.1'},[pscustomobject]@{name='lxml';version='6.1.1'})})}) -match 'duplicate') 'Duplicate package accepted.'
    $schema=Join-Path $PackageRoot 'global/skills/1c-task/schemas/request.schema.json'
    $unicaProfile=[pscustomobject]@{provider='opencode';executable=$python;executable_sha256=$pythonSha;sandbox=[pscustomobject]@{executable=$python;sha256=$pythonSha};toolset=[pscustomobject]@{name='unica';root=(Join-Path $testRoot 'toolset');sha256=('a'*64)};runtime=[pscustomobject]@{executable=$python;sha256=$pythonSha;version='3.12.14';packages=@([pscustomobject]@{name='lxml';version='6.1.1'})};unica=[pscustomobject]@{plugin_root=(Join-Path $testRoot 'unica');bootstrap_sha256=('b'*64);manifest_sha256=('c'*64);runtime_cache=(Join-Path $testRoot 'runtime');allowed_tools=@('unica.code.search')};denied_read_roots=@((Join-Path $testRoot 'private'))}
    Assert-R ((Failure-R {Assert-BFExecutionProfile $unicaProfile}) -match 'runtime block is forbidden') 'Unica accepted a pinned runtime block.'
    $unicaSchemaJson=@{provider='opencode';executable=$python;executable_sha256=$pythonSha;sandbox=@{executable=$python;sha256=$pythonSha};toolset=@{name='unica';root=(Join-Path $testRoot 'toolset');sha256=('a'*64)};runtime=@{executable=$python;sha256=$pythonSha;version='3.12.14';packages=@(@{name='lxml';version='6.1.1'})};denied_read_roots=@((Join-Path $testRoot 'private'))}|ConvertTo-Json -Depth 20
    Assert-R (-not (Test-Json -Json $unicaSchemaJson -SchemaFile $schema -ErrorAction SilentlyContinue)) 'Schema accepted a runtime block on a Unica profile.'
    Write-Output "TASK_RUNTIME_PIN_OK checks=$script:checks; model/sandbox/database processes=0"
} finally {
    $resolved=[IO.Path]::GetFullPath($testRoot)
    $temp=[IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\','/')+[IO.Path]::DirectorySeparatorChar
    if(-not $resolved.StartsWith($temp,[StringComparison]::OrdinalIgnoreCase) -or (Split-Path $resolved -Leaf) -notlike 'bsl-flow-runtime-pin-*'){throw 'Unsafe test cleanup target.'}
    Remove-Item -LiteralPath $resolved -Recurse -Force
}
