#Requires -Version 7.0
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot '..\..\OneCOps.Common.ps1')

<#
Shared helpers for the native-1cv8 adapter. Command shapes below reuse the exact 1cv8 DESIGNER /
ENTERPRISE batch-mode argument construction already verified and merged in this repo at
global/skills/1c-task/scripts/Task.Runtime.ps1 (managed controller code, not edited here - this
is an independent re-derivation of the same flags for the onec-ops adapter, not a shared function
call, so this adapter has no runtime dependency on that managed file):
  DESIGNER  /DisableStartupDialogs /DisableStartupMessages /F<target> /N<user> /P<password>
            /Out<log> -NoTruncate /LoadConfigFromFiles<dir> -Extension<name>
            /Out<log> -NoTruncate /UpdateDBCfg -Extension<name>
  ENTERPRISE /DisableStartupDialogs /F<target> /N<user> /P<password>
            /C RunUnitTests=<config.json path, forward slashes> /Out<log>
/DumpCfg and /LoadCfg (build.cf/.cfe, extension.load) follow the same ITS batch-mode documentation
family as /LoadConfigFromFiles and /UpdateDBCfg above but are NOT exercised elsewhere in this repo
yet - flag names are marked UNVERIFIED at each call site below and must be checked against the
exact target platform version's ITS "Пакетный режим запуска" page before first real-world use.
#>

function Resolve-N1PlatformBin {
    param([Parameter(Mandatory)][object]$Params, [Parameter(Mandatory)][string]$ProjectPath)
    $fromParams = $null
    $prop = $Params.PSObject.Properties['platform_bin']
    if ($null -ne $prop -and -not [string]::IsNullOrWhiteSpace([string]$prop.Value)) { $fromParams = [string]$prop.Value }
    if ($fromParams) { return $fromParams }

    $yamlText = Get-OOBslFlowYamlText $ProjectPath
    $fromYaml = Get-OOYamlValue $yamlText @('onec', 'native-1cv8', 'platform_bin') $null
    if (-not $fromYaml) { $fromYaml = Get-OOYamlValue $yamlText @('environment', 'platform_bin') $null }
    if ($fromYaml) { return $fromYaml }

    $profilePath = Join-Path ([Environment]::GetFolderPath('UserProfile')) '.bsl-flow/workstation.json'
    if (Test-Path -LiteralPath $profilePath -PathType Leaf) {
        try {
            $profileObj = Get-Content -Raw -LiteralPath $profilePath -Encoding UTF8 | ConvertFrom-Json -ErrorAction Stop
            $bin = Get-OOProperty $profileObj @('platform_bin')
            if (-not [string]::IsNullOrWhiteSpace([string]$bin)) { return [string]$bin }
        }
        catch {}
    }
    return $null
}

function Invoke-N1Process {
    # Runs the configured executable and records the exact argument list and log path, without
    # ever starting the process when $DryRun is set - used by mutating-capability callers to
    # prove (in tests) that a missing/invalid authorization never reaches this function.
    param(
        [Parameter(Mandatory)][string]$ExecutablePath,
        [Parameter(Mandatory)][string[]]$Argv,
        [Parameter(Mandatory)][string]$LogPath,
        [switch]$DryRun
    )
    [void][IO.Directory]::CreateDirectory((Split-Path -Parent $LogPath))
    if ($DryRun) { return [pscustomobject]@{ Started = $false; ExitCode = $null; Argv = $Argv } }

    $isScript = $ExecutablePath.ToLowerInvariant().EndsWith('.ps1')
    $psi = [Diagnostics.ProcessStartInfo]::new()
    if ($isScript) {
        $psi.FileName = (Get-Process -Id $PID).Path
        foreach ($arg in (@('-NoProfile', '-NonInteractive', '-File', $ExecutablePath) + $Argv)) { [void]$psi.ArgumentList.Add($arg) }
    }
    else {
        $psi.FileName = $ExecutablePath
        foreach ($arg in $Argv) { [void]$psi.ArgumentList.Add($arg) }
    }
    $psi.RedirectStandardOutput = $true
    $psi.RedirectStandardError = $true
    $psi.UseShellExecute = $false
    $process = [Diagnostics.Process]::Start($psi)
    $stdout = $process.StandardOutput.ReadToEnd()
    $stderr = $process.StandardError.ReadToEnd()
    $process.WaitForExit()
    Set-Content -LiteralPath $LogPath -Value ($stdout + $stderr) -Encoding UTF8
    return [pscustomobject]@{ Started = $true; ExitCode = $process.ExitCode; Argv = $Argv; Log = ($stdout + $stderr) }
}

function Get-N1Param {
    param($Params, [string]$Name, $Default)
    $p = $Params.PSObject.Properties[$Name]
    if ($null -ne $p -and $null -ne $p.Value) { return $p.Value }
    return $Default
}
