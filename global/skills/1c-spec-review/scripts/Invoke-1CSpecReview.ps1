#Requires -Version 7.0
[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$ProjectPath,
    [Parameter(Mandatory)][string]$ChangeName,
    [ValidateSet('S', 'M', 'L')][string]$Complexity,
    [ValidateSet('low', 'medium', 'high')][string]$Risk,
    [string]$Model,
    [string]$Variant,
    [switch]$ForceReview,
    [switch]$ForceReplaceReview,
    [int]$TimeoutSeconds = 0,
    [int]$MaxOutputBytes = 0,
    [string]$OpenCodePath,
    [string]$ManagedCodexPath,
    [string]$EvidenceText = '',
    [Alias('HostStatePath')]
    [string]$ManagedStatePath,
    [string]$ImportRaw,
    [string]$AuthorModel,
    [string]$ClaudeCliPath,
    [string]$CodexCliPath
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'Review.Common.ps1')
. (Join-Path $PSScriptRoot 'Review.Providers.ps1')

function Get-SpecClassification {
    param([Parameter(Mandatory)][string]$SpecText)
    $complexityMatches = [regex]::Matches($SpecText, '(?im)^\s*-\s*(?:Сложность|Complexity):\s*(S|M|L)\s*$')
    $riskMatches = [regex]::Matches($SpecText, '(?im)^\s*-\s*(?:Риск|Risk):\s*(low|medium|high)\s*$')
    if ($complexityMatches.Count -ne 1) { throw 'spec.md must contain exactly one complexity classification.' }
    if ($riskMatches.Count -ne 1) { throw 'spec.md must contain exactly one risk classification.' }
    return [pscustomobject]@{
        Complexity = $complexityMatches[0].Groups[1].Value.ToUpperInvariant()
        Risk = $riskMatches[0].Groups[1].Value.ToLowerInvariant()
    }
}

