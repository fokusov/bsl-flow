#Requires -Version 7.0
# Offline task-context contract: read-only projection, authoritative next action,
# distinct blocked/needs-input/failed/unknown-effect states and schema conformance.
[CmdletBinding()]param([string]$PackageRoot)
Set-StrictMode -Version Latest
$ErrorActionPreference='Stop'
if(-not $PackageRoot){$PackageRoot=Split-Path -Parent $PSScriptRoot}
$root=[IO.Path]::GetFullPath($PackageRoot).TrimEnd('\','/')
$scripts=Join-Path $root 'global/skills/1c-task/scripts'
foreach($module in @('Task.Storage.ps1','Task.Contracts.ps1','Task.Architecture.ps1','Task.Gates.ps1')){. (Join-Path $scripts $module)}
$schemaPath=Join-Path $root 'global/skills/1c-task/schemas/context.schema.json'
$script:checks=0
function Assert-C([bool]$Condition,[string]$Message){if(-not $Condition){throw "ASSERTION FAILED: $Message"};$script:checks++}
function New-State([string]$Status,[string]$Stage,[object[]]$Evidence=@()){
    return [pscustomobject]@{
        task_id=([guid]::NewGuid().ToString());revision=3;status=$Status;stage=$Stage
        intent_hash=('a'*64);policy_hash=('b'*64);baseline=('c'*40)
        classification=[pscustomobject]@{complexity='S';risk='low';impact_flags=@();rationale='fixture'}
        active_attempt=$null;unresolved_effect=$null;question=$null;evidence=@($Evidence)
        request=[pscustomobject]@{mode='analysis_only'}
    }
}
function New-FreshEvidence($State){
    return [pscustomobject]@{attempt_id=([guid]::NewGuid().ToString());stage='inspect';outcome='PASS';dependencies=(Get-BFDependencies $State 'inspect' $null);raw_hashes=@()}
}
function Assert-ContextSchema($Context){
    try { if(-not (Test-Json -Json (Get-BFCanonicalJson $Context) -SchemaFile $schemaPath -ErrorAction Stop)){throw 'schema mismatch'} }
    catch { throw ("ASSERTION FAILED: context schema: {0}" -f $_.Exception.Message) }
    $script:checks++
}

# completed: fresh evidence and an authoritative accept step.
$state=New-State 'completed' 'acceptance' @()
$state.evidence=@(New-FreshEvidence $state)
$context=Get-BFTaskContext $state $root ([ordered]@{action='accept';stage='acceptance';blockers=@()})
Assert-C ($context.status -ceq 'completed' -and $context.next.action -ceq 'accept') 'Completed task did not project accept.'
Assert-C (@($context.stale_context).Count -eq 0 -and $context.evidence[0].fresh -eq $true) 'Fresh evidence was marked stale.'
Assert-C ($context.generated_from.adr_index_sha256 -match '^[0-9a-f]{64}$' -and @($context.missing_context).Count -eq 0) 'ADR index identity missing from context.'
Assert-C ($context.generated_from.adr_scope -in @('project','package') -and $context.generated_from.adr_root.Length -gt 0) 'Context did not expose the resolved architecture source.'
Assert-C ($context.generated_from.package_version.Length -gt 0 -and $null -ne $context.generated_from.source_sha256) 'Context lost its package or source identity.'
Assert-ContextSchema $context

# A running attempt keeps a UUID string and still satisfies the schema.
$running=New-State 'running' 'implement'
$running.active_attempt=([guid]::NewGuid().ToString())
$runningContext=Get-BFTaskContext $running $root ([ordered]@{action='recover';stage='implement';blockers=@('unfinished attempt')})
Assert-C ($runningContext.active_attempt -is [string] -and $runningContext.next.action -eq 'recover') 'String active_attempt was not projected.'
Assert-ContextSchema $runningContext

# A read error still yields a versioned, schema-valid blocked context that keeps
# the useful resume evidence instead of a contradictory completed/empty shape.
$errorEnvelope=New-BFContextErrorEnvelope $state 'BF_BLOCKED: simulated read failure' $root
Assert-C ($errorEnvelope.next.action -eq 'inspect_blocker' -and $errorEnvelope.generated_from.adr_index_sha256 -ceq 'error') 'Context read error lost its structured envelope.'
Assert-C ($errorEnvelope.status -ceq 'blocked' -and @($errorEnvelope.evidence).Count -eq @($state.evidence).Count) 'Context error envelope kept a contradictory status or dropped evidence.'
Assert-C ($errorEnvelope.generated_from.package_source -in @('manifest','host','entrypoint','')) 'Package identity lost its source label.'
Assert-ContextSchema $errorEnvelope

# stale: mismatched dependency hashes must be reported, not hidden.
$stale=New-State 'ready' 'spec'
$entry=[pscustomobject]@{attempt_id=([guid]::NewGuid().ToString());stage='inspect';outcome='PASS';dependencies=[ordered]@{intent=('a'*64);policy=('b'*64);baseline=('d'*40)};raw_hashes=@()}
$stale.evidence=@($entry)
$staleContext=Get-BFTaskContext $stale $root ([ordered]@{action='dispatch';stage='spec';blockers=@()})
Assert-C ($staleContext.evidence[0].fresh -eq $false -and $staleContext.stale_context -contains $entry.attempt_id) 'Stale evidence was not distinguished.'

# blocked / needs_input / failed / unknown-effect remain distinguishable.
$blocked=Get-BFTaskContext (New-State 'blocked' 'implement') $root ([ordered]@{action='blocked';stage='implement';blockers=@('BF_BLOCKED: worker failed')})
Assert-C ($blocked.next.action -ceq 'blocked' -and $blocked.next.blockers.Count -eq 1) 'Blocked task lost its blocker.'
$questionState=New-State 'needs_input' 'spec';$questionState.question=[pscustomobject]@{text='Which greeting literal?'}
$needs=Get-BFTaskContext $questionState $root ([ordered]@{action='needs_input';stage='spec';blockers=@('answer required')})
Assert-C ($needs.next.action -ceq 'needs_input' -and $needs.question.text -ceq 'Which greeting literal?') 'Question was not projected.'
$failed=Get-BFTaskContext (New-State 'failed' 'verify') $root ([ordered]@{action='failed';stage='verify';blockers=@('BF_FAIL: tests failed')})
Assert-C ($failed.next.action -ceq 'failed') 'Failed task lost its state.'
$unknown=New-State 'running' 'implement';$unknown.unresolved_effect=[pscustomobject]@{stage='implement';reason='timeout'}
$recover=Get-BFTaskContext $unknown $root ([ordered]@{action='recover';stage='implement';blockers=@('uncertain effect requires control read')})
Assert-C ($recover.next.action -ceq 'recover' -and $null -ne $recover.unresolved_effect) 'Unknown effect was not projected as recover.'
Assert-ContextSchema $recover

# The projection is pure: repeated calls yield identical bytes and write nothing.
$before=Get-BFHash $state
[void](Get-BFTaskContext $state $root ([ordered]@{action='accept';stage='acceptance';blockers=@()}))
Assert-C ((Get-BFHash $state) -ceq $before) 'Task context mutated the state.'

# Missing ADR index is reported as missing context, never as success.
$emptyRoot=Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-context-'+[guid]::NewGuid().ToString('N'))
[void][IO.Directory]::CreateDirectory($emptyRoot)
try {
    $missing=Get-BFTaskContext (New-State 'ready' 'inspect') $emptyRoot ([ordered]@{action='dispatch';stage='inspect';blockers=@()}) -FallbackRoot $emptyRoot
    Assert-C ($missing.missing_context -contains 'adr-index' -and $missing.generated_from.adr_index_sha256 -ceq 'missing' -and $missing.generated_from.adr_scope -ceq 'missing') 'Missing ADR index was masked.'
}
finally { Remove-Item -LiteralPath $emptyRoot -Recurse -Force }

# last_attempt is the last terminal attempt, not the most recently started one.
$attemptRoot=Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-context-attempts-'+[guid]::NewGuid().ToString('N'))
try {
    $attemptTask=[guid]::NewGuid().ToString()
    $terminal=[guid]::NewGuid().ToString();$pending=[guid]::NewGuid().ToString()
    $taskDirectory=Join-Path $attemptRoot ('.bsl-flow/tasks/'+$attemptTask)
    [void][IO.Directory]::CreateDirectory((Join-Path $taskDirectory ('attempts/'+$terminal)))
    [void][IO.Directory]::CreateDirectory((Join-Path $taskDirectory ('attempts/'+$pending)))
    [IO.File]::WriteAllText((Join-Path $taskDirectory ('attempts/'+$terminal+'/result.json')),'{"stage":"verify","outcome":"PASS"}',[Text.UTF8Encoding]::new($false))
    $attemptState=[pscustomobject]@{task_id=$attemptTask;project_path=$attemptRoot;attempts=@($terminal,$pending)}
    $lastAttempt=Get-BFLastAttemptReceipts $attemptState
    Assert-C ($lastAttempt.attempt_id -eq $terminal -and $lastAttempt.outcome -ceq 'PASS') 'last_attempt did not choose the last terminal attempt.'
    Assert-C (@($lastAttempt.receipts | Where-Object { $_.name -eq 'result.json' }).Count -eq 1) 'last_attempt did not expose its receipt.'
}
finally { Remove-Item -LiteralPath $attemptRoot -Recurse -Force }
Write-Output "TASK_CONTEXT_OK checks=$script:checks; model/runtime/database=0"
