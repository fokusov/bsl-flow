#Requires -Version 7.0
Set-StrictMode -Version Latest

# Council independence admission (review.council.independence).
#
#   distinct_models (default) - the chair's resolved model differs from every
#       enabled critic's model, and when two or more critics are enabled they
#       use at least two distinct resolved models.
#   distinct_providers - the same rule by provider, where two roles share a
#       provider when they name the same provider entry OR dial the same
#       endpoint host (aliases of one endpoint are not independent). The model
#       rule applies as well: distinct providers are never weaker than
#       distinct models.
#   any - no admission block. When the distinct_models rule does not hold the
#       published review carries limitations: ["single_model_council"] and a
#       chair PASS is published as PASS_WITH_LIMITATIONS.
#
# The check reads only controller bindings (configured provider/model after
# the profile merge and local overlay; for a tokenless current-agent fallback
# role, the host model of its trusted capability receipt), never model output,
# and runs before any paid dispatch. Brainstorm is generative, not a critic,
# and is excluded.

$script:BSLFlowCouncilIndependenceModes = @('distinct_models', 'distinct_providers', 'any')
$script:BSLFlowCouncilCriticRoles = @('intent_critic', 'architecture_critic', 'executability_critic')

function Get-BSLFlowCouncilIndependenceMode {
    param([Parameter(Mandatory)]$Council)
    $mode = $null
    if ($Council -is [System.Collections.IDictionary]) {
        if ($Council.Contains('independence')) { $mode = $Council['independence'] }
    }
    elseif ($null -ne $Council.PSObject.Properties['independence']) { $mode = $Council.independence }
    if ([string]::IsNullOrWhiteSpace([string]$mode)) { return 'distinct_models' }
    if ([string]$mode -cnotin $script:BSLFlowCouncilIndependenceModes) {
        throw "BF_INVALID: unknown review.council.independence mode: $mode"
    }
    return [string]$mode
}

function Get-BSLFlowCouncilIndependenceViolations {
    # Returns the list of rule violations for one identity kind. Identities are
    # compared case-insensitively; each role maps to a set of identity keys so a
    # provider can be matched by name or endpoint host.
    param(
        [Parameter(Mandatory)]$Chair,
        [Parameter(Mandatory)][AllowEmptyCollection()][object[]]$Critics,
        [Parameter(Mandatory)][ValidateSet('model', 'provider')][string]$Kind
    )
    $violations = [System.Collections.Generic.List[string]]::new()
    $label = if ($Kind -ceq 'model') { 'model' } else { 'provider' }
    foreach ($critic in $Critics) {
        $shared = @($critic.keys[$Kind] | Where-Object { $_ -cin @($Chair.keys[$Kind]) })
        if ($shared.Count -gt 0) {
            $violations.Add(("chair and {0} share {1} '{2}'" -f $critic.role, $label, $critic.display[$Kind]))
        }
    }
    if ($Critics.Count -ge 2) {
        # Union-find over shared keys: critics are one group when any key links them.
        $groups = [System.Collections.Generic.List[object]]::new()
        foreach ($critic in $Critics) {
            $merged = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::Ordinal)
            foreach ($key in @($critic.keys[$Kind])) { [void]$merged.Add($key) }
            foreach ($group in @($groups)) {
                if (@($group | Where-Object { $merged.Contains($_) }).Count -gt 0) {
                    foreach ($key in $group) { [void]$merged.Add($key) }
                    [void]$groups.Remove($group)
                }
            }
            $groups.Add($merged)
        }
        if ($groups.Count -lt 2) {
            $names = @($Critics | ForEach-Object { $_.role }) -join ', '
            $violations.Add(("enabled critics ({0}) all use one {1} '{2}'; at least two distinct {1}s are required" -f $names, $label, $Critics[0].display[$Kind]))
        }
    }
    return @($violations)
}

