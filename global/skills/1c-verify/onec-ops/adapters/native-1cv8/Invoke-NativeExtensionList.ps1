#Requires -Version 7.0
<#
onec-ops extension.list adapter: intentionally does NOT declare -Extension against
/LoadConfigFromFiles, /LoadCfg or /UpdateDBCfg here - none of those are read-only, and this
capability must stay non-mutating per plan Ф5.1. There is no ITS-documented 1cv8 DESIGNER batch
flag verified (as of writing) that enumerates installed extensions without mutating the target,
and this repo's own test-setup guidance
(global/skills/1c-init-project/references/test-setup.md:16) explicitly forbids treating Unica's
`operation=extensions` as a list route for the same reason - it is documented as MUTATING there.
Rather than fabricate an unverified flag, this adapter fails closed: BLOCKED, pointing the caller
at the metadata.inspect capability (grounding adapter, once Ф5.2 lands) or an interactive
Configurator/COM-based read documented per project as the real non-mutating source of truth.

Required -Params: target.
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
[pscustomobject]@{
    status  = 'BLOCKED'
    evidence = @()
    message = 'BF_BLOCKED: native-1cv8 has no verified non-mutating route to enumerate installed extensions; use metadata.inspect (grounding) or an approved read-only Configurator/COM inspection instead'
    target  = $target
}
