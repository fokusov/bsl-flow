#Requires -Version 7.0
Set-StrictMode -Version Latest

# Stage 2: council engine core without network.
# Snapshot, role views, attempt binding, readiness, aggregate and prepared publication.
# One writer persists aggregate artifacts. No model dispatch here.

function Get-BSLFlowCouncilPolicyHash {
    # -PolicyText hashes the effective merged policy (project + user profile)
    # directly; -PolicyPath keeps the legacy raw-file behavior.
    param(
        [Parameter(Mandatory, Position = 0, ParameterSetName = 'Path')][string]$PolicyPath,
        [Parameter(Mandatory, ParameterSetName = 'Text')][AllowEmptyString()][string]$PolicyText
    )
    # The council snapshot hashes the decoded UTF-8 policy text. Read it through
    # the same BOM-stripping decoder during recovery, so a UTF-8 BOM is encoding
    # metadata rather than a false policy change.
    $utf8 = [System.Text.UTF8Encoding]::new($false)
    if ($PSCmdlet.ParameterSetName -ceq 'Text') {
        return Get-BSLFlowBytesSha256 ($utf8.GetBytes($PolicyText))
    }
    if (-not (Test-Path -LiteralPath $PolicyPath -PathType Leaf)) {
        throw "Council policy file not found: $PolicyPath"
    }
    $text = [System.IO.File]::ReadAllText($PolicyPath, $utf8)
    return Get-BSLFlowBytesSha256 ($utf8.GetBytes($text))
}

function New-BSLFlowCouncilSnapshot {
    param(
        [Parameter(Mandatory)][string]$ChangeDir,
        [int]$MaxBytes = 262144,
        [string]$EvidenceText = '',
        [string]$PolicyHash = ('0' * 64)
    )
    . (Join-Path $PSScriptRoot 'Review.Common.ps1')
    . (Join-Path $PSScriptRoot 'Council.Validation.ps1')
    $originalPath = Join-Path $ChangeDir 'original-task.md'
    $specPath = Join-Path $ChangeDir 'spec.md'
    $designPath = Join-Path $ChangeDir 'design.md'
    foreach ($required in @($originalPath, $specPath)) {
        if (-not (Test-Path -LiteralPath $required -PathType Leaf)) { throw "Council snapshot missing required input: $required" }
    }
    $original = Get-BSLFlowBoundedUtf8Snapshot -Path $originalPath -MaxBytes $MaxBytes
    $spec = Get-BSLFlowBoundedUtf8Snapshot -Path $specPath -MaxBytes $MaxBytes
    $design = $null
    if (Test-Path -LiteralPath $designPath -PathType Leaf) {
        $design = Get-BSLFlowBoundedUtf8Snapshot -Path $designPath -MaxBytes $MaxBytes
    }
    $complexityMatches = [regex]::Matches($spec.Text, '(?im)^\s*-\s*(?:Сложность|Complexity):\s*(S|M|L)\s*$')
    $riskMatches = [regex]::Matches($spec.Text, '(?im)^\s*-\s*(?:Риск|Risk):\s*(low|medium|high)\s*$')
    if ($complexityMatches.Count -ne 1 -or $riskMatches.Count -ne 1) { throw 'Council snapshot requires exactly one complexity and one risk classification.' }
    $manifest = New-BSLFlowRequirementManifest $spec.Text
    $evidenceHash = $null
    if (-not [string]::IsNullOrEmpty($EvidenceText)) {
        $utf8 = [System.Text.UTF8Encoding]::new($false)
        if ($utf8.GetByteCount($EvidenceText) -gt $MaxBytes) { throw "Council evidence exceeds $MaxBytes bytes." }
        $sha = [System.Security.Cryptography.SHA256]::Create()
        try { $evidenceHash = ([BitConverter]::ToString($sha.ComputeHash($utf8.GetBytes($EvidenceText)))).Replace('-', '').ToLowerInvariant() }
        finally { $sha.Dispose() }
    }
    if ($PolicyHash -cnotmatch '^[a-f0-9]{64}$') { throw 'Council snapshot needs a policy hash.' }
    return [pscustomobject][ordered]@{
        original_task_sha256 = $original.Sha256
        spec_sha256 = $spec.Sha256
        design_sha256 = if ($null -ne $design) { $design.Sha256 } else { $null }
        original_task_text = $original.Text
        spec_text = $spec.Text
        design_text = if ($null -ne $design) { $design.Text } else { $null }
        complexity = $complexityMatches[0].Groups[1].Value.ToUpperInvariant()
        risk = $riskMatches[0].Groups[1].Value.ToLowerInvariant()
        manifest = $manifest
        evidence_text = $EvidenceText
        evidence_sha256 = $evidenceHash
        policy_hash = $PolicyHash
    }
}

function Get-BSLFlowCouncilRoleView {
    param(
        [Parameter(Mandatory)]$Snapshot,
        [Parameter(Mandatory)][ValidateSet('brainstorm', 'intent_critic', 'architecture_critic', 'executability_critic')][string]$Role,
        [string]$RubricText = ''
    )
    $classification = [pscustomobject][ordered]@{ complexity = $Snapshot.complexity; risk = $Snapshot.risk }
    if ($Role -ceq 'brainstorm') {
        return [pscustomobject][ordered]@{
            role = $Role
            original_task = $Snapshot.original_task_text
            evidence = $Snapshot.evidence_text
            classification = $classification
        }
    }
    return [pscustomobject][ordered]@{
        role = $Role
        original_task = $Snapshot.original_task_text
        spec = $Snapshot.spec_text
        design = $Snapshot.design_text
        evidence = $Snapshot.evidence_text
        classification = $classification
        rubric = $RubricText
    }
}

