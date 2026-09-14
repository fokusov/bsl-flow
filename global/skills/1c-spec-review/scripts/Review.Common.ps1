#Requires -Version 7.0
Set-StrictMode -Version Latest

function Get-BSLFlowSha256 {
    param([Parameter(Mandatory)][string]$Path)
    return (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
}

function Get-BSLFlowBytesSha256 {
    param([Parameter(Mandatory)][AllowEmptyCollection()][byte[]]$Bytes)
    $sha = [System.Security.Cryptography.SHA256]::Create()
    try { return ([BitConverter]::ToString($sha.ComputeHash($Bytes))).Replace('-', '').ToLowerInvariant() }
    finally { $sha.Dispose() }
}

function Get-BSLFlowYamlValue {
    param(
        [Parameter(Mandatory)][AllowEmptyString()][string]$Text,
        [Parameter(Mandatory)][string[]]$Path,
        [string]$Default
    )

    $stack = [System.Collections.Generic.List[object]]::new()
    $resultValues = [System.Collections.Generic.List[string]]::new()
    foreach ($line in ($Text -split "`r?`n")) {
        if ($line -match '^\s*(?:#.*)?$') { continue }
        if ($line -notmatch '^(?<indent>\s*)(?<key>[A-Za-z0-9_-]+):(?:\s*(?<value>.*?))?\s*$') { continue }
        if ($Matches.indent.Contains("`t")) { throw 'Tabs are not supported in bsl-flow.yaml indentation.' }
        $indent = $Matches.indent.Length
        while ($stack.Count -gt 0 -and $stack[$stack.Count - 1].Indent -ge $indent) { $stack.RemoveAt($stack.Count - 1) }
        $keys = @($stack | ForEach-Object { $_.Key }) + @($Matches.key)

        $value = $Matches.value.Trim()
        if ($value -and (($keys -join '/') -eq ($Path -join '/'))) {
            $resultValues.Add($value.Trim('"', "'"))
        }
        if (-not $value) { $stack.Add([pscustomobject]@{ Indent = $indent; Key = $Matches.key }) }
    }
    if ($resultValues.Count -gt 1) { throw "Duplicate YAML value: $($Path -join '.')" }
    if ($resultValues.Count -eq 1) { return $resultValues[0] }
    return $Default
}

function ConvertTo-BSLFlowBoolean {
    param([Parameter(Mandatory)][string]$Value, [Parameter(Mandatory)][string]$Name)
    switch ($Value.ToLowerInvariant()) {
        'true' { return $true }
        'false' { return $false }
        default { throw "Expected true or false for $Name, got: $Value" }
    }
}

function Write-BSLFlowJsonAtomic {
    param(
        [Parameter(Mandatory)]$Value,
        [Parameter(Mandatory)][string]$Path,
        [int]$Depth = 20
    )
    $directory = Split-Path -Parent $Path
    if (-not (Test-Path -LiteralPath $directory -PathType Container)) {
        New-Item -ItemType Directory -Path $directory -Force | Out-Null
    }
    $tempPath = Join-Path $directory ('.' + [System.IO.Path]::GetFileName($Path) + '.' + [guid]::NewGuid().ToString('N') + '.tmp')
    try {
        $Value | ConvertTo-Json -Depth $Depth | Set-Content -LiteralPath $tempPath -Encoding utf8
        Move-Item -LiteralPath $tempPath -Destination $Path -Force
    }
    finally {
        if (Test-Path -LiteralPath $tempPath -PathType Leaf) {
            Remove-Item -LiteralPath $tempPath -Force -ErrorAction SilentlyContinue
        }
    }
}

function Assert-BSLFlowText {
    param($Value, [Parameter(Mandatory)][string]$Name, [switch]$AllowEmpty)
    if ($null -eq $Value -or $Value -isnot [string] -or ((-not $AllowEmpty) -and [string]::IsNullOrWhiteSpace($Value))) {
        throw "Review field must be a non-empty string: $Name"
    }
}

function Assert-BSLFlowObjectProperties {
    param($Value, [Parameter(Mandatory)][string]$Name, [Parameter(Mandatory)][string[]]$Required)
    if ($null -eq $Value -or $Value -is [string] -or $null -eq $Value.PSObject) { throw "$Name must be an object." }
    $actual = @($Value.PSObject.Properties.Name)
    foreach ($property in $actual) {
        if ($property -notin $Required) { throw "Unknown $Name property: $property" }
    }
    foreach ($property in $Required) {
        if ($property -notin $actual) { throw "Missing $Name property: $property" }
    }
}

function Get-BSLFlowJsonNumber {
    param($Value, [Parameter(Mandatory)][string]$Name, [switch]$Integer)
    if ($null -eq $Value -or $Value -is [string] -or $Value -is [bool] -or $Value -isnot [ValueType]) { throw "$Name must be a JSON number." }
    try { $number = [double]$Value } catch { throw "$Name must be a JSON number." }
    if ([double]::IsNaN($number) -or [double]::IsInfinity($number)) { throw "$Name must be finite." }
    if ($Integer -and $number -ne [math]::Floor($number)) { throw "$Name must be an integer." }
    return $number
}

function Assert-BSLFlowArray {
    param($Value, [Parameter(Mandatory)][string]$Name)
    if ($null -eq $Value -or $Value -is [string] -or $Value -isnot [System.Collections.IList]) { throw "$Name must be a JSON array." }
}

function Get-BSLFlowAllowedFindingCategories {
    return @(
        'intent_drift',
        'missing_requirement',
        'lost_requirement',
        'unsupported_assumption',
        'scope_creep',
        'overengineering',
        'architecture_fit',
        'testability',
        'clarity',
        'prompt_injection'
    )
}

function Assert-BSLFlowReviewPayload {
    param([Parameter(Mandatory)]$Review, [switch]$Completed)

    $rawProperties = @('schema_version', 'reviewer_verdict', 'summary', 'scores', 'overengineering', 'findings', 'do_not_change', 'confidence')
    $completedProperties = @($rawProperties + @('reviewed_at_utc', 'review_iteration', 'verdict', 'weighted_score', 'blocking_findings', 'reviewer', 'inputs', 'gate'))
    $allowedTop = if ($Completed) { $completedProperties } else { $rawProperties }
    Assert-BSLFlowObjectProperties $Review 'review' $allowedTop

    if ((Get-BSLFlowJsonNumber $Review.schema_version 'review.schema_version' -Integer) -ne 1) { throw 'review.schema_version must be 1.' }
    if ($Review.reviewer_verdict -notin @('PASS', 'REVISE', 'BLOCK')) { throw 'Invalid reviewer_verdict.' }
    Assert-BSLFlowText $Review.summary 'summary'
    $scoreNames = @('intent_fidelity', 'minimality', 'completeness', 'architecture_fit', 'testability', 'assumption_discipline', 'clarity')
    Assert-BSLFlowObjectProperties $Review.scores 'scores' $scoreNames
    foreach ($name in $scoreNames) {
        $score = Get-BSLFlowJsonNumber $Review.scores.$name "scores.$name" -Integer
        if ($score -lt 1 -or $score -gt 5) { throw "Invalid integer 1..5 score: $name" }
    }
    $confidence = Get-BSLFlowJsonNumber $Review.confidence 'confidence'
    if ($confidence -lt 0 -or $confidence -gt 1) { throw 'confidence must be between 0 and 1.' }

    $rawOverengineeringProperties = @('items')
    $completedOverengineeringProperties = @('architectural_decision_count', 'required_count', 'justified_count', 'optional_count', 'unjustified_count', 'index', 'optional_ratio', 'unjustified_ratio', 'normalized_index', 'items')
    Assert-BSLFlowObjectProperties $Review.overengineering 'overengineering' $(if ($Completed) { $completedOverengineeringProperties } else { $rawOverengineeringProperties })
    Assert-BSLFlowArray $Review.overengineering.items 'overengineering.items'
    Assert-BSLFlowArray $Review.findings 'findings'
    Assert-BSLFlowArray $Review.do_not_change 'do_not_change'

    $findingIds = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::Ordinal)
    $findingIndex = 0
    foreach ($finding in @($Review.findings)) {
        $findingIndex++
        Assert-BSLFlowObjectProperties $finding 'finding' @('id', 'severity', 'category', 'spec_ref', 'issue', 'evidence', 'suggested_direction')
        if ($finding.id -notmatch '^R-[0-9]{3,}$') { throw "Invalid finding id: $($finding.id)" }
        if ($finding.id -ne ('R-{0:D3}' -f $findingIndex)) { throw "Finding IDs must be sequential: expected R-$('{0:D3}' -f $findingIndex)." }
        if (-not $findingIds.Add([string]$finding.id)) { throw "Duplicate finding id: $($finding.id)" }
        if ($finding.severity -notin @('blocker', 'high', 'medium', 'low')) { throw "Invalid finding severity: $($finding.id)" }
        if ($finding.category -notin @(Get-BSLFlowAllowedFindingCategories)) {
            throw "Invalid finding category '$($finding.category)' in finding $($finding.id)."
        }
        foreach ($field in @('spec_ref', 'issue', 'evidence', 'suggested_direction')) {
            Assert-BSLFlowText $finding.$field "findings.$($finding.id).$field"
        }
    }
    foreach ($item in @($Review.overengineering.items)) {
        Assert-BSLFlowObjectProperties $item 'overengineering item' @('spec_ref', 'item', 'necessity', 'evidence', 'simpler_direction')
        foreach ($field in @('spec_ref', 'item', 'necessity', 'evidence')) {
            Assert-BSLFlowText $item.$field "overengineering.items.$field"
        }
        Assert-BSLFlowText $item.simpler_direction 'overengineering.items.simpler_direction' -AllowEmpty
        if ($item.necessity -notin @('required', 'justified', 'optional', 'unjustified')) { throw "Invalid necessity: $($item.necessity)" }
    }
    $doNotChange = @($Review.do_not_change)
    foreach ($item in $doNotChange) { Assert-BSLFlowText $item 'do_not_change[]' }
    if (@($doNotChange | Select-Object -Unique).Count -ne $doNotChange.Count) { throw 'do_not_change items must be unique.' }

    if ($Completed) {
        if ((Get-BSLFlowJsonNumber $Review.review_iteration 'review_iteration' -Integer) -ne 1) { throw 'review_iteration must be 1.' }
        if ($Review.verdict -notin @('PASS', 'REVISE', 'BLOCK')) { throw 'Invalid gate verdict.' }
        $weightedScore = Get-BSLFlowJsonNumber $Review.weighted_score 'weighted_score'
        if ($weightedScore -lt 1 -or $weightedScore -gt 5) { throw 'weighted_score must be between 1 and 5.' }
        $validDate = $false
        if ($Review.reviewed_at_utc -is [DateTime]) {
            # PowerShell 7.5+ may materialize JSON date-time strings as DateTime.
            $validDate = $Review.reviewed_at_utc.Kind -ne [DateTimeKind]::Unspecified
        }
        elseif ($Review.reviewed_at_utc -is [DateTimeOffset]) { $validDate = $true }
        elseif ($Review.reviewed_at_utc -is [string]) {
            $parsedDate = [DateTimeOffset]::MinValue
            $validDate = [DateTimeOffset]::TryParse($Review.reviewed_at_utc, [Globalization.CultureInfo]::InvariantCulture, [Globalization.DateTimeStyles]::RoundtripKind, [ref]$parsedDate) -and $Review.reviewed_at_utc -match '(?:Z|[+-][0-9]{2}:[0-9]{2})$'
        }
        if (-not $validDate) { throw 'reviewed_at_utc must be an RFC 3339 date-time string with an offset.' }
        foreach ($name in @('architectural_decision_count', 'required_count', 'justified_count', 'optional_count', 'unjustified_count', 'index')) {
            if ((Get-BSLFlowJsonNumber $Review.overengineering.$name "overengineering.$name" -Integer) -lt 0) { throw "Invalid overengineering count: $name" }
        }
        foreach ($name in @('optional_ratio', 'unjustified_ratio', 'normalized_index')) {
            $ratio = Get-BSLFlowJsonNumber $Review.overengineering.$name "overengineering.$name"
            if ($ratio -lt 0 -or $ratio -gt 1) { throw "Invalid overengineering ratio: $name" }
        }
        Assert-BSLFlowObjectProperties $Review.reviewer 'reviewer' @('provider', 'agent', 'model')
        if ($Review.reviewer.provider -ne 'opencode') { throw 'reviewer.provider must be opencode.' }
        foreach ($name in @('agent', 'model')) { Assert-BSLFlowText $Review.reviewer.$name "reviewer.$name" }
        Assert-BSLFlowObjectProperties $Review.inputs 'inputs' @('original_task_sha256', 'spec_sha256', 'design_sha256')
        foreach ($name in @('original_task_sha256', 'spec_sha256')) {
            if ($Review.inputs.$name -isnot [string] -or $Review.inputs.$name -notmatch '^[a-f0-9]{64}$') { throw "Invalid input hash: $name" }
        }
        if ($null -ne $Review.inputs.design_sha256 -and ($Review.inputs.design_sha256 -isnot [string] -or $Review.inputs.design_sha256 -notmatch '^[a-f0-9]{64}$')) { throw 'Invalid design_sha256.' }
        Assert-BSLFlowObjectProperties $Review.gate 'gate' @('pass_weighted_score', 'block_below_weighted_score', 'max_overengineering_index_for_pass', 'max_unjustified_ratio_for_pass')
        foreach ($name in @('pass_weighted_score', 'block_below_weighted_score')) {
            $threshold = Get-BSLFlowJsonNumber $Review.gate.$name "gate.$name"
            if ($threshold -lt 1 -or $threshold -gt 5) { throw "Invalid gate threshold: $name" }
        }
        if ((Get-BSLFlowJsonNumber $Review.gate.max_overengineering_index_for_pass 'gate.max_overengineering_index_for_pass' -Integer) -lt 0) { throw 'Invalid max_overengineering_index_for_pass.' }
        $maxRatio = Get-BSLFlowJsonNumber $Review.gate.max_unjustified_ratio_for_pass 'gate.max_unjustified_ratio_for_pass'
        if ($maxRatio -lt 0 -or $maxRatio -gt 1) { throw 'Invalid max_unjustified_ratio_for_pass.' }
        Assert-BSLFlowArray $Review.blocking_findings 'blocking_findings'
        $blocking = @($Review.blocking_findings)
        foreach ($id in $blocking) { if ($id -isnot [string] -or $id -notmatch '^R-[0-9]{3,}$') { throw 'Invalid blocking_findings item.' } }
        if (@($blocking | Select-Object -Unique).Count -ne $blocking.Count) { throw 'blocking_findings must be unique.' }
        if ($Review.verdict -ne 'PASS' -and @($Review.findings).Count -eq 0) {
            throw 'A non-PASS review must contain at least one finding.'
        }
    }
}