function Get-BSLFlowCouncilIndependenceAssessment {
    # Pure assessment over role bindings: no throw for rule violations, so the
    # dry-run plan and the admission gate share one decision.
    param(
        [Parameter(Mandatory)]$Council,
        [Parameter(Mandatory)][AllowEmptyCollection()][object[]]$Bindings,
        [hashtable]$Capabilities
    )
    $mode = Get-BSLFlowCouncilIndependenceMode -Council $Council
    $identities = @{}
    foreach ($entry in @($Bindings)) {
        $roleName = [string]$entry.role_name
        if ($roleName -cnotin @($script:BSLFlowCouncilCriticRoles + @('chair'))) { continue }
        $binding = $entry.binding
        $model = ([string]$binding.model).Trim()
        $provider = ([string]$binding.provider).Trim()
        $endpointHost = ''
        try { $endpointHost = ([string]$binding.endpoint.host).Trim() } catch { $endpointHost = '' }
        # A tokenless role served by the current-agent fallback resolves to the
        # host model stated by its trusted capability receipt, not to the
        # configured profile: several fallback roles collapse onto one model.
        $credentialSource = ''
        try { $credentialSource = [string]$entry.credential.credential_source } catch { $credentialSource = '' }
        if ($credentialSource -ceq 'missing' -and $null -ne $Capabilities -and $Capabilities.ContainsKey($roleName) -and $null -ne $Capabilities[$roleName]) {
            $capabilityModel = ''
            try { $capabilityModel = ([string]$Capabilities[$roleName].model).Trim() } catch { $capabilityModel = '' }
            if (-not [string]::IsNullOrWhiteSpace($capabilityModel)) {
                $model = $capabilityModel
                $provider = 'current_agent'
                $endpointHost = ''
            }
        }
        if ([string]::IsNullOrWhiteSpace($model) -or [string]::IsNullOrWhiteSpace($provider)) {
            throw "BF_BLOCKED: council independence cannot be assessed: role $roleName has no resolved provider/model binding."
        }
        $providerKeys = @('name:' + $provider.ToLowerInvariant())
        if (-not [string]::IsNullOrWhiteSpace($endpointHost)) { $providerKeys += ('host:' + $endpointHost.ToLowerInvariant()) }
        $identities[$roleName] = [pscustomobject]@{
            role = $roleName
            keys = @{ model = @('model:' + $model.ToLowerInvariant()); provider = $providerKeys }
            display = @{ model = $model; provider = $(if ($endpointHost) { "$provider ($endpointHost)" } else { $provider }) }
        }
    }
    if (-not $identities.ContainsKey('chair')) { throw 'BF_BLOCKED: council independence cannot be assessed without an enabled chair binding.' }
    $critics = @($script:BSLFlowCouncilCriticRoles | Where-Object { $identities.ContainsKey($_) } | ForEach-Object { $identities[$_] })
    $modelViolations = @(Get-BSLFlowCouncilIndependenceViolations -Chair $identities['chair'] -Critics $critics -Kind 'model')
    $providerViolations = @(Get-BSLFlowCouncilIndependenceViolations -Chair $identities['chair'] -Critics $critics -Kind 'provider')
    $violations = @()
    if ($mode -ceq 'distinct_models') { $violations = @($modelViolations) }
    elseif ($mode -ceq 'distinct_providers') { $violations = @($providerViolations) + @($modelViolations) }
    $limitations = @()
    if ($mode -ceq 'any' -and $modelViolations.Count -gt 0) { $limitations = @('single_model_council') }
    return [pscustomobject][ordered]@{
        mode = $mode
        admitted = ($violations.Count -eq 0)
        violations = @($violations)
        limitations = @($limitations)
    }
}

function Assert-BSLFlowCouncilIndependence {
    # Admission gate: runs before any paid call. Returns the assessment so the
    # cycle can publish the limitations of an explicitly accepted `any` council.
    param(
        [Parameter(Mandatory)]$Council,
        [Parameter(Mandatory)][AllowEmptyCollection()][object[]]$Bindings,
        [hashtable]$Capabilities
    )
    $assessment = Get-BSLFlowCouncilIndependenceAssessment -Council $Council -Bindings $Bindings -Capabilities $Capabilities
    if (-not [bool]$assessment.admitted) {
        throw ("BF_BLOCKED: council independence '{0}' violated: {1}. Bind critics and the chair to different model profiles (llm.models / review.council.roles.<role>.model), or set review.council.independence: any to accept a limited council." -f $assessment.mode, (@($assessment.violations) -join '; '))
    }
    return $assessment
}

function Get-BSLFlowCouncilPublishedVerdict {
    # A council with limitations can never publish a clean PASS.
    param(
        [Parameter(Mandatory)][string]$ChairVerdict,
        [AllowEmptyCollection()][string[]]$Limitations = @()
    )
    if ($ChairVerdict -ceq 'PASS' -and @($Limitations).Count -gt 0) { return 'PASS_WITH_LIMITATIONS' }
    return $ChairVerdict
}
