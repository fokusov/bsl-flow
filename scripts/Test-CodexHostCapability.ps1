#Requires -Version 7.0
[CmdletBinding()]
param([string]$PackageRoot)
Set-StrictMode -Version Latest
$ErrorActionPreference='Stop'
if(-not $PackageRoot){$PackageRoot=Split-Path $PSScriptRoot -Parent}
$core=Join-Path $PackageRoot 'global/skills/1c-task'
foreach($file in @('scripts/Task.Storage.ps1','scripts/Task.Contracts.ps1','scripts/Task.Process.ps1','adapters/Codex.ps1')){. (Join-Path $core $file)}
# The fixture lives below the test user's profile, where a real Codex config
# may exist; the production configuration fence is covered by other suites.
function Assert-BFWorkerConfiguration { param([string]$WorkerPath) }
$script:checks=0
function Check([bool]$Value,[string]$Message){if(-not $Value){throw "ASSERTION FAILED: $Message"};$script:checks++}
function Reject([scriptblock]$Body,[string]$Pattern){
    $message='';try{& $Body|Out-Null}catch{$message=$_.Exception.Message}
    Check ($message -match $Pattern) "Expected $Pattern, got $message"
}
foreach($version in @('codex-cli 0.153.0','codex-cli 0.154.0','codex-cli 0.155.1','codex-cli 0.161.0')){
    Check ((Assert-BFCodexHostVersion $version) -ceq $version) "Stable Codex version rejected: $version"
}
foreach($version in @('codex-cli 0.152.9','codex-cli 0.155.0-alpha.1','codex-cli latest','other-cli 0.155.1')){
    Reject {Assert-BFCodexHostVersion $version} 'BF_BLOCKED'
}
$root=Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-codex-capability-'+[guid]::NewGuid().ToString('N'))
[void][IO.Directory]::CreateDirectory($root)
try{
    $worker=Join-Path $root 'worker';[void][IO.Directory]::CreateDirectory($worker)
    $codex=Join-Path $root 'codex.exe';[IO.File]::WriteAllBytes($codex,[byte[]](1,2,3))
    $state=[pscustomobject]@{worker_path=$worker}
    $script:fixtureVersion='codex-cli 0.155.1';$script:fixtureIsolation='good';$script:sandboxCalls=0
    function Invoke-BFProcess {
        param($Executable,$Arguments,$WorkingDirectory,$InputText,$OutputDirectory,$TimeoutSeconds)
        [void][IO.Directory]::CreateDirectory($OutputDirectory)
        $stdout=Join-Path $OutputDirectory 'stdout.txt'
        if(@($Arguments).Count -eq 1 -and $Arguments[0] -ceq '--version'){
            [IO.File]::WriteAllText($stdout,$script:fixtureVersion)
        }else{
            $script:sandboxCalls++
            $writable=@($Arguments) -contains (Get-BFPermissionProfile $worker $true)
            $result=if($script:fixtureIsolation -ceq 'good'){
                if($writable){'allowed:denied'}else{'denied:denied'}
            }else{'allowed:allowed'}
            [IO.File]::WriteAllText($stdout,$result)
        }
        return [pscustomobject]@{exit_code=0;stdout=$stdout}
    }
    $capability=Test-BFCodexCapability $state $codex (Join-Path $root 'supported')
    Check ($capability.version -ceq 'codex-cli 0.155.1' -and $script:sandboxCalls -eq 2) 'Codex 0.155.1 did not require both sandbox observations.'
    $script:fixtureVersion='codex-cli 0.161.0';$script:sandboxCalls=0
    $capability=Test-BFCodexCapability $state $codex (Join-Path $root 'future')
    Check ($capability.version -ceq 'codex-cli 0.161.0' -and $script:sandboxCalls -eq 2) 'Future stable Codex version did not require both sandbox observations.'
    $script:fixtureIsolation='bad';$script:sandboxCalls=0
    Reject {Test-BFCodexCapability $state $codex (Join-Path $root 'unsafe')} 'sandbox capability was not demonstrated'
    Check ($script:sandboxCalls -eq 1) 'Unsafe sandbox probe did not fail immediately.'
    Write-Host "Codex host capability contracts: $script:checks PASS; model calls=0."
}finally{
    $safe=[IO.Path]::GetFullPath($root);$temp=[IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\')+'\'
    if(-not $safe.StartsWith($temp,[StringComparison]::OrdinalIgnoreCase) -or (Split-Path $safe -Leaf) -notlike 'bsl-flow-codex-capability-*'){throw 'Unsafe fixture cleanup target.'}
    if(Test-Path -LiteralPath $safe){Remove-Item -LiteralPath $safe -Recurse -Force}
}
