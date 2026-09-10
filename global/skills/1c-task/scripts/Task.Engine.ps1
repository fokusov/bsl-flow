#Requires -Version 7.0
Set-StrictMode -Version Latest

function Get-BFTaskDirectory {
    param([string]$ProjectPath, [string]$TaskId)
    Assert-BFUuid $TaskId
    return Assert-BFSafePath (Join-Path $ProjectPath ('.bsl-flow/tasks/' + $TaskId))
}

function Read-BFTask {
    param([string]$ProjectPath, [string]$TaskId)
    $state=Read-BFJournal (Get-BFTaskDirectory $ProjectPath $TaskId)
    if ($null -eq $state) { throw 'BF_INVALID: task does not exist.' }
    Assert-BFState $state
    if ($state.project_path -ne (Assert-BFSafePath $ProjectPath)) { throw 'BF_BLOCKED: task belongs to a different project.' }
    return $state
}

function Save-BFTask {
    param($State, [int]$ExpectedRevision)
    $State.updated_at=[DateTime]::UtcNow.ToString('o')
    return Write-BFRevision -Directory (Get-BFTaskDirectory $State.project_path $State.task_id) -State $State -ExpectedRevision $ExpectedRevision
}

function Get-BFIntentHash {
    param($Request)
    $intent=[ordered]@{prompt=$Request.prompt;analysis_goal=$Request.analysis_goal;criteria=$Request.criteria;complexity=$Request.complexity;risk=$Request.risk;impact_flags=$Request.impact_flags;source_paths=@(Get-BFValue $Request 'source_paths' @('.'))}
    if ((Get-BFValue $Request 'max_source_repairs' 0) -gt 0) { $intent.max_source_repairs=$Request.max_source_repairs }
    if(Test-BFCoverageProperty $Request 'requirements'){$intent.requirements=$Request.requirements}
    return Get-BFHash $intent
}

function Get-BFProjectRules {
    param([string]$ProjectPath)
    . (Join-Path (Split-Path (Split-Path $PSScriptRoot -Parent) -Parent) '1c-spec-review/scripts/Review.Common.ps1')
    $path=Join-Path $ProjectPath 'bsl-flow.yaml'
    $text=if(Test-Path -LiteralPath $path -PathType Leaf){[IO.File]::ReadAllText($path)}else{'# No project overrides.'}
    if([string]::IsNullOrWhiteSpace($text)){$text='# No project overrides.'}
    $s=Get-BSLFlowYamlValue $text @('review','routing','s_default') 'optional'
    if($s -notin @('optional','required','off')){throw 'BF_INVALID: invalid S review policy.'}
    foreach($name in @('m_default','l_default','high_risk_override')){if((Get-BSLFlowYamlValue $text @('review','routing',$name) 'required') -ne 'required'){throw 'BF_BLOCKED: project policy weakens mandatory M/L/high review.'}}
    return [ordered]@{s_review_required=($s -eq 'required')}
}

