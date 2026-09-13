#Requires -Version 7.0
Set-StrictMode -Version Latest

# The compatibility provider is deliberately a narrow stage host.  It receives
# a controller-owned view and returns observations; it never materializes a
# controller journal or decides a task transition.
$script:BFNativeProviderContract = 'bsl-flow.native-provider.windows-ps.v1'
$script:BFNativeProviderStages = @('inspect','spec','spec_review','implement','code_review','verify','diagnose')
$script:BFNativeProviderArtifactKinds = @('raw','process','model','verification','review','budget','failure')
$script:BFNativeProviderPriorArtifacts = @{}

# Task.Stages is shared with the controller but the native provider deliberately
# does not import Task.Engine (which owns the journal commands). Keep this pure
# classification fold available to the standalone provider process.
function Set-BFClassification {
    param($State, $Proposal)
    Assert-BFFields $Proposal @('complexity','risk','impact_flags','rationale') @() 'classification'
    if ($Proposal.complexity -notin @('S','M','L') -or $Proposal.risk -notin @('low','medium','high')) { throw 'BF_INVALID: invalid inspected classification.' }
    Assert-BFImpactFlags $Proposal.impact_flags
    Assert-BFText $Proposal.rationale 'classification.rationale'
    $sizes=@('S','M','L')
    $risks=@('low','medium','high')
    $flags=@((@($State.classification.impact_flags)+@($Proposal.impact_flags)) | Select-Object -Unique)
    $risk=$risks[[math]::Max([Array]::IndexOf($risks,$State.classification.risk),[Array]::IndexOf($risks,$Proposal.risk))]
    if (@($flags | Where-Object { $_ -in @('permissions','data_migration','data_deletion') }).Count) { $risk='high' }
    $State.classification=[ordered]@{complexity=$sizes[[math]::Max([Array]::IndexOf($sizes,$State.classification.complexity),[Array]::IndexOf($sizes,$Proposal.complexity))];risk=$risk;impact_flags=$flags;rationale=$Proposal.rationale}
}

function Assert-BFProviderSha256 {
    param([string]$Value,[string]$Name)
    if($Value -isnot [string] -or $Value -cnotmatch '^[0-9a-f]{64}$'){
        throw "BF_INVALID: $Name must be a lower-case SHA-256."
    }
}

