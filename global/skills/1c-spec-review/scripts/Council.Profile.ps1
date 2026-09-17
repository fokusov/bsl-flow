#Requires -Version 7.0
Set-StrictMode -Version Latest

# User profile council configuration: %USERPROFILE%\.bsl-flow\config.yaml
# (Windows) / $HOME/.bsl-flow/config.yaml (POSIX), overridable with the
# BSL_FLOW_USER_CONFIG environment variable (full file path, must exist).
#
# Priority "profile -> project -> local overlay". The merge is FIELD-LEVEL
# inside each named entity (provider, model profile, role binding): for a
# name present in both sources, only the profile fields whose valued paths
# are absent from the project text are appended, so the project displaces
# exactly the fields it sets and every other profile field survives.
# Entities defined in only one source are included whole, and the project's
# explicit non-empty role model always wins. The local overlay
# .bsl-flow/providers.local.yaml applies after this merge, before role
# bindings and the admission gate, and keeps top priority for
# token/base_url.
#
# Canonical policy hash. When the profile contributed, or the local overlay
# contributes token/base_url for any provider of this config, the policy
# hash is SHA-256 over the canonical JSON of the effective parsed policy:
# recursively ordinal-sorted property keys, restricted to the allowlisted
# parsed policy fields, with the computed fields (provider endpoint,
# transport_capability_version at the root and per provider) removed, no
# insignificant whitespace, UTF-8 bytes without BOM. Without any profile or
# overlay contribution the hash stays SHA-256 of the raw effective (==
# project) text, byte-compatible with previously recorded results.

. (Join-Path $PSScriptRoot 'Review.Common.ps1')
. (Join-Path $PSScriptRoot 'Council.Common.ps1')
. (Join-Path $PSScriptRoot 'Council.Engine.ps1')

function Resolve-BSLFlowUserProfileConfigPath {
    # Environment override must point to an existing file and fails closed
    # otherwise; the default path may be absent, which is a valid empty profile.
    param([string]$HomeDirectory = $HOME)
    $override = [System.Environment]::GetEnvironmentVariable('BSL_FLOW_USER_CONFIG')
    if (-not [string]::IsNullOrWhiteSpace($override)) {
        $fullPath = [System.IO.Path]::GetFullPath($override)
        if (-not (Test-Path -LiteralPath $fullPath -PathType Leaf)) {
            throw "BSL_FLOW_USER_CONFIG points to a missing user profile config file: $fullPath"
        }
        return $fullPath
    }
    return (Join-Path $HomeDirectory '.bsl-flow/config.yaml')
}

function Get-BSLFlowUserProfileText {
    param([Parameter(Mandatory)][string]$Path)
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { return '' }
    $utf8 = [System.Text.UTF8Encoding]::new($false)
    $text = [System.IO.File]::ReadAllText($Path, $utf8)
    # A comment-only or blank profile is a valid empty state: it must behave
    # exactly like an absent file, including the profile_used audit flag.
    foreach ($line in ($text -split "`r?`n")) {
        if ($line -notmatch '^\s*(?:#.*)?$') { return $text }
    }
    return ''
}

function Get-BSLFlowProfileTopLevelKeys {
    # The shared Council.Common walker cannot express the document root (its
    # Mandatory Path rejects an empty path), so depth-0 keys are enumerated
    # directly in the same line-based style.
    param([Parameter(Mandatory)][AllowEmptyString()][string]$Text)
    $keys = [System.Collections.Generic.List[string]]::new()
    foreach ($line in ($Text -split "`r?`n")) {
        if ($line -match '^\s*(?:#.*)?$') { continue }
        if ($line -notmatch '^(?<indent>\s*)(?<key>[A-Za-z0-9_-]+):(?:\s*(?<value>.*?))?\s*$') { continue }
        if ($Matches.indent.Contains("`t")) { throw 'Tabs are not supported in user profile config indentation.' }
        if ($Matches.indent.Length -eq 0 -and $Matches.key -cnotin $keys) { $keys.Add($Matches.key) }
    }
    return @($keys)
}

