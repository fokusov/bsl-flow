#Requires -Version 7.0
<#
Configurable fake adapter for offline contract tests: everything about the result is driven by
-Params so tests can exercise PASS/FAIL/BLOCKED, evidence hashing, and raw-output hashing without
touching a real 1C installation.

Recognised -Params properties (all optional):
  outcome        PASS | FAIL | BLOCKED (default PASS)
  evidence       array of paths (relative to ProjectPath) that must already exist
  message        free-text message
  raw_output     string hashed by the dispatcher into raw_output_sha256
  target         echoed back as the result's target
  provider_version override for provider_version
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

function Get-FKParam { param($Params, [string]$Name, $Default) $p = $Params.PSObject.Properties[$Name]; if ($null -ne $p -and $null -ne $p.Value) { return $p.Value }; return $Default }

$outcome = [string](Get-FKParam $Params 'outcome' 'PASS')
[pscustomobject]@{
    status           = $outcome
    evidence         = @(Get-FKParam $Params 'evidence' @())
    message          = Get-FKParam $Params 'message' $null
    raw_output       = Get-FKParam $Params 'raw_output' $null
    target           = Get-FKParam $Params 'target' $null
    provider_version = Get-FKParam $Params 'provider_version' '1.0.0'
}
