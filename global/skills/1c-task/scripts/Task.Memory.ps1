#Requires -Version 7.0
# Experience memory plane: a controller-owned, project-local, append-only event
# ledger with derived, rebuildable projections (index, stage bundle, context
# delta). Memory is advisory only: it never creates authorization, transition,
# acceptance or publication authority and cannot bypass controller gates.
# Physical layout: .bsl-flow/memory/events/<seq>.json (immutable canonical
# events) and .bsl-flow/memory/index.json (derived, deterministic rebuild).
# Attempt-bound bundles live in the attempt start.json written by the engine;
# no separate bundle directory is needed because bundles are recomputable.
# Extraction is best-effort by specification: a damaged or unavailable memory
# store must never break task recording or readability (memory surfaces its own
# blockers through the context projection and attempt binding instead).
Set-StrictMode -Version Latest
. (Join-Path $PSScriptRoot 'Task.Storage.ps1')
. (Join-Path $PSScriptRoot 'Task.Contracts.ps1')

$script:BFMemoryKnowledgeClasses = @('procedural','diagnostic','business_rule','authorization','test_or_waiver','architecture','controller_policy','model_routing','runtime_or_external_effect')
# Worker payloads may carry bounded observations for validation and audit, but
# they are never a trusted source for promotable knowledge. Policy,
# authorization, routing, waiver and runtime-effect classes stay authority-
# owned and can never be auto-promoted.
$script:BFMemoryWorkerClasses = @('procedural','diagnostic','architecture')
$script:BFMemoryRiskClasses = @('low','medium','high')
$script:BFMemoryActionTypes = @('recommended','avoid')
$script:BFMemoryEventTypes = @('candidate','confirmed','shadow','promoted','contradicted','quarantined','deprecated','superseded','reinstated','rejected')
$script:BFMemoryActiveStates = @('candidate','shadow','accepted')
$script:BFMemoryMaxObservationChars = 512
$script:BFMemoryMaxActionChars = 256
$script:BFMemoryMaxReasonChars = 256
$script:BFMemoryMaxScopePaths = 16
$script:BFMemoryMaxScopePathChars = 512
$script:BFMemoryMaxObservationItems = 16
$script:BFMemoryMaxRecords = 6
$script:BFMemoryMaxBundleChars = 4000
$script:BFMemoryMaxExcluded = 32
$script:BFMemoryMaxEventBytes = 65536
$script:BFMemoryMaxEventFiles = 8192
$script:BFMemoryMaxPlanEvents = 64
$script:BFMemoryMaxRejections = 8
$script:BFMemoryMaxWorkingSet = 24
$script:BFMemoryMaxEvidenceRefs = 8
$script:BFMemoryMaxEvidenceRefChars = 256
$script:BFMemorySecretPattern = '(?i)(password|passwd|secret|api[_-]?key|credential|authorization\s*[:=]|bearer\s+[A-Za-z0-9._\-]{8,}|-----BEGIN [A-Z ]*PRIVATE KEY)'
$script:BFMemoryStageRelevance = @{ diagnose = @('verify','diagnose'); implement = @('implement','recover') }
$script:BFMemoryFingerprintFields = @('policy','controller','version','schema','toolchain')
$script:BFMemoryTemplateAcceptedSourceOnly = 'accepted-source-only-v1'
$script:BFMemoryTemplateSuccessfulRecovery = 'successful-source-recovery-v1'
$script:BFMemoryTemplateObservationAcceptedSourceOnly = 'A low-risk source-only task completed through the declared controller stages and acceptance receipt.'
$script:BFMemoryTemplateActionAcceptedSourceOnly = 'Reuse the bounded controller workflow for matching low-risk source-only tasks.'
$script:BFMemoryTemplateObservationSuccessfulRecovery = 'A source-only recovery control-read matched the current source manifest before retry.'
$script:BFMemoryTemplateActionSuccessfulRecovery = 'Repeat the bounded source control-read before retrying a source-only attempt.'
$script:BFMemoryTemplateObservationConfirmedFailure = 'The controller verifier recorded a declared criterion failure with retained evidence.'
$script:BFMemoryTemplateActionConfirmedFailure = 'Address the retained controller failure evidence before retrying.'

function Test-BFSelfLearningMemoryEnabled {
    param($State)
    $rules=Get-BFValue $State 'policy_rules'
    return (Get-BFValue $rules 'self_learning_memory_enabled' $false) -eq $true
}

function Get-BFMemoryDirectory {
    param([string]$ProjectPath)
    return Assert-BFSafePath (Join-Path (Assert-BFSafePath $ProjectPath) '.bsl-flow/memory')
}

function Get-BFMemoryPackageRoot {
    # Do not depend on Task.Architecture being loaded: memory is also used by
    # the low-level engine during bootstrap and in isolated offline fixtures.
    return Assert-BFSafePath (Split-Path (Split-Path (Split-Path (Split-Path $PSScriptRoot -Parent) -Parent) -Parent) -Parent)
}

function Get-BFMemorySchemaFingerprint {
    # Schema identity is part of the applicability boundary. A changed schema
    # must invalidate old advice instead of silently interpreting it under a
    # different controller contract.
    try {
        $root = Get-BFMemoryPackageRoot
        $entries = [System.Collections.Generic.List[object]]::new()
        foreach ($name in @('memory-event.schema.json','memory-index.schema.json','memory-bundle.schema.json','context.schema.json')) {
            $path = Assert-BFSafePath (Join-Path $root ('global/skills/1c-task/schemas/' + $name))
            if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { return '' }
            $entries.Add([ordered]@{name=$name;sha256=Get-BFFileHash $path})
        }
        return Get-BFHash @($entries.ToArray())
    } catch { return '' }
}

function Get-BFMemoryToolchainFingerprint {
    param($State)
    $profile = Get-BFValue (Get-BFValue $State 'request') 'execution_profile'
    if ($null -eq $profile) { return 'unbound' }
    try {
        $selected = [ordered]@{
            provider=[string](Get-BFValue $profile 'provider' '')
            executable_sha256=[string](Get-BFValue $profile 'executable_sha256' '')
            sandbox=Get-BFValue $profile 'sandbox'
            toolset=Get-BFValue $profile 'toolset'
            runtime=Get-BFValue $profile 'runtime'
            unica=Get-BFValue $profile 'unica'
            codex_skills_sha256=[string](Get-BFValue $profile 'codex_skills_sha256' '')
        }
        return Get-BFHash $selected
    } catch { return '' }
}

function Get-BFMemoryTaskKind {
    param($State)
    $request = Get-BFValue $State 'request'
    if ($null -eq $request) { return 'legacy' }
    $criteria = @((Get-BFValue $request 'criteria' @()) | ForEach-Object { [string](Get-BFValue $_ 'kind' '') } | Where-Object { $_ } | Sort-Object -Unique)
    $flags = @((Get-BFValue $request 'impact_flags' @()) | ForEach-Object { [string]$_ } | Where-Object { $_ } | Sort-Object -Unique)
    $mode = [string](Get-BFValue $request 'mode' '')
    $goal = [string](Get-BFValue $request 'analysis_goal' '')
    return Get-BFMemoryBoundedText (('{0}|{1}|criteria={2}|flags={3}' -f $mode,$goal,($criteria -join ','),($flags -join ','))) 256
}

function Get-BFMemoryErrorSignature {
    param($Result)
    if ($null -eq $Result -or [string](Get-BFValue $Result 'outcome' '') -cne 'FAIL') { return '' }
    $proposal = Get-BFValue $Result 'proposal'
    $criterion = [string](Get-BFValue $proposal 'criterion_id' '')
    $kind = [string](Get-BFValue $proposal 'kind' '')
    $category = [string](Get-BFValue $proposal 'category' '')
    if ([string]::IsNullOrWhiteSpace($criterion) -and [string]::IsNullOrWhiteSpace($kind) -and [string]::IsNullOrWhiteSpace($category)) { return '' }
    return Get-BFHash ([ordered]@{stage=[string](Get-BFValue $Result 'stage' '');criterion_id=$criterion;kind=$kind;category=$category})
}

function Get-BFMemoryErrorSignatureForState {
    param($State, [string]$Stage, [AllowNull()][object]$PendingFailureResult = $null, [AllowNull()][string]$PendingFailureResultHash = '')
    if ($Stage -cne 'diagnose') { return '' }
    $failureId = Get-BFValue (Get-BFValue $State 'repair') 'pending_failure'
    if ([string]::IsNullOrWhiteSpace([string]$failureId)) { return '' }
    if ($null -ne $PendingFailureResult) {
        # Native callers relay the controller-owned canonical terminal result
        # because the task journal may live in a Git common directory rather
        # than in the worker worktree. Never fall back to that worktree when a
        # relay was supplied: a mismatch must fail closed instead of selecting
        # a diagnostic for another failure.
        try {
            if ($PendingFailureResultHash -notmatch '^[0-9a-f]{64}$') { return '' }
            if ((Get-BFHash $PendingFailureResult) -cne $PendingFailureResultHash) { return '' }
            if ([int](Get-BFValue $PendingFailureResult 'schema_version' 0) -ne 1) { return '' }
            if ([string](Get-BFValue $PendingFailureResult 'task_id' '') -cne [string](Get-BFValue $State 'task_id' '')) { return '' }
            if ([string](Get-BFValue $PendingFailureResult 'attempt_id' '') -cne [string]$failureId) { return '' }
            if ([string](Get-BFValue $PendingFailureResult 'stage' '') -cne 'verify' -or [string](Get-BFValue $PendingFailureResult 'outcome' '') -cne 'FAIL' -or [string](Get-BFValue $PendingFailureResult 'side_effects' '') -cne 'none') { return '' }
            return Get-BFMemoryErrorSignature $PendingFailureResult
        } catch { return '' }
    }
    if (-not [string]::IsNullOrWhiteSpace($PendingFailureResultHash)) { return '' }
    try {
        $path = Assert-BFSafePath (Join-Path (Join-Path (Join-Path ([string]$State.project_path) '.bsl-flow/tasks') ([string]$State.task_id)) ('attempts/' + [string]$failureId + '/result.json'))
        if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { return '' }
        return Get-BFMemoryErrorSignature (Read-BFJson $path)
    } catch { return '' }
}

function Get-BFMemoryEventFiles {
    param([string]$ProjectPath)
    $eventsDirectory = Join-Path (Get-BFMemoryDirectory $ProjectPath) 'events'
    if (-not (Test-Path -LiteralPath $eventsDirectory -PathType Container)) { return @() }
    $files = @(Get-ChildItem -LiteralPath $eventsDirectory -File -Filter '*.json' | Sort-Object Name)
    foreach ($file in $files) {
        if ($file.Name -cnotmatch '^[0-9]{6}\.json$') { throw (New-BFError 'BF_BLOCKED' ("Unexpected memory event filename: {0}." -f $file.Name)) }
    }
    return $files
}

function Get-BFMemoryEventFilesStamp {
    param($Files)
    # PowerShell turns an empty function result into `$null`; avoid treating
    # that as one synthetic file while constructing the metadata-only stamp.
    if ($null -eq $Files) { return Get-BFHash @() }
    $entries=@()
    foreach ($file in @($Files | Where-Object { $null -ne $_ })) { $entries += [ordered]@{name=[string]$file.Name;length=[int64]$file.Length;last_write_utc=$file.LastWriteTimeUtc.ToString('o')} }
    return Get-BFHash @($entries)
}

