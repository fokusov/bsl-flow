#Requires -Version 7.0
# Mixed-route council regression: one cycle where roles with credentials run
# through the direct API dispatcher and tokenless roles run through the
# current-agent fallback runner, with the real managed provider budget hooks
# attached. Direct roles must own their provider budget reservation/outcome;
# fallback roles must produce no provider budget entries (their worker owns
# the nested reservation when it really dispatches).
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$root = Split-Path $PSScriptRoot -Parent
foreach ($name in @('Task.Storage.ps1', 'Task.Process.ps1', 'Task.Contracts.ps1', 'Task.Gates.ps1', 'Task.Engine.ps1', 'Task.Execution.ps1', 'Task.Stages.ps1', 'Task.ManagedReview.ps1')) {
    . (Join-Path $root ('global/skills/1c-task/scripts/' + $name))
}
. (Join-Path $root 'global/skills/1c-spec-review/scripts/Invoke-CouncilReview.ps1')

$passed = 0
function Assert-True([bool]$Condition, [string]$Message) {
    if (-not $Condition) { throw "FAIL: $Message" }
    $script:passed++
    "PASS $Message"
}

$temp = Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-council-mixed-' + [guid]::NewGuid().ToString('N'))
$change = Join-Path $temp 'openspec/changes/demo'
[void][IO.Directory]::CreateDirectory($change)
Copy-Item -LiteralPath (Join-Path $root 'openspec/changes/api-specification-council/original-task.md') -Destination (Join-Path $change 'original-task.md')
Copy-Item -LiteralPath (Join-Path $root 'openspec/changes/api-specification-council/spec.md') -Destination (Join-Path $change 'spec.md')
# Default council config: chair runs on the openai provider, all three critics
# on deepseek. Setting only OPENAI_API_KEY yields a mixed cycle by construction.
[IO.File]::WriteAllText((Join-Path $temp 'bsl-flow.yaml'), (Get-Content -Raw -LiteralPath (Join-Path $root 'global/skills/1c-init-project/assets/project/bsl-flow.yaml')))

$state = [pscustomobject]@{
    project_path = $temp
    task_id      = [guid]::NewGuid().ToString()
    request      = [pscustomobject]@{
        models = [pscustomobject]@{ worker = 'fixture-worker'; reviewer = 'fixture-reviewer' }
        budget = [pscustomobject]@{ currency = 'USD'; limit = 5; reservation = 0.25 }
    }
}
$providerContext = [pscustomobject]@{
    task_id             = $state.task_id
    attempt_id          = [guid]::NewGuid().ToString()
    context_root        = (Join-Path $temp 'provider-context')
    artifact_root       = (Join-Path $temp 'provider-artifacts')
    canonical_store_root = (Join-Path $temp 'provider-canonical')
    prior_artifacts     = @()
}
$hooks = New-BFManagedCouncilProviderDispatchHooks -State $state -ProviderContext $providerContext

$script:directRoles = [System.Collections.Generic.List[string]]::new()
$stub = {
    param($Attempt, $PromptText, $Route)
    $role = [string]$Attempt.role
    $script:directRoles.Add($role)
    $specText = [regex]::Match($PromptText, '(?s)BEGIN UNTRUSTED DATA: spec\.md>>>[\r\n]+(?<spec>.*?)[\r\n]+<<<END UNTRUSTED DATA: spec\.md').Groups['spec'].Value
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
        return [ordered]@{
            status = 'completed'
            payload = [pscustomobject][ordered]@{
                verdict = 'PASS'; decisions = @(); protected_decisions = @($protectedDecisions)
                requirement_refs = @($reqRefs); final_spec_text = $specText; final_design_text = $null
            }
            observed = [ordered]@{ provider = 'stub'; model = 'stub-chair'; effort = $null }
            usage = [ordered]@{ input_tokens = 100; output_tokens = 50 }
            reported_cost_usd = 0.01
            execution_mode = 'direct_api'; fallback_reason = $null
        }
    }
    return [ordered]@{
        status = 'completed'
        payload = [pscustomobject][ordered]@{
            role = $role; verdict = 'PASS'; findings = @(); do_not_change = @(); needs_input_questions = @()
        }
        observed = [ordered]@{ provider = 'stub'; model = "stub-$role"; effort = $null }
        usage = [ordered]@{ input_tokens = 10; output_tokens = 5 }
        reported_cost_usd = 0.01
        execution_mode = 'direct_api'; fallback_reason = $null
    }
}

$script:fallbackRoles = [System.Collections.Generic.List[string]]::new()
$runner = {
    param($Attempt, $PromptText, $Capability)
    $role = [string]$Attempt.role
    $script:fallbackRoles.Add($role)
    if ([string]$Capability.model -ne 'current-model' -or [string]$Capability.effort -ne 'medium') { throw "FAIL fallback capability differs for $role" }
    return [ordered]@{
        status = 'completed'
        payload = [pscustomobject][ordered]@{
            role = $role; verdict = 'PASS'; findings = @(); do_not_change = @(); needs_input_questions = @()
        }
        observed_model = 'receipt-model'; observed_effort = 'medium'
        usage = $null; reported_cost_usd = $null
    }
}
$capability = [ordered]@{
    capability_version = 'fixture-current-agent-v1'; fresh_context = $true; sealed = $true; terminal = $true
    provider = 'current_agent'; model = 'current-model'; effort = 'medium'; source = 'fixture-host-proof'
    executable_sha256 = ('a' * 64); sandbox_sha256 = ('b' * 64); catalog_sha256 = ('c' * 64); skills_sha256 = ('d' * 64)
    catalog_source_path = 'C:\fixture\critic-catalog-source.json'; catalog_source_sha256 = ('e' * 64)
}
$capabilities = @{}
foreach ($role in @('intent_critic', 'architecture_critic', 'executability_critic')) { $capabilities[$role] = $capability }

