#Requires -Version 7.0
<#
onec-ops adapter for vanessa-runner (vrunner/oscript, https://github.com/vanessa-opensource/vanessa-runner).

UNVERIFIED: none of the subcommands or flags below were confirmed against a live vrunner install
or a specific pinned vanessa-runner release for this port; they are this adapter's best-effort
reading of the project's own README/wiki conventions at the time of writing. Re-verify every
subcommand and flag against `vrunner --help` / the exact vanessa-runner version pinned by the
project before depending on this in production, and update the comments below once confirmed:
  syntax.check  -> vrunner syntax-check --src <src>
  build.cf      -> vrunner compile --src <src> --out <out>
  build.cfe     -> vrunner compileext --src <src> --out <out> --extension <name>
  test.yaxunit  -> vrunner run --dbpath <target> --command "RunUnitTests=<config.json>"
  test.vanessa  -> vrunner vanessa --dbpath <target> --settings <settings>

Required -Params by capability:
  syntax.check: src
  build.cf/build.cfe: src, output_path (build.cfe also: extension)
  test.yaxunit: target, modules (array)
  test.vanessa: target, settings
Optional for all: vrunner_bin (test override), dry_run (bool - construct argv without starting a
process).
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
. (Join-Path $AdapterDir '..\native-1cv8\Native.Common.ps1')
if ($Capability -in @('build.cf', 'build.cfe', 'test.yaxunit', 'test.vanessa')) {
    $failure = Get-OOAuthorizationFailure -Capability $Capability -Target (Get-OOProperty $Params @('target')) -AuthorizationFile $AuthorizationFile
    if ($null -ne $failure) { return $failure }
}


function Get-VOParam { param($Params, [string]$Name, $Default) $p = $Params.PSObject.Properties[$Name]; if ($null -ne $p -and $null -ne $p.Value) { return $p.Value }; return $Default }

function Resolve-VOBin {
    param($Params, [string]$ProjectPath)
    $explicit = Get-VOParam $Params 'vrunner_bin' $null
    if ($explicit) { return $explicit }
    $cmd = Get-Command 'vrunner' -ErrorAction SilentlyContinue
    if ($cmd) { return $cmd.Source }
    $yamlText = Get-OOBslFlowYamlText $ProjectPath
    return (Get-OOYamlValue $yamlText @('onec', 'vrunner', 'bin') $null)
}

$dryRun = [bool](Get-VOParam $Params 'dry_run' $false)
$executable = Resolve-VOBin $Params $ProjectPath
if ([string]::IsNullOrWhiteSpace([string]$executable)) {
    return [pscustomobject]@{ status = 'BLOCKED'; evidence = @(); message = 'BF_BLOCKED: no vrunner binary configured'; target = (Get-VOParam $Params 'target' $null) }
}

$logDir = Join-Path $ProjectPath '.bsl-flow/reports/onec-ops-tmp'
[void][IO.Directory]::CreateDirectory($logDir)
$log = Join-Path $logDir ('vrunner-' + ($Capability -replace '[^A-Za-z0-9]', '_') + '-' + [guid]::NewGuid().ToString('N') + '.log')

$target = Get-VOParam $Params 'target' $null
$argv = $null
$evidence = @()

switch ($Capability) {
    'syntax.check' {
        $src = Get-VOParam $Params 'src' $null
        if ([string]::IsNullOrWhiteSpace([string]$src)) { return [pscustomobject]@{ status = 'BLOCKED'; evidence = @(); message = 'BF_INVALID: syntax.check requires src'; target = $target } }
        $argv = @('syntax-check', '--src', $src)
    }
    'build.cf' {
        $src = Get-VOParam $Params 'src' $null
        $outputPath = Get-VOParam $Params 'output_path' $null
        if ([string]::IsNullOrWhiteSpace([string]$src) -or [string]::IsNullOrWhiteSpace([string]$outputPath)) { return [pscustomobject]@{ status = 'BLOCKED'; evidence = @(); message = 'BF_INVALID: build.cf requires src and output_path'; target = $target } }
        $argv = @('compile', '--src', $src, '--out', $outputPath)
        $evidence = @($outputPath)
    }
    'build.cfe' {
        $src = Get-VOParam $Params 'src' $null
        $outputPath = Get-VOParam $Params 'output_path' $null
        $extension = Get-VOParam $Params 'extension' $null
        if ([string]::IsNullOrWhiteSpace([string]$src) -or [string]::IsNullOrWhiteSpace([string]$outputPath) -or [string]::IsNullOrWhiteSpace([string]$extension)) { return [pscustomobject]@{ status = 'BLOCKED'; evidence = @(); message = 'BF_INVALID: build.cfe requires src, output_path and extension'; target = $target } }
        $argv = @('compileext', '--src', $src, '--out', $outputPath, '--extension', $extension)
        $evidence = @($outputPath)
    }
    'test.yaxunit' {
        $modules = @(Get-VOParam $Params 'modules' @())
        if ([string]::IsNullOrWhiteSpace([string]$target) -or $modules.Count -eq 0) { return [pscustomobject]@{ status = 'BLOCKED'; evidence = @(); message = 'BF_INVALID: test.yaxunit requires target and a non-empty modules list'; target = $target } }
        $reportPath = Join-Path $logDir ('vrunner-yaxunit-junit-' + [guid]::NewGuid().ToString('N') + '.xml')
        $configPath = Join-Path $logDir ('vrunner-yaxunit-config-' + [guid]::NewGuid().ToString('N') + '.json')
        $config = [ordered]@{ filter = [ordered]@{ modules = @($modules) }; reportFormat = 'jUnit'; reportPath = $reportPath; closeAfterTests = $true; showReport = $false; logging = [ordered]@{ file = (Join-Path $logDir 'vrunner-yaxunit-runner.log'); console = $false; level = 'info' } }
        $config | ConvertTo-Json -Depth 10 | Set-Content -LiteralPath $configPath -Encoding utf8
        $argv = @('run', '--dbpath', $target, '--command', ('RunUnitTests=' + $configPath.Replace('\', '/')))
        $evidence = @($configPath, $reportPath)
    }
    'test.vanessa' {
        $settings = Get-VOParam $Params 'settings' $null
        if ([string]::IsNullOrWhiteSpace([string]$target) -or [string]::IsNullOrWhiteSpace([string]$settings)) { return [pscustomobject]@{ status = 'BLOCKED'; evidence = @(); message = 'BF_INVALID: test.vanessa requires target and settings'; target = $target } }
        $argv = @('vanessa', '--dbpath', $target, '--settings', $settings)
    }
    default { return [pscustomobject]@{ status = 'BLOCKED'; evidence = @(); message = "BF_INVALID: vrunner adapter does not implement capability $Capability"; target = $target } }
}

$result = Invoke-N1Process -ExecutablePath $executable -Argv $argv -LogPath $log -DryRun:$dryRun
if ($dryRun) {
    return [pscustomobject]@{ status = 'BLOCKED'; evidence = @(); target = $target; message = 'dry_run: argv constructed but process was not started'; raw_output = ($result.Argv -join ' ') }
}

# Build/test acceptance needs its artifact; process exit alone is insufficient.
$missingEvidence = @($evidence | Where-Object { -not (Test-Path -LiteralPath $_ -PathType Leaf) })
$operationStatus = if ($result.ExitCode -eq 0 -and $missingEvidence.Count -eq 0) { 'PASS' } else { 'FAIL' }
if ($Capability -in @('test.yaxunit', 'test.vanessa') -and $operationStatus -eq 'PASS') {
    $operationStatus = 'BLOCKED' # A verified runner/result parser is not available yet.
}
$relEvidence = @()
foreach ($path in $evidence) {
    if ([string]::IsNullOrWhiteSpace([string]$path)) { continue }
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { continue }
    $full = (Resolve-Path -LiteralPath $path).Path
    $relEvidence += $(if ($full.StartsWith($ProjectPath, [StringComparison]::OrdinalIgnoreCase)) { $full.Substring($ProjectPath.Length).TrimStart('\', '/') } else { $full })
}
[pscustomobject]@{
    status     = $operationStatus
    evidence   = $relEvidence
    message    = "vrunner.exit=$($result.ExitCode); CLI flags UNVERIFIED"
    raw_output = $result.Log
    target     = $target
}
