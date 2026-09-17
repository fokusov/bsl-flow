#Requires -Version 7.0
Set-StrictMode -Version Latest

# Stage 0: portable council configuration contract.
# No network, no secrets, no state machine changes.
# Committed bsl-flow.yaml contains only non-secret values.
# Literal token is allowed only in ignored .bsl-flow/providers.local.yaml (validated separately).

function Get-BSLFlowCouncilVersions {
    return [ordered]@{
        council_schema_version = 1
        transport_capability_version = 1
        prompt_version = 'council-prompt-v1'
        member_schema_version = 1
        review_schema_version = 2
    }
}

function Get-BSLFlowKnownProviderEndpoint {
    param([Parameter(Mandatory)][string]$Name)
    switch ($Name.ToLowerInvariant()) {
        'openai' { return 'https://api.openai.com/v1' }
        'deepseek' { return 'https://api.deepseek.com' }
        default { return $null }
    }
}

function Get-BSLFlowYamlChildren {
    param(
        [Parameter(Mandatory)][AllowEmptyString()][string]$Text,
        [Parameter(Mandatory)][string[]]$Path
    )
    $children = [System.Collections.Generic.List[string]]::new()
    $stack = [System.Collections.Generic.List[object]]::new()
    foreach ($line in ($Text -split "`r?`n")) {
        if ($line -match '^\s*(?:#.*)?$') { continue }
        if ($line -notmatch '^(?<indent>\s*)(?<key>[A-Za-z0-9_-]+):(?:\s*(?<value>.*?))?\s*$') { continue }
        if ($Matches.indent.Contains("`t")) { throw 'Tabs are not supported in bsl-flow.yaml indentation.' }
        $indent = $Matches.indent.Length
        while ($stack.Count -gt 0 -and $stack[$stack.Count - 1].Indent -ge $indent) { $stack.RemoveAt($stack.Count - 1) }
        $keys = @($stack | ForEach-Object { $_.Key }) + @($Matches.key)
        $value = $Matches.value.Trim()
        if ((-not $value) -and ($keys.Count -eq ($Path.Count + 1))) {
            $parent = @($keys | Select-Object -SkipLast 1)
            $same = $true
            if ($parent.Count -ne $Path.Count) { $same = $false }
            else { for ($i = 0; $i -lt $Path.Count; $i++) { if ($parent[$i] -cne $Path[$i]) { $same = $false; break } } }
            if ($same -and $Matches.key -cnotin $children) { $children.Add($Matches.key) }
        }
        if (-not $value) { $stack.Add([pscustomobject]@{ Indent = $indent; Key = $Matches.key }) }
    }
    return @($children)
}

function Get-BSLFlowYamlDirectKeys {
    param(
        [Parameter(Mandatory)][AllowEmptyString()][string]$Text,
        [Parameter(Mandatory)][string[]]$Path
    )
    # Direct child keys of a mapping node, scalar or map valued.
    # Unknown council fields are rejected; this enumerator makes that check exact.
    $keys = [System.Collections.Generic.List[string]]::new()
    $stack = [System.Collections.Generic.List[object]]::new()
    foreach ($line in ($Text -split "`r?`n")) {
        if ($line -match '^\s*(?:#.*)?$') { continue }
        if ($line -notmatch '^(?<indent>\s*)(?<key>[A-Za-z0-9_-]+):(?:\s*(?<value>.*?))?\s*$') { continue }
        if ($Matches.indent.Contains("`t")) { throw 'Tabs are not supported in bsl-flow.yaml indentation.' }
        $indent = $Matches.indent.Length
        while ($stack.Count -gt 0 -and $stack[$stack.Count - 1].Indent -ge $indent) { $stack.RemoveAt($stack.Count - 1) }
        $parent = @($stack | ForEach-Object { $_.Key })
        $same = ($parent.Count -eq $Path.Count)
        if ($same) { for ($i = 0; $i -lt $Path.Count; $i++) { if ($parent[$i] -cne $Path[$i]) { $same = $false; break } } }
        if ($same -and $Matches.key -cnotin $keys) { $keys.Add($Matches.key) }
        if (-not $Matches.value.Trim()) { $stack.Add([pscustomobject]@{ Indent = $indent; Key = $Matches.key }) }
    }
    return @($keys)
}

