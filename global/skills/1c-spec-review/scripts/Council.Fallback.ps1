#Requires -Version 7.0
Set-StrictMode -Version Latest

# Stage 4: current_agent fresh-context fallback contract.
# No inline continuation, no silent substitution with another default model.
# Every fallback dispatch needs a trusted capability receipt before use.

function Assert-BSLFlowCouncilCapability {
    param([Parameter(Mandatory)]$Capability)
    $read = {
        param($Object, [string]$Name)
        if ($null -eq $Object) { return $null }
        if ($Object -is [System.Collections.IDictionary]) {
            if ($Object.Contains($Name)) { return $Object[$Name] }
            return $null
        }
        $property = $Object.PSObject.Properties[$Name]
        if ($null -ne $property) { return $property.Value }
        return $null
    }
    foreach ($field in @('capability_version', 'provider', 'model', 'effort', 'fresh_context', 'sealed', 'terminal', 'source', 'executable_sha256', 'sandbox_sha256', 'catalog_sha256', 'skills_sha256', 'catalog_source_path', 'catalog_source_sha256')) {
        if ($null -eq (& $read $Capability $field)) { throw "BF_BLOCKED: fallback capability misses field: $field." }
    }
    if ([bool]$Capability.fresh_context -ne $true) { throw 'BF_BLOCKED: fallback requires a fresh model context.' }
    if ([bool]$Capability.sealed -ne $true) { throw 'BF_BLOCKED: fallback requires a sealed no-tools input.' }
    if ([bool]$Capability.terminal -ne $true) { throw 'BF_BLOCKED: fallback requires a terminal capability receipt.' }
    if ([string]$Capability.provider -cne 'current_agent') { throw 'BF_BLOCKED: fallback capability provider is not the current-agent adapter.' }
    if ([string]::IsNullOrWhiteSpace([string]$Capability.capability_version) -or [string]::IsNullOrWhiteSpace([string]$Capability.source)) { throw 'BF_BLOCKED: fallback capability must identify its sealed host proof.' }
    if ([string]::IsNullOrWhiteSpace([string]$Capability.model)) { throw 'BF_BLOCKED: fallback capability must state the actual model.' }
    if ([string]::IsNullOrWhiteSpace([string]$Capability.effort)) { throw 'BF_BLOCKED: fallback capability must state the actual effort.' }
    if ([string]::IsNullOrWhiteSpace([string]$Capability.catalog_source_path) -or
        [string]$Capability.catalog_source_sha256 -notmatch '^[0-9a-f]{64}$') {
        throw 'BF_BLOCKED: fallback capability must bind an exact critic catalog source.'
    }
    foreach ($field in @('executable_sha256', 'sandbox_sha256', 'catalog_sha256', 'skills_sha256')) {
        if ([string]$Capability.$field -notmatch '^[0-9a-f]{64}$') { throw "BF_BLOCKED: fallback capability has no valid $field proof." }
    }
    return [ordered]@{ model = [string]$Capability.model; effort = [string]$Capability.effort }
}

function Get-BSLFlowCouncilCapabilityBinding {
    <#
      Return the stable, non-secret portion of a host capability receipt. A
      session/turn id identifies one terminal invocation and therefore belongs
      in that receipt, never in the reusable council attempt binding. Resolved
      model/effort and capability/catalog/executable versions do belong there:
      changing the host contract must create a new attempt before cache lookup.
    #>
    param([Parameter(Mandatory)]$Capability)
    $actual = Assert-BSLFlowCouncilCapability $Capability
    $value = {
        param($Object, [string]$Name, $Default)
        if ($null -eq $Object) { return $Default }
        if ($Object -is [System.Collections.IDictionary] -and $Object.Contains($Name)) { return $Object[$Name] }
        $property = $Object.PSObject.Properties[$Name]
        if ($null -eq $property -or $null -eq $property.Value) { return $Default }
        return $property.Value
    }
    $stable = [ordered]@{
        capability_version = [string](& $value $Capability 'capability_version' 'council-current-agent-v1')
        provider = [string](& $value $Capability 'provider' 'current_agent')
        model = $actual.model
        effort = $actual.effort
        fresh_context = [bool]$Capability.fresh_context
        sealed = [bool]$Capability.sealed
        terminal = [bool]$Capability.terminal
        source = [string](& $value $Capability 'source' 'host_receipt')
        executable_sha256 = [string](& $value $Capability 'executable_sha256' '')
        sandbox_sha256 = [string](& $value $Capability 'sandbox_sha256' '')
        catalog_sha256 = [string](& $value $Capability 'catalog_sha256' '')
        skills_sha256 = [string](& $value $Capability 'skills_sha256' '')
        catalog_source_path = [string](& $value $Capability 'catalog_source_path' '')
        catalog_source_sha256 = [string](& $value $Capability 'catalog_source_sha256' '')
    }
    $json = $stable | ConvertTo-Json -Depth 10 -Compress
    if ($json -match '(?i)token|authorization|password|secret') { throw 'BF_BLOCKED: fallback capability binding contains credential material.' }
    return [ordered]@{
        sha256 = Get-BSLFlowBytesSha256 ([System.Text.Encoding]::UTF8.GetBytes($json))
        identity = [pscustomobject]$stable
    }
}

function New-BSLFlowCouncilFallbackEnvelope {
    param(
        [Parameter(Mandatory)]$Attempt,
        [Parameter(Mandatory)]$Capability,
        [Parameter(Mandatory)][ValidateSet('completed', 'failed_before_acceptance', 'unknown_after_dispatch', 'cancelled', 'invalid_response')][string]$Status,
        [Parameter(Mandatory)][string]$FallbackReason,
        [string]$PayloadSha256
    )
    $actual = Assert-BSLFlowCouncilCapability $Capability
    if ([string]::IsNullOrWhiteSpace($FallbackReason)) { throw 'Fallback envelope requires an explicit fallback reason.' }
    $provider = 'current_agent'
    try { if (-not [string]::IsNullOrWhiteSpace([string]$Capability.provider)) { $provider = [string]$Capability.provider } } catch { }
    return [pscustomobject][ordered]@{
        role = [string]$Attempt.role
        status = $Status
        requested = [pscustomobject][ordered]@{
            provider = [string]$Attempt.binding.provider
            model = [string]$Attempt.binding.model
            effort = [string]$Attempt.binding.effort
        }
        observed = [pscustomobject][ordered]@{ provider = $provider; model = $actual.model; effort = $actual.effort }
        execution_mode = 'current_agent_fallback'
        fallback_reason = $FallbackReason
        input_hashes = $Attempt.binding.input_hashes
        payload_sha256 = $PayloadSha256
    }
}

function Assert-BSLFlowCouncilFallbackPolicy {
    param(
        [Parameter(Mandatory)][string]$Fallback,
        [Parameter(Mandatory)]$Credential,
        $Capability
    )
    if ($Fallback -cnotin @('current_agent', 'block')) { throw "Invalid fallback policy: $Fallback" }
    if ($Credential.credential_source -cne 'missing') { return [ordered]@{ route = 'direct_api' } }
    if ($Fallback -ceq 'block') { throw 'BF_BLOCKED: role credential is missing and fallback policy is block.' }
    if ($null -eq $Capability) { throw 'BF_BLOCKED: fallback needs a trusted capability receipt before dispatch.' }
    $null = Assert-BSLFlowCouncilCapability $Capability
    return [ordered]@{ route = 'current_agent_fallback'; reason = 'credential_missing' }
}
