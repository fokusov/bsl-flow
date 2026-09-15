#Requires -Version 7.0
[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$ChangePath,
    [string]$EstimatePath,
    [switch]$NoThrow
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

# Deterministic validation of the 1c-estimate sidecar artifacts: applicability
# gate, closed JSON Schema, conservative sums, rounding steps, anchor divergence
# threshold with required explanation, and hash-bound freshness. No models.

$changeRoot = [System.IO.Path]::GetFullPath($ChangePath).TrimEnd('\', '/')
$skillRoot = Split-Path -Parent $PSScriptRoot
$schemaPath = Join-Path $skillRoot 'references\estimate-schema.json'
$anchorsPath = Join-Path $skillRoot 'references\anchors.json'
if (-not $EstimatePath) { $EstimatePath = Join-Path $changeRoot 'estimate.json' }
$estimateMdPath = Join-Path $changeRoot 'estimate.md'
$specPath = Join-Path $changeRoot 'spec.md'
$finalValidationPath = Join-Path $changeRoot 'final-validation.json'
$specLintPath = Join-Path $changeRoot 'spec-lint.json'

$errors = [System.Collections.Generic.List[string]]::new()
$warnings = [System.Collections.Generic.List[string]]::new()

function Get-LiveSha256 {
    param([Parameter(Mandatory)][string]$Path)
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { return $null }
    return (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
}
function Test-Step {
    param([Parameter(Mandatory)][double]$Value, [Parameter(Mandatory)][double]$Step, [Parameter(Mandatory)][string]$Label)
    if ([math]::Abs($Value / $Step - [math]::Round($Value / $Step)) -gt 1e-6) {
        $errors.Add("$Label violates the $Step h rounding step: $Value")
    }
}
function Test-ForkPair {
    param($Fork, [Parameter(Mandatory)][string]$Label, [Parameter(Mandatory)][double]$Step)
    if ($null -eq $Fork) { return }
    $min = [double]$Fork.min; $max = [double]$Fork.max
    if ($min -le 0 -or $max -le 0) { $errors.Add("$Label must be positive."); return }
    if ($min -gt $max) { $errors.Add("$Label min exceeds max."); return }
    Test-Step $min $Step "$Label.min"
    Test-Step $max $Step "$Label.max"
}

$estimate = $null
$estimateRaw = $null
if (-not (Test-Path -LiteralPath $specPath -PathType Leaf)) { $errors.Add('spec.md not found in the change directory.') }
if (-not (Test-Path -LiteralPath $EstimatePath -PathType Leaf)) { $errors.Add("estimate.json not found: $EstimatePath") }
else {
    try {
        $estimateRaw = [IO.File]::ReadAllText($EstimatePath, [Text.UTF8Encoding]::new($false))
        $estimate = $estimateRaw | ConvertFrom-Json -ErrorAction Stop
    }
    catch { $errors.Add("estimate.json is not valid JSON: $($_.Exception.Message)") }
}

# Closed JSON Schema check first: structural field checks below rely on it, so
# they run only when the schema itself did not already reject the document.
$schemaValid = $false
if ($null -ne $estimateRaw) {
    $schemaErrors = @()
    $schemaValid = Test-Json -Json $estimateRaw -SchemaFile $schemaPath -ErrorVariable schemaErrors -ErrorAction SilentlyContinue
    if (-not $schemaValid) {
        # PS 7.6 Test-Json writes plain strings into ErrorVariable, not ErrorRecords.
        foreach ($detail in @($schemaErrors | Select-Object -First 6)) { $errors.Add("schema: $detail") }
    }
}

# Applicability gate: a final estimate needs passed final validation, or a
# passed lint when the change is S with low/medium risk (no mandatory review).
$gate = 'none'
$specText = ''
if (Test-Path -LiteralPath $specPath -PathType Leaf) { $specText = [IO.File]::ReadAllText($specPath, [Text.UTF8Encoding]::new($false)) }
$complexityMatch = [regex]::Match($specText, '(?im)^\s*-\s*(?:Сложность|Complexity):\s*(S|M|L)\s*$')
$riskMatch = [regex]::Match($specText, '(?im)^\s*-\s*(?:Риск|Risk):\s*(low|medium|high)\s*$')
$liveComplexity = if ($complexityMatch.Success) { $complexityMatch.Groups[1].Value.ToUpperInvariant() } else { $null }
$liveRisk = if ($riskMatch.Success) { $riskMatch.Groups[1].Value.ToLowerInvariant() } else { $null }
if ($liveComplexity -and $liveRisk) {
    $finalPassed = $false
    if (Test-Path -LiteralPath $finalValidationPath -PathType Leaf) {
        try { $finalPassed = (([IO.File]::ReadAllText($finalValidationPath) | ConvertFrom-Json -ErrorAction Stop).passed -eq $true) } catch { $errors.Add("final-validation.json is not valid JSON: $($_.Exception.Message)") }
    }
    if ($finalPassed) { $gate = 'final-validation' }
    else {
        $lintPassed = $false
        if (Test-Path -LiteralPath $specLintPath -PathType Leaf) {
            try { $lintPassed = (([IO.File]::ReadAllText($specLintPath) | ConvertFrom-Json -ErrorAction Stop).passed -eq $true) } catch { $errors.Add("spec-lint.json is not valid JSON: $($_.Exception.Message)") }
        }
        if ($lintPassed -and $liveComplexity -ceq 'S' -and $liveRisk -in @('low', 'medium')) { $gate = 'lint' }
        else { $errors.Add("Final estimate is impossible: the change has no passed final-validation.json, and the lint-only path applies only to S changes with low/medium risk (live classification: $liveComplexity/$liveRisk).") }
    }
}
else { $errors.Add('spec.md does not contain a single Complexity/Risk classification.') }

# Freshness: hashes bind the estimate to the exact reviewed inputs; null equals
# only null, and a file appearing where null was recorded is a change too.
$stale = $false
$staleReasons = @()
if ($null -ne $estimate -and $schemaValid) {
    $liveSpecSha = Get-LiveSha256 $specPath
    $liveDesignSha = Get-LiveSha256 (Join-Path $changeRoot 'design.md')
    $liveOriginalSha = Get-LiveSha256 (Join-Path $changeRoot 'original-task.md')
    foreach ($pair in @(
        @{ Name = 'spec_sha256'; Recorded = $estimate.inputs.spec_sha256; Live = $liveSpecSha },
        @{ Name = 'design_sha256'; Recorded = $estimate.inputs.design_sha256; Live = $liveDesignSha },
        @{ Name = 'original_task_sha256'; Recorded = $estimate.inputs.original_task_sha256; Live = $liveOriginalSha }
    )) {
        $recorded = if ($null -ne $pair.Recorded) { [string]$pair.Recorded } else { $null }
        $live = $pair.Live
        if ($recorded -cne $live) {
            $stale = $true
            $recordedText = if ($null -ne $recorded) { $recorded.Substring(0, 12) } else { 'null' }
            $liveText = if ($null -ne $live) { $live.Substring(0, 12) } else { 'null' }
            $staleReasons += ("STALE: {0} recorded {1} but live input is {2}." -f $pair.Name, $recordedText, $liveText)
        }
    }
    foreach ($reason in $staleReasons) { $errors.Add($reason) }

    # estimate.md carries the same hashes and the generation date (spec p.10).
    if (-not (Test-Path -LiteralPath $estimateMdPath -PathType Leaf)) { $errors.Add('estimate.md not found beside estimate.json.') }
    else {
        $mdText = [IO.File]::ReadAllText($estimateMdPath, [Text.UTF8Encoding]::new($false))
        if ($null -ne $estimate.inputs.spec_sha256 -and $mdText -notmatch [regex]::Escape([string]$estimate.inputs.spec_sha256)) { $errors.Add('estimate.md does not embed the recorded spec_sha256.') }
        foreach ($hashField in @('design_sha256', 'original_task_sha256')) {
            $recorded = $estimate.inputs.$hashField
            if ($null -ne $recorded -and $mdText -notmatch [regex]::Escape([string]$recorded)) { $errors.Add("estimate.md does not embed the recorded $hashField.") }
        }
        # ConvertFrom-Json turns ISO date strings into DateTime; the md embed
        # check needs the verbatim JSON string, so extract it from raw text.
        $createdVerbatim = [regex]::Match($estimateRaw, '"created"\s*:\s*"([^"]+)"').Groups[1].Value
        if ($createdVerbatim -and $mdText -notmatch [regex]::Escape($createdVerbatim)) { $warnings.Add('estimate.md does not embed the estimate.json creation timestamp verbatim.') }
    }

    # Identity and classification binding.
    $changeLeaf = [IO.Path]::GetFileName($changeRoot)
    if ([string]$estimate.change -cne $changeLeaf) { $errors.Add("estimate.json change identity '$($estimate.change)' does not match the change directory '$changeLeaf'.") }
    if ($liveComplexity -and [string]$estimate.classification.complexity -cne $liveComplexity) { $errors.Add("estimate classification complexity does not match spec.md ($($estimate.classification.complexity) vs $liveComplexity).") }
    if ($liveRisk -and [string]$estimate.classification.risk -cne $liveRisk) { $errors.Add("estimate classification risk does not match spec.md ($($estimate.classification.risk) vs $liveRisk).") }
    if ([int]$estimate.ai_basis.attempts.min -gt [int]$estimate.ai_basis.attempts.max) { $errors.Add('ai_basis.attempts min exceeds max.') }

    $blocks = @()
    if ($estimate.PSObject.Properties['blocks']) { $blocks = @($estimate.blocks) }

    foreach ($block in $blocks) {
        Test-ForkPair $block.human ("block $($block.id) human") 0.5
        Test-ForkPair $block.ai ("block $($block.id) ai") 0.25
    }
    $blockIds = @($blocks | ForEach-Object { [string]$_.id })
    if (@($blockIds | Select-Object -Unique).Count -ne $blockIds.Count) { $errors.Add('Block ids are not unique.') }

    if ([string]$estimate.kind -ceq 'estimate') {
        Test-ForkPair $estimate.totals.human 'totals.human' 0.5
        Test-ForkPair $estimate.totals.ai 'totals.ai' 0.25
        # Conservative sums: totals are exactly the sum of block boundaries.
        foreach ($sum in @(
            @{ Label = 'totals.human.min'; Total = [double]$estimate.totals.human.min; Actual = ($blocks | Measure-Object -Property { [double]$_.human.min } -Sum).Sum },
            @{ Label = 'totals.human.max'; Total = [double]$estimate.totals.human.max; Actual = ($blocks | Measure-Object -Property { [double]$_.human.max } -Sum).Sum },
            @{ Label = 'totals.ai.min'; Total = [double]$estimate.totals.ai.min; Actual = ($blocks | Measure-Object -Property { [double]$_.ai.min } -Sum).Sum },
            @{ Label = 'totals.ai.max'; Total = [double]$estimate.totals.ai.max; Actual = ($blocks | Measure-Object -Property { [double]$_.ai.max } -Sum).Sum }
        )) {
            if ([math]::Abs($sum.Total - [double]$sum.Actual) -gt 1e-6) { $errors.Add("$($sum.Label) is not the conservative sum of blocks ($($sum.Total) vs $($sum.Actual)).") }
        }
    }
    elseif ([string]$estimate.kind -ceq 'timebox') {
        Test-Step ([double]$estimate.timebox_hours) 0.5 'timebox_hours'
    }

    # Anchor divergence: >30% on any total boundary requires an explicit
    # explanation section in estimate.md (spec p.7). The section must name
    # every divergent boundary with its computed percent, and any percent it
    # states must match the computed value: a section that waved "the rest
    # are within 30%" used to pass while ai_min sat at +200%.
    $anchorsDoc = $null
    try { $anchorsDoc = Get-Content -Raw -LiteralPath $anchorsPath | ConvertFrom-Json -ErrorAction Stop }
    catch { $errors.Add("anchors.json is not readable: $($_.Exception.Message)") }
    $divergence = [ordered]@{ human_min = $null; human_max = $null; ai_min = $null; ai_max = $null }
    $anchorEffectiveAi = $null
    $anchorFlagsApplied = @()
    $divergent = $false
    if ($null -ne $anchorsDoc -and [string]$estimate.kind -ceq 'estimate') {
        $threshold = [double]$anchorsDoc.divergence_threshold_percent
        $anchor = $anchorsDoc.anchors.([string]$estimate.classification.complexity).([string]$estimate.classification.risk)
        if ($null -eq $anchor) { $errors.Add("No anchor for classification $($estimate.classification.complexity)/$($estimate.classification.risk).") }
        else {
            # Request-flag AI anchor modifiers: flagged fork drivers multiply
            # both AI anchor bounds before the divergence math. Parsing an
            # external artifact format or posting documents makes every
            # agent attempt heavier; one successful attempt can already
            # reach the unmodified upper bound.
            $aiMultiplier = 1.0
            if ($anchorsDoc.PSObject.Properties['ai_anchor_modifiers'] -and $estimate.PSObject.Properties['fork_drivers']) {
                foreach ($modifier in @($anchorsDoc.ai_anchor_modifiers.modifiers)) {
                    foreach ($flag in @($modifier.flags)) {
                        $flagPattern = '(?i)(?<![A-Za-z0-9_])' + [regex]::Escape([string]$flag) + '(?![A-Za-z0-9_])'
                        if (@($estimate.fork_drivers | Where-Object { "$_" -match $flagPattern }).Count -gt 0) {
                            $aiMultiplier *= [double]$modifier.ai_multiplier
                            $anchorFlagsApplied += [string]$flag
                            break
                        }
                    }
                }
            }
            if ($anchorFlagsApplied.Count -gt 0) {
                $anchorEffectiveAi = [ordered]@{
                    min = [math]::Round([double]$anchor.ai.min * $aiMultiplier, 2)
                    max = [math]::Round([double]$anchor.ai.max * $aiMultiplier, 2)
                    multiplier = [math]::Round($aiMultiplier, 2)
                }
            }
            $effectiveAiMin = [double]$anchor.ai.min * $aiMultiplier
            $effectiveAiMax = [double]$anchor.ai.max * $aiMultiplier
            $boundaryPatterns = @{
                human_min = '(?i)\bhuman[\s_-]?min\b'
                human_max = '(?i)\bhuman[\s_-]?max\b'
                ai_min = '(?i)\bai[\s_-]?min\b'
                ai_max = '(?i)\bai[\s_-]?max\b'
            }
            $boundaries = @(
                @{ Key = 'human_min'; Total = [double]$estimate.totals.human.min; Anchor = [double]$anchor.human.min },
                @{ Key = 'human_max'; Total = [double]$estimate.totals.human.max; Anchor = [double]$anchor.human.max },
                @{ Key = 'ai_min'; Total = [double]$estimate.totals.ai.min; Anchor = $effectiveAiMin },
                @{ Key = 'ai_max'; Total = [double]$estimate.totals.ai.max; Anchor = $effectiveAiMax }
            )
            foreach ($boundary in $boundaries) {
                $percent = [math]::Round([math]::Abs($boundary.Total - $boundary.Anchor) / $boundary.Anchor * 100, 1)
                $divergence[$boundary.Key] = $percent
                $boundary.Percent = $percent
                $boundary.Divergent = ($percent -gt $threshold)
                if ($percent -gt $threshold) { $divergent = $true }
            }
            if ($divergent) {
                $sectionBody = $null
                if (Test-Path -LiteralPath $estimateMdPath -PathType Leaf) {
                    $mdAll = [IO.File]::ReadAllText($estimateMdPath, [Text.UTF8Encoding]::new($false))
                    $sectionMatch = [regex]::Match($mdAll, '(?ims)^##\s+Расхождение с якорем\s*$\s*(.*?)(?=^##\s|\z)')
                    if ($sectionMatch.Success) { $sectionBody = $sectionMatch.Groups[1].Value }
                }
                if ($null -eq $sectionBody) {
                    $errors.Add('The total fork diverges from the anchor by more than 30% on at least one boundary, but estimate.md has no "## Расхождение с якорем" explanation section.')
                }
                else {
                    foreach ($boundary in $boundaries) {
                        $statedLines = @($sectionBody -split "`r?`n" | Where-Object { $_ -match $boundaryPatterns[$boundary.Key] })
                        if ($statedLines.Count -eq 0) {
                            if ($boundary.Divergent) {
                                $errors.Add("The anchor divergence section does not address the $($boundary.Key) boundary (computed $($boundary.Percent)% from the anchor).")
                            }
                            continue
                        }
                        $statedNumbers = @()
                        foreach ($line in $statedLines) {
                            foreach ($numberMatch in [regex]::Matches($line, '[+-]?\d+(?:[.,]\d+)?')) {
                                $statedNumbers += [double]($numberMatch.Value -replace ',', '.')
                            }
                        }
                        if ($statedNumbers.Count -gt 0) {
                            if (-not (@($statedNumbers | Where-Object { [math]::Abs($_ - [double]$boundary.Percent) -le 0.5 }).Count -gt 0)) {
                                $statedText = ($statedNumbers | ForEach-Object { "$_" }) -join ', '
                                $errors.Add("The anchor divergence section states $($boundary.Key) as $statedText percent, but the validator computed $($boundary.Percent)%.")
                            }
                        }
                        elseif ($boundary.Divergent) {
                            $errors.Add("The anchor divergence section names the $($boundary.Key) boundary without its divergence percent (computed $($boundary.Percent)%).")
                        }
                    }
                }
            }
        }
    }
}
elseif ($null -ne $estimate -and -not $schemaValid) {
    $warnings.Add('Structural checks skipped because the document failed the closed schema.')
}

$result = [ordered]@{
    schema_version = 1
    checked_at_utc = [DateTime]::UtcNow.ToString('o')
    change = [IO.Path]::GetFileName($changeRoot)
    kind = if ($null -ne $estimate) { [string]$estimate.kind } else { $null }
    gate = $gate
    stale = $stale
    anchor_divergence_percent = $divergence
    anchor_effective_ai = $anchorEffectiveAi
    anchor_flags_applied = @($anchorFlagsApplied)
    passed = ($errors.Count -eq 0)
    errors = @($errors)
    warnings = @($warnings)
    stats = [ordered]@{
        blocks = if ($null -ne $estimate -and $estimate.PSObject.Properties['blocks']) { @($estimate.blocks).Count } else { 0 }
        exclusions = if ($null -ne $estimate -and $estimate.PSObject.Properties['exclusions']) { @($estimate.exclusions).Count } else { 0 }
    }
}
if (-not $result.passed -and -not $NoThrow) {
    throw "Estimate validation failed. Errors: $($errors -join ' | ')"
}
return [pscustomobject]$result