function Test-BFProviderNestedPath {
    param([string]$Candidate,[string]$Root)
    $candidate=(Assert-BFSafePath $Candidate).TrimEnd('\','/')
    $root=(Assert-BFSafePath $Root).TrimEnd('\','/')
    return $candidate -eq $root -or $candidate.StartsWith($root+'\',[StringComparison]::OrdinalIgnoreCase) -or $candidate.StartsWith($root+'/',[StringComparison]::OrdinalIgnoreCase)
}

function Assert-BFProviderDisjointPath {
    param([string]$Left,[string]$Right,[string]$Name)
    $left=(Assert-BFSafePath $Left).TrimEnd('\','/')
    $right=(Assert-BFSafePath $Right).TrimEnd('\','/')
    if($left -eq $right -or $left.StartsWith($right+'\',[StringComparison]::OrdinalIgnoreCase) -or $right.StartsWith($left+'\',[StringComparison]::OrdinalIgnoreCase)){
        throw "BF_INVALID: $Name paths overlap."
    }
}

function Assert-BFProviderGitStore {
    param($ProviderInput)
    $project=Assert-BFSafePath $ProviderInput.state_view.project_path
    $declared=Assert-BFSafePath $ProviderInput.canonical_store_root
    $common=$null
    $gitCommand=Get-Command Invoke-BFGit -CommandType Function -ErrorAction SilentlyContinue
    if($null -ne $gitCommand){$common=[string](Invoke-BFGit $project @('rev-parse','--git-common-dir'))}
    else {
        $commonText=& git -c core.hooksPath=NUL -c core.fsmonitor=false -C $project rev-parse --git-common-dir 2>$null
        if($LASTEXITCODE -ne 0){throw 'BF_BLOCKED: unable to verify the Git common directory for the provider store.'}
        $common=[string]$commonText.Trim()
    }
    if([IO.Path]::IsPathRooted($common)){$commonPath=Assert-BFSafePath $common}else{$commonPath=Assert-BFSafePath (Join-Path $project $common)}
    $actual=Assert-BFSafePath (Join-Path $commonPath 'bsl-flow')
    if($actual -cne $declared){throw 'BF_BLOCKED: declared canonical store is not the verified Git common-dir store.'}
    return $actual
}

function Get-BFProviderContextArtifact {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory = $true)][string]$ContextRoot,
        [Parameter(Mandatory = $true)][string]$RelativePath,
        [switch]$AsText,
        [switch]$AsJson
    )
    if($AsText -and $AsJson){throw 'BF_INVALID: context artifact output modes are mutually exclusive.'}
    Assert-BFRelativePath $RelativePath
    if($RelativePath -match '(^|[\/]).(git|bsl-flow)([\/]|$)'){throw 'BF_BLOCKED: provider context may not expose controller paths.'}
    $root=Assert-BFSafePath $ContextRoot
    $full=Assert-BFSafePath (Join-Path $root $RelativePath)
    if(-not $full.StartsWith($root.TrimEnd('\','/')+'\',[StringComparison]::OrdinalIgnoreCase)){
        throw 'BF_INVALID: context artifact escaped context_root.'
    }
    $key=$RelativePath.Replace('\','/')
    if(-not $script:BFNativeProviderPriorArtifacts.ContainsKey($key)){
        throw "BF_BLOCKED: context artifact was not declared by core: $key"
    }
    $declared=$script:BFNativeProviderPriorArtifacts[$key]
    if(-not (Test-Path -LiteralPath $full -PathType Leaf)){throw "BF_BLOCKED: declared context artifact is missing: $key"}
    $info=Get-Item -LiteralPath $full -Force
    if([int64]$info.Length -ne [int64]$declared.size_bytes){throw "BF_BLOCKED: context artifact size changed: $key"}
    if((Get-BFFileHash $full) -cne $declared.sha256){throw "BF_BLOCKED: context artifact bytes changed: $key"}
    if($AsText){return [IO.File]::ReadAllText($full)}
    if($AsJson){return Read-BFJson $full}
    return $full
}

function ConvertTo-BFProviderContextRelativePath {
    param(
        [string]$Path,
        [string]$AttemptId,
        [string]$ExpectedSha256='',
        [string]$ContextRoot=''
    )
    if($Path -isnot [string] -or [string]::IsNullOrWhiteSpace($Path)){throw 'BF_INVALID: artifact path is required.'}
    Assert-BFUuid $AttemptId
    $relative=$null
    if(-not [IO.Path]::IsPathRooted($Path)){
        Assert-BFRelativePath $Path
        $relative=$Path.Replace('\','/')
    }else{
        # An absolute path is accepted only when it is already inside the
        # immutable context projection. Searching for an `attempts/<id>`
        # substring made a path from an unrelated checkout look bound.
        if([string]::IsNullOrWhiteSpace($ContextRoot)){throw 'BF_BLOCKED: absolute provider artifact requires an explicit context root.'}
        $root=(Assert-BFSafePath $ContextRoot).TrimEnd('\','/')
        $full=Assert-BFSafePath $Path
        if(-not ($full -eq $root -or $full.StartsWith($root+'\',[StringComparison]::OrdinalIgnoreCase) -or $full.StartsWith($root+'/',[StringComparison]::OrdinalIgnoreCase))){throw 'BF_BLOCKED: absolute provider artifact escaped the declared context root.'}
        $relative=$full.Substring($root.Length).TrimStart('\','/') -replace '\\','/'
        Assert-BFRelativePath $relative
    }
    $expectedPrefix='attempts/'+$AttemptId+'/'
    if(-not $relative.StartsWith($expectedPrefix,[StringComparison]::OrdinalIgnoreCase)){throw 'BF_BLOCKED: provider artifact is not bound to the requested attempt.'}
    if(-not $script:BFNativeProviderPriorArtifacts.ContainsKey($relative)){throw "BF_BLOCKED: provider artifact was not declared by core: $relative"}
    $declared=$script:BFNativeProviderPriorArtifacts[$relative]
    if(-not [string]::IsNullOrWhiteSpace($ExpectedSha256)){
        Assert-BFProviderSha256 $ExpectedSha256 'provider artifact sha256'
        if($declared.sha256 -cne $ExpectedSha256){throw "BF_BLOCKED: provider artifact hash binding differs: $relative"}
    }
    return $relative
}

function Assert-BFProviderPriorArtifacts {
    param($ProviderInput)
    $root=Assert-BFSafePath $ProviderInput.context_root
    $seen=[Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
    $map=@{}
    foreach($artifact in @($ProviderInput.prior_artifacts)){
        Assert-BFFields $artifact @('path','sha256','size_bytes','kind') @() 'prior_artifact'
        Assert-BFRelativePath $artifact.path
        $path=$artifact.path.Replace('\','/')
        if(-not $seen.Add($path)){throw "BF_INVALID: duplicate prior artifact: $path"}
        Assert-BFProviderSha256 $artifact.sha256 "prior_artifact[$path].sha256"
        if(($artifact.size_bytes -isnot [int] -and $artifact.size_bytes -isnot [long]) -or [int64]$artifact.size_bytes -lt 0){throw "BF_INVALID: invalid prior artifact size: $path"}
        if($artifact.kind -isnot [string] -or $artifact.kind -notin $script:BFNativeProviderArtifactKinds){throw "BF_INVALID: invalid prior artifact kind: $path"}
        $full=Assert-BFSafePath (Join-Path $root $path)
        if(-not $full.StartsWith($root.TrimEnd('\','/')+'\',[StringComparison]::OrdinalIgnoreCase)){throw 'BF_INVALID: prior artifact escaped context_root.'}
        if(-not (Test-Path -LiteralPath $full -PathType Leaf)){throw "BF_BLOCKED: declared prior artifact is missing: $path"}
        $info=Get-Item -LiteralPath $full -Force
        if([int64]$info.Length -ne [int64]$artifact.size_bytes -or (Get-BFFileHash $full) -cne $artifact.sha256){throw "BF_BLOCKED: declared prior artifact changed: $path"}
        $map[$path]=[ordered]@{path=$path;sha256=$artifact.sha256;size_bytes=[int64]$artifact.size_bytes;kind=$artifact.kind}
    }
    $script:BFNativeProviderPriorArtifacts=$map
    return $map
}

function Test-BFProviderCancelled {
    param([string]$SignalPath,[string]$TaskId,[string]$AttemptId)
    if([string]::IsNullOrWhiteSpace($SignalPath)){return $false}
    $path=Assert-BFSafePath $SignalPath
    if(-not (Test-Path -LiteralPath $path -PathType Leaf)){return $false}
    try{$signal=Read-BFJson $path}catch{throw 'BF_BLOCKED: cancellation signal is malformed.'}
    Assert-BFFields $signal @('schema_version','task_id','attempt_id','cancelled') @('reason') 'cancel_signal'
    if($signal.schema_version -ne 1 -or $signal.task_id -cne $TaskId -or $signal.attempt_id -cne $AttemptId){throw 'BF_BLOCKED: cancellation signal identity mismatch.'}
    if($signal.cancelled -isnot [bool]){throw 'BF_BLOCKED: cancellation signal has an invalid cancelled flag.'}
    return [bool]$signal.cancelled
}

function Get-BFProviderProjectRules {
    param([string]$ProjectPath)
    $reviewCommon=Join-Path (Split-Path (Split-Path $PSScriptRoot -Parent) -Parent) '1c-spec-review/scripts/Review.Common.ps1'
    if(-not (Test-Path -LiteralPath $reviewCommon -PathType Leaf)){throw 'BF_BLOCKED: project policy parser is unavailable.'}
    . $reviewCommon
    $path=Join-Path (Assert-BFSafePath $ProjectPath) 'bsl-flow.yaml'
    $text=if(Test-Path -LiteralPath $path -PathType Leaf){[IO.File]::ReadAllText($path)}else{'# No project overrides.'}
    if([string]::IsNullOrWhiteSpace($text)){$text='# No project overrides.'}
    $s=Get-BSLFlowYamlValue $text @('review','routing','s_default') 'optional'
    if($s -notin @('optional','required','off')){throw 'BF_INVALID: invalid S review policy.'}
    foreach($name in @('m_default','l_default','high_risk_override')){if((Get-BSLFlowYamlValue $text @('review','routing',$name) 'required') -ne 'required'){throw 'BF_BLOCKED: project policy weakens mandatory M/L/high review.'}}
    return [ordered]@{s_review_required=($s -eq 'required')}
}

function Assert-BFProviderAttempt {
    param($ProviderInput)
    $isMeasure=$ProviderInput.operation -eq 'measure'
    if($null -eq $ProviderInput.attempt){
        if(-not $isMeasure){throw 'BF_INVALID: execute requires a registered attempt.'}
        # The first activation measure is intentionally provisional.  A later
        # stage-bound measure carries a registered attempt and is validated by
        # the exact binding checks below.
        if($null -ne $ProviderInput.state_view.active_attempt){throw 'BF_BLOCKED: measure view contains an active attempt without its binding.'}
        return
    }
    Assert-BFFields $ProviderInput.attempt @('schema_version','task_id','attempt_id','stage','intent_revision','authorization_revision','dependencies','source_manifest','worker_path','executable','requested_models','started_at','operation_id') @('memory','controller_process') 'attempt'
    if($ProviderInput.attempt.schema_version -ne 1){throw 'BF_INVALID: unsupported attempt schema.'}
    Assert-BFUuid $ProviderInput.attempt.attempt_id
    if($ProviderInput.attempt.task_id -cne $ProviderInput.task_id){throw 'BF_CONFLICT: attempt is not bound to the provider task.'}
    if($isMeasure){
        # A pre-dispatch stage measure may be prepared before Go publishes the
        # active_attempt field.  Once a view carries an active attempt, the
        # measurement must still match it exactly; this conditional binding
        # keeps the measure read-only without granting dispatch authority.
        if($null -ne $ProviderInput.state_view.active_attempt -and $ProviderInput.attempt.attempt_id -cne $ProviderInput.state_view.active_attempt){throw 'BF_CONFLICT: attempt is not bound to the state view.'}
    }elseif($ProviderInput.attempt.attempt_id -cne $ProviderInput.state_view.active_attempt){throw 'BF_CONFLICT: attempt is not bound to the state view.'}
    if($ProviderInput.attempt.stage -notin $script:BFNativeProviderStages){throw 'BF_INVALID: unsupported provider stage.'}
    if($ProviderInput.state_view.stage -cne $ProviderInput.attempt.stage){throw 'BF_CONFLICT: attempt stage differs from state view.'}
    if((Assert-BFSafePath $ProviderInput.attempt.worker_path) -cne (Assert-BFSafePath $ProviderInput.state_view.worker_path)){throw 'BF_CONFLICT: attempt worker root differs from state view.'}
    [void](Assert-BFSafePath $ProviderInput.attempt.executable)
    if($ProviderInput.attempt.executable -notmatch '\.exe$'){throw 'BF_BLOCKED: provider execution requires a native executable.'}
}

function Assert-BFProviderInput {
    param($ProviderInput)
    Assert-BFFields $ProviderInput @('schema_version','contract','operation','task_id','state_view','attempt','context_root','artifact_root','canonical_store_root','cancel_signal','provider_contract','prior_artifacts') @() 'provider_input'
    if($ProviderInput.schema_version -ne 1 -or $ProviderInput.contract -cne $script:BFNativeProviderContract){throw 'BF_INVALID: unsupported provider contract.'}
    if($ProviderInput.operation -notin @('measure','execute')){throw 'BF_INVALID: unsupported provider operation.'}
    Assert-BFUuid $ProviderInput.task_id
    Assert-BFFields $ProviderInput.provider_contract @('name','version','host_sha256','provider_sha256','asset_manifest_sha256') @() 'provider_contract'
    if($ProviderInput.provider_contract.name -cne $script:BFNativeProviderContract -or $ProviderInput.provider_contract.version -ne 1){throw 'BF_INVALID: provider contract identity mismatch.'}
    foreach($name in @('host_sha256','provider_sha256','asset_manifest_sha256')){Assert-BFProviderSha256 $ProviderInput.provider_contract.$name "provider_contract.$name"}
    Assert-BFState $ProviderInput.state_view
    if($ProviderInput.state_view.task_id -cne $ProviderInput.task_id -or $ProviderInput.state_view.request.request_id -cne $ProviderInput.task_id){throw 'BF_CONFLICT: state view/task identity mismatch.'}
    [void](Assert-BFSafePath $ProviderInput.state_view.project_path)
    [void](Assert-BFSafePath $ProviderInput.state_view.worker_path)
    [void](Assert-BFProviderGitStore $ProviderInput)
    $context=Assert-BFSafePath $ProviderInput.context_root
    $artifact=Assert-BFSafePath $ProviderInput.artifact_root
    $canonical=Assert-BFSafePath $ProviderInput.canonical_store_root
    Assert-BFProviderDisjointPath $context $canonical 'context/canonical store'
    Assert-BFProviderDisjointPath $artifact $canonical 'artifact/canonical store'
    Assert-BFProviderDisjointPath $context $artifact 'context/artifact'
    Assert-BFProviderDisjointPath $context $ProviderInput.state_view.worker_path 'context/worker'
    Assert-BFProviderDisjointPath $artifact $ProviderInput.state_view.worker_path 'artifact/worker'
    [void](Assert-BFSafePath $ProviderInput.cancel_signal)
    if(Test-BFProviderNestedPath $ProviderInput.cancel_signal $canonical){throw 'BF_INVALID: cancellation signal is inside canonical store.'}
    if($ProviderInput.prior_artifacts -isnot [array]){throw 'BF_INVALID: prior_artifacts must be an array.'}
    [void](Assert-BFProviderPriorArtifacts $ProviderInput)
    Assert-BFProviderAttempt $ProviderInput
    return $ProviderInput
}

function Assert-BFProviderRepairFailure {
    param($State,[string]$AttemptId,[string]$ContextRoot)
    Assert-BFUuid $AttemptId
    $repair=Get-BFValue $State 'repair'
    if($null -eq $repair -or $repair.rounds -ge (Get-BFValue $State.request 'max_source_repairs' 0)){throw 'BF_BLOCKED: source repair budget exhausted.'}
    $entry=@($State.evidence | Where-Object {$_.attempt_id -ceq $AttemptId})
    if($entry.Count -ne 1 -or $entry[0].stage -cne 'verify' -or $entry[0].outcome -cne 'FAIL'){throw 'BF_BLOCKED: repair requires a registered failed verification.'}
    $failure=Get-BFProviderContextArtifact -ContextRoot $ContextRoot -RelativePath ('attempts/'+$AttemptId+'/result.json') -AsJson
    if((Get-BFHash $failure) -cne $entry[0].result_sha256 -or $failure.side_effects -cne 'none' -or (Get-BFValue $failure.proposal 'repair_eligible' $false) -ne $true){throw 'BF_BLOCKED: failed verification is not a trusted safe repair input.'}
    if((Get-BFHash (Get-BFDependencies $State 'verify' $null)) -ne (Get-BFHash $failure.dependencies)){throw 'BF_BLOCKED: failed verification inputs changed before diagnosis.'}
    foreach($raw in @($entry[0].raw_hashes)){
        $relative=ConvertTo-BFProviderContextRelativePath -Path $raw.path -AttemptId $AttemptId -ExpectedSha256 $raw.sha256 -ContextRoot $ContextRoot
        $declared=Get-BFProviderContextArtifact -ContextRoot $ContextRoot -RelativePath $relative
        if((Get-BFFileHash $declared) -cne $raw.sha256){throw 'BF_BLOCKED: retained failed verification evidence changed.'}
    }
}

function Assert-BFProviderProtectedTests {
    param($State,[string]$ContextRoot)
    $id=Get-BFValue (Get-BFValue $State 'repair') 'diagnosis_attempt'
    if($null -eq $id){return}
    Assert-BFUuid $id
    $entry=@($State.evidence | Where-Object {$_.attempt_id -ceq $id})
    $diagnosis=Get-BFProviderContextArtifact -ContextRoot $ContextRoot -RelativePath ('attempts/'+$id+'/result.json') -AsJson
    if($entry.Count -ne 1 -or (Get-BFHash $diagnosis) -ne $entry[0].result_sha256){throw 'BF_BLOCKED: retained repair diagnosis changed.'}
    $failureId=$diagnosis.proposal.failure_attempt_id
    Assert-BFUuid $failureId
    $failure=Get-BFProviderContextArtifact -ContextRoot $ContextRoot -RelativePath ('attempts/'+$failureId+'/start.json') -AsJson
    if($failure.task_id -ne $State.task_id -or $failure.attempt_id -ne $failureId -or $failure.source_manifest.sha256 -ne $diagnosis.dependencies.source -or (Get-BFHash $failure.source_manifest.files) -ne $failure.source_manifest.sha256){throw 'BF_BLOCKED: protected test baseline does not match the diagnosed failure.'}
    $before=@(Get-BFProtectedTestManifest $State $failure.source_manifest)
    $after=@(Get-BFProtectedTestManifest $State (Get-BFSourceManifest $State))
    if((Get-BFHash $before) -ne (Get-BFHash $after)){throw 'BF_BLOCKED: automatic repair changed protected test inputs; a trusted test-contract revision is required.'}
}

function Assert-BFProviderCoverageAccepted {
    param($State,[string]$ContextRoot,$PriorArtifacts)
    if($State.request.mode -ne 'implement' -or -not(Test-BFCoverageProperty $State.request 'requirements')){return}
    $reviews=@($State.evidence | Where-Object {$_.stage -eq 'code_review'})
    if($reviews.Count -eq 0){throw 'BF_BLOCKED: requirement coverage needs a fresh independent code review.'}
    $review=$reviews[-1]
    $reviewId=$review.attempt_id
    Assert-BFUuid $reviewId
    $result=Get-BFProviderContextArtifact -ContextRoot $ContextRoot -RelativePath ('attempts/'+$reviewId+'/result.json') -AsJson
    if((Get-BFHash $result) -cne $review.result_sha256){throw 'BF_BLOCKED: coverage review result changed.'}
    $proposal=$result.proposal
    if(Test-BFCoverageProperty $proposal 'review'){$proposal=$proposal.review}
    $coverage=Get-BFValue $proposal 'coverage_review'
    if($null -eq $coverage -or $coverage.verdict -cne 'PASS'){throw 'BF_BLOCKED: independently sufficient requirement coverage is missing.'}
    # Coverage inspection is read-only from the source perspective. Keep its
    # generated binding below the existing worker-admin exclusion so a fresh
    # source manifest cannot mistake it for an implementation change.
    $rawDir=Join-Path (Assert-BFSafePath $State.worker_path) ('.bsl-flow-worker/provider/coverage-'+$reviewId)
    [void][IO.Directory]::CreateDirectory($rawDir)
    $binding=Get-BFProviderContextArtifact -ContextRoot $ContextRoot -RelativePath ('attempts/'+$reviewId+'/raw/coverage-review-binding.json') -AsJson
    $computed=Assert-BFCoverageReview $State $coverage $rawDir
    if((Get-BFHash $binding) -ne (Get-BFHash $computed)){throw 'BF_BLOCKED: registered coverage binding is stale.'}
}

function Get-BFProviderManagedCouncilEvidence {
    param($State,[string]$ContextRoot,[int]$MaxBytes=262144)
    $entries=@((Get-BFValue $State 'evidence' @()) | Where-Object {$_.stage -eq 'inspect' -and $_.outcome -eq 'PASS'})
    $entry=if($entries.Count -gt 0){$entries[-1]}else{$null}
    $attemptId=$null;$resultSha256=$null;$outcome='missing';$dependencies=$null;$rawHashes=@();$proposal=$null;$missingContext=@()
    if($null -ne $entry){
        $attemptId=[string]$entry.attempt_id;Assert-BFUuid $attemptId
        $result=Get-BFProviderContextArtifact -ContextRoot $ContextRoot -RelativePath ('attempts/'+$attemptId+'/result.json') -AsJson
        if((Get-BFHash $result) -cne [string]$entry.result_sha256 -or $result.stage -cne 'inspect' -or $result.outcome -cne 'PASS' -or $result.attempt_id -cne $attemptId -or $result.task_id -cne $State.task_id){throw 'BF_BLOCKED: inspect evidence no longer matches the provider context.'}
        $current=Get-BFDependencies $State 'inspect' $null
        if((Get-BFHash $current) -cne (Get-BFHash $entry.dependencies) -or (Get-BFHash $current) -cne (Get-BFHash $result.dependencies)){throw 'BF_BLOCKED: inspect evidence is stale for the provider context.'}
        foreach($raw in @($entry.raw_hashes)){
            $relative=ConvertTo-BFProviderContextRelativePath -Path $raw.path -AttemptId $attemptId -ExpectedSha256 $raw.sha256 -ContextRoot $ContextRoot
            $full=Get-BFProviderContextArtifact -ContextRoot $ContextRoot -RelativePath $relative
            if((Get-BFFileHash $full) -cne $raw.sha256){throw 'BF_BLOCKED: inspect evidence raw artifact changed.'}
        }
        $resultSha256=[string]$entry.result_sha256;$outcome='PASS';$dependencies=$entry.dependencies;$rawHashes=@($entry.raw_hashes);$proposal=$result.proposal
    }else{$missingContext=@('verified inspect evidence')}
    $architectureRoot=Get-BFArchitectureContextRoot ([string]$State.project_path)
    $architecture=Get-BFArchitectureBundle 'spec_review' $architectureRoot
    $bundle=[ordered]@{schema_version=2;source='bsl-flow.inspect+architecture';task_id=[string]$State.task_id;attempt_id=$attemptId;outcome=$outcome;result_sha256=$resultSha256;dependencies=$dependencies;raw_hashes=$rawHashes;proposal=$proposal;missing_context=$missingContext;architecture=$architecture}
    $text=Get-BFCanonicalJson $bundle
    if([Text.UTF8Encoding]::new($false).GetByteCount($text) -gt $MaxBytes){throw 'BF_BLOCKED: verified inspect evidence exceeds the council input bound.'}
    return $text
}

function Get-BFProviderArtifactKind {
    param([string]$RelativePath)
    $lower=$RelativePath.ToLowerInvariant()
    if($lower -match '(^|/)budget(/|$)'){return 'budget'}
    if($lower -match '(^|/)(process|exit)\.json$' -or $lower -match '(^|/)(stdout|stderr)\.txt$'){return 'process'}
    if($lower -match 'model-result\.json$'){return 'model'}
    if($lower -match 'review|reconciliation|spec-lint'){return 'review'}
    if($lower -match 'verification|observations|junit|coverage'){return 'verification'}
    if($lower -match 'failure\.json$'){return 'failure'}
    return 'raw'
}

function Get-BFProviderArtifactManifest {
    param([string]$ArtifactRoot)
    $root=Assert-BFSafePath $ArtifactRoot
    if(-not(Test-Path -LiteralPath $root -PathType Container)){return @()}
    $items=@(Get-ChildItem -LiteralPath $root -File -Recurse | Where-Object {$_.Name -notlike '*.tmp'} | Sort-Object FullName)
    return @($items | ForEach-Object {
        $relative=$_.FullName.Substring($root.Length).TrimStart('\','/') -replace '\\','/'
        Assert-BFRelativePath $relative
        $segments=@($relative -split '/')
        if(@($segments | Where-Object { $_ -in @('current','revisions','inputs','acceptance','current.json','acceptance.json') }).Count -gt 0){throw 'BF_BLOCKED: provider artifact contains controller state.'}
        [ordered]@{path=$relative;sha256=Get-BFFileHash $_.FullName;size_bytes=[int64]$_.Length;kind=Get-BFProviderArtifactKind $relative}
    })
}

function Get-BFProviderProcessReceipts {
    param([string]$ArtifactRoot)
    $root=Assert-BFSafePath $ArtifactRoot;$receipts=@()
    foreach($exitFile in @(Get-ChildItem -LiteralPath $root -Filter 'exit.json' -File -Recurse)){
        $dir=$exitFile.Directory.FullName;$processFile=Join-Path $dir 'process.json'
        if(-not(Test-Path -LiteralPath $processFile -PathType Leaf)){throw 'BF_BLOCKED: native process exit receipt has no process identity.'}
        $exit=Read-BFJson $exitFile.FullName;$process=Read-BFJson $processFile
        Assert-BFFields $exit @('exit_code','stop_reason','elapsed_seconds','process_id','executable','stdout','stderr') @() 'process_exit'
        Assert-BFFields $process @('pid','start_time_utc','executable','arguments_sha256') @() 'process_identity'
        foreach($path in @($exit.stdout,$exit.stderr)){
            if(-not(Test-Path -LiteralPath $path -PathType Leaf)){throw 'BF_BLOCKED: native process stream receipt is missing.'}
        }
        $processPath=ConvertTo-BFProviderArtifactRelativePath $root $processFile;$exitPath=ConvertTo-BFProviderArtifactRelativePath $root $exitFile;$stdoutPath=ConvertTo-BFProviderArtifactRelativePath $root ([string]$exit.stdout);$stderrPath=ConvertTo-BFProviderArtifactRelativePath $root ([string]$exit.stderr)
        $receipts+=,[ordered]@{process_path=$processPath;process_sha256=Get-BFFileHash $processFile;exit_path=$exitPath;exit_sha256=Get-BFFileHash $exitFile;stdout_path=$stdoutPath;stdout_sha256=Get-BFFileHash $exit.stdout;stderr_path=$stderrPath;stderr_sha256=Get-BFFileHash $exit.stderr;exit_code=$exit.exit_code;stop_reason=$exit.stop_reason}
    }
    return $receipts
}

function ConvertTo-BFProviderArtifactRelativePath {
    param([string]$ArtifactRoot,[string]$Path)
    $root=(Assert-BFSafePath $ArtifactRoot).TrimEnd('\','/')
    $full=Assert-BFSafePath $Path
    if(-not ($full -eq $root -or $full.StartsWith($root+'\',[StringComparison]::OrdinalIgnoreCase) -or $full.StartsWith($root+'/',[StringComparison]::OrdinalIgnoreCase))){throw 'BF_INVALID: process receipt escaped artifact_root.'}
    $relative=$full.Substring($root.Length).TrimStart('\','/') -replace '\\','/'
    Assert-BFRelativePath $relative
    return $relative
}

function ConvertTo-BFProviderOutput {
    param($ProviderInput,$Terminal)
    $status=switch($Terminal.outcome){'PASS'{'completed'}'REVISE'{'completed'}'REPAIR'{'completed'}'NEEDS_INPUT'{'needs_input'}'FAIL'{'failed'}default{'blocked'}}
    # A single-artifact attempt (for example a deterministic verify that only
    # retains observations.json) must still serialize artifacts as an array;
    # command output unrolls one-element arrays into a scalar on assignment.
    $manifest=@(Get-BFProviderArtifactManifest $ProviderInput.artifact_root)
    $providerContract=$ProviderInput.provider_contract
    $sourceManifest=$null
    try{$sourceManifest=Get-BFSourceManifest $ProviderInput.state_view}catch{
        # A failed/blocked attempt may have lost access to the worker (or its
        # HEAD). Preserve the closed observation with an explicit null so the
        # controller can retain the outer receipt and independently fail closed;
        # never manufacture a stale source manifest from the attempt start.
        if($status -eq 'completed'){throw}
    }
    return [ordered]@{
        schema_version=1;contract=$script:BFNativeProviderContract;task_id=$ProviderInput.task_id;attempt_id=$ProviderInput.attempt.attempt_id;stage=$ProviderInput.attempt.stage;status=$status;summary=[string]$Terminal.summary;proposal=$Terminal.proposal;side_effects=$Terminal.side_effects;dependencies=$Terminal.dependencies;source_manifest=$sourceManifest;artifacts=$manifest;process_receipt=[ordered]@{processes=@(Get-BFProviderProcessReceipts $ProviderInput.artifact_root)};provider_contract=$providerContract
    }
}

function Invoke-BFProviderMeasure {
    param($ProviderInput)
    $state=$ProviderInput.state_view;$blockers=[Collections.Generic.List[string]]::new();$requestValid=$true;$policyFiles=@();$policyRules=$null;$manifest=$null;$specInputs=$null;$dependencies=$null;$capability=$null
    try{Assert-BFRequest $state.request}catch{$requestValid=$false;[void]$blockers.Add($_.Exception.Message)}
    try{$policyFiles=@(Get-BFPolicyFiles $state.project_path);$policyRules=Get-BFProviderProjectRules $state.project_path}catch{[void]$blockers.Add($_.Exception.Message)}
    try{$manifest=Get-BFSourceManifest $state}catch{[void]$blockers.Add($_.Exception.Message)}
    try{$specInputs=Get-BFSpecInputs $state}catch{[void]$blockers.Add($_.Exception.Message)}
    # Initial activation measures the inspect inputs.  A future stage-bound
    # measure may carry an attempt-shaped binding; when it does, preserve that
    # stage in the dependency observation instead of silently measuring as
    # inspect.
    $measureStage=if($null -ne $ProviderInput.attempt){[string]$ProviderInput.attempt.stage}else{'inspect'}
    try{$dependencies=Get-BFDependencies $state $measureStage $null}catch{[void]$blockers.Add($_.Exception.Message)}
    if($null -ne (Get-BFValue $state.request 'execution_profile')){
        $capabilityRoot=Assert-BFSafePath (Join-Path $ProviderInput.artifact_root 'capability')
        $capabilitySource=Assert-BFSafePath (Join-Path $capabilityRoot 'source')
        $capabilityScratch=Assert-BFSafePath (Join-Path $capabilityRoot 'scratch')
        $capabilityConfig=Assert-BFSafePath (Join-Path $capabilityRoot 'config')
        try{
            # The activation view intentionally points at the real project until
            # the controller has a baseline. Only the host capability probe gets
            # a cloned state and an isolated source root, because the permission
            # profile must never reopen project/.bsl-flow/tasks as a worker tree.
            $capabilityJson=Get-BFCanonicalJson $state
            $convertCommand=Get-Command ConvertFrom-Json -ErrorAction Stop
            if($convertCommand.Parameters.ContainsKey('DateKind')){
                $capabilityState=ConvertFrom-Json -InputObject $capabilityJson -DateKind String -Depth 100 -ErrorAction Stop
            }else{
                $capabilityState=ConvertFrom-Json -InputObject $capabilityJson -Depth 100 -ErrorAction Stop
            }
            $capabilityState.worker_path=$capabilitySource
            [void][IO.Directory]::CreateDirectory($capabilitySource)
            $permissions=Get-BFExecutionPermissionProfile $capabilityState $capabilityScratch $capabilityConfig $false $ProviderInput.canonical_store_root
            $capability=Test-BFExecutionCapability -State $capabilityState -Directory $capabilityRoot -Scratch $capabilityScratch -Config $capabilityConfig -Permissions $permissions -Writable $false -CanonicalStoreRoot $ProviderInput.canonical_store_root
        }catch{[void]$blockers.Add($_.Exception.Message)}
    }else{[void]$blockers.Add('BF_BLOCKED: measure requires a trusted execution_profile.')}
    if($null -ne $manifest){
        try{
            # Re-enumerate the original project after the isolated probe. This
            # closes the provider-side source TOCTOU window and keeps the
            # capability clone from becoming the measured source manifest.
            $afterManifest=Get-BFSourceManifest $state
            if((Get-BFHash $afterManifest) -cne (Get-BFHash $manifest)){throw 'BF_BLOCKED: source changed during provider measure.'}
        }catch{[void]$blockers.Add($_.Exception.Message)}
    }
    return [ordered]@{schema_version=1;contract=$script:BFNativeProviderContract;task_id=$ProviderInput.task_id;operation='measure';request_valid=$requestValid;policy_files=$policyFiles;policy_rules=$policyRules;source_manifest=$manifest;spec_inputs=$specInputs;dependencies=$dependencies;capability=$capability;blockers=@($blockers)}
}

function Invoke-BFProviderExecute {
    param($ProviderInput)
    $state=$ProviderInput.state_view;$attempt=$ProviderInput.attempt;$canonical=$ProviderInput.canonical_store_root
    $state | Add-Member -NotePropertyName canonical_store_root -NotePropertyValue $canonical -Force
    $providerContext=[ordered]@{task_id=$ProviderInput.task_id;context_root=$ProviderInput.context_root;artifact_root=$ProviderInput.artifact_root;cancel_signal=$ProviderInput.cancel_signal;canonical_store_root=$canonical;prior_artifacts=$ProviderInput.prior_artifacts;attempt_id=$attempt.attempt_id}
    if(Test-BFProviderCancelled $ProviderInput.cancel_signal $ProviderInput.task_id $attempt.attempt_id){throw 'BF_BLOCKED: cancelled before provider dispatch.'}
    $current=Get-BFDependencies $state $attempt.stage $attempt.source_manifest
    if((Get-BFHash $current) -cne (Get-BFHash $attempt.dependencies)){throw 'BF_BLOCKED: provider inputs changed before dispatch.'}
    $run=[ordered]@{state=$state;attempt=$attempt;directory=$ProviderInput.artifact_root;context_root=$ProviderInput.context_root}
    $terminal=Invoke-BFStageObservation -Run $run -CodexPath $attempt.executable -StageExecutor $null -RecoveredResult $null -ProviderContext $providerContext
    return ConvertTo-BFProviderOutput $ProviderInput $terminal
}

function Invoke-BFNativeProviderRequest {
    [CmdletBinding()]
    param([Parameter(Mandatory = $true)]$ProviderInput)
    Assert-BFProviderInput $ProviderInput | Out-Null
    if($ProviderInput.operation -eq 'measure'){return Invoke-BFProviderMeasure $ProviderInput}
    return Invoke-BFProviderExecute $ProviderInput
}