$hadDeepSeek = [Environment]::GetEnvironmentVariable('DEEPSEEK_API_KEY')
try {
    [Environment]::SetEnvironmentVariable('OPENAI_API_KEY', 'test-token')
    [Environment]::SetEnvironmentVariable('DEEPSEEK_API_KEY', $null)
    $result = Invoke-BSLFlowCouncilReview -ProjectPath $temp -ChangeName 'demo' -AllowLiveDispatch `
        -Dispatcher $stub -FallbackRunner $runner -Capabilities $capabilities `
        -BeforeDispatch $hooks.BeforeDispatch -AfterDispatch $hooks.AfterDispatch

    Assert-True ([string]$result.review.verdict -eq 'PASS') 'mixed cycle completes with an accepted review'
    Assert-True (@($script:directRoles).Count -eq 1 -and $script:directRoles[0] -ceq 'chair') 'only the credentialed chair dispatched through the direct API route'
    Assert-True (@($script:fallbackRoles).Count -eq 3) 'all three tokenless critics ran through the fallback runner'
    foreach ($envelope in @($result.review.members)) {
        $expectedMode = if ([string]$envelope.role -ceq 'chair') { 'direct_api' } else { 'current_agent_fallback' }
        Assert-True ([string]$envelope.execution_mode -ceq $expectedMode) ("member {0} kept its route" -f [string]$envelope.role)
    }

    $ledgerPath = Join-Path $providerContext.artifact_root 'budget/ledger.json'
    Assert-True (Test-Path -LiteralPath $ledgerPath -PathType Leaf) 'provider budget ledger was created for the mixed cycle'
    $ledger = Read-BFJson $ledgerPath
    $entries = @($ledger.entries)
    Assert-True ($ledger.task_id -ceq $state.task_id) 'provider budget ledger is bound to the controller task'
    $reservations = @($entries | Where-Object { $_.kind -ceq 'reservation' })
    $outcomes = @($entries | Where-Object { $_.kind -ceq 'outcome' })
    Assert-True ($reservations.Count -eq 1 -and $outcomes.Count -eq 1) 'exactly one provider reservation/outcome pair exists for the single direct dispatch'
    Assert-True ([string]$reservations[0].dispatch -cmatch '/provider/council/chair/attempt-[0-9a-fA-F]{32}$') 'provider budget reservation belongs to the direct chair dispatch'
    Assert-True ([double]$reservations[0].reservation_usd -eq 0.25) 'provider budget reservation keeps the authorized request amount'
    Assert-True ([string]$outcomes[0].dispatch -ceq [string]$reservations[0].dispatch) 'provider budget outcome closes the same dispatch'
    Assert-True ([string]$outcomes[0].cost_state -ceq 'known' -and [double]$outcomes[0].reported_cost_usd -eq 0.01) 'direct outcome is a known cost from retained host evidence'
    foreach ($entry in $entries) {
        Assert-True ([string]$entry.dispatch -cnotmatch '/council/(intent_critic|architecture_critic|executability_critic)/') 'no fallback role wrote a provider budget entry'
    }
    $chairDirectory = [string]$reservations[0].dispatch -replace '^.*/provider/', ''
    $hostReceiptPath = Join-Path $providerContext.artifact_root ($chairDirectory + '/host-result.json')
    Assert-True (Test-Path -LiteralPath $hostReceiptPath -PathType Leaf) 'AfterDispatch hook retained the direct host receipt'
    $hostReceipt = Read-BFJson $hostReceiptPath
    Assert-True ([double]$hostReceipt.reported_cost_usd -eq 0.01 -and [int]$hostReceipt.usage.input_tokens -eq 100) 'host receipt binds the exact reported usage and cost'
    $summary = Get-BFProviderBudgetSummary $ledger
    Assert-True ([int]$summary.open -eq 0 -and [int]$summary.unknown -eq 0) 'mixed cycle leaves no open or unknown provider budget dispatches'
    Assert-True ([double]$summary.spent_usd -eq 0.01) 'provider budget spent total matches the single known direct cost'
    Assert-True (Test-Path -LiteralPath (Join-Path $temp 'openspec/changes/demo/review.json') -PathType Leaf) 'mixed cycle published review.json'
    $onDisk = Get-Content -Raw -LiteralPath (Join-Path $temp 'openspec/changes/demo/review.json') | ConvertFrom-Json
    Assert-True ([int]$onDisk.schema_version -eq 2) 'published mixed review is schema v2'
    Assert-True ([bool]$result.final_validation.passed) 'mixed cycle passes the public final validation'
}
finally {
    [Environment]::SetEnvironmentVariable('OPENAI_API_KEY', $null)
    if ($null -ne $hadDeepSeek) { [Environment]::SetEnvironmentVariable('DEEPSEEK_API_KEY', $hadDeepSeek) }
    Remove-Item -LiteralPath $temp -Recurse -Force -ErrorAction SilentlyContinue
}

"MIXED_ROUTE_OK checks=$script:passed"
