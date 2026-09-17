#Requires -Version 7.0
Set-StrictMode -Version Latest

# Stage 1: council schemas, manifest, diversity, canonical order and deterministic final gate.
# No network, no transports. Controller builds all provenance; model payload claims are rejected.

$script:CouncilRoleOrder = @('brainstorm', 'intent_critic', 'architecture_critic', 'executability_critic')
$script:CouncilCostStates = @('unknown', 'provider_usage_reported', 'no_usage_reported')
$script:CouncilForbiddenPayloadFields = @(
    'type',
    'provider', 'model', 'effort', 'status', 'usage', 'cost_state',
    'reviewed_at_utc', 'reconciled_at_utc', 'timestamps',
    'input_hashes', 'payload_sha256', 'execution_mode', 'requested', 'observed',
    'schema_version', 'reviewer', 'inputs', 'gate', 'confidence',
    'weighted_score', 'scores', 'overengineering', 'token', 'Authorization'
)

function Assert-BSLFlowCouncilRfc3339 {
    param($Value, [Parameter(Mandatory)][string]$Name)
    if ($null -eq $Value) { return }
    # JSON round trips may materialize date-time strings as DateTime objects.
    if ($Value -is [DateTime]) {
        if ($Value.Kind -ceq [DateTimeKind]::Unspecified) { throw "$Name must carry an explicit offset." }
        return
    }
    if ($Value -is [DateTimeOffset]) { return }
    if ($Value -isnot [string] -or $Value -cnotmatch '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]+)?(?:Z|[+-][0-9]{2}:[0-9]{2})$') {
        throw "$Name must be an RFC 3339 date-time string with an offset."
    }
}

function Get-BSLFlowCouncilNormalizedReference {
    param([AllowNull()][string]$Text)
    if ($null -eq $Text) { return '' }
    return (($Text -replace '\s+', ' ').Trim())
}

function Test-BSLFlowCouncilReferenceResolves {
    param(
        [AllowNull()][string]$Ref,
        [Parameter(Mandatory)][AllowEmptyString()][string]$FinalSpecText,
        [Parameter(Mandatory)][int]$FinalRequirementCount
    )
    # A reference resolves only against the actual final text: either a numbered
    # requirement anchor within the final requirement manifest, or a verbatim
    # (whitespace-normalized) fragment of the final specification.
    if ([string]::IsNullOrWhiteSpace($Ref)) { return $false }
    $numberMatch = [regex]::Match($Ref, '^\s*(?:Требуемое поведение|Required behavior)\s*/\s*([0-9]{1,4})(?:\.([0-9]{1,4}))?\s*$')
    if ($numberMatch.Success) {
        $number = [int]$numberMatch.Groups[1].Value
        if ($number -lt 1 -or $number -gt $FinalRequirementCount) { return $false }
        if (-not $numberMatch.Groups[2].Success) { return $true }
        # A subsection anchor (N.M) must exist as a numbered marker inside the
        # final required-behavior section, not merely as free prose.
        $sectionMatch = [regex]::Match($FinalSpecText, '(?ims)^##\s+(Требуемое поведение|Required behavior)\s*$\s*(?<body>.*?)(?=^##\s|\z)')
        if (-not $sectionMatch.Success) { return $false }
        $subMarker = ('(?m)^\s*' + [regex]::Escape("$($numberMatch.Groups[1].Value).$($numberMatch.Groups[2].Value)") + '[\s.]')
        return [regex]::IsMatch($sectionMatch.Groups['body'].Value, $subMarker)
    }
    $final = Get-BSLFlowCouncilNormalizedReference $FinalSpecText
    if (-not $final) { return $false }
    $needle = Get-BSLFlowCouncilNormalizedReference $Ref
    if (-not $needle) { return $false }
    return $final.Contains($needle)
}

function Assert-BSLFlowCouncilNoProvenanceClaims {
    param([Parameter(Mandatory)]$Payload, [Parameter(Mandatory)][string]$Name)
    if ($null -eq $Payload -or $Payload -is [string]) { throw "$Name must be an object." }
    $names = @($Payload.PSObject.Properties.Name)
    foreach ($field in $script:CouncilForbiddenPayloadFields) {
        if ($field -cin $names) { throw "Model payload must not contain ${Name}.${field}; provenance is built by the controller." }
    }
}

function Assert-BSLFlowCouncilFinding {
    param([Parameter(Mandatory)]$Finding, [Parameter(Mandatory)][string]$Name)
    . (Join-Path $PSScriptRoot 'Review.Common.ps1')
    Assert-BSLFlowObjectProperties $Finding $Name @('id', 'severity', 'category', 'spec_ref', 'issue', 'evidence', 'suggested_direction')
    if ($Finding.id -cnotmatch '^F-[0-9]{3,}$') { throw "Invalid finding id in ${Name}: $($Finding.id)" }
    if ($Finding.severity -notin @('blocker', 'high', 'medium', 'low')) { throw "Invalid severity in ${Name}: $($Finding.id)" }
    if ($Finding.category -notin @(Get-BSLFlowAllowedFindingCategories)) { throw "Invalid category in ${Name}: $($Finding.id)" }
    foreach ($field in @('spec_ref', 'issue', 'evidence', 'suggested_direction')) {
        Assert-BSLFlowText $Finding.$field "${Name}.$($Finding.id).$field"
    }
}