function Assert-BSLFlowReviewReconciliationPayload {
    param([Parameter(Mandatory)]$Reconciliation)

    Assert-BSLFlowObjectProperties $Reconciliation 'reconciliation' @(
        'schema_version', 'review_sha256', 'draft_spec_sha256', 'final_spec_sha256',
        'draft_design_sha256', 'final_design_sha256', 'reconciled_at_utc', 'summary',
        'decisions', 'do_not_change_checks'
    )
    if ((Get-BSLFlowJsonNumber $Reconciliation.schema_version 'reconciliation.schema_version' -Integer) -ne 1) {
        throw 'reconciliation.schema_version must be 1.'
    }
    foreach ($name in @('review_sha256', 'draft_spec_sha256', 'final_spec_sha256')) {
        if ($Reconciliation.$name -isnot [string] -or $Reconciliation.$name -notmatch '^[a-f0-9]{64}$') {
            throw "Invalid reconciliation hash: $name"
        }
    }
    foreach ($name in @('draft_design_sha256', 'final_design_sha256')) {
        if ($null -ne $Reconciliation.$name -and ($Reconciliation.$name -isnot [string] -or $Reconciliation.$name -notmatch '^[a-f0-9]{64}$')) {
            throw "Invalid reconciliation hash: $name"
        }
    }
    $validDate = $false
    if ($Reconciliation.reconciled_at_utc -is [DateTime]) {
        $validDate = $Reconciliation.reconciled_at_utc.Kind -ne [DateTimeKind]::Unspecified
    }
    elseif ($Reconciliation.reconciled_at_utc -is [DateTimeOffset]) { $validDate = $true }
    elseif ($Reconciliation.reconciled_at_utc -is [string]) {
        $parsedDate = [DateTimeOffset]::MinValue
        $validDate = [DateTimeOffset]::TryParse($Reconciliation.reconciled_at_utc, [Globalization.CultureInfo]::InvariantCulture, [Globalization.DateTimeStyles]::RoundtripKind, [ref]$parsedDate) -and $Reconciliation.reconciled_at_utc -match '(?:Z|[+-][0-9]{2}:[0-9]{2})$'
    }
    if (-not $validDate) { throw 'reconciliation.reconciled_at_utc must be an RFC 3339 date-time string with an offset.' }
    Assert-BSLFlowText $Reconciliation.summary 'reconciliation.summary'
    Assert-BSLFlowArray $Reconciliation.decisions 'reconciliation.decisions'
    Assert-BSLFlowArray $Reconciliation.do_not_change_checks 'reconciliation.do_not_change_checks'

    foreach ($decision in @($Reconciliation.decisions)) {
        Assert-BSLFlowObjectProperties $decision 'reconciliation decision' @('finding_id', 'decision', 'reason', 'evidence', 'status', 'resolution', 'spec_ref_after')
        if ($decision.finding_id -isnot [string] -or $decision.finding_id -notmatch '^R-[0-9]{3,}$') { throw 'Invalid reconciliation decision finding_id.' }
        if ($decision.decision -notin @('accepted', 'rejected')) { throw "Invalid reconciliation decision: $($decision.finding_id)" }
        if ($decision.status -notin @('addressed', 'not_applicable')) { throw "Invalid reconciliation status: $($decision.finding_id)" }
        foreach ($name in @('reason', 'evidence', 'resolution', 'spec_ref_after')) {
            Assert-BSLFlowText $decision.$name "reconciliation.decisions.$($decision.finding_id).$name"
        }
    }
    foreach ($check in @($Reconciliation.do_not_change_checks)) {
        Assert-BSLFlowObjectProperties $check 'do_not_change check' @('item', 'decision', 'reason', 'evidence')
        foreach ($name in @('item', 'reason', 'evidence')) { Assert-BSLFlowText $check.$name "reconciliation.do_not_change_checks.$name" }
        if ($check.decision -notin @('preserved', 'rejected')) { throw "Invalid do_not_change decision: $($check.item)" }
    }
}

