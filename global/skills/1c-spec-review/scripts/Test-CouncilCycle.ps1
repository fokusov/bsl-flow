#Requires -Version 7.0
[CmdletBinding()]
param([string]$PackageRoot)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($PackageRoot)) { $PackageRoot = Split-Path -Parent (Split-Path -Parent (Split-Path -Parent (Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)))) }
$skill = Join-Path $PackageRoot 'global\skills\1c-spec-review'
. (Join-Path $skill 'scripts\Invoke-CouncilReview.ps1')

$passed = 0
function Assert-True([bool]$Condition, [string]$Message) {
    if (-not $Condition) { throw "FAIL: $Message" }
    $script:passed++
    "PASS $Message"
}

function New-TempProject {
    $root = Join-Path ([IO.Path]::GetTempPath()) ('council-cycle-' + [guid]::NewGuid().ToString('N'))
    $change = Join-Path $root 'openspec\changes\demo'
    New-Item -ItemType Directory -Path $change -Force | Out-Null
    Copy-Item -LiteralPath (Join-Path $PackageRoot 'openspec\changes\api-specification-council\original-task.md') -Destination (Join-Path $change 'original-task.md')
    Copy-Item -LiteralPath (Join-Path $PackageRoot 'openspec\changes\api-specification-council\spec.md') -Destination (Join-Path $change 'spec.md')
    [System.IO.File]::WriteAllText((Join-Path $root 'bsl-flow.yaml'), (Get-Content -Raw -LiteralPath (Join-Path $PackageRoot 'global\skills\1c-init-project\assets\project\bsl-flow.yaml')))
    return $root
}

# Stub dispatcher: deterministic role payloads without network. One critic asks
# for input; the chair reconciles the aggregate embedded in its prompt and must
# emit the complete final specification text with resolvable references.
$script:stubCalls = [System.Collections.Concurrent.ConcurrentQueue[string]]::new()
$stub = {
    param($Attempt, $PromptText, $Route)
    $role = [string]$Attempt.role
    [void]$script:stubCalls.Enqueue($role)
    $specText = [regex]::Match($PromptText, '(?s)BEGIN UNTRUSTED DATA: spec\.md>>>[\r\n]+(?<spec>.*?)[\r\n]+<<<END UNTRUSTED DATA: spec\.md').Groups['spec'].Value
    # Preserve the exact draft bytes: the sealed block carries the file's trailing newline.
    $specText += "`n"
    if ($role -ceq 'chair') {
        $block = [regex]::Match($PromptText, '(?s)BEGIN TRUSTED AGGREGATES.*?>>>[\r\n]+(?<json>\{.*\})[\r\n]+<<<END TRUSTED AGGREGATES')
        $aggregate = $block.Groups['json'].Value | ConvertFrom-Json -ErrorAction Stop
        $protectedDecisions = @()
        foreach ($item in @($aggregate.protected)) {
            $protectedDecisions += [pscustomobject][ordered]@{
                composite_id = [string]$item.composite_id; decision = 'preserved'
                reason = 'unchanged scope'; evidence = 'stub aggregate'
            }
        }
        $reqRefs = @()
        foreach ($req in @($aggregate.requirements)) {
            $reqRefs += [pscustomobject][ordered]@{ id = [string]$req.id; final_refs = @('Требуемое поведение / 1') }
        }
        # The questions asked by members must reach the chair aggregate.
        $questionsSeen = @(@($aggregate.questions) | ForEach-Object { [string]$_.text })
        $needsInput = ($questionsSeen.Count -gt 0)
        return [ordered]@{
            status = 'completed'
            payload = [pscustomobject][ordered]@{
                verdict = if ($needsInput) { 'needs_input' } else { 'PASS' }
                decisions = @(); protected_decisions = @($protectedDecisions); requirement_refs = @($reqRefs)
                final_spec_text = $specText; final_design_text = $null
            }
            observed = [ordered]@{ provider = 'stub'; model = 'stub-chair'; effort = $null }
            execution_mode = 'direct_api'; fallback_reason = $null
        }
    }
    if ($role -ceq 'brainstorm') {
        return [ordered]@{
            status = 'completed'
            payload = [pscustomobject][ordered]@{
                role = 'brainstorm'; alternatives = @('ship as is'); risks = @('vendor lock'); unknowns = @(); questions = @('which vendor?')
            }
            observed = [ordered]@{ provider = 'stub'; model = 'stub-critic'; effort = $null }
            execution_mode = 'direct_api'; fallback_reason = $null
        }
    }
    $verdict = if ($role -ceq 'intent_critic') { 'needs_input' } else { 'PASS' }
    $questions = if ($role -ceq 'intent_critic') { @('Is the review budget explicitly authorized for live smoke?') } else { @() }
    return [ordered]@{
        status = 'completed'
        payload = [pscustomobject][ordered]@{
            role = $role; verdict = $verdict; findings = @()
            do_not_change = @('keep scope'); needs_input_questions = $questions
        }
        observed = [ordered]@{ provider = 'stub'; model = 'stub-critic'; effort = $null }
        execution_mode = 'direct_api'; fallback_reason = $null
    }
}