function Import-PublicManagedCouncilAdapter {
    param(
        [Parameter(Mandatory)][string]$StatePath,
        [Parameter(Mandatory)][string]$ProjectRoot,
        [Parameter(Mandatory)][string]$ChangeRoot,
        [string]$CodexPath,
        [int]$MaxBytes = 262144
    )
    $taskSkillRoot = Join-Path (Split-Path (Split-Path $PSScriptRoot -Parent) -Parent) '1c-task'
    foreach ($module in @(
        'scripts/Task.Storage.ps1', 'scripts/Task.Contracts.ps1', 'scripts/Task.Memory.ps1',
        'scripts/Task.Architecture.ps1', 'scripts/Task.Gates.ps1', 'scripts/Task.Process.ps1',
        'scripts/Task.Engine.ps1', 'scripts/Task.Toolsets.ps1', 'scripts/Task.Execution.ps1',
        'scripts/Task.Stages.ps1', 'scripts/Task.ManagedReview.ps1',
        'adapters/Codex.ps1', 'adapters/Codex.Skills.ps1', 'adapters/ProfiledCodex.ps1'
    )) {
        . (Join-Path $taskSkillRoot $module)
    }
    $resolvedStatePath = Assert-BFSafePath $StatePath
    if (-not (Test-Path -LiteralPath $resolvedStatePath -PathType Leaf)) { throw "Managed host state not found: $resolvedStatePath" }
    $taskRoot = (Assert-BFSafePath (Join-Path $ProjectRoot '.bsl-flow/tasks')).TrimEnd('\', '/') + '\'
    if (-not $resolvedStatePath.StartsWith($taskRoot, [StringComparison]::OrdinalIgnoreCase)) {
        throw 'BF_INVALID: managed host state must be an existing registered task state under .bsl-flow/tasks.'
    }
    $managedState = Read-BFJson $resolvedStatePath
    Assert-BFState $managedState
    if ((Assert-BFSafePath ([string]$managedState.project_path)) -ine $ProjectRoot) { throw 'BF_BLOCKED: managed host state belongs to another project.' }
    if ((Assert-BFSafePath (Get-BFChangePath $managedState)) -ine $ChangeRoot) {
        throw 'BF_BLOCKED: managed host state does not identify the requested OpenSpec change.'
    }
    $adapterDirectory = Join-Path (Get-BFTaskDirectory $ProjectRoot ([string]$managedState.task_id)) 'managed-council/public'
    $adapter = New-BFManagedCouncilHostAdapter -State $managedState -Directory $adapterDirectory -CodexPath $CodexPath
    return [pscustomobject][ordered]@{
        state = $managedState
        capabilities = $adapter.capabilities
        fallback_runner = $adapter.fallback_runner
        fallback_roles = @($adapter.fallback_roles)
        evidence_text = Get-BFManagedCouncilEvidence $managedState $MaxBytes
    }
}

$projectRoot = [System.IO.Path]::GetFullPath($ProjectPath).TrimEnd('\', '/')
if (-not (Test-Path -LiteralPath $projectRoot -PathType Container)) { throw "Project not found: $projectRoot" }
if ($ChangeName -notmatch '^[A-Za-z0-9][A-Za-z0-9._-]*$') { throw "Unsafe OpenSpec change name: $ChangeName" }
$changeRoot = Join-Path $projectRoot "openspec\changes\$ChangeName"
$specPath = Join-Path $changeRoot 'spec.md'
$originalTaskPath = Join-Path $changeRoot 'original-task.md'
$designPath = Join-Path $changeRoot 'design.md'
$reviewPath = Join-Path $changeRoot 'review.json'
$configPath = Join-Path $projectRoot 'bsl-flow.yaml'

if (-not (Test-Path -LiteralPath $specPath -PathType Leaf)) { throw "spec.md not found: $specPath" }
$specText = [IO.File]::ReadAllText($specPath, (New-Object Text.UTF8Encoding($false)))
$classification = Get-SpecClassification -SpecText $specText
if ($Complexity -and $classification.Complexity -and $Complexity -ne $classification.Complexity) { throw 'Explicit complexity conflicts with spec.md classification.' }
if ($Risk -and $classification.Risk -and $Risk -ne $classification.Risk) { throw 'Explicit risk conflicts with spec.md classification.' }
if (-not $Complexity) { $Complexity = $classification.Complexity }
if (-not $Risk) { $Risk = $classification.Risk }
if ($Complexity -notin @('S', 'M', 'L') -or $Risk -notin @('low', 'medium', 'high')) { throw 'Complexity and risk are required in spec.md or command parameters.' }

$configText = if (Test-Path -LiteralPath $configPath -PathType Leaf) { Get-Content -Raw -LiteralPath $configPath } else { '' }
$councilRouting = $null
$enabled = ConvertTo-BSLFlowBoolean (Get-BSLFlowYamlValue $configText @('review', 'enabled') 'true') 'review.enabled'
$route = if ($Risk -eq 'high') {
    Get-BSLFlowYamlValue $configText @('review', 'routing', 'high_risk_override') 'required'
} else {
    Get-BSLFlowYamlValue $configText @('review', 'routing', ($Complexity.ToLowerInvariant() + '_default')) $(if ($Complexity -eq 'S') { 'optional' } else { 'required' })
}
if ($route -notin @('required', 'optional', 'off')) { throw "Invalid review route: $route" }
$policyRequired = ($Complexity -in @('M', 'L')) -or ($Risk -eq 'high')
if ($policyRequired -and $route -ne 'required') {
    throw 'Project routing cannot weaken the mandatory M/L/high-risk review policy.'
}
$reviewRequired = $ForceReview -or ($route -eq 'required')
$reviewMode = Get-BSLFlowSpecReviewMode -Complexity $Complexity -Risk $Risk -ReviewRequired $reviewRequired

if ($reviewMode -ceq 'council' -and -not (Test-Path (Join-Path $PSScriptRoot 'Invoke-CouncilReview.ps1') -PathType Leaf)) {
    if (Test-Path (Join-Path $projectRoot ('.bsl-flow/reports/spec-review/' + $ChangeName + '.council/publication/prepared.json'))) {
        throw 'BF_BLOCKED: prepared council publication requires Managed recovery; an override cannot replace it.'
    }
    . (Join-Path $PSScriptRoot 'Review.Override.ps1')
    $ownerOverride = Get-BSLFlowOwnerOverride -ChangeRoot $changeRoot
    if ($null -eq $ownerOverride) { throw 'BF_BLOCKED: L/high-risk review requires Council (install bsl-flow-managed) or an owner override recorded in review-reconciliation.json' }
    $final = & (Join-Path $PSScriptRoot 'Test-1CSpecFinal.ps1') -ProjectPath $projectRoot -ChangeName $ChangeName
    return [pscustomobject]@{Complexity=$Complexity;Risk=$Risk;Route='owner_override';ReviewMode='owner_override';ReviewRequired=$true;LintPassed=$final.passed;ReviewPath=$null;Verdict='PASS_WITH_LIMITATIONS';Limitations=@('owner_override_without_council')}
}

if ($reviewMode -ceq 'council' -or (Get-BSLFlowYamlValue $configText @('review','reviewer','provider') 'opencode') -ceq 'api') {
    . (Join-Path $PSScriptRoot 'Review.Api.Profile.ps1')
    $councilRouting = (Get-BSLFlowCouncilEffectivePolicy -ProjectRoot $projectRoot).policy
}

# A prepared council publication is a durable recovery record. Resume it before
# lint or any route can start another model call; Resume performs the final lint
# and publication checks against the exact prepared bytes.
$councilRunRoot = Join-Path $projectRoot ('.bsl-flow/reports/spec-review/' + $ChangeName + '.council')
$councilPreparedPath = Join-Path $councilRunRoot 'publication/prepared.json'
if (Test-Path -LiteralPath $councilPreparedPath -PathType Leaf) {
    if ($reviewMode -cne 'council' -or $null -eq $councilRouting -or -not [bool]$councilRouting.enabled -or $councilRouting.legacy_mode -ceq 'opencode_compat') {
        throw 'BF_BLOCKED: prepared council publication requires the current L/high-risk council route to remain enabled.'
    }
    . (Join-Path $PSScriptRoot 'Council.Engine.ps1')
    $recovery = Resume-BSLFlowCouncilPreparedPublicationIfPresent -ProjectRoot $projectRoot -ChangeName $ChangeName
    if ($null -eq $recovery) { throw 'BF_BLOCKED: prepared council publication disappeared before recovery.' }
    return [pscustomobject]@{
        Complexity = $Complexity; Risk = $Risk; Route = 'council'; ReviewMode = 'council'; ReviewRequired = $true
        LintPassed = $true; ReviewPath = $reviewPath; Council = [ordered]@{ resumed = $true; publication = $recovery }
    }
}

$lint = & (Join-Path $PSScriptRoot 'Test-1CSpec.ps1') -ChangePath $changeRoot

if ($reviewRequired -and -not $enabled -and -not $ForceReview) {
    throw 'Review is required by routing but review.enabled is false.'
}

if (-not $reviewRequired) {
    return [pscustomobject]@{
        Complexity = $Complexity; Risk = $Risk; Route = $route; ReviewMode = 'lint'; ReviewRequired = $false
        LintPassed = [bool]$lint.passed; ReviewPath = $null
    }
}
if (-not (Test-Path -LiteralPath $originalTaskPath -PathType Leaf)) { throw "original-task.md is required for independent review: $originalTaskPath" }
if ((Test-Path -LiteralPath $reviewPath -PathType Leaf) -and -not $ForceReplaceReview) { throw "review.json already exists; refusing to overwrite evidence: $reviewPath" }

$managedAdapter = $null
if ($ManagedStatePath) {
    if ($reviewMode -cne 'council' -or $null -eq $councilRouting -or -not [bool]$councilRouting.enabled -or $councilRouting.legacy_mode -ceq 'opencode_compat') {
        throw 'BF_BLOCKED: managed host state is supported only by the enabled L/high-risk council route.'
    }
    $managedInputLimit = [int]::Parse((Get-BSLFlowYamlValue $configText @('review', 'input', 'max_file_bytes') '262144'), [Globalization.CultureInfo]::InvariantCulture)
    $managedAdapter = Import-PublicManagedCouncilAdapter -StatePath $ManagedStatePath -ProjectRoot $projectRoot -ChangeRoot $changeRoot -CodexPath $ManagedCodexPath -MaxBytes $managedInputLimit
}

$reviewPolicy = Get-BSLFlowReviewPolicy $configText
$readMode = $reviewPolicy.ReadMode

# Council is reserved for L/high-risk review. M and explicitly reviewed S use
# the packaged single-reviewer route, so enabling Council does not multiply
# ordinary review cost. L/high-risk fails closed instead of degrading to one critic.
if ($reviewMode -ceq 'council') {
    if ($null -eq $councilRouting -or -not [bool]$councilRouting.enabled -or $councilRouting.legacy_mode -ceq 'opencode_compat') {
        throw 'BF_BLOCKED: L/high-risk specification review requires an enabled API Council route.'
    }
    . (Join-Path $PSScriptRoot 'Invoke-CouncilReview.ps1')
    $councilArguments = @{
        ProjectPath = $projectRoot; ChangeName = $ChangeName
        EvidenceText = if ($null -ne $managedAdapter) { [string]$managedAdapter.evidence_text } else { $EvidenceText }
        AllowLiveDispatch = $true
    }
    if ($null -ne $managedAdapter) {
        $councilArguments.Capabilities = $managedAdapter.capabilities
        $councilArguments.FallbackRunner = $managedAdapter.fallback_runner
    }
    $councilResult = Invoke-BSLFlowCouncilReview @councilArguments
    return [pscustomobject]@{
        Complexity = $Complexity; Risk = $Risk; Route = 'council'; ReviewMode = 'council'; ReviewRequired = $true
        LintPassed = [bool]$lint.passed; ReviewPath = $reviewPath; Council = $councilResult
    }
}

$provider = Get-BSLFlowYamlValue $configText @('review', 'reviewer', 'provider') 'opencode'
if ($provider -notin @('opencode', 'claude_cli', 'codex_exec', 'api', 'claude_subagent')) { throw "Unsupported reviewer provider: $provider" }
if (-not $Model) { $Model = Get-BSLFlowYamlValue $configText @('review', 'reviewer', 'model') '' }
if ($provider -ne 'claude_subagent' -and -not $Model) {
    throw 'Reviewer model is not configured: set review.reviewer.model in bsl-flow.yaml (or ~/.bsl-flow/config.yaml).'
}
if ($provider -eq 'opencode' -and -not $Variant) { $Variant = Get-BSLFlowYamlValue $configText @('review', 'reviewer', 'variant') 'high' }
$configuredAgent = Get-BSLFlowYamlValue $configText @('review', 'reviewer', 'agent') 'bsl-flow-spec-reviewer'
$agent = if ($readMode -eq 'attached_only') { 'bsl-flow-spec-reviewer-sealed' } else { $configuredAgent }
if ($provider -eq 'opencode' -and $agent -notin @('bsl-flow-spec-reviewer', 'bsl-flow-spec-reviewer-sealed')) {
    throw "Only packaged hard-deny reviewer agents are allowed: $agent"
}

$culture = [System.Globalization.CultureInfo]::InvariantCulture
$passScore = $reviewPolicy.PassWeightedScore
$blockScore = $reviewPolicy.BlockBelowWeightedScore
$maxIndex = $reviewPolicy.MaxOverengineeringIndexForPass
$maxRatio = $reviewPolicy.MaxUnjustifiedRatioForPass
$skillRoot = Split-Path -Parent $PSScriptRoot
$reviewerConfig = Join-Path $skillRoot 'reviewer\opencode-reviewer.json'
$rubricPath = Join-Path $skillRoot 'references\reviewer-rubric.md'
$reviewSchemaPath = Join-Path $skillRoot 'references\review-schema.json'
foreach ($required in @($rubricPath)) {
    if (-not (Test-Path -LiteralPath $required -PathType Leaf)) { throw "Packaged reviewer file missing: $required" }
}
if ($provider -eq 'opencode' -and -not (Test-Path -LiteralPath $reviewerConfig -PathType Leaf)) {
    throw "Packaged reviewer file missing: $reviewerConfig"
}
$maxInputBytes = [int]::Parse((Get-BSLFlowYamlValue $configText @('review', 'input', 'max_file_bytes') '262144'), $culture)
if ($maxInputBytes -lt 1024 -or $maxInputBytes -gt 1048576) { throw 'review.input.max_file_bytes must be between 1024 and 1048576.' }
$configuredTimeout = [int]::Parse((Get-BSLFlowYamlValue $configText @('review', 'runtime', 'timeout_seconds') '600'), $culture)
$configuredOutput = [int]::Parse((Get-BSLFlowYamlValue $configText @('review', 'runtime', 'max_output_bytes') '1048576'), $culture)
if ($TimeoutSeconds -eq 0) { $TimeoutSeconds = $configuredTimeout }
if ($MaxOutputBytes -eq 0) { $MaxOutputBytes = $configuredOutput }
if ($TimeoutSeconds -lt 1 -or $TimeoutSeconds -gt 3600) { throw 'review.runtime.timeout_seconds must be between 1 and 3600.' }
if ($MaxOutputBytes -lt 65536 -or $MaxOutputBytes -gt 16777216) { throw 'review.runtime.max_output_bytes must be between 65536 and 16777216.' }

$capturedInputs = [ordered]@{
    original_task = Get-BSLFlowBoundedUtf8Snapshot -Path $originalTaskPath -MaxBytes $maxInputBytes
    spec = Get-BSLFlowBoundedUtf8Snapshot -Path $specPath -MaxBytes $maxInputBytes
    design = if (Test-Path -LiteralPath $designPath -PathType Leaf) { Get-BSLFlowBoundedUtf8Snapshot -Path $designPath -MaxBytes $maxInputBytes } else { $null }
}

# Create the durable attempt before the provider is started. Exact sent input
# bytes and their hashes remain available even when parsing or validation fails.
$runId = ([DateTime]::UtcNow.ToString('yyyyMMddTHHmmssfffZ') + '-' + [guid]::NewGuid().ToString('N'))
$artifactRoot = Join-Path $projectRoot '.bsl-flow\reports\spec-review'
$runRoot = Join-Path $artifactRoot $runId
$snapshotRoot = Join-Path $runRoot 'inputs'
New-Item -ItemType Directory -Path $snapshotRoot -Force | Out-Null
$snapshotRows = [System.Collections.Generic.List[object]]::new()
foreach ($name in @('original_task', 'spec', 'design')) {
    $snapshot = $capturedInputs[$name]
    if ($null -eq $snapshot) {
        $snapshotRows.Add([ordered]@{ name = $name; present = $false; size_bytes = 0; sha256 = $null; snapshot_path = $null })
        continue
    }
    $snapshotPath = Join-Path $snapshotRoot ($name + '.md')
    [System.IO.File]::WriteAllBytes($snapshotPath, $snapshot.Bytes)
    $snapshotRows.Add([ordered]@{ name = $name; present = $true; size_bytes = $snapshot.Bytes.Length; sha256 = $snapshot.Sha256; snapshot_path = $snapshotPath })
}
$inputSnapshotPath = Join-Path $runRoot 'input-snapshot.json'
Write-BSLFlowJsonAtomic -Value ([ordered]@{ schema_version = 1; captured_at_utc = [DateTime]::UtcNow.ToString('o'); inputs = @($snapshotRows) }) -Path $inputSnapshotPath

$blocks = [System.Collections.Generic.List[string]]::new()
foreach ($entry in @(
    @{ Label = 'ORIGINAL TASK'; Snapshot = $capturedInputs.original_task; Trusted = $false },
    @{ Label = 'DRAFT SPEC'; Snapshot = $capturedInputs.spec; Trusted = $false },
    @{ Label = 'TECHNICAL DESIGN'; Snapshot = $capturedInputs.design; Trusted = $false },
    @{ Label = 'REVIEW RUBRIC'; Snapshot = (Get-BSLFlowBoundedUtf8Snapshot -Path $rubricPath -MaxBytes $maxInputBytes); Trusted = $true }
)) {
    if ($null -eq $entry.Snapshot) { continue }
    $kind = if ($entry.Trusted) { 'TRUSTED REVIEW POLICY' } else { 'UNTRUSTED DATA' }
    $content = $entry.Snapshot.Text
    $blocks.Add("<<<BEGIN ${kind}: $($entry.Label)>>>`n$content`n<<<END ${kind}: $($entry.Label)>>>")
}
$contextEnvelope = $blocks -join "`n`n"

# Provider output is sensitive and is retained only under the project's ignored
# local reports directory. It is never copied to review.json, metrics, or errors.
$eventsPath = Join-Path $runRoot 'events.jsonl'
$rawResponsePath = Join-Path $runRoot 'raw-response.txt'
$stderrPath = Join-Path $runRoot 'provider-stderr.log'
$statusPath = Join-Path $runRoot 'status.json'
$diagnosticPath = Join-Path $runRoot 'diagnostic.json'
$startedAt = [DateTime]::UtcNow.ToString('o')
$status = [ordered]@{
    schema_version = 1; run_id = $runId; state = 'running'; phase = 'preflight'
    started_at_utc = $startedAt; updated_at_utc = $startedAt
    events_path = $eventsPath; raw_response_path = $rawResponsePath; stderr_path = $stderrPath
    diagnostic_path = $diagnosticPath; review_path = $reviewPath; input_snapshot_path = $inputSnapshotPath
}
function Update-ReviewStatus {
    param([Parameter(Mandatory)][string]$Phase, [string]$State = 'running', [string]$Message, [int]$ExitCode = -1)
    $callArgs = @{ Status = $status; StatusPath = $statusPath; Phase = $Phase; State = $State; ExitCode = $ExitCode }
    if ($PSBoundParameters.ContainsKey('Message')) { $callArgs.Message = $Message }
    Update-BSLFlowReviewStatus @callArgs
}
Update-ReviewStatus -Phase 'input'

$exitCode = -1
$failurePhase = 'launching'
$outputDrained = $true
try {
    Update-ReviewStatus -Phase 'launching'
    $providerResult = Invoke-BSLFlowSingleReviewProvider -Provider $provider -ProjectRoot $projectRoot -ChangeName $ChangeName -ContextEnvelope $contextEnvelope `
        -Agent $agent -Model $Model -Variant $Variant -ReadMode $readMode -TimeoutSeconds $TimeoutSeconds -MaxOutputBytes $MaxOutputBytes `
        -OpenCodePath $OpenCodePath -ReviewerConfigPath $reviewerConfig -ReviewSchemaPath $reviewSchemaPath -ImportRawPath $ImportRaw -AuthorModel $AuthorModel `
        -ClaudeCliPath $ClaudeCliPath -CodexCliPath $CodexCliPath `
        -CouncilRouting $councilRouting -RunRoot $runRoot -EventsPath $eventsPath -RawResponsePath $rawResponsePath -StderrPath $stderrPath
    $outputDrained = [bool]$providerResult.OutputDrained
    if ([bool]$providerResult.Failed) {
        $failurePhase = [string]$providerResult.FailurePhase
        $exitCode = [int]$providerResult.ExitCode
        throw [string]$providerResult.FailureMessage
    }
    $rawReview = $providerResult.RawReview
    $failurePhase = 'parsing'
    Update-ReviewStatus -Phase 'parsing' -ExitCode $exitCode
    $failurePhase = 'validating'
    Update-ReviewStatus -Phase 'validating' -ExitCode $exitCode
    $completionParameters = @{
        RawReview = $rawReview; OriginalTaskPath = $originalTaskPath; SpecPath = $specPath; DesignPath = $designPath
        Agent = [string]$providerResult.Agent; Model = [string]$providerResult.RequestedModel; PassWeightedScore = $passScore; BlockBelowWeightedScore = $blockScore
        MaxOverengineeringIndexForPass = $maxIndex; MaxUnjustifiedRatioForPass = $maxRatio
        OriginalTaskSha256 = $capturedInputs.original_task.Sha256; SpecSha256 = $capturedInputs.spec.Sha256
        Provider = [string]$providerResult.Provider; ObservedModel = $providerResult.ObservedModel; AuthorModel = $AuthorModel
    }
    if ($null -ne $capturedInputs.design) { $completionParameters.DesignSha256 = $capturedInputs.design.Sha256 }
    $review = Complete-BSLFlowReview @completionParameters
    $failurePhase = 'freshness'
    Update-ReviewStatus -Phase 'freshness' -ExitCode $exitCode
    foreach ($entry in @(
        @{ Name = 'original-task.md'; Path = $originalTaskPath; Snapshot = $capturedInputs.original_task },
        @{ Name = 'spec.md'; Path = $specPath; Snapshot = $capturedInputs.spec },
        @{ Name = 'design.md'; Path = $designPath; Snapshot = $capturedInputs.design }
    )) {
        $isPresent = Test-Path -LiteralPath $entry.Path -PathType Leaf
        $snapshotWasPresent = $null -ne $entry.Snapshot
        if ($snapshotWasPresent -ne $isPresent) { throw "Review input changed during provider execution: $($entry.Name)" }
        if ($isPresent -and (Get-BSLFlowSha256 $entry.Path) -ne $entry.Snapshot.Sha256) { throw "Review input changed during provider execution: $($entry.Name)" }
    }
    $failurePhase = 'publishing'
    Update-ReviewStatus -Phase 'publishing' -ExitCode $exitCode
    Write-BSLFlowJsonAtomic -Value $review -Path $reviewPath
    $published = $true
    Update-ReviewStatus -Phase 'completed' -State 'completed' -ExitCode $exitCode
}
catch {
    $failureMessage = $_.Exception.Message
    $status.message = $failureMessage
    Update-ReviewStatus -Phase $failurePhase -State 'failed' -Message $failureMessage -ExitCode $exitCode
    $diagnostic = [ordered]@{
        schema_version = 1; run_id = $runId; state = 'failed'; phase = $failurePhase
        failed_at_utc = [DateTime]::UtcNow.ToString('o'); message = $failureMessage
        exit_code = if ($exitCode -ge 0) { $exitCode } else { $null }
        events_path = $eventsPath; raw_response_path = $rawResponsePath; stderr_path = $stderrPath
        input_snapshot_path = $inputSnapshotPath; review_published = $false; output_drained = $outputDrained
    }
    Write-BSLFlowJsonAtomic -Value $diagnostic -Path $diagnosticPath
    throw $failureMessage
}

return [pscustomobject]@{
    Complexity = $Complexity; Risk = $Risk; Route = $route; ReviewMode = 'single'; ReviewRequired = $true
    LintPassed = [bool]$lint.passed; ReviewPath = $reviewPath; Verdict = $review.verdict
    ReviewerVerdict = $review.reviewer_verdict; WeightedScore = $review.weighted_score
    RunId = $runId; ArtifactsPath = $runRoot; StatusPath = $statusPath
}