function Start-BFTask {
    param([string]$ProjectPath, $Request)
    Assert-BFRequest $Request
    $project=Assert-BFSafePath $ProjectPath
    if (-not (Test-Path -LiteralPath $project -PathType Container)) { throw 'BF_INVALID: project directory missing.' }
    $root=Invoke-BFGit $project @('rev-parse','--show-toplevel')
    if ((Assert-BFSafePath $root) -ne $project) { throw 'BF_INVALID: Start requires the exact Git project root.' }
    $directory=Get-BFTaskDirectory $project $Request.request_id
    $lock=Enter-BFLock $directory
    try {
        $existing=Read-BFJournal $directory
        if ($null -ne $existing) {
            if ($existing.request_hash -ne (Get-BFHash $Request)) { throw 'BF_CONFLICT: request_id already used with another initial request.' }
            Assert-BFState $existing
            return $existing
        }
        if(@($Request.criteria | Where-Object { $null -ne (Get-BFValue $_ 'native_1c') }).Count -gt 0 -and -not(Test-BFCoverageProperty $Request 'requirements')){
            throw 'BF_INVALID: new native tasks require trusted requirements and independent coverage review.'
        }
        # Git clean/smudge/process filters can execute project scripts during status
        # or checkout. This adapter does not run those commands with controller rights.
        $config=Invoke-BFGit $project @('config','--list')
        if($config -match '(?im)^filter\..*\.(clean|smudge|process)='){
            $names=@((Invoke-BFGit $project @('-c','core.quotePath=false','ls-files','--cached','--others','--exclude-standard')) -split '\r?\n' | Where-Object {$_})
            for($i=0;$i -lt $names.Count;$i+=50){
                $batch=@($names[$i..([math]::Min($i+49,$names.Count-1))])
                foreach($cached in @($false,$true)){
                    $args=@('check-attr');if($cached){$args+='--cached'};$args+=@('filter','--')+$batch
                    $attributes=Invoke-BFGit $project $args
                    if(@($attributes -split '\r?\n'|Where-Object{$_ -match ': filter: ' -and $_ -notmatch ': filter: (unspecified|unset)$'}).Count){throw 'BF_BLOCKED: executable Git filter applies to the checkout; no source script was launched.'}
                }
            }
        }
        $dirty=Invoke-BFGit $project @('status','--porcelain','--untracked-files=all')
        $remaining=@($dirty -split '\r?\n' | Where-Object { $_ -and $_ -notmatch '^\?\? \.bsl-flow/' })
        if ($remaining.Count -gt 0) { throw 'BF_BLOCKED: Start requires a clean baseline. Preserve and commit your source snapshot first; no automatic stash is performed.' }
        $baseline=Invoke-BFGit $project @('rev-parse','HEAD')
        $worker=Assert-BFSafePath (Join-Path $project ('.bsl-flow/worktrees/'+$Request.request_id))
        if (Test-Path -LiteralPath $worker) { throw 'BF_CONFLICT: unregistered worker directory exists; inspect it before Start.' }
        $policyFiles=@(Get-BFPolicyFiles $project)
        $now=[DateTime]::UtcNow.ToString('o')
        $state=[ordered]@{
            schema_version=1; task_id=$Request.request_id; revision=0;previous_sha256=$null;project_path=$project;worker_path=$worker;baseline=$baseline
            request=$Request;request_hash=Get-BFHash $Request;intent_revision=1;authorization_revision=1;intent_hash=Get-BFIntentHash $Request
            policy_hash=Get-BFHash $policyFiles;policy_files=$policyFiles;policy_rules=Get-BFProjectRules $project
            classification=[ordered]@{complexity=$Request.complexity;risk=$Request.risk;impact_flags=@($Request.impact_flags);rationale='Initial trusted task classification; inspect can only strengthen it.'}
            status='ready';stage='inspect';active_attempt=$null;unresolved_effect=$null;attempts=@();evidence=@();events=@();question=$null;blockers=@();acceptances=@();created_at=$now;updated_at=$now;correction_rounds=0
            repair=[ordered]@{rounds=0;pending_failure=$null;last_source_sha256=$null;diagnosis_attempt=$null}
        }
        [void](Invoke-BFGit $project @('worktree','add','--detach',$worker,$baseline))
        Write-BFJson -Path (Join-Path $directory 'inputs/initial-request.json') -Value $Request
        return Save-BFTask $state 0
    } finally { $lock.Dispose() }
}

