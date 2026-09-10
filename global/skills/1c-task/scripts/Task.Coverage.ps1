#Requires -Version 7.0
Set-StrictMode -Version Latest

function Test-BFCoverageProperty {
    param($Value,[string]$Name)

    if($Value -is [Collections.IDictionary]){return $Value.Contains($Name)}
    return $null -ne $Value -and $null -ne $Value.PSObject.Properties[$Name]
}

function Get-BFCoverageProperty {
    param($Value,[string]$Name)

    if($Value -is [Collections.IDictionary]){return ,$Value[$Name]}
    return ,$Value.PSObject.Properties[$Name].Value
}

function Assert-BFRequirements {
    param($Request)

    if(-not(Test-BFCoverageProperty $Request 'requirements')){return}
    $requirements=Get-BFCoverageProperty $Request 'requirements'
    if($null -eq $requirements){throw 'BF_INVALID: requirements cannot be null when supplied.'}
    if($requirements -isnot [array] -or $requirements.Count -eq 0){
        throw 'BF_INVALID: requirements must be a nonempty array when supplied.'
    }

    $criterionIds=@($Request.criteria | ForEach-Object {$_.id})
    $requirementIds=[Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
    $mapped=[Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
    foreach($requirement in $requirements){
        Assert-BFFields $requirement @('id','text','criterion_ids') @() 'requirement'
        if($requirement.id -cnotmatch '^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$' -or -not $requirementIds.Add($requirement.id)){
            throw 'BF_INVALID: requirement ids must be safe and unique.'
        }
        Assert-BFText $requirement.text 'requirement.text'
        if($requirement.criterion_ids -isnot [array] -or $requirement.criterion_ids.Count -eq 0){
            throw 'BF_INVALID: every requirement needs nonempty criterion_ids.'
        }
        $local=[Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
        foreach($criterionId in $requirement.criterion_ids){
            if($criterionId -isnot [string] -or -not $local.Add($criterionId)){
                throw 'BF_INVALID: requirement criterion_ids must be unique strings.'
            }
            if($criterionId -cnotin $criterionIds){
                throw 'BF_INVALID: requirement refers to an unknown criterion.'
            }
            [void]$mapped.Add($criterionId)
        }
    }
    foreach($criterionId in $criterionIds){
        if(-not $mapped.Contains($criterionId)){
            throw 'BF_INVALID: supplied requirements must map every criterion.'
        }
    }
    foreach($criterion in $Request.criteria){
        if($criterion.kind -notin @('file_assertion','external_artifact') -and @(Get-BFValue $criterion 'protected_paths' @()).Count -eq 0){
            throw 'BF_INVALID: requirement review needs protected test inputs for executable criteria.'
        }
    }
}

function Assert-BFCoverageAccepted {
    param($State)

    if($State.request.mode -ne 'implement' -or -not(Test-BFCoverageProperty $State.request 'requirements')){return}
    $reviews=@($State.evidence | Where-Object stage -eq 'code_review')
    if($reviews.Count -eq 0 -or -not(Test-BFEvidenceFresh $State $reviews[-1] (Get-BFSourceManifest $State))){
        throw 'BF_BLOCKED: requirement coverage needs a fresh independent code review.'
    }
    $attempt=Join-Path (Get-BFTaskDirectory $State.project_path $State.task_id) ('attempts/'+$reviews[-1].attempt_id)
    $result=Read-BFJson (Join-Path $attempt 'result.json')
    if((Get-BFHash $result) -cne $reviews[-1].result_sha256){throw 'BF_BLOCKED: coverage review result changed.'}
    $proposal=$result.proposal
    if(Test-BFCoverageProperty $proposal 'review'){$proposal=$proposal.review}
    $coverage=Get-BFValue $proposal 'coverage_review'
    if($null -eq $coverage -or $coverage.verdict -cne 'PASS'){throw 'BF_BLOCKED: independently sufficient requirement coverage is missing.'}
    $raw=Join-Path $attempt 'raw'
    $bindingPath=Join-Path $raw 'coverage-review-binding.json'
    if(-not(Test-Path -LiteralPath $bindingPath -PathType Leaf) -or @($reviews[-1].raw_hashes | Where-Object path -eq $bindingPath).Count -ne 1){
        throw 'BF_BLOCKED: registered coverage binding is missing.'
    }
    # Validation only: an existing binding must still match current requirements and files.
    [void](Assert-BFCoverageReview $State $coverage $raw)
}

function Test-BFCoveragePathInScope {
    param([string]$Path,[string[]]$Scopes)

    Assert-BFRelativePath $Path
    if($Path -match '(^|[\\/])\.(git|bsl-flow|bsl-flow-worker)([\\/]|$)'){return $false}
    $candidate=$Path.Replace('\','/').TrimEnd('/')
    foreach($scopeValue in $Scopes){
        $scope=$scopeValue.Replace('\','/').TrimEnd('/')
        if($scope -ceq '.' -or $candidate -ceq $scope -or $candidate.StartsWith($scope+'/',[StringComparison]::Ordinal)){
            return $true
        }
    }
    return $false
}

function Assert-BFCoverageStringArray {
    param($Value,[string]$Name)

    if($Value -isnot [array]){throw "BF_INVALID: $Name must be an array."}
    $seen=[Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
    foreach($item in $Value){
        if($item -isnot [string] -or [string]::IsNullOrWhiteSpace($item) -or -not $seen.Add($item)){
            throw "BF_INVALID: $Name must contain unique nonempty strings."
        }
    }
}

function Assert-BFCoverageReview {
    param($State,$Coverage,[string]$Directory)

    if(-not(Test-BFCoverageProperty $State.request 'requirements')){
        if($null -ne $Coverage){throw 'BF_INVALID: coverage_review requires trusted requirements.'}
        return $null
    }
    $requirements=Get-BFCoverageProperty $State.request 'requirements'
    Assert-BFRequirements $State.request
    Assert-BFFields $Coverage @('verdict','assessments') @() 'coverage_review'
    if($Coverage.verdict -notin @('PASS','BLOCK') -or $Coverage.assessments -isnot [array]){
        throw 'BF_INVALID: invalid coverage review verdict or assessments.'
    }

    $assessmentIds=[Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
    $coveredTests=@{}
    $bindings=[Collections.Generic.List[object]]::new()
    $insufficient=0
    foreach($assessment in $Coverage.assessments){
        Assert-BFFields $assessment @('requirement_id','verdict','criterion_evidence','rationale') @() 'coverage_assessment'
        $requirement=@($requirements | Where-Object {$_.id -ceq $assessment.requirement_id})
        if($requirement.Count -ne 1 -or -not $assessmentIds.Add($assessment.requirement_id)){
            throw 'BF_INVALID: coverage assessments must identify every requirement exactly once.'
        }
        if($assessment.verdict -notin @('SUFFICIENT','INSUFFICIENT')){
            throw 'BF_INVALID: invalid requirement coverage verdict.'
        }
        Assert-BFText $assessment.rationale 'coverage_assessment.rationale'
        if($assessment.verdict -eq 'INSUFFICIENT'){$insufficient++}
        if($assessment.criterion_evidence -isnot [array]){
            throw 'BF_INVALID: criterion_evidence must be an array.'
        }

        $evidenceIds=[Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
        foreach($evidence in $assessment.criterion_evidence){
            Assert-BFFields $evidence @('criterion_id','test_ids','source_paths','observation','evidence') @() 'criterion_evidence'
            if($evidence.criterion_id -cnotin @($requirement[0].criterion_ids) -or -not $evidenceIds.Add($evidence.criterion_id)){
                throw 'BF_INVALID: criterion_evidence must match the trusted requirement mapping exactly.'
            }
            Assert-BFCoverageStringArray $evidence.test_ids 'criterion_evidence.test_ids'
            Assert-BFCoverageStringArray $evidence.source_paths 'criterion_evidence.source_paths'
            Assert-BFText $evidence.observation 'criterion_evidence.observation'
            Assert-BFText $evidence.evidence 'criterion_evidence.evidence'
            $criterion=@($State.request.criteria | Where-Object {$_.id -ceq $evidence.criterion_id})[0]

            $expectedTests=@(Get-BFValue $criterion 'expected_tests' @())
            foreach($testId in $evidence.test_ids){
                if($testId -cnotin $expectedTests){throw 'BF_INVALID: coverage review refers to an undeclared test.'}
                if(-not $coveredTests.ContainsKey($criterion.id)){$coveredTests[$criterion.id]=[Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)}
                [void]$coveredTests[$criterion.id].Add($testId)
            }

            if($criterion.kind -eq 'file_assertion'){
                if($evidence.test_ids.Count -ne 0){throw 'BF_INVALID: file assertions cannot claim test ids.'}
                if($assessment.verdict -eq 'SUFFICIENT' -and (@($evidence.source_paths) -join "`n") -cne $criterion.path){
                    throw 'BF_INVALID: sufficient file assertion coverage must bind its declared path.'
                }
            }else{
                $protected=@(Get-BFValue $criterion 'protected_paths' @())
                if($assessment.verdict -eq 'SUFFICIENT' -and ($evidence.test_ids.Count -eq 0 -or $evidence.source_paths.Count -eq 0)){
                    throw 'BF_INVALID: sufficient executable coverage needs concrete tests and protected source paths.'
                }
                foreach($sourcePath in $evidence.source_paths){
                    if(-not(Test-BFCoveragePathInScope $sourcePath $protected)){
                        throw 'BF_INVALID: coverage source path is outside criterion protected_paths.'
                    }
                }
            }

            foreach($sourcePath in $evidence.source_paths){
                Assert-BFRelativePath $sourcePath
                $absolute=Assert-BFSafePath (Join-Path $State.worker_path $sourcePath)
                if(-not(Test-Path -LiteralPath $absolute -PathType Leaf)){
                    throw 'BF_BLOCKED: coverage source evidence file is missing.'
                }
                $bindings.Add([ordered]@{
                    requirement_id=$assessment.requirement_id
                    criterion_id=$criterion.id
                    path=$sourcePath.Replace('\','/')
                    sha256=Get-BFFileHash $absolute
                })
            }
        }
        if((@($evidenceIds | Sort-Object) -join "`n") -cne (@($requirement[0].criterion_ids | Sort-Object) -join "`n")){
            throw 'BF_INVALID: criterion_evidence must match the trusted requirement mapping exactly.'
        }
    }
    if((@($assessmentIds | Sort-Object) -join "`n") -cne (@($requirements.id | Sort-Object) -join "`n")){
        throw 'BF_INVALID: coverage assessments must identify every requirement exactly once.'
    }
    if(($Coverage.verdict -eq 'PASS' -and $insufficient -ne 0) -or ($Coverage.verdict -eq 'BLOCK' -and $insufficient -eq 0)){
        throw 'BF_INVALID: coverage verdict contradicts requirement assessments.'
    }
    if($Coverage.verdict -eq 'PASS'){
        foreach($criterion in $State.request.criteria){
            $expected=@(Get-BFValue $criterion 'expected_tests' @())
            if($expected.Count -eq 0){continue}
            $actual=if($coveredTests.ContainsKey($criterion.id)){@($coveredTests[$criterion.id])}else{@()}
            if((@($actual | Sort-Object) -join "`n") -cne (@($expected | Sort-Object) -join "`n")){
                throw 'BF_INVALID: PASS coverage must collectively bind every declared test id.'
            }
        }
    }

    $binding=[ordered]@{
        schema_version=1
        kind='bsl-flow.requirement-coverage-binding'
        requirements_sha256=Get-BFHash $requirements
        criteria_sha256=Get-BFHash $State.request.criteria
        coverage_sha256=Get-BFHash $Coverage
        files=@($bindings | Sort-Object requirement_id,criterion_id,path)
    }
    $path=Assert-BFSafePath (Join-Path $Directory 'coverage-review-binding.json')
    if(Test-Path -LiteralPath $path){
        if((Get-BFHash (Read-BFJson $path)) -cne (Get-BFHash $binding)){
            throw 'BF_CONFLICT: coverage evidence binding is stale or conflicting.'
        }
    }else{
        Write-BFJson -Path $path -Value $binding
    }
    return $binding
}
