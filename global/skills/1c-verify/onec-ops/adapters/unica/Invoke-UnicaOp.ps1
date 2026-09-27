#Requires -Version 7.0
# Runtime execution and imported runtime acceptance stay disabled under the temporary
# Unica recovery restriction. Authorization does not lift this independent gate.
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
. (Join-Path $AdapterDir '../../OneCOps.Common.ps1')
[pscustomobject]@{
    status = 'BLOCKED'
    mutating = $true
    evidence = @()
    target = Get-OOProperty $Params @('target')
    message = 'BF_BLOCKED: Unica runtime recovery restriction is active; runtime execution, durable jobs and imported runtime acceptance are disabled. A verified fix and explicit owner authorization are required to lift it.'
}
