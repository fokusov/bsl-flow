#Requires -Version 7.0
<#
Generic mapping adapter for external 1C skill sets (cf-*/cfe-*/epf-*/db-* etc.), configured in the
project's bsl-flow.yaml under `onec.skillset.map: { <capability>: "<skill-name-or-.ps1-path>" }`.
Generalizes the per-capability routing that used to live only in Task.Toolsets.ps1 for the managed
task runner, exposed here as a plain onec-ops provider so any skill can call it the same way.

Two routing shapes, decided per capability by the configured target's own shape:
  - target ends with ".ps1"  -> mode=script: run it out-of-process as
    `pwsh -File <target> -ProjectPath <p> -ParamsJson <json>`; its stdout is parsed as an
    onec-ops-shaped JSON result. Unstructured output is BLOCKED on exit 0; a nonzero exit
    is FAIL even when stdout claims PASS.
  - anything else            -> mode=agent_tool: treated as a Skill name. Returns BLOCKED with an
    agent_tool payload telling the calling agent which Skill to invoke and with what arguments;
    -ImportResult records an observed outcome bound to this capability and target, with evidence.

Required -Params: none beyond what the mapped target itself needs (forwarded verbatim as JSON).
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
. (Join-Path $AdapterDir '..\..\OneCOps.Common.ps1')
if ($Capability -in @('extension.load', 'config.update', 'test.yaxunit', 'test.vanessa')) {
    $failure = Get-OOAuthorizationFailure -Capability $Capability -Target (Get-OOProperty $Params @('target')) -AuthorizationFile $AuthorizationFile
    if ($null -ne $failure) { return $failure }
}


function Get-SOParam { param($Params, [string]$Name, $Default) $p = $Params.PSObject.Properties[$Name]; if ($null -ne $p -and $null -ne $p.Value) { return $p.Value }; return $Default }
function Get-SOProp { param($Object, [string]$Name, $Default) $p = $Object.PSObject.Properties[$Name]; if ($null -ne $p -and $null -ne $p.Value) { return $p.Value }; return $Default }

$yamlText = Get-OOBslFlowYamlText $ProjectPath
$map = Get-OOYamlFlatMap $yamlText @('onec', 'skillset', 'map')
if (-not $map.Contains($Capability)) {
    return [pscustomobject]@{ status = 'BLOCKED'; evidence = @(); message = "BF_BLOCKED: no skillset mapping for $Capability" }
}
$mapped = [string]$map[$Capability]
if ($mapped -match '(?i)unica[.:]runtime|(?:^|:)v8-runner$') {
    return [pscustomobject]@{ status = 'BLOCKED'; evidence = @(); message = 'BF_BLOCKED: Unica runtime recovery restriction also applies to skillset routes' }
}

if (-not [string]::IsNullOrWhiteSpace($ImportResult)) {
    if (-not (Test-Path -LiteralPath $ImportResult -PathType Leaf)) { throw "BF_INVALID: ImportResult file was not found: $ImportResult" }
    $imported = Get-Content -Raw -LiteralPath $ImportResult -Encoding UTF8 | ConvertFrom-Json -ErrorAction Stop
    $status = [string](Get-SOProp $imported 'status' $null)
    if ($status -notin @('PASS', 'FAIL', 'BLOCKED')) { throw "BF_INVALID: ImportResult status must be PASS, FAIL or BLOCKED, got: $status" }
    if ([string](Get-SOProp $imported 'capability' '') -cne $Capability -or [string](Get-SOProp $imported 'target' '') -cne [string](Get-SOParam $Params 'target' '')) {
        return [pscustomobject]@{ status = 'BLOCKED'; evidence = @(); message = 'BF_BLOCKED: imported capability/target does not match the request' }
    }
    if ($status -eq 'PASS' -and @(Get-SOProp $imported 'evidence' @()).Count -eq 0) {
        return [pscustomobject]@{ status = 'BLOCKED'; evidence = @(); message = 'BF_BLOCKED: imported PASS requires evidence files' }
    }
    return [pscustomobject]@{
        status     = $status
        evidence   = @(Get-SOProp $imported 'evidence' @())
        message    = Get-SOProp $imported 'message' 'Imported from agent-observed skillset outcome'
        raw_output = ($imported | ConvertTo-Json -Depth 20)
        target     = Get-SOParam $Params 'target' (Get-SOProp $imported 'target' $null)
    }
}

if ($mapped.ToLowerInvariant().EndsWith('.ps1')) {
    $scriptPath = if ([IO.Path]::IsPathRooted($mapped)) { $mapped } else { Join-Path $ProjectPath $mapped }
    if (-not (Test-Path -LiteralPath $scriptPath -PathType Leaf)) {
        return [pscustomobject]@{ status = 'BLOCKED'; evidence = @(); message = "BF_BLOCKED: mapped skillset script was not found: $scriptPath"; target = (Get-SOParam $Params 'target' $null) }
    }
    $paramsJson = ($Params | ConvertTo-Json -Depth 20 -Compress)
    $pwsh = (Get-Process -Id $PID).Path
    $stdout = & $pwsh -NoProfile -NonInteractive -File $scriptPath -ProjectPath $ProjectPath -ParamsJson $paramsJson 2>&1 | ForEach-Object { $_.ToString() } | Out-String
    $exit = $LASTEXITCODE
    try { $parsed = $stdout | ConvertFrom-Json -ErrorAction Stop } catch { $parsed = $null }
    if ($null -ne $parsed -and $parsed.PSObject.Properties['status']) {
        return [pscustomobject]@{ status = $(if ($exit -ne 0) { 'FAIL' } else { [string](Get-SOProp $parsed 'status' $null) }); evidence = @(Get-SOProp $parsed 'evidence' @()); message = (Get-SOProp $parsed 'message' $null); raw_output = $stdout; target = (Get-SOParam $Params 'target' $null) }
    }
    return [pscustomobject]@{ status = if ($exit -eq 0) { 'BLOCKED' } else { 'FAIL' }; evidence = @(); message = "mapped script exit=$exit"; raw_output = $stdout; target = (Get-SOParam $Params 'target' $null) }
}

# Skill-name mapping: instruct the agent, do not execute anything ourselves.
[pscustomobject]@{
    status     = 'BLOCKED'
    evidence   = @()
    message    = "Route this capability through the '$mapped' skill; re-invoke onec-ops with -ImportResult once you have an observed outcome."
    raw_output = $null
    target     = Get-SOParam $Params 'target' $null
    agent_tool = [ordered]@{
        agent_tool = $mapped
        arguments  = [ordered]@{ capability = $Capability; params = $Params }
    }
}
