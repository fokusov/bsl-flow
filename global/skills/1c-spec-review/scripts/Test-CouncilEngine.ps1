#Requires -Version 7.0
[CmdletBinding()]
param([string]$PackageRoot)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($PackageRoot)) { $PackageRoot = Split-Path -Parent (Split-Path -Parent (Split-Path -Parent (Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)))) }
$skill = Join-Path $PackageRoot 'global\skills\1c-spec-review'
. (Join-Path $skill 'scripts\Council.Engine.ps1')
. (Join-Path $skill 'scripts\Council.Validation.ps1')
. (Join-Path $skill 'scripts\Council.Common.ps1')

$passed = 0
function Assert-True([bool]$Condition, [string]$Message) {
    if (-not $Condition) { throw "FAIL: $Message" }
    $script:passed++
    "PASS $Message"
}
function Assert-Throws([scriptblock]$Block, [string]$Message) {
    try { & $Block } catch { $script:passed++; "PASS $Message"; return }
    throw "FAIL (no throw): $Message"
}

$changeDir = Join-Path $PackageRoot 'openspec\changes\api-specification-council'
$snapshot = New-BSLFlowCouncilSnapshot -ChangeDir $changeDir -EvidenceText 'bundle' -PolicyHash ('9' * 64)
Assert-True (@($snapshot.manifest.requirements).Count -ge 10) 'snapshot manifest from council spec'
Assert-True ($snapshot.complexity -eq 'L' -and $snapshot.risk -eq 'high') 'snapshot classification L/high'

$brainView = Get-BSLFlowCouncilRoleView -Snapshot $snapshot -Role 'brainstorm'
Assert-True ($brainView.PSObject.Properties.Name -cnotcontains 'spec') 'brainstorm view has no draft spec field'
Assert-True ($snapshot.spec_text.Length -gt 100) 'draft is substantive for negative check'
Assert-True (-not $brainView.original_task.Contains($snapshot.spec_text.Substring(0, 50))) 'brainstorm view leaks no draft bytes'

$criticView = Get-BSLFlowCouncilRoleView -Snapshot $snapshot -Role 'intent_critic' -RubricText 'rubric'
Assert-True ($criticView.spec -ceq $snapshot.spec_text) 'critic view contains draft spec'
Assert-True ($criticView.rubric -eq 'rubric') 'critic view contains own rubric'

