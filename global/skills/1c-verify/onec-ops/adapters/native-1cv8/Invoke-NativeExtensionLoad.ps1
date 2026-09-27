#Requires -Version 7.0
<#
onec-ops extension.load adapter (MUTATING - the dispatcher only reaches this script after
verifying a matching, unexpired authorization file; there is no authorization check here by
design, so this script must never be invoked directly outside the dispatcher).

DESIGNER /LoadCfg <path> -Extension <name>, then /UpdateDBCfg -Extension <name>. /LoadCfg is
UNVERIFIED here (not exercised elsewhere in this repo) - confirm against ITS "Пакетный режим
запуска" for the target platform version before relying on this in production. /UpdateDBCfg and
-Extension are verified precedent (global/skills/1c-task/scripts/Task.Runtime.ps1).

Required -Params: target, cfe_path, extension. Optional: username, password, executable_path
(test override), dry_run (bool - construct argv without starting a process).
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
$cfePath = Get-N1Param $Params 'cfe_path' $null
$extension = Get-N1Param $Params 'extension' $null
if ([string]::IsNullOrWhiteSpace([string]$target) -or [string]::IsNullOrWhiteSpace([string]$cfePath) -or [string]::IsNullOrWhiteSpace([string]$extension)) {
    return [pscustomobject]@{ status = 'BLOCKED'; evidence = @(); message = 'BF_INVALID: extension.load requires target, cfe_path and extension in -Params'; target = $target }
}

$executable = Get-N1Param $Params 'executable_path' (Resolve-N1PlatformBin -Params $Params -ProjectPath $ProjectPath)
if ([string]::IsNullOrWhiteSpace([string]$executable)) {
    return [pscustomobject]@{ status = 'BLOCKED'; evidence = @(); message = 'BF_BLOCKED: no platform_bin configured for native-1cv8'; target = $target }
}
$username = Get-N1Param $Params 'username' ''
$password = Get-N1Param $Params 'password' ''
$dryRun = [bool](Get-N1Param $Params 'dry_run' $false)

$logDir = Join-Path $ProjectPath '.bsl-flow/reports/onec-ops-tmp'
$loadLog = Join-Path $logDir ('native-extload-load-' + [guid]::NewGuid().ToString('N') + '.log')
$updateLog = Join-Path $logDir ('native-extload-update-' + [guid]::NewGuid().ToString('N') + '.log')

$base = @('DESIGNER', '/DisableStartupDialogs', '/DisableStartupMessages', '/F', $target, '/N', $username, '/P', $password)
$loadArgv = $base + @('/Out', $loadLog, '-NoTruncate', '/LoadCfg', $cfePath, '-Extension', $extension)
$updateArgv = $base + @('/Out', $updateLog, '-NoTruncate', '/UpdateDBCfg', '-Extension', $extension)

$loadResult = Invoke-N1Process -ExecutablePath $executable -Argv $loadArgv -LogPath $loadLog -DryRun:$dryRun
$updateResult = Invoke-N1Process -ExecutablePath $executable -Argv $updateArgv -LogPath $updateLog -DryRun:$dryRun

if ($dryRun) {
    return [pscustomobject]@{
        status = 'BLOCKED'; evidence = @(); target = $target
        message = 'dry_run: argv constructed but process was not started'
        raw_output = (@($loadResult.Argv -join ' '), ($updateResult.Argv -join ' ')) -join "`n"
    }
}

$ok = ($loadResult.ExitCode -eq 0 -and $updateResult.ExitCode -eq 0)
[pscustomobject]@{
    status     = if ($ok) { 'PASS' } else { 'FAIL' }
    evidence   = @()
    message    = "load.exit=$($loadResult.ExitCode) update.exit=$($updateResult.ExitCode)"
    raw_output = ($loadResult.Log + "`n" + $updateResult.Log)
    target     = $target
}