function Assert-BSLFlowUserProfileConfig {
    # Fail-closed allowlist. Only provider definitions, model profiles and role
    # model bindings may live in the profile; council switches, budgets,
    # routing, reviewer settings and every unknown key stay project-owned.
    param(
        [Parameter(Mandatory)][AllowEmptyString()][string]$Text,
        [Parameter(Mandatory)][string]$Path
    )
    if ([string]::IsNullOrWhiteSpace($Text)) { return }
    if ($Text -match '(?im)^\s*Authorization\s*:') { throw "Authorization header must not appear in user profile config: $Path" }
    foreach ($providerName in @(Get-BSLFlowYamlChildren $Text @('llm', 'providers'))) {
        $literal = Get-BSLFlowYamlValue $Text @('llm', 'providers', $providerName, 'token') ''
        if (-not [string]::IsNullOrWhiteSpace($literal)) { throw "Literal token is forbidden in user profile config: ${Path} (llm.providers.${providerName}.token)" }
    }
    foreach ($key in @(Get-BSLFlowProfileTopLevelKeys $Text)) {
        if ($key -cnotin @('llm', 'review')) { throw "Forbidden key in user profile config: ${Path} ($key)" }
    }
    foreach ($key in @(Get-BSLFlowYamlDirectKeys $Text @('llm'))) {
        if ($key -cnotin @('providers', 'models')) { throw "Forbidden key in user profile config: ${Path} (llm.${key})" }
    }
    foreach ($key in @(Get-BSLFlowYamlDirectKeys $Text @('review'))) {
        if ($key -cne 'council') { throw "Forbidden key in user profile config: ${Path} (review.${key})" }
    }
    foreach ($key in @(Get-BSLFlowYamlDirectKeys $Text @('review', 'council'))) {
        if ($key -cne 'roles') { throw "Forbidden key in user profile config: ${Path} (review.council.${key})" }
    }
    foreach ($role in @(Get-BSLFlowYamlChildren $Text @('review', 'council', 'roles'))) {
        if ($role -cnotin @('brainstorm', 'intent_critic', 'architecture_critic', 'executability_critic', 'chair')) { throw "Forbidden role in user profile config: ${Path} (review.council.roles.${role})" }
        foreach ($key in @(Get-BSLFlowYamlDirectKeys $Text @('review', 'council', 'roles', $role))) {
            if ($key -cne 'model') { throw "Forbidden key in user profile config: ${Path} (review.council.roles.${role}.${key})" }
        }
    }
    foreach ($providerName in @(Get-BSLFlowYamlChildren $Text @('llm', 'providers'))) {
        foreach ($key in @(Get-BSLFlowYamlDirectKeys $Text @('llm', 'providers', $providerName))) {
            if ($key -cnotin @('protocol', 'base_url', 'token_env')) { throw "Forbidden key in user profile config: ${Path} (llm.providers.${providerName}.${key})" }
        }
    }
    foreach ($modelName in @(Get-BSLFlowYamlChildren $Text @('llm', 'models'))) {
        foreach ($key in @(Get-BSLFlowYamlDirectKeys $Text @('llm', 'models', $modelName))) {
            if ($key -cnotin @('provider', 'model', 'effort', 'cost_estimate_usd')) { throw "Forbidden key in user profile config: ${Path} (llm.models.${modelName}.${key})" }
        }
    }
}

