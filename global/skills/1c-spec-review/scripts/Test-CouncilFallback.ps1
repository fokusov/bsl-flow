#Requires -Version 7.0
[CmdletBinding()]
param([string]$PackageRoot)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($PackageRoot)) { $PackageRoot = Split-Path -Parent (Split-Path -Parent (Split-Path -Parent (Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)))) }
$skill = Join-Path $PackageRoot 'global\skills\1c-spec-review'
. (Join-Path $skill 'scripts\Council.Fallback.ps1')
. (Join-Path $skill 'scripts\Council.Validation.ps1')

$passed = 0
function Assert-True([bool]$Condition, [string]$Message) {
    if (-not $Condition) { throw "FAIL: $Message" }
    $script:passed++
    "PASS $Message"
}
function Assert-Blocked([scriptblock]$Block, [string]$Message) {
    try { & $Block } catch {
        if ([string]$_.Exception.Message -notmatch 'BF_BLOCKED') { throw "FAIL ${Message}: wrong error: $($_.Exception.Message)" }
        $script:passed++; "PASS $Message"; return
    }
    throw "FAIL (no block): $Message"
}

$attempt = [pscustomobject][ordered]@{
    role = 'intent_critic'
    binding = [pscustomobject][ordered]@{
        provider = 'deepseek'; model = 'deepseek-flash'; effort = 'medium'
        input_hashes = [pscustomobject][ordered]@{ original_task_sha256 = ('a' * 64); spec_sha256 = ('b' * 64) }
    }
}
$good = [pscustomobject][ordered]@{
    capability_version = 'fixture-current-agent-v1'; fresh_context = $true; sealed = $true; terminal = $true
    model = 'current-model'; effort = 'medium'; provider = 'current_agent'; source = 'fixture-host-proof'
    executable_sha256 = ('a' * 64); sandbox_sha256 = ('b' * 64); catalog_sha256 = ('c' * 64); skills_sha256 = ('d' * 64)
    catalog_source_path = 'C:\fixture\critic-catalog-source.json'; catalog_source_sha256 = ('e' * 64)
}

# S/M/L without tokens route to fallback with an explicit reason.
$route = Assert-BSLFlowCouncilFallbackPolicy -Fallback 'current_agent' -Credential ([pscustomobject]@{ credential_source = 'missing' }) -Capability $good
Assert-True ($route.route -eq 'current_agent_fallback' -and $route.reason -eq 'credential_missing') 'missing credential routes to fallback with reason'

# fallback: block gives BLOCKED before any dispatch.
Assert-Blocked { Assert-BSLFlowCouncilFallbackPolicy -Fallback 'block' -Credential ([pscustomobject]@{ credential_source = 'missing' }) -Capability $good } 'fallback block is BLOCKED without dispatch'

# No capability receipt means BLOCKED, never inline continuation.
Assert-Blocked { Assert-BSLFlowCouncilFallbackPolicy -Fallback 'current_agent' -Credential ([pscustomobject]@{ credential_source = 'missing' }) -Capability $null } 'fallback without capability is BLOCKED'

# Stale context cannot be reused as fresh.
$stale = [pscustomobject][ordered]@{ fresh_context = $false; sealed = $true; terminal = $true; model = 'm'; effort = 'medium' }
Assert-Blocked { Assert-BSLFlowCouncilCapability $stale } 'non-fresh context is BLOCKED'
$unsealed = [pscustomobject][ordered]@{ fresh_context = $true; sealed = $false; terminal = $true; model = 'm'; effort = 'medium' }
Assert-Blocked { Assert-BSLFlowCouncilCapability $unsealed } 'unsealed capability is BLOCKED'

# Stable binding hashes are part of the capability proof; missing one is never
# repaired from the declared request or from a default value.
$missingHash = [pscustomobject][ordered]@{}
foreach ($property in @($good.PSObject.Properties)) { if ($property.Name -cne 'skills_sha256') { $missingHash | Add-Member -NotePropertyName $property.Name -NotePropertyValue $property.Value } }
Assert-Blocked { Assert-BSLFlowCouncilCapability $missingHash } 'capability without skill proof is BLOCKED'

# Fallback envelope records requested vs observed and stays visible.
$envelope = New-BSLFlowCouncilFallbackEnvelope -Attempt $attempt -Capability $good -Status 'completed' -FallbackReason 'credential_missing' -PayloadSha256 ('c' * 64)
Assert-True ($envelope.execution_mode -eq 'current_agent_fallback' -and $envelope.requested.model -eq 'deepseek-flash' -and $envelope.observed.model -eq 'current-model') 'fallback envelope keeps requested vs observed'
$diversity = Get-BSLFlowCouncilDiversity @($envelope, ($envelope | ConvertTo-Json -Depth 10 | ConvertFrom-Json))
Assert-True ($diversity.diversity -eq 'multi_role_single_model' -and $diversity.fallback_visible) 'fallback pair is never multi_model'

"ALL_STAGE4_FALLBACK_PASSED=$passed"
