#Requires -Version 7.0
# Experience memory contracts: closed versioned schemas, append-only hash
# chain, lifecycle and promotion policy, deduplication, contradiction and
# fingerprint invalidation, bounded deterministic retrieval, secret filtering,
# recovery from deleted/corrupt index and torn or forged events, disposition
# stability and old-task compatibility. No model, runtime or database calls.
[CmdletBinding()]param([string]$PackageRoot)
Set-StrictMode -Version Latest
$ErrorActionPreference='Stop'
if(-not $PackageRoot){$PackageRoot=Split-Path -Parent $PSScriptRoot}
$root=[IO.Path]::GetFullPath($PackageRoot).TrimEnd('\','/')
$core=Join-Path $root 'global/skills/1c-task/scripts'
foreach($module in @('Task.Storage.ps1','Task.Contracts.ps1','Task.Architecture.ps1','Task.Gates.ps1','Task.Process.ps1','Task.Engine.ps1','Task.Stages.ps1')){. (Join-Path $core $module)}
. (Join-Path $core 'Task.Memory.ps1')
$schemaRoot=Join-Path $root 'global/skills/1c-task/schemas'
$script:checks=0
function Assert-M([bool]$Condition,[string]$Message){if(-not $Condition){throw "ASSERTION FAILED: $Message"};$script:checks++}
function Assert-MThrows([scriptblock]$Body,[string]$Pattern){$message='';try{& $Body|Out-Null}catch{$message=$_.Exception.Message};Assert-M ($message -match $Pattern) "Expected $Pattern, got: $message"}
function Write-Fixture([string]$Path,[string]$Text){[void][IO.Directory]::CreateDirectory((Split-Path $Path -Parent));[IO.File]::WriteAllText($Path,$Text,(New-Object Text.UTF8Encoding($false)))}
function New-MemProject{
    $project=Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-memory-'+[guid]::NewGuid().ToString('N'))
    [void][IO.Directory]::CreateDirectory($project)
    return $project
}
function New-MemState([string]$Project,[string]$TaskId,[string]$PolicyHash){
    return [pscustomobject]@{
        task_id=$TaskId; project_path=$Project; policy_hash=$PolicyHash
        request=[pscustomobject]@{mode='implement';prompt='Apply the bounded fixture request.';analysis_goal='analysis';complexity='S';risk='low';impact_flags=@();source_paths=@('src/hello.txt');criteria=@([pscustomobject]@{id='fixture';observation='The fixture file contains the expected text.';kind='file_assertion';path='src/hello.txt';contains='fixture'})}
        classification=[pscustomobject]@{complexity='S';risk='low';impact_flags=@();rationale='Trusted fixture classification.'}
        # The production state carries a controller/entrypoint identity in its
        # policy snapshot. Keep the fixture boundary equally complete so the
        # mandatory fingerprint comparison exercises the real promotion gate.
        policy_files=@([pscustomobject]@{path='Invoke-BSLFlowTask.ps1';sha256=('h'*64)})
        policy_rules=[pscustomobject]@{self_learning_memory_enabled=$true}
    }
}
function Get-FixtureFingerprints([string]$Project,[string]$PolicyHash){
    return Get-BFMemoryFingerprints (New-MemState $Project ([guid]::NewGuid().ToString()) $PolicyHash)
}
function New-MemObservation{
    return [ordered]@{scope=@('src/hello.txt');observation='Always run the bounded static lint before declaring the verify stage complete.';action_type='recommended';action='Run the bounded lint before verify.';knowledge_class='procedural';risk_class='low'}
}
function New-MemResult([string]$TaskId,[string]$Stage,[string]$Outcome,[object]$Observations=$null,[string]$SideEffects='none',$Proposal=$null,[string]$Summary='Fixture result.'){
    $result=[ordered]@{schema_version=1;task_id=$TaskId;attempt_id=[guid]::NewGuid().ToString();stage=$Stage;outcome=$Outcome;summary=$Summary;side_effects=$SideEffects;proposal=$Proposal}
    if($null -ne $Observations){$result.proposal=[ordered]@{changed_files=@('src/hello.txt');observations=$Observations}}
    return $result
}
function New-MemAcceptanceReceipt([string]$TaskId){
    return [ordered]@{schema_version=1;task_id=$TaskId;verdict='PASS';gates=@([ordered]@{stage='implement';attempt_id=[guid]::NewGuid().ToString();result_sha256=('a'*64)},[ordered]@{stage='verify';attempt_id=[guid]::NewGuid().ToString();result_sha256=('b'*64)})}
}
function Add-MemAcceptedTemplate($State){
    $receipt=New-MemAcceptanceReceipt $State.task_id
    return Add-BFMemoryFromAcceptance $State $receipt (Get-BFHash $receipt)
}
function Submit-MemItem([string]$Project,$Item,[string]$TaskId,$Fingerprints,[string]$Reason='accepted-result',[switch]$ControllerTemplate,[string]$EvidenceKind='accepted-result'){
    # One validated session per submission against the fresh replayed state.
    $replay=Get-BFMemoryReplay $Project
    $provenance=if($ControllerTemplate){'controller-template'}else{''}
    $plan=Get-BFMemoryCandidatePlan -Records $replay.records -ProjectId $Project -Fingerprints $Fingerprints -Item $Item -SourceTaskId $TaskId -SourceAttemptId ([guid]::NewGuid().ToString()) -EvidenceRefs @([ordered]@{kind='attempt-result';sha256=('f'*64)}) -Reason $Reason -EvidenceKind $EvidenceKind -Provenance $provenance -TaskKind 'implement|analysis|criteria=file_assertion|flags='
    [void](Add-BFMemoryEvents -ProjectPath $Project -ProjectId $Project -Fingerprints $Fingerprints -Plan $plan)
}
function Get-MemEventCount([string]$Project){
    return @(Get-BFMemoryReplay $Project).events.Count
}
function Assert-MSchemaOk([string]$Json,[string]$SchemaFile,[string]$Message){
    $ok=$false
    try { $ok=[bool](Test-Json -Json $Json -SchemaFile $SchemaFile -ErrorAction Stop) } catch { $ok=$false }
    Assert-M $ok "$Message"
}

# --- 0. Self-learning memory is opt-in and absent policy stays disabled -------
$project=New-MemProject
try {
    $state=New-MemState $project ([guid]::NewGuid().ToString()) ('p'*64)
    $state.PSObject.Properties.Remove('policy_rules')
    $rules=Get-BFProjectRules $project
    Assert-M ($rules.self_learning_memory_enabled -eq $false) 'Missing project policy did not default self-learning memory off.'
    Write-Fixture (Join-Path $project 'bsl-flow.yaml') "features:`n  self_learning_memory:`n    enabled: true`n"
    $rules=Get-BFProjectRules $project
    Assert-M ($rules.self_learning_memory_enabled -eq $true) 'Explicit project opt-in did not enable self-learning memory.'
    Write-Fixture (Join-Path $project 'bsl-flow.yaml') "features:`n  self_learning_memory:`n    enabled: maybe`n"
    Assert-MThrows { Get-BFProjectRules $project } 'Expected true or false for features.self_learning_memory.enabled'
    Remove-Item -LiteralPath (Join-Path $project 'bsl-flow.yaml') -Force
    $result=New-MemResult $state.task_id 'verify' 'FAIL' $null 'none' ([ordered]@{criterion_id='fixture';kind='file_assertion'})
    Assert-M ($null -eq (Add-BFMemoryFromAttempt $state $result (Get-BFHash $result))) 'Default-off task extracted attempt memory.'
    Assert-M ($null -eq (Add-BFMemoryFromAcceptance $state (New-MemAcceptanceReceipt $state.task_id) ('a'*64))) 'Default-off task extracted acceptance memory.'
    Assert-M ($null -eq (Add-BFMemoryFromRecovery $state ([ordered]@{}) ([ordered]@{}) ('b'*64))) 'Default-off task extracted recovery memory.'
    $binding=Add-BFMemoryAttemptBinding $state 'inspect'
    Assert-M ($binding.available -eq $false -and $binding.disabled_reason -ceq 'disabled-by-project-policy') 'Default-off attempt binding was not explicitly disabled.'
    $projection=Get-BFMemoryProjection $state ([ordered]@{action='dispatch';stage='inspect';blockers=@()})
    Assert-M ($projection.available -eq $false -and $projection.blocker -ceq 'disabled-by-project-policy' -and $null -eq $projection.bundle) 'Default-off context projection was not explicitly disabled.'
    Assert-M (-not (Test-Path -LiteralPath (Join-Path $project '.bsl-flow/memory'))) 'Default-off task created a memory store.'
} finally { Remove-Item -LiteralPath $project -Recurse -Force }

# --- 1. Closed versioned schemas over real artifacts -------------------------
$project=New-MemProject
try {
    $state=New-MemState $project ([guid]::NewGuid().ToString()) ('p'*64)
    $result=New-MemResult $state.task_id 'implement' 'PASS' @(New-MemObservation)
    $created=Add-BFMemoryFromAttempt $state $result (Get-BFHash $result)
    Assert-M ($null -ne $created) 'Extractor did not audit untrusted worker observations.'
    Assert-M (1 -eq (Get-MemEventCount $project) -and 'rejected' -ceq (Get-BFMemoryReplay $project).events[0].event_type -and 'worker-prose-untrusted' -ceq (Get-BFMemoryReplay $project).events[0].reason) 'Worker observation was not rejected before task acceptance.'
    $receipt=New-MemAcceptanceReceipt $state.task_id
    $receiptHash=Get-BFHash $receipt
    Assert-M ($null -ne (Add-BFMemoryFromAcceptance $state $receipt $receiptHash)) 'Controller acceptance template did not create a candidate.'
    $afterAcceptance=Get-MemEventCount $project
    Assert-M ($null -eq (Add-BFMemoryFromAcceptance $state $receipt $receiptHash) -and $afterAcceptance -eq (Get-MemEventCount $project)) 'Repeated identical acceptance receipt was not idempotent.'
    Assert-M (2 -eq (Get-MemEventCount $project)) 'Acceptance extraction emitted an unexpected event count.'
    $eventFile=@(Get-ChildItem -LiteralPath (Join-Path $project '.bsl-flow/memory/events') -File | Where-Object { $_.Name -ne '000001.json' })[0]
    Assert-MSchemaOk ([IO.File]::ReadAllText($eventFile.FullName)) (Join-Path $schemaRoot 'memory-event.schema.json') 'Candidate event does not satisfy memory-event.schema.json.'
    Assert-MSchemaOk ([IO.File]::ReadAllText((Join-Path $project '.bsl-flow/memory/index.json'))) (Join-Path $schemaRoot 'memory-index.schema.json') 'Persisted index does not satisfy memory-index.schema.json.'
    $bundle=Get-BFMemoryBundleFromReplay (Get-BFMemoryReplay $project) $project (Get-BFMemoryFingerprints $state) 'implement' @('.')
    Assert-MSchemaOk (Get-BFCanonicalJson $bundle) (Join-Path $schemaRoot 'memory-bundle.schema.json') 'Stage bundle does not satisfy memory-bundle.schema.json.'
    # Closed contract: unknown fields and unknown enums fail closed.
    $mutated=Read-BFJson $eventFile.FullName
    $mutated | Add-Member -NotePropertyName unexpected -NotePropertyValue 'no'
    Assert-MThrows { Assert-BFMemoryEventShape $mutated } 'Unknown memory event field'
    $badEnum=Read-BFJson $eventFile.FullName
    $badEnum.event_type='invented'
    Assert-MThrows { Assert-BFMemoryEventShape $badEnum } 'Unknown memory event type'
    $recordId=@((Get-BFMemoryReplay $project).records.Keys)[0]
    Assert-M ($recordId -match '^[0-9a-f]{64}$') 'Record identity is not a canonical hash.'
    $replay=Get-BFMemoryReplay $project
    Assert-M ('candidate' -ceq [string](Get-BFValue $replay.records[$recordId] 'state')) 'Fresh candidate did not replay as candidate.'
    Assert-M (1 -eq [int](Get-BFValue $replay.records[$recordId] 'confirmations')) 'The accepted source outcome did not seed the first confirmation.'
    Assert-M ('controller-template:accepted-source-only-v1' -ceq [string](Get-BFValue $replay.records[$recordId] 'last_reason')) 'Candidate lost its explainable reason.'
    Assert-M ('controller-template' -ceq [string](Get-BFValue $replay.records[$recordId] 'provenance')) 'Candidate provenance was not controller-owned.'
    Assert-M ('acceptance-receipt' -ceq [string](Get-BFValue (Get-BFValue $replay.records[$recordId] 'evidence_ref') 'kind')) 'Candidate lost its bounded evidence reference.'
    $disguised=New-MemResult $state.task_id 'implement' 'PASS' @([ordered]@{scope=@('src/hello.txt');observation='For this task set the customer default payment term to 30 days.';action_type='recommended';action='Apply the customer default.';knowledge_class='procedural';risk_class='low'})
    [void](Add-BFMemoryFromAttempt $state $disguised (Get-BFHash $disguised))
    $replay=Get-BFMemoryReplay $project
    Assert-M (1 -eq @($replay.records.Keys).Count -and 2 -eq (@($replay.events | Where-Object { $_.event_type -ceq 'rejected' -and $_.reason -ceq 'worker-prose-untrusted' })).Count) 'Business prose disguised as procedural worker output was retained as memory.'
} finally { Remove-Item -LiteralPath $project -Recurse -Force }

# --- 2. Promotion matrix, cross-task deduplication, extraction idempotency ----
$project=New-MemProject
try {
    $fingerprints=Get-FixtureFingerprints $project ('p'*64)
    $item=[ordered]@{scope=(ConvertTo-BFMemoryScope 'implement' @('src/hello.txt'));observation='Reuse the deterministic fixture helper for repeat greetings.';action=[ordered]@{type='recommended';text='Reuse the deterministic fixture helper.'};knowledge_class='procedural';risk_class='low'}
    $recordId=Get-BFMemoryRecordId $project $item.scope $item.action $fingerprints
    $taskA=[guid]::NewGuid().ToString(); $taskB=[guid]::NewGuid().ToString(); $taskC=[guid]::NewGuid().ToString()
    Submit-MemItem $project $item $taskA $fingerprints -ControllerTemplate
    Submit-MemItem $project $item $taskA $fingerprints -ControllerTemplate
    $index=Get-BFMemoryReplay $project
    Assert-M ('candidate' -ceq [string](Get-BFValue $index.records[$recordId] 'state') -and 2 -eq [int](Get-BFValue $index.records[$recordId] 'confirmations') -and 1 -eq @(Get-BFValue $index.records[$recordId] 'confirmation_tasks').Count) 'Same-task repeat must confirm without shadow transition or a second record.'
    Submit-MemItem $project $item $taskB $fingerprints -ControllerTemplate
    $index=Get-BFMemoryReplay $project
    Assert-M ('accepted' -ceq [string](Get-BFValue $index.records[$recordId] 'state')) 'Three consistent confirmations from two tasks did not promote the record.'
    Assert-M ('promotion policy satisfied' -ceq [string](Get-BFValue $index.records[$recordId] 'last_reason')) 'Promotion event lost its explainable reason.'
    Assert-M (1 -eq (@($index.events | Where-Object { $_.record_id -ceq $recordId -and $_.event_type -ceq 'shadow' -and $_.reason -ceq 'first cross-task confirmation' })).Count) 'The cross-task shadow transition event is missing.'
    Assert-M (1 -eq (@($index.events | Where-Object { $_.record_id -ceq $recordId -and $_.event_type -ceq 'promoted' })).Count) 'The promotion event is missing.'
    Assert-M (5 -eq (Get-MemEventCount $project)) 'Lifecycle changes were not emitted as separate events.'
    $firstEvent=@(Get-BFMemoryReplay $project).events[0]
    Assert-M ('candidate' -ceq [string]$firstEvent.event_type) 'History was overwritten; the original candidate event disappeared.'
    # Re-recording the same attempt result registers exactly one confirmation, then deduplicates.
    $state=New-MemState $project $taskA ('p'*64)
    $obs=[ordered]@{scope=@('src/hello.txt');observation='Reuse the deterministic fixture helper for repeat greetings.';action_type='recommended';action='Reuse the deterministic fixture helper.';knowledge_class='procedural';risk_class='low'}
    $result=New-MemResult $taskA 'implement' 'PASS' @($obs)
    $hash=Get-BFHash $result
    $before=Get-MemEventCount $project
    [void](Add-BFMemoryFromAttempt $state $result $hash)
    $afterFirst=Get-MemEventCount $project
    [void](Add-BFMemoryFromAttempt $state $result $hash)
    Assert-M ($afterFirst -eq (Get-MemEventCount $project)) 'Repeated extraction of the same result was not deduplicated.'
    Assert-M ($before+1 -eq $afterFirst -and [int](Get-BFValue (Get-BFMemoryReplay $project).records[$recordId] 'confirmations') -eq 3) 'Worker prose unexpectedly registered a confirmation.'
    # Promotion gate: medium-risk procedural never promotes regardless of confirmations.
    $mediumItem=[ordered]@{scope=(ConvertTo-BFMemoryScope 'implement' @('src/other.txt'));observation='Medium risk procedural observation.';action=[ordered]@{type='recommended';text='Medium risk action.'};knowledge_class='procedural';risk_class='medium'}
    $mediumId=Get-BFMemoryRecordId $project $mediumItem.scope $mediumItem.action $fingerprints
    Submit-MemItem $project $mediumItem $taskA $fingerprints -ControllerTemplate
    for($i=0;$i -lt 6;$i++){ Submit-MemItem $project $mediumItem ([guid]::NewGuid().ToString()) $fingerprints -ControllerTemplate }
    $index=Get-BFMemoryReplay $project
    Assert-M ('shadow' -ceq [string](Get-BFValue $index.records[$mediumId] 'state')) 'Medium-risk procedural knowledge was auto-promoted.'
    Assert-M (7 -eq [int](Get-BFValue $index.records[$mediumId] 'confirmations')) 'Confirmation counting failed for repeated confirmations.'
    # Non-proposable classes become audited rejections and never create records.
    $stateA=New-MemState $project $taskA ('p'*64)
    $rejectedResult=New-MemResult $taskA 'implement' 'PASS' @([ordered]@{scope=@('src/hello.txt');observation='Business rule observation.';action_type='recommended';action='Apply the rule.';knowledge_class='business_rule';risk_class='low'})
    [void](Add-BFMemoryFromAttempt $stateA $rejectedResult (Get-BFHash $rejectedResult))
    $index=Get-BFMemoryReplay $project
    Assert-M (1 -eq (@($index.events | Where-Object { $_.event_type -ceq 'rejected' -and $_.reason -ceq 'class-not-proposable' })).Count) 'Non-proposable knowledge class did not become an audited rejection.'
    Assert-M (0 -eq (@($index.events | Where-Object { $_.event_type -ceq 'candidate' -and $_.knowledge_class -ceq 'business_rule' })).Count) 'A business rule was recorded from worker output.'
} finally { Remove-Item -LiteralPath $project -Recurse -Force }

# --- 3. Contradiction, quarantine, superseding, reinstatement -----------------
$project=New-MemProject
try {
    $fingerprints=Get-FixtureFingerprints $project ('q'*64)
    $scope=ConvertTo-BFMemoryScope 'implement' @('src/greet.bsl')
    $positive=[ordered]@{scope=$scope;observation='Greeting should be uppercase.';action=[ordered]@{type='recommended';text='Uppercase the greeting.'};knowledge_class='procedural';risk_class='low'}
    $negative=[ordered]@{scope=$scope;observation='Uppercase greetings were rejected.';action=[ordered]@{type='avoid';text='Uppercase the greeting.'};knowledge_class='procedural';risk_class='low'}
    $positiveId=Get-BFMemoryRecordId $project $positive.scope $positive.action $fingerprints
    $negativeId=Get-BFMemoryRecordId $project $negative.scope $negative.action $fingerprints
    Submit-MemItem $project $positive ([guid]::NewGuid().ToString()) $fingerprints -ControllerTemplate
    Submit-MemItem $project $positive ([guid]::NewGuid().ToString()) $fingerprints -ControllerTemplate
    Submit-MemItem $project $positive ([guid]::NewGuid().ToString()) $fingerprints -ControllerTemplate
    Assert-M ('accepted' -ceq [string](Get-BFValue (Get-BFMemoryReplay $project).records[$positiveId] 'state')) 'Fixture record did not reach accepted.'
    Submit-MemItem $project $negative ([guid]::NewGuid().ToString()) $fingerprints -ControllerTemplate
    $index=Get-BFMemoryReplay $project
    Assert-M ('superseded' -ceq [string](Get-BFValue $index.records[$positiveId] 'state') -and (Get-BFValue $index.records[$positiveId] 'superseded_by') -ceq $negativeId) 'Contradicting evidence did not supersede the accepted record.'
    Assert-M ('candidate' -ceq [string](Get-BFValue $index.records[$negativeId] 'state')) 'Contradicting candidate was not registered as its own record.'
    Assert-M (1 -eq [int](Get-BFValue $index.records[$positiveId] 'contradictions')) 'Contradiction counter was not recorded before superseding.'
    # Same contradiction against an unpromoted record quarantines it.
    $positive2=[ordered]@{scope=$scope;observation='Another positive observation.';action=[ordered]@{type='recommended';text='Uppercase greeting twice.'};knowledge_class='diagnostic';risk_class='low'}
    $negative2=[ordered]@{scope=$scope;observation='Doubling was rejected.';action=[ordered]@{type='avoid';text='Uppercase greeting twice.'};knowledge_class='diagnostic';risk_class='low'}
    $positive2Id=Get-BFMemoryRecordId $project $positive2.scope $positive2.action $fingerprints
    Submit-MemItem $project $positive2 ([guid]::NewGuid().ToString()) $fingerprints -ControllerTemplate
    Submit-MemItem $project $negative2 ([guid]::NewGuid().ToString()) $fingerprints -ControllerTemplate
    $index=Get-BFMemoryReplay $project
    Assert-M ('quarantined' -ceq [string](Get-BFValue $index.records[$positive2Id] 'state')) 'Contradiction did not quarantine the unpromoted record.'
    # Return from quarantine requires an explicit event and keeps the reason in history.
    [void](Move-BFMemoryRecord $project $positive2Id 'reinstated' 'Operator confirmed the quarantine reason was obsolete.')
    $index=Get-BFMemoryReplay $project
    Assert-M ('shadow' -ceq [string](Get-BFValue $index.records[$positive2Id] 'state')) 'Reinstatement did not return the record as shadow.'
    Assert-M (1 -eq (@($index.events | Where-Object { $_.record_id -ceq $positive2Id -and $_.event_type -ceq 'quarantined' })).Count) 'Quarantine reason was not preserved in history.'
    # Illegal transitions fail closed.
    Assert-MThrows { Move-BFMemoryRecord $project $negativeId 'promoted' 'threshold not met' } 'Promotion requires'
    Assert-MThrows { Move-BFMemoryRecord $project $positiveId 'quarantined' 'already superseded' } 'active record'
    $archItem=[ordered]@{scope=$scope;observation='Layering observation.';action=[ordered]@{type='recommended';text='Keep the adapter thin.'};knowledge_class='architecture';risk_class='low'}
    $archId=Get-BFMemoryRecordId $project $archItem.scope $archItem.action $fingerprints
    Submit-MemItem $project $archItem ([guid]::NewGuid().ToString()) $fingerprints -ControllerTemplate
    Assert-MThrows { Move-BFMemoryRecord $project $archId 'promoted' 'attempted authority expansion' } 'procedural'
} finally { Remove-Item -LiteralPath $project -Recurse -Force }

# --- 4. Fingerprint invalidation: read excludes, write quarantines ------------
$project=New-MemProject
try {
    $state=New-MemState $project ([guid]::NewGuid().ToString()) ('oldp'*16)
    [void](Add-MemAcceptedTemplate $state)
    $recordId=@((Get-BFMemoryReplay $project).records.Keys)[0]
    $state2=New-MemState $project ([guid]::NewGuid().ToString()) ('newp'*16)
    # Read-only projection excludes the record without writing anything.
    $eventsBefore=Get-MemEventCount $project
    $context=Get-BFMemoryProjection $state2 ([ordered]@{action='dispatch';stage='implement';blockers=@()})
    Assert-M ($context.available -eq $true) 'Projection disabled memory without damage.'
    Assert-M (0 -eq @($context.records).Count) 'Stale-fingerprint record leaked into the active projection.'
    Assert-M (1 -eq (@($context.bundle.excluded | Where-Object { $_.reason -ceq 'fingerprint-mismatch' })).Count) 'Fingerprint exclusion lost its explicit reason.'
    Assert-M ($eventsBefore -eq (Get-MemEventCount $project)) 'Read-only projection wrote invalidation events.'
    # Write path quarantines the stale record as an audited event.
    $binding=Add-BFMemoryAttemptBinding $state2 'implement'
    Assert-M ($binding.available -eq $true -and 0 -eq @($binding.records).Count) 'Attempt binding did not exclude the stale record.'
    $index=Get-BFMemoryReplay $project
    Assert-M ('quarantined' -ceq [string](Get-BFValue $index.records[$recordId] 'state')) 'Stale fingerprint did not quarantine the record.'
    Assert-M (1 -eq (@($index.events | Where-Object { $_.event_type -ceq 'quarantined' -and $_.reason -ceq 'fingerprint-mismatch' })).Count) 'Fingerprint invalidation was not audited.'
} finally { Remove-Item -LiteralPath $project -Recurse -Force }

# --- 5. Bounded deterministic retrieval, scoping, working set ------------------
$project=New-MemProject
try {
    $state=New-MemState $project ([guid]::NewGuid().ToString()) ('r'*64)
    $fingerprints=Get-FixtureFingerprints $project ('r'*64)
    for($i=0;$i -lt 8;$i++){
        $item=[ordered]@{scope=(ConvertTo-BFMemoryScope 'implement' @('src/hello.txt'));observation=('Accepted technique number {0} with enough durable detail to identify it.' -f $i);action=[ordered]@{type='recommended';text=('Action {0}' -f $i)};knowledge_class='procedural';risk_class='low'}
        Submit-MemItem $project $item ([guid]::NewGuid().ToString()) $fingerprints -ControllerTemplate
    }
    $replay=Get-BFMemoryReplay $project
    Assert-M (8 -eq @($replay.records.Keys).Count) 'Fixture records were not all created.'
    $bundle=Get-BFMemoryBundleFromReplay $replay $project $fingerprints 'implement' @('.')
    Assert-M ($script:BFMemoryMaxRecords -eq @($bundle.records).Count) 'Bundle record limit was not applied.'
    Assert-M ((@($bundle.excluded) | ForEach-Object { $_.reason }) -contains 'limit-records') 'Over-limit records were not excluded with an explicit reason.'
    $again=Get-BFMemoryBundleFromReplay (Get-BFMemoryReplay $project) $project $fingerprints 'implement' @('.')
    Assert-M ($again.bundle_id -ceq $bundle.bundle_id) 'Bundle selection was not reproducible on identical inputs.'
    $ids=@($bundle.records | ForEach-Object { $_.record_id })
    Assert-M ((@($ids) -join '|') -ceq ((@($ids | Sort-Object { $_ })) -join '|')) 'Same-rank records were not ordered by the stable record_id tie-breaker.'
    $verifyBundle=Get-BFMemoryBundleFromReplay $replay $project $fingerprints 'verify' @('.')
    Assert-M (0 -eq @($verifyBundle.records).Count -and ((@($verifyBundle.excluded) | ForEach-Object { $_.reason }) -contains 'stage-scope-mismatch')) 'Stage scope did not exclude implement records.'
    $scopedState=New-MemState $project ([guid]::NewGuid().ToString()) ('r'*64)
    $scopedState.request=[pscustomobject]@{mode='implement';source_paths=@('docs')}
    $scopedBundle=Get-BFMemoryBundleFromReplay (Get-BFMemoryReplay $project) $project (Get-BFMemoryFingerprints $scopedState) 'implement' @('docs')
    Assert-M (0 -eq @($scopedBundle.records).Count -and ((@($scopedBundle.excluded) | ForEach-Object { $_.reason }) -contains 'path-scope-mismatch')) 'Path scope did not exclude disjoint records.'
    Assert-M ((@($bundle.records) | Where-Object { $_.state -ceq 'candidate' }).Count -gt 0) 'Fresh candidates were not offered as recommendations.'
    foreach($record in @($bundle.records)){ Assert-M (-not [string]::IsNullOrWhiteSpace([string]$record.selected_reason)) 'A selected record lost its selection reason.' }
    # Diagnosis stage consumes verify-stage diagnostics.
    $failResult=[ordered]@{schema_version=1;task_id=[guid]::NewGuid().ToString();attempt_id=[guid]::NewGuid().ToString();stage='verify';outcome='FAIL';summary='Greeting check failed.';side_effects='none';proposal=[ordered]@{repair_eligible=$true;criterion_id='greeting';kind='file_assertion';observation='The source contains the requested description.'}}
    $diagState=New-MemState $project ([guid]::NewGuid().ToString()) ('r'*64)
    [void](Add-BFMemoryFromAttempt $diagState $failResult (Get-BFHash $failResult))
    $diagnoseBundle=Get-BFMemoryBundleFromReplay (Get-BFMemoryReplay $project) $project (Get-BFMemoryFingerprints $diagState) 'diagnose' @('.')
    Assert-M ((@($diagnoseBundle.records) | Where-Object { $_.knowledge_class -ceq 'diagnostic' }).Count -ge 1) 'Diagnose stage did not receive the verify-stage diagnostic.'
} finally { Remove-Item -LiteralPath $project -Recurse -Force }

# --- 6. Secret filtering, bounded free text, unknown effects -------------------
$project=New-MemProject
try {
    $state=New-MemState $project ([guid]::NewGuid().ToString()) ('s'*64)
    $secretResult=New-MemResult $state.task_id 'implement' 'PASS' @([ordered]@{scope=@('src/config.bsl');observation='Set password=TopSecretValue123 in the settings block.';action_type='recommended';action='Configure the token.';knowledge_class='procedural';risk_class='low'})
    [void](Add-BFMemoryFromAttempt $state $secretResult (Get-BFHash $secretResult))
    $index=Get-BFMemoryReplay $project
    Assert-M (1 -eq @($index.events).Count -and 'rejected' -ceq $index.events[0].event_type -and 'secret-like-content' -ceq $index.events[0].reason) 'Secret-like observation was not rejected with an audited marker.'
    Assert-M (0 -eq @($index.records.Keys).Count) 'Secret-like content created a record.'
    $longResult=New-MemResult $state.task_id 'implement' 'PASS' @([ordered]@{scope=@('src/a.bsl');observation=('x'*600);action_type='recommended';action='ok';knowledge_class='procedural';risk_class='low'})
    [void](Add-BFMemoryFromAttempt $state $longResult (Get-BFHash $longResult))
    $index=Get-BFMemoryReplay $project
    Assert-M (1 -eq (@($index.events | Where-Object { $_.event_type -ceq 'rejected' -and $_.reason -ceq 'observation-too-long' })).Count) 'Oversized observation was not rejected.'
    $failResult=[ordered]@{schema_version=1;task_id=$state.task_id;attempt_id=[guid]::NewGuid().ToString();stage='verify';outcome='FAIL';summary='Greeting check failed.';side_effects='none';proposal=[ordered]@{repair_eligible=$true;criterion_id='greeting';kind='file_assertion';observation='The source contains the requested description.'}}
    [void](Add-BFMemoryFromAttempt $state $failResult (Get-BFHash $failResult))
    $index=Get-BFMemoryReplay $project
    $diagnostic=@($index.records.Values | Where-Object { $_.knowledge_class -ceq 'diagnostic' })
    Assert-M (1 -eq @($diagnostic).Count -and 'avoid' -ceq [string](Get-BFValue $diagnostic[0].action 'type')) 'Confirmed error did not produce a bounded avoid-action diagnostic.'
    Assert-M (0 -eq [int](Get-BFValue $diagnostic[0] 'confirmations') -and 0 -eq (@($index.events | Where-Object { $_.event_type -ceq 'promoted' })).Count) 'Failed verification was allowed to confirm or promote memory.'
    Assert-M ([string]$diagnostic[0].observation -ceq 'The controller verifier recorded a declared criterion failure with retained evidence.') 'Diagnostic observation was not reduced to the controller-owned fixed template.'
    $failResult2=[ordered]@{schema_version=1;task_id=$state.task_id;attempt_id=[guid]::NewGuid().ToString();stage='verify';outcome='FAIL';summary='Second criterion failed.';side_effects='none';proposal=[ordered]@{repair_eligible=$true;criterion_id='other-criterion';kind='file_assertion';observation='The other source criterion failed.'}}
    [void](Add-BFMemoryFromAttempt $state $failResult2 (Get-BFHash $failResult2))
    $index=Get-BFMemoryReplay $project
    $diagnostics=@($index.records.Values | Where-Object { $_.knowledge_class -ceq 'diagnostic' })
    $secondSignature=Get-BFMemoryErrorSignature $failResult2
    $secondBundle=Get-BFMemoryBundleFromReplay $index $project (Get-BFMemoryFingerprints $state) 'diagnose' @('.') '' $secondSignature
    Assert-M (2 -eq @($diagnostics).Count -and 1 -eq @($secondBundle.records).Count -and $secondSignature -in @($secondBundle.records[0].error_signatures) -and (@($diagnostics | Where-Object { [int]$_.confirmations -gt 0 })).Count -eq 0) 'Distinct verify failure signatures were collapsed or promoted instead of retaining separate diagnostic applicability.'
    $unknownResult=[ordered]@{schema_version=1;task_id=$state.task_id;attempt_id=[guid]::NewGuid().ToString();stage='implement';outcome='FAIL';summary='unknown';side_effects='unknown';proposal=$null}
    $before=Get-MemEventCount $project
    [void](Add-BFMemoryFromAttempt $state $unknownResult (Get-BFHash $unknownResult))
    Assert-M ($before -eq (Get-MemEventCount $project)) 'Unknown-effect failure was treated as a confirmed error.'
    $untypedResult=[ordered]@{schema_version=1;task_id=$state.task_id;attempt_id=[guid]::NewGuid().ToString();stage='inspect';outcome='FAIL';summary='Worker operation failed.';side_effects='none';proposal=$null}
    $before=Get-MemEventCount $project
    [void](Add-BFMemoryFromAttempt $state $untypedResult (Get-BFHash $untypedResult))
    Assert-M ($before -eq (Get-MemEventCount $project)) 'Untyped worker failure was treated as a controller verification diagnostic.'
    Assert-MThrows { Assert-BFMemoryObservations @([ordered]@{scope=@('src/a.bsl');observation='ok';action_type='recommended';action='ok';knowledge_class='controller_policy';risk_class='low'}) } 'class-not-proposable'
    Assert-MThrows { Assert-BFMemoryObservations @([ordered]@{scope=@('../escape.bsl');observation='ok';action_type='recommended';action='ok';knowledge_class='procedural';risk_class='low'}) } 'forbidden-scope-path'
} finally { Remove-Item -LiteralPath $project -Recurse -Force }

# --- 7. Recovery: deleted/corrupt index, torn tail, broken chain ---------------
$project=New-MemProject
try {
    $state=New-MemState $project ([guid]::NewGuid().ToString()) ('v'*64)
    [void](Add-MemAcceptedTemplate $state)
    $authoritative=Get-BFMemoryReplay $project
    $eventsDir=Join-Path $project '.bsl-flow/memory/events'
    # A self-consistent derived index uses the bounded fast path: metadata plus
    # the authoritative head, without replaying every event.
    $source=Get-BFMemoryReadSource $project
    Assert-M ($source.indexed -eq $true -and $source.replayed -eq $false -and 1 -eq @($source.records.Keys).Count) 'Healthy self-consistent index did not use the bounded read path.'
    $indexPath=Join-Path $project '.bsl-flow/memory/index.json'
    $forgedIndex=Read-BFJson $indexPath
    $forgedIndex.last_event_id=('0'*64)
    Write-BFJson -Path $indexPath -Value $forgedIndex -Replace
    $source=Get-BFMemoryReadSource $project
    Assert-M ($source.indexed -eq $false -and $source.replayed -eq $true -and 1 -eq @($source.events).Count) 'A forged index head was accepted instead of rebuilding from authoritative events.'
    [void](Write-BFJson -Path $indexPath -Value (New-BFMemoryIndexObject -ProjectId $project -Events $authoritative.events -Records $authoritative.records -Torn $authoritative.torn -EventFilesCount $authoritative.event_files_count -LastEventSeq $authoritative.last_event_seq) -Replace)
    # Deleted derived index: read rebuilds in memory, next write restores it.
    Remove-Item -LiteralPath (Join-Path $project '.bsl-flow/memory/index.json') -Force
    $context=Get-BFMemoryProjection $state ([ordered]@{action='dispatch';stage='implement';blockers=@()})
    Assert-M ($context.available -eq $true -and $context.index.replayed -eq $true -and 1 -eq @($context.records).Count) 'Deleted index was not rebuilt from authoritative events.'
    $binding=Add-BFMemoryAttemptBinding $state 'implement'
    Assert-M ($binding.available -eq $true -and 1 -eq @($binding.records).Count) 'Attempt binding did not restore the rebuilt index.'
    Assert-M ([bool](Test-BFMemoryIndexFresh (Read-BFJson (Join-Path $project '.bsl-flow/memory/index.json')) $authoritative)) 'Restored index does not match the authoritative replay.'
    # Corrupt index file: projection still replays and stays available.
    Write-Fixture (Join-Path $project '.bsl-flow/memory/index.json') '{"schema_version":1,'
    $context=Get-BFMemoryProjection $state ([ordered]@{action='dispatch';stage='implement';blockers=@()})
    Assert-M ($context.available -eq $true -and 1 -eq @($context.records).Count) 'Corrupt index made memory unavailable although events were intact.'
    # Complete JSON corruption is a blocker. It must not be mistaken for a
    # crash-tail event and silently skipped.
    $invalidPath=Join-Path $eventsDir '000002.json'
    $invalid=Read-BFJson (Join-Path $eventsDir '000001.json')
    $invalid.schema_version=2
    Write-Fixture $invalidPath (Get-BFCanonicalJson $invalid)
    Assert-MThrows { Get-BFMemoryReplay $project } 'complete but invalid'
    Remove-Item -LiteralPath $invalidPath -Force
    $invalid=Read-BFJson (Join-Path $eventsDir '000001.json')
    $invalid.reason='tampered-complete-event'
    Write-Fixture $invalidPath (Get-BFCanonicalJson $invalid)
    Assert-MThrows { Get-BFMemoryReplay $project } 'failed its identity hash'
    Remove-Item -LiteralPath $invalidPath -Force
    $validEvent=Read-BFJson (Join-Path $eventsDir '000001.json')
    Write-Fixture $invalidPath ((Get-BFCanonicalJson $validEvent) + (' ' * ($script:BFMemoryMaxEventBytes)))
    Assert-MThrows { Get-BFMemoryReplay $project } 'exceeds the supported size'
    Remove-Item -LiteralPath $invalidPath -Force
    # Torn tail is skipped and reported; the chain keeps validating.
    $eventsDir=Join-Path $project '.bsl-flow/memory/events'
    $nextSeq=@(Get-ChildItem -LiteralPath $eventsDir -File).Count+1
    Write-Fixture (Join-Path $eventsDir ('{0:D6}.json' -f $nextSeq)) '{"schema_version":1,"trunc'
    $replay=Get-BFMemoryReplay $project
    Assert-M (1 -eq @($replay.torn).Count -and 1 -eq @($replay.events).Count) 'Torn tail event was not skipped with an explicit report.'
    Assert-M ($nextSeq+1 -eq [int]$replay.next_seq) 'Torn tail did not reserve the next sequence number after itself.'
    $beforeFiles=@(Get-ChildItem -LiteralPath $eventsDir -File).Count
    $blockedPlan=@([ordered]@{event_type='rejected';record_id=$null;knowledge_class=$null;risk_class=$null;scope=$null;observation=$null;action=$null;superseded_by=$null;source_task_id=$null;source_attempt_id=$null;evidence_refs=@();reason='torn-tail-test'})
    Assert-MThrows { Add-BFMemoryEvents -ProjectPath $project -ProjectId $project -Fingerprints (Get-BFMemoryFingerprints $state) -Plan $blockedPlan } 'unresolved torn tail'
    Assert-M ($beforeFiles -eq @((Get-ChildItem -LiteralPath $eventsDir -File)).Count) 'Append after a torn tail changed the event ledger.'
    # A self-consistent event with a broken chain link fails closed.
    $entry=[ordered]@{event_type='confirmed';record_id=('b'*64);knowledge_class=$null;risk_class=$null;scope=$null;observation=$null;action=$null;superseded_by=$null;source_task_id=[guid]::NewGuid().ToString();source_attempt_id=[guid]::NewGuid().ToString();evidence_refs=@();reason='forged'}
    $forged=New-BFMemoryEventObject -ProjectId $project -Fingerprints (Get-BFMemoryFingerprints $state) -Entry $entry -PreviousEventId ('0'*64)
    Write-Fixture (Join-Path $eventsDir ('{0:D6}.json' -f ($nextSeq+1))) (Get-BFCanonicalJson $forged)
    $context=Get-BFMemoryProjection $state ([ordered]@{action='dispatch';stage='implement';blockers=@()})
    Assert-M ($context.available -eq $false -and $context.index.replayed -eq $false -and -not [string]::IsNullOrWhiteSpace([string]$context.blocker)) 'Broken event chain did not disable memory with an explicit blocker.'
} finally { Remove-Item -LiteralPath $project -Recurse -Force }

# --- 8. Resume Capsule delta over the real task context projection ------------
$project=New-MemProject
try {
    $state=New-MemState $project ([guid]::NewGuid().ToString()) ('w'*64)
    $state | Add-Member -NotePropertyName worker_path -NotePropertyValue (Join-Path $project 'worktree') -Force
    $state | Add-Member -NotePropertyName baseline -NotePropertyValue ('c'*40) -Force
    # Worker observations stay untrusted; the controller acceptance receipt is
    # the closed source for the recommendation used by this capsule fixture.
    [void](Add-MemAcceptedTemplate $state)
    $context=Get-BFMemoryProjection $state ([ordered]@{action='dispatch';stage='implement';blockers=@()})
    Assert-M ($context.available -eq $true) 'Healthy store reported unavailable.'
    Assert-M (1 -eq @($context.records).Count -and 'candidate-recommendation' -ceq [string]$context.records[0].selected_reason) 'Projection lost the recommendation record or its reason.'
    Assert-M (1 -eq @($context.working_set).Count -and 'src/hello.txt' -ceq $context.working_set[0]) 'Working set did not expose the record scope paths in sorted order.'
    Assert-M (1 -eq @($context.bundle.record_ids).Count -and [string]$context.bundle.record_ids[0] -ceq [string]$context.records[0].record_id) 'Bundle record ids disagree with the projected records.'
    Assert-M ($context.bundle.bundle_id -ceq $context.bundle.bundle_sha256 -and $context.bundle.bundle_id -match '^[0-9a-f]{64}$') 'Bundle identity hashes are malformed.'
    Assert-M ('acceptance-receipt' -ceq [string](Get-BFValue (Get-BFValue $context.records[0] 'evidence_ref') 'kind')) 'Selected memory summary lost its evidence reference.'
    # The rendered prompt section is deterministic and carries the boundary sentence.
    $memory=[ordered]@{schema_version=1;available=$true;bundle_id=$context.bundle.bundle_id;bundle_sha256=$context.bundle.bundle_sha256;records=$context.records;excluded=$context.bundle.excluded;disabled_reason=$null}
    $prompt=Format-BFMemoryBundlePrompt $memory
    Assert-M ($prompt.Contains('Memory context (advisory experience only')) 'Prompt memory section lost the advisory boundary sentence.'
    Assert-M ($prompt.Contains('Recommended action')) 'Prompt memory section lost the recommended action.'
    Assert-M ($prompt.Contains('Evidence ref:')) 'Prompt memory section lost the bounded evidence reference.'
    $stagePrompt=Get-BFStagePrompt $state 'implement' '' ([pscustomobject]@{memory=$memory})
    Assert-M ($stagePrompt.Contains('payload_json must encode exactly {changed_files:[relative paths]}') -and $stagePrompt.Contains('do not add worker-authored observations')) 'Implement prompt still solicited worker-authored memory observations.'
    $prompt2=Format-BFMemoryBundlePrompt $memory
    Assert-M ($prompt -ceq $prompt2) 'Prompt rendering was not deterministic.'
    $emptyPrompt=Format-BFMemoryBundlePrompt $null
    Assert-M ($emptyPrompt.Contains('No applicable memory records')) 'Empty memory did not render the deterministic no-records block.'
    # Dispositions are independent of memory: recover/blocked dispositions survive.
    $nativeState=[pscustomobject]@{task_id=[guid]::NewGuid().ToString();revision=2;status='running';stage='verify';intent_hash=('i'*64);policy_hash=('w'*64);baseline=('c'*40);classification=$null;active_attempt=[guid]::NewGuid().ToString();unresolved_effect=[pscustomobject]@{attempt_id=[guid]::NewGuid().ToString();stage='verify';scope='native_1c'};question=$null;evidence=@();request=[pscustomobject]@{mode='implement';source_paths=@('.')};project_path=$project;policy_files=@([pscustomobject]@{path='Invoke-BSLFlowTask.ps1';sha256=('h'*64)});policy_rules=[pscustomobject]@{self_learning_memory_enabled=$true}}
    $context=Get-BFMemoryProjection $nativeState ([ordered]@{action='recover';stage='verify';blockers=@('uncertain effect requires control read')})
    Assert-M ($context.available -eq $true) 'Memory projection broke on a recover-state task.'
    Assert-M (0 -eq @($context.records).Count -and ((@($context.bundle.excluded) | ForEach-Object { $_.reason }) -contains 'stage-scope-mismatch')) 'Memory recommendations leaked across a stage boundary for a verify recover task.'
} finally { Remove-Item -LiteralPath $project -Recurse -Force }

# --- 9. Offline controller integration: accepted outcomes promote memory and
# --- the next authorized attempt receives the attempt-bound bundle -----------
$testRoot=Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-memory-e2e-'+[guid]::NewGuid().ToString('N'))
$project=Join-Path $testRoot 'project';[void][IO.Directory]::CreateDirectory($project)
try {
    [void](Invoke-BFGit $project @('init'))
    Write-Fixture (Join-Path $project 'hello.txt') "Initial`n"
    Write-Fixture (Join-Path $project '.gitignore') ".bsl-flow/`nopenspec/changes/`n"
    Write-Fixture (Join-Path $project 'bsl-flow.yaml') "features:`n  self_learning_memory:`n    enabled: true`n"
    [void](Invoke-BFGit $project @('add','.'))
    [void](Invoke-BFGit $project @('-c','user.name=BSL Flow Test','-c','user.email=test@example.invalid','commit','-m','Fixture'))
    function New-E2EObservation{
        return [ordered]@{scope=@('hello.txt');observation='Write the greeting with a trailing newline and run the bounded lint before verify.';action_type='recommended';action='Keep the greeting newline-stable.';knowledge_class='procedural';risk_class='low'}
    }
    function New-E2ERequest{
        return [pscustomobject]@{schema_version=1;request_id=[guid]::NewGuid().ToString();prompt='Add Example greeting to hello.txt.';mode='implement';analysis_goal='analysis';complexity='S';risk='low';impact_flags=@();source_paths=@('hello.txt');criteria=@([pscustomobject]@{id='greeting';observation='The source contains the requested description.';kind='file_assertion';path='hello.txt';contains='Example greeting'});provenance=[pscustomobject]@{source='user';reference='memory-e2e';text='Implement the greeting.'};models=[pscustomobject]@{worker='gpt-6-astra';worker_effort='medium';reviewer='gpt-6-astra';reviewer_effort='high'}}
    }
    $e2eExecutor={param($run)
        $payload=@{}
        switch($run.attempt.stage){
            'inspect'{$payload=@{complexity='S';risk='low';impact_flags=@();rationale='Filesystem fixture with one bounded greeting change.'}}
            'implement'{
                Write-Fixture (Join-Path $run.state.worker_path 'hello.txt') "Example greeting`n"
                $payload=[ordered]@{changed_files=@('hello.txt');observations=@(New-E2EObservation)}
            }
            'verify'{return Invoke-BFVerification $run.state (Join-Path $run.directory 'raw') '' $null}
            default{throw "Unexpected E2E stage $($run.attempt.stage)"}
        }
        return [ordered]@{schema_version=1;status='completed';summary='E2E fixture stage complete.';payload_json=Get-BFCanonicalJson $payload}
    }
    $taskIds=@()
    for($i=1;$i -le 3;$i++){
        $request=New-E2ERequest
        $state=Start-BFTask $project $request
        $taskIds+=$state.task_id
        $state=Invoke-BFRun $project $state.task_id '' $e2eExecutor
        Assert-M ('completed' -ceq $state.status) "E2E task $i did not complete."
    }
    $replay=Get-BFMemoryReplay $project
    $recordId=@($replay.records.Keys)[0]
    Assert-M (1 -eq @($replay.records.Keys).Count) 'E2E observations did not deduplicate into one canonical record.'
    Assert-M ('accepted' -ceq [string](Get-BFValue $replay.records[$recordId] 'state')) 'Three accepted controller outcomes did not promote the record.'
    Assert-M (3 -eq [int](Get-BFValue $replay.records[$recordId] 'confirmations')) 'E2E confirmations did not reach the threshold.'
    Assert-M ((@($replay.events) | Where-Object { $_.event_type -ceq 'promoted' }).Count -ge 1) 'E2E promotion event is missing.'
    # The next authorized attempt receives the attempt-bound bundle with the accepted record.
    $request=New-E2ERequest
    $paused=Start-BFTask $project $request
    $taskIds+=$paused.task_id
    $inspectAttempt=New-BFAttempt $project $paused.task_id ''
    [void](Invoke-BFStage $inspectAttempt '' $e2eExecutor)
    # Pause: the authoritative next step is the implement dispatch; the Resume
    # Capsule supplements it with the working set and bundle identity without
    # changing the disposition.
    $contextState=Read-BFTask $project $paused.task_id
    $nextBefore=Get-BFNext $contextState
    Assert-M ('dispatch' -ceq $nextBefore.action -and 'implement' -ceq $nextBefore.stage) 'Paused E2E task did not authorize the implement dispatch.'
    $context=Get-BFTaskContext $contextState $project $nextBefore
    Assert-MSchemaOk (Get-BFCanonicalJson $context) (Join-Path $schemaRoot 'context.schema.json') 'Resume context does not satisfy context.schema.json.'
    Assert-M ($context.memory.available -eq $true) 'Resume Capsule lost memory availability.'
    Assert-M (1 -eq @($context.memory.records).Count) 'Resume Capsule lost the memory record.'
    Assert-M ('accepted-knowledge' -ceq [string]$context.memory.records[0].selected_reason) 'Resume Capsule lost the explainable selection reason.'
    Assert-M ('hello.txt' -ceq [string]$context.memory.working_set[0]) 'Resume Capsule lost the working set.'
    $nextAfter=Get-BFNext $contextState
    Assert-M ((@($nextBefore.blockers) -join '|') -ceq (@($nextAfter.blockers) -join '|') -and $nextBefore.action -ceq $nextAfter.action) 'Memory changed the authoritative disposition.'
    # The implement dispatch itself binds the exact capsule bundle.
    $state=Invoke-BFRun $project $paused.task_id '' $e2eExecutor
    Assert-M ('completed' -ceq $state.status) 'Fourth E2E task did not complete.'
    $memory=$null
    foreach($attemptId in @($state.attempts)){
        $start=Read-BFJson (Join-Path (Get-BFTaskDirectory $project $state.task_id) ('attempts/'+$attemptId+'/start.json'))
        if([string]$start.stage -ceq 'implement'){ $memory=$start.memory }
    }
    Assert-M ($null -ne $memory -and $memory.available -eq $true -and [string]$memory.bundle_sha256 -match '^[0-9a-f]{64}$') 'Implement attempt did not receive a bound memory bundle.'
    Assert-M (1 -eq @($memory.records).Count -and 'accepted' -ceq [string]$memory.records[0].state -and [string]$memory.records[0].record_id -ceq $recordId) 'Attempt bundle lost the accepted record.'
    Assert-M (1 -eq @($context.memory.bundle.record_ids).Count -and [string]$context.memory.bundle.record_ids[0] -ceq $recordId) 'Capsule bundle ids disagree with the promoted record.'
    # Old task without the new policy key stays readable and safely disabled.
    $oldProject=New-MemProject
    try {
        $oldState=New-MemState $oldProject ([guid]::NewGuid().ToString()) ('z'*64)
        $oldState.PSObject.Properties.Remove('policy_rules')
        $oldContext=Get-BFMemoryProjection $oldState ([ordered]@{action='dispatch';stage='inspect';blockers=@()})
        Assert-M ($oldContext.available -eq $false -and $oldContext.blocker -ceq 'disabled-by-project-policy' -and 0 -eq @($oldContext.records).Count -and 0 -eq $oldContext.index.events_count) 'Old task without the opt-in flag was not safely disabled.'
    } finally { Remove-Item -LiteralPath $oldProject -Recurse -Force }
} finally {
    if(Test-Path -LiteralPath $testRoot){ Remove-Item -LiteralPath $testRoot -Recurse -Force }
}

# --- 10. Public negative E2E: real verification failure stays diagnostic ------
$testRoot=Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-memory-negative-e2e-'+[guid]::NewGuid().ToString('N'))
$project=Join-Path $testRoot 'project';[void][IO.Directory]::CreateDirectory($project)
try {
    [void](Invoke-BFGit $project @('init'))
    Write-Fixture (Join-Path $project 'hello.txt') "Initial`n"
    Write-Fixture (Join-Path $project '.gitignore') ".bsl-flow/`nopenspec/changes/`n"
    Write-Fixture (Join-Path $project 'bsl-flow.yaml') "features:`n  self_learning_memory:`n    enabled: true`n"
    [void](Invoke-BFGit $project @('add','.'))
    [void](Invoke-BFGit $project @('-c','user.name=BSL Flow Test','-c','user.email=test@example.invalid','commit','-m','Fixture'))
    function New-NegativeE2ERequest{
        return [pscustomobject]@{schema_version=1;request_id=[guid]::NewGuid().ToString();prompt='Add Example greeting to hello.txt.';mode='implement';analysis_goal='analysis';complexity='S';risk='low';impact_flags=@();source_paths=@('hello.txt');criteria=@([pscustomobject]@{id='greeting';observation='The source contains the requested description.';kind='file_assertion';path='hello.txt';contains='Expected greeting'});provenance=[pscustomobject]@{source='user';reference='memory-negative-e2e';text='Implement the greeting.'};models=[pscustomobject]@{worker='gpt-6-astra';worker_effort='medium';reviewer='gpt-6-astra';reviewer_effort='high'}}
    }
    $negativeE2EExecutor={param($run)
        $payload=@{}
        switch($run.attempt.stage){
            'inspect'{$payload=@{complexity='S';risk='low';impact_flags=@();rationale='Filesystem fixture with one bounded greeting change.'}}
            'implement'{
                # Deliberately leave the declared criterion unsatisfied. The
                # verify stage below must observe this through the real
                # controller verifier, not a fabricated terminal result.
                Write-Fixture (Join-Path $run.state.worker_path 'hello.txt') "Wrong greeting`n"
                $payload=[ordered]@{changed_files=@('hello.txt')}
            }
            'verify'{return Invoke-BFVerification $run.state (Join-Path $run.directory 'raw') '' $null}
            default{throw "Unexpected negative E2E stage $($run.attempt.stage)"}
        }
        return [ordered]@{schema_version=1;status='completed';summary='Negative E2E fixture stage complete.';payload_json=Get-BFCanonicalJson $payload}
    }
    $failedTaskIds=@()
    # Exercise three explicit failing task runs. max_source_repairs defaults to
    # zero, so a failed verification cannot enter an automatic repair loop.
    for($i=1;$i -le 3;$i++){
        $state=Start-BFTask $project (New-NegativeE2ERequest)
        $failedTaskIds+=$state.task_id
        $state=Invoke-BFRun $project $state.task_id '' $negativeE2EExecutor
        Assert-M ('failed' -ceq $state.status) "Negative E2E task $i did not stop at failed verification."
        Assert-M ('failed' -ceq (Get-BFNext $state).action) "Negative E2E task $i exposed a retryable controller action."
        $verify=@($state.evidence | Where-Object { $_.stage -ceq 'verify' })[-1]
        Assert-M ($null -ne $verify -and 'FAIL' -ceq [string]$verify.outcome) "Negative E2E task $i did not retain a failed verify evidence entry."
        $verifyResult=Read-BFJson (Join-Path (Get-BFTaskDirectory $project $state.task_id) ('attempts/'+$verify.attempt_id+'/result.json'))
        Assert-M ('BF_FAIL: greeting: expected content is absent.' -ceq [string]$verifyResult.summary -and 'none' -ceq [string]$verifyResult.side_effects -and 'greeting' -ceq [string]$verifyResult.proposal.criterion_id -and 'file_assertion' -ceq [string]$verifyResult.proposal.kind -and $verifyResult.proposal.repair_eligible -eq $false) "Negative E2E task $i did not retain the real verifier failure and typed proposal in result.json."
        Assert-M (0 -eq @($state.acceptances).Count -and -not (Test-Path -LiteralPath (Join-Path (Get-BFTaskDirectory $project $state.task_id) 'acceptance'))) "Negative E2E task $i created an acceptance entry or directory."
    }
    $replay=Get-BFMemoryReplay $project
    $records=@($replay.records.Values)
    Assert-M (1 -eq $records.Count -and 'diagnostic' -ceq [string]$records[0].knowledge_class -and 0 -eq [int]$records[0].confirmations -and 0 -eq @($records | Where-Object { [int]$_.confirmations -gt 0 }).Count) 'Repeated failed verification did not remain diagnostic with zero confirmations.'
    Assert-M (0 -eq @($replay.events | Where-Object { [string]$_.knowledge_class -ceq 'procedural' }).Count) 'Failed verification created a procedural memory event.'
    Assert-M (0 -eq @($replay.events | Where-Object { $_.event_type -ceq 'promoted' }).Count) 'Failed verification promoted memory.'
    Assert-M (3 -eq $failedTaskIds.Count) 'Negative E2E did not execute three explicit failing task runs.'
} finally {
    if(Test-Path -LiteralPath $testRoot){ Remove-Item -LiteralPath $testRoot -Recurse -Force }
}

# --- 11. Closed source-only recovery template and idempotence -----------------
$project=New-MemProject
try {
    $state=New-MemState $project ([guid]::NewGuid().ToString()) ('y'*64)
    $manifest=[pscustomobject]@{sha256=('m'*64)}
    $resolution=[ordered]@{attempt_id=[guid]::NewGuid().ToString();scope='source_only';source_sha256=$manifest.sha256;observation='Controller read matched the current source manifest.'}
    $hash=Get-BFHash $resolution
    Assert-M ($null -ne (Add-BFMemoryFromRecovery $state $resolution $manifest $hash)) 'Source-only recovery template did not create a candidate.'
    $replay=Get-BFMemoryReplay $project
    $recordId=@($replay.records.Keys)[0]
    Assert-M (1 -eq @($replay.records.Keys).Count -and 'successful-recovery' -in @($replay.records[$recordId].evidence_kinds) -and 'controller-template' -ceq [string]$replay.records[$recordId].provenance) 'Recovery candidate was not controller-owned successful-recovery evidence.'
    $before=Get-MemEventCount $project
    Assert-M ($null -eq (Add-BFMemoryFromRecovery $state $resolution $manifest $hash) -and $before -eq (Get-MemEventCount $project)) 'Repeated identical recovery receipt was not idempotent.'
} finally { Remove-Item -LiteralPath $project -Recurse -Force }

# --- 12. Legacy v1 records remain readable and conservatively inapplicable ---
$project=New-MemProject
try {
    $legacyFingerprints=[ordered]@{policy=('l'*64);controller=('c'*64);version='legacy-v1'}
    $legacyScope=ConvertTo-BFMemoryScope 'implement' @('src/legacy.txt')
    $legacyAction=[ordered]@{type='recommended';text='Use the legacy bounded action.'}
    $legacyEntry=[ordered]@{event_type='candidate';record_id=(Get-BFMemoryRecordId $project $legacyScope $legacyAction $legacyFingerprints);knowledge_class='procedural';risk_class='low';scope=$legacyScope;observation='Legacy bounded observation.';action=$legacyAction;superseded_by=$null;source_task_id=[guid]::NewGuid().ToString();source_attempt_id=[guid]::NewGuid().ToString();evidence_refs=@();reason='legacy-fixture'}
    $legacyEvent=New-BFMemoryEventObject -ProjectId $project -Fingerprints $legacyFingerprints -Entry $legacyEntry -PreviousEventId $null
    Write-BFJson -Path (Join-Path $project '.bsl-flow/memory/events/000001.json') -Value $legacyEvent
    $replay=Get-BFMemoryReplay $project
    $legacySchemaOk=$false
    try { $legacySchemaOk=[bool](Test-Json -Json (Get-BFCanonicalJson (Read-BFJson (Join-Path $project '.bsl-flow/memory/events/000001.json'))) -SchemaFile (Join-Path $schemaRoot 'memory-event.schema.json') -ErrorAction Stop) } catch { $legacySchemaOk=$false }
    Assert-M (1 -eq @($replay.events).Count -and 1 -eq @($replay.records.Keys).Count -and $legacySchemaOk) 'Legacy v1 event was not replayable or does not satisfy memory-event.schema.json compatibility.'
    $legacyIndex=New-BFMemoryIndexObject -ProjectId $project -Events $replay.events -Records $replay.records -Torn $replay.torn -EventFilesCount $replay.event_files_count -LastEventSeq $replay.last_event_seq
    Assert-MSchemaOk (Get-BFCanonicalJson $legacyIndex) (Join-Path $schemaRoot 'memory-index.schema.json') 'Rebuilt index for legacy fingerprints does not satisfy memory-index.schema.json.'
    $state=New-MemState $project ([guid]::NewGuid().ToString()) ('l'*64)
    $context=Get-BFMemoryProjection $state ([ordered]@{action='dispatch';stage='implement';blockers=@()})
    Assert-M ($context.available -eq $true -and 0 -eq @($context.records).Count -and ((@($context.bundle.excluded) | ForEach-Object { $_.reason }) -contains 'fingerprint-mismatch')) 'Legacy advice was applied without the complete current fingerprint boundary.'
} finally { Remove-Item -LiteralPath $project -Recurse -Force }

Write-Output "TASK_MEMORY_OK checks=$script:checks; model/runtime/database=0"