function Get-BFMemoryJsonFailureKind {
    # A parse failure is recoverable only when the object is visibly truncated
    # (the usual crash tail). A complete JSON value with an unsupported schema,
    # wrong top-level type or malformed object must disable memory instead of
    # being silently hidden as a torn event.
    param([string]$Path)
    try { $bytes=[IO.File]::ReadAllBytes((Assert-BFSafePath $Path)) } catch { return 'torn' }
    try { $text=[Text.UTF8Encoding]::new($false,$true).GetString($bytes) } catch { return 'invalid' }
    if ($text.Length -gt 0 -and $text[0] -eq [char]0xFEFF) { $text=$text.Substring(1) }
    $trim=$text.Trim()
    if ([string]::IsNullOrWhiteSpace($trim)) { return 'torn' }
    if ($trim[0] -ne '{' -and $trim[0] -ne '[') { return 'invalid' }
    $stack=[Collections.Generic.Stack[char]]::new();$inString=$false;$escaped=$false
    for($i=0;$i -lt $trim.Length;$i++){
        $ch=$trim[$i]
        if($inString){
            if($escaped){$escaped=$false;continue}
            if($ch -eq '\'){$escaped=$true;continue}
            if($ch -eq '"'){$inString=$false}
            continue
        }
        if($ch -eq '"'){$inString=$true;continue}
        if($ch -eq '{' -or $ch -eq '['){$stack.Push($ch);continue}
        if($ch -eq '}' -or $ch -eq ']'){
            if($stack.Count -eq 0){return 'invalid'}
            $open=$stack.Pop()
            if(($ch -eq '}' -and $open -ne '{') -or ($ch -eq ']' -and $open -ne '[')){return 'invalid'}
        }
    }
    if($inString -or $escaped -or $stack.Count -gt 0){return 'torn'}
    return 'invalid'
}

function Get-BFMemoryBoundedText {
    param([string]$Text, [int]$Limit)
    $value = [string]$Text
    if ($value.Length -gt $Limit) { return $value.Substring(0, $Limit).TrimEnd() }
    return $value
}

function ConvertTo-BFMemoryEvidenceRef {
    # Evidence is a reference, never copied evidence. Keep the selected record
    # bounded to one closed reference even when an event carries several refs.
    param($Reference)
    if ($null -eq $Reference -or ($Reference -isnot [System.Collections.IDictionary] -and $Reference -isnot [pscustomobject])) { return $null }
    $allowed=@('kind','task_id','attempt_id','sha256','policy','controller','version')
    $keys=if($Reference -is [System.Collections.IDictionary]){@($Reference.Keys)}else{@($Reference.PSObject.Properties.Name)}
    foreach($key in $keys){if([string]$key -cnotin $allowed){return $null}}
    $kind=[string](Get-BFValue $Reference 'kind' '')
    if([string]::IsNullOrWhiteSpace($kind) -or $kind.Length -gt 64 -or (Test-BFMemorySecretLike $kind)){return $null}
    $result=[ordered]@{kind=$kind}
    foreach($name in @('task_id','attempt_id','sha256','policy','controller','version')){
        if(-not (Test-BFObjectProperty $Reference $name)){continue}
        $value=Get-BFValue $Reference $name
        if($null -eq $value){$result[$name]=$null;continue}
        if($value -isnot [string] -or $value.Length -gt $script:BFMemoryMaxEvidenceRefChars -or (Test-BFMemorySecretLike ([string]$value))){return $null}
        $result[$name]=[string]$value
    }
    return $result
}

function Get-BFMemoryEvidenceRef {
    param($References)
    foreach($reference in @($References | Where-Object { $null -ne $_ })){
        $converted=ConvertTo-BFMemoryEvidenceRef $reference
        if($null -ne $converted){return $converted}
    }
    return $null
}

function Format-BFMemoryEvidenceRef {
    param($Reference)
    $converted=ConvertTo-BFMemoryEvidenceRef $Reference
    if($null -eq $converted){return ''}
    $parts=[Collections.Generic.List[string]]::new();$parts.Add('kind='+[string]$converted.kind)
    foreach($name in @('sha256','task_id','attempt_id','policy','controller','version')){
        $value=Get-BFValue $converted $name
        if($null -ne $value -and -not [string]::IsNullOrWhiteSpace([string]$value)){$parts.Add($name+'='+[string]$value)}
    }
    return Get-BFMemoryBoundedText ($parts -join '; ') 768
}

function ConvertTo-BFMemoryScope {
    param($Stage, [string[]]$Paths)
    $normalized = @(@($Paths) | ForEach-Object { ([string]$_).Replace('\','/').Trim('/') } | Where-Object { $_ } | Select-Object -Unique | Sort-Object { $_ })
    return [ordered]@{ stage = [string]$Stage; paths = @($normalized) }
}

function Get-BFMemoryRecordId {
    # Canonical identity: project, scope, action and mandatory environment
    # fingerprints. Same content across tasks deduplicates into one record.
    param([string]$ProjectId, $Scope, $Action, $Fingerprints, [string]$IdentityVariant = '')
    $identity=[ordered]@{project_id=$ProjectId; scope=$Scope; action=$Action; fingerprints=$Fingerprints}
    if(-not [string]::IsNullOrWhiteSpace($IdentityVariant)){$identity.identity_variant=$IdentityVariant}
    return Get-BFHash $identity
}

function Get-BFMemoryScopeKey {
    param([string]$ProjectId, $Scope)
    return Get-BFHash ([ordered]@{project_id=$ProjectId; scope=$Scope})
}

function Test-BFMemoryPathOverlap {
    param([string]$Left, [string]$Right)
    $leftPath=([string]$Left).Replace('\','/').Trim('/')
    $rightPath=([string]$Right).Replace('\','/').Trim('/')
    if ($leftPath -eq '.' -or $rightPath -eq '.' -or [string]::IsNullOrWhiteSpace($leftPath) -or [string]::IsNullOrWhiteSpace($rightPath)) { return $true }
    return $leftPath -ceq $rightPath -or $leftPath.StartsWith($rightPath + '/', [StringComparison]::Ordinal) -or $rightPath.StartsWith($leftPath + '/', [StringComparison]::Ordinal)
}

function Get-BFMemoryFingerprints {
    param($State)
    $identity = Get-BFPackageIdentity $State
    $schema=Get-BFMemorySchemaFingerprint
    $toolchain=Get-BFMemoryToolchainFingerprint $State
    return [ordered]@{policy=[string]$State.policy_hash; controller=[string](Get-BFValue $identity 'sha256' ''); version=[string](Get-BFValue $identity 'version' ''); schema=$schema; toolchain=$toolchain}
}

function Test-BFMemorySameFingerprints {
    param($Left, $Right)
    if ($null -eq $Left -or $null -eq $Right) { return $false }
    foreach ($name in $script:BFMemoryFingerprintFields) {
        # Missing schema/toolchain identity is a legacy record. It remains
        # readable and replayable, but is never silently applicable to a new
        # controller request whose boundary includes those identities.
        if (-not (Test-BFObjectProperty $Left $name) -or -not (Test-BFObjectProperty $Right $name)) { return $false }
        $leftValue = [string](Get-BFValue $Left $name '')
        $rightValue = [string](Get-BFValue $Right $name '')
        if ([string]::IsNullOrWhiteSpace($leftValue) -or [string]::IsNullOrWhiteSpace($rightValue) -or $leftValue -cne $rightValue) { return $false }
    }
    return $true
}

function Test-BFMemorySecretLike {
    param([string]$Text)
    if ([string]::IsNullOrWhiteSpace($Text)) { return $true }
    return [bool]($Text -match $script:BFMemorySecretPattern)
}

function Get-BFMemoryTemplateScopePaths {
    param($State)
    $paths=@(Get-BFValue (Get-BFValue $State 'request') 'source_paths' @('.'))
    if ($paths.Count -eq 0) { $paths=@('.') }
    foreach ($path in $paths) {
        if ([string]::IsNullOrWhiteSpace([string]$path) -or ([string]$path).Length -gt $script:BFMemoryMaxScopePathChars) { return $null }
        try { Assert-BFRelativePath ([string]$path) } catch { return $null }
    }
    return @($paths)
}

function Test-BFMemoryLowRiskSourceOnlyTask {
    # This is the closed predicate used by controller-generated procedural
    # templates. It deliberately accepts only file_assertion tasks with no
    # impact flag; a worker cannot satisfy or alter these conditions.
    param($State)
    $request=Get-BFValue $State 'request'
    $classification=Get-BFValue $State 'classification'
    if ($null -eq $request -or $null -eq $classification) { return $false }
    if ([string](Get-BFValue $request 'mode' '') -cne 'implement' -or [string](Get-BFValue $classification 'risk' '') -cne 'low') { return $false }
    if (@(Get-BFValue $classification 'impact_flags' @()).Count -gt 0 -or @((Get-BFValue $request 'impact_flags' @())).Count -gt 0) { return $false }
    $criteria=@(Get-BFValue $request 'criteria' @())
    if ($criteria.Count -eq 0) { return $false }
    if (@($criteria | Where-Object { [string](Get-BFValue $_ 'kind' '') -cne 'file_assertion' }).Count -gt 0) { return $false }
    return $true
}

function Get-BFMemoryAcceptedTemplateItem {
    # The text is a fixed controller template. Its predicate is proven by the
    # acceptance receipt and fresh controller evidence; no worker observation,
    # summary, changed-file list or criterion prose is copied into memory.
    param($State, $Receipt)
    if ($null -eq $Receipt -or [string](Get-BFValue $Receipt 'verdict' '') -cne 'PASS') { return $null }
    if (-not (Test-BFMemoryLowRiskSourceOnlyTask $State)) { return $null }
    $gates=@(Get-BFValue $Receipt 'gates' @())
    if (@($gates | Where-Object { [string](Get-BFValue $_ 'stage' '') -eq 'implement' }).Count -eq 0 -or @($gates | Where-Object { [string](Get-BFValue $_ 'stage' '') -eq 'verify' }).Count -eq 0) { return $null }
    $paths=Get-BFMemoryTemplateScopePaths $State
    if ($null -eq $paths) { return $null }
    return [ordered]@{template_id=$script:BFMemoryTemplateAcceptedSourceOnly; evidence_kind='accepted-result'; provenance='controller-template'; task_kind=(Get-BFMemoryTaskKind $State); error_signature=''; item=[ordered]@{scope=(ConvertTo-BFMemoryScope 'implement' $paths);observation=$script:BFMemoryTemplateObservationAcceptedSourceOnly;action=[ordered]@{type='recommended';text=$script:BFMemoryTemplateActionAcceptedSourceOnly};knowledge_class='procedural';risk_class='low'}}
}

function Get-BFMemorySuccessfulRecoveryTemplateItem {
    # A source-only recovery receipt proves only the control-read/retry
    # procedure. Native/runtime and external effects are intentionally excluded.
    param($State, $Resolution, $Manifest)
    if ($null -eq $Resolution -or [string](Get-BFValue $Resolution 'scope' '') -cne 'source_only') { return $null }
    if ($null -eq $Manifest -or [string]$Resolution.source_sha256 -cne [string]$Manifest.sha256) { return $null }
    if (-not (Test-BFMemoryLowRiskSourceOnlyTask $State)) { return $null }
    $paths=Get-BFMemoryTemplateScopePaths $State
    if ($null -eq $paths) { return $null }
    return [ordered]@{template_id=$script:BFMemoryTemplateSuccessfulRecovery; evidence_kind='successful-recovery'; provenance='controller-template'; task_kind=(Get-BFMemoryTaskKind $State); error_signature=''; item=[ordered]@{scope=(ConvertTo-BFMemoryScope 'recover' $paths);observation=$script:BFMemoryTemplateObservationSuccessfulRecovery;action=[ordered]@{type='recommended';text=$script:BFMemoryTemplateActionSuccessfulRecovery};knowledge_class='procedural';risk_class='low'}}
}

function ConvertTo-BFMemoryItem {
    # Validates one proposed memory item. The same validator backs the strict
    # stage contract (ok must be true or the attempt is blocked) and the
    # best-effort extractor (rejections become audited marker events).
    param($Item, [string]$Stage)
    $reject = { param([string]$Reason) return [ordered]@{ok=$false; item=$null; reason=$Reason} }
    if ($null -eq $Item -or ($Item -isnot [System.Collections.IDictionary] -and $Item -isnot [pscustomobject])) { return & $reject 'invalid-shape' }
    $keys = @()
    if ($Item -is [System.Collections.IDictionary]) { $keys = @($Item.Keys) } else { $keys = @($Item.PSObject.Properties.Name) }
    foreach ($key in @('scope','observation','action_type','action','knowledge_class','risk_class')) {
        if ($key -cnotin $keys) { return & $reject 'invalid-shape' }
    }
    foreach ($key in $keys) { if ($key -cnotin @('scope','observation','action_type','action','knowledge_class','risk_class')) { return & $reject 'invalid-shape' } }
    $scopePaths = $Item.scope
    if ($scopePaths -isnot [array] -or @($scopePaths).Count -eq 0) { return & $reject 'invalid-shape' }
    if (@($scopePaths).Count -gt $script:BFMemoryMaxScopePaths) { return & $reject 'scope-too-large' }
    foreach ($path in @($scopePaths)) {
        if ([string]::IsNullOrWhiteSpace([string]$path)) { return & $reject 'invalid-shape' }
        if (([string]$path).Length -gt $script:BFMemoryMaxScopePathChars) { return & $reject 'scope-path-too-long' }
        try { Assert-BFRelativePath ([string]$path) } catch { return & $reject 'forbidden-scope-path' }
        if (([string]$path).Replace('\','/') -match '(^|/)\.(bsl-flow|bsl-flow-worker|git)(/|$)') { return & $reject 'forbidden-scope-path' }
    }
    $observation = [string]$Item.observation
    if ([string]::IsNullOrWhiteSpace($observation) -or $observation.Length -gt $script:BFMemoryMaxObservationChars) { return & $reject 'observation-too-long' }
    if (Test-BFMemorySecretLike $observation) { return & $reject 'secret-like-content' }
    $actionText = [string]$Item.action
    if ([string]::IsNullOrWhiteSpace($actionText) -or $actionText.Length -gt $script:BFMemoryMaxActionChars) { return & $reject 'action-too-long' }
    if (Test-BFMemorySecretLike $actionText) { return & $reject 'secret-like-content' }
    if ([string]$Item.action_type -cnotin $script:BFMemoryActionTypes) { return & $reject 'invalid-action-type' }
    if ([string]$Item.knowledge_class -cnotin $script:BFMemoryWorkerClasses) { return & $reject 'class-not-proposable' }
    if ([string]$Item.risk_class -cnotin $script:BFMemoryRiskClasses) { return & $reject 'invalid-risk-class' }
    $scope = ConvertTo-BFMemoryScope $Stage @($scopePaths)
    $action = [ordered]@{type=[string]$Item.action_type; text=$actionText}
    return [ordered]@{ok=$true; item=[ordered]@{scope=$scope; observation=$observation; action=$action; knowledge_class=[string]$Item.knowledge_class; risk_class=[string]$Item.risk_class}; reason=$null}
}

function Assert-BFMemoryObservations {
    # Stage contract for optional implement payload observations: closed shape,
    # bounded text, worker-proposable classes only. Invalid content blocks the
    # attempt with a concrete reason instead of being silently stored.
    param($Observations, [string]$Stage)
    if ($Observations -isnot [array] -or @($Observations).Count -eq 0) { throw 'BF_INVALID: observations must be a nonempty array when present.' }
    if (@($Observations).Count -gt $script:BFMemoryMaxObservationItems) { throw "BF_INVALID: observations cannot contain more than $($script:BFMemoryMaxObservationItems) items." }
    foreach ($observation in @($Observations)) {
        $check = ConvertTo-BFMemoryItem $observation $Stage
        if (-not $check.ok) { throw ("BF_INVALID: invalid worker observation ({0})." -f $check.reason) }
    }
}

function New-BFMemoryEventObject {
    param([string]$ProjectId, $Fingerprints, $Entry, [string]$PreviousEventId)
    $payload = [ordered]@{
        schema_version=1; project_id=$ProjectId; timestamp=[DateTime]::UtcNow.ToString('o')
        record_id=(Get-BFValue $entry 'record_id'); event_type=[string]$entry.event_type
        knowledge_class=(Get-BFValue $entry 'knowledge_class'); risk_class=(Get-BFValue $entry 'risk_class')
        scope=(Get-BFValue $entry 'scope'); observation=(Get-BFValue $entry 'observation')
        action=(Get-BFValue $entry 'action'); superseded_by=(Get-BFValue $entry 'superseded_by')
        evidence_refs=@(Get-BFValue $entry 'evidence_refs' @()); fingerprints=$Fingerprints
        source_task_id=(Get-BFValue $entry 'source_task_id'); source_attempt_id=(Get-BFValue $entry 'source_attempt_id')
        reason=(Get-BFValue $entry 'reason'); previous_event_id=if([string]::IsNullOrEmpty($PreviousEventId)){$null}else{$PreviousEventId}
        task_kind=(Get-BFValue $entry 'task_kind' ''); error_signature=(Get-BFValue $entry 'error_signature' '')
        evidence_kind=(Get-BFValue $entry 'evidence_kind' ''); provenance=(Get-BFValue $entry 'provenance' '')
    }
    $payload.event_id = Get-BFHash $payload
    $payload.content_hash = Get-BFHash $payload
    return $payload
}

function Assert-BFMemoryEventShape {
    param($Event)
    if ($Event -isnot [System.Collections.IDictionary] -and $Event -isnot [pscustomobject]) { throw (New-BFError 'BF_BLOCKED' 'Memory event must be an object.') }
    $requiredFields=@('schema_version','project_id','event_id','timestamp','record_id','event_type','knowledge_class','risk_class','scope','observation','action','superseded_by','evidence_refs','fingerprints','source_task_id','source_attempt_id','reason','previous_event_id','content_hash')
    $knownFields=$requiredFields+@('task_kind','error_signature','evidence_kind','provenance')
    foreach ($field in $requiredFields) {
        if (-not (Test-BFObjectProperty $Event $field)) { throw (New-BFError 'BF_BLOCKED' ("Memory event field {0} is missing." -f $field)) }
    }
    $keys = @()
    if ($Event -is [System.Collections.IDictionary]) { $keys = @($Event.Keys) } else { $keys = @($Event.PSObject.Properties.Name) }
    foreach ($key in $keys) { if ([string]$key -cnotin $knownFields) { throw (New-BFError 'BF_BLOCKED' ("Unknown memory event field {0}." -f $key)) } }
    if ($Event.schema_version -cne 1) { throw (New-BFError 'BF_BLOCKED' 'Unsupported memory event schema version.') }
    if ([string]$Event.event_type -cnotin $script:BFMemoryEventTypes) { throw (New-BFError 'BF_BLOCKED' 'Unknown memory event type.') }
    if ($null -ne $Event.knowledge_class -and [string]$Event.knowledge_class -cnotin $script:BFMemoryKnowledgeClasses) { throw (New-BFError 'BF_BLOCKED' 'Unknown memory knowledge class.') }
    if ($null -ne $Event.risk_class -and [string]$Event.risk_class -cnotin $script:BFMemoryRiskClasses) { throw (New-BFError 'BF_BLOCKED' 'Unknown memory risk class.') }
    if ([string]$Event.event_id -cnotmatch '^[0-9a-f]{64}$' -or [string]$Event.content_hash -cnotmatch '^[0-9a-f]{64}$') { throw (New-BFError 'BF_BLOCKED' 'Memory event identity hashes are malformed.') }
    if ($Event.evidence_refs -isnot [array]) { throw (New-BFError 'BF_BLOCKED' 'Memory event evidence_refs must be an array.') }
    if (@($Event.evidence_refs).Count -gt $script:BFMemoryMaxEvidenceRefs) { throw (New-BFError 'BF_BLOCKED' 'Memory event has too many evidence references.') }
    foreach($reference in @($Event.evidence_refs)){ if($null -eq (ConvertTo-BFMemoryEvidenceRef $reference)){throw (New-BFError 'BF_BLOCKED' 'Memory event evidence reference is malformed.') } }
    foreach ($name in @('task_kind','error_signature','evidence_kind','provenance')) {
        if ($null -ne (Get-BFValue $Event $name) -and (Get-BFValue $Event $name) -isnot [string]) { throw (New-BFError 'BF_BLOCKED' ("Memory event {0} must be a string or null." -f $name)) }
    }
    if (([string](Get-BFValue $Event 'task_kind' '')).Length -gt 256) { throw (New-BFError 'BF_BLOCKED' 'Memory event task_kind is too long.') }
    if (([string](Get-BFValue $Event 'error_signature' '')).Length -gt 64 -and [string](Get-BFValue $Event 'error_signature' '') -cne '') { throw (New-BFError 'BF_BLOCKED' 'Memory event error_signature is malformed.') }
    if ([string](Get-BFValue $Event 'evidence_kind' '') -cnotin @('','accepted-result','confirmed-error','successful-recovery')) { throw (New-BFError 'BF_BLOCKED' 'Unknown memory evidence kind.') }
    if ([string](Get-BFValue $Event 'provenance' '') -cnotin @('','controller-template','controller-error')) { throw (New-BFError 'BF_BLOCKED' 'Unknown memory provenance.') }
    if ($null -ne $Event.scope) {
        foreach ($field in @('stage','paths')) { if (-not (Test-BFObjectProperty $Event.scope $field)) { throw (New-BFError 'BF_BLOCKED' 'Memory event scope requires stage and paths.') } }
        if ($Event.scope.paths -isnot [array]) { throw (New-BFError 'BF_BLOCKED' 'Memory event scope.paths must be an array.') }
        if (@($Event.scope.paths).Count -gt $script:BFMemoryMaxScopePaths) { throw (New-BFError 'BF_BLOCKED' 'Memory event scope has too many paths.') }
        foreach ($path in @($Event.scope.paths)) { if (([string]$path).Length -gt $script:BFMemoryMaxScopePathChars) { throw (New-BFError 'BF_BLOCKED' 'Memory event scope path is too long.') } }
    }
    if ($null -ne $Event.action) {
        foreach ($field in @('type','text')) { if (-not (Test-BFObjectProperty $Event.action $field)) { throw (New-BFError 'BF_BLOCKED' 'Memory event action requires type and text.') } }
        if ([string]$Event.action.type -cnotin $script:BFMemoryActionTypes) { throw (New-BFError 'BF_BLOCKED' 'Unknown memory action type.') }
    }
    if ([string]$Event.event_type -ceq 'superseded' -and ($null -eq $Event.superseded_by -or [string]$Event.superseded_by -ceq [string]$Event.record_id)) { throw (New-BFError 'BF_BLOCKED' 'Superseding event requires a different replacement record.') }
    $payload = [ordered]@{
        schema_version=$Event.schema_version; project_id=$Event.project_id; timestamp=$Event.timestamp
        record_id=$Event.record_id; event_type=$Event.event_type; knowledge_class=$Event.knowledge_class
        risk_class=$Event.risk_class; scope=$Event.scope; observation=$Event.observation; action=$Event.action
        superseded_by=$Event.superseded_by; evidence_refs=@($Event.evidence_refs); fingerprints=$Event.fingerprints
        source_task_id=$Event.source_task_id; source_attempt_id=$Event.source_attempt_id
        reason=$Event.reason; previous_event_id=$Event.previous_event_id
    }
    # New fields are optional in v1 so historical events remain replayable. A
    # new event always has them and therefore hashes them into its identity.
    foreach ($name in @('task_kind','error_signature','evidence_kind','provenance')) {
        if (Test-BFObjectProperty $Event $name) { $payload[$name] = Get-BFValue $Event $name }
    }
    if ((Get-BFHash $payload) -cne [string]$Event.event_id) { throw (New-BFError 'BF_BLOCKED' ("Memory event {0} failed its identity hash." -f $Event.event_id)) }
    $payload.event_id = $Event.event_id
    if ((Get-BFHash $payload) -cne [string]$Event.content_hash) { throw (New-BFError 'BF_BLOCKED' ("Memory event {0} failed its content hash." -f $Event.event_id)) }
    return $Event
}

function Copy-BFMemoryRecord {
    param($Record)
    $copy = [ordered]@{}
    if ($Record -is [System.Collections.IDictionary]) { foreach ($key in @($Record.Keys)) { $copy[[string]$key] = $Record[$key] } }
    else { foreach ($property in $Record.PSObject.Properties) { $copy[$property.Name] = $property.Value } }
    return $copy
}

function Invoke-BFMemoryEventOnRecords {
    # Applies one event to the replayed record state. Structural transition and
    # promotion-policy violations fail closed and abort the whole write plan.
    param($Records, $Event)
    $recordId = [string]$Event.record_id
    $record = if ($recordId) { $Records[$recordId] } else { $null }
    switch ([string]$Event.event_type) {
        'rejected' { if ($recordId) { throw (New-BFError 'BF_CONFLICT' 'Rejected marker events carry no record identity.') } }
        'candidate' {
            if ($null -ne $record) { throw (New-BFError 'BF_CONFLICT' 'Candidate event duplicates an existing record identity.') }
            # The accepted controller outcome that produced the observation is
            # the first confirmation; independent confirmations come from other
            # events, and the promotion threshold additionally requires two
            # different task IDs.
            $evidenceKind = [string](Get-BFValue $Event 'evidence_kind' '')
            $initialConfirmations = if ($evidenceKind -eq 'accepted-result' -or [string]::IsNullOrWhiteSpace($evidenceKind)) { 1 } else { 0 }
            $initialTasks = if ($initialConfirmations -gt 0 -and -not [string]::IsNullOrWhiteSpace([string]$Event.source_task_id)) { @([string]$Event.source_task_id) } else { @() }
            $taskKinds = if (-not [string]::IsNullOrWhiteSpace([string](Get-BFValue $Event 'task_kind' ''))) { @([string]$Event.task_kind) } else { @() }
            $errorSignatures = if (-not [string]::IsNullOrWhiteSpace([string](Get-BFValue $Event 'error_signature' ''))) { @([string]$Event.error_signature) } else { @() }
            $eventEvidenceKinds = if (-not [string]::IsNullOrWhiteSpace([string](Get-BFValue $Event 'evidence_kind' ''))) { @([string]$Event.evidence_kind) } else { @() }
            $initialEvidenceRef=Get-BFMemoryEvidenceRef $Event.evidence_refs
            $initialTasksArray=@($initialTasks | Where-Object { -not [string]::IsNullOrWhiteSpace([string]$_) })
            $taskKindsArray=@($taskKinds | Where-Object { -not [string]::IsNullOrWhiteSpace([string]$_) })
            $errorSignaturesArray=@($errorSignatures | Where-Object { -not [string]::IsNullOrWhiteSpace([string]$_) })
            $eventEvidenceKindsArray=@($eventEvidenceKinds | Where-Object { -not [string]::IsNullOrWhiteSpace([string]$_) })
            $Records[$recordId] = [ordered]@{record_id=$recordId; state='candidate'; knowledge_class=[string]$Event.knowledge_class; risk_class=[string]$Event.risk_class
                scope=$Event.scope; observation=$Event.observation; action=$Event.action; confirmations=$initialConfirmations; confirmation_tasks=$initialTasksArray; contradictions=0
                superseded_by=$null; fingerprints=$Event.fingerprints; evidence_count=1; first_event_id=$Event.event_id; last_event_id=$Event.event_id; last_reason=[string]$Event.reason
                task_kind=[string](Get-BFValue $Event 'task_kind' ''); task_kinds=$taskKindsArray; error_signatures=$errorSignaturesArray; evidence_kinds=$eventEvidenceKindsArray; evidence_ref=$initialEvidenceRef; provenance=[string](Get-BFValue $Event 'provenance' '')}
        }
        'confirmed' {
            if ($null -eq $record -or [string]$record.state -cnotin $script:BFMemoryActiveStates) { throw (New-BFError 'BF_CONFLICT' 'Confirmation requires an active record.') }
            $record.confirmations = [int]$record.confirmations + 1
            $task = [string]$Event.source_task_id
            if ($task -cnotin @($record.confirmation_tasks)) { $record.confirmation_tasks = @(@($record.confirmation_tasks) + $task) }
            $record.evidence_count = [int]$record.evidence_count + 1; $record.last_event_id = $Event.event_id; $record.last_reason = [string]$Event.reason
            $evidenceKind = [string](Get-BFValue $Event 'evidence_kind' '')
            if (-not [string]::IsNullOrWhiteSpace($evidenceKind) -and $evidenceKind -notin @(Get-BFValue $record 'evidence_kinds' @())) { $record.evidence_kinds = @(@(Get-BFValue $record 'evidence_kinds' @()) + $evidenceKind) | Select-Object -Unique }
            $evidenceRef=Get-BFMemoryEvidenceRef $Event.evidence_refs
            if($null -ne $evidenceRef){$record.evidence_ref=$evidenceRef}
            $taskKind = [string](Get-BFValue $Event 'task_kind' '')
            if (-not [string]::IsNullOrWhiteSpace($taskKind) -and [string]::IsNullOrWhiteSpace([string](Get-BFValue $record 'task_kind' ''))) { $record.task_kind=$taskKind }
            if (-not [string]::IsNullOrWhiteSpace($taskKind) -and $taskKind -cnotin @(Get-BFValue $record 'task_kinds' @())) { $record.task_kinds = @(@(Get-BFValue $record 'task_kinds' @()) + $taskKind) | Select-Object -Unique }
            $errorSignature = [string](Get-BFValue $Event 'error_signature' '')
            if (-not [string]::IsNullOrWhiteSpace($errorSignature) -and $errorSignature -cnotin @($record.error_signatures)) { $record.error_signatures = @(@(Get-BFValue $record 'error_signatures' @()) + $errorSignature) | Select-Object -Unique }
        }
        'shadow' {
            if ($null -eq $record -or [string]$record.state -cnotin @('candidate','shadow')) { throw (New-BFError 'BF_CONFLICT' 'Shadow transition requires an unpromoted active record.') }
            $record.state='shadow'; $record.last_event_id=$Event.event_id; $record.last_reason=[string]$Event.reason
        }
        'promoted' {
            if ($null -eq $record -or [string]$record.state -cnotin @('candidate','shadow')) { throw (New-BFError 'BF_CONFLICT' 'Promotion requires an unpromoted active record.') }
            if ([string]$record.knowledge_class -cne 'procedural' -or [string]$record.risk_class -cne 'low') { throw (New-BFError 'BF_CONFLICT' 'Promotion is restricted to procedural low-risk records.') }
            $legacyPromotion = -not (Test-BFObjectProperty $Event 'provenance') -and [string]::IsNullOrWhiteSpace([string](Get-BFValue $record 'provenance' ''))
            if (-not $legacyPromotion -and ([string](Get-BFValue $record 'provenance' '') -cne 'controller-template' -or @((Get-BFValue $record 'evidence_kinds' @()) | Where-Object { $_ -in @('accepted-result','successful-recovery') }).Count -eq 0)) { throw (New-BFError 'BF_CONFLICT' 'Promotion requires a controller-generated procedural template with accepted evidence.') }
            if ([int]$record.contradictions -gt 0) { throw (New-BFError 'BF_CONFLICT' 'Promotion requires a contradiction-free record.') }
            if ([int]$record.confirmations -lt 3 -or @($record.confirmation_tasks).Count -lt 2) { throw (New-BFError 'BF_CONFLICT' 'Promotion requires three confirmations from two different tasks.') }
            $record.state='accepted'; $record.last_event_id=$Event.event_id; $record.last_reason=[string]$Event.reason
        }
        'contradicted' {
            if ($null -eq $record -or [string]$record.state -cnotin $script:BFMemoryActiveStates) { throw (New-BFError 'BF_CONFLICT' 'Contradiction requires an active record.') }
            $record.contradictions=[int]$record.contradictions+1; $record.last_event_id=$Event.event_id; $record.last_reason=[string]$Event.reason
        }
        'quarantined' {
            if ($null -eq $record -or [string]$record.state -cnotin $script:BFMemoryActiveStates) { throw (New-BFError 'BF_CONFLICT' 'Quarantine requires an active record.') }
            $record.state='quarantined'; $record.last_event_id=$Event.event_id; $record.last_reason=[string]$Event.reason
        }
        'deprecated' {
            if ($null -eq $record -or [string]$record.state -cnotin $script:BFMemoryActiveStates) { throw (New-BFError 'BF_CONFLICT' 'Deprecation requires an active record.') }
            $record.state='deprecated'; $record.last_event_id=$Event.event_id; $record.last_reason=[string]$Event.reason
        }
        'superseded' {
            if ($null -eq $record -or [string]$record.state -cne 'accepted') { throw (New-BFError 'BF_CONFLICT' 'Superseding requires an accepted record.') }
            if (-not $Event.superseded_by) { throw (New-BFError 'BF_CONFLICT' 'Superseding requires a replacement record identity.') }
            $record.state='superseded'; $record.superseded_by=[string]$Event.superseded_by; $record.last_event_id=$Event.event_id; $record.last_reason=[string]$Event.reason
        }
        'reinstated' {
            if ($null -eq $record -or [string]$record.state -cne 'quarantined') { throw (New-BFError 'BF_CONFLICT' 'Reinstatement requires a quarantined record.') }
            $record.state='shadow'; $record.last_event_id=$Event.event_id; $record.last_reason=[string]$Event.reason
        }
        default { throw (New-BFError 'BF_BLOCKED' 'Unknown memory event type.') }
    }
}

function Get-BFMemoryRecordsCopy {
    param($Records)
    $copy = @{}
    foreach ($recordId in $Records.Keys) { $copy[$recordId] = Copy-BFMemoryRecord $Records[$recordId] }
    return $copy
}

function Get-BFMemoryReplay {
    # Replays the authoritative append-only event log into record state. A
    # visibly truncated/unreadable tail is skipped and reported; a complete but
    # unsupported or malformed event is genuine corruption and fails closed.
    param([string]$ProjectPath)
    $files = Get-BFMemoryEventFiles $ProjectPath
    if (@($files).Count -gt $script:BFMemoryMaxEventFiles) { throw (New-BFError 'BF_BLOCKED' ("Memory event file count exceeds the supported limit of {0}." -f $script:BFMemoryMaxEventFiles)) }
    $eventFilesStamp=Get-BFMemoryEventFilesStamp $files
    $events = @(); $torn = @(); $previous = $null; $records = @{}; $maxSeq = 0; $expectedSeq=1; $sawTorn=$false
    foreach ($file in $files) {
        $sequence=[int]$file.BaseName
        if($sequence -ne $expectedSeq){throw (New-BFError 'BF_BLOCKED' ("Memory event sequence has a gap before {0}." -f $file.Name))}
        $maxSeq = $sequence; $expectedSeq++
        if ($file.Length -gt $script:BFMemoryMaxEventBytes) {
            # A size limit is not a license to hide a complete forged event.
            # Only a visibly truncated tail remains recoverable; any balanced
            # or otherwise complete oversized JSON disables the ledger.
            if ((Get-BFMemoryJsonFailureKind $file.FullName) -eq 'torn') { $torn += [string]$file.Name; $sawTorn=$true; continue }
            throw (New-BFError 'BF_BLOCKED' ("Memory event exceeds the supported size and is complete or invalid at {0}." -f $file.Name))
        }
        $event = $null
        try {
            $event = Read-BFJson $file.FullName
        } catch {
            if((Get-BFMemoryJsonFailureKind $file.FullName) -eq 'torn'){$torn += [string]$file.Name;$sawTorn=$true;continue}
            throw (New-BFError 'BF_BLOCKED' ("Memory event JSON is complete but invalid at {0}: {1}" -f $file.Name,$_.Exception.Message))
        }
        try { [void](Assert-BFMemoryEventShape $event) } catch { throw (New-BFError 'BF_BLOCKED' ("Memory event is complete but invalid at {0}: {1}" -f $file.Name,$_.Exception.Message)) }
        if($sawTorn){throw (New-BFError 'BF_BLOCKED' ("Memory event follows a torn event at {0}; the append-only tail cannot be reconciled." -f $file.Name))}
        if (([string]$event.previous_event_id) -cne ([string]$previous)) { throw (New-BFError 'BF_BLOCKED' ("Memory event chain is broken at {0}." -f $file.Name)) }
        if (@($events).Count -gt 0 -and [string]$event.project_id -cne [string]$events[0].project_id) { throw (New-BFError 'BF_BLOCKED' ("Memory event project changed at {0}." -f $file.Name)) }
        if ([string]$event.project_id -cne (Assert-BFSafePath $ProjectPath)) { throw (New-BFError 'BF_BLOCKED' ("Memory event project does not match the requested project at {0}." -f $file.Name)) }
        Invoke-BFMemoryEventOnRecords $records $event
        $events += $event
        $previous = [string]$event.event_id
    }
    $projectId = $null
    if (@($events).Count -gt 0) { $projectId = [string]$events[0].project_id }
    $sortedRecords=[ordered]@{}
    foreach ($recordId in @($records.Keys | Sort-Object { $_ })) { $sortedRecords[$recordId]=$records[$recordId] }
    return [ordered]@{events=@($events); torn=@($torn); records=$records; last_event_id=$previous; next_seq=($maxSeq+1); project_id=$projectId; event_files_count=@($files).Count; last_event_seq=$maxSeq; event_files_stamp=$eventFilesStamp; records_sha256=(Get-BFHash $sortedRecords); replayed=$true}
}

function New-BFMemoryIndexObject {
    param([string]$ProjectId, $Events, $Records, $Torn, [int]$EventFilesCount = -1, [int]$LastEventSeq = -1)
    $quarantined=@(); $deprecated=@(); $superseded=@()
    foreach ($recordId in ($Records.Keys | Sort-Object { $_ })) {
        $state = [string]$Records[$recordId].state
        if ($state -ceq 'quarantined') { $quarantined += $recordId }
        elseif ($state -ceq 'deprecated') { $deprecated += $recordId }
        elseif ($state -ceq 'superseded') { $superseded += $recordId }
    }
    $lastEventId = $null
    if (@($Events).Count -gt 0) { $lastEventId = [string]$Events[-1].event_id }
    $sortedRecords=[ordered]@{}
    foreach ($recordId in @($Records.Keys | Sort-Object { $_ })) { $sortedRecords[$recordId]=$Records[$recordId] }
    $maxSeq=$LastEventSeq
    if ($maxSeq -lt 0) {
        $maxSeq=0
        foreach ($name in @($Torn)) { if ([string]$name -match '^([0-9]{6})\.json$') { $maxSeq=[Math]::Max($maxSeq,[int]$Matches[1]) } }
    }
    $eventFilesCount=if($EventFilesCount -ge 0){$EventFilesCount}else{@($Events).Count+@($Torn).Count}
    $eventFilesStamp=''
    try { $eventFilesStamp=Get-BFMemoryEventFilesStamp (Get-BFMemoryEventFiles $ProjectId) } catch { $eventFilesStamp=Get-BFHash @() }
    return [ordered]@{schema_version=1; project_id=$ProjectId; events_count=@($Events).Count; event_files_count=$eventFilesCount; last_event_seq=$maxSeq; event_files_stamp=$eventFilesStamp; last_event_id=$lastEventId; torn_events=@($Torn); quarantined_ids=@($quarantined); deprecated_ids=@($deprecated); superseded_ids=@($superseded); records_sha256=(Get-BFHash $sortedRecords); records=$Records}
}

function Test-BFMemoryIndexFresh {
    param($Index, $Replay)
    if ($null -eq $Index) { return $false }
    try {
        if ([int]$Index.schema_version -ne 1) { return $false }
        if ([int]$Index.events_count -ne @($Replay.events).Count) { return $false }
        if ((Test-BFObjectProperty $Index 'event_files_count') -and [int]$Index.event_files_count -ne [int](Get-BFValue $Replay 'event_files_count' 0)) { return $false }
        if ((Test-BFObjectProperty $Index 'last_event_seq') -and [int]$Index.last_event_seq -ne [int](Get-BFValue $Replay 'last_event_seq' 0)) { return $false }
        if (([string]$Index.last_event_id) -cne [string]$Replay.last_event_id) { return $false }
        if ((@($Index.torn_events) -join '|') -cne (@($Replay.torn) -join '|')) { return $false }
        if ((Test-BFObjectProperty $Index 'records_sha256') -and [string]$Index.records_sha256 -cne [string](Get-BFValue $Replay 'records_sha256' '')) { return $false }
        if ((Test-BFObjectProperty $Index 'event_files_stamp') -and [string]$Index.event_files_stamp -cne [string](Get-BFValue $Replay 'event_files_stamp' '')) { return $false }
        return $true
    } catch { return $false }
}

function Test-BFMemoryIndexUsable {
    # Retrieval may use a valid derived index without replaying every event on
    # every prompt. Directory metadata detects append activity cheaply; the
    # records hash detects ordinary projection tampering. A missing legacy
    # freshness marker deliberately falls back to a full replay.
    param($Index, [string]$ProjectPath)
    try {
        if ($null -eq $Index -or [int]$Index.schema_version -ne 1) { return $false }
        foreach ($field in @('project_id','events_count','event_files_count','last_event_seq','event_files_stamp','last_event_id','torn_events','records_sha256','records')) {
            if (-not (Test-BFObjectProperty $Index $field)) { return $false }
        }
        if ([string]::IsNullOrWhiteSpace([string]$Index.project_id) -or (Assert-BFSafePath ([string]$ProjectPath)) -cne (Assert-BFSafePath ([string]$Index.project_id))) { return $false }
        if ($Index.records -isnot [System.Collections.IDictionary] -and $Index.records -isnot [pscustomobject]) { return $false }
        if ([string]$Index.records_sha256 -notmatch '^[0-9a-f]{64}$') { return $false }
        if ([string]$Index.event_files_stamp -notmatch '^[0-9a-f]{64}$') { return $false }
        $sorted=[ordered]@{}
        $recordKeys=if($Index.records -is [System.Collections.IDictionary]){@($Index.records.Keys)}else{@($Index.records.PSObject.Properties.Name)}
        foreach ($recordId in @($recordKeys | Sort-Object { $_ })) { $sorted[[string]$recordId]=Get-BFValue $Index.records ([string]$recordId) }
        if ((Get-BFHash $sorted) -cne [string]$Index.records_sha256) { return $false }
        $files=Get-BFMemoryEventFiles $ProjectPath
        if (@($files).Count -ne [int]$Index.event_files_count) { return $false }
        if (@($Index.torn_events).Count -gt 0) { return $false }
        if ([int]$Index.events_count -ne @($files).Count) { return $false }
        $lastSeq=0
        if (@($files).Count -gt 0) { $lastSeq=[int]$files[-1].BaseName }
        if ($lastSeq -ne [int]$Index.last_event_seq) { return $false }
        if (@($files).Count -gt 0) {
            # Verify the indexed head against the authoritative event file;
            # this is one bounded read and avoids replaying the whole ledger on
            # every retrieval while still rejecting a forged index head.
            $head=Read-BFJson $files[-1].FullName
            [void](Assert-BFMemoryEventShape $head)
            if ([string]$head.project_id -cne [string]$Index.project_id -or [string]$head.event_id -cne [string]$Index.last_event_id) { return $false }
        } elseif ($null -ne $Index.last_event_id) { return $false }
        $currentStamp=Get-BFHash (@($files | ForEach-Object { [ordered]@{name=[string]$_.Name;length=[int64]$_.Length;last_write_utc=$_.LastWriteTimeUtc.ToString('o')} }))
        if ($currentStamp -cne [string]$Index.event_files_stamp) { return $false }
        return $true
    } catch { return $false }
}

function Get-BFMemoryReadSource {
    # Returns either the bounded derived index or a replay result. `replayed`
    # is diagnostic metadata only; it never changes task disposition.
    param([string]$ProjectPath)
    $indexPath=Join-Path (Get-BFMemoryDirectory $ProjectPath) 'index.json'
    if (Test-Path -LiteralPath $indexPath -PathType Leaf) {
        try {
            $index=Read-BFJson $indexPath
            if (Test-BFMemoryIndexUsable $index $ProjectPath) {
                $indexedRecords=@{}
                if ($index.records -is [System.Collections.IDictionary]) { foreach ($recordId in @($index.records.Keys)) { $indexedRecords[[string]$recordId]=$index.records[$recordId] } }
                else { foreach ($property in $index.records.PSObject.Properties) { $indexedRecords[[string]$property.Name]=$property.Value } }
                return [ordered]@{events=@();events_count=[int]$index.events_count;torn=@($index.torn_events);records=$indexedRecords;last_event_id=$index.last_event_id;next_seq=([int]$index.last_event_seq+1);project_id=[string]$index.project_id;event_files_count=[int]$index.event_files_count;last_event_seq=[int]$index.last_event_seq;event_files_stamp=[string]$index.event_files_stamp;records_sha256=[string]$index.records_sha256;replayed=$false;indexed=$true}
            }
        } catch {
            # The authoritative replay below decides whether memory is still
            # usable. A corrupt derived index alone is not a task blocker.
        }
    }
    $replay=Get-BFMemoryReplay $ProjectPath
    $replay.indexed=$false
    return $replay
}

function Add-BFMemoryEvents {
    # Single locked write session: replay, validate the whole plan against the
    # state machine, then append every event in deterministic seq order and
    # persist the derived index.
    param([string]$ProjectPath, [string]$ProjectId, $Fingerprints, $Plan)
    $directory = Get-BFMemoryDirectory $ProjectPath
    $lock = Enter-BFLock $directory
    try {
        $replay = Get-BFMemoryReplay $ProjectPath
        if ($null -ne $replay.project_id -and $replay.project_id -cne $ProjectId) { throw (New-BFError 'BF_CONFLICT' 'Memory store belongs to a different project.') }
        if (@($replay.torn).Count -gt 0) { throw (New-BFError 'BF_BLOCKED' 'Memory store has an unresolved torn tail; repair or quarantine it before appending.') }
        $records = Get-BFMemoryRecordsCopy $replay.records
        $previous = $replay.last_event_id
        $appended = @()
        foreach ($entry in @($Plan)) {
            $event = New-BFMemoryEventObject -ProjectId $ProjectId -Fingerprints $Fingerprints -Entry $entry -PreviousEventId $previous
            Invoke-BFMemoryEventOnRecords $records $event
            $appended += $event
            $previous = [string]$event.event_id
        }
        $writeSeq = [int]$replay.next_seq
        foreach ($event in $appended) {
            Write-BFJson -Path (Join-Path $directory ('events/{0:D6}.json' -f $writeSeq)) -Value $event
            $writeSeq++
        }
        $index = New-BFMemoryIndexObject -ProjectId $ProjectId -Events (@($replay.events) + @($appended)) -Records $records -Torn $replay.torn -EventFilesCount ($replay.event_files_count + @($appended).Count) -LastEventSeq ($writeSeq - 1)
        Write-BFJson -Path (Join-Path $directory 'index.json') -Value $index -Replace
        return $index
    } finally { $lock.Dispose() }
}

function Test-BFMemoryPromotionEligible {
    # Pure deterministic promotion policy over the replayed record and the
    # current version-controlled environment fingerprints.
    param($Record, $Fingerprints)
    if ($null -eq $Record) { return $false }
    if ([string]$Record.state -cnotin @('candidate','shadow')) { return $false }
    if ([string]$Record.knowledge_class -cne 'procedural' -or [string]$Record.risk_class -cne 'low') { return $false }
    if ([string](Get-BFValue $Record 'provenance' '') -cne 'controller-template') { return $false }
    if (@((Get-BFValue $Record 'evidence_kinds' @()) | Where-Object { $_ -in @('accepted-result','successful-recovery') }).Count -eq 0) { return $false }
    if ([int]$Record.contradictions -gt 0) { return $false }
    # Three consistent confirmations total (the accepted source outcome counts
    # as the first) from at least two different task IDs.
    if ([int]$Record.confirmations -lt 3) { return $false }
    if (@($Record.confirmation_tasks).Count -lt 2) { return $false }
    if (-not (Test-BFMemorySameFingerprints $Record.fingerprints $Fingerprints)) { return $false }
    return $true
}

function Move-BFMemoryRecord {
    # Validated lifecycle transition with a mandatory reason. Nothing is ever
    # deleted: quarantined records return only through an explicit reinstated
    # event and the original quarantine reason stays in history.
    param([string]$ProjectPath, [string]$RecordId, [Parameter(Mandatory)][ValidateSet('shadow','promoted','quarantined','deprecated','superseded','reinstated')][string]$Transition, [string]$Reason, [string]$SupersededBy)
    $replay = Get-BFMemoryReplay $ProjectPath
    $record = Get-BFValue $replay.records $RecordId
    if ($null -eq $record) { throw (New-BFError 'BF_INVALID' 'Memory record does not exist.') }
    $fingerprints = Get-BFMemoryFingerprintsFromReplay $replay
    $plan = @([ordered]@{event_type=$Transition; record_id=$RecordId; knowledge_class=$null; risk_class=$null; scope=$null; observation=$null; action=$null; superseded_by=$SupersededBy; source_task_id=$null; source_attempt_id=$null; evidence_refs=@(); reason=(Get-BFMemoryBoundedText $Reason $script:BFMemoryMaxReasonChars)})
    return Add-BFMemoryEvents -ProjectPath $ProjectPath -ProjectId ([string]$replay.project_id) -Fingerprints $fingerprints -Plan $plan
}

function Get-BFMemoryFingerprintsFromReplay {
    param($Replay)
    if (@($Replay.events).Count -eq 0) { throw (New-BFError 'BF_INVALID' 'Memory fingerprints require at least one event.') }
    return (Get-BFValue $Replay.events[-1] 'fingerprints')
}

function Get-BFMemoryCandidatePlan {
    # Pure plan builder for one proposed item against a replayed record state:
    # dedup by canonical identity, contradiction scan, cross-task shadow
    # transition and promotion evaluation. The returned entries are validated
    # again against fresh state inside the single write session.
    param($Records, [string]$ProjectId, $Fingerprints, $Item, [string]$SourceTaskId, [string]$SourceAttemptId, $EvidenceRefs, [string]$Reason, [string]$EvidenceKind = '', [string]$Provenance = '', [string]$TaskKind = '', [string]$ErrorSignature = '')
    # Controller diagnostics are keyed by their typed error signature. The
    # fixed diagnostic text stays bounded, while failures of distinct criteria
    # remain separately retrievable without counting as confirmations.
    $recordId = if($EvidenceKind -eq 'confirmed-error' -and -not [string]::IsNullOrWhiteSpace($ErrorSignature)) { Get-BFMemoryRecordId $ProjectId $Item.scope $Item.action $Fingerprints $ErrorSignature } else { Get-BFMemoryRecordId $ProjectId $Item.scope $Item.action $Fingerprints }
    $existing = Get-BFValue $Records $recordId
    $plan = [System.Collections.Generic.List[object]]::new()
    if ($null -ne $existing) {
        # A failed verification is an error observation, not a confirmation of
        # useful knowledge. Keep its first diagnostic candidate at zero and do
        # not turn repeated failures into promotion evidence.
        if ($EvidenceKind -eq 'confirmed-error') { return @() }
        $existingTaskKind=[string](Get-BFValue $existing 'task_kind' '')
        if (-not [string]::IsNullOrWhiteSpace($TaskKind) -and -not [string]::IsNullOrWhiteSpace($existingTaskKind) -and $TaskKind -cne $existingTaskKind) { return @() }
        # Same canonical content is a confirmation, never a second record.
        $plan.Add([ordered]@{event_type='confirmed'; record_id=$recordId; knowledge_class=$null; risk_class=$null; scope=$null; observation=$null; action=$null; superseded_by=$null; source_task_id=$SourceTaskId; source_attempt_id=$SourceAttemptId; evidence_refs=@($EvidenceRefs); reason=$Reason; evidence_kind=$EvidenceKind; provenance=$Provenance; task_kind=$TaskKind; error_signature=$ErrorSignature})
        $newConfirmations = [int]$existing.confirmations + 1
        $tasks = @(@($existing.confirmation_tasks) + $SourceTaskId | Select-Object -Unique)
        $probe = Copy-BFMemoryRecord $existing
        $probe.confirmations = $newConfirmations
        $probe.confirmation_tasks = $tasks
        if ([string]$existing.state -ceq 'candidate' -and $SourceTaskId -cnotin @($existing.confirmation_tasks)) {
            $plan.Add([ordered]@{event_type='shadow'; record_id=$recordId; knowledge_class=$null; risk_class=$null; scope=$null; observation=$null; action=$null; superseded_by=$null; source_task_id=$null; source_attempt_id=$null; evidence_refs=@($EvidenceRefs); reason='first cross-task confirmation'; evidence_kind=$EvidenceKind; provenance=$Provenance; task_kind=$TaskKind; error_signature=$ErrorSignature})
            $probe.state = 'shadow'
        }
        if (Test-BFMemoryPromotionEligible $probe $Fingerprints) {
            $plan.Add([ordered]@{event_type='promoted'; record_id=$recordId; knowledge_class=$null; risk_class=$null; scope=$null; observation=$null; action=$null; superseded_by=$null; source_task_id=$null; source_attempt_id=$null; evidence_refs=@($EvidenceRefs); reason='promotion policy satisfied'; evidence_kind=$EvidenceKind; provenance=$Provenance; task_kind=$TaskKind; error_signature=$ErrorSignature})
        }
        return @($plan.ToArray())
    }
    $scopeKey = Get-BFMemoryScopeKey $ProjectId $Item.scope
    foreach ($candidateId in (@($Records.Keys) | Sort-Object { $_ })) {
        $other = $Records[$candidateId]
        if ([string]$other.state -cnotin $script:BFMemoryActiveStates) { continue }
        if ((Get-BFMemoryScopeKey $ProjectId $other.scope) -cne $scopeKey) { continue }
        if ([string](Get-BFValue $other.action 'text') -cne [string]$Item.action.text) { continue }
        if ([string](Get-BFValue $other.action 'type') -ceq [string]$Item.action.type) { continue }
        $plan.Add([ordered]@{event_type='contradicted'; record_id=$candidateId; knowledge_class=$null; risk_class=$null; scope=$null; observation=$null; action=$null; superseded_by=$null; source_task_id=$SourceTaskId; source_attempt_id=$SourceAttemptId; evidence_refs=@($EvidenceRefs); reason='contradicting confirmed evidence'; evidence_kind=$EvidenceKind; provenance=$Provenance; task_kind=$TaskKind; error_signature=$ErrorSignature})
        if ([string]$other.state -ceq 'accepted') {
            $plan.Add([ordered]@{event_type='superseded'; record_id=$candidateId; knowledge_class=$null; risk_class=$null; scope=$null; observation=$null; action=$null; superseded_by=$recordId; source_task_id=$null; source_attempt_id=$null; evidence_refs=@($EvidenceRefs); reason='superseded by contradicting candidate'; evidence_kind=$EvidenceKind; provenance=$Provenance; task_kind=$TaskKind; error_signature=$ErrorSignature})
        } else {
            $plan.Add([ordered]@{event_type='quarantined'; record_id=$candidateId; knowledge_class=$null; risk_class=$null; scope=$null; observation=$null; action=$null; superseded_by=$null; source_task_id=$null; source_attempt_id=$null; evidence_refs=@($EvidenceRefs); reason='quarantined by contradicting candidate'; evidence_kind=$EvidenceKind; provenance=$Provenance; task_kind=$TaskKind; error_signature=$ErrorSignature})
        }
    }
    $plan.Add([ordered]@{event_type='candidate'; record_id=$recordId; knowledge_class=$Item.knowledge_class; risk_class=$Item.risk_class; scope=$Item.scope; observation=$Item.observation; action=$Item.action; superseded_by=$null; source_task_id=$SourceTaskId; source_attempt_id=$SourceAttemptId; evidence_refs=@($EvidenceRefs); reason=$Reason; evidence_kind=$EvidenceKind; provenance=$Provenance; task_kind=$TaskKind; error_signature=$ErrorSignature})
    return @($plan)
}

function Add-BFMemoryFromAttempt {
    # Extractor over controller-owned terminal results. Idempotent per attempt
    # result hash; implement worker prose is explicitly rejected because an
    # implementation PASS does not prove every worker-authored statement.
    # Controller-generated diagnostics from a completed failed verification are
    # retained as zero-confirmation advice and cannot promote.
    param($State, $Result, [string]$ResultHash)
    if (-not (Test-BFSelfLearningMemoryEnabled $State)) { return $null }
    try {
        $projectId = [string]$State.project_path
        $evidence = @([ordered]@{kind='attempt-result'; task_id=[string]$Result.task_id; attempt_id=[string]$Result.attempt_id; sha256=$ResultHash})
        $replay = Get-BFMemoryReplay $projectId
        foreach ($event in @($replay.events)) {
            if ([string]$event.source_attempt_id -cne [string]$Result.attempt_id) { continue }
            foreach ($ref in @($event.evidence_refs)) {
                if ([string](Get-BFValue $ref 'sha256') -ceq $ResultHash) { return $null }
            }
        }
        $fingerprints = Get-BFMemoryFingerprints $State
        $items = [System.Collections.Generic.List[object]]::new()
        $rejections = [System.Collections.Generic.List[string]]::new()
        $reason = 'confirmed-error'
        $taskKind=Get-BFMemoryTaskKind $State
        if ([string]$Result.outcome -ceq 'PASS' -and [string]$Result.stage -ceq 'implement') {
            # The stage result is not a task acceptance receipt. Do not copy or
            # classify any observation supplied by the worker; record only an
            # auditable marker explaining why it was excluded from learning.
            $proposal = Get-BFValue $Result 'proposal'
            foreach ($observation in @(Get-BFValue $proposal 'observations' @())) {
                # Preserve a more specific validator reason for malformed or
                # authority-class payloads; valid worker prose still receives
                # the explicit untrusted marker. Neither path creates a record.
                $checked=ConvertTo-BFMemoryItem $observation 'implement'
                if (-not $checked.ok) { $rejections.Add([string]$checked.reason) }
                else { $rejections.Add('worker-prose-untrusted') }
            }
        } elseif ([string]$Result.outcome -ceq 'FAIL' -and [string]$Result.side_effects -ceq 'none' -and [string]$Result.stage -ceq 'verify') {
            $proposal = Get-BFValue $Result 'proposal'
            $criterionId = Get-BFValue $proposal 'criterion_id'
            $criterionKind = Get-BFValue $proposal 'kind'
            $typedFailure=$null -ne $criterionId -and -not [string]::IsNullOrWhiteSpace([string]$criterionId) -and -not [string]::IsNullOrWhiteSpace([string]$criterionKind)
            $observationText = $null; $actionText = $null
            if ($typedFailure) {
                # The marker is controller-created, but criterion text still
                # originates in the request. Keep the diagnostic itself a
                # closed template; identity and kind are carried only by the
                # hashed error signature.
                $observationText = $script:BFMemoryTemplateObservationConfirmedFailure
                $actionText = $script:BFMemoryTemplateActionConfirmedFailure
                if ([string]::IsNullOrWhiteSpace($observationText) -or [string]::IsNullOrWhiteSpace($actionText)) { $rejections.Add('invalid-shape') }
                elseif (Test-BFMemorySecretLike $observationText -or Test-BFMemorySecretLike $actionText) { $rejections.Add('secret-like-content') }
                else {
                    $items.Add([ordered]@{scope=(ConvertTo-BFMemoryScope ([string]$Result.stage) @()); observation=$observationText; action=[ordered]@{type='avoid'; text=$actionText}; knowledge_class='diagnostic'; risk_class='low'})
                }
            }
        }
        if ($items.Count -eq 0 -and $rejections.Count -eq 0) { return $null }
        # One validated session for the whole extraction: rejection markers plus
        # every candidate plan (dedup/contradiction/shadow/promotion derived
        # against the same replayed state).
        $plan = @()
        $shown = 0
        foreach ($reasonCode in $rejections) {
            if ($shown -ge $script:BFMemoryMaxRejections) { break }
            $plan += [ordered]@{event_type='rejected'; record_id=$null; knowledge_class=$null; risk_class=$null; scope=$null; observation=$null; action=$null; superseded_by=$null; source_task_id=[string]$Result.task_id; source_attempt_id=[string]$Result.attempt_id; evidence_refs=$evidence; reason=$reasonCode}
            $shown++
        }
        foreach ($item in $items) {
            $evidenceKind=if($item.knowledge_class -ceq 'diagnostic'){'confirmed-error'}else{$null}
            $provenance=if($item.knowledge_class -ceq 'diagnostic'){'controller-error'}else{$null}
            $errorSignature=if($evidenceKind -eq 'confirmed-error'){Get-BFMemoryErrorSignature $Result}else{''}
            $plan += Get-BFMemoryCandidatePlan -Records $replay.records -ProjectId $projectId -Fingerprints $fingerprints -Item $item -SourceTaskId ([string]$Result.task_id) -SourceAttemptId ([string]$Result.attempt_id) -EvidenceRefs $evidence -Reason $reason -EvidenceKind $evidenceKind -Provenance $provenance -TaskKind $taskKind -ErrorSignature $errorSignature
        }
        if (@($plan).Count -eq 0) { return $null }
        [void](Add-BFMemoryEvents -ProjectPath $projectId -ProjectId $projectId -Fingerprints $fingerprints -Plan $plan)
        return $true
    } catch { return $null }
}

function Add-BFMemoryFromAcceptance {
    # Acceptance is the only source that can seed a promotable procedural
    # record. The item is a fixed template whose predicate is checked from the
    # controller receipt; worker result prose is never consulted.
    param($State, $Receipt, [string]$ReceiptHash)
    if (-not (Test-BFSelfLearningMemoryEnabled $State)) { return $null }
    try {
        if ([string]::IsNullOrWhiteSpace($ReceiptHash)) { $ReceiptHash=Get-BFHash $Receipt }
        if ($ReceiptHash -notmatch '^[0-9a-f]{64}$') { return $null }
        $template=Get-BFMemoryAcceptedTemplateItem $State $Receipt
        if ($null -eq $template) { return $null }
        $projectId=[string]$State.project_path
        $replay=Get-BFMemoryReplay $projectId
        foreach ($event in @($replay.events)) {
            foreach ($ref in @($event.evidence_refs)) {
                if ([string](Get-BFValue $ref 'sha256' '') -ceq $ReceiptHash -and [string](Get-BFValue $ref 'kind' '') -ceq 'acceptance-receipt') { return $null }
            }
        }
        $fingerprints=Get-BFMemoryFingerprints $State
        $evidence=@([ordered]@{kind='acceptance-receipt';task_id=[string]$State.task_id;sha256=$ReceiptHash})
        $plan=Get-BFMemoryCandidatePlan -Records $replay.records -ProjectId $projectId -Fingerprints $fingerprints -Item $template.item -SourceTaskId ([string]$State.task_id) -SourceAttemptId ('acceptance:'+ $ReceiptHash) -EvidenceRefs $evidence -Reason ('controller-template:' + [string]$template.template_id) -EvidenceKind $template.evidence_kind -Provenance $template.provenance -TaskKind $template.task_kind -ErrorSignature $template.error_signature
        if (@($plan).Count -eq 0) { return $null }
        [void](Add-BFMemoryEvents -ProjectPath $projectId -ProjectId $projectId -Fingerprints $fingerprints -Plan $plan)
        return $true
    } catch { return $null }
}

function Add-BFMemoryFromRecovery {
    # A successful source-only recovery control-read is a second closed
    # controller template. It can teach the retry procedure, while native,
    # runtime and external-effect recoveries remain outside auto-promotion.
    param($State, $Resolution, $Manifest, [string]$RecoveryHash)
    if (-not (Test-BFSelfLearningMemoryEnabled $State)) { return $null }
    try {
        if ([string]::IsNullOrWhiteSpace($RecoveryHash)) { $RecoveryHash=Get-BFHash $Resolution }
        if ($RecoveryHash -notmatch '^[0-9a-f]{64}$') { return $null }
        $template=Get-BFMemorySuccessfulRecoveryTemplateItem $State $Resolution $Manifest
        if ($null -eq $template) { return $null }
        $projectId=[string]$State.project_path
        $replay=Get-BFMemoryReplay $projectId
        foreach ($event in @($replay.events)) {
            foreach ($ref in @($event.evidence_refs)) {
                if ([string](Get-BFValue $ref 'sha256' '') -ceq $RecoveryHash -and [string](Get-BFValue $ref 'kind' '') -ceq 'recovery-receipt') { return $null }
            }
        }
        $fingerprints=Get-BFMemoryFingerprints $State
        $evidence=@([ordered]@{kind='recovery-receipt';task_id=[string]$State.task_id;attempt_id=[string](Get-BFValue $Resolution 'attempt_id' '');sha256=$RecoveryHash})
        $attemptId=[string](Get-BFValue $Resolution 'attempt_id' '')
        $sourceAttemptId=if([string]::IsNullOrWhiteSpace($attemptId)){('recovery:'+ $RecoveryHash)}else{('recovery:'+ $attemptId)}
        $plan=Get-BFMemoryCandidatePlan -Records $replay.records -ProjectId $projectId -Fingerprints $fingerprints -Item $template.item -SourceTaskId ([string]$State.task_id) -SourceAttemptId $sourceAttemptId -EvidenceRefs $evidence -Reason ('controller-template:' + [string]$template.template_id) -EvidenceKind $template.evidence_kind -Provenance $template.provenance -TaskKind $template.task_kind -ErrorSignature $template.error_signature
        if (@($plan).Count -eq 0) { return $null }
        [void](Add-BFMemoryEvents -ProjectPath $projectId -ProjectId $projectId -Fingerprints $fingerprints -Plan $plan)
        return $true
    } catch { return $null }
}

function Get-BFMemoryBundleFromReplay {
    # Deterministic bounded selection for one stage. Accepted records lead,
    # then shadow/candidate recommendations; record_id is the stable
    # tie-breaker. Selection uses task kind, error signature and ancestor-aware
    # path overlap. The final limit is measured over the actually rendered
    # prompt, including its fixed boundary and excluded-record explanation.
    param($Replay, [string]$ProjectId, $Fingerprints, [string]$Stage, [string[]]$Paths, [string]$TaskKind = '', [string]$ErrorSignature = '', [switch]$RequireErrorSignature)
    if ($null -ne $Replay -and -not [string]::IsNullOrWhiteSpace([string](Get-BFValue $Replay 'project_id' '')) -and [string](Get-BFValue $Replay 'project_id' '') -cne (Assert-BFSafePath $ProjectId)) { throw (New-BFError 'BF_BLOCKED' 'Memory replay project does not match the requested project.') }
    $relevantStages = @($Stage)
    if ($script:BFMemoryStageRelevance.ContainsKey($Stage)) { $relevantStages = @($script:BFMemoryStageRelevance[$Stage]) }
    $hintPaths = @($Paths | ForEach-Object { (([string]$_).Replace('\','/').Trim('/')) })
    $hasHints = @($hintPaths).Count -gt 0 -and @($hintPaths | Where-Object { $_ -eq '' -or $_ -eq '.' }).Count -eq 0
    $candidates = @()
    foreach ($recordId in (@($Replay.records.Keys) | Sort-Object { $_ })) {
        $record = $Replay.records[$recordId]
        $state = [string]$record.state
        if ($state -cnotin $script:BFMemoryActiveStates) { continue }
        if (-not (Test-BFMemorySameFingerprints $record.fingerprints $Fingerprints)) {
            $candidates += [ordered]@{record=$record; rank=$null; selected_reason=$null; excluded_reason='fingerprint-mismatch'}
            continue
        }
        $recordStage = [string](Get-BFValue $record.scope 'stage')
        if (-not [string]::IsNullOrWhiteSpace($recordStage) -and $recordStage -cnotin $relevantStages) {
            $candidates += [ordered]@{record=$record; rank=99; selected_reason=$null; excluded_reason='stage-scope-mismatch'}
            continue
        }
        $recordTaskKind=[string](Get-BFValue $record 'task_kind' '')
        if (-not [string]::IsNullOrWhiteSpace($TaskKind) -and -not [string]::IsNullOrWhiteSpace($recordTaskKind) -and $recordTaskKind -cne $TaskKind) {
            $candidates += [ordered]@{record=$record; rank=99; selected_reason=$null; excluded_reason='task-kind-mismatch'}
            continue
        }
        $recordErrors=@(Get-BFValue $record 'error_signatures' @())
        if ($RequireErrorSignature -and [string]$record.knowledge_class -ceq 'diagnostic' -and [string]::IsNullOrWhiteSpace($ErrorSignature)) {
            $candidates += [ordered]@{record=$record; rank=99; selected_reason=$null; excluded_reason='error-signature-unavailable'}
            continue
        }
        if (-not [string]::IsNullOrWhiteSpace($ErrorSignature) -and $recordErrors.Count -gt 0 -and $ErrorSignature -notin $recordErrors) {
            $candidates += [ordered]@{record=$record; rank=99; selected_reason=$null; excluded_reason='error-signature-mismatch'}
            continue
        }
        $recordPaths=@(Get-BFValue $record.scope 'paths' @())
        if ($hasHints -and $recordPaths.Count -gt 0) {
            $overlap=$false
            foreach ($recordPath in $recordPaths) { foreach ($hintPath in $hintPaths) { if (Test-BFMemoryPathOverlap ([string]$recordPath) ([string]$hintPath)) { $overlap=$true; break } }; if ($overlap) { break } }
            if (-not $overlap) {
                $candidates += [ordered]@{record=$record; rank=99; selected_reason=$null; excluded_reason='path-scope-mismatch'}
                continue
            }
        }
        $rank = switch ($state) { 'accepted' {0} 'shadow' {1} default {2} }
        if (-not [string]::IsNullOrWhiteSpace($TaskKind) -and $recordTaskKind -ceq $TaskKind) { $rank -= 1 }
        if (-not [string]::IsNullOrWhiteSpace($ErrorSignature) -and $ErrorSignature -in $recordErrors) { $rank -= 1 }
        $selectedReason = switch ($state) { 'accepted' {'accepted-knowledge'} 'shadow' {'shadow-recommendation'} default {'candidate-recommendation'} }
        $candidates += [ordered]@{record=$record; rank=$rank; selected_reason=$selectedReason; excluded_reason=$null}
    }
    $ordered = @($candidates | Sort-Object -Property @{Expression={$_.rank};Descending=$false}, @{Expression={[string]$_.record.record_id};Descending=$false})
    $selected = @(); $excluded = [System.Collections.Generic.List[object]]::new()
    foreach ($entry in $ordered) {
        if ($entry.excluded_reason) {
            if ($excluded.Count -lt $script:BFMemoryMaxExcluded) { $excluded.Add([ordered]@{record_id=[string]$entry.record.record_id; reason=$entry.excluded_reason}) }
            continue
        }
        if (@($selected).Count -ge $script:BFMemoryMaxRecords) {
            if ($excluded.Count -lt $script:BFMemoryMaxExcluded) { $excluded.Add([ordered]@{record_id=[string]$entry.record.record_id; reason='limit-records'}) }
            continue
        }
        $selected += [ordered]@{record=$entry.record; selected_reason=$entry.selected_reason}
    }
    function Convert-BFMemorySelectedSummary($Entry) {
        $record=$Entry.record
        return [ordered]@{record_id=[string]$record.record_id; state=[string]$record.state; knowledge_class=[string]$record.knowledge_class; risk_class=[string]$record.risk_class
            scope=$record.scope; observation=$record.observation; action=$record.action
            confirmations=[int]$record.confirmations; contradictions=[int]$record.contradictions
            selected_reason=[string]$Entry.selected_reason; task_kind=[string](Get-BFValue $record 'task_kind' ''); error_signatures=@(Get-BFValue $record 'error_signatures' @()); evidence_ref=(Get-BFMemoryEvidenceRef (Get-BFValue $record 'evidence_ref'))}
    }
    $summaries=@($selected | ForEach-Object { Convert-BFMemorySelectedSummary $_ })
    while ($true) {
        $promptMemory=[ordered]@{available=$true;records=@($summaries);excluded=@($excluded.ToArray())}
        $rendered=Format-BFMemoryBundlePrompt $promptMemory
        $renderedBytes=[Text.Encoding]::UTF8.GetByteCount([string]$rendered)
        if ($renderedBytes -le $script:BFMemoryMaxBundleChars -or @($summaries).Count -eq 0) { break }
        $last=$selected[-1]
        if ($excluded.Count -lt $script:BFMemoryMaxExcluded) { $excluded.Add([ordered]@{record_id=[string]$last.record.record_id; reason='limit-size'}) }
        $selected=@($selected | Select-Object -First (@($selected).Count-1))
        $summaries=@($selected | ForEach-Object { Convert-BFMemorySelectedSummary $_ })
    }
    $content = [ordered]@{schema_version=1; stage=$Stage; project_id=$ProjectId; fingerprints=$Fingerprints; records=@($summaries); excluded=@($excluded.ToArray())}
    $hash = Get-BFHash $content
    $content.bundle_id = $hash
    $content.bundle_sha256 = $hash
    return $content
}

function Assert-BFMemoryBundleShape {
    param($Bundle)
    if ($null -eq $Bundle -or ($Bundle -isnot [System.Collections.IDictionary] -and $Bundle -isnot [pscustomobject])) { throw (New-BFError 'BF_BLOCKED' 'Memory bundle must be an object.') }
    foreach ($field in @('schema_version','stage','project_id','fingerprints','records','excluded','bundle_id','bundle_sha256')) {
        if (-not (Test-BFObjectProperty $Bundle $field)) { throw (New-BFError 'BF_BLOCKED' ("Memory bundle field {0} is missing." -f $field)) }
    }
    $keys = @()
    if ($Bundle -is [System.Collections.IDictionary]) { $keys = @($Bundle.Keys) } else { $keys = @($Bundle.PSObject.Properties.Name) }
    foreach ($key in $keys) { if ([string]$key -cnotin @('schema_version','stage','project_id','fingerprints','records','excluded','bundle_id','bundle_sha256')) { throw (New-BFError 'BF_BLOCKED' ("Unknown memory bundle field {0}." -f $key)) } }
    if ($Bundle.schema_version -cne 1) { throw (New-BFError 'BF_BLOCKED' 'Unsupported memory bundle schema version.') }
    if ($Bundle.records -isnot [array] -or $Bundle.excluded -isnot [array]) { throw (New-BFError 'BF_BLOCKED' 'Memory bundle records and excluded must be arrays.') }
    if ([string]$Bundle.bundle_id -cnotmatch '^[0-9a-f]{64}$' -or [string]$Bundle.bundle_sha256 -cnotmatch '^[0-9a-f]{64}$') { throw (New-BFError 'BF_BLOCKED' 'Memory bundle identity hashes are malformed.') }
    if ((Get-BFHash ([ordered]@{schema_version=$Bundle.schema_version; stage=$Bundle.stage; project_id=$Bundle.project_id; fingerprints=$Bundle.fingerprints; records=@($Bundle.records); excluded=@($Bundle.excluded)})) -cne [string]$Bundle.bundle_id) { throw (New-BFError 'BF_BLOCKED' 'Memory bundle identity hash mismatch.') }
    return $Bundle
}

function Add-BFMemoryAttemptBinding {
    # Write-path binding for a new attempt: quarantines stale-fingerprint
    # records (audited events), rebuilds and persists the index, then returns
    # the attempt-bound bundle. Any memory failure degrades to an explicit
    # disabled envelope and never breaks dispatch.
    param($State, [string]$Stage, [AllowNull()][object]$PendingFailureResult = $null, [AllowNull()][string]$PendingFailureResultHash = '')
    if (-not (Test-BFSelfLearningMemoryEnabled $State)) {
        return [ordered]@{schema_version=1; available=$false; bundle_id=$null; bundle_sha256=$null; records=@(); excluded=@(); disabled_reason='disabled-by-project-policy'}
    }
    try {
        $projectId = [string]$State.project_path
        $directory = Get-BFMemoryDirectory $projectId
        $fingerprints = Get-BFMemoryFingerprints $State
        $replay = Get-BFMemoryReplay $projectId
        $staleIds = @(@($replay.records.Keys) | Where-Object { $r = $replay.records[$_]; [string]$r.state -cne '' -and $script:BFMemoryActiveStates -ccontains [string]$r.state -and -not (Test-BFMemorySameFingerprints $r.fingerprints $fingerprints) } | Sort-Object { $_ })
        if (@($staleIds).Count -gt 0) {
            $plan = @()
            foreach ($recordId in $staleIds) {
                $plan += [ordered]@{event_type='quarantined'; record_id=$recordId; knowledge_class=$null; risk_class=$null; scope=$null; observation=$null; action=$null; superseded_by=$null; source_task_id=$null; source_attempt_id=$null; evidence_refs=@([ordered]@{kind='fingerprint-check'; policy=[string]$fingerprints.policy; controller=[string]$fingerprints.controller; version=[string]$fingerprints.version}); reason='fingerprint-mismatch'}
            }
            [void](Add-BFMemoryEvents -ProjectPath $projectId -ProjectId $projectId -Fingerprints $fingerprints -Plan $plan)
            $replay = Get-BFMemoryReplay $projectId
        }
        $paths = @(Get-BFValue (Get-BFValue $State 'request') 'source_paths' @('.'))
        $taskKind=Get-BFMemoryTaskKind $State
        $errorSignature=Get-BFMemoryErrorSignatureForState $State $Stage $PendingFailureResult $PendingFailureResultHash
        $bundle = Get-BFMemoryBundleFromReplay $replay $projectId $fingerprints $Stage $paths $taskKind $errorSignature -RequireErrorSignature:($Stage -ceq 'diagnose')
        [void](Assert-BFMemoryBundleShape $bundle)
        # Deterministic index restoration on the write path: a deleted or stale
        # derived index is rebuilt from the authoritative events.
        $indexPath = Join-Path $directory 'index.json'
        $needsPersist = $true
        if (Test-Path -LiteralPath $indexPath -PathType Leaf) {
            try { $needsPersist = -not (Test-BFMemoryIndexFresh (Read-BFJson $indexPath) $replay) } catch { $needsPersist = $true }
        }
        if ($needsPersist -and @($replay.events).Count -gt 0) {
            $index = New-BFMemoryIndexObject -ProjectId ([string]$replay.project_id) -Events $replay.events -Records $replay.records -Torn $replay.torn -EventFilesCount $replay.event_files_count -LastEventSeq $replay.last_event_seq
            Write-BFJson -Path $indexPath -Value $index -Replace
        }
        return [ordered]@{schema_version=1; available=$true; bundle_id=$bundle.bundle_id; bundle_sha256=$bundle.bundle_sha256; records=@($bundle.records); excluded=@($bundle.excluded); disabled_reason=$null}
    } catch {
        return [ordered]@{schema_version=1; available=$false; bundle_id=$null; bundle_sha256=$null; records=@(); excluded=@(); disabled_reason=(Get-BFMemoryBoundedText $_.Exception.Message 256)}
    }
}

function Get-BFMemoryProjection {
    # Read-only Resume Capsule delta over the authoritative event log. Never
    # writes: a stale index is rebuilt in memory and reported as replayed;
    # damaged stores disable memory with an explicit blocker.
    param($State, $Next, [AllowNull()][object]$PendingFailureResult = $null, [AllowNull()][string]$PendingFailureResultHash = '')
    if (-not (Test-BFSelfLearningMemoryEnabled $State)) {
        return [ordered]@{schema_version=1; available=$false; blocker='disabled-by-project-policy'
            index=[ordered]@{events_count=0; event_files_count=0; last_event_seq=0; last_event_id=$null; replayed=$false}
            working_set=@(); records=@(); bundle=$null}
    }
    try {
        $projectId = [string]$State.project_path
        $stage = [string](Get-BFValue $Next 'stage' '')
        if ([string]::IsNullOrWhiteSpace($stage)) { $stage = [string]$State.stage }
        $replay = Get-BFMemoryReadSource $projectId
        $paths = @(Get-BFValue (Get-BFValue $State 'request') 'source_paths' @('.'))
        $taskKind=Get-BFMemoryTaskKind $State
        $errorSignature=Get-BFMemoryErrorSignatureForState $State $stage $PendingFailureResult $PendingFailureResultHash
        $bundle = Get-BFMemoryBundleFromReplay $replay $projectId (Get-BFMemoryFingerprints $State) $stage $paths $taskKind $errorSignature -RequireErrorSignature:($stage -ceq 'diagnose')
        $replayed = -not [bool](Get-BFValue $replay 'indexed' $false)
        $working = [System.Collections.Generic.List[string]]::new()
        foreach ($record in @($bundle.records)) {
            foreach ($path in @(Get-BFValue $record.scope 'paths' @())) {
                if (-not $working.Contains([string]$path) -and $working.Count -lt $script:BFMemoryMaxWorkingSet) { $working.Add([string]$path) }
            }
        }
        $workingSet = @($working.ToArray() | Sort-Object { $_ })
        $summaries = @()
        foreach ($record in @($bundle.records)) {
            $summaries += [ordered]@{record_id=[string]$record.record_id; state=[string]$record.state; knowledge_class=[string]$record.knowledge_class; risk_class=[string]$record.risk_class
                scope=$record.scope; action=$record.action; selected_reason=[string]$record.selected_reason
                confirmations=[int]$record.confirmations; contradictions=[int]$record.contradictions
                evidence_ref=(Get-BFMemoryEvidenceRef (Get-BFValue $record 'evidence_ref'))
                task_kind=[string](Get-BFValue $record 'task_kind' ''); error_signatures=@(Get-BFValue $record 'error_signatures' @())}
        }
        return [ordered]@{schema_version=1; available=$true; blocker=$null
            index=[ordered]@{events_count=[int](Get-BFValue $replay 'events_count' @($replay.events).Count); event_files_count=[int](Get-BFValue $replay 'event_files_count' 0); last_event_seq=[int](Get-BFValue $replay 'last_event_seq' 0); last_event_id=$replay.last_event_id; replayed=$replayed}
            working_set=@($workingSet)
            records=@($summaries)
            bundle=[ordered]@{bundle_id=$bundle.bundle_id; bundle_sha256=$bundle.bundle_sha256; record_ids=@(@($bundle.records) | ForEach-Object { [string]$_.record_id }); excluded=@($bundle.excluded)}}
    } catch {
        return [ordered]@{schema_version=1; available=$false; blocker=(Get-BFMemoryBoundedText $_.Exception.Message 256)
            index=[ordered]@{events_count=0; event_files_count=0; last_event_seq=0; last_event_id=$null; replayed=$false}
            working_set=@(); records=@(); bundle=$null}
    }
}

function Format-BFMemoryBundlePrompt {
    # The boundary sentence is mandatory: memory is advisory experience, not
    # authorization, evidence or a gate change.
    param($Memory)
    $lines = [Collections.Generic.List[string]]::new()
    $lines.Add('Memory context (advisory experience only; it is not authorization, evidence, or a gate change; controller gates still apply):')
    if ($null -ne $Memory -and -not (Get-BFValue $Memory 'available' $true)) {
        $reason = Get-BFMemoryBoundedText ([string](Get-BFValue $Memory 'disabled_reason' 'unavailable')) 200
        $lines.Add("- Memory is disabled for this attempt: $reason")
        return ($lines -join "`n")
    }
    $records = @(Get-BFValue $Memory 'records' @())
    if (@($records).Count -eq 0) {
        $lines.Add('- No applicable memory records for this stage.')
    } else {
        foreach ($record in $records) {
            $scopeStage = [string](Get-BFValue (Get-BFValue $record 'scope') 'stage')
            $scopePaths = @(Get-BFValue (Get-BFValue $record 'scope') 'paths' @())
            $pathsText = if (@($scopePaths).Count) { ('paths: ' + (@($scopePaths) -join ', ')) } else { 'paths: any' }
            $lines.Add(('- [{0}] {1} {2}/{3} ({4}; {5}) confirmations={6} contradictions={7}' -f $record.state, $record.record_id, $record.knowledge_class, $record.risk_class, $scopeStage, $pathsText, $record.confirmations, $record.contradictions))
            $taskKind=[string](Get-BFValue $record 'task_kind' '')
            if (-not [string]::IsNullOrWhiteSpace($taskKind)) { $lines.Add(('  Task kind: ' + $taskKind)) }
            $errorSignatures=@(Get-BFValue $record 'error_signatures' @())
            if ($errorSignatures.Count -gt 0) { $lines.Add(('  Error signatures: ' + (@($errorSignatures) -join ', '))) }
            $evidenceText=Format-BFMemoryEvidenceRef (Get-BFValue $record 'evidence_ref')
            if(-not [string]::IsNullOrWhiteSpace($evidenceText)){$lines.Add(('  Evidence ref: ' + $evidenceText))}
            if (-not [string]::IsNullOrWhiteSpace([string](Get-BFValue $record 'observation'))) { $lines.Add(('  Observation: ' + [string]$record.observation)) }
            $actionText = Get-BFValue $record 'action'
            if ($null -ne $actionText) {
                $prefix = if ([string]$actionText.type -ceq 'avoid') { 'Avoid action' } else { 'Recommended action' }
                $lines.Add(('  ' + $prefix + ': ' + [string]$actionText.text))
            }
            $lines.Add(('  Why selected: ' + [string](Get-BFValue $record 'selected_reason')))
        }
    }
    $excluded = @(Get-BFValue $Memory 'excluded' @())
    if (@($excluded).Count -gt 0) {
        $sample = @($excluded | Select-Object -First 8) | ForEach-Object { ('{0} ({1})' -f $_.record_id, $_.reason) }
        $lines.Add(('Excluded memory: ' + (@($sample) -join ', ')))
    }
    return ($lines -join "`n")
}