function Update-BFTask {
    param([string]$ProjectPath, [string]$TaskId, $Event)
    Assert-BFFields $Event @('schema_version','input_event_id','expected_revision','kind','provenance') @('question_id','text','mode','resume','request','resolution') 'event'
    if ($Event.schema_version -ne 1) { throw 'BF_INVALID: unsupported event version.' }
    Assert-BFUuid $Event.input_event_id; Assert-BFProvenance $Event.provenance
    $directory=Get-BFTaskDirectory $ProjectPath $TaskId; $lock=Enter-BFLock $directory
    try {
        $state=Read-BFTask $ProjectPath $TaskId
        $hash=Get-BFHash $Event
        $previous=@($state.events | Where-Object { $_.input_event_id -eq $Event.input_event_id })
        if ($previous.Count) {
            if ($previous[0].sha256 -ne $hash) { throw 'BF_CONFLICT: input event identity reused with different payload.' }
            if($Event.kind -eq 'recovery' -and (Get-BFValue (Get-BFValue $Event 'resolution') 'scope') -eq 'native_1c'){[void](Complete-BFNativeRecovery $state $Event.resolution)}
            return $state
        }
        if ($Event.expected_revision -ne $state.revision) { throw 'BF_CONFLICT: stale expected_revision.' }
        if ($null -ne $state.active_attempt -and $Event.kind -ne 'recovery') { throw 'BF_CONFLICT: reconcile the active attempt before changing task inputs.' }
        if ($null -ne $state.unresolved_effect -and $Event.kind -ne 'recovery') { throw 'BF_BLOCKED: unresolved effects cannot be cleared by authorization or scope changes.' }
        if ($null -ne $state.question) {
            if ((Get-BFValue $Event 'question_id') -ne $state.question.question_id) { throw 'BF_CONFLICT: answer must identify the current question.' }
        } elseif (Get-BFValue $Event 'question_id') { throw 'BF_CONFLICT: answer refers to a closed question.' }
        switch ($Event.kind) {
            'authorization' {
                if ((Get-BFValue $Event 'mode') -notin @('analysis_only','implement') -and (Get-BFValue $Event 'resume') -ne $true) { throw 'BF_INVALID: authorization requires a mode or explicit resume.' }
                if ($state.status -eq 'completed' -and $state.request.mode -eq 'implement') { throw 'BF_CONFLICT: completed implementation requires new intent or request_id.' }
                if (Get-BFValue $Event 'mode') { $state.request.mode=$Event.mode }
                Assert-BFRequest $state.request
                $state.authorization_revision++
            }
            'clarification' {
                Assert-BFText (Get-BFValue $Event 'text') 'clarification.text'
                $state.request.prompt += "`n`nUser clarification:`n" + $Event.text
                $state.classification.impact_flags=@($state.classification.impact_flags | Where-Object { $_ -ne 'ambiguous_business_rule' })
                $state.request.impact_flags=@($state.request.impact_flags | Where-Object { $_ -ne 'ambiguous_business_rule' })
                $state.intent_revision++
                if($null -ne (Get-BFValue $state 'repair')){$state.repair.pending_failure=$null;$state.repair.diagnosis_attempt=$null;$state.repair.last_source_sha256=$null}
            }
            'scope_change' {
                $request=Get-BFValue $Event 'request'; Assert-BFRequest $request
                if ($request.request_id -ne $state.task_id) { throw 'BF_INVALID: scope update must keep task identity.' }
                $state.request=$request; $state.intent_revision++
                $state.classification=[ordered]@{complexity=$request.complexity;risk=$request.risk;impact_flags=@($request.impact_flags);rationale='Explicit user scope update.'}
                $state.policy_files=@(Get-BFPolicyFiles $state.project_path); $state.policy_hash=Get-BFHash $state.policy_files
                $state.policy_rules=Get-BFProjectRules $state.project_path
                $state.correction_rounds=0
                if($null -ne (Get-BFValue $state 'repair')){$state.repair=[ordered]@{rounds=0;pending_failure=$null;last_source_sha256=$null;diagnosis_attempt=$null}}
            }
            'recovery' {
                $wasCancelled=$state.status -eq 'cancelled'
                $unresolvedId=if($state.active_attempt){$state.active_attempt}elseif($null -ne $state.unresolved_effect){$state.unresolved_effect.attempt_id}else{$null}
                if($null -eq $unresolvedId){throw 'BF_CONFLICT: no unresolved effect or interrupted attempt.'}
                $resolution=Get-BFValue $Event 'resolution'
                Assert-BFFields $resolution @('attempt_id','scope','source_sha256','observation') @('target','inventory_sha256','retry_authorized') 'resolution'
                if($resolution.attempt_id -ne $unresolvedId){throw 'BF_BLOCKED: recovery must identify the exact unresolved attempt.'}
                $attemptDir=Join-Path $directory ('attempts/'+$unresolvedId)
                $start=Read-BFJson (Join-Path $attemptDir 'start.json')
                $runtimeAttempt=$start.stage -eq 'verify' -and @($state.request.criteria | Where-Object { $null -ne (Get-BFValue $_ 'native_1c') }).Count -gt 0
                $requiredScope=if($runtimeAttempt){'native_1c'}else{'source_only'}
                if($resolution.scope -ne $requiredScope){throw "BF_BLOCKED: this attempt requires $requiredScope control-read recovery."}
                if(-not $runtimeAttempt){Assert-BFFields $resolution @('attempt_id','scope','source_sha256','observation') @() 'resolution'}
                if($state.active_attempt){
                    if(Test-Path -LiteralPath (Join-Path $attemptDir 'result.json')){throw 'BF_BLOCKED: saved terminal result must be imported with Resume before recovery.'}
                    $owner=Get-BFValue $start 'controller_process'
                    if($null -ne $owner -and $null -ne (Get-BFOwnedProcess $owner)){throw 'BF_BLOCKED: attempt controller is still running; cancel or wait before recovery.'}
                }
                foreach($file in @(Get-ChildItem -LiteralPath $attemptDir -Filter process.json -File -Recurse)){
                    if($null -ne (Get-BFOwnedProcess (Read-BFJson $file.FullName))){throw 'BF_BLOCKED: owned child is still running; cancel or wait before recovery.'}
                }
                Assert-BFText $resolution.observation 'resolution.observation'
                $manifest=Get-BFSourceManifest $state
                if($resolution.source_sha256 -ne $manifest.sha256){throw 'BF_CONFLICT: recovery control-read source hash is stale.'}
                $runtimeRead=if($runtimeAttempt){Resolve-BFNativeRecovery $state $resolution $attemptDir}else{$null}
                $recoveryPath=Join-Path $directory ('inputs/recovery-'+$Event.input_event_id+'.json')
                $recovery=[ordered]@{resolution=$resolution;actual_source_manifest=$manifest;abandoned_attempt=$state.active_attempt;retained_raw_hashes=@(Get-BFRawHashes $attemptDir)}
                if($runtimeAttempt){$recovery.runtime_control_read=$runtimeRead}
                if(Test-Path -LiteralPath $recoveryPath){
                    $saved=Read-BFJson $recoveryPath
                    if((Get-BFHash $saved.resolution) -ne (Get-BFHash $resolution) -or $saved.actual_source_manifest.sha256 -ne $manifest.sha256){throw 'BF_CONFLICT: conflicting recovery receipt.'}
                }else{Write-BFJson -Path $recoveryPath -Value $recovery}
                $state.active_attempt=$null
                $state.unresolved_effect=$null
            }
            default { throw 'BF_INVALID: unsupported update kind.' }
        }
        $state.intent_hash=Get-BFIntentHash $state.request
        $state.events+=,[ordered]@{input_event_id=$Event.input_event_id;sha256=$hash;kind=$Event.kind;accepted_revision=$state.revision+1}
        $state.question=$null; $state.blockers=@(); $state.status='ready'
        if($Event.kind -eq 'recovery' -and $wasCancelled){$state.status='cancelled';$state.blockers=@('Effects reconciled. An explicit resume authorization is still required after cancellation.')}
        Write-BFJson -Path (Join-Path $directory ('inputs/'+$Event.input_event_id+'.json')) -Value $Event
        $state=Save-BFTask $state $state.revision
        if($Event.kind -eq 'recovery' -and $runtimeAttempt){[void](Complete-BFNativeRecovery $state $Event.resolution)}
        return $state
    } finally { $lock.Dispose() }
}

