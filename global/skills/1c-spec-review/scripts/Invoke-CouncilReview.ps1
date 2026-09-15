#Requires -Version 7.0
Set-StrictMode -Version Latest

# Shared council routing and executable cycle for assisted and managed hosts.
# One spec_review route. Council is the default; legacy OpenCode stays only
# on the explicit opencode_compat route. No silent reinterpretation.

$script:BSLFlowCouncilScriptRoot = $PSScriptRoot

$script:BSLFlowCouncilRoleRubrics = @{
    brainstorm = 'Generate alternatives, risks, unknown preconditions and questions from the original task and evidence only. You never see the draft specification. Return no verdict and no specification text.'
    intent_critic = 'Look for lost requirements, intent drift, unsupported assumptions and scope creep. Cite the original task as evidence for every finding. Allowed finding categories: lost_requirement, intent_drift, unsupported_assumption, scope_creep, missing_requirement, overengineering, clarity.'
    architecture_critic = 'Check fit with existing mechanisms, minimality, data, transactions/locks, integration and security boundaries. Cite draft design/spec lines as evidence for every finding. Allowed finding categories (choose exactly one per finding, no synonyms): architecture_fit, overengineering, missing_requirement, unsupported_assumption, clarity.'
    executability_critic = 'Look for implementation ambiguity, missing decisions, untestable acceptance criteria and incomplete requirement-to-observation traceability. Every finding must name the affected requirement or criterion. Allowed finding categories: testability, clarity, missing_requirement, unsupported_assumption, architecture_fit.'
    chair = 'You are the only model reconciler. Weigh every finding and protected item exactly once against the original task and trusted evidence. Produce the minimally revised complete final specification text.'
}

function Resolve-BSLFlowCouncilDispatcher {
    # The default live dispatcher travels as the 'live' sentinel, never as a
    # code object: functions and closures created in the controller session
    # execute unreliably inside parallel thread-job runspaces, where they can
    # corrupt the shared state and stop resolving mid-dispatch. Each consumer
    # resolves the sentinel in its own runspace, where the engine was loaded.
    param([Parameter(Mandatory)]$Dispatcher)
    if ($Dispatcher -is [string] -and $Dispatcher -ceq 'live') {
        return ${function:Invoke-BSLFlowCouncilLiveDispatch}
    }
    return $Dispatcher
}