function Assert-BSLFlowCouncilModelPayload {
    param([Parameter(Mandatory)]$Payload)
    Assert-BSLFlowCouncilNoProvenanceClaims $Payload 'member'
    # needs_input_questions is optional in the wire contract: models may omit it
    # when the verdict is not needs_input.
    $allowed = @('role', 'verdict', 'findings', 'do_not_change')
    if (Test-BSLFlowEnvelopeField2 $Payload 'needs_input_questions') { $allowed += 'needs_input_questions' }
    Assert-BSLFlowObjectProperties $Payload 'member' $allowed
    if ($Payload.role -notin @('intent_critic', 'architecture_critic', 'executability_critic')) { throw "Invalid critic role: $($Payload.role)" }
    if ($Payload.verdict -notin @('PASS', 'REVISE', 'BLOCK', 'needs_input')) { throw 'Invalid member verdict.' }
    Assert-BSLFlowArray $Payload.findings 'member.findings'
    Assert-BSLFlowArray $Payload.do_not_change 'member.do_not_change'
    $findings = @($Payload.findings)
    $index = 0
    $seen = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::Ordinal)
    foreach ($finding in $findings) {
        $index++
        Assert-BSLFlowCouncilFinding $finding "member.findings"
        $expected = ('F-{0:D3}' -f $index)
        if ($finding.id -cne $expected) { throw "Member finding IDs must be sequential per role: expected $expected." }
        if (-not $seen.Add([string]$finding.id)) { throw "Duplicate member finding id: $($finding.id)" }
    }
    $items = @($Payload.do_not_change)
    foreach ($item in $items) { Assert-BSLFlowText $item 'member.do_not_change[]' }
    if (@($items | Select-Object -Unique).Count -ne $items.Count) { throw 'member.do_not_change items must be unique.' }
    $questions = @()
    if (Test-BSLFlowEnvelopeField2 $Payload 'needs_input_questions') { $questions = @($Payload.needs_input_questions) }
    if ($Payload.verdict -eq 'needs_input' -and $questions.Count -eq 0) { throw 'needs_input verdict requires at least one question.' }
    foreach ($question in $questions) { Assert-BSLFlowText $question 'member.needs_input_questions[]' }
    if ($Payload.verdict -ne 'PASS' -and $findings.Count -eq 0 -and $Payload.verdict -ne 'needs_input') {
        throw 'A non-PASS member payload must contain at least one finding.'
    }
}

function Test-BSLFlowEnvelopeField2 {
    param($Object, [Parameter(Mandatory)][string]$Name)
    if ($null -eq $Object) { return $false }
    if ($Object -is [System.Collections.IDictionary]) { return $Object.Contains($Name) }
    return ($null -ne $Object.PSObject.Properties[$Name])
}

function Assert-BSLFlowCouncilChairResult {
    # Structural contract for the raw chair model payload before assembly.
    param([Parameter(Mandatory)]$Payload)
    Assert-BSLFlowCouncilNoProvenanceClaims $Payload 'chair'
    foreach ($field in @('verdict', 'decisions', 'protected_decisions', 'requirement_refs', 'final_spec_text')) {
        if (-not (Test-BSLFlowEnvelopeField2 $Payload $field)) { throw "chair payload misses field: $field." }
    }
    if ($Payload.verdict -notin @('PASS', 'REVISE', 'BLOCK', 'needs_input')) { throw 'Invalid chair verdict.' }
    Assert-BSLFlowText $Payload.final_spec_text 'chair.final_spec_text'
    if ((Test-BSLFlowEnvelopeField2 $Payload 'final_design_text') -and ($null -ne $Payload.final_design_text)) { Assert-BSLFlowText $Payload.final_design_text 'chair.final_design_text' }
    foreach ($name in @('decisions', 'protected_decisions', 'requirement_refs')) { Assert-BSLFlowArray $Payload.$name "chair.$name" }
}

function Assert-BSLFlowBrainstormPayload {
    param([Parameter(Mandatory)]$Payload)
    Assert-BSLFlowCouncilNoProvenanceClaims $Payload 'brainstorm'
    # Every brainstorm list is optional on the wire; at least one entry in
    # total is still required so the role cannot return an empty answer.
    $brainstormAllowed = @('role')
    foreach ($name in @('alternatives', 'risks', 'unknowns', 'questions')) {
        if (Test-BSLFlowEnvelopeField2 $Payload $name) { $brainstormAllowed += $name }
    }
    Assert-BSLFlowObjectProperties $Payload 'brainstorm' $brainstormAllowed
    if ($Payload.role -cne 'brainstorm') { throw 'Invalid brainstorm role.' }
    $total = 0
    foreach ($name in @('alternatives', 'risks', 'unknowns', 'questions')) {
        if (-not (Test-BSLFlowEnvelopeField2 $Payload $name)) {
            $Payload | Add-Member -NotePropertyName $name -NotePropertyValue @() -Force
        }
        Assert-BSLFlowArray $Payload.$name "brainstorm.$name"
        foreach ($item in @($Payload.$name)) { Assert-BSLFlowText $item "brainstorm.${name}[]" }
        $total += @($Payload.$name).Count
    }
    if ($total -eq 0) { throw 'Brainstorm payload must contain at least one alternative, risk, unknown or question.' }
}