function Get-BSLFlowCouncilLatestAttempt {
    param(
        [Parameter(Mandatory)][string]$RunRoot,
        [Parameter(Mandatory)][ValidateSet('brainstorm', 'intent_critic', 'architecture_critic', 'executability_critic', 'chair')][string]$Role
    )
    $roleDir = Join-Path $RunRoot $Role
    if (-not (Test-Path -LiteralPath $roleDir -PathType Container)) { return $null }
    $latest = @(Get-ChildItem -LiteralPath $roleDir -File -Filter 'attempt-*.json' | Sort-Object Name | Select-Object -Last 1)
    if ($latest.Count -eq 0) { return $null }
    return (Get-Content -Raw -LiteralPath $latest[0].FullName | ConvertFrom-Json -ErrorAction Stop)
}

function New-BSLFlowCouncilAttempt {
    param(
        [Parameter(Mandatory)][string]$RunRoot,
        [Parameter(Mandatory)][ValidateSet('brainstorm', 'intent_critic', 'architecture_critic', 'executability_critic', 'chair')][string]$Role,
        [Parameter(Mandatory)]$Binding
    )
    . (Join-Path $PSScriptRoot 'Review.Common.ps1')
    . (Join-Path $PSScriptRoot 'Council.Validation.ps1')
    foreach ($field in @('provider', 'model', 'effort', 'protocol', 'endpoint', 'transport_capability_version', 'prompt_version', 'member_schema_version', 'input_hashes')) {
        if ($null -eq $Binding.$field) { throw "Attempt binding misses field: $field" }
    }
    foreach ($field in @('scheme', 'host', 'port', 'base_path')) {
        if ($null -eq $Binding.endpoint.$field) { throw "Attempt binding endpoint misses field: $field" }
    }
    foreach ($field in @('original_task_sha256', 'spec_sha256', 'design_sha256', 'evidence_sha256', 'policy_hash', 'rubric_sha256')) {
        if (-not (Test-BSLFlowEnvelopeField2 $Binding.input_hashes $field)) { throw "Attempt binding input_hashes misses field: $field" }
    }
    if ($null -ne $Binding.PSObject.Properties['fallback_capability']) {
        $fallback = $Binding.fallback_capability
        if ($null -eq $fallback -or [string]$fallback.sha256 -notmatch '^[a-f0-9]{64}$' -or $null -eq $fallback.identity) {
            throw 'Attempt binding fallback capability is incomplete.'
        }
        foreach ($field in @('capability_version', 'provider', 'model', 'effort', 'fresh_context', 'sealed', 'terminal')) {
            if ($null -eq $fallback.identity.PSObject.Properties[$field]) { throw "Attempt binding fallback capability misses field: $field" }
        }
    }
    # Secrets never belong in the binding.
    $serialized = $Binding | ConvertTo-Json -Depth 10
    if ($serialized -match '(?i)"token"\s*:') { throw 'Attempt binding must not contain a token.' }
    if ($serialized -match '(?i)Authorization') { throw 'Attempt binding must not contain Authorization material.' }
    $roleDir = Join-Path $RunRoot $Role
    New-Item -ItemType Directory -Path $roleDir -Force | Out-Null
    $bindingHash = Get-BSLFlowBytesSha256 ([System.Text.Encoding]::UTF8.GetBytes($serialized))
    # Sequenced attempts preserve retained evidence: identical binding reuses the
    # latest attempt, legitimate drift creates the next sequence entry instead of
    # throwing BF_STALE or deleting history.
    $existingFiles = @(Get-ChildItem -LiteralPath $roleDir -File -Filter 'attempt-*.json' | Sort-Object Name)
    if ($existingFiles.Count -gt 0) {
        $latest = Get-Content -Raw -LiteralPath $existingFiles[-1].FullName | ConvertFrom-Json -ErrorAction Stop
        $latestHash = Get-BSLFlowBytesSha256 ([System.Text.Encoding]::UTF8.GetBytes(($latest.binding | ConvertTo-Json -Depth 10)))
        if ($latestHash -ceq $bindingHash) { return $latest }
        $sequence = $existingFiles.Count + 1
    }
    else { $sequence = 1 }
    $attempt = [pscustomobject][ordered]@{
        schema_version = 1
        attempt_id = ([guid]::NewGuid().ToString('N'))
        sequence = $sequence
        role = $Role
        created_at_utc = [DateTime]::UtcNow.ToString('o')
        binding = $Binding
        binding_sha256 = $bindingHash
    }
    Write-BSLFlowJsonAtomic -Value $attempt -Path (Join-Path $roleDir ('attempt-{0:D4}.json' -f $sequence))
    return $attempt
}