function Assert-BSLFlowCouncilNoUnknownKeys {
    param([Parameter(Mandatory)][AllowEmptyString()][string]$Text)
    $allowed = @{
        'llm' = @('providers', 'models')
        'review.council' = @('enabled', 'max_parallel', 'allow_local_http', 'legacy_mode', 'roles', 'budget', 'request_timeout_seconds')
    }
    foreach ($path in @(@('llm'), @('review', 'council'))) {
        $name = ($path -join '.')
        if ((Get-BSLFlowYamlValue $Text ($path + @('__missing__')) '__absent__') -eq '__absent__' -and @(Get-BSLFlowYamlDirectKeys $Text $path).Count -eq 0) { continue }
        foreach ($key in @(Get-BSLFlowYamlDirectKeys $Text $path)) {
            if ($key -cnotin $allowed[$name]) { throw "Unknown council configuration field: ${name}.${key}." }
        }
    }
    foreach ($providerName in @(Get-BSLFlowYamlChildren $Text @('llm', 'providers'))) {
        foreach ($key in @(Get-BSLFlowYamlDirectKeys $Text @('llm', 'providers', $providerName))) {
            if ($key -cnotin @('protocol', 'base_url', 'token_env')) { throw "Unknown provider field: llm.providers.${providerName}.${key}." }
        }
    }
    foreach ($modelName in @(Get-BSLFlowYamlChildren $Text @('llm', 'models'))) {
        foreach ($key in @(Get-BSLFlowYamlDirectKeys $Text @('llm', 'models', $modelName))) {
            if ($key -cnotin @('provider', 'model', 'effort', 'cost_estimate_usd')) { throw "Unknown model field: llm.models.${modelName}.${key}." }
        }
    }
    foreach ($role in @('brainstorm', 'intent_critic', 'architecture_critic', 'executability_critic', 'chair')) {
        foreach ($key in @(Get-BSLFlowYamlDirectKeys $Text @('review', 'council', 'roles', $role))) {
            if ($key -cnotin @('enabled', 'required', 'model', 'fallback')) { throw "Unknown role field: review.council.roles.${role}.${key}." }
        }
    }
    foreach ($key in @(Get-BSLFlowYamlDirectKeys $Text @('review', 'council', 'budget'))) {
        if ($key -cnotin @('currency', 'limit', 'reservation')) { throw "Unknown budget field: review.council.budget.${key}." }
    }
}

function Test-BSLFlowExplicitYamlValue {
    param([Parameter(Mandatory)][AllowEmptyString()][string]$Text, [Parameter(Mandatory)][string[]]$Path)
    foreach ($line in ($Text -split "`r?`n")) {
        if ($line -match '^\s*(?:#.*)?$') { continue }
        if ($line -notmatch '^(?<indent>\s*)(?<key>[A-Za-z0-9_-]+):(?:\s*(?<value>.*?))?\s*$') { continue }
        # Cheap explicit-presence check is enough for the legacy migration blocker.
        # Full path tracking is done by Get-BSLFlowYamlValue / Get-BSLFlowYamlChildren.
        if ($Path.Count -eq 3 -and $Path[0] -eq 'review' -and $Path[1] -eq 'reviewer' -and $Path[2] -eq 'provider') {
            if ($Matches.key -ceq 'provider' -and $Matches.value.Trim() -match '^(["'']?)opencode\1\s*$') { return $true }
        }
    }
    return $false
}

function Assert-BSLFlowEndpointUrl {
    param([Parameter(Mandatory)][string]$Url, [Parameter(Mandatory)][string]$Name, [bool]$AllowLocalHttp = $false)

    $uri = $null
    try { $uri = [System.Uri]$Url } catch { throw "Invalid endpoint URL for ${Name}: $Url" }
    if ($uri.Scheme -notin @('https', 'http')) { throw "Endpoint must use https for ${Name}: $Url" }
    if ($uri.UserInfo) { throw "Endpoint must not contain userinfo for ${Name}." }
    if ($uri.Fragment) { throw "Endpoint must not contain fragment for ${Name}." }
    if ($Url -match '\?') { throw "Endpoint must not contain query for ${Name}." }
    if ([string]::IsNullOrWhiteSpace($uri.Host)) { throw "Endpoint must have a host for ${Name}." }

    $isLoopback = $uri.Host -eq 'localhost' -or $uri.Host -eq '127.0.0.1' -or $uri.Host -eq '::1'
    if ($uri.Scheme -eq 'http') {
        if (-not ($isLoopback -and $AllowLocalHttp)) {
            throw "Plain HTTP endpoint is allowed only for loopback with an explicit local-development flag: $Name."
        }
    }
    $port = if ($uri.IsDefaultPort) { if ($uri.Scheme -eq 'https') { 443 } else { 80 } } else { $uri.Port }
    return [ordered]@{ scheme = $uri.Scheme; host = $uri.Host.ToLowerInvariant(); port = $port; base_path = $uri.AbsolutePath }
}