function Get-BSLFlowReviewPolicy {
    param([AllowEmptyString()][string]$ConfigText)
    $readMode=Get-BSLFlowYamlValue $ConfigText @('review','permissions','project_read_mode') 'read_search'
    if($readMode -notin @('read_search','attached_only')){throw "Invalid project_read_mode: $readMode"}
    foreach($forbidden in @('edit','shell','subagents','web','external_directory')){
        if(ConvertTo-BSLFlowBoolean (Get-BSLFlowYamlValue $ConfigText @('review','permissions',$forbidden) 'false') "review.permissions.$forbidden"){throw "Unsafe reviewer permission cannot be enabled: $forbidden"}
    }
    $culture=[Globalization.CultureInfo]::InvariantCulture
    $policy=[ordered]@{
        ReadMode=$readMode
        PassWeightedScore=[double]::Parse((Get-BSLFlowYamlValue $ConfigText @('review','thresholds','pass_weighted_score') '4.3'),$culture)
        BlockBelowWeightedScore=[double]::Parse((Get-BSLFlowYamlValue $ConfigText @('review','thresholds','block_below_weighted_score') '3.5'),$culture)
        MaxOverengineeringIndexForPass=[int]::Parse((Get-BSLFlowYamlValue $ConfigText @('review','thresholds','max_overengineering_index_for_pass') '1'),$culture)
        MaxUnjustifiedRatioForPass=[double]::Parse((Get-BSLFlowYamlValue $ConfigText @('review','thresholds','max_unjustified_ratio_for_pass') '0'),$culture)
    }
    if($policy.PassWeightedScore -lt 1 -or $policy.PassWeightedScore -gt 5 -or $policy.BlockBelowWeightedScore -lt 1 -or $policy.BlockBelowWeightedScore -gt 5){throw 'Review score thresholds must be between 1 and 5.'}
    if($policy.BlockBelowWeightedScore -gt $policy.PassWeightedScore){throw 'block_below_weighted_score must not exceed pass_weighted_score.'}
    if($policy.MaxOverengineeringIndexForPass -lt 0){throw 'max_overengineering_index_for_pass must not be negative.'}
    if($policy.MaxUnjustifiedRatioForPass -lt 0 -or $policy.MaxUnjustifiedRatioForPass -gt 1){throw 'max_unjustified_ratio_for_pass must be between 0 and 1.'}
    return $policy
}