function Set-BFClassification {
    param($State, $Proposal)
    Assert-BFFields $Proposal @('complexity','risk','impact_flags','rationale') @() 'classification'
    if ($Proposal.complexity -notin @('S','M','L') -or $Proposal.risk -notin @('low','medium','high')) { throw 'BF_INVALID: invalid inspected classification.' }
    Assert-BFImpactFlags $Proposal.impact_flags; Assert-BFText $Proposal.rationale 'classification.rationale'
    $sizes=@('S','M','L'); $risks=@('low','medium','high')
    $flags=@((@($State.classification.impact_flags)+@($Proposal.impact_flags)) | Select-Object -Unique)
    $risk=$risks[[math]::Max([Array]::IndexOf($risks,$State.classification.risk),[Array]::IndexOf($risks,$Proposal.risk))]
    if (@($flags | Where-Object { $_ -in @('permissions','data_migration','data_deletion') }).Count) { $risk='high' }
    $State.classification=[ordered]@{complexity=$sizes[[math]::Max([Array]::IndexOf($sizes,$State.classification.complexity),[Array]::IndexOf($sizes,$Proposal.complexity))];risk=$risk;impact_flags=$flags;rationale=$Proposal.rationale}
}

function New-BFAttempt {
    param([string]$ProjectPath,[string]$TaskId,[string]$CodexPath)
    $directory=Get-BFTaskDirectory $ProjectPath $TaskId; $lock=Enter-BFLock $directory
    try {
        $state=Read-BFTask $ProjectPath $TaskId
        $next=Get-BFNext $state
        if ($next.action -ne 'dispatch') { throw "BF_CONFLICT: cannot dispatch: $($next.action)." }
        if (@($state.attempts).Count -ge (Get-BFValue $state.request 'max_attempts' 16)) { throw 'BF_BLOCKED: finite task attempt limit reached.' }
        if ($next.stage -in @('implement','code_review','verify')) { Assert-BFVerificationCoverage $state }
        if((Get-BFValue (Get-BFValue $state 'repair') 'rounds' 0) -gt 0){Assert-BFProtectedTests $state}
        $id=[guid]::NewGuid().ToString()
        $attemptPath=Join-Path $directory ('attempts/'+$id)
        $manifest=Get-BFSourceManifest $state
        $attempt=[ordered]@{schema_version=1;task_id=$state.task_id;attempt_id=$id;stage=$next.stage;intent_revision=$state.intent_revision;authorization_revision=$state.authorization_revision;dependencies=Get-BFDependencies $state $next.stage $manifest;source_manifest=$manifest;worker_path=$state.worker_path;executable=$CodexPath;requested_models=$state.request.models;started_at=[DateTime]::UtcNow.ToString('o');operation_id=$id}
        $attempt.controller_process=[ordered]@{pid=$PID;start_time_utc=(Get-Process -Id $PID).StartTime.ToUniversalTime().ToString('o')}
        Write-BFJson -Path (Join-Path $attemptPath 'start.json') -Value $attempt
        $state.active_attempt=$id; $state.stage=$next.stage; $state.status='running'; $state.blockers=@(); $state.attempts+=,$id
        $state=Save-BFTask $state $state.revision
        return [ordered]@{state=$state;attempt=$attempt;directory=$attemptPath}
    } finally { $lock.Dispose() }
}