function Get-BSLFlowRequirementSection {
    param([Parameter(Mandatory)][string]$SpecText)
    $match = [regex]::Match($SpecText, '(?ims)^##\s+(Требуемое поведение|Required behavior)\s*$\s*(?<body>.*?)(?=^##\s|\z)')
    if (-not $match.Success) { throw 'Required behavior section not found.' }
    return $match.Groups['body'].Value
}

function New-BSLFlowRequirementManifest {
    param([Parameter(Mandatory)][string]$SpecText)
    . (Join-Path $PSScriptRoot 'Review.Common.ps1')
    $body = Get-BSLFlowRequirementSection $SpecText
    $items = [System.Collections.Generic.List[string]]::new()
    foreach ($line in ($body -split "`r?`n")) {
        $match = [regex]::Match($line, '^\s*(?:\d+\.\s+|[-*]\s+)(?<item>\S.*\S|\S)\s*$')
        if ($match.Success) {
            $text = $match.Groups['item'].Value.Trim()
            if ($text -and $text -notmatch '^\s*<!--') { $items.Add($text) }
        }
    }
    if ($items.Count -eq 0) { throw 'Requirement manifest needs at least one numbered or bulleted behavior item.' }
    $requirements = @()
    $utf8 = [System.Text.UTF8Encoding]::new($false)
    $sha = [System.Security.Cryptography.SHA256]::Create()
    try {
        for ($i = 0; $i -lt $items.Count; $i++) {
            $bytes = $utf8.GetBytes($items[$i])
            $hash = ([BitConverter]::ToString($sha.ComputeHash($bytes))).Replace('-', '').ToLowerInvariant()
            $requirements += [pscustomobject][ordered]@{
                id = ('REQ-{0:D3}' -f ($i + 1))
                source_hash = $hash
                draft_ref = ("Требуемое поведение / {0}" -f ($i + 1))
            }
        }
    }
    finally { $sha.Dispose() }
    return [pscustomobject][ordered]@{ version = 1; requirements = @($requirements) }
}

function Assert-BSLFlowRequirementManifest {
    param([Parameter(Mandatory)]$Manifest)
    Assert-BSLFlowObjectProperties $Manifest 'manifest' @('version', 'requirements')
    if ((Get-BSLFlowJsonNumber $Manifest.version 'manifest.version' -Integer) -ne 1) { throw 'manifest.version must be 1.' }
    Assert-BSLFlowArray $Manifest.requirements 'manifest.requirements'
    $requirements = @($Manifest.requirements)
    if ($requirements.Count -eq 0) { throw 'manifest.requirements must not be empty.' }
    $index = 0
    $seen = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::Ordinal)
    foreach ($requirement in $requirements) {
        $index++
        Assert-BSLFlowObjectProperties $requirement 'manifest requirement' @('id', 'source_hash', 'draft_ref')
        $expected = ('REQ-{0:D3}' -f $index)
        if ($requirement.id -cne $expected) { throw "Manifest requirement IDs must be sequential: expected $expected." }
        if (-not $seen.Add([string]$requirement.id)) { throw "Duplicate manifest requirement id: $($requirement.id)" }
        if ($requirement.source_hash -isnot [string] -or $requirement.source_hash -cnotmatch '^[a-f0-9]{64}$') { throw "Invalid source_hash for $($requirement.id)." }
        Assert-BSLFlowText $requirement.draft_ref "manifest.$($requirement.id).draft_ref"
    }
}

function Get-BSLFlowCouncilDiversity {
    param([Parameter(Mandatory)]$Members)
    $list = @($Members)
    $fallbackVisible = $false
    foreach ($member in $list) {
        if ($member.execution_mode -ceq 'current_agent_fallback') { $fallbackVisible = $true }
        if ($null -ne $member.fallback_reason -and ([string]$member.fallback_reason).Trim()) { $fallbackVisible = $true }
    }
    $failed = @($list | Where-Object { $_.status -cne 'completed' })
    if ($failed.Count -gt 0) {
        return [ordered]@{ diversity = 'degraded'; fallback_visible = $fallbackVisible }
    }
    $completed = @($list | Where-Object { $_.status -ceq 'completed' })
    $observed = @()
    $hasUnknown = $false
    foreach ($member in $completed) {
        $model = $null
        try { $model = $member.observed.model } catch { $model = $null }
        if ([string]::IsNullOrWhiteSpace([string]$model)) { $hasUnknown = $true }
        else { $observed += [string]$model }
    }
    if ($hasUnknown -or $completed.Count -eq 0) {
        return [ordered]@{ diversity = 'unknown'; fallback_visible = $fallbackVisible }
    }
    $distinct = @($observed | Select-Object -Unique)
    if ($distinct.Count -ge 2) { return [ordered]@{ diversity = 'multi_model'; fallback_visible = $fallbackVisible } }
    return [ordered]@{ diversity = 'multi_role_single_model'; fallback_visible = $fallbackVisible }
}