$tempProject = Join-Path ([IO.Path]::GetTempPath()) ('council-project-' + [guid]::NewGuid().ToString('N'))
$tempChange = Join-Path $tempProject 'openspec\changes\fixture'
$tempRun = Join-Path $tempProject '.bsl-flow\reports\spec-review\fixture.council'
$utf8 = [System.Text.UTF8Encoding]::new($false)
New-Item -ItemType Directory -Path $tempChange -Force | Out-Null
New-Item -ItemType Directory -Path (Split-Path $tempRun -Parent) -Force | Out-Null
[System.IO.File]::WriteAllText((Join-Path $tempProject 'bsl-flow.yaml'), "review:`n  enabled: true`n", $utf8)
try {
    $binding = [pscustomobject][ordered]@{
        provider = 'deepseek'; model = 'deepseek-flash'; effort = 'medium'; protocol = 'openai_compatible'
        endpoint = [pscustomobject][ordered]@{ scheme = 'https'; host = 'api.deepseek.com'; port = 443; base_path = '/' }
        transport_capability_version = 1; prompt_version = 'council-prompt-v1'; member_schema_version = 1
        input_hashes = [pscustomobject][ordered]@{
            original_task_sha256 = $snapshot.original_task_sha256; spec_sha256 = $snapshot.spec_sha256
            design_sha256 = $snapshot.design_sha256; evidence_sha256 = $snapshot.evidence_sha256
            policy_hash = $snapshot.policy_hash; rubric_sha256 = ('c' * 64)
        }
    }
    $a1 = New-BSLFlowCouncilAttempt -RunRoot $tempRun -Role 'intent_critic' -Binding $binding
    $a2 = New-BSLFlowCouncilAttempt -RunRoot $tempRun -Role 'intent_critic' -Binding $binding
    Assert-True ($a1.attempt_id -eq $a2.attempt_id) 'identical binding reuses attempt without new dispatch'
    $drifted = $binding | ConvertTo-Json -Depth 10 | ConvertFrom-Json
    $drifted.input_hashes.design_sha256 = ('d' * 64)
    $aDesign = New-BSLFlowCouncilAttempt -RunRoot $tempRun -Role 'intent_critic' -Binding $drifted
    Assert-True ($aDesign.attempt_id -cne $a1.attempt_id -and [int]$aDesign.sequence -eq 2) 'design drift creates a new sequenced attempt'
    $driftedEvidence = $binding | ConvertTo-Json -Depth 10 | ConvertFrom-Json
    $driftedEvidence.input_hashes.evidence_sha256 = ('e' * 64)
    $aEvidence = New-BSLFlowCouncilAttempt -RunRoot $tempRun -Role 'intent_critic' -Binding $driftedEvidence
    Assert-True ($aEvidence.attempt_id -cne $a1.attempt_id) 'evidence drift creates a new attempt'
    $driftedPolicy = $binding | ConvertTo-Json -Depth 10 | ConvertFrom-Json
    $driftedPolicy.input_hashes.policy_hash = ('f' * 64)
    $aPolicy = New-BSLFlowCouncilAttempt -RunRoot $tempRun -Role 'intent_critic' -Binding $driftedPolicy
    Assert-True ($aPolicy.attempt_id -cne $a1.attempt_id) 'policy drift creates a new attempt'
    $drifted.endpoint.port = 8443
    $a3 = New-BSLFlowCouncilAttempt -RunRoot $tempRun -Role 'intent_critic' -Binding $drifted
    Assert-True ($a3.attempt_id -cne $a1.attempt_id -and [int]$a3.sequence -eq 5) 'endpoint drift creates a new sequenced attempt'
    $latest = Get-BSLFlowCouncilLatestAttempt -RunRoot $tempRun -Role 'intent_critic'
    Assert-True ($latest.attempt_id -eq $a3.attempt_id) 'latest attempt resolves to the drifted sequence'
    $history = @(Get-ChildItem -LiteralPath (Join-Path $tempRun 'intent_critic') -File -Filter 'attempt-*.json')
    Assert-True ($history.Count -eq 5) 'retained attempt history is preserved, not overwritten'
    $evil = $binding | ConvertTo-Json -Depth 10 | ConvertFrom-Json
    $evil | Add-Member -NotePropertyName 'token' -NotePropertyValue 's' -Force
    Assert-Throws { New-BSLFlowCouncilAttempt -RunRoot $tempRun -Role 'architecture_critic' -Binding $evil } 'token in binding rejected'

    $payload = [pscustomobject][ordered]@{
        role = 'intent_critic'; verdict = 'REVISE'
        findings = @([pscustomobject][ordered]@{ id = 'F-001'; severity = 'medium'; category = 'clarity'; spec_ref = 'Требуемое поведение / 1'; issue = 'i'; evidence = 'e'; suggested_direction = 'd' })
        do_not_change = @(); needs_input_questions = @()
    }
    $observed = [pscustomobject][ordered]@{ provider = 'deepseek'; model = 'deepseek-flash'; effort = 'medium' }
    $env = Register-BSLFlowCouncilMemberResult -RunRoot $tempRun -Attempt $a1 -Payload $payload -Status 'completed' -Summary 'fixture member result' -Observed $observed -ExecutionMode 'direct_api'
    Assert-True ($env.requested.model -eq 'deepseek-flash' -and $env.payload_sha256 -match '^[a-f0-9]{64}$') 'member envelope built by controller'

    # The packaged template names symbolic profiles; bind them inline.
    $policyText = (Get-Content -Raw -LiteralPath (Join-Path $PackageRoot 'global\skills\1c-init-project\assets\project\bsl-flow.yaml')).TrimEnd() + "`nllm:`n  models:`n    review-fast:`n      provider: deepseek`n      model: deepseek-flash`n    review-strong:`n      provider: openai`n      model: gpt-6-astra`n    review-chair:`n      provider: openai`n      model: gpt-5.6-sol`n"
    $policy = Get-BSLFlowCouncilPolicy $policyText
    $readiness = Get-BSLFlowCouncilReadiness -PolicyRoles $policy.roles -RunRoot $tempRun
    Assert-True (-not $readiness.chair_allowed) 'chair blocked while required roles miss terminal results'

    # Prepared publication + resume on an isolated change copy.
    Copy-Item -LiteralPath (Join-Path $changeDir 'original-task.md') -Destination (Join-Path $tempChange 'original-task.md')
    Copy-Item -LiteralPath (Join-Path $changeDir 'spec.md') -Destination (Join-Path $tempChange 'spec.md')
    $finalSpec = $utf8.GetBytes(($utf8.GetString([System.IO.File]::ReadAllBytes((Join-Path $tempChange 'spec.md'))) + "`n"))
    $manifest = New-BSLFlowRequirementManifest ([System.IO.File]::ReadAllText((Join-Path $tempChange 'spec.md'), $utf8))
    $reqRefs = @()
    foreach ($r in @($manifest.requirements)) { $reqRefs += [pscustomobject][ordered]@{ id = [string]$r.id; final_refs = @('Требуемое поведение / 1') } }
    $finalSpecText = [System.IO.File]::ReadAllText((Join-Path $tempChange 'spec.md'), $utf8)
    $policyHash = (Get-FileHash -LiteralPath (Join-Path $tempProject 'bsl-flow.yaml') -Algorithm SHA256).Hash.ToLowerInvariant()
    $review = [pscustomobject][ordered]@{
        schema_version = 2; reviewed_at_utc = '2026-09-11T00:00:00Z'; council_schema_version = 1
        verdict = 'PASS'; diversity = 'multi_role_single_model'; fallback_visible = $true
        inputs = [pscustomobject][ordered]@{
            original_task_sha256 = (Get-FileHash -LiteralPath (Join-Path $tempChange 'original-task.md') -Algorithm SHA256).Hash.ToLowerInvariant()
            spec_sha256 = (Get-FileHash -LiteralPath (Join-Path $tempChange 'spec.md') -Algorithm SHA256).Hash.ToLowerInvariant()
            design_sha256 = $null; policy_hash = $policyHash
        }
        manifest = $manifest
        members = @(
            [pscustomobject][ordered]@{
                schema_version = 1; role = 'intent_critic'; attempt_id = ('1' * 32); status = 'completed'
                summary = 'fixture intent critic'
                requested = [pscustomobject][ordered]@{ provider = 'deepseek'; model = 'deepseek-flash'; effort = 'medium' }
                observed = [pscustomobject][ordered]@{ provider = 'deepseek'; model = 'deepseek-flash'; effort = 'medium' }
                execution_mode = 'current_agent_fallback'; fallback_reason = 'credential_missing'
                input_hashes = [pscustomobject][ordered]@{
                    original_task_sha256 = (Get-FileHash -LiteralPath (Join-Path $tempChange 'original-task.md') -Algorithm SHA256).Hash.ToLowerInvariant()
                    spec_sha256 = (Get-FileHash -LiteralPath (Join-Path $tempChange 'spec.md') -Algorithm SHA256).Hash.ToLowerInvariant()
                     design_sha256 = $null; evidence_sha256 = $null; policy_hash = $policyHash; rubric_sha256 = ('c' * 64)
                }
                payload_sha256 = ('a' * 64)
                usage = $null; cost_state = 'no_usage_reported'
                dispatched_at_utc = '2026-09-11T00:00:00Z'; completed_at_utc = '2026-09-11T00:01:00Z'
            },
            [pscustomobject][ordered]@{
                schema_version = 1; role = 'chair'; attempt_id = ('2' * 32); status = 'completed'
                summary = 'fixture chair'
                requested = [pscustomobject][ordered]@{ provider = 'openai'; model = 'gpt-5.6-sol'; effort = 'medium' }
                observed = [pscustomobject][ordered]@{ provider = 'openai'; model = 'gpt-5.6-sol'; effort = 'medium' }
                execution_mode = 'direct_api'; fallback_reason = $null
                input_hashes = [pscustomobject][ordered]@{
                    original_task_sha256 = (Get-FileHash -LiteralPath (Join-Path $tempChange 'original-task.md') -Algorithm SHA256).Hash.ToLowerInvariant()
                    spec_sha256 = (Get-FileHash -LiteralPath (Join-Path $tempChange 'spec.md') -Algorithm SHA256).Hash.ToLowerInvariant()
                     design_sha256 = $null; evidence_sha256 = $null; policy_hash = $policyHash; rubric_sha256 = ('c' * 64); member_aggregate_sha256 = ('b' * 64)
                }
                payload_sha256 = ('d' * 64)
                usage = $null; cost_state = 'no_usage_reported'
                dispatched_at_utc = '2026-09-11T00:00:00Z'; completed_at_utc = '2026-09-11T00:01:00Z'
            }
        )
        findings = @()
        protected = @()
        questions = @()
        chair = [pscustomobject][ordered]@{
            verdict = 'PASS'
            decisions = @()
            protected_decisions = @()
            requirement_refs = @($reqRefs)
            final_spec_text = $finalSpecText; final_design_text = $null
        }
        reconciliation = [pscustomobject][ordered]@{
            review_sha256 = ('b' * 64)
            draft_spec_sha256 = (Get-FileHash -LiteralPath (Join-Path $tempChange 'spec.md') -Algorithm SHA256).Hash.ToLowerInvariant()
            final_spec_sha256 = 'PENDING'
            draft_design_sha256 = $null; final_design_sha256 = $null
        }
        gate = [pscustomobject][ordered]@{ structural_only = $true; passed = $true }
    }
    # Fix final hash to the intended bytes for a consistent package.
    $sha = [System.Security.Cryptography.SHA256]::Create()
    try { $intendedHash = ([BitConverter]::ToString($sha.ComputeHash($finalSpec))).Replace('-', '').ToLowerInvariant() }
    finally { $sha.Dispose() }
    $review.reconciliation.final_spec_sha256 = $intendedHash
    $review.reconciliation.review_sha256 = Get-BSLFlowCouncilReviewDigest $review
    # The final gate requires the published bytes to be the chair final text.
    [System.IO.File]::WriteAllBytes((Join-Path $tempChange 'spec.md'), $finalSpec)
    $review = $review | ConvertTo-Json -Depth 20 | ConvertFrom-Json
    $review.chair.final_spec_text = $finalSpecText
    $review.reconciliation.review_sha256 = Get-BSLFlowCouncilReviewDigest $review
    $package = New-BSLFlowCouncilPreparedPackage -RunRoot $tempRun -Review $review -FinalSpecBytes $finalSpec
    Assert-True (Test-Path -LiteralPath (Join-Path $tempRun 'publication/prepared.event.json') -PathType Leaf) 'durable prepared event precedes live writes'
    # Recovery verifies every content-addressed prepared byte before touching the
    # live change. These cases model a crash recovery directory edited after the
    # prepare event and a third-party design file appearing in the live change.
    $preparedSpecPath = Join-Path $tempRun 'publication/intended-spec.md'
    $preparedSpecBytes = [System.IO.File]::ReadAllBytes($preparedSpecPath)
    [System.IO.File]::WriteAllText($preparedSpecPath, 'tampered prepared spec', $utf8)
    Assert-Throws { Resume-BSLFlowCouncilPublication -ChangeDir $tempChange -RunRoot $tempRun -Review $review } 'tampered prepared spec is rejected before live writes'
    [System.IO.File]::WriteAllBytes($preparedSpecPath, $preparedSpecBytes)
    $originalPath = Join-Path $tempChange 'original-task.md'
    $originalBytes = [System.IO.File]::ReadAllBytes($originalPath)
    [System.IO.File]::WriteAllText($originalPath, 'tampered original task', $utf8)
    Assert-Throws { Resume-BSLFlowCouncilPublication -ChangeDir $tempChange -RunRoot $tempRun -Review $review } 'changed original task is rejected before live writes'
    [System.IO.File]::WriteAllBytes($originalPath, $originalBytes)
    $unexpectedDesignPath = Join-Path $tempChange 'design.md'
    [System.IO.File]::WriteAllText($unexpectedDesignPath, 'unexpected design', $utf8)
    Assert-Throws { Resume-BSLFlowCouncilPublication -ChangeDir $tempChange -RunRoot $tempRun -Review $review } 'unexpected live design is rejected before live writes'
    Remove-Item -LiteralPath $unexpectedDesignPath -Force
    # Live already equals the intended final bytes -> resume completes without model calls.
    $resumed = Resume-BSLFlowCouncilPreparedPublicationIfPresent -ProjectRoot $tempProject -ChangeName 'fixture'
    Assert-True ([bool]$resumed.resumed) 'resume completes publication when live equals draft'
    Assert-True ((Get-FileHash -LiteralPath (Join-Path $tempChange 'spec.md') -Algorithm SHA256).Hash.ToLowerInvariant() -eq $intendedHash) 'live spec converged on intended bytes'
    Assert-True ([bool]$resumed.final_validation.passed -and (Test-Path -LiteralPath (Join-Path $tempChange 'final-validation.json') -PathType Leaf)) 'resume writes final-validation before completion'
    # Third content blocks.
    [System.IO.File]::WriteAllText((Join-Path $tempChange 'spec.md'), 'third content', $utf8)
    Assert-Throws { Resume-BSLFlowCouncilPublication -ChangeDir $tempChange -RunRoot $tempRun -Review $review } 'third live content gives BLOCKED without overwrite'

    # --- Chair anchors (spec-lint anchors[], id refs, legacy compatibility). ---
    function Get-ThrowMessage([scriptblock]$Block) { try { & $Block; return $null } catch { return [string]$_.Exception.Message } }
    function Copy-Review($Value) { return ($Value | ConvertTo-Json -Depth 20 | ConvertFrom-Json) }
    $anchors = @(Get-BSLFlowSpecAnchors -SpecText $finalSpecText)
    $reqAnchors = @($anchors | Where-Object { $_.id -like 'REQ-*' })
    Assert-True ($reqAnchors.Count -eq @($manifest.requirements).Count -and [string]$reqAnchors[0].id -ceq 'REQ-1') 'REQ anchors follow the requirement manifest numbering'
    Assert-True (@($anchors | Where-Object { $_.id -like 'AC-*' }).Count -ge 1 -and @($anchors | Where-Object { $_.id -like 'NG-*' }).Count -ge 1) 'acceptance criteria and non-goals items get AC/NG anchors'
    Assert-True ([string]$reqAnchors[0].section -ceq 'Требуемое поведение' -and [int]$reqAnchors[0].item -eq 1 -and [string]$reqAnchors[0].text_sha256 -match '^[a-f0-9]{64}$') 'anchor carries section, item and text hash'
    $lintDir = Join-Path $tempProject 'lint-anchors'
    New-Item -ItemType Directory -Path $lintDir -Force | Out-Null
    [System.IO.File]::WriteAllText((Join-Path $lintDir 'spec.md'), $finalSpecText, $utf8)
    $lintResult = & (Join-Path $skill 'scripts\Test-1CSpec.ps1') -ChangePath $lintDir -NoThrow
    $lintJson = Get-Content -Raw -LiteralPath (Join-Path $lintDir 'spec-lint.json') | ConvertFrom-Json
    Assert-True (@($lintJson.anchors).Count -eq $anchors.Count -and [string]$lintJson.anchors[0].id -ceq 'REQ-1' -and (@($lintJson.anchors[0].PSObject.Properties.Name) -join ',') -ceq 'id,section,item,text_sha256') 'spec-lint.json emits additive anchors[] {id, section, item, text_sha256}'
    Assert-True ([bool]$lintResult.passed -eq [bool]$lintJson.passed) 'anchors do not change the lint verdict'

    $req6 = @($manifest.requirements)[5].id
    # Regression (execution-contract-v01 review-blocked.md): an invented heading
    # anchor must be rejected with an actionable message naming the id space.
    $invented = Copy-Review $review
    @($invented.chair.requirement_refs | Where-Object { $_.id -ceq $req6 })[0].final_refs = @('Форматы файлов / Evidence/T-NNN.json')
    $inventedError = Get-ThrowMessage { Assert-BSLFlowCouncilReview $invented }
    Assert-True ($inventedError -match [regex]::Escape("$req6 -> Форматы файлов / Evidence/T-NNN.json") -and $inventedError -match 'anchor ids of the final specification \(available: REQ-1\.\.REQ-[0-9]+, AC-1') 'invented anchor REQ-006 -> Форматы файлов / Evidence/T-NNN.json is rejected with the available anchor ids'
    $idBased = Copy-Review $review
    $index = 0
    foreach ($ref in @($idBased.chair.requirement_refs)) { $index++; $ref.final_refs = @("REQ-$index") }
    @($idBased.chair.requirement_refs | Where-Object { $_.id -ceq $req6 })[0].final_refs = @('REQ-6', 'AC-1', 'REQ-006')
    $idBased.reconciliation.review_sha256 = Get-BSLFlowCouncilReviewDigest $idBased
    Assert-True ($null -eq (Get-ThrowMessage { Assert-BSLFlowCouncilReview $idBased })) 'id-based refs (REQ-N, AC-N, zero-padded manifest spelling) resolve against the final specification'
    $missingId = Copy-Review $idBased
    @($missingId.chair.requirement_refs)[0].final_refs = @('REQ-999')
    Assert-True ((Get-ThrowMessage { Assert-BSLFlowCouncilReview $missingId }) -match 'REQ-999; refs must be anchor ids') 'a non-existent anchor id never falls back to substring matching'
    $verbatim = Copy-Review $review
    @($verbatim.chair.requirement_refs)[0].final_refs = @([string]$reqAnchors[0].text)
    Assert-True ($null -eq (Get-ThrowMessage { Assert-BSLFlowCouncilReview $verbatim })) 'legacy verbatim and "Требуемое поведение / N" refs of published reviews stay valid'
    . (Join-Path $skill 'scripts\Invoke-CouncilReview.ps1')
    $chairSchema = Get-BSLFlowCouncilRoleOutputSchema -Role 'chair' -AnchorIds @($anchors | ForEach-Object { $_.id }) -RequirementIds @($manifest.requirements | ForEach-Object { $_.id })
    $finalRefEnum = @($chairSchema.properties.requirement_refs.items.properties.final_refs.items.enum)
    Assert-True ($finalRefEnum.Count -eq $anchors.Count -and 'REQ-6' -cin $finalRefEnum -and 'Форматы файлов / Evidence/T-NNN.json' -cnotin $finalRefEnum) 'structured chair schema pins final_refs to the actual anchor ids'
    Assert-True (@($chairSchema.properties.decisions.items.properties.resolution_refs.items.enum).Count -eq $anchors.Count -and [int]$chairSchema.properties.decisions.maxItems -eq 0) 'structured chair schema pins resolution_refs and allows no decisions without findings'
    $chairPrompt = New-BSLFlowCouncilPrompt -Role 'chair' -View ([pscustomobject]@{ original_task = 'o'; spec = $finalSpecText; anchors = $anchors; aggregates = '{}' })
    Assert-True ($chairPrompt -match '(?m)^REQ-1 \[Требуемое поведение #1\]: ' -and $chairPrompt -match 'MUST be exactly one anchor id') 'chair prompt lists the controller-computed anchors and demands id refs'

    # --- Council limitations: PASS_WITH_LIMITATIONS never passes as a clean PASS. ---
    $limited = Copy-Review $review
    $limited.verdict = 'PASS_WITH_LIMITATIONS'
    $limited | Add-Member -NotePropertyName limitations -NotePropertyValue @('single_model_council')
    Assert-True ($null -eq (Get-ThrowMessage { Assert-BSLFlowCouncilReview $limited })) 'PASS_WITH_LIMITATIONS with single_model_council is a valid v2 review'
    $cleanWithLimit = Copy-Review $limited
    $cleanWithLimit.verdict = 'PASS'
    Assert-True ((Get-ThrowMessage { Assert-BSLFlowCouncilReview $cleanWithLimit }) -match 'cannot be a clean PASS') 'limitations forbid a clean PASS'
    $noLimit = Copy-Review $review
    $noLimit.verdict = 'PASS_WITH_LIMITATIONS'
    Assert-True ((Get-ThrowMessage { Assert-BSLFlowCouncilReview $noLimit }) -match 'requires review.limitations') 'PASS_WITH_LIMITATIONS needs recorded limitations'
    $upgraded = Copy-Review $limited
    $upgraded.chair.verdict = 'REVISE'
    Assert-True ((Get-ThrowMessage { Assert-BSLFlowCouncilReview $upgraded }) -match 'cannot exceed the chair verdict') 'PASS_WITH_LIMITATIONS cannot upgrade a non-PASS chair'
    $emptyLimit = Copy-Review $limited
    $emptyLimit.limitations = @()
    Assert-True ((Get-ThrowMessage { Assert-BSLFlowCouncilReview $emptyLimit }) -match 'omitted when empty') 'empty limitations are rejected'
    $unknownLimit = Copy-Review $limited
    $unknownLimit.limitations = @('trust_me')
    Assert-True ((Get-ThrowMessage { Assert-BSLFlowCouncilReview $unknownLimit }) -match 'Unknown review limitation') 'unknown limitations are rejected'
    $degradedLimited = Copy-Review $limited
    $degradedLimited.diversity = 'degraded'
    $gateLint = [pscustomobject]@{ passed = $true }
    $degradedGate = Test-BSLFlowCouncilFinalGate -Review $degradedLimited -OriginalTaskPath (Join-Path $tempChange 'original-task.md') -SpecPath (Join-Path $tempChange 'spec.md') -Lint $gateLint
    Assert-True ('Degraded council cannot PASS.' -cin @($degradedGate.errors)) 'final gate treats PASS_WITH_LIMITATIONS as a PASS-family verdict'
    $limitedGate = Test-BSLFlowCouncilFinalGate -Review $limited -OriginalTaskPath (Join-Path $tempChange 'original-task.md') -SpecPath (Join-Path $tempChange 'spec.md') -Lint $gateLint
    Assert-True (@($limitedGate.errors | Where-Object { $_ -match 'non-PASS council review must contain' }).Count -eq 0) 'a limited PASS without findings needs no reconciled decision'
}
finally {
    Remove-Item -LiteralPath $tempProject -Recurse -Force -ErrorAction SilentlyContinue
}

"ALL_STAGE2_ENGINE_PASSED=$passed"
