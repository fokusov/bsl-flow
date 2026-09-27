#Requires -Version 7.0
<#
onec-ops adapter for Unica (an MCP server the calling AGENT invokes directly - this script never
opens a connection to it and never mutates anything). Two modes:

1. Default (no -ImportResult): returns BLOCKED with an `agent_tool` instruction payload naming
   the exact MCP call the agent should make, always with dryRun:true. The agent is responsible
   for calling that MCP tool, obtaining real authorization for the underlying mutation (which
   happens entirely inside Unica/the calling agent's own permission flow - out of scope for this
   dispatcher), and then re-invoking onec-ops with -ImportResult once it has an observed outcome.

2. With -ImportResult <file>: relays that file's already-observed outcome as this operation's
   result instead of emitting a fresh instruction. The file must be a JSON object shaped like an
   onec-ops adapter result (status, evidence, message, ...); this script does not re-run or
   re-verify the underlying Unica call, it only carries the agent's own report into the onec-ops
   report trail so it is hashed and recorded like any other result.

extension.list is deliberately NOT declared in provider.json for this adapter: the inspected
public Unica contract's `operation=extensions` synchronizes configured properties and is
MUTATING, not a list API (global/skills/1c-init-project/references/test-setup.md:16). Treating it
as a list route here would silently mutate on what looks like a read.
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

function Get-UOParam { param($Params, [string]$Name, $Default) $p = $Params.PSObject.Properties[$Name]; if ($null -ne $p -and $null -ne $p.Value) { return $p.Value }; return $Default }
function Get-UOProp { param($Object, [string]$Name, $Default) $p = $Object.PSObject.Properties[$Name]; if ($null -ne $p -and $null -ne $p.Value) { return $p.Value }; return $Default }

if (-not [string]::IsNullOrWhiteSpace($ImportResult)) {
    if (-not (Test-Path -LiteralPath $ImportResult -PathType Leaf)) { throw "BF_INVALID: ImportResult file was not found: $ImportResult" }
    $imported = Get-Content -Raw -LiteralPath $ImportResult -Encoding UTF8 | ConvertFrom-Json -ErrorAction Stop
    $status = [string](Get-UOProp $imported 'status' $null)
    if ($status -notin @('PASS', 'FAIL', 'BLOCKED')) { throw "BF_INVALID: ImportResult status must be PASS, FAIL or BLOCKED, got: $status" }
    return [pscustomobject]@{
        status     = $status
        mutating   = $true
        evidence   = @(Get-UOProp $imported 'evidence' @())
        message    = Get-UOProp $imported 'message' 'Imported from agent-observed Unica outcome'
        raw_output = ($imported | ConvertTo-Json -Depth 20)
        target     = Get-UOParam $Params 'target' (Get-UOProp $imported 'target' $null)
    }
}

$operationByCapability = @{
    'extension.load' = 'extensions'
    'test.yaxunit'    = 'tests'
    'test.vanessa'    = 'tests'
}
$operation = $operationByCapability[$Capability]
if ($null -eq $operation) { return [pscustomobject]@{ status = 'BLOCKED'; mutating = $true; evidence = @(); message = "BF_INVALID: unica adapter does not implement capability $Capability"; target = (Get-UOParam $Params 'target' $null) } }

$arguments = [ordered]@{ operation = $operation; mode = 'load'; dryRun = $true }
switch ($Capability) {
    'extension.load' {
        $arguments.mode = 'load'
        $arguments.path = Get-UOParam $Params 'cfe_path' $null
        $arguments.extension = Get-UOParam $Params 'extension' $null
    }
    default {
        $arguments.mode = 'run'
        $arguments.settings = Get-UOParam $Params 'settings' $null
        $arguments.modules = @(Get-UOParam $Params 'modules' @())
    }
}

[pscustomobject]@{
    status     = 'BLOCKED'
    mutating   = $true
    evidence   = @()
    message    = 'Unica preview only (dryRun:true). The agent must call the named MCP tool, obtain authorization for the real mutation, then re-invoke onec-ops with -ImportResult to record the observed outcome.'
    raw_output = ($arguments | ConvertTo-Json -Depth 10)
    target     = Get-UOParam $Params 'target' $null
    agent_tool = [ordered]@{
        agent_tool = 'unica.runtime.execute'
        arguments  = $arguments
    }
}
