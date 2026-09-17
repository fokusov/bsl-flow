#Requires -Version 7.0
Set-StrictMode -Version Latest
. (Join-Path $PSScriptRoot 'Task.Runtime.ps1')
. (Join-Path $PSScriptRoot 'Task.Execution.ps1')
. (Join-Path $PSScriptRoot 'Task.NativeReuse.ps1')
. (Join-Path $PSScriptRoot 'Task.Coverage.ps1')

function Assert-BFFields {
    param($Value, [string[]]$Required, [string[]]$Optional = @(), [string]$Name = 'object')
    if ($null -eq $Value -or ($Value -isnot [System.Collections.IDictionary] -and $Value -isnot [pscustomobject])) { throw "BF_INVALID: $Name must be an object." }
    # Enumerate properties through the pipeline.  Under StrictMode an empty
    # PSCustomObject has no scalar `.Name` member on its property collection,
    # so `$Value.PSObject.Properties.Name` raises a raw implementation error
    # instead of the closed BF_INVALID contract diagnostic.
    $keys = if ($Value -is [System.Collections.IDictionary]) { @($Value.Keys) } else { @($Value.PSObject.Properties | ForEach-Object { $_.Name }) }
    foreach ($key in $Required) { if ($key -cnotin $keys) { throw "BF_INVALID: $Name.$key is required." } }
    foreach ($key in $keys) { if ($key -cnotin ($Required + $Optional)) { throw "BF_INVALID: unknown field $Name.$key." } }
}

function Get-BFValue {
    param($Value, [string]$Name, $Default = $null)
    if ($Value -is [System.Collections.IDictionary]) { if ($Value.Contains($Name)) { return $Value[$Name] } }
    elseif ($null -ne $Value -and $null -ne $Value.PSObject.Properties[$Name]) { return $Value.$Name }
    return $Default
}

function Assert-BFText {
    param($Value, [string]$Name, [int]$Limit = 262144)
    if ($Value -isnot [string] -or [string]::IsNullOrWhiteSpace($Value) -or $Value.Length -gt $Limit) { throw "BF_INVALID: invalid $Name." }
}

function Assert-BFUuid {
    param([string]$Value)
    if ($Value -cnotmatch '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$') { throw 'BF_INVALID: identity must be a canonical lower-case UUID.' }
}

function Assert-BFRelativePath {
    param([string]$Value)
    if ([string]::IsNullOrWhiteSpace($Value) -or [IO.Path]::IsPathRooted($Value) -or $Value -match '[:*?"<>|\x00-\x1f]' -or $Value -match '(^|[\\/])\.\.([\\/]|$)') { throw "BF_INVALID: unsafe relative path: $Value" }
}

function Assert-BFProvenance {
    param($Value)
    Assert-BFFields $Value @('source', 'reference', 'text') @() 'provenance'
    if ($Value.source -ne 'user') { throw 'BF_INVALID: only a trusted operator can relay user input; worker output is not authorization.' }
    Assert-BFText $Value.reference 'provenance.reference' 2048
    Assert-BFText $Value.text 'provenance.text'
}

function Assert-BFBudget {
    param($Budget)
    Assert-BFFields $Budget @('currency','limit','reservation') @() 'budget'
    if($Budget.currency -cne 'USD'){throw 'BF_INVALID: only a USD budget currency is supported.'}
    foreach($name in @('limit','reservation')){
        $value=$Budget.$name
        if($null -eq $value){continue}
        if($value -isnot [int] -and $value -isnot [long] -and $value -isnot [double] -and $value -isnot [decimal] -and $value -isnot [single]){throw "BF_INVALID: budget.$name must be a number or null."}
        if([double]::IsNaN([double]$value) -or [double]::IsInfinity([double]$value) -or [double]$value -lt 0){throw "BF_INVALID: budget.$name must be a non-negative finite number."}
    }
    if($null -eq $Budget.limit -and [double]$Budget.reservation -ne 0){throw 'BF_INVALID: budget.reservation must be zero when no monetary limit is enforced.'}
}