# 1. Full public cycle: member dispatch, chair fan-in, publication, final validation.
$proj = New-TempProject
try {
    # Direct_api route needs resolvable credentials; point the token env at a test value.
    [System.Environment]::SetEnvironmentVariable('OPENAI_API_KEY', 'test-token')
    [System.Environment]::SetEnvironmentVariable('DEEPSEEK_API_KEY', 'test-token')
    try {
        $result = Invoke-BSLFlowCouncilReview -ProjectPath $proj -ChangeName 'demo' -AllowLiveDispatch -Dispatcher $stub
    }
    finally {
        [System.Environment]::SetEnvironmentVariable('OPENAI_API_KEY', $null)
        [System.Environment]::SetEnvironmentVariable('DEEPSEEK_API_KEY', $null)
    }
    Assert-True ([string]$result.review.verdict -eq 'needs_input') 'member questions propagate to a needs_input verdict'
    Assert-True (@($result.review.questions).Count -ge 1) 'material member questions are stored in the review'
    Assert-True ([string]$result.review.diversity -eq 'multi_model') 'observed stub models give multi_model diversity'
    Assert-True (Test-Path -LiteralPath (Join-Path $proj 'openspec\changes\demo\review.json') -PathType Leaf) 'review.json v2 published to the change'
    $onDisk = Get-Content -Raw -LiteralPath (Join-Path $proj 'openspec\changes\demo\review.json') | ConvertFrom-Json
    Assert-True ([int]$onDisk.schema_version -eq 2) 'published artifact is schema v2'
    Assert-True ([bool]$result.final_validation.passed) 'public final validation passes'
    Assert-True (Test-Path -LiteralPath (Join-Path $proj '.bsl-flow/reports/spec-review/demo.council/publication/completed.event.json') -PathType Leaf) 'durable completion event recorded'
    $resultFiles = @(Get-ChildItem -LiteralPath (Join-Path $proj '.bsl-flow/reports/spec-review/demo.council/intent_critic') -File -Filter 'result-*.json')
    Assert-True ($resultFiles.Count -eq 1) 'terminal member result is an immutable per-attempt file'
    $reviewCalls = @(@($script:stubCalls.ToArray()) | Where-Object { $_ -eq 'intent_critic' }).Count
    Assert-True ($reviewCalls -eq 1) 'no role was dispatched twice in one cycle'
}
finally { Remove-Item -LiteralPath $proj -Recurse -Force -ErrorAction SilentlyContinue }

# 2. Missing credentials without capabilities block before any dispatch.
$proj2 = New-TempProject
try {
    try { $null = Invoke-BSLFlowCouncilReview -ProjectPath $proj2 -ChangeName 'demo' -AllowLiveDispatch -Dispatcher $stub; throw 'FAIL cycle dispatched without route' }
    catch { Assert-True ([string]$_.Exception.Message -match 'BF_BLOCKED') 'credential-less cycle without capabilities is BLOCKED' }
}
finally { Remove-Item -LiteralPath $proj2 -Recurse -Force -ErrorAction SilentlyContinue }