function Complete-BSLFlowReview {
    param(
        [Parameter(Mandatory)]$RawReview,
        [Parameter(Mandatory)][string]$OriginalTaskPath,
        [Parameter(Mandatory)][string]$SpecPath,
        [string]$DesignPath,
        [Parameter(Mandatory)][string]$Agent,
        [Parameter(Mandatory)][string]$Model,
        [Parameter(Mandatory)][double]$PassWeightedScore,
        [Parameter(Mandatory)][double]$BlockBelowWeightedScore,
        [Parameter(Mandatory)][int]$MaxOverengineeringIndexForPass,
        [Parameter(Mandatory)][double]$MaxUnjustifiedRatioForPass,
        [string]$OriginalTaskSha256,
        [string]$SpecSha256,
        [AllowNull()]$DesignSha256
    )

    Assert-BSLFlowReviewPayload -Review $RawReview
    $weights = [ordered]@{
        intent_fidelity = 0.25
        minimality = 0.20
        completeness = 0.15
        architecture_fit = 0.15
        testability = 0.10
        assumption_discipline = 0.10
        clarity = 0.05
    }
    $weighted = 0.0
    foreach ($entry in $weights.GetEnumerator()) { $weighted += [double]$RawReview.scores.($entry.Key) * $entry.Value }
    $weighted = [math]::Round($weighted, 2)

    $items = @($RawReview.overengineering.items)
    $counts = @{}
    foreach ($necessity in @('required', 'justified', 'optional', 'unjustified')) {
        $counts[$necessity] = @($items | Where-Object { $_.necessity -eq $necessity }).Count
    }
    $decisionCount = $items.Count
    $index = $counts.optional + (3 * $counts.unjustified)
    if ($decisionCount -eq 0) {
        $optionalRatio = 0.0; $unjustifiedRatio = 0.0; $normalizedIndex = 0.0
    }
    else {
        $optionalRatio = [math]::Round($counts.optional / $decisionCount, 4)
        $unjustifiedRatio = [math]::Round($counts.unjustified / $decisionCount, 4)
        $normalizedIndex = [math]::Round($index / (3 * $decisionCount), 4)
    }
    $blocking = @($RawReview.findings | Where-Object { $_.severity -eq 'blocker' } | ForEach-Object { $_.id })

    $materialFindings = @($RawReview.findings | Where-Object { $_.severity -in @('high', 'medium') })
    if ($RawReview.reviewer_verdict -eq 'BLOCK' -or $blocking.Count -gt 0 -or $weighted -lt $BlockBelowWeightedScore) { $gateVerdict = 'BLOCK' }
    elseif ($RawReview.reviewer_verdict -eq 'REVISE' -or $materialFindings.Count -gt 0) { $gateVerdict = 'REVISE' }
    elseif ($weighted -ge $PassWeightedScore -and $index -le $MaxOverengineeringIndexForPass -and $unjustifiedRatio -le $MaxUnjustifiedRatioForPass) { $gateVerdict = 'PASS' }
    else { $gateVerdict = 'REVISE' }
    if ($gateVerdict -ne 'PASS' -and @($RawReview.findings).Count -eq 0) {
        throw 'A computed non-PASS review without findings is invalid.'
    }

    if (-not $OriginalTaskSha256) { $OriginalTaskSha256 = Get-BSLFlowSha256 $OriginalTaskPath }
    if (-not $SpecSha256) { $SpecSha256 = Get-BSLFlowSha256 $SpecPath }
    if (-not $PSBoundParameters.ContainsKey('DesignSha256')) {
        $DesignSha256 = if ($DesignPath -and (Test-Path -LiteralPath $DesignPath -PathType Leaf)) { Get-BSLFlowSha256 $DesignPath } else { $null }
    }
    foreach ($hash in @($OriginalTaskSha256, $SpecSha256)) {
        if ($hash -notmatch '^[a-f0-9]{64}$') { throw 'Invalid captured review input hash.' }
    }
    if ($null -ne $DesignSha256 -and $DesignSha256 -notmatch '^[a-f0-9]{64}$') { throw 'Invalid captured design hash.' }

    return [ordered]@{
        schema_version = 1
        reviewed_at_utc = [DateTime]::UtcNow.ToString('o')
        review_iteration = 1
        reviewer_verdict = [string]$RawReview.reviewer_verdict
        verdict = $gateVerdict
        summary = [string]$RawReview.summary
        scores = $RawReview.scores
        weighted_score = $weighted
        overengineering = [ordered]@{
            architectural_decision_count = $decisionCount
            required_count = $counts.required
            justified_count = $counts.justified
            optional_count = $counts.optional
            unjustified_count = $counts.unjustified
            index = $index
            optional_ratio = $optionalRatio
            unjustified_ratio = $unjustifiedRatio
            normalized_index = $normalizedIndex
            items = $items
        }
        blocking_findings = $blocking
        findings = @($RawReview.findings)
        do_not_change = @($RawReview.do_not_change)
        confidence = [double]$RawReview.confidence
        reviewer = [ordered]@{ provider = 'opencode'; agent = $Agent; model = $Model }
        inputs = [ordered]@{
            original_task_sha256 = $OriginalTaskSha256
            spec_sha256 = $SpecSha256
            design_sha256 = $DesignSha256
        }
        gate = [ordered]@{
            pass_weighted_score = $PassWeightedScore
            block_below_weighted_score = $BlockBelowWeightedScore
            max_overengineering_index_for_pass = $MaxOverengineeringIndexForPass
            max_unjustified_ratio_for_pass = $MaxUnjustifiedRatioForPass
        }
    }
}