function Assert-BFCriteria {
    param($Criteria)
    if ($Criteria -isnot [array]) { throw 'BF_INVALID: criteria must be an array.' }
    $ids = @()
    foreach ($criterion in $Criteria) {
        Assert-BFFields $criterion @('id', 'observation', 'kind') @('path', 'contains', 'executable', 'arguments', 'report', 'expected_tests', 'target', 'profile', 'retry_safe', 'protected_paths', 'native_1c') 'criterion'
        $keys=if($criterion -is [System.Collections.IDictionary]){@($criterion.Keys)}else{@($criterion.PSObject.Properties | ForEach-Object { $_.Name })}
        if ($criterion.id -cnotmatch '^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$' -or $criterion.id -in $ids) { throw 'BF_INVALID: criterion ids must be safe and unique.' }
        $ids += $criterion.id
        Assert-BFText $criterion.observation 'criterion.observation'
        if ($null -ne (Get-BFValue $criterion 'retry_safe') -and (Get-BFValue $criterion 'retry_safe') -isnot [bool]) { throw 'BF_INVALID: criterion.retry_safe must be boolean.' }
        if('protected_paths' -cin $keys){
            # Direct property access preserves empty/singleton JSON arrays;
            # the general value helper streams collection members.
            $protected=$criterion.protected_paths
            if($protected -isnot [array] -or $protected.Count -eq 0){throw 'BF_INVALID: protected_paths must be a nonempty array.'}
            foreach($path in $protected){Assert-BFRelativePath $path;if($path -match '(^|[\\/])\.(bsl-flow|bsl-flow-worker|git)([\\/]|$)'){throw 'BF_INVALID: protected test inputs must be source files, not generated/admin paths.'}}
        }
        if ($criterion.kind -notin @('file_assertion', 'static', 'unit', 'integration', 'ui', 'external_artifact')) { throw 'BF_INVALID: unsupported criterion kind.' }
        if ($criterion.kind -eq 'file_assertion') {
            Assert-BFRelativePath (Get-BFValue $criterion 'path')
            Assert-BFText (Get-BFValue $criterion 'contains') 'criterion.contains'
        } elseif ($criterion.kind -ne 'external_artifact') {
            Assert-BFText (Get-BFValue $criterion 'executable') 'criterion.executable'
            if (-not [IO.Path]::IsPathRooted($criterion.executable)) { throw 'BF_INVALID: test executable must be absolute.' }
            if ('arguments' -cnotin $keys -or $criterion.arguments -isnot [array]) { throw 'BF_INVALID: test arguments must be an array.' }
            foreach ($arg in $criterion.arguments) { if ($arg -isnot [string] -or $arg -match '[\x00\r\n]') { throw 'BF_INVALID: test arguments must be single-line strings.' } }
            Assert-BFRelativePath (Get-BFValue $criterion 'report')
            if ($criterion.report -notmatch '^\.bsl-flow-worker[/\\]') { throw 'BF_INVALID: raw test reports belong under .bsl-flow-worker/ in the worktree.' }
            if ('expected_tests' -cnotin $keys -or $criterion.expected_tests -isnot [array] -or @($criterion.expected_tests).Count -eq 0) { throw 'BF_INVALID: exact expected test names are required.' }
            if ($criterion.kind -in @('integration','ui')) { Assert-BFText (Get-BFValue $criterion 'target') 'criterion.target' }
        }
        if ('native_1c' -cin $keys) { Assert-BFNativeCriterion $criterion }
    }
    if (@($Criteria | Where-Object { $null -ne (Get-BFValue $_ 'native_1c') }).Count -gt 1) { throw 'BF_INVALID: the native route supports one target operation per task.' }
}