function Get-BFRawHashes {
    param([string]$Directory)
    return @(Get-ChildItem -LiteralPath $Directory -File -Recurse | Where-Object { $_.Name -notin @('result.json','record.json') -and $_.Name -notlike '*.tmp' } | Sort-Object FullName | ForEach-Object { [void](Assert-BFSafePath $_.FullName); [ordered]@{path=$_.FullName;sha256=Get-BFFileHash $_.FullName} })
}

function Record-BFAttempt {
    param([string]$ProjectPath,[string]$TaskId,[string]$AttemptId)
    Assert-BFUuid $AttemptId
    $directory=Get-BFTaskDirectory $ProjectPath $TaskId; $lock=Enter-BFLock $directory
    try {
        $state=Read-BFTask $ProjectPath $TaskId
        if ($AttemptId -notin @($state.attempts)) { throw 'BF_CONFLICT: unregistered attempt.' }
        $attemptDir=Join-Path $directory ('attempts/'+$AttemptId)
        $result=Read-BFJson (Join-Path $attemptDir 'result.json')
        Assert-BFFields $result @('schema_version','task_id','attempt_id','stage','outcome','summary','dependencies','raw_hashes','proposal','side_effects') @() 'attempt_result'
        if ($result.schema_version -ne 1 -or $result.task_id -ne $TaskId -or $result.attempt_id -ne $AttemptId) { throw 'BF_CONFLICT: adapter result identity mismatch.' }
        $start=Read-BFJson (Join-Path $attemptDir 'start.json')
        if ($result.stage -ne $start.stage -or $result.outcome -notin @('PASS','FAIL','BLOCKED','NEEDS_INPUT','REVISE','REPAIR')) { throw 'BF_INVALID: invalid attempt result.' }
        if ($result.outcome -eq 'REVISE' -and ($result.stage -ne 'code_review' -or $state.correction_rounds -ge 1)) { throw 'BF_INVALID: correction limit or stage conflict.' }
        if ($result.outcome -eq 'REPAIR' -and ($result.stage -ne 'diagnose' -or $result.side_effects -ne 'none')) { throw 'BF_INVALID: repair must come from a read-only diagnosis.' }
        foreach ($raw in $result.raw_hashes) {
            $safe=Assert-BFSafePath $raw.path
            if (-not $safe.StartsWith($attemptDir+[IO.Path]::DirectorySeparatorChar,[StringComparison]::OrdinalIgnoreCase)) { throw 'BF_INVALID: raw evidence escaped its registered attempt.' }
            if ((Get-BFFileHash $safe) -ne $raw.sha256) { throw 'BF_BLOCKED: raw evidence changed after adapter completion.' }
        }
        $resultHash=Get-BFHash $result
        $recordPath=Join-Path $attemptDir 'record.json'
        $existing=@($state.evidence | Where-Object { $_.attempt_id -eq $AttemptId })
        if ($existing.Count) {
            if ($existing[0].result_sha256 -ne $resultHash) { throw 'BF_CONFLICT: conflicting terminal attempt result.' }
            Complete-BFRecordedNativeSuccess $state $AttemptId
            return $state
        }
        if ($state.active_attempt -ne $AttemptId) { throw 'BF_CONFLICT: attempt is no longer active.' }
        if ($start.intent_revision -ne $state.intent_revision) { throw 'BF_CONFLICT: task intent changed during execution.' }
        if ($result.outcome -in @('PASS','REPAIR')) {
            Assert-BFPolicyFresh $state
            if ($result.stage -eq 'inspect') { Set-BFClassification $state $result.proposal }
            $current=Get-BFDependencies $state $result.stage $null
            # Inspect computes a classification; other stages must bind to current bytes.
            if ((Get-BFHash $current) -ne (Get-BFHash $result.dependencies)) { throw 'BF_BLOCKED: adapter result is stale.' }
        }
        if ($result.outcome -eq 'REPAIR') {
            Assert-BFRepairFailure $state $state.repair.pending_failure
            Assert-BFDiagnosis $state $result.proposal
            if ($result.proposal.category -ne 'implementation') { throw 'BF_INVALID: only an implementation diagnosis may start a repair.' }
        }
        $state.evidence+=,[ordered]@{stage=$result.stage;attempt_id=$AttemptId;outcome=$result.outcome;dependencies=$result.dependencies;raw_hashes=@($result.raw_hashes);result_sha256=$resultHash;summary=$result.summary}
        $state.active_attempt=$null
        if($result.side_effects -eq 'unknown'){
            $runtimeAttempt=$result.stage -eq 'verify' -and @($state.request.criteria | Where-Object { $null -ne (Get-BFValue $_ 'native_1c') }).Count -gt 0
            $state.unresolved_effect=[ordered]@{attempt_id=$AttemptId;stage=$result.stage;source_before_sha256=$start.source_manifest.sha256;state='unknown';scope=if($runtimeAttempt){'native_1c'}else{'source_only'}}
        }
        if ($state.status -ne 'cancelled') {
            switch ($result.outcome) {
                'PASS' { $state.status='ready'; $state.blockers=@() }
                'NEEDS_INPUT' { $state.status='needs_input'; $state.question=[ordered]@{question_id=[guid]::NewGuid().ToString();text=$result.summary;intent_revision=$state.intent_revision};$state.blockers=@($result.summary) }
                'FAIL' {
                    $state.status='failed';$state.blockers=@($result.summary)
                    if ($result.stage -eq 'verify' -and $result.side_effects -eq 'none' -and (Get-BFValue $result.proposal 'repair_eligible' $false)) {
                        if ($null -eq (Get-BFValue $state 'repair')) { $state | Add-Member -NotePropertyName repair -NotePropertyValue ([ordered]@{rounds=0;pending_failure=$null;last_source_sha256=$null;diagnosis_attempt=$null}) }
                        $limit=Get-BFValue $state.request 'max_source_repairs' 0
                        if ($state.repair.rounds -lt $limit -and $state.repair.last_source_sha256 -ne $result.dependencies.source) {
                            $state.repair.pending_failure=$AttemptId;$state.status='ready';$state.blockers=@()
                        }
                    }
                }
                'REVISE' { $state.correction_rounds++;$state.status='ready';$state.blockers=@() }
                'REPAIR' {
                    $state.repair.rounds++;$state.repair.last_source_sha256=$result.dependencies.source
                    $state.repair.pending_failure=$null;$state.repair.diagnosis_attempt=$AttemptId
                    $state.status='ready';$state.blockers=@()
                }
                default { $state.status='blocked';$state.blockers=@($result.summary) }
            }
        }
        $state=Save-BFTask $state $state.revision
        if (-not (Test-Path -LiteralPath $recordPath)) { Write-BFJson -Path $recordPath -Value ([ordered]@{attempt_id=$AttemptId;result_sha256=$resultHash;revision=$state.revision}) }
        Complete-BFRecordedNativeSuccess $state $AttemptId
        return $state
    } finally { $lock.Dispose() }
}