function Get-BSLFlowCouncilOptionalNumber {
    param([Parameter(Mandatory)][AllowEmptyString()][string]$Text, [Parameter(Mandatory)][string[]]$Path, [Parameter(Mandatory)][string]$Name)
    $raw = Get-BSLFlowYamlValue $Text $Path ''
    if ([string]::IsNullOrWhiteSpace($raw)) { return $null }
    $number = 0.0
    if (-not [double]::TryParse($raw, [System.Globalization.NumberStyles]::Float, [System.Globalization.CultureInfo]::InvariantCulture, [ref]$number)) { throw "Invalid ${Name}: must be a number." }
    if ([double]::IsNaN($number) -or [double]::IsInfinity($number) -or $number -lt 0) { throw "Invalid ${Name}: must be a non-negative finite number." }
    return $number
}

function Get-BSLFlowCouncilCostEstimate {
    param([Parameter(Mandatory)][AllowEmptyString()][string]$Text, [Parameter(Mandatory)][string]$ModelName)
    return (Get-BSLFlowCouncilOptionalNumber $Text @('llm', 'models', $ModelName, 'cost_estimate_usd') "llm.models.${ModelName}.cost_estimate_usd")
}

function Get-BSLFlowCouncilPolicy {
    param([AllowEmptyString()][string]$ConfigText = '')

    . (Join-Path $PSScriptRoot 'Review.Common.ps1')

    $versions = Get-BSLFlowCouncilVersions
    $culture = [Globalization.CultureInfo]::InvariantCulture

    $councilEnabled = ConvertTo-BSLFlowBoolean (Get-BSLFlowYamlValue $ConfigText @('review', 'council', 'enabled') 'false') 'review.council.enabled'
    $maxParallel = [int]::Parse((Get-BSLFlowYamlValue $ConfigText @('review', 'council', 'max_parallel') '2'), $culture)
    if ($maxParallel -lt 1 -or $maxParallel -gt 8) { throw 'review.council.max_parallel must be between 1 and 8.' }
    $allowLocalHttp = ConvertTo-BSLFlowBoolean (Get-BSLFlowYamlValue $ConfigText @('review', 'council', 'allow_local_http') 'false') 'review.council.allow_local_http'
    $legacyMode = Get-BSLFlowYamlValue $ConfigText @('review', 'council', 'legacy_mode') 'block'
    if ($legacyMode -notin @('block', 'opencode_compat')) { throw "Invalid review.council.legacy_mode: $legacyMode" }
    Assert-BSLFlowCouncilNoUnknownKeys $ConfigText

    # Committed config must never carry a literal secret. Local overlay is a separate ignored file.
    foreach ($providerName in @(Get-BSLFlowYamlChildren $ConfigText @('llm', 'providers'))) {
        $literal = Get-BSLFlowYamlValue $ConfigText @('llm', 'providers', $providerName, 'token') ''
        if (-not [string]::IsNullOrWhiteSpace($literal)) { throw "Literal token is forbidden in committed config: llm.providers.$providerName.token" }
    }
    if ($ConfigText -match '(?im)^\s*Authorization\s*:') { throw 'Authorization header must not appear in committed config.' }

    # Providers.
    $providers = [ordered]@{}
    foreach ($providerName in @(Get-BSLFlowYamlChildren $ConfigText @('llm', 'providers'))) {
        if ($providerName -cnotmatch '^[A-Za-z0-9][A-Za-z0-9_-]{0,31}$') { throw "Unsafe provider id: $providerName" }
        $protocol = Get-BSLFlowYamlValue $ConfigText @('llm', 'providers', $providerName, 'protocol') ''
        if ($protocol -notin @('openai_responses', 'openai_compatible')) { throw "Invalid protocol for llm.providers.$providerName." }
        $baseUrl = Get-BSLFlowYamlValue $ConfigText @('llm', 'providers', $providerName, 'base_url') ''
        $tokenEnv = Get-BSLFlowYamlValue $ConfigText @('llm', 'providers', $providerName, 'token_env') ''
        if ($tokenEnv -and $tokenEnv -cnotmatch '^[A-Za-z_][A-Za-z0-9_]*$') { throw "Invalid token_env for llm.providers.$providerName." }
        $knownDefault = Get-BSLFlowKnownProviderEndpoint $providerName
        if ([string]::IsNullOrWhiteSpace($baseUrl)) {
            if ($null -eq $knownDefault) { throw "Custom provider requires base_url: $providerName." }
            $baseUrl = $knownDefault
        }
        $endpoint = Assert-BSLFlowEndpointUrl -Url $baseUrl -Name "llm.providers.$providerName" -AllowLocalHttp $allowLocalHttp
        $providers[$providerName] = [ordered]@{
            protocol = $protocol; base_url = $baseUrl; token_env = $tokenEnv
            endpoint = $endpoint; transport_capability_version = $versions.transport_capability_version
        }
    }

    # Models.
    $models = [ordered]@{}
    foreach ($modelName in @(Get-BSLFlowYamlChildren $ConfigText @('llm', 'models'))) {
        if ($modelName -cnotmatch '^[A-Za-z0-9][A-Za-z0-9_-]{0,31}$') { throw "Unsafe model profile id: $modelName" }
        $provider = Get-BSLFlowYamlValue $ConfigText @('llm', 'models', $modelName, 'provider') ''
        if ([string]::IsNullOrWhiteSpace($provider) -or $provider -cnotin @($providers.Keys)) { throw "Model profile references unknown provider: $modelName." }
        $modelId = Get-BSLFlowYamlValue $ConfigText @('llm', 'models', $modelName, 'model') ''
        if ($modelId -cnotmatch '^[A-Za-z0-9._:/-]{1,128}$') { throw "Unsafe model id for llm.models.$modelName." }
        $effort = Get-BSLFlowYamlValue $ConfigText @('llm', 'models', $modelName, 'effort') 'medium'
        $effortInt = 0
        if ($effort -cnotin @('low', 'medium', 'high', 'xhigh')) {
            if (-not [int]::TryParse($effort, [ref]$effortInt) -or $effortInt -lt 1 -or $effortInt -gt 10000) {
                throw "Invalid effort for llm.models.$modelName. Use low/medium/high/xhigh or a positive integer."
            }
        }
        $models[$modelName] = [ordered]@{ provider = $provider; model = $modelId; effort = $effort; cost_estimate_usd = (Get-BSLFlowCouncilCostEstimate $ConfigText $modelName) }
    }

    # Optional council budget. When a limit is present the cycle admits the full
    # dispatch set before the first call; unknown per-role costs block admission.
    $budget = $null
    if (@(Get-BSLFlowYamlDirectKeys $ConfigText @('review', 'council', 'budget')).Count -gt 0) {
        $currency = Get-BSLFlowYamlValue $ConfigText @('review', 'council', 'budget', 'currency') 'USD'
        if ($currency -cne 'USD') { throw 'review.council.budget.currency must be USD.' }
        $budget = [ordered]@{
            currency = 'USD'
            limit = (Get-BSLFlowCouncilOptionalNumber $ConfigText @('review', 'council', 'budget', 'limit') 'review.council.budget.limit')
            reservation = (Get-BSLFlowCouncilOptionalNumber $ConfigText @('review', 'council', 'budget', 'reservation') 'review.council.budget.reservation')
        }
        if ($null -eq $budget.limit -and [double]$budget.reservation -ne 0) { throw 'review.council.budget.reservation must be zero when no monetary limit is enforced.' }
    }

    # Roles.
    $roleNames = @('brainstorm', 'intent_critic', 'architecture_critic', 'executability_critic', 'chair')
    $roles = [ordered]@{}
    foreach ($role in $roleNames) {
        $defaultEnabled = if ($role -eq 'brainstorm') { 'false' } else { 'true' }
        $defaultRequired = if ($role -eq 'brainstorm') { 'false' } else { 'true' }
        if (-not $councilEnabled) { $defaultEnabled = 'false'; $defaultRequired = 'false' }
        $enabled = ConvertTo-BSLFlowBoolean (Get-BSLFlowYamlValue $ConfigText @('review', 'council', 'roles', $role, 'enabled') $defaultEnabled) "review.council.roles.$role.enabled"
        $required = ConvertTo-BSLFlowBoolean (Get-BSLFlowYamlValue $ConfigText @('review', 'council', 'roles', $role, 'required') $defaultRequired) "review.council.roles.$role.required"
        if (-not $councilEnabled) { $enabled = $false; $required = $false }
        $model = Get-BSLFlowYamlValue $ConfigText @('review', 'council', 'roles', $role, 'model') ''
        $fallback = Get-BSLFlowYamlValue $ConfigText @('review', 'council', 'roles', $role, 'fallback') 'current_agent'
        if ($fallback -notin @('current_agent', 'block')) { throw "Invalid fallback for role ${role}." }
        if ($required -and -not $enabled) { throw "Role $role cannot be required while disabled." }
        if ($enabled) {
            if ([string]::IsNullOrWhiteSpace($model)) { throw "Enabled role $role must reference llm.models.<profile>." }
            if ($model -cnotin @($models.Keys)) { throw "Role $role references unknown model profile: $model." }
        }
        $roles[$role] = [ordered]@{ enabled = $enabled; required = $required; model = $model; fallback = $fallback }
    }

    if ($councilEnabled) {
        if (-not $roles['chair'].enabled -or -not $roles['chair'].required) { throw 'Council chair must be enabled and required when council.enabled is true.' }
        # Legacy OpenCode config must not be silently reinterpreted as council.
        $explicitLegacy = Test-BSLFlowExplicitYamlValue -Text $ConfigText -Path @('review', 'reviewer', 'provider')
        if ($explicitLegacy -and $legacyMode -ne 'opencode_compat') {
            throw 'BF_MIGRATION_BLOCKED: explicit review.reviewer.provider opencode cannot be silently reinterpreted. Set review.council.legacy_mode to opencode_compat for the separate compatibility route, or remove the legacy reviewer block to use the API council.'
        }
    }

    $requestTimeout = Get-BSLFlowCouncilOptionalNumber $ConfigText @('review', 'council', 'request_timeout_seconds') 'review.council.request_timeout_seconds'
    if ($null -eq $requestTimeout) { $requestTimeout = 300 }
    if ([double]$requestTimeout -lt 60 -or [double]$requestTimeout -gt 900) { throw 'review.council.request_timeout_seconds must be between 60 and 900.' }

    return [ordered]@{
        council_schema_version = $versions.council_schema_version
        transport_capability_version = $versions.transport_capability_version
        enabled = $councilEnabled; max_parallel = $maxParallel
        allow_local_http = $allowLocalHttp; legacy_mode = $legacyMode
        request_timeout_seconds = [int]$requestTimeout
        providers = $providers; models = $models; roles = $roles; budget = $budget
    }
}

function Get-BSLFlowLocalProviderOverlay {
    param([Parameter(Mandatory)][string]$ProjectPath)
    $localPath = Join-Path $ProjectPath '.bsl-flow/providers.local.yaml'
    if (-not (Test-Path -LiteralPath $localPath -PathType Leaf)) { return [ordered]@{} }
    # Stage 0 defines only the shape: providers.<name>.token / base_url.
    # Secret merge and redaction are implemented with transports in Stage 3.
    $text = Get-Content -Raw -LiteralPath $localPath
    if ($text -match '(?im)^\s*token_env\s*:') { throw 'token_env belongs in committed config, not in providers.local.yaml.' }
    $overlay = [ordered]@{}
    foreach ($providerName in @(Get-BSLFlowYamlChildren $text @('providers'))) {
        $token = Get-BSLFlowYamlValue $text @('providers', $providerName, 'token') ''
        $baseUrl = Get-BSLFlowYamlValue $text @('providers', $providerName, 'base_url') ''
        $overlay[$providerName] = [ordered]@{
            has_token = (-not [string]::IsNullOrWhiteSpace($token))
            has_base_url = (-not [string]::IsNullOrWhiteSpace($baseUrl))
            base_url = $baseUrl
        }
    }
    return $overlay
}