function Register-BSLFlowCouncilMemberResult {
    param(
        [Parameter(Mandatory)][string]$RunRoot,
        [Parameter(Mandatory)]$Attempt,
        [AllowNull()]$Payload,
        [Parameter(Mandatory)][ValidateSet('completed', 'failed_before_acceptance', 'unknown_after_dispatch', 'cancelled', 'invalid_response')][string]$Status,
        [Parameter(Mandatory)]$Observed,
        [Parameter(Mandatory)][ValidateSet('direct_api', 'current_agent_fallback')][string]$ExecutionMode,
        [Parameter(Mandatory)][string]$Summary,
        [string]$FallbackReason,
        [datetime]$DispatchedAtUtc,
        [datetime]$CompletedAtUtc,
        $Usage,
        [ValidateSet('unknown', 'provider_usage_reported', 'no_usage_reported')][string]$CostState
    )
    . (Join-Path $PSScriptRoot 'Review.Common.ps1')
    . (Join-Path $PSScriptRoot 'Council.Validation.ps1')
    $role = [string]$Attempt.role
    if ($Status -ceq 'completed') {
        if ($role -ceq 'brainstorm') { Assert-BSLFlowBrainstormPayload $Payload }
        elseif ($role -ceq 'chair') { Assert-BSLFlowCouncilChairResult $Payload }
        else { Assert-BSLFlowCouncilModelPayload $Payload }
    }
    $payloadHash = $null
    if ($Status -ceq 'completed') {
        $payloadHash = Get-BSLFlowBytesSha256 ([System.Text.Encoding]::UTF8.GetBytes(($Payload | ConvertTo-Json -Depth 20)))
    }
    $resolvedCostState = $CostState
    if ($Status -ceq 'completed' -and -not $resolvedCostState) {
        $resolvedCostState = if ($null -ne $Usage) { 'provider_usage_reported' } else { 'no_usage_reported' }
    }
    if (-not $resolvedCostState) { $resolvedCostState = 'unknown' }
    $usageRecord = $null
    if ($null -ne $Usage) {
        # Accept the provider vocabularies: OpenAI input_tokens/output_tokens and
        # chat prompt_tokens/completion_tokens, plus nested reasoning details.
        $inputValue = Get-BSLFlowEnvelopeValue2 $Usage 'input_tokens'
        if ($null -eq $inputValue) { $inputValue = Get-BSLFlowEnvelopeValue2 $Usage 'prompt_tokens' }
        $outputValue = Get-BSLFlowEnvelopeValue2 $Usage 'output_tokens'
        if ($null -eq $outputValue) { $outputValue = Get-BSLFlowEnvelopeValue2 $Usage 'completion_tokens' }
        $reasoningValue = Get-BSLFlowEnvelopeValue2 $Usage 'reasoning_tokens'
        if ($null -eq $reasoningValue) {
            $completionDetails = Get-BSLFlowEnvelopeValue2 $Usage 'completion_tokens_details'
            if ($null -ne $completionDetails) { $reasoningValue = Get-BSLFlowEnvelopeValue2 $completionDetails 'reasoning_tokens' }
        }
        $usageRecord = [pscustomobject][ordered]@{
            input_tokens = $(if ($null -ne $inputValue) { [long]$inputValue } else { $null })
            output_tokens = $(if ($null -ne $outputValue) { [long]$outputValue } else { $null })
            reasoning_tokens = $(if ($null -ne $reasoningValue) { [long]$reasoningValue } else { $null })
        }
    }
    $envelope = [pscustomobject][ordered]@{
        schema_version = 1
        role = $role
        attempt_id = [string]$Attempt.attempt_id
        status = $Status
        summary = [string]$Summary
        requested = [pscustomobject][ordered]@{
            provider = [string]$Attempt.binding.provider
            model = [string]$Attempt.binding.model
            effort = [string]$Attempt.binding.effort
        }
        observed = [pscustomobject][ordered]@{
            provider = if ($null -ne $Observed.provider) { [string]$Observed.provider } else { $null }
            model = if ($null -ne $Observed.model) { [string]$Observed.model } else { $null }
            effort = if ($null -ne $Observed.effort) { [string]$Observed.effort } else { $null }
        }
        execution_mode = $ExecutionMode
        fallback_reason = $FallbackReason
        input_hashes = $Attempt.binding.input_hashes
        payload_sha256 = $payloadHash
        usage = $usageRecord
        cost_state = $resolvedCostState
        dispatched_at_utc = $(if ($null -ne $DispatchedAtUtc) { $DispatchedAtUtc.ToUniversalTime().ToString('o') } else { $null })
        completed_at_utc = $(if ($null -ne $CompletedAtUtc) { $CompletedAtUtc.ToUniversalTime().ToString('o') } else { $null })
    }
    $roleDir = Join-Path $RunRoot $role
    New-Item -ItemType Directory -Path $roleDir -Force | Out-Null
    # Immutable terminal result bound to the attempt sequence: never rewritten,
    # so resume classifies the exact attempt instead of dispatching it again.
    $resultPath = Join-Path $roleDir ('result-{0:D4}.json' -f [int]$Attempt.sequence)
    if (Test-Path -LiteralPath $resultPath -PathType Leaf) {
        $existing = Get-Content -Raw -LiteralPath $resultPath | ConvertFrom-Json -ErrorAction Stop
        if ([string]$existing.envelope.attempt_id -cne [string]$Attempt.attempt_id) {
            throw 'BF_BLOCKED: terminal result for another attempt already occupies this sequence.'
        }
    }
    else {
        Write-BSLFlowJsonAtomic -Value ([ordered]@{ envelope = $envelope; payload = $(if ($Status -ceq 'completed') { $Payload } else { $null }) }) -Path $resultPath
    }
    return $envelope
}

