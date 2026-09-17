#Requires -Version 7.0
# Stage D read-only resume pilot. Drives the real controller to three saved
# scenarios, then reads the journal through Get-BFTaskContext and compares the
# projection with the journal, receipts and Get-BFNext. No model/provider call.
[CmdletBinding()]param([string]$PackageRoot)
Set-StrictMode -Version Latest
$ErrorActionPreference='Stop'
if(-not $PackageRoot){$PackageRoot=Split-Path -Parent $PSScriptRoot}
$root=[IO.Path]::GetFullPath($PackageRoot).TrimEnd('\','/')
$core=Join-Path $root 'global/skills/1c-task/scripts'
foreach($module in @('Task.Storage.ps1','Task.Contracts.ps1','Task.Architecture.ps1','Task.Gates.ps1','Task.Process.ps1','Task.Engine.ps1','Task.Stages.ps1')){. (Join-Path $core $module)}
$script:checks=0
function Assert-P([bool]$Condition,[string]$Message){if(-not $Condition){throw "ASSERTION FAILED: $Message"};$script:checks++}
function Failure-P([scriptblock]$Action){try{& $Action|Out-Null;return ''}catch{return $_.Exception.Message}}
function Write-P([string]$Path,[string]$Text){[void][IO.Directory]::CreateDirectory((Split-Path $Path -Parent));[IO.File]::WriteAllText($Path,$Text,[Text.UTF8Encoding]::new($false))}
function Get-TreeFingerprint([string]$Path){
    if(-not (Test-Path -LiteralPath $Path)){return ''}
    $normalized=[IO.Path]::GetFullPath($Path).TrimEnd('\','/')
    $items=Get-ChildItem -LiteralPath $normalized -File -Recurse -Force | Sort-Object FullName | ForEach-Object { [ordered]@{path=$_.FullName.Substring($normalized.Length+1);sha256=(Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash} }
    return Get-BFHash $items
}
function New-Request([string]$Mode,[string]$Complexity='S',[string]$Risk='low'){
    return [pscustomobject]@{schema_version=1;request_id=[guid]::NewGuid().ToString();prompt='Add Example greeting to hello.txt.';mode=$Mode;analysis_goal='analysis';complexity=$Complexity;risk=$Risk;impact_flags=@();criteria=@([pscustomobject]@{id='greeting';observation='The source contains the requested description.';kind='file_assertion';path='hello.txt';contains='Example greeting'});provenance=[pscustomobject]@{source='user';reference='resume-pilot';text='Implement the greeting.'};models=[pscustomobject]@{worker='gpt-6-astra';worker_effort='medium';reviewer='gpt-6-astra';reviewer_effort='high'}}
}
function Get-PilotMetrics($Context){
    $stage=if($Context.next.action -eq 'dispatch'){[string]$Context.next.stage}else{[string]$Context.stage}
    $bundle=Get-BFArchitectureBundle $stage
    $chars=@($bundle.decisions | ForEach-Object { $_.excerpt.Length }) | Measure-Object -Sum
    return [ordered]@{
        next_action=[string]$Context.next.action;next_stage=[string]$Context.next.stage;bundle_stage=$stage
        bundle_decisions=@($bundle.decisions).Count;bundle_excerpt_chars=[int]$chars.Sum;bundle_sha256=[string]$bundle.bundle_sha256
        missing_context=@($Context.missing_context).Count;stale_context=@($Context.stale_context).Count
        evidence_total=@($Context.evidence).Count;evidence_fresh=@($Context.evidence|Where-Object{$_.fresh}).Count
        manual_doc_lookups_without_bundle=@($bundle.decisions).Count;manual_doc_lookups_with_bundle=0
    }
}
function Invoke-Probe([string]$Project,[string]$TaskId){
    # Resume from the saved journal only; every read must be side-effect free.
    $state=Read-BFTask $Project $TaskId
    $next=Get-BFNext $state
    $taskDirectory=Get-BFTaskDirectory $Project $TaskId
    $before=Get-TreeFingerprint $taskDirectory
    $context=Get-BFTaskContext $state $Project $next
    $after=Get-TreeFingerprint $taskDirectory
    Assert-P ($before -ceq $after) 'Task context wrote to the journal.'
    Assert-P ($context.task_id -eq $state.task_id -and $context.revision -eq $state.revision -and $context.status -eq $state.status -and $context.stage -eq $state.stage) 'Projection diverged from journal identity.'
    Assert-P ($context.intent_hash -eq $state.intent_hash -and $context.policy_hash -eq $state.policy_hash -and $context.baseline -eq $state.baseline) 'Projection lost journal hashes.'
    Assert-P ($context.next.action -eq $next.action -and $context.next.stage -eq $next.stage) 'Projection diverged from Get-BFNext action/stage.'
    Assert-P ((@($context.next.blockers) -join '|') -ceq (@($next.blockers) -join '|')) 'Projection diverged from Get-BFNext blockers.'
    Assert-P ($context.generated_from.state_sha256 -ceq (Get-BFHash $state)) 'Projection state hash mismatch.'
    Assert-P ($context.generated_from.policy_sha256 -ceq $state.policy_hash) 'Projection policy hash mismatch.'
    Assert-P (@($context.missing_context).Count -eq 0) 'ADR index unexpectedly missing in the repository pilot.'
    # Staleness must be visible exactly as the controller computes it, never hidden.
    $expectedStale=@()
    foreach($entry in @($state.evidence)){
        $fresh=$false;try{$fresh=[bool](Test-BFEvidenceFresh $state $entry $null)}catch{$fresh=$false}
        if(-not $fresh){$expectedStale+=[string]$entry.attempt_id}
    }
    Assert-P ((@($context.stale_context|Sort-Object) -join '|') -ceq (@($expectedStale|Sort-Object) -join '|')) 'Projection hid or invented stale evidence.'
    foreach($entry in @($context.evidence)){Assert-P ($null -ne $entry.fresh) 'Projection omitted evidence freshness.'}
    try { if(-not (Test-Json -Json (Get-BFCanonicalJson $context) -SchemaFile (Join-Path $root 'global/skills/1c-task/schemas/context.schema.json') -ErrorAction Stop)){throw 'schema mismatch'} }
    catch { throw ("ASSERTION FAILED: context schema: {0}" -f $_.Exception.Message) }
    $script:checks++
    return [pscustomobject]@{State=$state;Next=$next;Context=$context;Metrics=(Get-PilotMetrics $context)}
}

$testRoot=Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-resume-pilot-'+[guid]::NewGuid().ToString('N'))
[void][IO.Directory]::CreateDirectory($testRoot)
$project=Join-Path $testRoot 'project';[void][IO.Directory]::CreateDirectory($project)
[void](Invoke-BFGit $project @('init'))
# Isolate from the host global Git config so a clean baseline is deterministic.
[void](Invoke-BFGit $project @('config','core.autocrlf','false'))
[void](Invoke-BFGit $project @('config','core.eol','lf'))
[void](Invoke-BFGit $project @('config','core.safecrlf','false'))
Write-P (Join-Path $project 'hello.txt') "Initial greeting`n"
Write-P (Join-Path $project '.gitignore') ".bsl-flow/`nopenspec/changes/`n"
[void](Invoke-BFGit $project @('add','.'))
[void](Invoke-BFGit $project @('-c','user.name=BSL Flow Test','-c','user.email=test@example.invalid','commit','-m','Fixture'))
# A BSL Flow project carries its own ADR index; the read-only context resolves
# it from the project root, never from a hidden package cache.
[void][IO.Directory]::CreateDirectory((Join-Path $project 'docs/architecture'))
Copy-Item -LiteralPath (Join-Path $root 'docs/ARCHITECTURE_RU.md') -Destination (Join-Path $project 'docs/ARCHITECTURE_RU.md')
Copy-Item -LiteralPath (Join-Path $root 'docs/architecture/adr-index.json') -Destination (Join-Path $project 'docs/architecture/adr-index.json')
Copy-Item -LiteralPath (Join-Path $root 'docs/architecture/adr-index.schema.json') -Destination (Join-Path $project 'docs/architecture/adr-index.schema.json')
[void](Invoke-BFGit $project @('add','.'))
[void](Invoke-BFGit $project @('-c','user.name=BSL Flow Test','-c','user.email=test@example.invalid','commit','-m','Architecture index'))
$script:pilot=[ordered]@{}

$inspectPayload=Get-BFCanonicalJson ([ordered]@{complexity='S';risk='low';impact_flags=@();rationale='Source-only greeting.'})

try {
    # Scenario 1: normal stop between stages (inspect complete, acceptance pending).
    $inspectOk={param($run) if($run.attempt.stage -ne 'inspect'){throw "Unexpected stage $($run.attempt.stage)"};[ordered]@{schema_version=1;status='completed';summary='Inspected.';payload_json=$inspectPayload}}
    $analysisTask=Start-BFTask $project (New-Request 'analysis_only')
    $analysisTask=Invoke-BFRun $project $analysisTask.task_id '' $inspectOk
    $probe1=Invoke-Probe $project $analysisTask.task_id
    Assert-P ($probe1.Next.action -eq 'accept') 'Normal stop did not authorize acceptance.'
    Assert-P (@($probe1.Context.stale_context).Count -eq 0) 'Completed stop carried stale evidence.'
    Assert-P ($probe1.Metrics.evidence_total -ge 1 -and $probe1.Metrics.evidence_fresh -eq $probe1.Metrics.evidence_total) 'Normal stop lost fresh evidence.'
    $script:pilot['normal_stop']=$probe1.Metrics

    # Scenario 2a: question (needs_input) pauses the loop.
    $ask={param($run) [ordered]@{schema_version=1;status='needs_input';summary='Which greeting must be used?';payload_json='{}'}}
    $questionTask=Start-BFTask $project (New-Request 'analysis_only')
    $questionTask=Invoke-BFRun $project $questionTask.task_id '' $ask
    $probe2a=Invoke-Probe $project $questionTask.task_id
    Assert-P ($probe2a.Next.action -eq 'needs_input' -and $null -ne $probe2a.Context.question) 'Question was not projected as needs_input.'
    Assert-P ($probe2a.Next.action -ne 'dispatch') 'Question scenario proposed a dispatch.'
    $script:pilot['question']=$probe2a.Metrics

    # Scenario 2b: blocker without an unknown effect.
    $block={param($run) throw 'BF_BLOCKED: simulated preflight failure'}
    $blockedTask=Start-BFTask $project (New-Request 'analysis_only')
    $blockedTask=Invoke-BFRun $project $blockedTask.task_id '' $block
    $probe2b=Invoke-Probe $project $blockedTask.task_id
    Assert-P ($probe2b.Next.action -eq 'blocked' -and @($probe2b.Context.next.blockers).Count -ge 1) 'Blocker was not projected.'
    Assert-P ($null -eq $probe2b.Context.unresolved_effect) 'Read-only blocker fabricated an unknown effect.'
    Assert-P ($probe2b.Next.action -ne 'dispatch') 'Blocker scenario proposed a dispatch.'
    $script:pilot['blocker']=$probe2b.Metrics

    # Scenario 3: modifying attempt with unknown effect must never re-dispatch.
    $uncertain={param($run)
        if($run.attempt.stage -eq 'inspect'){return [ordered]@{schema_version=1;status='completed';summary='Inspected.';payload_json=$inspectPayload}}
        if($run.attempt.stage -eq 'implement'){Write-P (Join-Path $run.state.worker_path 'hello.txt') "Example greeting partially written`n";throw 'BF_BLOCKED: simulated uncertain effect after source write'}
        throw "Unexpected stage $($run.attempt.stage)"
    }
    $effectTask=Start-BFTask $project (New-Request 'implement')
    $effectTask=Invoke-BFRun $project $effectTask.task_id '' $uncertain
    $probe3=Invoke-Probe $project $effectTask.task_id
    Assert-P ($probe3.Next.action -eq 'recover') 'Unknown effect did not project recover.'
    Assert-P ($null -ne $probe3.Context.unresolved_effect) 'Unknown effect was hidden.'
    Assert-P ($probe3.Next.action -ne 'dispatch') 'Unknown effect proposed a repeated dispatch.'
    Assert-P ($probe3.Context.next.reason -match 'reconciled|uncertain|recover') 'Recover reason did not explain the blocker.'
    # Recovery may register a retained terminal result, but it must never
    # dispatch a second worker for an unknown effect.
    $attemptsBefore=@((Read-BFTask $project $effectTask.task_id).attempts).Count
    [void](Failure-P {Resume-BFAttempt $project $effectTask.task_id})
    $resumedState=Read-BFTask $project $effectTask.task_id
    Assert-P (@($resumedState.attempts).Count -eq $attemptsBefore) 'Recovery created a new attempt for an unknown effect.'
    Assert-P ((Get-BFNext $resumedState).action -ne 'dispatch') 'Recovery allowed re-dispatch of an unknown effect.'
    $script:pilot['unknown_effect']=$probe3.Metrics

    # Bundle is bounded and useful across the three scenarios; no quality claim.
    $totalDecisions=0
    foreach($name in $script:pilot.Keys){$totalDecisions+=[int]$script:pilot[$name].bundle_decisions;Assert-P ([int]$script:pilot[$name].bundle_excerpt_chars -le $script:BFArchitectureBundleMaxChars) "Bundle for $name exceeded the character limit."}
    Assert-P ($totalDecisions -ge 3) 'Resume pilot produced no architecture context.'
    $script:pilot['summary']=[ordered]@{scenarios=3;bundle_total_decisions=$totalDecisions;manual_doc_lookups_without_bundle=$totalDecisions;manual_doc_lookups_with_bundle=0;model_calls=0;runtime_1c='not_run'}
    [IO.File]::WriteAllText((Join-Path $testRoot 'pilot-result.json'),($script:pilot|ConvertTo-Json -Depth 8),[Text.UTF8Encoding]::new($false))
} finally {
    if(Test-Path -LiteralPath $testRoot){Remove-Item -LiteralPath $testRoot -Recurse -Force}
}
Write-Output ("TASK_RESUME_PILOT_OK checks=$script:checks; scenarios=3 bundle_decisions="+$totalDecisions+"; model/runtime/database=0")
