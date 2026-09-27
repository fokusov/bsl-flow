#Requires -Version 7.0
<#
onec-ops build.cf / build.cfe adapter: DESIGNER /LoadConfigFromFiles then /DumpCfg. Non-mutating
from the caller's perspective (plan Ф5.1: "мутирующая: нет (локальный файл)") because the target
is a scratch FILE infobase used only to materialize the .cf/.cfe artifact, never a shared/working
database - callers must point -target at a disposable infobase, never a project's real test DB.

/DumpCfg is UNVERIFIED here (not exercised elsewhere in this repo) - confirm the exact flag name
and -Extension applicability against ITS "Пакетный режим запуска" for the target platform version
before relying on this in production; /LoadConfigFromFiles and -Extension are verified precedent
(global/skills/1c-task/scripts/Task.Runtime.ps1).

Required -Params: target, source_dir, output_path. build.cfe additionally requires: extension.
Optional: username, password, executable_path (test override), dry_run (bool - construct argv
without starting a process).
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
$sourceDir = Get-N1Param $Params 'source_dir' $null
$outputPath = Get-N1Param $Params 'output_path' $null
$extension = Get-N1Param $Params 'extension' $null
if ([string]::IsNullOrWhiteSpace([string]$target) -or [string]::IsNullOrWhiteSpace([string]$sourceDir) -or [string]::IsNullOrWhiteSpace([string]$outputPath)) {
    return [pscustomobject]@{ status = 'BLOCKED'; evidence = @(); message = 'BF_INVALID: build.cf/build.cfe requires target, source_dir and output_path in -Params'; target = $target }
}
if ($Capability -eq 'build.cfe' -and [string]::IsNullOrWhiteSpace([string]$extension)) {
    return [pscustomobject]@{ status = 'BLOCKED'; evidence = @(); message = 'BF_INVALID: build.cfe requires extension in -Params'; target = $target }
}

$executable = Get-N1Param $Params 'executable_path' (Resolve-N1PlatformBin -Params $Params -ProjectPath $ProjectPath)
if ([string]::IsNullOrWhiteSpace([string]$executable)) {
    return [pscustomobject]@{ status = 'BLOCKED'; evidence = @(); message = 'BF_BLOCKED: no platform_bin configured for native-1cv8'; target = $target }
}
$username = Get-N1Param $Params 'username' ''
$password = Get-N1Param $Params 'password' ''
$dryRun = [bool](Get-N1Param $Params 'dry_run' $false)

$logDir = Join-Path $ProjectPath '.bsl-flow/reports/onec-ops-tmp'
$loadLog = Join-Path $logDir ('native-build-load-' + [guid]::NewGuid().ToString('N') + '.log')
$dumpLog = Join-Path $logDir ('native-build-dump-' + [guid]::NewGuid().ToString('N') + '.log')

$base = @('DESIGNER', '/DisableStartupDialogs', '/DisableStartupMessages', '/F', $target, '/N', $username, '/P', $password)
$loadArgv = $base + @('/Out', $loadLog, '-NoTruncate', '/LoadConfigFromFiles', $sourceDir)
$dumpArgv = $base + @('/Out', $dumpLog, '-NoTruncate', '/DumpCfg', $outputPath)
if ($Capability -eq 'build.cfe') {
    $loadArgv += @('-Extension', $extension)
    $dumpArgv += @('-Extension', $extension)
}

$loadResult = Invoke-N1Process -ExecutablePath $executable -Argv $loadArgv -LogPath $loadLog -DryRun:$dryRun
$dumpResult = Invoke-N1Process -ExecutablePath $executable -Argv $dumpArgv -LogPath $dumpLog -DryRun:$dryRun

if ($dryRun) {
    return [pscustomobject]@{
        status = 'BLOCKED'; evidence = @(); target = $target
        message = 'dry_run: argv constructed but process was not started'
        raw_output = (@($loadResult.Argv -join ' '), ($dumpResult.Argv -join ' ')) -join "`n"
    }
}

$ok = ($loadResult.ExitCode -eq 0 -and $dumpResult.ExitCode -eq 0 -and (Test-Path -LiteralPath $outputPath -PathType Leaf))
[pscustomobject]@{
    status     = if ($ok) { 'PASS' } else { 'FAIL' }
    evidence   = @(if ($ok) { $outputPath })
    message    = "load.exit=$($loadResult.ExitCode) dump.exit=$($dumpResult.ExitCode)"
    raw_output = ($loadResult.Log + "`n" + $dumpResult.Log)
    target     = $target
}