function Assert-BFRequest {
    param($Request)
    Assert-BFFields $Request @('schema_version','request_id','prompt','mode','analysis_goal','complexity','risk','impact_flags','criteria','provenance','models') @('source_paths','require_spec_review','require_code_review','max_attempts','timeout_seconds','max_source_repairs','requirements','execution_profile','budget') 'request'
    if ($Request.schema_version -ne 1) { throw 'BF_INVALID: unsupported request schema_version.' }
    Assert-BFUuid $Request.request_id
    Assert-BFText $Request.prompt 'prompt'
    Assert-BFProvenance $Request.provenance
    if ($Request.mode -notin @('analysis_only','implement') -or $Request.analysis_goal -notin @('analysis','specification')) { throw 'BF_INVALID: unsupported task mode or analysis goal.' }
    if ($Request.complexity -notin @('S','M','L') -or $Request.risk -notin @('low','medium','high')) { throw 'BF_INVALID: unsupported classification.' }
    Assert-BFImpactFlags $Request.impact_flags
    Assert-BFCriteria $Request.criteria
    Assert-BFRequirements $Request
    if ($Request.mode -eq 'implement' -and @($Request.criteria).Count -eq 0) { throw 'BF_INVALID: implementation requires observable acceptance criteria before dispatch.' }
    Assert-BFFields $Request.models @('worker','worker_effort','reviewer','reviewer_effort') @() 'models'
    $profile=Get-BFValue $Request 'execution_profile'
    if(Test-BFCoverageProperty $Request 'execution_profile'){Assert-BFExecutionProfile $profile}
    $budget=Get-BFValue $Request 'budget'
    if($null -ne $profile){
        if($null -eq $budget){throw 'BF_INVALID: a managed execution profile requires an explicit budget.'}
        Assert-BFBudget $budget
    } elseif(Test-BFCoverageProperty $Request 'budget'){throw 'BF_INVALID: budget is only valid with a managed execution profile.'}
    if($null -ne $profile -and $profile.provider -eq 'opencode'){
        foreach($field in @('worker','reviewer')){if($Request.models.$field -cne 'deepseek/deepseek-v4-flash'){throw "BF_INVALID: invalid OpenCode model $field."}}
        foreach($field in @('worker_effort','reviewer_effort')){if($null -ne $Request.models.$field){throw "BF_INVALID: OpenCode effort $field must be null."}}
    } else {
        foreach ($field in @('worker','reviewer')) { if ($Request.models.$field -notmatch '^[A-Za-z0-9._:-]+$') { throw "BF_INVALID: invalid model $field." } }
        foreach ($field in @('worker_effort','reviewer_effort')) { if ($Request.models.$field -notin @('low','medium','high','xhigh')) { throw "BF_INVALID: invalid effort $field." } }
    }
    foreach ($flag in @('require_spec_review','require_code_review')) { if ($null -ne (Get-BFValue $Request $flag) -and (Get-BFValue $Request $flag) -isnot [bool]) { throw "BF_INVALID: $flag must be boolean." } }
    foreach ($path in @(Get-BFValue $Request 'source_paths' @('.'))) { Assert-BFRelativePath $path }
    $max = Get-BFValue $Request 'max_attempts' 16
    $timeout = Get-BFValue $Request 'timeout_seconds' 1800
    if ($max -isnot [int] -and $max -isnot [long]) { throw 'BF_INVALID: max_attempts must be an integer.' }
    if ($timeout -isnot [int] -and $timeout -isnot [long]) { throw 'BF_INVALID: timeout_seconds must be an integer.' }
    if ($max -lt 1 -or $max -gt 64 -or $timeout -lt 1 -or $timeout -gt 14400) { throw 'BF_INVALID: execution limits out of range.' }
    $repairs = Get-BFValue $Request 'max_source_repairs' 0
    if (($repairs -isnot [int] -and $repairs -isnot [long]) -or $repairs -lt 0 -or $repairs -gt 3) { throw 'BF_INVALID: max_source_repairs must be an integer from 0 to 3.' }
    if($repairs -gt 0){
        foreach($criterion in $Request.criteria){
            if($criterion.kind -in @('static','unit') -and (Get-BFValue $criterion 'retry_safe' $false) -eq $true -and $null -eq (Get-BFValue $criterion 'protected_paths')){throw 'BF_INVALID: repairable command checks require protected_paths covering their test code and fixtures.'}
        }
    }
}

function Assert-BFImpactFlags {
    param($Flags)
    if ($Flags -isnot [array]) { throw 'BF_INVALID: impact_flags must be an array.' }
    foreach ($flag in $Flags) { if ($flag -notin @('permissions','data_migration','data_deletion','posting','data_exchange','form_flow','external_artifact','ambiguous_business_rule')) { throw "BF_INVALID: unknown impact flag $flag." } }
}