function Get-BSLFlowCanonicalFindings {
    param([Parameter(Mandatory)]$Findings)
    $order = @{ brainstorm = 0; intent_critic = 1; architecture_critic = 2; executability_critic = 3 }
    $list = @($Findings)
    $seen = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::Ordinal)
    foreach ($finding in $list) {
        if ($finding.role -cnotin @($script:CouncilRoleOrder)) { throw "Unknown finding role: $($finding.role)" }
        $expected = ("{0}:{1}" -f $finding.role, $finding.id)
        if ($finding.composite_id -cne $expected) { throw "Finding composite_id must equal role:id: $expected." }
        if (-not $seen.Add([string]$finding.composite_id)) { throw "Duplicate composite finding: $($finding.composite_id)" }
    }
    return @($list | Sort-Object -Property @{ Expression = { $order[$_.role] } }, @{ Expression = { [string]$_.id } }, @{ Expression = { [string]$_.composite_id } })
}

function Get-BSLFlowCouncilReviewDigest {
    param([Parameter(Mandatory)]$Review)
    # Tamper-evident binding for the self-contained v2 artifact: the digest covers
    # the canonical review bytes with the digest field itself zeroed, so the
    # builder and the gate compute the identical value without regress.
    $clone = $Review | ConvertTo-Json -Depth 20 | ConvertFrom-Json -ErrorAction Stop
    $clone.reconciliation.review_sha256 = ('0' * 64)
    . (Join-Path $PSScriptRoot 'Review.Common.ps1')
    return (Get-BSLFlowBytesSha256 ([System.Text.Encoding]::UTF8.GetBytes(($clone | ConvertTo-Json -Depth 20))))
}