function Accept-BFTask {
    param([string]$ProjectPath,[string]$TaskId)
    $directory=Get-BFTaskDirectory $ProjectPath $TaskId; $lock=Enter-BFLock $directory
    try {
        $state=Read-BFTask $ProjectPath $TaskId; $next=Get-BFNext $state
        if ($next.action -ne 'accept') { throw "BF_BLOCKED: acceptance requires $($next.stage): $($next.action)." }
        Assert-BFVerificationCoverage $state
        Assert-BFCoverageAccepted $state
        $manifest=Get-BFSourceManifest $state
        $gates=@()
        foreach ($stage in @(Get-BFRoute $state | Where-Object { $_ -ne 'acceptance' })) {
            $gate=@($state.evidence | Where-Object { $_.stage -eq $stage })[-1]
            if (-not (Test-BFEvidenceFresh $state $gate $manifest)) { throw "BF_BLOCKED: stale $stage evidence at acceptance." }
            $gates+=,[ordered]@{stage=$stage;attempt_id=$gate.attempt_id;result_sha256=$gate.result_sha256}
        }
        $receipt=[ordered]@{schema_version=1;task_id=$TaskId;intent_revision=$state.intent_revision;mode=$state.request.mode;intent_hash=$state.intent_hash;policy_hash=$state.policy_hash;baseline=$state.baseline;source_manifest=$manifest;gates=$gates;verdict='PASS';scope=if ($state.request.mode -eq 'analysis_only') {'analysis'} else {'source-and-declared-checks'} }
        $id=Get-BFHash $receipt; $receiptPath=Join-Path $directory ('acceptance/'+$id+'.json')
        if (-not (Test-Path -LiteralPath $receiptPath)) { Write-BFJson -Path $receiptPath -Value $receipt }
        if ($state.status -eq 'completed' -and @($state.acceptances).Count -and $state.acceptances[-1].sha256 -eq $id) { return $state }
        $state.acceptances+=,[ordered]@{sha256=$id;path=$receiptPath;verdict='PASS';mode=$state.request.mode;intent_revision=$state.intent_revision}
        $state.stage='acceptance'; $state.status='completed'; $state.blockers=@()
        return Save-BFTask $state $state.revision
    } finally { $lock.Dispose() }
}