# 3. Fresh contexts from a fallback runner complete the tokenless default route.
$proj3 = New-TempProject
try {
    $script:fallbackCalls = [System.Collections.Generic.List[string]]::new()
    $fallbackRunner = {
        param($Attempt, $PromptText, $Capability)
        $script:fallbackCalls.Add([string]$Attempt.role)
        if ([string]$Capability.model -ne 'current-model' -or [string]$Capability.effort -ne 'high') { throw 'FAIL runner saw a wrong capability receipt' }
        $specText = [regex]::Match($PromptText, '(?s)BEGIN UNTRUSTED DATA: spec\.md>>>[\r\n]+(?<spec>.*?)[\r\n]+<<<END UNTRUSTED DATA: spec\.md').Groups['spec'].Value
        $specText += "`n"
        $role = [string]$Attempt.role
        # The runner stands in for the host adapter: it reads the terminal host
        # receipt and returns the observed identity it actually executed with.
        $observed = [ordered]@{ observed_model = 'receipt-model'; observed_effort = 'receipt-effort' }
        if ($role -ceq 'chair') {
            $block = [regex]::Match($PromptText, '(?s)BEGIN TRUSTED AGGREGATES.*?>>>[\r\n]+(?<json>\{.*\})[\r\n]+<<<END TRUSTED AGGREGATES')
            $aggregate = $block.Groups['json'].Value | ConvertFrom-Json -ErrorAction Stop
            $protectedDecisions = @()
            foreach ($item in @($aggregate.protected)) {
                $protectedDecisions += [pscustomobject][ordered]@{ composite_id = [string]$item.composite_id; decision = 'preserved'; reason = 'scope'; evidence = 'stub' }
            }
            $reqRefs = @()
            foreach ($req in @($aggregate.requirements)) { $reqRefs += [pscustomobject][ordered]@{ id = [string]$req.id; final_refs = @('Требуемое поведение / 1') } }
            return [ordered]@{ status = 'completed'; payload = [pscustomobject][ordered]@{
                verdict = 'PASS'; decisions = @(); protected_decisions = @($protectedDecisions); requirement_refs = @($reqRefs)
                final_spec_text = $specText
            }; usage = $null } + $observed
        }
        return [ordered]@{ status = 'completed'; payload = [pscustomobject][ordered]@{
            role = $role; verdict = 'PASS'; findings = @(); do_not_change = @(); needs_input_questions = @()
        }; usage = $null } + $observed
    }
    # Capability claims the identity pre-dispatch; the envelope must still carry
    # the receipt-observed values, so the review must show them, not the claim.
    $capability = [ordered]@{
        capability_version = 'fixture-current-agent-v1'; fresh_context = $true; sealed = $true; terminal = $true
        provider = 'current_agent'; model = 'current-model'; effort = 'high'; source = 'fixture-host-proof'
        executable_sha256 = ('a' * 64); sandbox_sha256 = ('b' * 64); catalog_sha256 = ('c' * 64); skills_sha256 = ('d' * 64)
        catalog_source_path = 'C:\fixture\critic-catalog-source.json'; catalog_source_sha256 = ('e' * 64)
    }
    $capabilities = @{}
    foreach ($role in @('brainstorm', 'intent_critic', 'architecture_critic', 'executability_critic', 'chair')) { $capabilities[$role] = $capability }
    $result3 = Invoke-BSLFlowCouncilReview -ProjectPath $proj3 -ChangeName 'demo' -AllowLiveDispatch -FallbackRunner $fallbackRunner -Capabilities $capabilities
    Assert-True ([string]$result3.review.verdict -eq 'PASS') 'tokenless default route completes through fresh current-agent contexts'
    Assert-True ([string]$result3.review.diversity -eq 'multi_role_single_model') 'fallback diversity is never multi_model'
    Assert-True ([bool]$result3.review.fallback_visible) 'fallback stays visible in the report'
    Assert-True (@($script:fallbackCalls).Count -eq 4) 'all four required roles ran as fresh fallback contexts'
    $fallbackEnvelope = @($result3.review.members | Where-Object { $_.role -eq 'intent_critic' })[0]
    Assert-True ([string]$fallbackEnvelope.execution_mode -eq 'current_agent_fallback' -and [string]$fallbackEnvelope.observed.model -eq 'receipt-model' -and [string]$fallbackEnvelope.observed.effort -eq 'receipt-effort') 'fallback envelope keeps receipt-observed provenance, not requested values'
}
finally { Remove-Item -LiteralPath $proj3 -Recurse -Force -ErrorAction SilentlyContinue }