function Get-BSLFlowJsonFromOpenCodeEvents {
    param([Parameter(Mandatory)][string[]]$Lines)
    $parts = [System.Collections.Generic.List[string]]::new()
    foreach ($line in $Lines) {
        if ([string]::IsNullOrWhiteSpace($line)) { continue }
        try { $event = $line | ConvertFrom-Json -ErrorAction Stop }
        catch { continue }
        if ($event.type -eq 'text' -and $event.part -and $event.part.text) { $parts.Add([string]$event.part.text) }
        elseif ($event.type -eq 'error') { throw "OpenCode returned an error event: $line" }
    }
    if ($parts.Count -eq 0) { throw 'OpenCode returned no completed text event.' }

    # Chunked providers split one JSON document across several text parts and
    # must reassemble without separators; block providers emit prose and the
    # fenced review as separate parts, whose boundary needs a newline to keep
    # the fence on its own line. Try both joins; every extraction guard runs
    # unchanged for each candidate, so ambiguity is never weakened.
    $joinErrors = [System.Collections.Generic.List[string]]::new()
    foreach ($joined in @(($parts -join ''), ($parts -join "`n"))) {
        $text = $joined.Trim()
        if (-not $text) { continue }
        try { return Get-BSLFlowReviewPayloadFromOpenCodeText -Text $text } catch { $joinErrors.Add([string]$_.Exception.Message) }
    }
    if ($joinErrors.Count -eq 0) { throw 'OpenCode returned no completed text event.' }
    throw "OpenCode text was not one JSON object: $($joinErrors[0])"
}

