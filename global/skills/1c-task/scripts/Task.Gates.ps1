#Requires -Version 7.0
Set-StrictMode -Version Latest

function Invoke-BFGit {
    param([string]$Root, [string[]]$Arguments)
    $previous = $ErrorActionPreference
    try {
        $ErrorActionPreference = 'Continue'
        $text = & git -c core.hooksPath=NUL -c core.fsmonitor=false -C $Root @Arguments 2>&1 | ForEach-Object { $_.ToString() } | Out-String
        if ($LASTEXITCODE -ne 0) { throw "BF_BLOCKED: Git failed: $($text.Trim())" }
        return $text.TrimEnd("`r", "`n")
    } finally { $ErrorActionPreference = $previous }
}

function Get-BFSourceManifest {
    param($State)
    $root = Assert-BFSafePath $State.worker_path
    if (-not (Test-Path -LiteralPath $root -PathType Container)) { throw 'BF_BLOCKED: worktree is missing.' }
    if ((Invoke-BFGit $root @('rev-parse','HEAD')) -ne $State.baseline) { throw 'BF_BLOCKED: worker HEAD changed; explicit scope reconciliation is required.' }
    # Workers can write the full checkout: bind every source file, even when callers
    # supplied narrower discovery hints. Otherwise edits outside a hint evade gates.
    $paths = @('.')
    if ($paths.Count -eq 0) { throw 'BF_BLOCKED: empty source set.' }
    $files = @{}
    foreach ($relative in $paths) {
        Assert-BFRelativePath $relative
        $path = Assert-BFSafePath (Join-Path $root $relative)
        if (-not (Test-Path -LiteralPath $path)) { throw "BF_BLOCKED: required source path missing: $relative" }
        $queue = New-Object 'System.Collections.Generic.Queue[string]'
        $queue.Enqueue($path)
        while ($queue.Count -gt 0) {
            $item = Get-Item -LiteralPath $queue.Dequeue() -Force
            $rel = $item.FullName.Substring($root.Length).TrimStart('\','/') -replace '\\','/'
            if ($rel -match '(^|/)(\.git|\.bsl-flow|\.bsl-flow-worker)(/|$)') { continue }
            [void](Assert-BFSafePath $item.FullName)
            if ($item.PSIsContainer) {
                foreach ($child in @(Get-ChildItem -LiteralPath $item.FullName -Force)) { $queue.Enqueue($child.FullName) }
            } else { $files[$rel] = [ordered]@{path=$rel; sha256=Get-BFFileHash $item.FullName; deleted=$false} }
        }
    }
    # Include deletions from the baseline, as well as files not tracked by Git.
    $baselinePaths = (Invoke-BFGit $root @('-c','core.quotePath=false','ls-tree','-r','--name-only',$State.baseline)) -split '\r?\n'
    foreach ($path in $baselinePaths) {
        if ($path -match '(^|/)(\.git|\.bsl-flow|\.bsl-flow-worker)(/|$)') { continue }
        $inScope = $false
        foreach ($scope in $paths) { $s = $scope.TrimEnd('/','\') -replace '\\','/'; if ($s -eq '.' -or $path -eq $s -or $path.StartsWith($s + '/', [StringComparison]::Ordinal)) { $inScope = $true } }
        if ($inScope -and -not $files.ContainsKey($path)) { $files[$path] = [ordered]@{path=$path;sha256=$null;deleted=$true} }
    }
    if ($files.Count -eq 0) { throw 'BF_BLOCKED: empty source manifest.' }
    $names = [string[]]@($files.Keys); [Array]::Sort($names, [StringComparer]::Ordinal)
    $entries = @($names | ForEach-Object { $files[$_] })
    return [ordered]@{schema_version=1;baseline=$State.baseline;source_paths=$paths;files=$entries;sha256=Get-BFHash $entries}
}

function Get-BFPolicyFiles {
    param([string]$ProjectPath)
    $skillRoot = Split-Path $PSScriptRoot -Parent
    $skillsRoot = Split-Path $skillRoot -Parent
    $paths = [string[]]@(Get-ChildItem -LiteralPath $skillsRoot -File -Recurse | ForEach-Object { $_.FullName })
    [Array]::Sort($paths, [StringComparer]::Ordinal)
    $files = @($paths | ForEach-Object { [ordered]@{path=$_;sha256=Get-BFFileHash $_} })
    foreach ($relative in @('AGENTS.md','.ai/model-routing.md','bsl-flow.yaml','openspec/config.yaml')) {
        $file = Assert-BFSafePath (Join-Path $ProjectPath $relative)
        $files += [ordered]@{path=$file;sha256=if (Test-Path -LiteralPath $file -PathType Leaf) { Get-BFFileHash $file } else { $null }}
    }
    if (-not [string]::IsNullOrWhiteSpace($env:BSL_FLOW_HOST_PATH)) {
        $hostFile=Assert-BFSafePath $env:BSL_FLOW_HOST_PATH
        if (-not [IO.Path]::IsPathRooted($env:BSL_FLOW_HOST_PATH) -or -not (Test-Path -LiteralPath $hostFile -PathType Leaf)) { throw 'BF_BLOCKED: compiled host identity is unavailable.' }
        $files += [ordered]@{path=$hostFile;sha256=Get-BFFileHash $hostFile}
    }
    return $files
}

function Assert-BFPolicyFresh {
    param($State)
    # Recompute the executing installation's inventory. Checking only saved paths
    # would miss new files or a different controller installation with the old
    # installation still present and unchanged.
    $actual=@(Get-BFPolicyFiles $State.project_path)
    if((Get-BFHash $actual) -ne $State.policy_hash){throw 'BF_BLOCKED: policy/package changed: current controller installation or project policy differs from the registered snapshot. Register an explicit scope update.'}
}

function Get-BFChangePath {
    param($State)
    return Assert-BFSafePath (Join-Path $State.project_path ('openspec/changes/bsl-flow-' + $State.task_id))
}

function Get-BFSpecInputs {
    param($State)
    $change = Get-BFChangePath $State
    $value = [ordered]@{}
    foreach ($name in @('original-task.md','spec.md','design.md')) {
        $path = Assert-BFSafePath (Join-Path $change $name)
        $value[$name] = if (Test-Path -LiteralPath $path -PathType Leaf) { Get-BFFileHash $path } else { $null }
    }
    return $value
}

function Get-BFDependencies {
    param($State, [string]$Stage, $Manifest)
    $inputs = [ordered]@{intent=$State.intent_hash;policy=$State.policy_hash}
    if ($Stage -eq 'inspect') { $inputs.baseline = $State.baseline }
    if ($Stage -ne 'inspect') { $inputs.classification = Get-BFHash $State.classification }
    if ($Stage -in @('spec','spec_review','implement','code_review','verify','diagnose','acceptance')) { $inputs.spec = Get-BFHash (Get-BFSpecInputs $State) }
    if($Stage -eq 'spec_review'){
        $sidecars=[ordered]@{}
        foreach($name in @('review.json','review-reconciliation.json','final-validation.json')){
            $path=Assert-BFSafePath (Join-Path (Get-BFChangePath $State) $name)
            $sidecars[$name]=if(Test-Path -LiteralPath $path -PathType Leaf){Get-BFFileHash $path}else{$null}
        }
        $inputs.review_binding=Get-BFHash $sidecars
    }
    if ($Stage -in @('implement','code_review','verify','diagnose','acceptance')) {
        if ($null -eq $Manifest) { $Manifest = Get-BFSourceManifest $State }
        $inputs.source = $Manifest.sha256
        $inputs.criteria = Get-BFHash $State.request.criteria
        if(Test-BFCoverageProperty $State.request 'requirements'){$inputs.requirements=Get-BFHash $State.request.requirements}
        $native=@($State.request.criteria | Where-Object { $null -ne (Get-BFValue $_ 'native_1c') })
        if ($native.Count) { $inputs.native_platform=Get-BFHash @(Get-BFNativeDependencies $native[0]) }
        $inputs.correction_round = $State.correction_rounds
        if ((Get-BFValue $State.request 'max_source_repairs' 0) -gt 0) {
            $inputs.repair_round=Get-BFValue (Get-BFValue $State 'repair') 'rounds' 0
            $executables=@($State.request.criteria|Where-Object{$_.kind -in @('static','unit')}|ForEach-Object{[ordered]@{path=$_.executable;sha256=Get-BFFileHash (Assert-BFSafePath $_.executable)}})
            $inputs.test_executables=Get-BFHash $executables
        }
    }
    if ($Stage -eq 'diagnose') { $inputs.failure_attempt=(Get-BFValue $State 'repair').pending_failure }
    return $inputs
}

function Test-BFEvidenceFresh {
    param($State, $Evidence, $Manifest)
    if ($Evidence.outcome -ne 'PASS') { return $false }
    $current=Get-BFDependencies $State $Evidence.stage $Manifest
    if((Get-BFHash $Evidence.dependencies) -ne (Get-BFHash $current)){
        # A registered reconciliation is the only legitimate draft-to-final spec
        # transformation. Its own current binding must still validate.
        if($Evidence.stage -ne 'spec'){return $false}
        $current.spec=$Evidence.dependencies.spec
        if((Get-BFHash $Evidence.dependencies) -ne (Get-BFHash $current)){return $false}
        $reviews=@($State.evidence|Where-Object{$_.stage -eq 'spec_review'})
        if($reviews.Count -eq 0 -or -not(Test-BFEvidenceFresh $State $reviews[-1] $Manifest)){return $false}
        $reviewAttempt=Join-Path (Get-BFTaskDirectory $State.project_path $State.task_id) ('attempts/'+$reviews[-1].attempt_id+'/start.json')
        if((Read-BFJson $reviewAttempt).dependencies.spec -ne $Evidence.dependencies.spec){return $false}
    }
    foreach ($raw in $Evidence.raw_hashes) {
        [void](Assert-BFSafePath $raw.path)
        if (-not (Test-Path -LiteralPath $raw.path -PathType Leaf) -or (Get-BFFileHash $raw.path) -ne $raw.sha256) { return $false }
    }
    return $true
}

function Get-BFNext {
    param($State)
    Assert-BFState $State
    Assert-BFPolicyFresh $State
    if ($State.status -eq 'cancelled') { return [ordered]@{stage=$State.stage;action='cancelled';blockers=@('Task cancelled; an explicit user Update is required.')} }
    if ($null -ne $State.unresolved_effect) { return [ordered]@{stage=$State.stage;action='recover';blockers=@('Uncertain effects require an explicit control-read resolution before another dispatch.')} }
    if ($null -ne $State.active_attempt) { return [ordered]@{stage=$State.stage;action='recover';blockers=@('Unfinished attempt must be reconciled before new dispatch.')} }
    if ($null -ne $State.question) { return [ordered]@{stage=$State.stage;action='needs_input';blockers=@($State.question.text)} }
    if ($State.status -eq 'failed') { return [ordered]@{stage=$State.stage;action='failed';blockers=@($State.blockers)} }
    if ($State.status -eq 'blocked') { return [ordered]@{stage=$State.stage;action='blocked';blockers=@($State.blockers)} }
    $repair=Get-BFValue $State 'repair'
    if ($null -ne $repair -and $null -ne $repair.pending_failure) {
        # This helper validates the retained failed attempt, current dependencies and budget.
        Assert-BFRepairFailure $State $repair.pending_failure
        return [ordered]@{stage='diagnose';action='dispatch';blockers=@()}
    }
    $manifest = if ($State.request.mode -eq 'implement') { Get-BFSourceManifest $State } else { $null }
    foreach ($stage in @(Get-BFRoute $State)) {
        if ($stage -eq 'acceptance') { return [ordered]@{stage=$stage;action='accept';blockers=@()} }
        $candidates = @($State.evidence | Where-Object { $_.stage -eq $stage })
        if ($candidates.Count -eq 0 -or -not (Test-BFEvidenceFresh $State $candidates[-1] $manifest)) { return [ordered]@{stage=$stage;action='dispatch';blockers=@()} }
    }
}

function Assert-BFVerificationCoverage {
    param($State)
    if ($State.request.mode -ne 'implement') { return }
    $kinds = @($State.request.criteria | ForEach-Object { $_.kind })
    foreach ($flag in $State.classification.impact_flags) {
        $required = switch ($flag) { 'posting' {'integration'} 'data_exchange' {'integration'} 'permissions' {'integration'} 'data_migration' {'integration'} 'data_deletion' {'integration'} 'form_flow' {'ui'} 'external_artifact' {'external_artifact'} default {''} }
        if ($required -and $required -notin $kinds) { throw "BF_BLOCKED: impact $flag requires $required evidence selected from the original requirements." }
    }
}

function Test-BFJUnit {
    param([string]$Path, [string[]]$ExpectedTests, [switch]$AllowFailure)
    [void](Assert-BFSafePath $Path)
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { throw 'BF_BLOCKED: original JUnit report missing.' }
    $settings = New-Object System.Xml.XmlReaderSettings
    $settings.DtdProcessing = [System.Xml.DtdProcessing]::Prohibit
    $settings.XmlResolver = $null
    $reader = [System.Xml.XmlReader]::Create($Path, $settings)
    try { $xml = New-Object System.Xml.XmlDocument; $xml.XmlResolver=$null; $xml.Load($reader) } catch { throw 'BF_BLOCKED: malformed or unsafe JUnit report.' } finally { $reader.Dispose() }
    if ($xml.DocumentElement.Name -notin @('testsuite','testsuites')) { throw 'BF_BLOCKED: unsupported JUnit root.' }
    $cases = @($xml.SelectNodes('//testcase'))
    $actual = @($cases | ForEach-Object { $_.GetAttribute('name') })
    if ($cases.Count -eq 0 -or $actual.Count -ne @($actual | Select-Object -Unique).Count -or (@($actual | Sort-Object) -join "`n") -cne (@($ExpectedTests | Sort-Object) -join "`n")) { throw 'BF_BLOCKED: JUnit selection does not match exact expected test names.' }
    if (@($xml.SelectNodes('//skipped')).Count -gt 0) { throw 'BF_BLOCKED: required tests were skipped.' }
    $failed=@($xml.SelectNodes('//failure|//error')).Count -gt 0
    foreach ($suite in @($xml.SelectNodes('//testsuite|//testsuites'))) {
        foreach ($name in @('tests','failures','errors','skipped','disabled')) {
            if(-not $suite.HasAttribute($name)){continue}
            $value=$suite.GetAttribute($name)
            if($value -cnotmatch '^(0|[1-9][0-9]*)$'){throw "BF_BLOCKED: invalid JUnit $name aggregate."}
            $observed=switch($name){'tests'{@($suite.SelectNodes('.//testcase')).Count} 'failures'{@($suite.SelectNodes('.//testcase[failure]')).Count} 'errors'{@($suite.SelectNodes('.//testcase[error]')).Count} default{0}}
            if($value -cne [string]$observed){throw "BF_BLOCKED: inconsistent JUnit $name aggregate."}
        }
    }
    if($failed -and -not $AllowFailure){throw 'BF_FAIL: required tests failed.'}
    return [ordered]@{tests=$actual;sha256=Get-BFFileHash $Path;outcome=if($failed){'FAIL'}else{'PASS'}}
}

function Assert-BFCodeReview {
    param($Review)
    Assert-BFFields $Review @('verdict','findings') @('coverage_review') 'code_review'
    if ($Review.verdict -notin @('PASS','REVISE','BLOCK') -or $Review.findings -isnot [array]) { throw 'BF_INVALID: invalid code review.' }
    if ($Review.verdict -ne 'PASS' -and @($Review.findings).Count -eq 0) { throw 'BF_INVALID: non-PASS review requires addressable findings.' }
    if ($Review.verdict -eq 'PASS' -and @($Review.findings).Count -ne 0) { throw 'BF_INVALID: PASS with unresolved findings is contradictory.' }
    $ids = @()
    foreach ($finding in $Review.findings) {
        Assert-BFFields $finding @('id','severity','file','line','scenario','evidence') @() 'finding'
        foreach ($key in @('id','file','scenario','evidence')) { Assert-BFText $finding.$key "finding.$key" }
        Assert-BFRelativePath $finding.file
        if ($finding.id -in $ids -or $finding.severity -notin @('critical','high','medium','low') -or $finding.line -lt 1) { throw 'BF_INVALID: invalid finding identity or location.' }
        $ids += $finding.id
    }
}