# 4. A timeout after dispatch is persisted once and never blindly repeated.
$proj4 = New-TempProject
try {
    # Cycle admission requires every enabled role to be servable before the
    # first dispatch, so the timeout path needs both provider tokens.
    [System.Environment]::SetEnvironmentVariable('DEEPSEEK_API_KEY', 'test-token')
    [System.Environment]::SetEnvironmentVariable('OPENAI_API_KEY', 'test-token')
    try {
        $timeoutDispatcher = {
            param($Attempt, $PromptText, $Route)
            throw 'BF_UNKNOWN_AFTER_DISPATCH: council request timed out after dispatch.'
        }
        try {
            $null = Invoke-BSLFlowCouncilReview -ProjectPath $proj4 -ChangeName 'demo' -AllowLiveDispatch -Dispatcher $timeoutDispatcher
            throw 'FAIL unknown dispatch did not block'
        }
        catch { Assert-True ([string]$_.Exception.Message -match 'unknown_after_dispatch') 'required timeout blocks the cycle' }
        $resultFiles = @(Get-ChildItem -LiteralPath (Join-Path $proj4 '.bsl-flow/reports/spec-review/demo.council/intent_critic') -File -Filter 'result-*.json')
        Assert-True ($resultFiles.Count -eq 1) 'unknown outcome is persisted as an immutable terminal result'
        $stored = Get-Content -Raw -LiteralPath $resultFiles[0].FullName | ConvertFrom-Json
        Assert-True ([string]$stored.envelope.status -eq 'unknown_after_dispatch') 'persisted status is unknown_after_dispatch'
        $ledgerFile = Join-Path $proj4 '.bsl-flow/reports/spec-review/demo.council/budget/reservation-intent_critic-0001.json'
        Assert-True (Test-Path -LiteralPath $ledgerFile -PathType Leaf) 'durable budget reservation exists for the dispatch'
        $ledger = Get-Content -Raw -LiteralPath $ledgerFile | ConvertFrom-Json
        Assert-True ([string]$ledger.cost_state -eq 'unknown') 'unknown cost stays unknown in the ledger'
        # A resume must refuse to re-dispatch the same retained attempt.
        try {
            $null = Invoke-BSLFlowCouncilReview -ProjectPath $proj4 -ChangeName 'demo' -AllowLiveDispatch -Dispatcher $stub
            throw 'FAIL resume re-dispatched an unknown attempt'
        }
        catch { Assert-True ([string]$_.Exception.Message -match 'unknown_after_dispatch') 'resume refuses to repeat an unknown paid dispatch' }
        $afterCalls = @(Get-ChildItem -LiteralPath (Join-Path $proj4 '.bsl-flow/reports/spec-review/demo.council/intent_critic') -File -Filter 'result-*.json')
        Assert-True ($afterCalls.Count -eq 1) 'no second dispatch happened for the unknown attempt'
    }
    finally {
        [System.Environment]::SetEnvironmentVariable('DEEPSEEK_API_KEY', $null)
        [System.Environment]::SetEnvironmentVariable('OPENAI_API_KEY', $null)
    }
}
finally { Remove-Item -LiteralPath $proj4 -Recurse -Force -ErrorAction SilentlyContinue }