function Merge-BSLFlowCouncilPolicyText {
    # Text-level merge with FIELD-LEVEL displacement inside each named entity:
    # the project text stays verbatim and only the profile fields whose valued
    # paths are absent from the project are appended in a canonical serialized
    # form. Get-BSLFlowYamlValue throws on duplicate paths, so appended fields
    # are absent-by-construction; profile-only entities are appended whole and
    # the project's explicit role model always wins. The appended role model
    # blocks must come after the project text so an enabled role without a
    # project model binding resolves through the profile before
    # Get-BSLFlowCouncilPolicy validates the effective policy.
    param(
        [Parameter(Mandatory)][AllowEmptyString()][string]$ProjectText,
        [Parameter(Mandatory)][AllowEmptyString()][string]$ProfileText,
        [Parameter(Mandatory)][string]$ProfilePath
    )
    if ([string]::IsNullOrWhiteSpace($ProfileText)) { return $ProjectText }
    Assert-BSLFlowUserProfileConfig -Text $ProfileText -Path $ProfilePath

    $providerLines = [System.Collections.Generic.List[string]]::new()
    foreach ($providerName in @(Get-BSLFlowYamlChildren $ProfileText @('llm', 'providers'))) {
        $appendedFields = [System.Collections.Generic.List[string]]::new()
        foreach ($field in @('protocol', 'base_url', 'token_env')) {
            $value = Get-BSLFlowYamlValue $ProfileText @('llm', 'providers', $providerName, $field) ''
            if ([string]::IsNullOrWhiteSpace($value)) { continue }
            $projectValue = Get-BSLFlowYamlValue $ProjectText @('llm', 'providers', $providerName, $field) ''
            if (-not [string]::IsNullOrWhiteSpace($projectValue)) { continue }
            $appendedFields.Add("      ${field}: $value")
        }
        if ($appendedFields.Count -eq 0) { continue }
        $providerLines.Add("    ${providerName}:")
        foreach ($line in $appendedFields) { $providerLines.Add($line) }
    }
    $modelLines = [System.Collections.Generic.List[string]]::new()
    foreach ($modelName in @(Get-BSLFlowYamlChildren $ProfileText @('llm', 'models'))) {
        $appendedFields = [System.Collections.Generic.List[string]]::new()
        foreach ($field in @('provider', 'model', 'effort', 'cost_estimate_usd')) {
            $value = Get-BSLFlowYamlValue $ProfileText @('llm', 'models', $modelName, $field) ''
            if ([string]::IsNullOrWhiteSpace($value)) { continue }
            $projectValue = Get-BSLFlowYamlValue $ProjectText @('llm', 'models', $modelName, $field) ''
            if (-not [string]::IsNullOrWhiteSpace($projectValue)) { continue }
            $appendedFields.Add("      ${field}: $value")
        }
        if ($appendedFields.Count -eq 0) { continue }
        $modelLines.Add("    ${modelName}:")
        foreach ($line in $appendedFields) { $modelLines.Add($line) }
    }
    $roleLines = [System.Collections.Generic.List[string]]::new()
    foreach ($role in @('brainstorm', 'intent_critic', 'architecture_critic', 'executability_critic', 'chair')) {
        $profileModel = Get-BSLFlowYamlValue $ProfileText @('review', 'council', 'roles', $role, 'model') ''
        if ([string]::IsNullOrWhiteSpace($profileModel)) { continue }
        $projectModel = Get-BSLFlowYamlValue $ProjectText @('review', 'council', 'roles', $role, 'model') '__bsl_flow_absent__'
        if ($projectModel -cne '__bsl_flow_absent__') { continue }
        $roleLines.Add("      ${role}:")
        $roleLines.Add("        model: $profileModel")
    }
    if ($providerLines.Count -eq 0 -and $modelLines.Count -eq 0 -and $roleLines.Count -eq 0) { return $ProjectText }

    $lines = [System.Collections.Generic.List[string]]::new()
    if ($providerLines.Count -gt 0 -or $modelLines.Count -gt 0) {
        $lines.Add('llm:')
        if ($providerLines.Count -gt 0) { $lines.Add('  providers:'); foreach ($line in $providerLines) { $lines.Add($line) } }
        if ($modelLines.Count -gt 0) { $lines.Add('  models:'); foreach ($line in $modelLines) { $lines.Add($line) } }
    }
    if ($roleLines.Count -gt 0) {
        $lines.Add('review:')
        $lines.Add('  council:')
        $lines.Add('    roles:')
        foreach ($line in $roleLines) { $lines.Add($line) }
    }
    $baseText = $ProjectText
    if ($baseText.Length -gt 0 -and -not $baseText.EndsWith("`n")) { $baseText += "`n" }
    return ($baseText + (($lines -join "`n") + "`n"))
}