function Cancel-BFTask {
    param([string]$ProjectPath,[string]$TaskId)
    $directory=Get-BFTaskDirectory $ProjectPath $TaskId; $lock=Enter-BFLock $directory
    try {
        $state=Read-BFTask $ProjectPath $TaskId
        if ($state.status -ne 'cancelled') {
            $state.status='cancelled'; $state.blockers=@('New dispatch is disabled. An already dispatched action may have changed sources or external state; cancellation is not rollback.')
            $state=Save-BFTask $state $state.revision
        }
        $ownedAttempt=if($state.active_attempt){$state.active_attempt}elseif($null -ne $state.unresolved_effect){$state.unresolved_effect.attempt_id}else{$null}
        if($ownedAttempt){
            $attemptDir=Join-Path $directory ('attempts/'+$ownedAttempt)
            foreach($file in @(Get-ChildItem -LiteralPath $attemptDir -Filter process.json -File -Recurse)) {Stop-BFOwnedProcess (Read-BFJson $file.FullName)}
        }
        return $state
    } finally { $lock.Dispose() }
}

function Resume-BFAttempt {
    param([string]$ProjectPath,[string]$TaskId)
    $state=Read-BFTask $ProjectPath $TaskId
    if (-not $state.active_attempt) {
        foreach($entry in @($state.evidence | Where-Object { $_.stage -eq 'verify' -and $_.outcome -eq 'PASS' })){Complete-BFRecordedNativeSuccess $state $entry.attempt_id}
        return $state
    }
    $directory=Join-Path (Get-BFTaskDirectory $ProjectPath $TaskId) ('attempts/'+$state.active_attempt)
    if (Test-Path -LiteralPath (Join-Path $directory 'result.json')) { return Record-BFAttempt $ProjectPath $TaskId $state.active_attempt }
    $start=Read-BFJson (Join-Path $directory 'start.json')
    $owner=Get-BFValue $start 'controller_process'
    if($null -ne $owner -and $null -ne (Get-BFOwnedProcess $owner)){throw 'BF_BLOCKED: exact attempt controller is still running; concurrent Resume is disabled.'}
    $processFiles=@(Get-ChildItem -LiteralPath $directory -Filter process.json -File -Recurse)
    foreach ($file in $processFiles) {
        $identity=Read-BFJson $file.FullName
        $process=Get-Process -Id $identity.pid -ErrorAction SilentlyContinue
        if ($null -ne $process -and $process.StartTime.ToUniversalTime().ToString('o') -eq $identity.start_time_utc) { throw 'BF_BLOCKED: exact owned process is still running; wait for its durable result, do not dispatch a second worker.' }
    }
    if($start.stage -eq 'verify' -and @($state.request.criteria).Count -eq 1 -and $null -ne (Get-BFValue $state.request.criteria[0] 'native_1c')){
        $raw=Join-Path $directory 'raw'
        $nativeRaw=Join-Path $raw $state.request.criteria[0].id
        if(Test-Path -LiteralPath (Join-Path $nativeRaw 'runtime-success.json')){
            if((Get-BFHash (Get-BFDependencies $state 'verify' $null)) -ne (Get-BFHash $start.dependencies)){throw 'BF_BLOCKED: saved native verification dependencies changed.'}
            $observation=Get-BFNativeSavedObservation $state $state.request.criteria[0] $nativeRaw $state.active_attempt
            $observationsPath=Join-Path $raw 'observations.json'
            $observations=[ordered]@{criteria=@($observation)}
            if(Test-Path -LiteralPath $observationsPath){
                if((Get-BFHash (Read-BFJson $observationsPath)) -ne (Get-BFHash $observations)){throw 'BF_BLOCKED: saved native observations differ from original reports.'}
            }else{Write-BFJson $observationsPath $observations}
            $result=[ordered]@{schema_version=1;status='completed';summary='Recovered original completed native verification without repeating database operations.';payload_json='{}'}
            $state=Invoke-BFStage -Run ([ordered]@{state=$state;attempt=$start;directory=$directory}) -CodexPath $start.executable -RecoveredResult $result
            Complete-BFNativeSavedSuccess $state $nativeRaw $start.attempt_id
            return $state
        }
    }
    $worker=Join-Path $directory 'raw/worker'
    if($start.stage -in @('inspect','spec','code_review','diagnose') -and (Test-Path -LiteralPath (Join-Path $worker 'host-result.json')) -and (Test-Path -LiteralPath (Join-Path $worker 'exit.json')) -and (Test-Path -LiteralPath (Join-Path $worker 'model-result.json'))){
        $exit=Read-BFJson (Join-Path $worker 'exit.json');$hostResult=Read-BFJson (Join-Path $worker 'host-result.json')
        if($exit.exit_code -eq 0 -and $null -eq $exit.stop_reason -and $hostResult.session_id){
            if($start.stage -eq 'diagnose'){Assert-BFRepairFailure $state $state.repair.pending_failure}
            if((Get-BFSourceManifest $state).sha256 -ne $start.source_manifest.sha256){throw 'BF_BLOCKED: source changed after the saved read-only worker; inspect the exact diff.'}
            $result=Read-BFJson (Join-Path $worker 'model-result.json')
            Assert-BFFields $result @('schema_version','status','summary','payload_json') @() 'recovered_worker'
            $run=[ordered]@{state=$state;attempt=$start;directory=$directory}
            return Invoke-BFStage -Run $run -CodexPath $start.executable -RecoveredResult $result
        }
    }
    # Missing receipt is not proof that the operation never began.
    throw 'BF_BLOCKED: attempt has no terminal receipt. Inspect retained raw output and actual source/target state; automatic retry is disabled.'
}