# 5. Completed member attempts are reused on resume without a second call.
$proj5 = New-TempProject
try {
    [System.Environment]::SetEnvironmentVariable('OPENAI_API_KEY', 'test-token')
    [System.Environment]::SetEnvironmentVariable('DEEPSEEK_API_KEY', 'test-token')
    try {
        $null = Invoke-BSLFlowCouncilReview -ProjectPath $proj5 -ChangeName 'demo' -AllowLiveDispatch -Dispatcher $stub
        $before = @($script:stubCalls.ToArray()).Count
        $null = Invoke-BSLFlowCouncilReview -ProjectPath $proj5 -ChangeName 'demo' -AllowLiveDispatch -Dispatcher $stub
        $after = @($script:stubCalls.ToArray()).Count
        Assert-True ($after -eq $before) 'resume reuses completed terminal results without new dispatches'
    }
    finally {
        [System.Environment]::SetEnvironmentVariable('OPENAI_API_KEY', $null)
        [System.Environment]::SetEnvironmentVariable('DEEPSEEK_API_KEY', $null)
    }
}
finally { Remove-Item -LiteralPath $proj5 -Recurse -Force -ErrorAction SilentlyContinue }

# 6. Design/evidence drift creates new attempts instead of reusing stale results.
$proj6 = New-TempProject
try {
    [System.Environment]::SetEnvironmentVariable('OPENAI_API_KEY', 'test-token')
    [System.Environment]::SetEnvironmentVariable('DEEPSEEK_API_KEY', 'test-token')
    try {
        $null = Invoke-BSLFlowCouncilReview -ProjectPath $proj6 -ChangeName 'demo' -AllowLiveDispatch -Dispatcher $stub
        $callsBefore = @($script:stubCalls.ToArray()).Count
        $runRoot6 = Join-Path $proj6 '.bsl-flow/reports/spec-review/demo.council'
        $oldAttemptId = (Get-Content -Raw -LiteralPath (Join-Path $runRoot6 'intent_critic/attempt-0001.json') | ConvertFrom-Json).attempt_id
        $designPath = Join-Path $proj6 'openspec\changes\demo\design.md'
        [System.IO.File]::WriteAllText($designPath, "# Design`n`nDrifted design content.", [System.Text.UTF8Encoding]::new($false))
        $null = Invoke-BSLFlowCouncilReview -ProjectPath $proj6 -ChangeName 'demo' -AllowLiveDispatch -Dispatcher $stub
        Assert-True (@($script:stubCalls.ToArray()).Count -gt $callsBefore) 'design drift triggered fresh dispatches instead of stale reuse'
        $newAttemptId = (Get-Content -Raw -LiteralPath (Join-Path $runRoot6 'intent_critic/attempt-0002.json') | ConvertFrom-Json).attempt_id
        Assert-True ($newAttemptId -and $newAttemptId -cne $oldAttemptId) 'design drift created a new sequenced attempt with a new attempt_id'
    }
    finally {
        [System.Environment]::SetEnvironmentVariable('OPENAI_API_KEY', $null)
        [System.Environment]::SetEnvironmentVariable('DEEPSEEK_API_KEY', $null)
    }
}
finally { Remove-Item -LiteralPath $proj6 -Recurse -Force -ErrorAction SilentlyContinue }

# 7. An optional role's terminal failure stays in the report and degrades diversity.
$proj7 = New-TempProject
try {
    $brainstormPattern = '(?m)^(\s+)brainstorm:\r?\n(\s+)enabled: false'
    $brainstormReplacement = '$1brainstorm:' + "`n" + '$2enabled: true'
    $config7 = [regex]::Replace((Get-Content -Raw -LiteralPath (Join-Path $proj7 'bsl-flow.yaml')), $brainstormPattern, $brainstormReplacement)
    [System.IO.File]::WriteAllText((Join-Path $proj7 'bsl-flow.yaml'), $config7, [System.Text.UTF8Encoding]::new($false))
    [System.Environment]::SetEnvironmentVariable('OPENAI_API_KEY', 'test-token')
    [System.Environment]::SetEnvironmentVariable('DEEPSEEK_API_KEY', 'test-token')
    try {
        $failBrainstorm = {
            param($Attempt, $PromptText, $Route)
            if ([string]$Attempt.role -ceq 'brainstorm') { throw 'BF_FAILED_BEFORE_ACCEPTANCE: provider refused the request.' }
            return & $stub $Attempt $PromptText $Route
        }
        $result7 = Invoke-BSLFlowCouncilReview -ProjectPath $proj7 -ChangeName 'demo' -AllowLiveDispatch -Dispatcher $failBrainstorm
        $brainstormEnvelope = @($result7.review.members | Where-Object { $_.role -eq 'brainstorm' })[0]
        Assert-True ($null -ne $brainstormEnvelope -and [string]$brainstormEnvelope.status -eq 'failed_before_acceptance') 'optional failure envelope is published in members'
        Assert-True ([string]$result7.review.diversity -eq 'degraded') 'optional failure degrades diversity'
    }
    finally {
        [System.Environment]::SetEnvironmentVariable('OPENAI_API_KEY', $null)
        [System.Environment]::SetEnvironmentVariable('DEEPSEEK_API_KEY', $null)
    }
}
finally { Remove-Item -LiteralPath $proj7 -Recurse -Force -ErrorAction SilentlyContinue }