function ConvertTo-BSLFlowCouncilCanonicalPolicyJson {
    # Canonical form of the effective policy (see the module header): the
    # parsed policy projected recursively to ordinal-sorted keys with the
    # computed fields (endpoint, transport_capability_version) removed, then
    # serialized without insignificant whitespace. The parser already rejects
    # every key outside the allowlist, so the parsed policy contains only
    # allowlisted fields by construction.
    param([Parameter(Mandatory)]$Policy)
    function Project([object]$Value) {
        if ($null -eq $Value) { return $null }
        if ($Value -is [System.Collections.IDictionary]) {
            $keys = [string[]]@($Value.Keys | ForEach-Object { [string]$_ })
            [Array]::Sort($keys, [System.StringComparer]::Ordinal)
            $sorted = [ordered]@{}
            foreach ($key in $keys) {
                if ($key -ceq 'endpoint' -or $key -ceq 'transport_capability_version') { continue }
                $sorted[$key] = (Project $Value[$key])
            }
            return $sorted
        }
        if ($Value -is [string]) { return [string]$Value }
        if ($Value -is [System.Collections.IEnumerable]) { return @($Value | ForEach-Object { Project $_ }) }
        if ($Value -is [pscustomobject]) {
            $keys = [string[]]@($Value.PSObject.Properties | ForEach-Object { [string]$_.Name })
            [Array]::Sort($keys, [System.StringComparer]::Ordinal)
            $sorted = [ordered]@{}
            foreach ($key in $keys) {
                $property = $Value.PSObject.Properties[$key]
                if ($null -eq $property) { continue }
                $sorted[$key] = (Project $property.Value)
            }
            return $sorted
        }
        return $Value
    }
    return (ConvertTo-Json -InputObject (Project $Policy) -Depth 40 -Compress)
}

function Get-BSLFlowCouncilCanonicalPolicyHash {
    param([Parameter(Mandatory)]$Policy)
    $utf8 = [System.Text.UTF8Encoding]::new($false)
    return (Get-BSLFlowBytesSha256 ($utf8.GetBytes((ConvertTo-BSLFlowCouncilCanonicalPolicyJson -Policy $Policy))))
}

function Get-BSLFlowCouncilEffectivePolicy {
    # Single load path for every council consumer: project text -> user profile
    # -> merged effective text -> parsed policy -> hash. The hash uses the
    # canonical policy form when the profile contributed or the local overlay
    # contributes token/base_url for a provider of this config; with no
    # contribution at all it stays the raw project text hash, so previously
    # recorded results stay valid (see the module header).
    param(
        [Parameter(Mandatory)][string]$ProjectRoot,
        [string]$UserProfilePath = ''
    )
    $projectPath = Join-Path $ProjectRoot 'bsl-flow.yaml'
    $utf8 = [System.Text.UTF8Encoding]::new($false)
    $projectText = if (Test-Path -LiteralPath $projectPath -PathType Leaf) { [System.IO.File]::ReadAllText($projectPath, $utf8) } else { '' }
    $profilePath = if ([string]::IsNullOrWhiteSpace($UserProfilePath)) { Resolve-BSLFlowUserProfileConfigPath } else { $UserProfilePath }
    $profileText = Get-BSLFlowUserProfileText -Path $profilePath
    $profileUsed = -not [string]::IsNullOrWhiteSpace($profileText)
    $effectiveText = Merge-BSLFlowCouncilPolicyText -ProjectText $projectText -ProfileText $profileText -ProfilePath $profilePath
    try { $policy = Get-BSLFlowCouncilPolicy $effectiveText }
    catch {
        if (-not $profileUsed) { throw }
        throw ("Council policy is invalid (project bsl-flow.yaml with user profile config ${profilePath}): " + $_.Exception.Message)
    }
    $overlay = Get-BSLFlowLocalProviderOverlay -ProjectPath $ProjectRoot
    $overlayUsed = $false
    foreach ($providerName in [string[]]@($policy.providers.Keys)) {
        if (-not $overlay.Contains($providerName)) { continue }
        $overlayEntry = $overlay[$providerName]
        if ([bool]$overlayEntry.has_token -or [bool]$overlayEntry.has_base_url) { $overlayUsed = $true; break }
    }
    $policyHash = if ($profileUsed -or $overlayUsed) {
        Get-BSLFlowCouncilCanonicalPolicyHash -Policy $policy
    }
    else {
        Get-BSLFlowCouncilPolicyHash -PolicyText $effectiveText
    }
    return [ordered]@{
        project_path = $projectPath
        project_text = $projectText
        profile_path = $profilePath
        profile_used = $profileUsed
        text = $effectiveText
        policy = $policy
        hash = $policyHash
    }
}
