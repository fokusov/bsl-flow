#Requires -Version 7.0
<#
onec-ops config.update adapter (MUTATING - reached only after dispatcher-verified authorization).
DESIGNER /UpdateDBCfg [-Extension <name>] - verified precedent
(global/skills/1c-task/scripts/Task.Runtime.ps1).

Required -Params: target. Optional: extension, username, password, executable_path (test
override), dry_run (bool - construct argv without starting a process).
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
if ([string]::IsNullOrWhiteSpace([string]$target)) {
    return [pscustomobject]@{ status = 'BLOCKED'; evidence = @(); message = 'BF_INVALID: config.update requires target in -Params'; target = $target }
}
$extension = Get-N1Param $Params 'extension' $null
$executable = Get-N1Param $Params 'executable_path' (Resolve-N1PlatformBin -Params $Params -ProjectPath $ProjectPath)
if ([string]::IsNullOrWhiteSpace([string]$executable)) {
    return [pscustomobject]@{ status = 'BLOCKED'; evidence = @(); message = 'BF_BLOCKED: no platform_bin configured for native-1cv8'; target = $target }
}
$username = Get-N1Param $Params 'username' ''
$password = Get-N1Param $Params 'password' ''
$dryRun = [bool](Get-N1Param $Params 'dry_run' $false)

$logDir = Join-Path $ProjectPath '.bsl-flow/reports/onec-ops-tmp'
$log = Join-Path $logDir ('native-configupdate-' + [guid]::NewGuid().ToString('N') + '.log')
$argv = @('DESIGNER', '/DisableStartupDialogs', '/DisableStartupMessages', '/F', $target, '/N', $username, '/P', $password, '/Out', $log, '-NoTruncate', '/UpdateDBCfg')
if (-not [string]::IsNullOrWhiteSpace([string]$extension)) { $argv += @('-Extension', $extension) }

$result = Invoke-N1Process -ExecutablePath $executable -Argv $argv -LogPath $log -DryRun:$dryRun
if ($dryRun) {
    return [pscustomobject]@{ status = 'BLOCKED'; evidence = @(); target = $target; message = 'dry_run: argv constructed but process was not started'; raw_output = ($result.Argv -join ' ') }
}
[pscustomobject]@{
    status     = if ($result.ExitCode -eq 0) { 'PASS' } else { 'FAIL' }
    evidence   = @()
    message    = "update.exit=$($result.ExitCode)"
    raw_output = $result.Log
    target     = $target
}