function Assert-BSLFlowCouncilReview {
    param([Parameter(Mandatory)]$Review)
    . (Join-Path $PSScriptRoot 'Review.Common.ps1')
    # Get-BSLFlowEnvelopeValue2 lives in the engine file; standalone consumers
    # (Test-1CSpecFinal) load only this module, so source it here like every
    # other cross-file dependency in the council scripts.
    . (Join-Path $PSScriptRoot 'Council.Engine.ps1')
    Assert-BSLFlowObjectProperties $Review 'review' @(
        'schema_version', 'reviewed_at_utc', 'council_schema_version', 'verdict',
        'diversity', 'fallback_visible', 'inputs', 'manifest', 'members',
        'findings', 'protected', 'questions', 'chair', 'reconciliation', 'gate'
    )
    if ((Get-BSLFlowJsonNumber $Review.schema_version 'review.schema_version' -Integer) -ne 2) { throw 'review.schema_version must be 2.' }
    if ((Get-BSLFlowJsonNumber $Review.council_schema_version 'review.council_schema_version' -Integer) -ne 1) { throw 'review.council_schema_version must be 1.' }
    if ($Review.verdict -notin @('PASS', 'REVISE', 'BLOCK', 'needs_input')) { throw 'Invalid council verdict.' }
    if ($Review.diversity -notin @('multi_model', 'multi_role_single_model', 'degraded', 'unknown')) { throw 'Invalid diversity status.' }
    if ($Review.fallback_visible -isnot [bool]) { throw 'review.fallback_visible must be boolean.' }
    Assert-BSLFlowObjectProperties $Review.inputs 'inputs' @('original_task_sha256', 'spec_sha256', 'design_sha256', 'policy_hash')
    foreach ($name in @('original_task_sha256', 'spec_sha256', 'policy_hash')) {
        if ($Review.inputs.$name -isnot [string] -or $Review.inputs.$name -cnotmatch '^[a-f0-9]{64}$') { throw "Invalid inputs hash: $name" }
    }
    if ($null -ne $Review.inputs.design_sha256 -and ($Review.inputs.design_sha256 -isnot [string] -or $Review.inputs.design_sha256 -cnotmatch '^[a-f0-9]{64}$')) { throw 'Invalid inputs.design_sha256.' }
    Assert-BSLFlowRequirementManifest $Review.manifest
    Assert-BSLFlowArray $Review.members 'members'
    $members = @($Review.members)
    if ($members.Count -eq 0) { throw 'Council review must contain at least one member.' }
    $roles = @($members | ForEach-Object { [string]$_.role })
    if (@($roles | Select-Object -Unique).Count -ne $roles.Count) { throw 'Member roles must be unique.' }
    if ('chair' -cnotin $roles) { throw 'Council review must contain the chair member record.' }
    foreach ($member in $members) {
        Assert-BSLFlowObjectProperties $member 'member' @(
            'schema_version', 'role', 'attempt_id', 'status', 'summary', 'requested', 'observed', 'execution_mode',
            'fallback_reason', 'input_hashes', 'payload_sha256', 'usage', 'cost_state',
            'dispatched_at_utc', 'completed_at_utc'
        )
        if ($member.role -cnotin @('brainstorm', 'intent_critic', 'architecture_critic', 'executability_critic', 'chair')) { throw "Unknown member role: $($member.role)" }
        if ([int]$member.schema_version -ne 1) { throw "Member envelope schema_version must be 1: $($member.role)" }
        if ([string]::IsNullOrWhiteSpace([string]$member.attempt_id)) { throw "Member attempt_id is required: $($member.role)" }
        Assert-BSLFlowText $member.summary "member.summary ($($member.role))"
        if ($member.status -cnotin @('completed', 'failed_before_acceptance', 'unknown_after_dispatch', 'cancelled', 'invalid_response')) { throw "Invalid member status: $($member.role)" }
        Assert-BSLFlowObjectProperties $member.requested 'member.requested' @('provider', 'model', 'effort')
        Assert-BSLFlowObjectProperties $member.observed 'member.observed' @('provider', 'model', 'effort')
        foreach ($field in @('provider', 'model', 'effort')) {
            if ([string]::IsNullOrWhiteSpace([string]$member.requested.$field)) { throw "Member requested.$field is required: $($member.role)" }
        }
        if ($member.execution_mode -cnotin @('direct_api', 'current_agent_fallback')) { throw "Invalid execution_mode: $($member.role)" }
        # usage carries provider-reported token counts only; it never contains cost estimates.
        if ($null -ne $member.usage) {
            Assert-BSLFlowObjectProperties $member.usage 'member.usage' @('input_tokens', 'output_tokens', 'reasoning_tokens')
        }
        if ($null -ne $member.cost_state -and [string]$member.cost_state -cnotin $script:CouncilCostStates) { throw "Invalid member cost_state: $($member.role)" }
        if ($member.status -ceq 'completed' -and $null -eq $member.cost_state) { throw "Completed member needs cost_state: $($member.role)" }
        Assert-BSLFlowCouncilRfc3339 $member.dispatched_at_utc "member.dispatched_at_utc ($($member.role))"
        Assert-BSLFlowCouncilRfc3339 $member.completed_at_utc "member.completed_at_utc ($($member.role))"
        # member_aggregate_sha256 is chair-only provenance binding the fan-in aggregate.
        $allowedHashFields = @('original_task_sha256', 'spec_sha256', 'design_sha256', 'evidence_sha256', 'policy_hash', 'rubric_sha256')
        if ($member.role -ceq 'chair') { $allowedHashFields += 'member_aggregate_sha256' }
        Assert-BSLFlowObjectProperties $member.input_hashes 'member.input_hashes' $allowedHashFields
        if ($member.role -ceq 'chair' -and $null -eq (Get-BSLFlowEnvelopeValue2 $member.input_hashes 'member_aggregate_sha256')) { throw 'Chair member envelope needs member_aggregate_sha256.' }
        if ($member.input_hashes.original_task_sha256 -cnotmatch '^[a-f0-9]{64}$') { throw "Invalid member input hash: $($member.role)" }
        foreach ($hashField in @('spec_sha256', 'design_sha256', 'evidence_sha256', 'policy_hash', 'rubric_sha256', 'member_aggregate_sha256')) {
            $hashValue = Get-BSLFlowEnvelopeValue2 $member.input_hashes $hashField
            if ($null -ne $hashValue -and ([string]$hashValue) -cnotmatch '^[a-f0-9]{64}$') { throw "Invalid member $hashField : $($member.role)" }
        }
        if ($null -ne $member.payload_sha256 -and $member.payload_sha256 -cnotmatch '^[a-f0-9]{64}$') { throw "Invalid member payload hash: $($member.role)" }
        if ($member.status -ceq 'completed' -and $null -eq $member.payload_sha256) { throw "Completed member needs payload_sha256: $($member.role)" }
        # Stale-draft votes aggregate silently unless bound to the frozen inputs.
        if ([string]$member.input_hashes.original_task_sha256 -cne [string]$Review.inputs.original_task_sha256) {
            throw "Member voted on another original-task snapshot: $($member.role)"
        }
        if ($null -ne $Review.inputs.spec_sha256 -and [string]$member.input_hashes.spec_sha256 -cne [string]$Review.inputs.spec_sha256) {
            throw "Member voted on another spec draft: $($member.role)"
        }
        if ($null -ne $Review.inputs.design_sha256 -or $null -ne (Get-BSLFlowEnvelopeValue2 $member.input_hashes 'design_sha256')) {
            $liveDesign = Get-BSLFlowEnvelopeValue2 $member.input_hashes 'design_sha256'
            if ([string]$liveDesign -cne [string]$Review.inputs.design_sha256) {
                throw "Member voted on another design draft: $($member.role)"
            }
        }
        if ([string](Get-BSLFlowEnvelopeValue2 $member.input_hashes 'policy_hash') -cne [string]$Review.inputs.policy_hash) {
            throw "Member voted under another council policy: $($member.role)"
        }
    }
    $canonical = @(Get-BSLFlowCanonicalFindings $Review.findings)
    $actualIds = @($Review.findings | ForEach-Object { [string]$_.composite_id })
    $canonicalIds = @($canonical | ForEach-Object { [string]$_.composite_id })
    if (($actualIds -join '|') -cne ($canonicalIds -join '|')) { throw 'Review findings must be stored in canonical role/id order.' }
    foreach ($finding in @($Review.findings)) {
        Assert-BSLFlowObjectProperties $finding 'finding' @('composite_id', 'role', 'id', 'severity', 'category', 'spec_ref', 'issue', 'evidence', 'suggested_direction')
        if ($finding.id -cnotmatch '^F-[0-9]{3,}$') { throw "Invalid aggregate finding id: $($finding.composite_id)" }
    }
    Assert-BSLFlowArray $Review.protected 'protected'
    $protectedIds = @($Review.protected | ForEach-Object { [string]$_.composite_id })
    if (@($protectedIds | Select-Object -Unique).Count -ne $protectedIds.Count) { throw 'Protected composite IDs must be unique.' }
    foreach ($item in @($Review.protected)) {
        Assert-BSLFlowObjectProperties $item 'protected' @('composite_id', 'role', 'item')
        Assert-BSLFlowText $item.item "protected.$($item.composite_id)"
    }
    # Material member questions reach the chair through this sanitized list; the
    # deterministic gate uses it to block a PASS over unanswered input requests.
    Assert-BSLFlowArray $Review.questions 'questions'
    $questionRoles = @()
    foreach ($question in @($Review.questions)) {
        Assert-BSLFlowObjectProperties $question 'question' @('role', 'text')
        if ([string]$question.role -cnotin @($script:CouncilRoleOrder)) { throw "Unknown question role: $($question.role)" }
        Assert-BSLFlowText $question.text "question.$($question.role)"
        $questionRoles += [string]$question.role
    }
    Assert-BSLFlowObjectProperties $Review.chair 'chair' @('verdict', 'decisions', 'protected_decisions', 'requirement_refs', 'final_spec_text', 'final_design_text')
    if ($Review.chair.verdict -notin @('PASS', 'REVISE', 'BLOCK', 'needs_input')) { throw 'Invalid chair verdict.' }
    # The chair contract requires the complete revised final specification text;
    # an absent or whitespace-only text would silently republish the draft.
    Assert-BSLFlowText $Review.chair.final_spec_text 'chair.final_spec_text'
    if ((Test-BSLFlowEnvelopeField2 $Review.chair 'final_design_text') -and ($null -ne $Review.chair.final_design_text)) { Assert-BSLFlowText $Review.chair.final_design_text 'chair.final_design_text' }
    if ($Review.chair.verdict -notin @('PASS', 'REVISE', 'BLOCK', 'needs_input')) { throw 'Invalid chair verdict.' }
    $decisions = @($Review.chair.decisions)
    $decisionIds = @($decisions | ForEach-Object { [string]$_.composite_id })
    if (@($decisionIds | Select-Object -Unique).Count -ne $decisionIds.Count) { throw 'Chair decisions must be unique.' }
    foreach ($id in $actualIds) {
        if ($id -cnotin $decisionIds) { throw "Chair must decide every finding exactly once: $id" }
    }
    foreach ($id in $decisionIds) {
        if ($id -cnotin $actualIds) { throw "Unknown chair decision: $id" }
    }
    foreach ($decision in $decisions) {
        # Scope fields are required only for partially_accepted decisions; the
        # wire contract keeps them optional for plain accept/reject.
        $decisionAllowed = @('composite_id', 'decision', 'reason', 'evidence', 'resolution', 'resolution_refs')
        if ($decision.decision -ceq 'partially_accepted') { $decisionAllowed += @('accepted_scope', 'rejected_scope') }
        if (Test-BSLFlowEnvelopeField2 $decision 'accepted_scope') { $decisionAllowed = @($decisionAllowed | Where-Object { $_ -cne 'accepted_scope' }) + @('accepted_scope') }
        Assert-BSLFlowObjectProperties $decision 'chair decision' $decisionAllowed
        if ($decision.decision -cnotin @('accepted', 'rejected', 'partially_accepted')) { throw "Invalid chair decision: $($decision.composite_id)" }
        foreach ($field in @('reason', 'evidence', 'resolution')) { Assert-BSLFlowText $decision.$field "chair.$($decision.composite_id).$field" }
        if ($decision.decision -ceq 'partially_accepted') {
            Assert-BSLFlowText $decision.accepted_scope "chair.$($decision.composite_id).accepted_scope"
            Assert-BSLFlowText $decision.rejected_scope "chair.$($decision.composite_id).rejected_scope"
            if (@($decision.resolution_refs).Count -eq 0) { throw "Partially accepted finding needs resolution_refs: $($decision.composite_id)" }
        }
        if ($decision.decision -ceq 'accepted' -and @($decision.resolution_refs).Count -eq 0) { throw "Accepted finding needs resolution_refs: $($decision.composite_id)" }
        foreach ($ref in @($decision.resolution_refs)) { Assert-BSLFlowText $ref "chair.$($decision.composite_id).resolution_refs[]" }
    }
    $protectedDecisions = @($Review.chair.protected_decisions)
    $protectedDecisionIds = @($protectedDecisions | ForEach-Object { [string]$_.composite_id })
    if (@($protectedDecisionIds | Select-Object -Unique).Count -ne $protectedDecisionIds.Count) { throw 'Protected decisions must be unique.' }
    foreach ($id in $protectedIds) {
        if ($id -cnotin $protectedDecisionIds) { throw "Chair must decide every protected item exactly once: $id" }
    }
    foreach ($id in $protectedDecisionIds) {
        if ($id -cnotin $protectedIds) { throw "Unknown protected decision: $id" }
    }
    foreach ($check in $protectedDecisions) {
        Assert-BSLFlowObjectProperties $check 'protected decision' @('composite_id', 'decision', 'reason', 'evidence')
        if ($check.decision -cnotin @('preserved', 'rejected')) { throw "Invalid protected decision: $($check.composite_id)" }
        Assert-BSLFlowText $check.reason "protected.$($check.composite_id).reason"
        Assert-BSLFlowText $check.evidence "protected.$($check.composite_id).evidence"
    }
    $refs = @($Review.chair.requirement_refs)
    $refIds = @($refs | ForEach-Object { [string]$_.id })
    $manifestIds = @($Review.manifest.requirements | ForEach-Object { [string]$_.id })
    if (@($refIds | Select-Object -Unique).Count -ne $refIds.Count) { throw 'Requirement refs must be unique.' }
    foreach ($id in $manifestIds) {
        if ($id -cnotin $refIds) { throw "Chair must return final refs for every requirement: $id" }
    }
    foreach ($id in $refIds) {
        if ($id -cnotin $manifestIds) { throw "Unknown requirement ref: $id" }
    }
    foreach ($ref in $refs) {
        Assert-BSLFlowObjectProperties $ref 'requirement ref' @('id', 'final_refs')
        if (@($ref.final_refs).Count -eq 0) { throw "Requirement needs at least one final ref: $($ref.id)" }
        foreach ($final in @($ref.final_refs)) { Assert-BSLFlowText $final "requirement.$($ref.id).final_refs[]" }
    }
    Assert-BSLFlowObjectProperties $Review.reconciliation 'reconciliation' @('review_sha256', 'draft_spec_sha256', 'final_spec_sha256', 'draft_design_sha256', 'final_design_sha256')
    Assert-BSLFlowObjectProperties $Review.gate 'gate' @('structural_only', 'passed')
    if ($Review.gate.structural_only -ne $true) { throw 'Council gate is structural-only.' }
    if ($Review.gate.passed -isnot [bool]) { throw 'gate.passed must be boolean.' }
    # Final references must point into the actual final specification text, not
    # be merely non-empty strings. The draft text is not sufficient evidence.
    $finalSpecForRefs = [string]$Review.chair.final_spec_text
    try { $finalRequirementCount = (@(New-BSLFlowRequirementManifest $finalSpecForRefs).requirements).Count }
    catch { throw 'chair.final_spec_text does not contain a valid Требуемое поведение requirement section.' }
    if ($finalRequirementCount -lt @($Review.manifest.requirements).Count) {
        throw 'chair.final_spec_text covers fewer material requirements than the reviewed draft manifest.'
    }
    foreach ($ref in $refs) {
        foreach ($final in @($ref.final_refs)) {
            if (-not (Test-BSLFlowCouncilReferenceResolves -Ref ([string]$final) -FinalSpecText $finalSpecForRefs -FinalRequirementCount $finalRequirementCount)) {
                throw "Requirement final ref does not resolve in the final specification: $($ref.id) -> $final"
            }
        }
    }
    foreach ($decision in $decisions) {
        if ($decision.decision -cnotin @('rejected')) {
            foreach ($resolutionRef in @($decision.resolution_refs)) {
                if (-not (Test-BSLFlowCouncilReferenceResolves -Ref ([string]$resolutionRef) -FinalSpecText $finalSpecForRefs -FinalRequirementCount $finalRequirementCount)) {
                    throw "Chair resolution ref does not resolve in the final specification: $($decision.composite_id) -> $resolutionRef"
                }
            }
        }
    }
    # Static secret guard for the sanitized artifact. Only provenance fields are
    # guarded; the embedded final specification text may legitimately discuss
    # security requirements, so the check runs against the metadata shape.
    $metadataClone = [pscustomobject][ordered]@{
        members = $Review.members; reconciliation = $Review.reconciliation; gate = $Review.gate
        inputs = $Review.inputs; manifest = $Review.manifest
    }
    $serialized = $metadataClone | ConvertTo-Json -Depth 20
    if ($serialized -match '(?i)"token"\s*:') { throw 'Sanitized council review must not contain a token field.' }
    if ($serialized -match '(?i)Authorization') { throw 'Sanitized council review must not contain Authorization material.' }
    # Chair verdict consistency: model semantics stay with critics/chair, gate only downgrades later.
    if ($Review.verdict -ceq 'PASS' -and $Review.chair.verdict -cne 'PASS') { throw 'Review PASS cannot exceed the chair verdict.' }
}