function Get-BSLFlowEnvelopeValue2 {
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

function Get-BSLFlowCouncilRoleResult {
    param(
        [Parameter(Mandatory)][string]$RunRoot,
        [Parameter(Mandatory)][ValidateSet('brainstorm', 'intent_critic', 'architecture_critic', 'executability_critic', 'chair')][string]$Role,
        [Parameter(Mandatory)]$Attempt
    )
    $resultPath = Join-Path (Join-Path $RunRoot $Role) ('result-{0:D4}.json' -f [int]$Attempt.sequence)
    if (-not (Test-Path -LiteralPath $resultPath -PathType Leaf)) { return $null }
    $stored = Get-Content -Raw -LiteralPath $resultPath | ConvertFrom-Json -ErrorAction Stop
    if ([string]$stored.envelope.attempt_id -cne [string]$Attempt.attempt_id) { return $null }
    return $stored
}

function Get-BSLFlowCouncilReadiness {
    param(
        [Parameter(Mandatory)]$PolicyRoles,
        [Parameter(Mandatory)][string]$RunRoot
    )
    . (Join-Path $PSScriptRoot 'Review.Common.ps1')
    $blockers = [System.Collections.Generic.List[string]]::new()
    $completed = 0
    foreach ($name in @('brainstorm', 'intent_critic', 'architecture_critic', 'executability_critic')) {
        $role = $PolicyRoles.$name
        if ($null -eq $role -or -not [bool]$role.enabled) { continue }
        $roleDir = Join-Path $RunRoot $name
        $resultFiles = @()
        if (Test-Path -LiteralPath $roleDir -PathType Container) { $resultFiles = @(Get-ChildItem -LiteralPath $roleDir -File -Filter 'result-*.json' | Sort-Object Name) }
        if ($resultFiles.Count -eq 0) {
            $blockers.Add("Role has no terminal result: $name")
            continue
        }
        $stored = Get-Content -Raw -LiteralPath $resultFiles[-1].FullName | ConvertFrom-Json -ErrorAction Stop
        $status = [string]$stored.envelope.status
        if ($status -cne 'completed') {
            if ([bool]$role.required) { $blockers.Add("Required role is not completed: $name ($status)") }
            continue
        }
        $completed++
    }
    return [ordered]@{ chair_allowed = ($blockers.Count -eq 0); completed_roles = $completed; blockers = @($blockers) }
}

function Get-BSLFlowCouncilBudgetLedger {
    param([Parameter(Mandatory)][string]$RunRoot)
    # Durable per-dispatch ledger: reservations are created before network/host
    # dispatch and reconciled with terminal outcomes, so unknown cost persists
    # across resume instead of being treated as zero.
    $ledgerDir = Join-Path $RunRoot 'budget'
    New-Item -ItemType Directory -Path $ledgerDir -Force | Out-Null
    return $ledgerDir
}

function Invoke-BSLFlowCouncilLedgerLocked {
    # Ledger admission/reservation/outcome updates serialize across thread jobs
    # and processes through one named mutex, so bounded-parallel dispatches
    # never race on admission or double-book a reservation.
    param(
        [Parameter(Mandatory)][string]$RunRoot,
        [Parameter(Mandatory)][scriptblock]$Body,
        [object[]]$BodyArgs = @()
    )
    $hashBytes = [System.Security.Cryptography.SHA256]::Create().ComputeHash([System.Text.Encoding]::UTF8.GetBytes($RunRoot))
    $hex = [System.Text.StringBuilder]::new()
    foreach ($b in $hashBytes) { [void]$hex.Append($b.ToString('x2')) }
    $mutexName = 'Global\bsl-flow-council-ledger-' + $hex.ToString()
    $created = $false
    $mutex = [Threading.Mutex]::new($false, $mutexName, [ref]$created)
    try {
        [void]$mutex.WaitOne(30000)
        try { return (& $Body @BodyArgs) } finally { [void]$mutex.ReleaseMutex() }
    }
    finally { $mutex.Dispose() }
}

function Add-BSLFlowCouncilBudgetReservation {
    param(
        [Parameter(Mandatory)][string]$RunRoot,
        [Parameter(Mandatory)][string]$Role,
        [Parameter(Mandatory)]$Attempt,
        [AllowNull()]$EstimateUsd
    )
    . (Join-Path $PSScriptRoot 'Review.Common.ps1')
    $estimate = $null
    if ($null -ne $EstimateUsd) { $estimate = [double]$EstimateUsd }
    return (Invoke-BSLFlowCouncilLedgerLocked -RunRoot $RunRoot -Body {
        param($RunRoot, $Role, $Attempt, $Estimate)
        $ledgerDir = Get-BSLFlowCouncilBudgetLedger -RunRoot $RunRoot
        $path = Join-Path $ledgerDir ('reservation-{0}-{1:D4}.json' -f $Role, [int]$Attempt.sequence)
        Write-BSLFlowJsonAtomic -Value ([ordered]@{
            role = $Role; attempt_id = [string]$Attempt.attempt_id; sequence = [int]$Attempt.sequence
            estimated_usd = $Estimate; outcome = 'open'; outcome_usd = $null
            cost_state = 'unknown'; recorded_at_utc = [DateTime]::UtcNow.ToString('o')
        }) -Path $path
        return $path
    } @($RunRoot, $Role, $Attempt, $estimate))
}

function Approve-BSLFlowCouncilBudgetDispatch {
    # One atomic ledger transaction: admission check and reservation creation
    # happen under a single mutex hold, so parallel roles can never pass
    # admission against the same pre-reservation ledger state.
    param(
        [Parameter(Mandatory)][string]$RunRoot,
        [Parameter(Mandatory)][string]$Role,
        [Parameter(Mandatory)]$Attempt,
        [AllowNull()]$EstimateUsd,
        [AllowNull()]$Budget
    )
    . (Join-Path $PSScriptRoot 'Review.Common.ps1')
    $estimate = $null
    if ($null -ne $EstimateUsd) { $estimate = [double]$EstimateUsd }
    return (Invoke-BSLFlowCouncilLedgerLocked -RunRoot $RunRoot -Body {
        param($RunRoot, $Role, $Attempt, $Estimate, $Budget)

        function Get-LedgerTotals {
            param([string]$RunRoot)
            $prior = 0.0
            $hasUnknown = $false
            $ledgerDir = Get-BSLFlowCouncilBudgetLedger -RunRoot $RunRoot
            foreach ($file in @(Get-ChildItem -LiteralPath $ledgerDir -File -Filter 'reservation-*.json' -ErrorAction SilentlyContinue)) {
                $entry = Get-Content -Raw -LiteralPath $file.FullName | ConvertFrom-Json -ErrorAction Stop
                if ([string]$entry.cost_state -ceq 'unknown') { $hasUnknown = $true }
                $spent = $entry.outcome_usd
                if ($null -ne $spent) { $prior += [double]$spent; continue }
                if ($null -ne $entry.estimated_usd) { $prior += [double]$entry.estimated_usd }
            }
            return [ordered]@{ prior_usd = $prior; has_unknown = $hasUnknown }
        }

        $totals = Get-LedgerTotals -RunRoot $RunRoot
        $limit = $null
        $reservationFloor = 0.0
        if ($null -ne $Budget) {
            try { $limit = $Budget.limit } catch { $limit = $null }
            try { if ($null -ne $Budget.reservation) { $reservationFloor = [double]$Budget.reservation } } catch { $reservationFloor = 0.0 }
        }
        if ($null -ne $limit) {
            $limitNumber = [double]$limit
            $total = [double]$totals.prior_usd + $Estimate
            if ([double]::IsNaN($total) -or [double]::IsInfinity($total) -or $total -lt 0) { throw 'BF_BLOCKED: dispatch cost estimate must be a non-negative finite number.' }
            if ($total + $reservationFloor -gt $limitNumber) {
                throw 'BF_BLOCKED: council budget admission refused for this dispatch.'
            }
        }
        $ledgerDir = Get-BSLFlowCouncilBudgetLedger -RunRoot $RunRoot
        $path = Join-Path $ledgerDir ('reservation-{0}-{1:D4}.json' -f $Role, [int]$Attempt.sequence)
        Write-BSLFlowJsonAtomic -Value ([ordered]@{
            role = $Role; attempt_id = [string]$Attempt.attempt_id; sequence = [int]$Attempt.sequence
            estimated_usd = $Estimate; outcome = 'open'; outcome_usd = $null
            cost_state = 'unknown'; recorded_at_utc = [DateTime]::UtcNow.ToString('o')
        }) -Path $path
        return [ordered]@{ reservation_path = $path; prior_usd = [double]$totals.prior_usd; has_unknown = [bool]$totals.has_unknown; admitted_total_usd = ([double]$totals.prior_usd + $Estimate) }
    } @($RunRoot, $Role, $Attempt, $estimate, $Budget))
}

function Complete-BSLFlowCouncilBudgetOutcome {
    param(
        [Parameter(Mandatory)][string]$RunRoot,
        [Parameter(Mandatory)][string]$Role,
        [Parameter(Mandatory)]$Attempt,
        [Parameter(Mandatory)][ValidateSet('completed', 'failed_before_acceptance', 'unknown_after_dispatch', 'cancelled', 'invalid_response')][string]$Status,
        [AllowNull()]$ProviderReportedUsd,
        [string]$CostState = 'unknown'
    )
    . (Join-Path $PSScriptRoot 'Review.Common.ps1')
    return (Invoke-BSLFlowCouncilLedgerLocked -RunRoot $RunRoot -Body {
        param($RunRoot, $Role, $Attempt, $Status, $ProviderReported, $CostState)
        $ledgerDir = Get-BSLFlowCouncilBudgetLedger -RunRoot $RunRoot
        $path = Join-Path $ledgerDir ('reservation-{0}-{1:D4}.json' -f $Role, [int]$Attempt.sequence)
        if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { throw "BF_BLOCKED: budget reservation is missing for $Role." }
        $entry = Get-Content -Raw -LiteralPath $path | ConvertFrom-Json -ErrorAction Stop
        if ([string]$entry.attempt_id -cne [string]$Attempt.attempt_id) { throw "BF_BLOCKED: budget reservation belongs to another attempt for $Role." }
        # Only unknown cost is preserved as unknown; a provider-reported amount is
        # stored separately from the pre-dispatch estimate and never overwrites it.
        $entry | Add-Member -NotePropertyName outcome -NotePropertyValue ([string]$Status) -Force
        $entry | Add-Member -NotePropertyName outcome_usd -NotePropertyValue $(if ($null -ne $ProviderReported) { [double]$ProviderReported } else { $null }) -Force
        $entry | Add-Member -NotePropertyName cost_state -NotePropertyValue ([string]$CostState) -Force
        $entry | Add-Member -NotePropertyName outcome_recorded_at_utc -NotePropertyValue ([DateTime]::UtcNow.ToString('o')) -Force
        Write-BSLFlowJsonAtomic -Value $entry -Path $path
        return $entry
    } @($RunRoot, $Role, $Attempt, $Status, $ProviderReportedUsd, $CostState))
}

function Test-BSLFlowCouncilLedgerAdmission {
    param(
        [Parameter(Mandatory)]$Budget,
        [Parameter(Mandatory)][string]$RunRoot,
        [Parameter(Mandatory)]$Dispatches
    )
    # Admission covers the whole council cycle: every terminal reservation stays
    # in the total, and an unknown outcome keeps blocking until reconciled.
    # The read happens under the ledger mutex so concurrent dispatches cannot
    # observe a half-written reservation.
    $ledgerRead = Invoke-BSLFlowCouncilLedgerLocked -RunRoot $RunRoot -Body {
        param($RunRoot)
        $prior = 0.0
        $hasUnknown = $false
        $ledgerDir = Get-BSLFlowCouncilBudgetLedger -RunRoot $RunRoot
        foreach ($file in @(Get-ChildItem -LiteralPath $ledgerDir -File -Filter 'reservation-*.json' -ErrorAction SilentlyContinue)) {
            $entry = Get-Content -Raw -LiteralPath $file.FullName | ConvertFrom-Json -ErrorAction Stop
            if ([string]$entry.cost_state -ceq 'unknown') { $hasUnknown = $true }
            $spent = $entry.outcome_usd
            if ($null -ne $spent) { $prior += [double]$spent; continue }
            if ($null -ne $entry.estimated_usd) { $prior += [double]$entry.estimated_usd }
        }
        return [ordered]@{ prior_usd = $prior; has_unknown = $hasUnknown }
    } @($RunRoot)
    $prior = [double]$ledgerRead.prior_usd
    $hasUnknown = [bool]$ledgerRead.has_unknown
    $list = @($Dispatches)
    $total = $prior
    foreach ($dispatch in $list) {
        $estimate = $null
        try { $estimate = $dispatch.cost_estimate_usd } catch { $estimate = $null }
        if ($null -eq $estimate) { throw 'BF_BLOCKED: unknown dispatch cost is not zero; an explicit estimate is required.' }
        $number = 0.0
        if ($estimate -is [string]) {
            if (-not [double]::TryParse($estimate, [System.Globalization.NumberStyles]::Float, [System.Globalization.CultureInfo]::InvariantCulture, [ref]$number)) {
                throw 'BF_BLOCKED: dispatch cost estimate must be a non-negative finite number.'
            }
        }
        else { $number = [double]$estimate }
        if ([double]::IsNaN($number) -or [double]::IsInfinity($number) -or $number -lt 0) {
            throw 'BF_BLOCKED: dispatch cost estimate must be a non-negative finite number.'
        }
        $total += $number
    }
    $limit = $null
    try { $limit = $Budget.limit } catch { $limit = $null }
    if ($null -ne $limit) {
        $limitNumber = [double]$limit
        $reservation = 0.0
        try { if ($null -ne $Budget.reservation) { $reservation = [double]$Budget.reservation } } catch { $reservation = 0.0 }
        if ($total + $reservation -gt $limitNumber) { throw 'BF_BLOCKED: council budget admission refused for the full dispatch cycle.' }
    }
    return [ordered]@{ admitted = $true; estimated_total_usd = $total; has_unknown_outcome = $hasUnknown }
}

function New-BSLFlowCouncilPreparedPackage {
    param(
        [Parameter(Mandatory)][string]$RunRoot,
        [Parameter(Mandatory)]$Review,
        [Parameter(Mandatory)][byte[]]$FinalSpecBytes,
        [byte[]]$FinalDesignBytes,
        [byte[]]$ExistingReviewBytes
    )
    . (Join-Path $PSScriptRoot 'Review.Common.ps1')
    . (Join-Path $PSScriptRoot 'Council.Validation.ps1')
    Assert-BSLFlowCouncilReview $Review
    $canonicalReview = $Review | ConvertTo-Json -Depth 20
    $reviewBytes = [System.Text.Encoding]::UTF8.GetBytes($canonicalReview)
    $package = [pscustomobject][ordered]@{
        schema_version = 1
        prepared_at_utc = [DateTime]::UtcNow.ToString('o')
        expected_draft_spec_sha256 = [string]$Review.inputs.spec_sha256
        expected_draft_original_sha256 = [string]$Review.inputs.original_task_sha256
        expected_draft_design_sha256 = $(if ($null -ne $Review.inputs.design_sha256) { [string]$Review.inputs.design_sha256 } else { $null })
        expected_draft_review_file_sha256 = $(if ($null -ne $ExistingReviewBytes) { Get-BSLFlowBytesSha256 $ExistingReviewBytes } else { $null })
        intended_final_spec_sha256 = Get-BSLFlowBytesSha256 $FinalSpecBytes
        intended_final_design_sha256 = if ($null -ne $FinalDesignBytes) { Get-BSLFlowBytesSha256 $FinalDesignBytes } else { $null }
        review_sha256 = Get-BSLFlowCouncilReviewDigest $Review
        review_file_sha256 = Get-BSLFlowBytesSha256 $reviewBytes
    }
    $packageDir = Join-Path $RunRoot 'publication'
    New-Item -ItemType Directory -Path $packageDir -Force | Out-Null
    [System.IO.File]::WriteAllBytes((Join-Path $packageDir 'intended-spec.md'), $FinalSpecBytes)
    if ($null -ne $FinalDesignBytes) { [System.IO.File]::WriteAllBytes((Join-Path $packageDir 'intended-design.md'), $FinalDesignBytes) }
    [System.IO.File]::WriteAllBytes((Join-Path $packageDir 'canonical-review.json'), $reviewBytes)
    Write-BSLFlowJsonAtomic -Value $package -Path (Join-Path $packageDir 'prepared.json')
    # Durable prepared event precedes the first live write.
    Write-BSLFlowJsonAtomic -Value ([ordered]@{ event = 'prepared'; at_utc = $package.prepared_at_utc; package_sha256 = Get-BSLFlowSha256 (Join-Path $packageDir 'prepared.json') }) -Path (Join-Path $packageDir 'prepared.event.json')
    return $package
}

function Resume-BSLFlowCouncilPublication {
    param(
        [Parameter(Mandatory)][string]$ChangeDir,
        [Parameter(Mandatory)][string]$RunRoot,
        [Parameter(Mandatory)]$Review,
        [string]$ProjectPath
    )
    . (Join-Path $PSScriptRoot 'Review.Common.ps1')
    . (Join-Path $PSScriptRoot 'Council.Validation.ps1')
    $packageDir = Join-Path $RunRoot 'publication'
    $packagePath = Join-Path $packageDir 'prepared.json'
    if (-not (Test-Path -LiteralPath $packagePath -PathType Leaf)) { throw 'No prepared publication package to resume.' }
    $preparedEventPath = Join-Path $packageDir 'prepared.event.json'
    if (-not (Test-Path -LiteralPath $preparedEventPath -PathType Leaf)) { throw 'BF_BLOCKED: prepared council publication has no durable prepared event.' }
    $preparedEvent = Get-Content -Raw -LiteralPath $preparedEventPath | ConvertFrom-Json -ErrorAction Stop
    if ([string]$preparedEvent.event -cne 'prepared' -or [string]$preparedEvent.package_sha256 -notmatch '^[a-f0-9]{64}$') {
        throw 'BF_BLOCKED: prepared council publication event is invalid.'
    }
    if ((Get-BSLFlowSha256 $packagePath) -cne [string]$preparedEvent.package_sha256) {
        throw 'BF_BLOCKED: prepared council publication package changed after its durable event.'
    }
    $package = Get-Content -Raw -LiteralPath $packagePath | ConvertFrom-Json -ErrorAction Stop
    foreach ($field in @('expected_draft_spec_sha256', 'expected_draft_original_sha256', 'intended_final_spec_sha256', 'review_sha256', 'review_file_sha256')) {
        if ([string]$package.$field -notmatch '^[a-f0-9]{64}$') { throw "BF_BLOCKED: prepared council publication misses a valid $field." }
    }
    foreach ($field in @('expected_draft_design_sha256', 'intended_final_design_sha256')) {
        if ($null -ne $package.$field -and [string]$package.$field -notmatch '^[a-f0-9]{64}$') { throw "BF_BLOCKED: prepared council publication has an invalid $field." }
    }
    $intendedSpec = [System.IO.File]::ReadAllBytes((Join-Path $packageDir 'intended-spec.md'))
    $intendedDesignPath = Join-Path $packageDir 'intended-design.md'
    $intendedDesign = if (Test-Path -LiteralPath $intendedDesignPath -PathType Leaf) { [System.IO.File]::ReadAllBytes($intendedDesignPath) } else { $null }
    if ((Get-BSLFlowBytesSha256 $intendedSpec) -cne [string]$package.intended_final_spec_sha256) {
        throw 'BF_BLOCKED: prepared intended spec bytes do not match their recorded hash.'
    }
    if ($null -ne $intendedDesign) {
        if ($null -eq $package.intended_final_design_sha256 -or (Get-BSLFlowBytesSha256 $intendedDesign) -cne [string]$package.intended_final_design_sha256) {
            throw 'BF_BLOCKED: prepared intended design bytes do not match their recorded hash.'
        }
    }
    elseif ($null -ne $package.intended_final_design_sha256) {
        throw 'BF_BLOCKED: prepared intended design bytes are missing.'
    }
    $canonicalReviewPath = Join-Path $packageDir 'canonical-review.json'
    if (-not (Test-Path -LiteralPath $canonicalReviewPath -PathType Leaf)) { throw 'BF_BLOCKED: prepared council publication has no canonical review.' }
    $canonicalReview = [System.IO.File]::ReadAllBytes($canonicalReviewPath)
    if ((Get-BSLFlowBytesSha256 $canonicalReview) -cne [string]$package.review_file_sha256) {
        throw 'BF_BLOCKED: prepared canonical review bytes do not match their recorded hash.'
    }
    $canonicalReviewObject = [System.Text.Encoding]::UTF8.GetString($canonicalReview) | ConvertFrom-Json -ErrorAction Stop
    Assert-BSLFlowCouncilReview $canonicalReviewObject
    if ((Get-BSLFlowCouncilReviewDigest $canonicalReviewObject) -cne [string]$package.review_sha256) {
        throw 'BF_BLOCKED: prepared canonical review digest does not match its recorded hash.'
    }
    if ($null -ne $Review -and (Get-BSLFlowCouncilReviewDigest $Review) -cne [string]$package.review_sha256) {
        throw 'BF_BLOCKED: supplied council review does not match the prepared publication.'
    }
    foreach ($pair in @(
        @('expected_draft_spec_sha256', [string]$canonicalReviewObject.inputs.spec_sha256),
        @('expected_draft_original_sha256', [string]$canonicalReviewObject.inputs.original_task_sha256),
        @('expected_draft_design_sha256', $(if ($null -ne $canonicalReviewObject.inputs.design_sha256) { [string]$canonicalReviewObject.inputs.design_sha256 } else { $null }))
    )) {
        if ($null -eq $package.($pair[0]) -and $null -eq $pair[1]) { continue }
        if ([string]$package.($pair[0]) -cne [string]$pair[1]) { throw "BF_BLOCKED: prepared publication input hash mismatch: $($pair[0])." }
    }
    $specPath = Join-Path $ChangeDir 'spec.md'
    $designPath = Join-Path $ChangeDir 'design.md'
    $reviewPath = Join-Path $ChangeDir 'review.json'
    $originalPath = Join-Path $ChangeDir 'original-task.md'
    if (-not (Test-Path -LiteralPath $originalPath -PathType Leaf)) { throw 'BF_BLOCKED: live original-task.md is missing; refusing to publish recovery bytes.' }
    if ((Get-BSLFlowSha256 $originalPath) -cne [string]$package.expected_draft_original_sha256) {
        throw 'BF_BLOCKED: live original-task.md changed after the council publication was prepared.'
    }
    if ($ProjectPath) {
        $policyPath = Join-Path ([System.IO.Path]::GetFullPath($ProjectPath).TrimEnd('\', '/')) 'bsl-flow.yaml'
        if (-not (Test-Path -LiteralPath $policyPath -PathType Leaf)) { throw 'BF_BLOCKED: current council policy is missing; refusing to publish recovery bytes.' }
        . (Join-Path $PSScriptRoot 'Council.Profile.ps1')
        try { $effectivePolicy = Get-BSLFlowCouncilEffectivePolicy -ProjectRoot $ProjectPath }
        catch { throw ('BF_BLOCKED: current council policy could not be resolved: ' + $_.Exception.Message) }
        if ($effectivePolicy.hash -cne [string]$canonicalReviewObject.inputs.policy_hash) {
            throw 'BF_BLOCKED: council policy changed after the publication was prepared.'
        }
    }
    # Every live artifact must match draft or intended final bytes; historical
    # v1 review.json is third content and is never overwritten.
    $liveSpec = [System.IO.File]::ReadAllBytes($specPath)
    $liveSpecHash = Get-BSLFlowBytesSha256 $liveSpec
    if ($liveSpecHash -cne [string]$package.expected_draft_spec_sha256 -and $liveSpecHash -cne [string]$package.intended_final_spec_sha256) {
        throw 'BF_BLOCKED: live spec.md matches neither draft nor intended final bytes; refusing to overwrite.'
    }
    if (Test-Path -LiteralPath $designPath -PathType Leaf) {
        $liveDesignHash = Get-BSLFlowBytesSha256 ([System.IO.File]::ReadAllBytes($designPath))
        $designDraftOk = ($null -ne $package.expected_draft_design_sha256 -and $liveDesignHash -ceq [string]$package.expected_draft_design_sha256)
        $designFinalOk = ($null -ne $package.intended_final_design_sha256 -and $liveDesignHash -ceq [string]$package.intended_final_design_sha256)
        if (-not ($designDraftOk -or $designFinalOk)) {
            throw 'BF_BLOCKED: live design.md matches neither draft nor intended final bytes; refusing to overwrite.'
        }
    }
    elseif ($null -ne $package.expected_draft_design_sha256 -or $null -ne $intendedDesign) {
        throw 'BF_BLOCKED: live design.md state is ambiguous; refusing to create it.'
    }
    if (Test-Path -LiteralPath $reviewPath -PathType Leaf) {
        $liveReviewHash = Get-BSLFlowBytesSha256 ([System.IO.File]::ReadAllBytes($reviewPath))
        $reviewDraftOk = ($null -ne $package.expected_draft_review_file_sha256 -and $liveReviewHash -ceq [string]$package.expected_draft_review_file_sha256)
        $reviewFinalOk = ($liveReviewHash -ceq [string]$package.review_file_sha256)
        if (-not ($reviewDraftOk -or $reviewFinalOk)) {
            throw 'BF_BLOCKED: live review.json matches neither draft nor intended final bytes; refusing to overwrite.'
        }
    }
    elseif ($null -ne $package.expected_draft_review_file_sha256) {
        throw 'BF_BLOCKED: live review.json state is ambiguous; refusing to create it.'
    }
    # Single writer completes the remaining files without model calls.
    [System.IO.File]::WriteAllBytes($specPath, $intendedSpec)
    if ($null -ne $intendedDesign) { [System.IO.File]::WriteAllBytes($designPath, $intendedDesign) }
    [System.IO.File]::WriteAllBytes($reviewPath, $canonicalReview)
    $finalSpecHash = Get-BSLFlowSha256 $specPath
    $finalReviewHash = Get-BSLFlowSha256 $reviewPath
    if ($finalSpecHash -cne [string]$package.intended_final_spec_sha256) { throw 'Publication failed to converge on intended final spec bytes.' }
    if ($finalReviewHash -cne [string]$package.review_file_sha256) { throw 'Publication failed to converge on intended review bytes.' }
    # Completion is recorded only after the deterministic final validation passes.
    . (Join-Path $PSScriptRoot 'Council.Validation.ps1')
    $resumeLint = & (Join-Path $PSScriptRoot 'Test-1CSpec.ps1') -ChangePath $ChangeDir -NoThrow
    $resumeReview = Get-Content -Raw -LiteralPath $reviewPath | ConvertFrom-Json -ErrorAction Stop
    $resumeProject = if ($ProjectPath) {
        [System.IO.Path]::GetFullPath($ProjectPath).TrimEnd('\', '/')
    }
    else {
        Split-Path (Split-Path (Split-Path $ChangeDir -Parent) -Parent) -Parent
    }
    $resumeGate = Test-BSLFlowCouncilFinalGate -Review $resumeReview -OriginalTaskPath (Join-Path $ChangeDir 'original-task.md') -SpecPath $specPath -DesignPath $designPath -Lint $resumeLint -ProjectPath $resumeProject
    if (-not [bool]$resumeGate.passed) { throw ("BF_BLOCKED: final validation failed before completion: " + (@($resumeGate.errors) -join '; ')) }
    # Materialize the normal final-validation artifact during recovery as well.
    # The prepared package is the source of the intended bytes, but completion
    # is not durable until the public final validator has written its receipt.
    $finalValidation = & (Join-Path $PSScriptRoot 'Test-1CSpecFinal.ps1') -ProjectPath $resumeProject -ChangeName (Split-Path $ChangeDir -Leaf)
    if ($null -eq $finalValidation -or -not [bool]$finalValidation.passed) {
        throw 'BF_BLOCKED: final validation did not produce a passing recovery receipt.'
    }
    $finalValidationPath = Join-Path $ChangeDir 'final-validation.json'
    if (-not (Test-Path -LiteralPath $finalValidationPath -PathType Leaf)) {
        throw 'BF_BLOCKED: final validation receipt is missing after recovery.'
    }
    Write-BSLFlowJsonAtomic -Value ([ordered]@{ event = 'completed'; at_utc = [DateTime]::UtcNow.ToString('o'); review_sha256 = $finalReviewHash }) -Path (Join-Path $packageDir 'completed.event.json')
    return [ordered]@{
        resumed = $true; review_sha256 = $finalReviewHash
        final_validation = $finalValidation
        final_validation_sha256 = Get-BSLFlowSha256 $finalValidationPath
    }
}

function Resume-BSLFlowCouncilPreparedPublicationIfPresent {
    <#
      Shared public recovery seam. Callers use this before lint, evidence
      construction, host preflight or any council snapshot so a prepared
      publication can finish without creating another attempt.
      Returns $null when no prepared package exists.
    #>
    param(
        [Parameter(Mandatory)][string]$ProjectRoot,
        [Parameter(Mandatory)][string]$ChangeName
    )
    if ($ChangeName -notmatch '^[A-Za-z0-9][A-Za-z0-9._-]*$') {
        throw "Unsafe OpenSpec change name: $ChangeName"
    }
    $project = [System.IO.Path]::GetFullPath($ProjectRoot).TrimEnd('\', '/')
    $runRoot = Join-Path $project ('.bsl-flow/reports/spec-review/' + $ChangeName + '.council')
    $packagePath = Join-Path $runRoot 'publication/prepared.json'
    if (-not (Test-Path -LiteralPath $packagePath -PathType Leaf)) { return $null }
    $reviewPath = Join-Path $runRoot 'publication/canonical-review.json'
    if (-not (Test-Path -LiteralPath $reviewPath -PathType Leaf)) {
        throw 'BF_BLOCKED: prepared council publication has no canonical review.'
    }
    $review = Get-Content -Raw -LiteralPath $reviewPath | ConvertFrom-Json -ErrorAction Stop
    $changeDir = Join-Path $project "openspec\changes\$ChangeName"
    return (Resume-BSLFlowCouncilPublication -ChangeDir $changeDir -RunRoot $runRoot -Review $review -ProjectPath $project)
}