function Get-BFRoute {
    param($State)
    $request = $State.request
    $classification = $State.classification
    $high = $classification.risk -eq 'high' -or $classification.complexity -eq 'L'
    $spec = $high -or $classification.complexity -eq 'M' -or $classification.risk -eq 'medium' -or ($request.mode -eq 'analysis_only' -and $request.analysis_goal -eq 'specification')
    $review = $high -or $classification.complexity -eq 'M' -or (Get-BFValue $request 'require_spec_review' $false)
    $rules=Get-BFValue $State 'policy_rules'
    if ($null -ne $rules -and $classification.complexity -eq 'S' -and $rules.s_review_required) { $review=$true }
    if ($review) { $spec = $true }
    $route = @('inspect')
    if ($request.mode -eq 'implement' -or $request.analysis_goal -eq 'specification') {
        if ($spec) { $route += 'spec' }
        if ($review) { $route += 'spec_review' }
    }
    if ($request.mode -eq 'implement') {
        $route += 'implement'
        $native=@($request.criteria | Where-Object { $null -ne (Get-BFValue $_ 'native_1c') }).Count -gt 0
        if ($high -or $native -or (Test-BFCoverageProperty $request 'requirements') -or (Get-BFValue $request 'require_code_review' $false) -or (Get-BFValue (Get-BFValue $State 'repair') 'rounds' 0) -gt 0) { $route += 'code_review' }
        $route += 'verify'
    }
    return @($route + 'acceptance')
}

function Assert-BFState {
    param($State)
    Assert-BFFields $State @('schema_version','task_id','revision','previous_sha256','project_path','worker_path','baseline','request','request_hash','intent_revision','authorization_revision','intent_hash','policy_hash','policy_files','policy_rules','classification','status','stage','active_attempt','unresolved_effect','attempts','evidence','events','question','blockers','acceptances','created_at','updated_at','correction_rounds') @('repair') 'state'
    if ($State.schema_version -ne 1) { throw 'BF_BLOCKED: unsupported state schema.' }
    Assert-BFUuid $State.task_id
    Assert-BFRequest $State.request
    if ($State.request.request_id -ne $State.task_id) { throw 'BF_BLOCKED: request/task identity mismatch.' }
    if ($State.status -notin @('ready','running','needs_input','blocked','failed','completed','cancelled')) { throw 'BF_BLOCKED: invalid state status.' }
    if ($State.stage -notin @('inspect','spec','spec_review','implement','code_review','verify','diagnose','acceptance')) { throw 'BF_BLOCKED: invalid state stage.' }
    $repair=Get-BFValue $State 'repair'
    if ($null -ne $repair) {
        Assert-BFFields $repair @('rounds','pending_failure','last_source_sha256','diagnosis_attempt') @() 'repair'
        if (($repair.rounds -isnot [int] -and $repair.rounds -isnot [long]) -or $repair.rounds -lt 0 -or $repair.rounds -gt 3) { throw 'BF_BLOCKED: invalid source repair count.' }
        foreach($name in @('pending_failure','diagnosis_attempt')) { if($null -ne $repair.$name){Assert-BFUuid $repair.$name} }
    }
    foreach ($field in @('attempts','evidence','events','blockers','acceptances','policy_files')) { if ($State.$field -isnot [array]) { throw "BF_BLOCKED: $field must be an array." } }
}

function New-BFEnvelope {
    param($State, [string]$NextAction = '', [string[]]$Blockers = @(), [string]$NextStage = '')
    $effectiveStatus=$State.status
    $acceptance=$null
    if($State.status -eq 'completed'){
        if($NextAction -eq 'accept' -and @($State.acceptances).Count){
            $candidate=$State.acceptances[-1]
            if((Test-Path -LiteralPath $candidate.path -PathType Leaf) -and (Get-BFHash (Read-BFJson $candidate.path)) -eq $candidate.sha256){$acceptance=$candidate}else{$effectiveStatus='ready'}
        }else{$effectiveStatus='ready'}
    }
    return [ordered]@{ schema_version=1; task_id=$State.task_id; revision=$State.revision; status=$effectiveStatus; stage=$State.stage; next_stage=$NextStage; next_action=$NextAction; blockers=@($Blockers); evidence_refs=@($State.evidence | ForEach-Object { $_.attempt_id }); worker_path=$State.worker_path; acceptance=$acceptance; unresolved_effect=$State.unresolved_effect }
}