# 8. Resume with retained results does not double-count budget estimates.
$proj8 = New-TempProject
try {
    # Inject a small budget section into the council block (deterministic YAML edit).
    $configPath8 = Join-Path $proj8 'bsl-flow.yaml'
    $configText8 = Get-Content -Raw -LiteralPath $configPath8
    $budgetAnchor = "  council:`n"
    $budgetBlock = "  council:`n    budget:`n      limit: 100.0`n      reservation: 0.0`n"
    $configText8 = $configText8.Replace($budgetAnchor, $budgetBlock)
    # Give each model profile an explicit per-dispatch estimate so the cycle
    # admission is a real number, then the resume leg clamps the limit.
    foreach ($modelName in @('sol', 'astra', 'flash')) {
        $modelAnchor = '    ' + $modelName + ":" + "`n" + '      provider:'
        $modelReplacement = '    ' + $modelName + ":" + "`n" + '      cost_estimate_usd: 0.01' + "`n" + '      provider:'
        $configText8 = $configText8.Replace($modelAnchor, $modelReplacement)
    }
    [System.IO.File]::WriteAllText($configPath8, $configText8, [System.Text.UTF8Encoding]::new($false))
    [System.Environment]::SetEnvironmentVariable('OPENAI_API_KEY', 'test-token')
    [System.Environment]::SetEnvironmentVariable('DEEPSEEK_API_KEY', 'test-token')
    try {
        $null = Invoke-BSLFlowCouncilReview -ProjectPath $proj8 -ChangeName 'demo' -AllowLiveDispatch -Dispatcher $stub
        $callsBefore = @($script:stubCalls.ToArray()).Count
        $null = Invoke-BSLFlowCouncilReview -ProjectPath $proj8 -ChangeName 'demo' -AllowLiveDispatch -Dispatcher $stub
        Assert-True (@($script:stubCalls.ToArray()).Count -eq $callsBefore) 'resume with retained results passes admission without new dispatches under a limit that forbids any new call'
    }
    finally {
        [System.Environment]::SetEnvironmentVariable('OPENAI_API_KEY', $null)
        [System.Environment]::SetEnvironmentVariable('DEEPSEEK_API_KEY', $null)
    }
}
finally { Remove-Item -LiteralPath $proj8 -Recurse -Force -ErrorAction SilentlyContinue }

