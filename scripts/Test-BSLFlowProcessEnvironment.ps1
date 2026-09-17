#Requires -Version 7.0
Set-StrictMode -Version Latest
$ErrorActionPreference='Stop'
$root=Split-Path $PSScriptRoot -Parent
. (Join-Path $root 'global/skills/1c-task/scripts/Task.Storage.ps1')
. (Join-Path $root 'global/skills/1c-task/scripts/Task.Contracts.ps1')
. (Join-Path $root 'global/skills/1c-task/scripts/Task.Process.ps1')

$testRoot=Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-process-environment-'+[guid]::NewGuid().ToString('N'))
$canary='BF_CANARY_VALUE_7f01';$secret='BF_PARENT_SECRET_7f01';$default='BF_DEFAULT_VALUE_7f01'
$hadSecret=Test-Path Env:BF_TEST_PARENT_SECRET;$oldSecret=$env:BF_TEST_PARENT_SECRET
$hadDefault=Test-Path Env:BF_TEST_DEFAULT;$oldDefault=$env:BF_TEST_DEFAULT
$checks=0
function Read-Text([string]$Path){return [IO.File]::ReadAllText($Path).TrimEnd("`r","`n")}
function Assert-True([bool]$Value,[string]$Message){if(-not $Value){throw $Message};$script:checks++}
try {
    [void][IO.Directory]::CreateDirectory($testRoot)
    $env:BF_TEST_PARENT_SECRET=$secret
    $env:BF_TEST_DEFAULT=$default
    $shell=Join-Path $PSHOME 'pwsh.exe'
    $readEnvironment='Write-Output ($env:BF_TEST_CANARY+"|"+$env:BF_TEST_PARENT_SECRET)'
    $cleanDir=Join-Path $testRoot 'clean'
    $clean=Invoke-BFProcess -Executable $shell -Arguments @('-NoLogo','-NoProfile','-Command',$readEnvironment) -WorkingDirectory $testRoot -InputText '' -OutputDirectory $cleanDir -TimeoutSeconds 10 -Environment @{BF_TEST_CANARY=$canary} -CleanEnvironment
    Assert-True ($clean.exit_code -eq 0 -and $null -eq $clean.stop_reason) 'Clean environment child did not complete.'
    Assert-True ((Read-Text $clean.stdout) -ceq ($canary+'|')) 'Clean environment did not pass only the supplied canary.'
    $receiptText=(Read-Text (Join-Path $cleanDir 'process.json'))+(Read-Text (Join-Path $cleanDir 'exit.json'))
    Assert-True (-not $receiptText.Contains($canary) -and -not $receiptText.Contains($secret)) 'Process receipt contains an environment value.'

    $defaultDir=Join-Path $testRoot 'default'
    $defaultResult=Invoke-BFProcess $shell @('-NoLogo','-NoProfile','-Command','Write-Output $env:BF_TEST_DEFAULT') $testRoot '' $defaultDir 10
    Assert-True ($defaultResult.exit_code -eq 0 -and (Read-Text $defaultResult.stdout) -ceq $default) 'Default inherited environment changed.'

    $sleepCommand='Start-Sleep -Seconds 10'
    $timeout=Invoke-BFProcess $shell @('-NoLogo','-NoProfile','-Command',$sleepCommand) $testRoot '' (Join-Path $testRoot 'timeout') 1
    Assert-True ($timeout.stop_reason -ceq 'timeout') 'Timeout behavior changed.'
    $script:cancelChecks=0
    $cancel={$script:cancelChecks++;return $script:cancelChecks -ge 2}
    $cancelled=Invoke-BFProcess $shell @('-NoLogo','-NoProfile','-Command',$sleepCommand) $testRoot '' (Join-Path $testRoot 'cancelled') 10 $cancel
    Assert-True ($cancelled.stop_reason -ceq 'cancelled') 'Cancellation behavior changed.'
    $large=Invoke-BFProcess -Executable $shell -Arguments @('-NoProfile','-Command','[Console]::Out.Write("x"*40000); [Console]::Error.Write("y"*40000)') -WorkingDirectory $testRoot -OutputDirectory (Join-Path $testRoot 'output-bound') -TimeoutSeconds 10 -MaxOutputBytes 65536
    Assert-True ($large.stop_reason -ceq 'output_limit') 'Combined output exceeding the bound was accepted.'
    Write-Output ('PROCESS_ENVIRONMENT_OK checks='+$checks)
} finally {
    if($hadSecret){$env:BF_TEST_PARENT_SECRET=$oldSecret}else{Remove-Item Env:BF_TEST_PARENT_SECRET -ErrorAction SilentlyContinue}
    if($hadDefault){$env:BF_TEST_DEFAULT=$oldDefault}else{Remove-Item Env:BF_TEST_DEFAULT -ErrorAction SilentlyContinue}
    for($attempt=0;$attempt -lt 10 -and (Test-Path -LiteralPath $testRoot);$attempt++){
        if(-not [IO.Path]::GetFullPath($testRoot).StartsWith([IO.Path]::GetFullPath([IO.Path]::GetTempPath()),[StringComparison]::OrdinalIgnoreCase)){throw 'Unsafe cleanup target.'}
        try {Remove-Item -LiteralPath $testRoot -Recurse -Force -ErrorAction Stop}catch{Start-Sleep -Milliseconds 200}
    }
    if(Test-Path -LiteralPath $testRoot){Write-Warning 'Temporary synthetic process evidence is still locked and was retained outside the workspace.'}
}
