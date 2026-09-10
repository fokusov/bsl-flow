#Requires -Version 7.0
[CmdletBinding()]
param([string]$PackageRoot)
Set-StrictMode -Version Latest
$ErrorActionPreference='Stop'
$root=if($PackageRoot){[IO.Path]::GetFullPath($PackageRoot)}else{Split-Path -Parent $PSScriptRoot}
. (Join-Path $root 'global/skills/1c-task/scripts/Task.Storage.ps1')
. (Join-Path $root 'global/skills/1c-task/scripts/Task.Contracts.ps1')
$script:checks=0
function Check([bool]$Value,[string]$Message){if(-not $Value){throw $Message};$script:checks++}
function Reject([scriptblock]$Action,[string]$Pattern){try{&$Action;throw 'Expected rejection'}catch{if($_.Exception.Message -notmatch $Pattern){throw}};$script:checks++}
function Clone($Value){return ($Value|ConvertTo-Json -Depth 30|ConvertFrom-Json -Depth 30)}
$tmp=Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-coverage-'+[guid]::NewGuid().ToString('N'))
try{
    $worker=Join-Path $tmp 'worker';$raw=Join-Path $tmp 'raw';[void][IO.Directory]::CreateDirectory((Join-Path $worker 'tests'));[void][IO.Directory]::CreateDirectory($raw)
    [IO.File]::WriteAllText((Join-Path $worker 'tests/a.bsl'),'test a');[IO.File]::WriteAllText((Join-Path $worker 'tests/b.bsl'),'test b');[IO.File]::WriteAllText((Join-Path $worker 'notice.txt'),'ready')
    $criteria=@(
        [pscustomobject]@{id='runtime';kind='integration';expected_tests=@('M.A','M.B');protected_paths=@('tests');observation='runtime'},
        [pscustomobject]@{id='notice';kind='file_assertion';path='notice.txt';observation='notice'}
    )
    $request=[pscustomobject]@{criteria=$criteria;requirements=@(
        [pscustomobject]@{id='positive';text='Positive case';criterion_ids=@('runtime')},
        [pscustomobject]@{id='negative';text='Negative case';criterion_ids=@('runtime')},
        [pscustomobject]@{id='handoff';text='Notice exists';criterion_ids=@('notice')}
    )}
    Assert-BFRequirements $request;Check $true 'Valid trusted mapping was rejected.'
    Check (Test-BFCoveragePathInScope 'tests/a.bsl' @('.')) 'Root protected scope must include source files.'
    Check (-not(Test-BFCoveragePathInScope '.bsl-flow-worker/fake.bsl' @('.'))) 'Generated worker output must not count as protected source evidence.'
    Reject {Test-BFCoveragePathInScope '../escaped.bsl' @('.')} 'unsafe relative path'
    $single=[pscustomobject]@{criteria=@($criteria[0]);requirements=@([pscustomobject]@{id='only';text='One requirement';criterion_ids=@('runtime')})};Assert-BFRequirements $single;Check $true 'Singleton requirements array was collapsed to a scalar.'
    $legacy=[pscustomobject]@{criteria=$criteria};Assert-BFRequirements $legacy;Check $true 'Absent legacy requirements field was rejected.'
    Check ($null -eq (Assert-BFCoverageReview ([pscustomobject]@{request=$legacy;worker_path=$worker}) $null $raw)) 'Absent legacy coverage review was rejected.'
    $empty=[pscustomobject]@{criteria=$criteria;requirements=@()};Reject {Assert-BFRequirements $empty} 'nonempty array'
    $nullRequirements=[pscustomobject]@{criteria=$criteria;requirements=$null};Reject {Assert-BFRequirements $nullRequirements} 'cannot be null'
    $bad=Clone $request;$bad.requirements[0].criterion_ids=@('missing');Reject {Assert-BFRequirements $bad} 'unknown criterion'
    $bad=Clone $request;$bad.requirements=@($bad.requirements|Where-Object{$_.id -ne 'handoff'});Reject {Assert-BFRequirements $bad} 'map every criterion'
    $state=[pscustomobject]@{request=$request;worker_path=$worker}
    $singleCoverage=[pscustomobject]@{verdict='PASS';assessments=@([pscustomobject]@{requirement_id='only';verdict='SUFFICIENT';criterion_evidence=@([pscustomobject]@{criterion_id='runtime';test_ids=@('M.A','M.B');source_paths=@('tests/a.bsl','tests/b.bsl');observation='Both cases are observed';evidence='The two protected tests cover the one mapped requirement.'});rationale='All declared cases are covered.'})}
    $singleRaw=Join-Path $tmp 'single';[void][IO.Directory]::CreateDirectory($singleRaw);$singleBinding=Assert-BFCoverageReview ([pscustomobject]@{request=$single;worker_path=$worker}) $singleCoverage $singleRaw;Check ($singleBinding.files.Count -eq 2) 'Singleton assessment or criterion_evidence array was collapsed.'
    $coverage=[pscustomobject]@{verdict='PASS';assessments=@(
        [pscustomobject]@{requirement_id='positive';verdict='SUFFICIENT';criterion_evidence=@([pscustomobject]@{criterion_id='runtime';test_ids=@('M.A');source_paths=@('tests/a.bsl');observation='A observes positive';evidence='Test source asserts the positive outcome.'});rationale='Covered by A.'},
        [pscustomobject]@{requirement_id='negative';verdict='SUFFICIENT';criterion_evidence=@([pscustomobject]@{criterion_id='runtime';test_ids=@('M.B');source_paths=@('tests/b.bsl');observation='B observes rejection';evidence='Test source asserts the negative outcome.'});rationale='Covered by B.'},
        [pscustomobject]@{requirement_id='handoff';verdict='SUFFICIENT';criterion_evidence=@([pscustomobject]@{criterion_id='notice';test_ids=@();source_paths=@('notice.txt');observation='File contains marker';evidence='Literal criterion reads this file.'});rationale='Covered by file assertion.'}
    )}
    $binding=Assert-BFCoverageReview $state $coverage $raw;Check ($binding.files.Count -eq 3 -and (Test-Path (Join-Path $raw 'coverage-review-binding.json'))) 'Valid coverage did not create exact file bindings.'
    $insufficient=Clone $coverage;$insufficient.verdict='BLOCK';$insufficient.assessments[1].verdict='INSUFFICIENT';$insufficient.assessments[1].criterion_evidence[0].test_ids=@();$insufficient.assessments[1].criterion_evidence[0].source_paths=@();$insufficient.assessments[1].rationale='No negative test exists.';$blockRaw=Join-Path $tmp 'block';[void][IO.Directory]::CreateDirectory($blockRaw);$blockedBinding=Assert-BFCoverageReview $state $insufficient $blockRaw;Check ($blockedBinding.coverage_sha256 -eq (Get-BFHash $insufficient)) 'Well-formed insufficient coverage was rejected.'
    $unknown=Clone $coverage;$unknown.assessments[0].criterion_evidence[0].test_ids=@('M.Unknown');Reject {Assert-BFCoverageReview $state $unknown (Join-Path $tmp 'unknown')} 'undeclared test'
    $partial=Clone $coverage;$partial.assessments[1].criterion_evidence[0].test_ids=@();$partial.assessments[1].criterion_evidence[0].source_paths=@('tests/b.bsl');Reject {Assert-BFCoverageReview $state $partial (Join-Path $tmp 'partial')} 'concrete tests|collectively bind'
    $missing=Clone $coverage;$missing.assessments=@($missing.assessments|Where-Object{$_.requirement_id -ne 'negative'});Reject {Assert-BFCoverageReview $state $missing (Join-Path $tmp 'missing')} 'every requirement exactly once'
    [IO.File]::WriteAllText((Join-Path $worker 'tests/a.bsl'),'changed');Reject {Assert-BFCoverageReview $state $coverage $raw} 'stale or conflicting'
    $stored=Read-BFJson (Join-Path $raw 'coverage-review-binding.json');$storedA=@($stored.files|Where-Object{$_.path -ceq 'tests/a.bsl'})[0];Check ($storedA.sha256 -ne (Get-BFFileHash (Join-Path $worker 'tests/a.bsl'))) 'Stored evidence hash was silently rewritten after source change.'
    Write-Host "Requirement coverage contracts passed: $script:checks checks; model=0; network=0; DB=0."
}finally{
    if(Test-Path -LiteralPath $tmp){Remove-Item -LiteralPath $tmp -Recurse -Force}
}