# 9. Concurrent dispatches cannot collectively oversubscribe the budget: admission
# and reservation are one atomic ledger transaction under a single mutex hold.
$proj9 = New-TempProject
try {
    $runRoot9 = Join-Path $proj9 '.bsl-flow/reports/spec-review/demo.council'
    [void][IO.Directory]::CreateDirectory($runRoot9)
    $budget9 = [ordered]@{ limit = 0.05; reservation = 0.0 }
    $binding9 = [pscustomobject][ordered]@{
        provider = 'p'; model = 'm'; effort = 'e'; protocol = 'openai_compatible'
        endpoint = [pscustomobject][ordered]@{ scheme = 'https'; host = 'x'; port = 443; base_path = '/' }
        transport_capability_version = 1; prompt_version = 'v'; member_schema_version = 1
        input_hashes = [pscustomobject][ordered]@{
            original_task_sha256 = ('a' * 64); spec_sha256 = ('b' * 64)
            design_sha256 = $null; evidence_sha256 = $null; policy_hash = ('c' * 64); rubric_sha256 = ('d' * 64)
        }
    }
    $raceWorker = {
        param($RunRoot, $Role, $Binding, $Budget, $ScriptsRoot)
        . (Join-Path $ScriptsRoot 'Review.Common.ps1')
        . (Join-Path $ScriptsRoot 'Council.Engine.ps1')
        try {
            $attempt = New-BSLFlowCouncilAttempt -RunRoot $RunRoot -Role $Role -Binding $Binding
            $null = Approve-BSLFlowCouncilBudgetDispatch -RunRoot $RunRoot -Role $Role -Attempt $attempt -EstimateUsd 0.04 -Budget $Budget
            return [pscustomobject]@{ admitted = $true }
        }
        catch { return [pscustomobject]@{ admitted = $false } }
    }
    $raceJobs = @()
    foreach ($role in @('intent_critic', 'architecture_critic')) {
        $raceJobs += Start-ThreadJob -ScriptBlock $raceWorker -ArgumentList $runRoot9, $role, $binding9, $budget9, $PSScriptRoot
    }
    $raceResults = @()
    foreach ($job in (Wait-Job $raceJobs)) { $raceResults += @(Receive-Job $job -Wait) }
    Remove-Job $raceJobs -Force -ErrorAction SilentlyContinue
    $admittedCount = @($raceResults | Where-Object { $_.admitted }).Count
    Assert-True ($admittedCount -eq 1) 'concurrent over-limit roles cannot both pass admission'
    $reservations9 = @(Get-ChildItem (Join-Path $runRoot9 'budget') -File -Filter 'reservation-*.json')
    $reservedTotal = 0.0
    foreach ($f in $reservations9) { $e = Get-Content -Raw $f.FullName | ConvertFrom-Json; $reservedTotal += [double]$e.estimated_usd }
    Assert-True ($reservedTotal -le 0.05) 'durable reserved total never exceeds the limit under concurrency'
}
finally { Remove-Item -LiteralPath $proj9 -Recurse -Force -ErrorAction SilentlyContinue }

# 10. Cycle admission refuses the whole cycle before ANY dispatch when an
# enabled role cannot be served: members hold credentials while the tokenless
# chair has no trusted capability receipt, so even the cheaper member roles
# must never dispatch.
$proj10 = New-TempProject
try {
    $oldOpenAiKey = [System.Environment]::GetEnvironmentVariable('OPENAI_API_KEY')
    try {
        [System.Environment]::SetEnvironmentVariable('DEEPSEEK_API_KEY', 'test-token')
        [System.Environment]::SetEnvironmentVariable('OPENAI_API_KEY', $null)
        $callsBefore = @($script:stubCalls.ToArray()).Count
        try {
            $null = Invoke-BSLFlowCouncilReview -ProjectPath $proj10 -ChangeName 'demo' -AllowLiveDispatch -Dispatcher $stub
            throw 'FAIL cycle dispatched with an unservable chair role'
        }
        catch {
            Assert-True ([string]$_.Exception.Message -match 'BF_BLOCKED: council cannot start: role chair') 'unservable chair role refuses the whole cycle at admission'
        }
        Assert-True (@($script:stubCalls.ToArray()).Count -eq $callsBefore) 'no role was dispatched when admission refuses the cycle'
        Assert-True (-not (Test-Path -LiteralPath (Join-Path $proj10 '.bsl-flow/reports/spec-review/demo.council/budget') -PathType Container)) 'no budget reservation was taken before the refusal'
    }
    finally {
        [System.Environment]::SetEnvironmentVariable('DEEPSEEK_API_KEY', $null)
        if ($null -ne $oldOpenAiKey) { [System.Environment]::SetEnvironmentVariable('OPENAI_API_KEY', $oldOpenAiKey) }
    }
}
finally { Remove-Item -LiteralPath $proj10 -Recurse -Force -ErrorAction SilentlyContinue }

"ALL_STAGE_CYCLE_PASSED=$passed"