function Test-BSLFlowCouncilFinalGate {
    param(
        [Parameter(Mandatory)]$Review,
        [Parameter(Mandatory)][string]$OriginalTaskPath,
        [Parameter(Mandatory)][string]$SpecPath,
        [string]$DesignPath,
        [Parameter(Mandatory)]$Lint,
        [string]$ProjectPath
    )
    . (Join-Path $PSScriptRoot 'Review.Common.ps1')
    $errors = [System.Collections.Generic.List[string]]::new()
    try { Assert-BSLFlowCouncilReview $Review }
    catch { $errors.Add("Invalid council review: $($_.Exception.Message)") }
    if (-not [bool]$Lint.passed) { $errors.Add('Final specification lint failed.') }
    try {
        $digest = Get-BSLFlowCouncilReviewDigest $Review
        if ($digest -cne [string]$Review.reconciliation.review_sha256) { $errors.Add('reconciliation.review_sha256 does not match the review digest.') }
    }
    catch { $errors.Add("Could not verify review digest: $($_.Exception.Message)") }
    try {
        $liveOriginal = Get-BSLFlowSha256 $OriginalTaskPath
        $liveSpec = Get-BSLFlowSha256 $SpecPath
        $liveDesign = if ($DesignPath -and (Test-Path -LiteralPath $DesignPath -PathType Leaf)) { Get-BSLFlowSha256 $DesignPath } else { $null }
        if ($liveOriginal -cne $Review.inputs.original_task_sha256) { $errors.Add('original-task.md changed after review.') }
        if ($liveSpec -cne $Review.reconciliation.final_spec_sha256) { $errors.Add('reconciliation.final_spec_sha256 does not match current spec.md.') }
        if ($Review.reconciliation.draft_spec_sha256 -cne $Review.inputs.spec_sha256) { $errors.Add('reconciliation.draft_spec_sha256 does not match the reviewed draft.') }
        if ($liveDesign -cne $Review.reconciliation.final_design_sha256) { $errors.Add('Design hash mismatch after review.') }
    }
    catch { $errors.Add("Could not verify live hashes: $($_.Exception.Message)") }
    if ($ProjectPath -and (Test-Path -LiteralPath (Join-Path $ProjectPath 'bsl-flow.yaml') -PathType Leaf)) {
        try {
            $liveConfig = Get-Content -Raw -LiteralPath (Join-Path $ProjectPath 'bsl-flow.yaml')
            $livePolicyHash = Get-BSLFlowBytesSha256 ([System.Text.Encoding]::UTF8.GetBytes($liveConfig))
            if ($livePolicyHash -cne [string]$Review.inputs.policy_hash) { $errors.Add('Council policy changed after review.') }
        }
        catch { $errors.Add("Could not verify policy hash: $($_.Exception.Message)") }
    }
    # Required-role terminal check is policy-driven; structurally every enabled member
    # recorded with a non-completed status already forces diversity degraded and blocks PASS.
    if ($Review.diversity -ceq 'degraded' -and $Review.verdict -ceq 'PASS') { $errors.Add('Degraded council cannot PASS.') }
    if ($Review.diversity -ceq 'unknown' -and $Review.verdict -ceq 'PASS') { $errors.Add('Unknown diversity cannot PASS as multi-model evidence.') }
    $requiredFailed = @($Review.members | Where-Object { $_.status -cne 'completed' })
    if ($requiredFailed.Count -gt 0 -and $Review.verdict -ceq 'PASS') { $errors.Add('Council with a terminal member failure cannot PASS.') }
    if ($Review.chair.verdict -cne 'PASS' -and $Review.verdict -ceq 'PASS') { $errors.Add('Deterministic gate cannot upgrade the chair verdict.') }
    if ($Review.chair.verdict -ceq 'needs_input' -and $Review.verdict -cne 'needs_input') { $errors.Add('Chair needs_input must propagate.') }
    # Material member questions asked the chair for trusted input; PASS would
    # silently claim they were resolved without evidence.
    if (@($Review.questions).Count -gt 0 -and $Review.verdict -cne 'needs_input') { $errors.Add('Unresolved member questions require a needs_input verdict.') }
    # The published final bytes must be exactly the chair's revised text.
    try {
        $liveSpecText = [System.IO.File]::ReadAllText($SpecPath, [System.Text.UTF8Encoding]::new($false, $true))
        if ((Get-BSLFlowCouncilNormalizedReference $liveSpecText) -cne (Get-BSLFlowCouncilNormalizedReference $Review.chair.final_spec_text)) {
            $errors.Add('Published spec.md is not the chair final specification text.')
        }
    }
    catch { $errors.Add("Could not compare final specification text: $($_.Exception.Message)") }
    if ($Review.verdict -cne 'PASS' -and @($Review.findings).Count -eq 0 -and @($Review.protected).Count -eq 0) {
        # needs_input without findings is allowed only with explicit questions in member payloads;
        # at review level a non-PASS still needs at least one decision to reconcile.
        if (@($Review.chair.decisions).Count -eq 0) { $errors.Add('A non-PASS council review must contain at least one reconciled decision.') }
    }
    return [ordered]@{ passed = ($errors.Count -eq 0); errors = @($errors) }
}
