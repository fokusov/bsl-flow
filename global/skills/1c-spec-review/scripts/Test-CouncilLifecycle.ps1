#Requires -Version 7.0
[CmdletBinding()]
param([string]$PackageRoot)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($PackageRoot)) { $PackageRoot = Split-Path -Parent (Split-Path -Parent (Split-Path -Parent (Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)))) }
$skill = Join-Path $PackageRoot 'global\skills\1c-spec-review'
. (Join-Path $skill 'scripts\Review.Common.ps1')
. (Join-Path $skill 'scripts\Council.Engine.ps1')
. (Join-Path $skill 'scripts\Council.Validation.ps1')
. (Join-Path $skill 'scripts\Council.Fallback.ps1')
. (Join-Path $skill 'scripts\Council.Common.ps1')

$passed = 0
function Assert-True([bool]$Condition, [string]$Message) {
    if (-not $Condition) { throw "FAIL: $Message" }
    $script:passed++
    "PASS $Message"
}

# Isolated change copy so the lifecycle never mutates the real spec.
$tempChange = Join-Path ([IO.Path]::GetTempPath()) ('council-lifecycle-' + [guid]::NewGuid().ToString('N'))
$tempRun = Join-Path ([IO.Path]::GetTempPath()) ('council-lifecycle-run-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $tempChange -Force | Out-Null
$src = Join-Path $PackageRoot 'openspec\changes\api-specification-council'
Copy-Item -LiteralPath (Join-Path $src 'original-task.md') -Destination (Join-Path $tempChange 'original-task.md')
Copy-Item -LiteralPath (Join-Path $src 'spec.md') -Destination (Join-Path $tempChange 'spec.md')
try {
    $policyText = Get-Content -Raw -LiteralPath (Join-Path $PackageRoot 'global\skills\1c-init-project\assets\project\bsl-flow.yaml')
    $policy = Get-BSLFlowCouncilPolicy $policyText
    Assert-True ([bool]$policy.enabled) 'lifecycle uses enabled council policy'
    $policyHash = Get-BSLFlowBytesSha256 ([System.Text.Encoding]::UTF8.GetBytes($policyText))
    $snapshot = New-BSLFlowCouncilSnapshot -ChangeDir $tempChange -EvidenceText 'inspect evidence' -PolicyHash $policyHash

    # Register one clean critic result per enabled critic role with distinct observed models.
    $models = @('model-alpha', 'model-beta', 'model-gamma')
    $index = 0
    $envelopes = @()
    foreach ($roleName in @('intent_critic', 'architecture_critic', 'executability_critic')) {
        $binding = [pscustomobject][ordered]@{
            provider = 'deepseek'; model = 'deepseek-flash'; effort = 'medium'; protocol = 'openai_compatible'
            endpoint = [pscustomobject][ordered]@{ scheme = 'https'; host = 'api.deepseek.com'; port = 443; base_path = '/' }
            transport_capability_version = 1; prompt_version = 'council-prompt-v1'; member_schema_version = 1
            input_hashes = [pscustomobject][ordered]@{ original_task_sha256 = $snapshot.original_task_sha256; spec_sha256 = $snapshot.spec_sha256; design_sha256 = $snapshot.design_sha256; evidence_sha256 = $snapshot.evidence_sha256; policy_hash = $snapshot.policy_hash; rubric_sha256 = ('c' * 64) }
        }
        $attempt = New-BSLFlowCouncilAttempt -RunRoot $tempRun -Role $roleName -Binding $binding
        $payload = [pscustomobject][ordered]@{
            role = $roleName; verdict = 'PASS'; findings = @(); do_not_change = @('keep scope')
            needs_input_questions = @()
        }
        $observed = [pscustomobject][ordered]@{ provider = 'deepseek'; model = $models[$index]; effort = 'medium' }
        $envelopes += Register-BSLFlowCouncilMemberResult -RunRoot $tempRun -Attempt $attempt -Payload $payload -Status 'completed' -Summary 'lifecycle critic result' -Observed $observed -ExecutionMode 'direct_api'
        $index++
    }
    # Replay: identical binding reuses the attempt without a new dispatch.
    $replayBinding = [pscustomobject][ordered]@{
        provider = 'deepseek'; model = 'deepseek-flash'; effort = 'medium'; protocol = 'openai_compatible'
        endpoint = [pscustomobject][ordered]@{ scheme = 'https'; host = 'api.deepseek.com'; port = 443; base_path = '/' }
        transport_capability_version = 1; prompt_version = 'council-prompt-v1'; member_schema_version = 1
        input_hashes = [pscustomobject][ordered]@{ original_task_sha256 = $snapshot.original_task_sha256; spec_sha256 = $snapshot.spec_sha256; design_sha256 = $snapshot.design_sha256; evidence_sha256 = $snapshot.evidence_sha256; policy_hash = $snapshot.policy_hash; rubric_sha256 = ('c' * 64) }
    }
    $replay = New-BSLFlowCouncilAttempt -RunRoot $tempRun -Role 'intent_critic' -Binding $replayBinding
    Assert-True ($replay.binding_sha256 -eq (Get-BSLFlowCouncilLatestAttempt -RunRoot $tempRun -Role 'intent_critic').binding_sha256) 'replay reuses ready attempt on identical hashes'

    $readiness = Get-BSLFlowCouncilReadiness -PolicyRoles $policy.roles -RunRoot $tempRun
    Assert-True ([bool]$readiness.chair_allowed) 'chair allowed once all required critics resolve'

    $diversity = Get-BSLFlowCouncilDiversity $envelopes
    Assert-True ($diversity.diversity -eq 'multi_model' -and -not $diversity.fallback_visible) 'three distinct models give multi_model'

    # Chair with zero findings reconciles every requirement; gate passes on unchanged inputs.
    $reqRefs = @()
    foreach ($r in @($snapshot.manifest.requirements)) { $reqRefs += [pscustomobject][ordered]@{ id = [string]$r.id; final_refs = @('Требуемое поведение / 1') } }
    $finalSpecText = [System.IO.File]::ReadAllText((Join-Path $tempChange 'spec.md'), [System.Text.UTF8Encoding]::new($false))
    $review = [pscustomobject][ordered]@{
        schema_version = 2; reviewed_at_utc = '2026-09-11T00:00:00Z'; council_schema_version = 1
        verdict = 'PASS'; diversity = 'multi_model'; fallback_visible = $false
        inputs = [pscustomobject][ordered]@{
            original_task_sha256 = $snapshot.original_task_sha256; spec_sha256 = $snapshot.spec_sha256
            design_sha256 = $snapshot.design_sha256; policy_hash = $policyHash
        }
        manifest = $snapshot.manifest
        members = @(
            @($envelopes | ForEach-Object {
                [pscustomobject][ordered]@{
                    schema_version = 1; role = $_.role; attempt_id = $_.attempt_id; status = $_.status; summary = $_.summary
                    requested = $_.requested; observed = $_.observed
                    execution_mode = $_.execution_mode; fallback_reason = $_.fallback_reason
                    input_hashes = $_.input_hashes; payload_sha256 = $_.payload_sha256
                    usage = $_.usage; cost_state = $_.cost_state
                    dispatched_at_utc = $_.dispatched_at_utc; completed_at_utc = $_.completed_at_utc
                }
            }) + @(
                [pscustomobject][ordered]@{
                    schema_version = 1; role = 'chair'; attempt_id = ('c' * 32); status = 'completed'
                    summary = 'lifecycle chair result'
                    requested = [pscustomobject][ordered]@{ provider = 'deepseek'; model = 'deepseek-flash'; effort = 'medium' }
                    observed = [pscustomobject][ordered]@{ provider = 'deepseek'; model = 'model-gamma'; effort = 'medium' }
                    execution_mode = 'direct_api'; fallback_reason = $null
                    input_hashes = [pscustomobject][ordered]@{ original_task_sha256 = $snapshot.original_task_sha256; spec_sha256 = $snapshot.spec_sha256; design_sha256 = $snapshot.design_sha256; evidence_sha256 = $snapshot.evidence_sha256; policy_hash = $snapshot.policy_hash; rubric_sha256 = ('c' * 64); member_aggregate_sha256 = ('b' * 64) }
                    payload_sha256 = ('e' * 64)
                    usage = $null; cost_state = 'no_usage_reported'
                    dispatched_at_utc = '2026-09-11T00:00:00Z'; completed_at_utc = '2026-09-11T00:01:00Z'
                }
            )
        )
        findings = @(); protected = @(); questions = @()
        chair = [pscustomobject][ordered]@{ verdict = 'PASS'; decisions = @(); protected_decisions = @(); requirement_refs = @($reqRefs); final_spec_text = $finalSpecText; final_design_text = $null }
        reconciliation = [pscustomobject][ordered]@{
            review_sha256 = ('0' * 64); draft_spec_sha256 = $snapshot.spec_sha256; final_spec_sha256 = $snapshot.spec_sha256
            draft_design_sha256 = $snapshot.design_sha256; final_design_sha256 = $snapshot.design_sha256
        }
        gate = [pscustomobject][ordered]@{ structural_only = $true; passed = $true }
    }
    $review.reconciliation.review_sha256 = Get-BSLFlowCouncilReviewDigest $review
    $lint = & (Join-Path $skill 'scripts\Test-1CSpec.ps1') -ChangePath $tempChange -NoThrow
    Assert-True ([bool]$lint.passed) 'lifecycle spec lint passes'
    $gate = Test-BSLFlowCouncilFinalGate -Review $review -OriginalTaskPath (Join-Path $tempChange 'original-task.md') -SpecPath (Join-Path $tempChange 'spec.md') -Lint $lint
    Assert-True ([bool]$gate.passed) 'clean offline lifecycle passes the deterministic gate'

    # Unknown terminal state never auto-retries into PASS.
    $unknownMembers = @($review.members)
    $unknownMembers[0] = ($unknownMembers[0] | ConvertTo-Json -Depth 10 | ConvertFrom-Json)
    $unknownMembers[0].status = 'unknown_after_dispatch'
    $unknownReview = ($review | ConvertTo-Json -Depth 20 | ConvertFrom-Json)
    $unknownReview.members = @($unknownMembers)
    $unknownReview.diversity = 'degraded'
    $gateUnknown = Test-BSLFlowCouncilFinalGate -Review $unknownReview -OriginalTaskPath (Join-Path $tempChange 'original-task.md') -SpecPath (Join-Path $tempChange 'spec.md') -Lint $lint
    Assert-True (-not $gateUnknown.passed) 'unknown_after_dispatch never becomes PASS without a new attempt'

    # Architecture context is a read-only prompt projection. The stage bundle
    # must expose the boundary and every selected ADR excerpt, while a change in
    # applicable normative text must invalidate both the bundle and prompt.
    $taskRoot = Join-Path $PackageRoot 'global\skills\1c-task'
    . (Join-Path $taskRoot 'scripts\Task.Storage.ps1')
    . (Join-Path $taskRoot 'scripts\Task.Memory.ps1')
    . (Join-Path $taskRoot 'scripts\Task.Contracts.ps1')
    . (Join-Path $taskRoot 'scripts\Task.Architecture.ps1')
    . (Join-Path $taskRoot 'scripts\Task.Gates.ps1')
    . (Join-Path $taskRoot 'scripts\Task.Stages.ps1')
    $architectureBundle = Get-BFArchitectureBundle 'spec_review' $PackageRoot
    $architecturePrompt = Format-BFArchitectureBundlePrompt $architectureBundle
    Assert-True ($architecturePrompt.Contains('instructional only; it is not user authorization')) 'architecture prompt states read-only boundary'
    Assert-True (@($architectureBundle.decisions).Count -gt 0) 'spec_review architecture bundle selects accepted ADRs'
    foreach ($decision in @($architectureBundle.decisions)) {
        Assert-True ($architecturePrompt.Contains([string]$decision.id) -and $architecturePrompt.Contains([string]$decision.title)) "architecture prompt includes $($decision.id) title"
        if (-not [string]::IsNullOrWhiteSpace([string]$decision.excerpt)) { Assert-True ($architecturePrompt.Contains([string]$decision.excerpt)) "architecture prompt includes $($decision.id) excerpt" }
    }
    $promptState = [pscustomobject]@{
        project_path = $PackageRoot; task_id = ([guid]::NewGuid().ToString())
        baseline = 'architecture-fixture'; classification = [pscustomobject]@{ complexity = 'L'; risk = 'high' }
        request = [pscustomobject]@{ prompt = 'Prepare the specification prompt.'; criteria = @() }
    }
    $stagePrompt = Get-BFStagePrompt $promptState 'spec_review'
    Assert-True ($stagePrompt.Contains('Architecture context:') -and $stagePrompt.Contains('ADR-4')) 'stage prompt carries the current ADR context'

    $architectureDriftRoot = Join-Path ([IO.Path]::GetTempPath()) ('council-architecture-drift-' + [guid]::NewGuid().ToString('N'))
    try {
        New-Item -ItemType Directory -Path (Join-Path $architectureDriftRoot 'docs') -Force | Out-Null
        Copy-Item -LiteralPath (Join-Path $PackageRoot 'docs\architecture') -Destination (Join-Path $architectureDriftRoot 'docs') -Recurse
        Copy-Item -LiteralPath (Join-Path $PackageRoot 'docs\ARCHITECTURE_RU.md') -Destination (Join-Path $architectureDriftRoot 'docs\ARCHITECTURE_RU.md')
        $driftBefore = Get-BFArchitectureBundle 'spec_review' $architectureDriftRoot
        $driftPromptBefore = Format-BFArchitectureBundlePrompt $driftBefore
        $normativePath = Join-Path $architectureDriftRoot 'docs\ARCHITECTURE_RU.md'
        $normativeText = [IO.File]::ReadAllText($normativePath, [Text.UTF8Encoding]::new($false))
        $oldDecision = 'Отдельный reconciler принимает или отклоняет каждый finding с evidence.'
        $newDecision = 'Тестовый ADR drift marker: reconciler принимает каждое finding с сохранённым evidence.'
        if (-not $normativeText.Contains($oldDecision)) { throw 'ADR drift fixture could not find the applicable normative sentence.' }
        [IO.File]::WriteAllText($normativePath, $normativeText.Replace($oldDecision, $newDecision), [Text.UTF8Encoding]::new($false))
        $driftAfter = Get-BFArchitectureBundle 'spec_review' $architectureDriftRoot
        $driftPromptAfter = Format-BFArchitectureBundlePrompt $driftAfter
        Assert-True ([string]$driftBefore.bundle_sha256 -cne [string]$driftAfter.bundle_sha256) 'applicable ADR drift changes architecture bundle identity'
        Assert-True ($driftPromptBefore -cne $driftPromptAfter -and $driftPromptAfter.Contains($newDecision)) 'applicable ADR drift changes the stage prompt text'
    }
    finally { Remove-Item -LiteralPath $architectureDriftRoot -Recurse -Force -ErrorAction SilentlyContinue }

    # A prepared changed-final package is a recovery record, not a dispatch plan.
    # A third live content blocks before any write; restoring the reviewed draft
    # permits one writer to publish the intended changed final and produce the
    # normal final-validation receipt.
    $recoveryProject = Join-Path ([IO.Path]::GetTempPath()) ('council-prepared-recovery-' + [guid]::NewGuid().ToString('N'))
    try {
        $recoveryChangeName = 'demo'
        $recoveryChange = Join-Path $recoveryProject ('openspec\changes\' + $recoveryChangeName)
        New-Item -ItemType Directory -Path $recoveryChange -Force | Out-Null
        Copy-Item -LiteralPath (Join-Path $tempChange 'original-task.md') -Destination (Join-Path $recoveryChange 'original-task.md')
        Copy-Item -LiteralPath (Join-Path $tempChange 'spec.md') -Destination (Join-Path $recoveryChange 'spec.md')
        $policyBytes = [Text.UTF8Encoding]::new($false).GetBytes($policyText)
        $bomPolicyBytes = [byte[]]::new($policyBytes.Length + 3)
        $bomPolicyBytes[0] = 0xEF; $bomPolicyBytes[1] = 0xBB; $bomPolicyBytes[2] = 0xBF
        [Array]::Copy($policyBytes, 0, $bomPolicyBytes, 3, $policyBytes.Length)
        [IO.File]::WriteAllBytes((Join-Path $recoveryProject 'bsl-flow.yaml'), $bomPolicyBytes)
        Assert-True ((Get-BSLFlowCouncilPolicyHash (Join-Path $recoveryProject 'bsl-flow.yaml')) -ceq $policyHash) 'council policy hash normalizes UTF-8 BOM'
        $recoveryRunRoot = Join-Path $recoveryProject '.bsl-flow\reports\spec-review\demo.council'
        $recoveryFinalText = $finalSpecText + "`n`nRecovery fixture publishes a changed chair final byte set.`n"
        $recoveryFinalBytes = [Text.UTF8Encoding]::new($false).GetBytes($recoveryFinalText)
        $recoveryReview = $review | ConvertTo-Json -Depth 30 | ConvertFrom-Json -ErrorAction Stop
        $recoveryReview.chair.final_spec_text = $recoveryFinalText
        $recoveryReview.reconciliation.final_spec_sha256 = Get-BSLFlowBytesSha256 $recoveryFinalBytes
        $recoveryReview.reconciliation.review_sha256 = ('0' * 64)
        $recoveryReview.reconciliation.review_sha256 = Get-BSLFlowCouncilReviewDigest $recoveryReview
        New-BSLFlowCouncilPreparedPackage -RunRoot $recoveryRunRoot -Review $recoveryReview -FinalSpecBytes $recoveryFinalBytes -ExistingReviewBytes $null | Out-Null

        $thirdLiveText = $finalSpecText + "`n`nUnrelated third live content must block recovery.`n"
        $thirdLiveBytes = [Text.UTF8Encoding]::new($false).GetBytes($thirdLiveText)
        [IO.File]::WriteAllBytes((Join-Path $recoveryChange 'spec.md'), $thirdLiveBytes)
        $beforeBlockedHash = Get-BSLFlowBytesSha256 ([IO.File]::ReadAllBytes((Join-Path $recoveryChange 'spec.md')))
        $blockedRecovery = $false
        $blockedRecoveryMessage = ''
        $publicReviewScript = Join-Path $PackageRoot 'global\skills\1c-spec-review\scripts\Invoke-1CSpecReview.ps1'
        $preparedFilesBeforeBlocked = @(Get-ChildItem -LiteralPath $recoveryRunRoot -Recurse -File -ErrorAction Stop | ForEach-Object { $_.FullName })
        try { & $publicReviewScript -ProjectPath $recoveryProject -ChangeName $recoveryChangeName -ForceReview -ForceReplaceReview | Out-Null }
        catch { $blockedRecoveryMessage = [string]$_.Exception.Message; $blockedRecovery = ($blockedRecoveryMessage -match 'matches neither draft nor intended final') }
        if (-not $blockedRecovery) { throw "FAIL: third live final content blocks prepared publication before write (error: $blockedRecoveryMessage)" }
        $script:passed++
        'PASS third live final content blocks prepared publication before write'
        Assert-True ((Get-BSLFlowBytesSha256 ([IO.File]::ReadAllBytes((Join-Path $recoveryChange 'spec.md')))) -ceq $beforeBlockedHash) 'blocked recovery preserves third live bytes'
        Assert-True (-not (Test-Path -LiteralPath (Join-Path $recoveryRunRoot 'publication\completed.event.json')) -and -not (Test-Path -LiteralPath (Join-Path $recoveryChange 'final-validation.json'))) 'blocked recovery records no completion or final validation'
        $preparedFilesAfterBlocked = @(Get-ChildItem -LiteralPath $recoveryRunRoot -Recurse -File -ErrorAction Stop | ForEach-Object { $_.FullName })
        Assert-True (@(Compare-Object -ReferenceObject $preparedFilesBeforeBlocked -DifferenceObject $preparedFilesAfterBlocked).Count -eq 0) 'public recovery blocks before creating a new council snapshot or dispatch'

        [IO.File]::WriteAllBytes((Join-Path $recoveryChange 'spec.md'), [Text.UTF8Encoding]::new($false).GetBytes($finalSpecText))
        $recovered = & $publicReviewScript -ProjectPath $recoveryProject -ChangeName $recoveryChangeName -ForceReview -ForceReplaceReview
        Assert-True ([bool]$recovered.Council.publication.resumed -and [string]$recovered.Route -ceq 'council') 'public prepared changed-final entry resumes from reviewed draft'
        Assert-True ((Get-BSLFlowBytesSha256 ([IO.File]::ReadAllBytes((Join-Path $recoveryChange 'spec.md')))) -ceq (Get-BSLFlowBytesSha256 $recoveryFinalBytes)) 'recovery publishes exact intended changed final bytes'
        $finalValidationPath = Join-Path $recoveryChange 'final-validation.json'
        Assert-True ((Test-Path -LiteralPath $finalValidationPath -PathType Leaf) -and [bool](Get-Content -Raw -LiteralPath $finalValidationPath | ConvertFrom-Json).passed) 'recovery writes passing final-validation receipt'
        Assert-True (Test-Path -LiteralPath (Join-Path $recoveryRunRoot 'publication\completed.event.json') -PathType Leaf) 'recovery records completion after final validation'
    }
    finally { Remove-Item -LiteralPath $recoveryProject -Recurse -Force -ErrorAction SilentlyContinue }
}
finally {
    Remove-Item -LiteralPath $tempChange -Recurse -Force -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath $tempRun -Recurse -Force -ErrorAction SilentlyContinue
}

"ALL_STAGE6_LIFECYCLE_PASSED=$passed"