function Get-BSLFlowReviewPayloadFromOpenCodeText {
    param([Parameter(Mandatory)][string]$Text)
    $text = $Text

    # OpenCode may surround its final response with prose. Accept one complete,
    # unambiguous fenced block only; schema validation remains the caller's gate.
    $blocks = [regex]::Matches($text, '(?ims)^```(?:json)?[ \t]*\r?\n(?<json>.*?)^```[ \t]*\r?$')
    if ($blocks.Count -gt 0) {
        if ($blocks.Count -ne 1) { throw 'OpenCode returned multiple fenced objects; review is ambiguous.' }
        $outside = $text.Remove($blocks[0].Index, $blocks[0].Length)
        if ($outside -match '```') { throw 'OpenCode returned additional fenced text outside the review block.' }

        # Reject an additional structured candidate beside the fenced review,
        # while allowing ordinary prose such as "Result [PASS]".
        $outsideCandidates = [System.Collections.Generic.List[object]]::new()
        $stack = [System.Collections.Generic.List[char]]::new()
        $startStack = [System.Collections.Generic.List[int]]::new()
        $nestedCandidates = [System.Collections.Generic.List[object]]::new()
        $inString = $false
        $escaped = $false
        for ($cursor = 0; $cursor -lt $outside.Length; $cursor++) {
            $character = $outside[$cursor]
            if ($inString) {
                if ($escaped) { $escaped = $false; continue }
                if ($character -eq '\') { $escaped = $true; continue }
                if ($character -eq '"') { $inString = $false }
                continue
            }
            if ($stack.Count -gt 0 -and $character -eq '"') { $inString = $true; continue }
            if ($character -eq '{' -or $character -eq '[') {
                if ($stack.Count -eq 0) { $nestedCandidates.Clear() }
                $stack.Add($character)
                $startStack.Add($cursor)
                continue
            }
            if ($character -ne '}' -and $character -ne ']') { continue }
            if ($stack.Count -eq 0) { continue }
            $opening = $stack[$stack.Count - 1]
            if (($opening -eq '{' -and $character -ne '}') -or ($opening -eq '[' -and $character -ne ']')) {
                foreach ($candidate in $nestedCandidates) { $outsideCandidates.Add($candidate) }
                $stack.Clear()
                $startStack.Clear()
                $nestedCandidates.Clear()
                $inString = $false
                $escaped = $false
                continue
            }
            $candidateStart = $startStack[$startStack.Count - 1]
            $stack.RemoveAt($stack.Count - 1)
            $startStack.RemoveAt($startStack.Count - 1)
            $candidateText = $outside.Substring($candidateStart, $cursor - $candidateStart + 1)
            $candidateValid = $false
            try {
                $candidate = ConvertFrom-Json -InputObject $candidateText -NoEnumerate -ErrorAction Stop
                if ($candidate -is [pscustomobject] -or $candidate -is [array]) {
                    $candidateValid = $true
                    if ($stack.Count -eq 0) { $outsideCandidates.Add($candidate) }
                    else { $nestedCandidates.Add($candidate) }
                }
            }
            catch { }
            if ($stack.Count -eq 0) {
                if (-not $candidateValid) { foreach ($nested in $nestedCandidates) { $outsideCandidates.Add($nested) } }
                $nestedCandidates.Clear()
            }
        }
        if ($stack.Count -ne 0) { throw 'OpenCode returned an unclosed structured candidate outside the review block.' }
        if ($outsideCandidates.Count -gt 0) { throw 'OpenCode returned additional structured text outside the review block.' }
        $text = $blocks[0].Groups['json'].Value.Trim()
    }

    try {
        # The review contract requires an object, including for one-item payloads.
        if ($text.TrimStart().StartsWith('[')) { throw 'Review must be a JSON object.' }
        $parsed = ConvertFrom-Json -InputObject $text -NoEnumerate -ErrorAction Stop
        if ($null -eq $parsed -or $parsed.GetType().FullName -ne 'System.Management.Automation.PSCustomObject') {
            throw 'Review must be a JSON object.'
        }
        return $parsed
    }
    catch { throw "OpenCode text was not one JSON object: $($_.Exception.Message)" }
}

function Get-BSLFlowBoundedUtf8Snapshot {
    param([Parameter(Mandatory)][string]$Path, [Parameter(Mandatory)][int]$MaxBytes)
    $bytes = [System.IO.File]::ReadAllBytes($Path)
    if ($bytes.Length -gt $MaxBytes) { throw "Review input exceeds $MaxBytes bytes: $Path" }
    $utf8 = [System.Text.UTF8Encoding]::new($false, $true)
    try { $text = $utf8.GetString($bytes) }
    catch { throw "Review input is not valid UTF-8: $Path" }
    return [pscustomobject]@{ Bytes = $bytes; Text = $text; Sha256 = Get-BSLFlowBytesSha256 $bytes }
}