function Invoke-BSLFlowCouncilReview {
    param(
        [Parameter(Mandatory)][string]$ProjectPath,
        [Parameter(Mandatory)][string]$ChangeName,
        [string]$EvidenceText = '',
        [int]$MaxInputBytes = 0,
        [switch]$DryRun,
        [switch]$AllowLiveDispatch,
        [scriptblock]$Dispatcher,
        [scriptblock]$FallbackRunner,
        [hashtable]$Capabilities,
        [scriptblock]$Cancelled,
        [scriptblock]$BeforeDispatch,
        [scriptblock]$AfterDispatch
    )
    if (($null -eq $BeforeDispatch) -xor ($null -eq $AfterDispatch)) {
        throw 'BF_INVALID: council dispatch hooks must be supplied as a pair.'
    }
    . (Join-Path $PSScriptRoot 'Review.Common.ps1')
    . (Join-Path $PSScriptRoot 'Council.Common.ps1')
    . (Join-Path $PSScriptRoot 'Council.Engine.ps1')

    $projectRoot = [System.IO.Path]::GetFullPath($ProjectPath).TrimEnd('\', '/')
    $changeDir = Join-Path $projectRoot "openspec\changes\$ChangeName"
    $configPath = Join-Path $projectRoot 'bsl-flow.yaml'
    $configText = if (Test-Path -LiteralPath $configPath -PathType Leaf) { Get-Content -Raw -LiteralPath $configPath } else { '' }
    # Council policy throws BF_MIGRATION_BLOCKED for explicit legacy opencode
    # unless the separate compatibility route was chosen.
    $council = Get-BSLFlowCouncilPolicy $configText
    if (-not [bool]$council.enabled) { throw 'Council route requires review.council.enabled true.' }
    if ($council.legacy_mode -ceq 'opencode_compat') { throw 'Legacy OpenCode compatibility route selected; council dispatch is skipped.' }

    $policyHash = Get-BSLFlowCouncilPolicyHash $configPath
    if ($MaxInputBytes -eq 0) {
        $MaxInputBytes = [int]::Parse((Get-BSLFlowYamlValue $configText @('review', 'input', 'max_file_bytes') '262144'), [Globalization.CultureInfo]::InvariantCulture)
    }
    if ($MaxInputBytes -lt 1024 -or $MaxInputBytes -gt 1048576) { throw 'review.input.max_file_bytes must be between 1024 and 1048576.' }
    $snapshot = New-BSLFlowCouncilSnapshot -ChangeDir $changeDir -MaxBytes $MaxInputBytes -EvidenceText $EvidenceText -PolicyHash $policyHash
    $runRoot = Join-Path $projectRoot ('.bsl-flow/reports/spec-review/' + $ChangeName + '.council')
    New-Item -ItemType Directory -Path $runRoot -Force | Out-Null

    $bindings = Get-BSLFlowCouncilRoleBindings -ProjectRoot $projectRoot -Council $council -Snapshot $snapshot
    if ($DryRun) {
        $plan = @()
        foreach ($entry in $bindings) {
            $attempt = New-BSLFlowCouncilAttempt -RunRoot $runRoot -Role ([string]$entry.role_name) -Binding $entry.binding
            $route = 'direct_api'
            if ($entry.credential.credential_source -ceq 'missing') {
                if ([string]$entry.role.fallback -ceq 'block') { $route = 'blocked' }
                else { $route = 'current_agent_fallback' }
            }
            $plan += [pscustomobject][ordered]@{
                role = [string]$entry.role_name; attempt_id = $attempt.attempt_id
                route = $route; credential_source = [string]$entry.credential.credential_source
            }
        }
        return [ordered]@{ dry_run = $true; run_root = $runRoot; plan = @($plan); manifest_requirements = @($snapshot.manifest.requirements).Count }
    }
    if (-not $AllowLiveDispatch) {
        throw 'BF_BLOCKED: live council dispatch needs explicit opt-in; dry-run plan recorded.'
    }
    $dispatch = $Dispatcher
    if ($null -eq $dispatch) { $dispatch = 'live' }
    return (Invoke-BSLFlowCouncilCycle -ProjectRoot $projectRoot -ChangeName $ChangeName -Council $council -Snapshot $snapshot -RunRoot $runRoot -Bindings $bindings -Dispatcher $dispatch -FallbackRunner $FallbackRunner -Capabilities $Capabilities -Cancelled $Cancelled -BeforeDispatch $BeforeDispatch -AfterDispatch $AfterDispatch)
}

function Get-BSLFlowCouncilRoleBindings {
    param(
        [Parameter(Mandatory)][string]$ProjectRoot,
        [Parameter(Mandatory)]$Council,
        [Parameter(Mandatory)]$Snapshot
    )
    . (Join-Path $PSScriptRoot 'Review.Common.ps1')
    . (Join-Path $PSScriptRoot 'Council.Common.ps1')
    . (Join-Path $PSScriptRoot 'Council.Transport.ps1')
    # Review dispatches never read the implementation execution_profile:
    # council bindings come only from llm.* above. No mixing by construction.
    $overlay = Get-BSLFlowLocalProviderOverlay -ProjectPath $ProjectRoot
    $entries = @()
    foreach ($roleName in @('brainstorm', 'intent_critic', 'architecture_critic', 'executability_critic', 'chair')) {
        $role = $Council.roles[$roleName]
        if ($null -eq $role -or -not [bool]$role.enabled) { continue }
        $profile = $Council.models[[string]$role.model]
        $provider = $Council.providers[[string]$profile.provider]
        $localToken = ''
        $endpoint = [pscustomobject][ordered]@{
            scheme = [string]$provider.endpoint.scheme; host = [string]$provider.endpoint.host
            port = [int]$provider.endpoint.port; base_path = [string]$provider.endpoint.base_path
        }
        if ($overlay.Contains([string]$profile.provider)) {
            # Raw local values are read here and never logged.
            $localPath = Join-Path $ProjectRoot '.bsl-flow/providers.local.yaml'
            $localText = Get-Content -Raw -LiteralPath $localPath
            $localToken = Get-BSLFlowYamlValue $localText @('providers', [string]$profile.provider, 'token') ''
            # The advertised local base_url override is validated and applied.
            $localBaseUrl = [string]$overlay[[string]$profile.provider].base_url
            if (-not [string]::IsNullOrWhiteSpace($localBaseUrl)) {
                $localEndpoint = Assert-BSLFlowEndpointUrl -Url $localBaseUrl -Name "providers.local.$([string]$profile.provider)" -AllowLocalHttp ([bool]$Council.allow_local_http)
                $endpoint = [pscustomobject][ordered]@{
                    scheme = [string]$localEndpoint.scheme; host = [string]$localEndpoint.host
                    port = [int]$localEndpoint.port; base_path = [string]$localEndpoint.base_path
                }
            }
        }
        $credential = Resolve-BSLFlowCouncilCredential -ProviderName ([string]$profile.provider) -TokenEnv ([string]$provider.token_env) -LocalToken $localToken
        # Every frozen dependency that shapes the sealed prompt invalidates the
        # attempt: design/evidence/policy/rubric here, and for the chair also
        # the canonical member aggregate hash added later by the cycle.
        $rubricText = [string]$script:BSLFlowCouncilRoleRubrics[$roleName]
        $utf8 = [System.Text.UTF8Encoding]::new($false)
        $sha = [System.Security.Cryptography.SHA256]::Create()
        try { $rubricHash = ([BitConverter]::ToString($sha.ComputeHash($utf8.GetBytes($rubricText)))).Replace('-', '').ToLowerInvariant() } finally { $sha.Dispose() }
        $binding = [pscustomobject][ordered]@{
            provider = [string]$profile.provider; model = [string]$profile.model; effort = [string]$profile.effort
            protocol = [string]$provider.protocol; endpoint = $endpoint
            transport_capability_version = [int]$provider.transport_capability_version
            prompt_version = 'council-prompt-v2'; member_schema_version = 1
            input_hashes = [pscustomobject][ordered]@{
                original_task_sha256 = $Snapshot.original_task_sha256; spec_sha256 = $Snapshot.spec_sha256
                design_sha256 = $Snapshot.design_sha256; evidence_sha256 = $Snapshot.evidence_sha256
                policy_hash = $Snapshot.policy_hash; rubric_sha256 = $rubricHash
            }
        }
        $entries += [pscustomobject][ordered]@{
            role_name = $roleName; role = $role; profile = $profile; provider = $provider
            credential = $credential; binding = $binding
        }
    }
    return @($entries)
}

function New-BSLFlowCouncilPrompt {
    param([Parameter(Mandatory)][string]$Role, [Parameter(Mandatory)]$View)
    # Deterministic sealed prompt: trusted policy markers separate the frozen
    # role contract from untrusted draft content. No tools, no extra context.
    $rubric = ''
    try { $rubric = [string]$script:BSLFlowCouncilRoleRubrics[$Role] } catch { $rubric = '' }
    if (-not $rubric) { $rubric = 'Follow the role contract exactly.' }
    $contract = if ($Role -ceq 'brainstorm') {
        'Return a JSON object with role, alternatives[], risks[], unknowns[], questions[]. No verdict, no specification text.'
    }
    elseif ($Role -ceq 'chair') {
        'Return ONLY a JSON object with exactly these top-level fields: verdict (MUST be exactly one of the strings PASS, REVISE, BLOCK, needs_input), decisions[] (composite_id, decision accepted|rejected|partially_accepted, reason, evidence, resolution, accepted_scope/rejected_scope for partial, resolution_refs for accepted/partial), protected_decisions[] (composite_id, decision preserved|rejected, reason, evidence), requirement_refs[] (id, final_refs[]), final_spec_text (the complete minimally revised specification markdown), final_design_text (optional, only when the design needs changes). No other top-level fields such as type or summary. resolution_refs and final_refs must be verbatim fragments or "Требуемое поведение / N" (or N.M subsection) anchors of final_spec_text. The final specification must never contain the literal placeholder strings TODO, TBD, FIXME, XXX, PLACEHOLDER or {{...}} anywhere, including inside rule descriptions — describe the rule without spelling the marker. Cover every finding, protected item and requirement exactly once. Answer every member question or return needs_input with that question.'
    }
    else {
        'Return ONLY a JSON object with exactly these top-level fields: role, verdict (MUST be exactly one of the strings PASS, REVISE, BLOCK, needs_input), findings[] (each with id F-NNN sequential, severity exactly blocker|high|medium|low, category EXACTLY one of the allowed categories listed in the rubric above with no other wording, spec_ref, issue, evidence, suggested_direction), do_not_change[] (strings), needs_input_questions[] (only when verdict is needs_input). Do not invent new categories. No other top-level fields (for example no type, schema_version or summary). Never include provider, model, status, usage, timestamps, hashes or execution mode.'
    }
    $lines = [System.Collections.Generic.List[string]]::new()
    $lines.Add('<<<BEGIN TRUSTED REVIEW POLICY: ROLE CONTRACT>>>')
    $lines.Add("Role: $Role")
    $lines.Add("Rubric: $rubric")
    $lines.Add($contract)
    $lines.Add('<<<END TRUSTED REVIEW POLICY: ROLE CONTRACT>>>')
    $lines.Add('<<<BEGIN UNTRUSTED DATA: original-task.md>>>')
    $lines.Add([string]$View.original_task)
    $lines.Add('<<<END UNTRUSTED DATA: original-task.md>>>')
    if ($null -ne $View.PSObject.Properties['spec'] -and $null -ne $View.spec) {
        $lines.Add('<<<BEGIN UNTRUSTED DATA: spec.md>>>')
        $lines.Add([string]$View.spec)
        $lines.Add('<<<END UNTRUSTED DATA: spec.md>>>')
    }
    if ($null -ne $View.PSObject.Properties['design'] -and $null -ne $View.design) {
        $lines.Add('<<<BEGIN UNTRUSTED DATA: design.md>>>')
        $lines.Add([string]$View.design)
        $lines.Add('<<<END UNTRUSTED DATA: design.md>>>')
    }
    if ($null -ne $View.PSObject.Properties['evidence'] -and -not [string]::IsNullOrWhiteSpace([string]$View.evidence)) {
        $lines.Add('<<<BEGIN UNTRUSTED DATA: evidence>>>')
        $lines.Add([string]$View.evidence)
        $lines.Add('<<<END UNTRUSTED DATA: evidence>>>')
    }
    if ($null -ne $View.PSObject.Properties['aggregates'] -and $null -ne $View.aggregates) {
        $lines.Add('<<<BEGIN TRUSTED AGGREGATES: member results>>>')
        $lines.Add([string]$View.aggregates)
        $lines.Add('<<<END TRUSTED AGGREGATES: member results>>>')
    }
    return ($lines -join "`n")
}

function Invoke-BSLFlowCouncilLiveDispatch {
    param(
        [Parameter(Mandatory)]$Attempt,
        [Parameter(Mandatory)][string]$PromptText,
        [Parameter(Mandatory)]$Route
    )
    . (Join-Path $script:BSLFlowCouncilScriptRoot 'Council.Transport.ps1')
    . (Join-Path $script:BSLFlowCouncilScriptRoot 'Council.Fallback.ps1')
    if ([string]$Route.route -ceq 'current_agent_fallback') {
        throw 'BF_BLOCKED: fallback dispatch needs a host capability receipt and runner; supply -Capabilities and -FallbackRunner.'
    }
    if ([string]$Route.route -ceq 'blocked') { throw 'BF_BLOCKED: role credential is missing and fallback policy is block.' }
    $credential = [string]$Route.credential.token
    if ([string]::IsNullOrWhiteSpace($credential)) { throw 'BF_BLOCKED: direct_api dispatch needs a resolved credential.' }
    $roleDir = Join-Path $Route.attempt_path ('dispatch-' + [string]$Route.attempt.sequence)
    New-Item -ItemType Directory -Path $roleDir -Force | Out-Null
    $timeoutSeconds = 300
    try { if ($null -ne $Route.request_timeout_seconds) { $timeoutSeconds = [int]$Route.request_timeout_seconds } } catch { }
    $result = Invoke-BSLFlowCouncilApi -Binding $Attempt.binding -PromptText $PromptText -Credential $credential -AttemptDir $roleDir -TimeoutSeconds $timeoutSeconds
    # observed model/usage come from the provider response envelope; provider is
    # the endpoint identity the controller actually dialed; effort is not observed.
    return [ordered]@{
        status = 'completed'; payload = $result.payload
        observed = [ordered]@{
            provider = [string]$Attempt.binding.provider
            model = $result.observed_model
            effort = $null
        }
        usage = $result.usage
        execution_mode = 'direct_api'; fallback_reason = $null
    }
}

function New-BSLFlowCouncilFallbackDispatch {
    param([Parameter(Mandatory)][scriptblock]$Runner)
    # Host-provided fresh-context adapter: the runner receives the sealed role
    # prompt and returns a controller-trusted terminal result for this context.
    # The script root is captured as a closure variable, not $script:, so the
    # dispatcher also works inside thread-job session state.
    $councilScriptRoot = $PSScriptRoot
    $fallbackDispatcher = {
        param($Attempt, $PromptText, $Route)
        . (Join-Path $councilScriptRoot 'Review.Common.ps1')
        . (Join-Path $councilScriptRoot 'Council.Fallback.ps1')
        . (Join-Path $councilScriptRoot 'Council.Validation.ps1')
        if ([string]$Route.route -ceq 'blocked') { throw 'BF_BLOCKED: role credential is missing and fallback policy is block.' }
        if ([string]$Route.route -cne 'current_agent_fallback') {
            throw 'BF_BLOCKED: fallback dispatcher received a non-fallback route.'
        }
        $capability = $Route.capability
        $runnerResult = & $Runner $Attempt $PromptText $capability
        $payload = & { param($r) if ($r -is [System.Collections.IDictionary]) { return $r['payload'] }; return $r.payload } $runnerResult
        $runnerUsage = & { param($r) if ($r -is [System.Collections.IDictionary]) { return $r['usage'] }; $p = $r.PSObject.Properties['usage']; if ($null -eq $p) { return $null }; return $p.Value } $runnerResult
        $runnerObservedModel = & { param($r) if ($r -is [System.Collections.IDictionary]) { return $r['observed_model'] }; $p = $r.PSObject.Properties['observed_model']; if ($null -eq $p) { return $null }; return $p.Value } $runnerResult
        $runnerObservedEffort = & { param($r) if ($r -is [System.Collections.IDictionary]) { return $r['observed_effort'] }; $p = $r.PSObject.Properties['observed_effort']; if ($null -eq $p) { return $null }; return $p.Value } $runnerResult
        # Observed provenance comes only from the runner's terminal host receipt.
        # Requested capability values are never promoted into observed identity;
        # an unverifiable receipt keeps both observed fields unknown (diversity
        # unknown) or blocks the role when the host claimed an identity.
        $hasObservedModel = -not [string]::IsNullOrWhiteSpace([string]$runnerObservedModel)
        $hasObservedEffort = -not [string]::IsNullOrWhiteSpace([string]$runnerObservedEffort)
        if ($hasObservedModel -xor $hasObservedEffort) { throw 'BF_BLOCKED: fallback host receipt must prove observed model and effort together.' }
        if (-not $hasObservedModel -and -not [string]::IsNullOrWhiteSpace([string]$capability.model)) { throw 'BF_BLOCKED: fallback host receipt carries no observed model; requested capability values are not provenance.' }
        if (-not $hasObservedEffort -and -not [string]::IsNullOrWhiteSpace([string]$capability.effort)) { throw 'BF_BLOCKED: fallback host receipt carries no observed effort; requested capability values are not provenance.' }
        if ([string]$Attempt.role -ceq 'brainstorm') { Assert-BSLFlowBrainstormPayload $payload }
        elseif ([string]$Attempt.role -ceq 'chair') { Assert-BSLFlowCouncilChairResult $payload }
        else { Assert-BSLFlowCouncilModelPayload $payload }
        return [ordered]@{
            status = 'completed'; payload = $payload
            observed = [ordered]@{
                provider = $(try { [string]$capability.provider } catch { 'current_agent' })
                model = if ($hasObservedModel) { [string]$runnerObservedModel } else { $null }
                effort = if ($hasObservedEffort) { [string]$runnerObservedEffort } else { $null }
            }
            usage = $runnerUsage
            execution_mode = 'current_agent_fallback'
            fallback_reason = [string]$Route.reason
        }
    }.GetNewClosure()
    return $fallbackDispatcher
}

function Invoke-BSLFlowCouncilDispatchRole {
    # One controller-owned dispatch attempt: persist, route, classify every
    # terminal outcome and never blindly repeat an unknown paid call.
    param(
        [Parameter(Mandatory)][string]$RunRoot,
        [Parameter(Mandatory)]$Entry,
        [Parameter(Mandatory)][scriptblock]$Dispatcher,
        [scriptblock]$FallbackRunner,
        [hashtable]$Capabilities,
        [scriptblock]$Cancelled,
        [scriptblock]$BeforeDispatch,
        [scriptblock]$AfterDispatch
    )
    if (($null -eq $BeforeDispatch) -xor ($null -eq $AfterDispatch)) {
        throw 'BF_INVALID: council dispatch hooks must be supplied as a pair.'
    }
    . (Join-Path $PSScriptRoot 'Review.Common.ps1')
    . (Join-Path $PSScriptRoot 'Council.Engine.ps1')
    . (Join-Path $PSScriptRoot 'Council.Fallback.ps1')
    $roleName = [string]$Entry.role_name
    # Resolve fallback policy and bind the stable host capability before looking
    # up a retained attempt. Otherwise a new current host model/effort could
    # accidentally reuse a result produced under an older host contract.
    $capability = $null
    $executionMode = 'direct_api'
    $route = [ordered]@{ route = 'direct_api'; credential = $Entry.credential }
    if ($Entry.credential.credential_source -ceq 'missing') {
        if ($null -ne $Capabilities -and $Capabilities.ContainsKey($roleName)) { $capability = $Capabilities[$roleName] }
        $policy = Assert-BSLFlowCouncilFallbackPolicy -Fallback ([string]$Entry.role.fallback) -Credential $Entry.credential -Capability $capability
        $route.route = [string]$policy.route
        if ([string]$policy.route -ceq 'current_agent_fallback') {
            $route.reason = [string]$policy.reason
            $route.capability = $capability
            $executionMode = 'current_agent_fallback'
            $capabilityBinding = Get-BSLFlowCouncilCapabilityBinding $capability
            $binding = [ordered]@{}
            foreach ($property in @($Entry.binding.PSObject.Properties)) { $binding[$property.Name] = $property.Value }
            $binding.fallback_capability = $capabilityBinding
            $Entry | Add-Member -NotePropertyName binding -NotePropertyValue ([pscustomobject]$binding) -Force
        }
    }
    $attempt = New-BSLFlowCouncilAttempt -RunRoot $RunRoot -Role $roleName -Binding $Entry.binding
    # A retained terminal result for this exact attempt is authoritative: reuse
    # completed work and refuse to repeat an unknown paid dispatch.
    $retained = Get-BSLFlowCouncilRoleResult -RunRoot $RunRoot -Role $roleName -Attempt $attempt
    if ($null -ne $retained) {
        $status = [string]$retained.envelope.status
        if ($status -ceq 'completed') {
            return [ordered]@{ reused = $true; envelope = $retained.envelope; payload = $retained.payload }
        }
        if ($status -ceq 'unknown_after_dispatch') {
            throw "BF_BLOCKED: retained $roleName attempt $($attempt.attempt_id) is unknown_after_dispatch; provider reconciliation is required before any new dispatch."
        }
        if ([bool]$Entry.role.required) { throw "BF_BLOCKED: required role has a retained non-completed result: $roleName ($status)" }
        return [ordered]@{ reused = $false; envelope = $retained.envelope; payload = $null }
    }
    $route.attempt = $attempt
    $route.attempt_path = (Join-Path $RunRoot $roleName)
    $route.request_timeout_seconds = $Entry.request_timeout_seconds

    if ($executionMode -ceq 'current_agent_fallback' -and $null -ne $FallbackRunner) {
        $route.dispatcher = New-BSLFlowCouncilFallbackDispatch -Runner $FallbackRunner
    }

    $view = Get-BSLFlowCouncilRoleView -Snapshot $Entry.snapshot -Role $roleName -RubricText ([string]$script:BSLFlowCouncilRoleRubrics[$roleName])
    $prompt = New-BSLFlowCouncilPrompt -Role $roleName -View $view
    if ($null -ne $Cancelled -and (& $Cancelled)) {
        $null = Register-BSLFlowCouncilMemberResult -RunRoot $RunRoot -Attempt $attempt -Payload $null -Status $status -Summary (($failure -replace '[\r\n]+', ' ')) -Observed ([ordered]@{ provider = $null; model = $null; effort = $null }) -ExecutionMode $executionMode -Cancelled
        if ([bool]$Entry.role.required) { throw "BF_BLOCKED: council cancelled before $roleName dispatch." }
        return [ordered]@{ reused = $false; envelope = $null; payload = $null }
    }

    $dispatchedAt = [DateTime]::UtcNow
    $activeDispatcher = $Dispatcher
    if ($route.Contains('dispatcher') -and $null -ne $route.dispatcher) { $activeDispatcher = $route.dispatcher }
    if ($null -ne $BeforeDispatch) {
        # Provider-owned hooks replace the Council budget ledger for this exact
        # dispatch. Fallback workers may deliberately make this a no-op because
        # Invoke-BFManagedWorker owns their nested provider reservation.
        & $BeforeDispatch $attempt $route
    }
    else {
        # Admission and reservation are one atomic ledger transaction: a
        # parallel role can never slip between the check and the write.
        $null = Approve-BSLFlowCouncilBudgetDispatch -RunRoot $RunRoot -Role $roleName -Attempt $attempt -EstimateUsd $Entry.cost_estimate_usd -Budget $Entry.cycle_budget
    }
    $dispatch = $null
    $failure = $null
    try { $dispatch = & $activeDispatcher $attempt $prompt $route }
    catch {
        $failure = [string]$_.Exception.Message
        $completedAt = [DateTime]::UtcNow
        $status = 'unknown_after_dispatch'
        if ($failure -match 'BF_FAILED_BEFORE_ACCEPTANCE') { $status = 'failed_before_acceptance' }
        elseif ($failure -match 'BF_INVALID_RESPONSE') { $status = 'invalid_response' }
        elseif ($failure -match 'BF_NOT_DISPATCHED') { $status = 'failed_before_acceptance' }
        $costState = if ($status -ceq 'failed_before_acceptance') { 'no_usage_reported' } else { 'unknown' }
        if ($null -ne $AfterDispatch) {
            try { & $AfterDispatch $attempt $route $null $status $failure }
            catch {
                # A provider receipt failure after an attempted call is itself
                # an unknown effect. Never downgrade it to a pre-call failure.
                $status = 'unknown_after_dispatch'
                $costState = 'unknown'
                $failure = 'AfterDispatch hook failed while recording the provider outcome.'
            }
        }
        $null = Register-BSLFlowCouncilMemberResult -RunRoot $RunRoot -Attempt $attempt -Payload $null -Status $status -Summary (($failure -replace '[\r\n]+', ' ')) -Observed ([ordered]@{ provider = $null; model = $null; effort = $null }) -ExecutionMode $executionMode -DispatchedAtUtc $dispatchedAt -CompletedAtUtc $completedAt -CostState $costState
        if ($null -eq $BeforeDispatch) {
            $null = Complete-BSLFlowCouncilBudgetOutcome -RunRoot $RunRoot -Role $roleName -Attempt $attempt -Status $status -ProviderReportedUsd $null -CostState $costState
        }
        if ([bool]$Entry.role.required) { throw ("BF_BLOCKED: required role did not complete: $roleName ($status): " + $(if ($failure) { $failure } else { 'dispatch failed' })) }
        return [ordered]@{ reused = $false; envelope = $null; payload = $null }
    }
    $completedAt = [DateTime]::UtcNow
    if ((Get-BSLFlowEnvelopeValue $dispatch 'status') -cne 'completed') {
        $status = if ($dispatch.status) { [string]$dispatch.status } else { 'unknown_after_dispatch' }
        if ($status -notin @('failed_before_acceptance', 'unknown_after_dispatch', 'cancelled', 'invalid_response')) {
            $status = 'unknown_after_dispatch'
        }
        $costState = if ($status -ceq 'failed_before_acceptance') { 'no_usage_reported' } else { 'unknown' }
        $failureSummary = $failure
        if ([string]::IsNullOrWhiteSpace($failureSummary)) { $failureSummary = "Dispatch returned $status." }
        $failureSummary = ($failureSummary -replace '[\r\n]+', ' ')
        if ($failureSummary.Length -gt 400) { $failureSummary = $failureSummary.Substring(0, 400) }
        if ($null -ne $AfterDispatch) {
            try { & $AfterDispatch $attempt $route $dispatch $status $failureSummary }
            catch {
                $status = 'unknown_after_dispatch'
                $costState = 'unknown'
                $failureSummary = 'AfterDispatch hook failed while recording the provider outcome.'
            }
        }
        $null = Register-BSLFlowCouncilMemberResult -RunRoot $RunRoot -Attempt $attempt -Payload $null -Status $status -Summary $failureSummary -Observed ([ordered]@{ provider = $null; model = $null; effort = $null }) -ExecutionMode $executionMode -DispatchedAtUtc $dispatchedAt -CompletedAtUtc $completedAt -CostState $costState
        if ($null -eq $BeforeDispatch) {
            $null = Complete-BSLFlowCouncilBudgetOutcome -RunRoot $RunRoot -Role $roleName -Attempt $attempt -Status $status -ProviderReportedUsd $null -CostState $costState
        }
        if ([bool]$Entry.role.required) { throw "BF_BLOCKED: required role did not complete: $roleName ($status)" }
        return [ordered]@{ reused = $false; envelope = $null; payload = $null }
    }
    $observed = Get-BSLFlowEnvelopeValue $dispatch 'observed'
    $dispatchUsage = Get-BSLFlowEnvelopeValue $dispatch 'usage'
    $costState = if ($null -ne $dispatchUsage) { 'provider_usage_reported' } else { 'no_usage_reported' }
    $dispatchPayload = Get-BSLFlowEnvelopeValue $dispatch 'payload'
    if ($null -ne $AfterDispatch) {
        try { & $AfterDispatch $attempt $route $dispatch 'completed' $null }
        catch {
            # The transport completed, but its provider receipt could not be
            # committed. Preserve the conservative unknown effect and stop any
            # automatic retry from treating the payload as accepted.
            $unknownSummary = 'AfterDispatch hook failed while recording the provider outcome.'
            $unknownEnvelope = Register-BSLFlowCouncilMemberResult -RunRoot $RunRoot -Attempt $attempt -Payload $null -Status 'unknown_after_dispatch' -Summary $unknownSummary -Observed ([ordered]@{ provider = $null; model = $null; effort = $null }) -ExecutionMode $executionMode -DispatchedAtUtc $dispatchedAt -CompletedAtUtc $completedAt -CostState 'unknown'
            if ([bool]$Entry.role.required) { throw "BF_BLOCKED: required role did not complete: $roleName (unknown_after_dispatch)" }
            return [ordered]@{ reused = $false; envelope = $unknownEnvelope; payload = $null }
        }
    }
    $envelope = Register-BSLFlowCouncilMemberResult -RunRoot $RunRoot -Attempt $attempt -Payload $dispatchPayload -Status 'completed' -Summary "Role $roleName completed its structured result." -Observed $observed -ExecutionMode ([string](Get-BSLFlowEnvelopeValue $dispatch 'execution_mode')) -DispatchedAtUtc $dispatchedAt -CompletedAtUtc $completedAt -Usage $dispatchUsage -CostState $costState
    $providerUsd = $null
    try { $reportedCost = Get-BSLFlowEnvelopeValue $dispatch 'reported_cost_usd'; if ($null -ne $reportedCost) { $providerUsd = [double]$reportedCost } } catch { $providerUsd = $null }
    if ($null -eq $BeforeDispatch) {
        $null = Complete-BSLFlowCouncilBudgetOutcome -RunRoot $RunRoot -Role $roleName -Attempt $attempt -Status 'completed' -ProviderReportedUsd $providerUsd -CostState $costState
    }
    return [ordered]@{ reused = $false; envelope = $envelope; payload = $dispatchPayload }
}

function Invoke-BSLFlowCouncilCycle {
    param(
        [Parameter(Mandatory)][string]$ProjectRoot,
        [Parameter(Mandatory)][string]$ChangeName,
        [Parameter(Mandatory)]$Council,
        [Parameter(Mandatory)]$Snapshot,
        [Parameter(Mandatory)][string]$RunRoot,
        [Parameter(Mandatory)]$Bindings,
        [Parameter(Mandatory)]$Dispatcher,
        [scriptblock]$FallbackRunner,
        [hashtable]$Capabilities,
        [scriptblock]$Cancelled,
        [scriptblock]$BeforeDispatch,
        [scriptblock]$AfterDispatch
    )
    if (($null -eq $BeforeDispatch) -xor ($null -eq $AfterDispatch)) {
        throw 'BF_INVALID: council dispatch hooks must be supplied as a pair.'
    }
    . (Join-Path $PSScriptRoot 'Review.Common.ps1')
    . (Join-Path $PSScriptRoot 'Council.Validation.ps1')
    . (Join-Path $PSScriptRoot 'Council.Transport.ps1')
    . (Join-Path $PSScriptRoot 'Council.Fallback.ps1')
    . (Join-Path $PSScriptRoot 'Council.Engine.ps1')
    $changeDir = Join-Path $ProjectRoot "openspec\changes\$ChangeName"

    # Decorate bindings with role snapshots and cost estimates once, so every
    # dispatch (sequential or bounded-parallel) sees identical frozen inputs.
    foreach ($entry in $Bindings) {
        $entry | Add-Member -NotePropertyName snapshot -NotePropertyValue $Snapshot -Force
        $estimate = $null
        try { $estimate = $Council.models[[string]$entry.role.model].cost_estimate_usd } catch { $estimate = $null }
        $entry | Add-Member -NotePropertyName cost_estimate_usd -NotePropertyValue $estimate -Force
        $entry | Add-Member -NotePropertyName cycle_budget -NotePropertyValue $Council.budget -Force
        $entry | Add-Member -NotePropertyName request_timeout_seconds -NotePropertyValue $Council.request_timeout_seconds -Force
    }

    # Admission is per-dispatch (DispatchRole and the chair leg): each real
    # dispatch is admitted against the whole durable ledger right before its
    # reservation, so retained results are never re-counted and the chair is
    # admitted with its final aggregate-extended binding.

    # Critics and brainstorm dispatch with bounded controller-owned parallelism;
    # every result is classified, persisted per attempt and sealed into a
    # controller-owned envelope before the chair may run.
    $memberEntries = @($Bindings | Where-Object { [string]$_.role_name -cne 'chair' })
    $results = @{}
    $maxParallel = [Math]::Max(1, [int]$Council.max_parallel)
    if ($null -ne $BeforeDispatch) {
        # Provider hooks execute in the controller runspace and own a shared
        # provider artifact ledger. Keep this route serialized; standalone
        # Council dispatch retains its configured bounded parallelism.
        $maxParallel = 1
    }
    # Host-dependent fallback roles run sequentially in the controller runspace:
    # the runner closure needs the live host session, not a thread-job copy.
    $parallelEntries = @($memberEntries | Where-Object { $_.credential.credential_source -cne 'missing' })
    $fallbackEntries = @($memberEntries | Where-Object { $_.credential.credential_source -ceq 'missing' })
    # Sequential dispatch runs in this controller runspace, so resolve the
    # sentinel here; the parallel branch keeps the sentinel for in-job resolve.
    $controllerDispatcher = Resolve-BSLFlowCouncilDispatcher $Dispatcher
    $dispatchOne = {
        param($Entry)
        Invoke-BSLFlowCouncilDispatchRole -RunRoot $RunRoot -Entry $Entry -Dispatcher $controllerDispatcher -FallbackRunner $FallbackRunner -Capabilities $Capabilities -Cancelled $Cancelled -BeforeDispatch $BeforeDispatch -AfterDispatch $AfterDispatch
    }
    foreach ($entry in $fallbackEntries) { $results[[string]$entry.role_name] = & $dispatchOne $entry }
    if ($maxParallel -le 1 -or $parallelEntries.Count -le 1) {
        foreach ($entry in $parallelEntries) { $results[[string]$entry.role_name] = & $dispatchOne $entry }
    }
    else {
        $workerScript = {
            param($Entries, $RunRoot, $Dispatcher, $ScriptRoot)
            $script:BSLFlowCouncilScriptRoot = $ScriptRoot
            . (Join-Path $ScriptRoot 'Invoke-CouncilReview.ps1')
            . (Join-Path $ScriptRoot 'Review.Common.ps1')
            . (Join-Path $ScriptRoot 'Council.Common.ps1')
            . (Join-Path $ScriptRoot 'Council.Engine.ps1')
            . (Join-Path $ScriptRoot 'Council.Validation.ps1')
            . (Join-Path $ScriptRoot 'Council.Transport.ps1')
            . (Join-Path $ScriptRoot 'Council.Fallback.ps1')
            # Resolve the 'live' sentinel in this job's own runspace; a code
            # object from the controller session does not survive the crossing.
            $Dispatcher = Resolve-BSLFlowCouncilDispatcher $Dispatcher
            $out = [ordered]@{}
            foreach ($entry in $Entries) {
                try { $out[[string]$entry.role_name] = (Invoke-BSLFlowCouncilDispatchRole -RunRoot $RunRoot -Entry $entry -Dispatcher $Dispatcher) }
                catch { $out[[string]$entry.role_name] = [ordered]@{ reused = $false; envelope = $null; payload = $null; error = [string]$_.Exception.Message } }
            }
            return $out
        }
        # Bounded chunks: each thread job owns at most max_parallel member roles.
        for ($offset = 0; $offset -lt $parallelEntries.Count; $offset += $maxParallel) {
            $chunk = @($parallelEntries | Select-Object -Skip $offset -First $maxParallel)
            $jobs = @()
            foreach ($entry in $chunk) {
                $jobs += Start-ThreadJob -ScriptBlock $workerScript -ArgumentList @($entry), $RunRoot, $Dispatcher, $PSScriptRoot
            }
            $outputs = @()
            try {
                foreach ($job in (Wait-Job $jobs)) { $outputs += @(Receive-Job $job -Wait -ErrorAction Stop) }
            }
            finally { Remove-Job $jobs -Force -ErrorAction SilentlyContinue }
            foreach ($record in $outputs) {
                foreach ($key in @($record.Keys)) { $results[$key] = $record[$key] }
            }
        }
    }
    foreach ($entry in $memberEntries) {
        $record = $results[[string]$entry.role_name]
        if ($null -ne $record -and $record.Contains('error') -and [string]$record.error) { throw [string]$record.error }
    }

    $readiness = Get-BSLFlowCouncilReadiness -PolicyRoles $Council.roles -RunRoot $RunRoot
    if (-not [bool]$readiness.chair_allowed) { throw ("BF_BLOCKED: chair cannot run: " + (@($readiness.blockers) -join '; ')) }

    # Chair fan-in over the canonical aggregate: findings, protected items,
    # brainstorm output, member questions, rubrics and classification. Chair
    # verdict and refs are validated structurally on assembly.
    $chairEntry = @($Bindings | Where-Object { [string]$_.role_name -ceq 'chair' })[0]
    if ($null -eq $chairEntry) { throw 'BF_BLOCKED: council chair must be enabled.' }
    $aggregate = [ordered]@{
        findings = @(); protected = @(); requirements = @($Snapshot.manifest.requirements)
        brainstorm = $null; questions = @()
        classification = [ordered]@{ complexity = $Snapshot.complexity; risk = $Snapshot.risk }
    }
    $questions = [System.Collections.Generic.List[object]]::new()
    foreach ($roleName in @('brainstorm', 'intent_critic', 'architecture_critic', 'executability_critic')) {
        if (-not [bool]$Council.roles[$roleName].enabled) { continue }
        $roleDir = Join-Path $RunRoot $roleName
        $resultFiles = @()
        if (Test-Path -LiteralPath $roleDir -PathType Container) { $resultFiles = @(Get-ChildItem -LiteralPath $roleDir -File -Filter 'result-*.json' | Sort-Object Name) }
        if ($resultFiles.Count -eq 0) { continue }
        $stored = Get-Content -Raw -LiteralPath $resultFiles[-1].FullName | ConvertFrom-Json -ErrorAction Stop
        if ([string]$stored.envelope.status -cne 'completed' -or $null -eq $stored.payload) { continue }
        $payload = $stored.payload
        if ($roleName -ceq 'brainstorm') {
            $aggregate.brainstorm = [ordered]@{
                alternatives = @(Get-BSLFlowEnvelopeValue $payload 'alternatives')
                risks = @(Get-BSLFlowEnvelopeValue $payload 'risks')
                unknowns = @(Get-BSLFlowEnvelopeValue $payload 'unknowns')
                questions = @(Get-BSLFlowEnvelopeValue $payload 'questions')
            }
            foreach ($question in @($aggregate.brainstorm.questions)) {
                $questions.Add([pscustomobject][ordered]@{ role = $roleName; text = [string]$question })
            }
            continue
        }
        $verdict = [string](Get-BSLFlowEnvelopeValue $payload 'verdict')
        foreach ($question in @((Get-BSLFlowEnvelopeValue $payload 'needs_input_questions') | Where-Object { $null -ne $_ })) {
            $questions.Add([pscustomobject]@{ role = $roleName; text = [string]$question })
        }
        foreach ($finding in @((Get-BSLFlowEnvelopeValue $payload 'findings') | Where-Object { $null -ne $_ })) {
            $aggregate.findings += [pscustomobject][ordered]@{
                composite_id = ("{0}:{1}" -f $roleName, [string]$finding.id); role = $roleName; id = [string]$finding.id
                severity = [string]$finding.severity; category = [string]$finding.category; spec_ref = [string]$finding.spec_ref
                issue = [string]$finding.issue; evidence = [string]$finding.evidence; suggested_direction = [string]$finding.suggested_direction
            }
        }
        $protectedIndex = 0
        foreach ($item in @((Get-BSLFlowEnvelopeValue $payload 'do_not_change') | Where-Object { $null -ne $_ -and ([string]$_).Trim() })) {
            $protectedIndex++
            $aggregate.protected += [pscustomobject][ordered]@{
                composite_id = ("{0}:do-not-change-{1:D3}" -f $roleName, $protectedIndex); role = $roleName; item = [string]$item
            }
        }
    }
    foreach ($question in $questions) { $aggregate.questions += $question }
    $aggregate.findings = @(Get-BSLFlowCanonicalFindings $aggregate.findings)
    # The chair prompt embeds the member aggregate, so the canonical aggregate
    # hash joins the chair binding before the attempt is resolved: any change in
    # member output (fresh results or new dispatches) forces a new chair attempt.
    $aggregateJson = $aggregate | ConvertTo-Json -Depth 10
    $aggregateHash = $null
    $sha = [System.Security.Cryptography.SHA256]::Create()
    try { $aggregateHash = ([BitConverter]::ToString($sha.ComputeHash([System.Text.Encoding]::UTF8.GetBytes($aggregateJson)))).Replace('-', '').ToLowerInvariant() } finally { $sha.Dispose() }
    $chairBindingInput = $chairEntry.binding.input_hashes
    $chairInputHashes = [pscustomobject][ordered]@{}
    foreach ($property in @($chairBindingInput.PSObject.Properties)) { $chairInputHashes | Add-Member -NotePropertyName $property.Name -NotePropertyValue $property.Value }
    $chairInputHashes | Add-Member -NotePropertyName member_aggregate_sha256 -NotePropertyValue $aggregateHash
    $chairBinding = [pscustomobject][ordered]@{}
    foreach ($property in @($chairEntry.binding.PSObject.Properties)) {
        if ($property.Name -ceq 'input_hashes') { $chairBinding | Add-Member -NotePropertyName input_hashes -NotePropertyValue $chairInputHashes }
        else { $chairBinding | Add-Member -NotePropertyName $property.Name -NotePropertyValue $property.Value }
    }
    $chairCapability = $null
    $chairExecutionMode = 'direct_api'
    $chairRoute = [ordered]@{ route = 'direct_api'; credential = $chairEntry.credential }
    if ($chairEntry.credential.credential_source -ceq 'missing') {
        if ($null -ne $Capabilities -and $Capabilities.ContainsKey('chair')) { $chairCapability = $Capabilities['chair'] }
        $chairPolicy = Assert-BSLFlowCouncilFallbackPolicy -Fallback ([string]$chairEntry.role.fallback) -Credential $chairEntry.credential -Capability $chairCapability
        $chairRoute.route = [string]$chairPolicy.route
        if ([string]$chairPolicy.route -ceq 'current_agent_fallback') {
            $chairRoute.reason = [string]$chairPolicy.reason
            $chairRoute.capability = $chairCapability
            $chairExecutionMode = 'current_agent_fallback'
            $chairBinding | Add-Member -NotePropertyName fallback_capability -NotePropertyValue (Get-BSLFlowCouncilCapabilityBinding $chairCapability) -Force
        }
    }
    $chairEntry | Add-Member -NotePropertyName binding -NotePropertyValue $chairBinding -Force
    $chairAttempt = New-BSLFlowCouncilAttempt -RunRoot $RunRoot -Role 'chair' -Binding $chairBinding
    $retainedChair = Get-BSLFlowCouncilRoleResult -RunRoot $RunRoot -Role 'chair' -Attempt $chairAttempt
    $chairRoute.attempt = $chairAttempt
    $chairRoute.attempt_path = (Join-Path $RunRoot 'chair')
    $chairView = [pscustomobject][ordered]@{
        original_task = $Snapshot.original_task_text; spec = $Snapshot.spec_text; design = $Snapshot.design_text
        evidence = $Snapshot.evidence_text; aggregates = $aggregateJson
    }
    $chairPrompt = New-BSLFlowCouncilPrompt -Role 'chair' -View $chairView

    $chairPayload = $null
    $chairDispatchInfo = $null
    if ($null -ne $retainedChair) {
        $chairStatus = [string]$retainedChair.envelope.status
        if ($chairStatus -ceq 'completed') {
            $chairPayload = $retainedChair.payload
            $chairDispatchInfo = [ordered]@{ envelope = $retainedChair.envelope; reused = $true }
        }
        elseif ($chairStatus -ceq 'unknown_after_dispatch') {
            throw "BF_BLOCKED: retained chair attempt $($chairAttempt.attempt_id) is unknown_after_dispatch; provider reconciliation is required before any new dispatch."
        }
        else { throw "BF_BLOCKED: retained chair result is $chairStatus; a new allowed attempt is required." }
    }
    if ($null -eq $chairPayload) {
        if ($null -ne $Cancelled -and (& $Cancelled)) {
            $null = Register-BSLFlowCouncilMemberResult -RunRoot $RunRoot -Attempt $chairAttempt -Payload $null -Status 'cancelled' -Summary 'Chair dispatch cancelled before the model call.' -Observed ([ordered]@{ provider = $null; model = $null; effort = $null }) -ExecutionMode $chairExecutionMode
            throw 'BF_BLOCKED: council cancelled before the chair dispatch.'
        }
        $chairDispatchedAt = [DateTime]::UtcNow
        $activeChairDispatcher = Resolve-BSLFlowCouncilDispatcher $Dispatcher
        if ($chairExecutionMode -ceq 'current_agent_fallback' -and $null -ne $FallbackRunner) {
            $activeChairDispatcher = New-BSLFlowCouncilFallbackDispatch -Runner $FallbackRunner
        }
        if ($null -ne $BeforeDispatch) {
            # The provider hook owns this exact chair admission. Fallback
            # worker reservations remain inside Invoke-BFManagedWorker.
            & $BeforeDispatch $chairAttempt $chairRoute
        }
        else {
            # Chair admission and reservation are one atomic ledger transaction.
            $null = Approve-BSLFlowCouncilBudgetDispatch -RunRoot $RunRoot -Role 'chair' -Attempt $chairAttempt -EstimateUsd $chairEntry.cost_estimate_usd -Budget $Council.budget
        }
        $chairDispatch = $null
        try { $chairDispatch = & $activeChairDispatcher $chairAttempt $chairPrompt $chairRoute }
        catch {
            $failure = [string]$_.Exception.Message
            $status = 'unknown_after_dispatch'
            if ($failure -match 'BF_FAILED_BEFORE_ACCEPTANCE') { $status = 'failed_before_acceptance' }
            elseif ($failure -match 'BF_INVALID_RESPONSE') { $status = 'invalid_response' }
            elseif ($failure -match 'BF_NOT_DISPATCHED') { $status = 'failed_before_acceptance' }
            $costState = if ($status -ceq 'failed_before_acceptance') { 'no_usage_reported' } else { 'unknown' }
            if ($null -ne $AfterDispatch) {
                try { & $AfterDispatch $chairAttempt $chairRoute $null $status $failure }
                catch {
                    $status = 'unknown_after_dispatch'
                    $costState = 'unknown'
                    $failure = 'AfterDispatch hook failed while recording the provider outcome.'
                }
            }
            $null = Register-BSLFlowCouncilMemberResult -RunRoot $RunRoot -Attempt $chairAttempt -Payload $null -Status $status -Summary (($failure -replace '[\r\n]+', ' ')) -Observed ([ordered]@{ provider = $null; model = $null; effort = $null }) -ExecutionMode $chairExecutionMode -DispatchedAtUtc $chairDispatchedAt -CompletedAtUtc ([DateTime]::UtcNow) -CostState $costState
            if ($null -eq $BeforeDispatch) {
                $null = Complete-BSLFlowCouncilBudgetOutcome -RunRoot $RunRoot -Role 'chair' -Attempt $chairAttempt -Status $status -ProviderReportedUsd $null -CostState $costState
            }
            throw ("BF_BLOCKED: chair did not complete ($status): " + $(if ($failure) { $failure } else { 'dispatch failed' }))
        }
        $chairCompletedAt = [DateTime]::UtcNow
        if ((Get-BSLFlowEnvelopeValue $chairDispatch 'status') -cne 'completed') {
            $status = [string](Get-BSLFlowEnvelopeValue $chairDispatch 'status')
            if ($status -notin @('failed_before_acceptance', 'unknown_after_dispatch', 'cancelled', 'invalid_response')) { $status = 'unknown_after_dispatch' }
            $failureSummary = "Chair dispatch returned $status."
            if ($null -ne $AfterDispatch) {
                try { & $AfterDispatch $chairAttempt $chairRoute $chairDispatch $status $failureSummary }
                catch { $status = 'unknown_after_dispatch'; $failureSummary = 'AfterDispatch hook failed while recording the provider outcome.' }
            }
            $null = Register-BSLFlowCouncilMemberResult -RunRoot $RunRoot -Attempt $chairAttempt -Payload $null -Status $status -Summary $failureSummary -Observed ([ordered]@{ provider = $null; model = $null; effort = $null }) -ExecutionMode $chairExecutionMode -DispatchedAtUtc $chairDispatchedAt -CompletedAtUtc $chairCompletedAt -CostState $(if ($status -ceq 'failed_before_acceptance') { 'no_usage_reported' } else { 'unknown' })
            if ($null -eq $BeforeDispatch) {
                $null = Complete-BSLFlowCouncilBudgetOutcome -RunRoot $RunRoot -Role 'chair' -Attempt $chairAttempt -Status $status -ProviderReportedUsd $null -CostState $(if ($status -ceq 'failed_before_acceptance') { 'no_usage_reported' } else { 'unknown' })
            }
            throw "BF_BLOCKED: chair did not complete: $status"
        }
        $chairPayload = Get-BSLFlowEnvelopeValue $chairDispatch 'payload'
        $chairDispatchUsage = Get-BSLFlowEnvelopeValue $chairDispatch 'usage'
        $chairCostState = if ($null -ne $chairDispatchUsage) { 'provider_usage_reported' } else { 'no_usage_reported' }
        if ($null -ne $AfterDispatch) {
            try { & $AfterDispatch $chairAttempt $chairRoute $chairDispatch 'completed' $null }
            catch {
                $unknownSummary = 'AfterDispatch hook failed while recording the provider outcome.'
                $chairEnvelope = Register-BSLFlowCouncilMemberResult -RunRoot $RunRoot -Attempt $chairAttempt -Payload $null -Status 'unknown_after_dispatch' -Summary $unknownSummary -Observed ([ordered]@{ provider = $null; model = $null; effort = $null }) -ExecutionMode $chairExecutionMode -DispatchedAtUtc $chairDispatchedAt -CompletedAtUtc $chairCompletedAt -CostState 'unknown'
                throw 'BF_BLOCKED: chair provider outcome is unknown_after_dispatch.'
            }
        }
        $chairEnvelope = Register-BSLFlowCouncilMemberResult -RunRoot $RunRoot -Attempt $chairAttempt -Payload $chairPayload -Status 'completed' -Summary 'Chair reconciliation completed.' -Observed (Get-BSLFlowEnvelopeValue $chairDispatch 'observed') -ExecutionMode ([string](Get-BSLFlowEnvelopeValue $chairDispatch 'execution_mode')) -DispatchedAtUtc $chairDispatchedAt -CompletedAtUtc $chairCompletedAt -Usage $chairDispatchUsage -CostState $chairCostState
        $providerUsd = $null
        try { $reportedCost = Get-BSLFlowEnvelopeValue $chairDispatch 'reported_cost_usd'; if ($null -ne $reportedCost) { $providerUsd = [double]$reportedCost } } catch { $providerUsd = $null }
        if ($null -eq $BeforeDispatch) {
            $null = Complete-BSLFlowCouncilBudgetOutcome -RunRoot $RunRoot -Role 'chair' -Attempt $chairAttempt -Status 'completed' -ProviderReportedUsd $providerUsd -CostState $chairCostState
        }
        $chairDispatchInfo = [ordered]@{ envelope = $chairEnvelope; reused = $false }
    }

    $chair = $chairPayload
    foreach ($field in @('verdict', 'decisions', 'protected_decisions', 'requirement_refs', 'final_spec_text')) {
        if (-not (Test-BSLFlowEnvelopeField $chair $field)) { throw "BF_BLOCKED: chair payload misses field: $field." }
    }
    $chairEnvelope = $chairDispatchInfo.envelope
    if (-not $chairDispatchInfo.reused) {
        # The dispatch already registered the chair envelope with controller-
        # observed provenance; nothing else to write here.
    }

    $memberEnvelopes = @()
    foreach ($roleName in @('brainstorm', 'intent_critic', 'architecture_critic', 'executability_critic')) {
        if (-not [bool]$Council.roles[$roleName].enabled) { continue }
        $roleDir = Join-Path $RunRoot $roleName
        $resultFiles = @()
        if (Test-Path -LiteralPath $roleDir -PathType Container) { $resultFiles = @(Get-ChildItem -LiteralPath $roleDir -File -Filter 'result-*.json' | Sort-Object Name) }
        if ($resultFiles.Count -eq 0) { continue }
        $stored = Get-Content -Raw -LiteralPath $resultFiles[-1].FullName | ConvertFrom-Json -ErrorAction Stop
        # Every enabled role's terminal envelope is published: optional failures
        # must stay visible in provenance and force degraded diversity.
        $memberEnvelopes += $stored.envelope
    }
    $memberEnvelopes += $chairEnvelope
    $diversity = Get-BSLFlowCouncilDiversity $memberEnvelopes
    $utf8NoBom = [System.Text.UTF8Encoding]::new($false)
    # The chair contract requires the complete final specification text; the
    # draft is never silently republished as the final bytes.
    $chairFinalSpec = Get-BSLFlowEnvelopeValue $chair 'final_spec_text'
    $chairFinalDesign = Get-BSLFlowEnvelopeValue $chair 'final_design_text'
    if ([string]::IsNullOrWhiteSpace([string]$chairFinalSpec)) { throw 'BF_BLOCKED: chair payload misses final_spec_text.' }
    $finalSpecBytes = $utf8NoBom.GetBytes([string]$chairFinalSpec)
    $finalDesignBytes = $null
    if (Test-Path -LiteralPath (Join-Path $changeDir 'design.md') -PathType Leaf) {
        $finalDesignBytes = [System.IO.File]::ReadAllBytes((Join-Path $changeDir 'design.md'))
    }
    if ($null -ne $chairFinalDesign) {
        $finalDesignBytes = $utf8NoBom.GetBytes([string]$chairFinalDesign)
    }
    $review = [pscustomobject][ordered]@{
        schema_version = 2; reviewed_at_utc = [DateTime]::UtcNow.ToString('o'); council_schema_version = 1
        verdict = [string]$chair.verdict; diversity = [string]$diversity.diversity; fallback_visible = [bool]$diversity.fallback_visible
        inputs = [pscustomobject][ordered]@{
            original_task_sha256 = $Snapshot.original_task_sha256; spec_sha256 = $Snapshot.spec_sha256
            design_sha256 = $Snapshot.design_sha256; policy_hash = $Snapshot.policy_hash
        }
        manifest = $Snapshot.manifest
        members = @($memberEnvelopes)
        findings = @($aggregate.findings)
        protected = @($aggregate.protected)
        questions = @($aggregate.questions)
        chair = [pscustomobject][ordered]@{
            verdict = [string]$chair.verdict; decisions = @($chair.decisions)
            protected_decisions = @($chair.protected_decisions); requirement_refs = @($chair.requirement_refs)
            final_spec_text = [string]$chairFinalSpec
            final_design_text = $(if ($null -ne $chairFinalDesign) { [string]$chairFinalDesign } else { $null })
        }
        reconciliation = [pscustomobject][ordered]@{
            review_sha256 = ('0' * 64); draft_spec_sha256 = $Snapshot.spec_sha256
            final_spec_sha256 = (Get-BSLFlowBytesSha256 $finalSpecBytes)
            draft_design_sha256 = $Snapshot.design_sha256
            final_design_sha256 = $(if ($null -ne $finalDesignBytes) { Get-BSLFlowBytesSha256 $finalDesignBytes } else { $null })
        }
        gate = [pscustomobject][ordered]@{ structural_only = $true; passed = $true }
    }
    $review.reconciliation.review_sha256 = Get-BSLFlowCouncilReviewDigest $review
    Assert-BSLFlowCouncilReview $review
    $existingReviewBytes = $null
    if (Test-Path -LiteralPath (Join-Path $changeDir 'review.json') -PathType Leaf) {
        $existingReviewBytes = [System.IO.File]::ReadAllBytes((Join-Path $changeDir 'review.json'))
    }
    $null = New-BSLFlowCouncilPreparedPackage -RunRoot $RunRoot -Review $review -FinalSpecBytes $finalSpecBytes -FinalDesignBytes $finalDesignBytes -ExistingReviewBytes $existingReviewBytes
    $null = Resume-BSLFlowCouncilPublication -ChangeDir $changeDir -RunRoot $RunRoot -Review $review -ProjectPath $ProjectRoot
    $final = & (Join-Path $PSScriptRoot 'Test-1CSpecFinal.ps1') -ProjectPath $ProjectRoot -ChangeName $ChangeName
    return [ordered]@{ review = $review; run_root = $RunRoot; final_validation = $final }
}

function Get-BSLFlowEnvelopeValue {
    param($Object, [Parameter(Mandatory)][string]$Name)
    if ($null -eq $Object) { return $null }
    if ($Object -is [System.Collections.IDictionary]) {
        if ($Object.Contains($Name)) { return $Object[$Name] }
        return $null
    }
    $property = $Object.PSObject.Properties[$Name]
    if ($null -eq $property) { return $null }
    return $property.Value
}

function Test-BSLFlowEnvelopeField {
    param($Object, [Parameter(Mandatory)][string]$Name)
    if ($null -eq $Object) { return $false }
    if ($Object -is [System.Collections.IDictionary]) { return $Object.Contains($Name) }
    return ($null -ne $Object.PSObject.Properties[$Name])
}
