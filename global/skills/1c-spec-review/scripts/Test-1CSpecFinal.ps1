[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$ProjectPath,
    [Parameter(Mandatory)][string]$ChangeName
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'Review.Common.ps1')

$projectRoot = [System.IO.Path]::GetFullPath($ProjectPath).TrimEnd('\', '/')
if ($ChangeName -notmatch '^[A-Za-z0-9][A-Za-z0-9._-]*$') { throw "Unsafe OpenSpec change name: $ChangeName" }
$changeRoot = Join-Path $projectRoot "openspec\changes\$ChangeName"
$reviewPath = Join-Path $changeRoot 'review.json'
$reconciliationPath = Join-Path $changeRoot 'review-reconciliation.json'
$specPath = Join-Path $changeRoot 'spec.md'
$originalTaskPath = Join-Path $changeRoot 'original-task.md'
$designPath = Join-Path $changeRoot 'design.md'
$outputPath = Join-Path $changeRoot 'final-validation.json'
$errors = [System.Collections.Generic.List[string]]::new()
$review = $null
$reconciliation = $null
$lint = $null
$reviewHash = $null
$reconciliationHash = $null
$finalSpecHash = $null
$finalDesignHash = $null
$originalTaskHash = $null

foreach ($required in @($reviewPath, $reconciliationPath, $specPath, $originalTaskPath)) {
    if (-not (Test-Path -LiteralPath $required -PathType Leaf)) { $errors.Add("Missing required final-validation input: $required") }
}
if ($errors.Count -eq 0) {
    try { $review = Get-Content -Raw -LiteralPath $reviewPath | ConvertFrom-Json -ErrorAction Stop; Assert-BSLFlowReviewPayload $review -Completed }
    catch { $errors.Add("Invalid review.json: $($_.Exception.Message)") }
    try { $reconciliation = Get-Content -Raw -LiteralPath $reconciliationPath | ConvertFrom-Json -ErrorAction Stop }
    catch { $errors.Add("Invalid review-reconciliation.json: $($_.Exception.Message)") }
    try { $lint = & (Join-Path $PSScriptRoot 'Test-1CSpec.ps1') -ChangePath $changeRoot -NoThrow }
    catch { $errors.Add("Final spec lint could not run: $($_.Exception.Message)") }
}

if ($errors.Count -eq 0) {
    if ($review.review_iteration -ne 1) { $errors.Add('Only one content review iteration is allowed in the normal workflow.') }
    $reviewHash = Get-BSLFlowSha256 $reviewPath
    $reconciliationHash = Get-BSLFlowSha256 $reconciliationPath
    $finalSpecHash = Get-BSLFlowSha256 $specPath
    $finalDesignHash = if (Test-Path -LiteralPath $designPath -PathType Leaf) { Get-BSLFlowSha256 $designPath } else { $null }
    $originalTaskHash = Get-BSLFlowSha256 $originalTaskPath
    if ($reconciliation.schema_version -ne 1) { $errors.Add('reconciliation.schema_version must be 1.') }
    if ($reconciliation.review_sha256 -ne $reviewHash) { $errors.Add('reconciliation.review_sha256 does not match review.json.') }
    if ($reconciliation.draft_spec_sha256 -ne $review.inputs.spec_sha256) { $errors.Add('reconciliation.draft_spec_sha256 does not match the reviewed draft.') }
    if ($reconciliation.final_spec_sha256 -ne $finalSpecHash) { $errors.Add('reconciliation.final_spec_sha256 does not match current spec.md.') }
    if ($reconciliation.draft_design_sha256 -ne $review.inputs.design_sha256) { $errors.Add('reconciliation.draft_design_sha256 does not match the reviewed design.') }
    if ($reconciliation.final_design_sha256 -ne $finalDesignHash) { $errors.Add('reconciliation.final_design_sha256 does not match current design.md.') }
    if ($originalTaskHash -ne $review.inputs.original_task_sha256) { $errors.Add('original-task.md changed after review.') }
    if ([string]::IsNullOrWhiteSpace([string]$reconciliation.summary)) { $errors.Add('reconciliation.summary is required.') }

    $findings = @($review.findings)
    $decisions = @($reconciliation.decisions)
    $decisionIds = @($decisions | ForEach-Object { $_.finding_id })
    if (@($decisionIds | Select-Object -Unique).Count -ne $decisionIds.Count) { $errors.Add('A finding is reconciled more than once.') }
    foreach ($finding in $findings) {
        $matches = @($decisions | Where-Object { $_.finding_id -eq $finding.id })
        if ($matches.Count -ne 1) { $errors.Add("Finding must be reconciled exactly once: $($finding.id)"); continue }
        $decision = $matches[0]
        if ($decision.decision -notin @('accepted', 'rejected')) { $errors.Add("Invalid decision for $($finding.id).") }
        foreach ($field in @('reason', 'evidence', 'resolution', 'spec_ref_after')) {
            if ([string]::IsNullOrWhiteSpace([string]$decision.$field)) { $errors.Add("Missing $field for $($finding.id).") }
        }
        if ($decision.decision -eq 'accepted' -and $decision.status -ne 'addressed') { $errors.Add("Accepted finding is not addressed: $($finding.id)") }
        if ($decision.decision -eq 'rejected' -and $decision.status -ne 'not_applicable') { $errors.Add("Rejected finding must be not_applicable: $($finding.id)") }
    }
    foreach ($decision in $decisions) {
        if ($decision.finding_id -notin @($findings.id)) { $errors.Add("Unknown finding in reconciliation: $($decision.finding_id)") }
    }
    $acceptedCount = @($decisions | Where-Object { $_.decision -eq 'accepted' }).Count
    if ($acceptedCount -gt 0 -and $finalSpecHash -eq $review.inputs.spec_sha256 -and $finalDesignHash -eq $review.inputs.design_sha256) { $errors.Add('Accepted findings exist but neither spec.md nor design.md changed.') }

    try {
        $rawReview = [pscustomobject][ordered]@{
            schema_version = 1; reviewer_verdict = $review.reviewer_verdict; summary = $review.summary
            scores = $review.scores; overengineering = [pscustomobject]@{ items = @($review.overengineering.items) }
            findings = @($review.findings); do_not_change = @($review.do_not_change); confidence = $review.confidence
        }
        $recomputed = Complete-BSLFlowReview -RawReview $rawReview -OriginalTaskPath $originalTaskPath -SpecPath $specPath -DesignPath $designPath -Agent $review.reviewer.agent -Model $review.reviewer.model -PassWeightedScore $review.gate.pass_weighted_score -BlockBelowWeightedScore $review.gate.block_below_weighted_score -MaxOverengineeringIndexForPass $review.gate.max_overengineering_index_for_pass -MaxUnjustifiedRatioForPass $review.gate.max_unjustified_ratio_for_pass
        foreach ($name in @('weighted_score', 'verdict')) {
            if ($review.$name -ne $recomputed.$name) { $errors.Add("Derived review value was altered: $name") }
        }
        foreach ($name in @('architectural_decision_count', 'required_count', 'justified_count', 'optional_count', 'unjustified_count', 'index', 'optional_ratio', 'unjustified_ratio', 'normalized_index')) {
            if ($review.overengineering.$name -ne $recomputed.overengineering.$name) { $errors.Add("Derived overengineering value was altered: $name") }
        }
        if ((@($review.blocking_findings) -join '|') -ne (@($recomputed.blocking_findings) -join '|')) { $errors.Add('Derived blocking_findings were altered.') }
    }
    catch { $errors.Add("Could not recompute review invariants: $($_.Exception.Message)") }

    $checks = @($reconciliation.do_not_change_checks)
    foreach ($item in @($review.do_not_change)) {
        $matches = @($checks | Where-Object { $_.item -eq $item })
        if ($matches.Count -ne 1) { $errors.Add("do_not_change item must be reconciled exactly once: $item"); continue }
        $check = $matches[0]
        if ($check.decision -notin @('preserved', 'rejected')) { $errors.Add("Invalid do_not_change decision: $item") }
        if ([string]::IsNullOrWhiteSpace([string]$check.reason) -or [string]::IsNullOrWhiteSpace([string]$check.evidence)) {
            $errors.Add("do_not_change decision lacks reason/evidence: $item")
        }
    }
    foreach ($check in $checks) {
        if ($check.item -notin @($review.do_not_change)) { $errors.Add("Unknown do_not_change check: $($check.item)") }
    }
    if (-not $lint.passed) { $errors.Add('Final specification lint failed.') }
}

$result = [ordered]@{
    schema_version = 1
    checked_at_utc = [DateTime]::UtcNow.ToString('o')
    passed = ($errors.Count -eq 0)
    review_iteration = if ($null -ne $review) { $review.review_iteration } else { $null }
    inputs = [ordered]@{
        review_sha256 = $reviewHash
        reconciliation_sha256 = $reconciliationHash
        final_spec_sha256 = $finalSpecHash
        final_design_sha256 = $finalDesignHash
        original_task_sha256 = $originalTaskHash
    }
    errors = @($errors)
}
Write-BSLFlowJsonAtomic -Value $result -Path $outputPath
if (-not $result.passed) { throw "Final specification invariant validation failed. See: $outputPath" }
return [pscustomobject]$result
