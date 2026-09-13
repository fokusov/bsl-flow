#Requires -Version 7.0
Set-StrictMode -Version Latest
$ErrorActionPreference='Stop'
$root=Split-Path $PSScriptRoot -Parent
foreach($name in @('Task.Storage.ps1','Task.Contracts.ps1','Task.Gates.ps1','Task.Engine.ps1','Task.Stages.ps1','Task.ManagedReview.ps1')){. (Join-Path $root ('global/skills/1c-task/scripts/'+$name))}
$fixture=Join-Path $PSScriptRoot 'Test-TaskLifecycle.ps1'
$ast=[Management.Automation.Language.Parser]::ParseFile($fixture,[ref]$null,[ref]$null)
$definition=$ast.Find({param($node)$node -is [Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq 'New-SpecText'},$true)
. ([scriptblock]::Create($definition.Extent.Text))
$temp=Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-managed-review-'+[guid]::NewGuid().ToString('N'))
[void][IO.Directory]::CreateDirectory($temp)
$checks=0;$script:mode='pass';$script:calls=0
$script:providerEvidenceCall=$null
$script:managedWorkerProviderContext=$null
$state=[pscustomobject]@{project_path=$temp;task_id=[guid]::NewGuid().ToString();request=[pscustomobject]@{models=[pscustomobject]@{worker='fixture-worker';reviewer='fixture-reviewer'}}}
$change=Get-BFChangePath $state
[void][IO.Directory]::CreateDirectory($change)
$spec=New-SpecText
[IO.File]::WriteAllText((Join-Path $change 'spec.md'),$spec)
[IO.File]::WriteAllText((Join-Path $change 'original-task.md'),'Add the Example greeting to hello.txt.')
function Invoke-BFManagedWorker {
    param($State,$Stage,$Prompt,$Directory,$CodexPath,$Cancelled,$MaxOutputBytes,[object]$ProviderContext=$null)
    $script:calls++
    $script:managedWorkerProviderContext=$ProviderContext
    if($Stage -ne 'spec_review' -or $State.request.models.reviewer -ne 'fixture-reviewer' -or $State.request.timeout_seconds -ne 600 -or $MaxOutputBytes -ne 1048576 -or $Prompt -notmatch 'attached-only independent review'){throw 'Critic dispatch contract changed.'}
    [void][IO.Directory]::CreateDirectory($Directory)
    if($script:mode -eq 'stale'){[IO.File]::AppendAllText((Join-Path $change 'spec.md'),' changed')}
    $raw=[ordered]@{schema_version=1;reviewer_verdict='PASS';summary='Synthetic independent review.';scores=@{intent_fidelity=5;minimality=5;completeness=5;architecture_fit=5;testability=5;assumption_discipline=5;clarity=5};overengineering=@{items=@()};findings=@();do_not_change=@();confidence=0.9}
    if($script:mode -eq 'invalid'){$raw.scores.minimality=8}
    return @{schema_version=1;status='completed';summary='Review produced.';payload_json=Get-BFCanonicalJson $raw}
}
function Get-BFProviderManagedCouncilEvidence {
    param($State,[string]$ContextRoot,[int]$MaxBytes)
    $script:providerEvidenceCall=[ordered]@{state=$State;context_root=$ContextRoot;max_bytes=$MaxBytes}
    return '{"source":"provider-context-fixture"}'
}
try {
    $providerContext=[pscustomobject]@{
        context_root=(Join-Path $temp 'provider-context')
        artifact_root=(Join-Path $temp 'provider-artifacts')
        canonical_store_root=(Join-Path $temp 'provider-canonical')
    }
    $providerEvidence=Get-BFManagedCouncilEvidence -State $state -MaxBytes 2048 -ProviderContext $providerContext
    if($providerEvidence -cne '{"source":"provider-context-fixture"}' -or
       $script:providerEvidenceCall.context_root -cne $providerContext.context_root -or
       $script:providerEvidenceCall.max_bytes -ne 2048){throw 'Provider council evidence did not use the validated context root.'};$checks++

    $contextReview=Invoke-BFProfileSpecCritic -State $state -Directory (Join-Path $temp 'provider-review') -CodexPath '' -Cancelled $null -ProviderContext $providerContext
    if($contextReview.verdict -ne 'PASS' -or
       $null -eq $script:managedWorkerProviderContext -or
       $script:managedWorkerProviderContext.context_root -cne $providerContext.context_root -or
       $script:managedWorkerProviderContext.artifact_root -cne $providerContext.artifact_root -or
       $script:managedWorkerProviderContext.canonical_store_root -cne $providerContext.canonical_store_root){throw 'Provider context was not forwarded to the managed critic worker.'};$checks++

    $result=Invoke-BFProfileSpecCritic $state (Join-Path $temp 'pass') '' $null
    if($result.verdict -ne 'PASS' -or $result.inputs.spec_sha256 -cne (Get-BFFileHash (Join-Path $change 'spec.md'))){throw 'Normalized review/input binding failed.'};$checks++
    if(-not(Test-Path (Join-Path $temp 'pass/critic-inputs/spec.md'))){throw 'Durable exact input snapshot missing.'};$checks++
    [IO.File]::WriteAllText((Join-Path $temp 'pass/critic/payload.json'),'{}')
    $before=Get-BFFileHash (Join-Path $change 'review.json');$failed=$false
    try{Invoke-BFProfileSpecCritic $state (Join-Path $temp 'pass') '' $null|Out-Null}catch{$failed=$_.Exception.Message -match 'cached critic payload differs'}
    if(-not $failed -or (Get-BFFileHash (Join-Path $change 'review.json')) -cne $before){throw 'Modified cached payload was accepted.'};$checks++
    foreach($case in @('invalid','stale')){
        $before=Get-BFFileHash (Join-Path $change 'review.json');$script:mode=$case;$failed=$false
        try {Invoke-BFProfileSpecCritic $state (Join-Path $temp $case) '' $null|Out-Null}catch{$failed=$true}
        if(-not $failed -or (Get-BFFileHash (Join-Path $change 'review.json')) -cne $before){throw 'Failed/stale critic published a review.'};$checks++
        [IO.File]::WriteAllText((Join-Path $change 'spec.md'),$spec)
    }
    [IO.File]::WriteAllText((Join-Path $temp 'bsl-flow.yaml'),"review:`n  permissions:`n    shell: true`n")
    $before=$script:calls;$failed=$false
    try{Invoke-BFProfileSpecCritic $state (Join-Path $temp 'unsafe-policy') '' $null|Out-Null}catch{$failed=$true}
    if(-not $failed -or $script:calls -ne $before){throw 'Unsafe reviewer policy reached dispatch.'};$checks++
    # Council v2 managed route: the chair is the only model reconciler. The
    # council path must not dispatch a legacy spec_reconcile worker and must
    # keep fallback provenance sourced from host receipts, not request values.
    $councilTemp=Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-managed-council-'+[guid]::NewGuid().ToString('N'))
    try {
        [void][IO.Directory]::CreateDirectory($councilTemp)
        $councilState=[pscustomobject]@{project_path=$councilTemp;task_id=[guid]::NewGuid().ToString();request=[pscustomobject]@{models=[pscustomobject]@{worker='fixture-worker';reviewer='fixture-reviewer'};timeout_seconds=1800}}
        $councilChange=Get-BFChangePath $councilState
        [void][IO.Directory]::CreateDirectory($councilChange)
        [IO.File]::WriteAllText((Join-Path $councilChange 'spec.md'),$spec)
        [IO.File]::WriteAllText((Join-Path $councilChange 'original-task.md'),'Add the Example greeting to hello.txt.')
        [IO.File]::WriteAllText((Join-Path $councilTemp 'bsl-flow.yaml'),"review:`n  council:`n    enabled: true`n    max_parallel: 1`n    allow_local_http: false`n    legacy_mode: block`n    roles:`n      brainstorm:`n        enabled: false`n        required: false`n        model: astra`n        fallback: block`n      intent_critic:`n        enabled: true`n        required: true`n        model: astra`n        fallback: block`n      architecture_critic:`n        enabled: true`n        required: true`n        model: astra`n        fallback: block`n      executability_critic:`n        enabled: true`n        required: true`n        model: astra`n        fallback: block`n      chair:`n        enabled: true`n        required: true`n        model: astra`n        fallback: block`n    budget:`n      limit: 1.0`n      reservation: 0.0`n  llm:`n    providers:`n      fixture:`n        protocol: openai_compatible`n        base_url: https://fixture.invalid/v1`n        token_env: FIXTURE_MISSING_TOKEN`n    models:`n      astra:`n        provider: fixture`n        model: fixture-model`n        effort: medium`n")
        $councilCalls=[System.Collections.Concurrent.ConcurrentQueue[string]]::new()
        function Invoke-BFCouncilStub { param($State,$Stage,$Prompt,$Directory,$CodexPath,$Cancelled,$MaxOutputBytes)
            [void]$councilCalls.Enqueue($Stage)
            $payload=[ordered]@{role='intent_critic';verdict='PASS';findings=@();do_not_change=@();needs_input_questions=@()}
            return @{schema_version=1;status='completed';summary='Fixture council worker.';payload_json=(ConvertTo-Json -InputObject $payload -Depth 10)}
        }
        $failed=$false
        try{Invoke-BFProfileSpecCritic $councilState (Join-Path $councilTemp 'council') '' $null|Out-Null}catch{$failed=$true}
        # Token is missing and fallback is block for every role: the cycle must
        # block before any dispatch instead of silently using the worker model.
        if(-not $failed){throw 'Council route dispatched with blocked fallback and no capability receipt.'};$checks++
        if(@($councilCalls.ToArray()).Count -ne 0){throw 'Council fallback dispatched without a trusted capability receipt.'};$checks++
    }
    finally {
        $resolvedCouncil=[IO.Path]::GetFullPath($councilTemp)
        if(-not $resolvedCouncil.StartsWith([IO.Path]::GetFullPath([IO.Path]::GetTempPath()),[StringComparison]::OrdinalIgnoreCase)){throw 'Unsafe test cleanup target.'}
        Remove-Item -LiteralPath $resolvedCouncil -Recurse -Force
    }
    # Positive public tokenless managed scenario: fallback current_agent over the
    # real adapter chain. The codex process boundary is stubbed (Invoke-BFProcess),
    # but the rollout session observation, host receipt and fallback provenance
    # rules run exactly as packaged: the controller must complete four fresh
    # contexts and publish observed identity from the rollout, not request values.
    $positiveTemp=Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-managed-council-fallback-'+[guid]::NewGuid().ToString('N'))
    $positiveCodexHome=Join-Path $positiveTemp 'codex-home'
    try {
        [void][IO.Directory]::CreateDirectory($positiveTemp)
        $env:CODEX_HOME=$positiveCodexHome
        $sessionDir=Join-Path $positiveCodexHome ('sessions/{0:D4}/{1:D2}/{2:D2}' -f [DateTime]::UtcNow.Year, [DateTime]::UtcNow.Month, [DateTime]::UtcNow.Day)
        [void][IO.Directory]::CreateDirectory($sessionDir)
        $hostSessionId=[guid]::NewGuid().ToString()
        $env:CODEX_SESSION_ID=$hostSessionId
        [IO.File]::WriteAllLines((Join-Path $sessionDir ('rollout-2026-09-12T00-00-00-{0}.jsonl' -f $hostSessionId)), @(
            (@{timestamp='2026-09-12T00:00:00Z';ordinal=0;type='session_meta';payload=[ordered]@{session_id=$hostSessionId;id=$hostSessionId;originator='codex_exec';cli_version='0.154.0'}}|ConvertTo-Json -Depth 10 -Compress),
            (@{timestamp='2026-09-12T00:00:00Z';ordinal=1;type='turn_context';payload=[ordered]@{turn_id='host-turn';model='gpt-5.6-luna';effort='medium';approval_policy='never'}}|ConvertTo-Json -Depth 10 -Compress)
        ))
        $script:positiveSessions=[System.Collections.Concurrent.ConcurrentQueue[string]]::new()
        function Invoke-BFManagedWorker {
            param($State,$Stage,$Prompt,$Directory,$CodexPath,$Cancelled,$MaxOutputBytes)
            [void][IO.Directory]::CreateDirectory($Directory)
            # A deterministic fresh-context session id per dispatch.
            $sessionId=[guid]::NewGuid().ToString()
            [void]$script:positiveSessions.Enqueue($sessionId)
            $rolloutPath=Join-Path $sessionDir ('rollout-2026-09-12T00-00-00-{0}.jsonl' -f $sessionId)
            $turnContext=@{timestamp='2026-09-12T00:00:00Z';ordinal=1;type='turn_context';payload=[ordered]@{turn_id='fixture-turn';model='gpt-5.6-luna';effort='medium';approval_policy='never'}}
            [IO.File]::WriteAllLines($rolloutPath, @((@{timestamp='2026-09-12T00:00:00Z';ordinal=0;type='session_meta';payload=[ordered]@{session_id=$sessionId;id=$sessionId;originator='codex_exec';cli_version='0.154.0'}}|ConvertTo-Json -Depth 10 -Compress), ($turnContext|ConvertTo-Json -Depth 10 -Compress)))
            $resultPath=Join-Path $Directory 'model-result.json'
            $hostPath=Join-Path $Directory 'host-result.json'
            # Role-appropriate fixture payloads; the chair must emit the complete
            # final specification with resolvable references.
            $roleMarker=[regex]::Match($Prompt,'(?m)^Role: (\w+)$').Groups[1].Value
            $specText=[regex]::Match($Prompt,'(?s)BEGIN UNTRUSTED DATA: spec\.md>>>[\r\n]+(?<spec>.*?)[\r\n]+<<<END UNTRUSTED DATA: spec\.md').Groups['spec'].Value
            if($roleMarker -eq 'chair'){
                $block=[regex]::Match($Prompt,'(?s)BEGIN TRUSTED AGGREGATES.*?>>>[\r\n]+(?<json>\{.*\})[\r\n]+<<<END TRUSTED AGGREGATES')
                $aggregate=$block.Groups['json'].Value|ConvertFrom-Json -ErrorAction Stop
                $pd=@();foreach($item in @($aggregate.protected)){$pd+=[ordered]@{composite_id=[string]$item.composite_id;decision='preserved';reason='fixture scope';evidence='fixture'}}
                $rr=@();foreach($req in @($aggregate.requirements)){$rr+=[ordered]@{id=[string]$req.id;final_refs=@('Требуемое поведение / 1')}}
                $payload=[ordered]@{verdict='PASS';decisions=@();protected_decisions=@($pd);requirement_refs=@($rr);final_spec_text=$specText;final_design_text=$null}
            }else{
                $payload=[ordered]@{role=$roleMarker;verdict='PASS';findings=@();do_not_change=@();needs_input_questions=@()}
            }
            [IO.File]::WriteAllText($resultPath,(ConvertTo-Json -InputObject ([ordered]@{schema_version=1;status='completed';summary='Fixture current-agent worker.';payload_json=(ConvertTo-Json -InputObject $payload -Depth 10)}) -Depth 10))
            $stdout=Join-Path $Directory 'stdout.txt'
            [IO.File]::WriteAllLines($stdout,@(
                (@{type='thread.started';thread_id=$sessionId}|ConvertTo-Json -Compress),
                (@{type='turn.started'}|ConvertTo-Json -Compress),
                (@{type='item.completed';item=@{id='answer';type='agent_message';text=(Get-Content -Raw $resultPath)}}|ConvertTo-Json -Depth 10 -Compress),
                (@{type='turn.completed';usage=@{input_tokens=1;cached_input_tokens=0;output_tokens=1}}|ConvertTo-Json -Depth 10 -Compress)
            ))
            [IO.File]::WriteAllText((Join-Path $Directory 'exit.json'),(ConvertTo-Json -InputObject ([ordered]@{exit_code=0;stop_reason=$null;stdout=$stdout;stderr=(Join-Path $Directory 'stderr.txt')}) -Depth 5))
            $process=Read-BFJson (Join-Path $Directory 'exit.json')
            $session=$null;$usage=$null;$turnComplete=$false
            foreach($line in [IO.File]::ReadLines($process.stdout)){
                if([string]::IsNullOrWhiteSpace($line)){continue}
                $event=ConvertFrom-Json -InputObject $line -ErrorAction Stop
                if($event.type -eq 'thread.started'){$session=$event.thread_id}
                if($event.type -eq 'turn.completed'){$turnComplete=$true;$usage=$event.usage}
            }
            if(-not $session -or -not $turnComplete){throw 'Fixture worker produced incomplete session evidence.'}
            $observed=Get-BFObservedModelEffort -SessionId $session
            $metadata=[ordered]@{session_id=$session;turn_id=$observed.turn_id;rollout_path=$observed.rollout_path;requested_model=$State.request.models.reviewer;requested_effort=$State.request.models.reviewer_effort;observed_model=$observed.observed_model;observed_effort=$observed.observed_effort;usage=$usage;usage_source=$process.stdout}
            Write-BFJson -Path $hostPath -Value $metadata
            return (Read-BFJson $resultPath)
        }
        $positiveProfile=[pscustomobject]@{provider='codex';executable_sha256='be96b992178b1e467c225800da0d65f2c86d5eba1ef0b14632f65db381cbdfde'}
        $positiveState=[pscustomobject]@{project_path=$positiveTemp;task_id=[guid]::NewGuid().ToString();request=[pscustomobject]@{models=[pscustomobject]@{worker='fixture-worker';reviewer='fixture-reviewer';reviewer_effort='medium'};timeout_seconds=1800;execution_profile=$positiveProfile}}
        # The host-proof adapter is exercised through its public managed-review
        # seam. This fixture supplies a proof-shaped capability and a valid,
        # content-addressed catalog source without a native Codex process.
        function Test-BFProfiledCodexHostCapability {
            param($State,$Directory,$CodexPath,$Cancelled)
            $catalogSourcePath=Join-Path $script:positiveTemp 'critic-catalog-source.json'
            return [ordered]@{
                capability_version='fixture-current-agent-v1'; provider='current_agent'; model='gpt-5.6-luna'; effort='medium'
                fresh_context=$true; sealed=$true; terminal=$true; source='fixture-host-proof'
                executable_sha256=('a'*64); sandbox_sha256=('b'*64); catalog_sha256=('c'*64); skills_sha256=('d'*64)
                catalog_source_path=$catalogSourcePath; catalog_source_sha256=(Get-BFFileHash $catalogSourcePath)
            }
        }
        $positiveChange=Get-BFChangePath $positiveState
        [void][IO.Directory]::CreateDirectory($positiveChange)
        # The council snapshot requires a proper Требуемое поведение manifest, so
        # this scenario reuses the packaged council spec fixture.
        $councilFixture=Join-Path $root 'openspec\changes\api-specification-council'
        Copy-Item -LiteralPath (Join-Path $councilFixture 'spec.md') -Destination (Join-Path $positiveChange 'spec.md')
        Copy-Item -LiteralPath (Join-Path $councilFixture 'original-task.md') -Destination (Join-Path $positiveChange 'original-task.md')
        # No token_env value is set anywhere: credential_source is missing for the
        # only provider, and every role falls back to current_agent.
        [IO.File]::WriteAllText((Join-Path $positiveTemp 'bsl-flow.yaml'),"llm:`n  providers:`n    fixture:`n      protocol: openai_compatible`n      base_url: https://fixture.invalid/v1`n      token_env: POSITIVE_FIXTURE_MISSING_TOKEN`n  models:`n    astra:`n      provider: fixture`n      model: fixture-model`n      effort: medium`nreview:`n  council:`n    enabled: true`n    max_parallel: 1`n    allow_local_http: false`n    legacy_mode: block`n    roles:`n      brainstorm:`n        enabled: false`n        required: false`n        model: astra`n        fallback: current_agent`n      intent_critic:`n        enabled: true`n        required: true`n        model: astra`n        fallback: current_agent`n      architecture_critic:`n        enabled: true`n        required: true`n        model: astra`n        fallback: current_agent`n      executability_critic:`n        enabled: true`n        required: true`n        model: astra`n        fallback: current_agent`n      chair:`n        enabled: true`n        required: true`n        model: astra`n        fallback: current_agent`n")
        Write-BFJson -Path (Join-Path $positiveTemp 'critic-catalog-source.json') -Value ([ordered]@{
            source_cache_path=(Join-Path $positiveTemp 'synthetic-model-cache.json'); source_cache_sha256=('f'*64)
            model=[ordered]@{slug='gpt-5.6-luna';use_responses_lite=$true;shell_type='unified_exec';apply_patch_tool_type='freeform';tool_mode='code_mode_only';experimental_supported_tools=@();base_instructions='fixture'}
        })
        $positiveReview=Invoke-BFProfileSpecCritic $positiveState (Join-Path $positiveTemp 'council') '' $null
        if(@($script:positiveSessions.ToArray()).Count -ne 4){throw 'Positive tokenless council did not run four fresh current-agent contexts.'};$checks++
        $memberRoles=@($positiveReview.members | Where-Object { $_.role -ne 'chair' })
        if($memberRoles.Count -ne 3){throw 'Positive tokenless council member envelopes missing.'};$checks++
        foreach($member in $memberRoles){
            if($member.execution_mode -ne 'current_agent_fallback'){throw 'Positive council member did not use current_agent fallback.'}
            if($member.observed.model -ne 'gpt-5.6-luna' -or $member.observed.effort -ne 'medium'){throw 'Positive council envelope did not publish rollout-observed identity.'}
        }
        $checks++
        if($positiveReview.diversity -ne 'multi_role_single_model' -or -not $positiveReview.fallback_visible){throw 'Positive tokenless council diversity/provenance wrong.'};$checks++

        # Public loading dot-sources the task modules in a local loader scope.
        # The returned callback must still work after that scope has exited;
        # exercise the real managed adapter with the fixture worker as its native
        # process boundary.
        $fixtureWorker = ${function:Invoke-BFManagedWorker}
        function New-ScopedManagedCouncilAdapter {
            param($State, [string]$Directory)
            $taskRoot = Join-Path (Split-Path $PSScriptRoot -Parent) 'global/skills/1c-task'
            foreach($module in @(
                'scripts/Task.Storage.ps1', 'scripts/Task.Process.ps1', 'scripts/Task.Contracts.ps1',
                'scripts/Task.Gates.ps1', 'scripts/Task.Engine.ps1', 'scripts/Task.Stages.ps1', 'scripts/Task.Provider.ps1',
                'adapters/Codex.Skills.ps1', 'adapters/Codex.ps1', 'adapters/ProfiledCodex.ps1'
            )) { . (Join-Path $taskRoot $module) }
            Set-Item -Path Function:Invoke-BFManagedWorker -Value $fixtureWorker
            return New-BFManagedCouncilHostAdapter -State $State -Directory $Directory -CodexPath ''
        }
        $scopedAdapter = New-ScopedManagedCouncilAdapter $positiveState (Join-Path $positiveTemp 'scoped-adapter')
        $scopedCapability = $scopedAdapter.capabilities['intent_critic']
        $scopedAttempt = [pscustomobject]@{ role='intent_critic'; sequence=99 }
        $scopedResult = & $scopedAdapter.fallback_runner $scopedAttempt 'Role: intent_critic' $scopedCapability
        if($scopedResult.status -ne 'completed' -or $scopedResult.observed_model -ne 'gpt-5.6-luna' -or $scopedResult.observed_effort -ne 'medium'){throw 'Returned managed fallback callback failed after its local loader scope exited.'};$checks++
    }
    finally {
        Remove-Item Env:CODEX_HOME -ErrorAction SilentlyContinue
        Remove-Item Env:CODEX_SESSION_ID -ErrorAction SilentlyContinue
        $resolvedPositive=[IO.Path]::GetFullPath($positiveTemp)
        if(-not $resolvedPositive.StartsWith([IO.Path]::GetFullPath([IO.Path]::GetTempPath()),[StringComparison]::OrdinalIgnoreCase)){throw 'Unsafe test cleanup target.'}
        Remove-Item -LiteralPath $resolvedPositive -Recurse -Force
    }
    Write-Output "MANAGED_REVIEW_OK checks=$checks"
} finally {
    $resolved=[IO.Path]::GetFullPath($temp)
    if(-not $resolved.StartsWith([IO.Path]::GetFullPath([IO.Path]::GetTempPath()),[StringComparison]::OrdinalIgnoreCase)){throw 'Unsafe test cleanup target.'}
    Remove-Item -LiteralPath $resolved -Recurse -Force
}
