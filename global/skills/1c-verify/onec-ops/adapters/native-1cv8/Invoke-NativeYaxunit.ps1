#Requires -Version 7.0
<#
onec-ops test.yaxunit adapter (MUTATING - reached only after dispatcher-verified authorization;
YAxUnit tests can write test data into the target base).

ENTERPRISE /C RunUnitTests=<config.json> with a YAxUnit JSON config - verified precedent
(global/skills/1c-task/scripts/Task.Runtime.ps1: filter.modules / reportFormat=jUnit /
reportPath / closeAfterTests / showReport / logging). The config path is written with forward
slashes, matching that precedent, because YAxUnit's own JSON reader has been observed to require
it on Windows targets.

Required -Params: target, modules (array of module names). Optional: username, password,
executable_path (test override), dry_run (bool - construct argv without starting a process).
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$ProjectPath,
    [object]$Params,
    [string]$AdapterDir,
    [string]$Capability,
    [string]$AuthorizationFile,
    [string]$ImportResult
)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
. (Join-Path $AdapterDir 'Native.Common.ps1')

$target = Get-N1Param $Params 'target' $null
$modules = @(Get-N1Param $Params 'modules' @())
if ([string]::IsNullOrWhiteSpace([string]$target) -or $modules.Count -eq 0) {
    return [pscustomobject]@{ status = 'BLOCKED'; evidence = @(); message = 'BF_INVALID: test.yaxunit requires target and a non-empty modules list in -Params'; target = $target }
}
$executable = Get-N1Param $Params 'executable_path' (Resolve-N1PlatformBin -Params $Params -ProjectPath $ProjectPath)
if ([string]::IsNullOrWhiteSpace([string]$executable)) {
    return [pscustomobject]@{ status = 'BLOCKED'; evidence = @(); message = 'BF_BLOCKED: no platform_bin configured for native-1cv8'; target = $target }
}
$username = Get-N1Param $Params 'username' ''
$password = Get-N1Param $Params 'password' ''
$dryRun = [bool](Get-N1Param $Params 'dry_run' $false)

$logDir = Join-Path $ProjectPath '.bsl-flow/reports/onec-ops-tmp'
[void][IO.Directory]::CreateDirectory($logDir)
$reportPath = Join-Path $logDir ('native-yaxunit-junit-' + [guid]::NewGuid().ToString('N') + '.xml')
$configPath = Join-Path $logDir ('native-yaxunit-config-' + [guid]::NewGuid().ToString('N') + '.json')
$log = Join-Path $logDir ('native-yaxunit-' + [guid]::NewGuid().ToString('N') + '.log')

$config = [ordered]@{
    filter        = [ordered]@{ modules = @($modules) }
    reportFormat  = 'jUnit'
    reportPath    = $reportPath
    closeAfterTests = $true
    showReport    = $false
    logging       = [ordered]@{ file = (Join-Path $logDir 'yaxunit-runner.log'); console = $false; level = 'info' }
}
$config | ConvertTo-Json -Depth 10 | Set-Content -LiteralPath $configPath -Encoding utf8

$argv = @('ENTERPRISE', '/DisableStartupDialogs', '/F', $target, '/N', $username, '/P', $password, '/C', ('RunUnitTests=' + $configPath.Replace('\', '/')), '/Out', $log)
$result = Invoke-N1Process -ExecutablePath $executable -Argv $argv -LogPath $log -DryRun:$dryRun

if ($dryRun) {
    return [pscustomobject]@{ status = 'BLOCKED'; evidence = @($configPath.Substring($ProjectPath.Length).TrimStart('\', '/')); target = $target; message = 'dry_run: argv constructed but process was not started'; raw_output = ($result.Argv -join ' ') }
}

$reportExists = Test-Path -LiteralPath $reportPath -PathType Leaf
$junitFailed = $false
if ($reportExists) {
    try {
        [xml]$junit = Get-Content -Raw -LiteralPath $reportPath -Encoding UTF8
        foreach ($suite in @($junit.SelectNodes('//testsuite|//testsuites'))) {
            foreach ($name in @('failures', 'errors')) {
                if ($suite.HasAttribute($name) -and [int]$suite.GetAttribute($name) -gt 0) { $junitFailed = $true }
            }
        }
    }
    catch { $junitFailed = $true }
}
$ok = ($result.ExitCode -eq 0 -and $reportExists -and -not $junitFailed)
$evidence = @($configPath.Substring($ProjectPath.Length).TrimStart('\', '/'))
if ($reportExists) { $evidence += $reportPath.Substring($ProjectPath.Length).TrimStart('\', '/') }
[pscustomobject]@{
    status     = if ($ok) { 'PASS' } else { 'FAIL' }
    evidence   = $evidence
    message    = "enterprise.exit=$($result.ExitCode)"
    raw_output = $result.Log
    target     = $target
}
