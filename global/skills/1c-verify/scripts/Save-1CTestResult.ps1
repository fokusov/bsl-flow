[CmdletBinding()]
param(
    [Alias('Receipt','EvidencePath')][string]$ReceiptPath,
    [Alias('JunitPath')][string]$JUnitReportPath,
    [Alias('ExpectedTestsPath','ManifestPath')][Parameter(Mandatory)][string]$ExpectedPath,
    [Alias('Run')][string]$RunId = ([guid]::NewGuid().ToString('N')),
    [Alias('HistoryPath')][string]$HistoryDirectory,
    [Alias('SummaryPath','CurrentPath')][string]$SummaryOutputPath,
    [Alias('EvidenceDirectory','ArtifactsDirectory')][string]$EvidenceOutputDirectory,
    [string[]]$ExpectedSelection,
    [Nullable[int]]$ExpectedTotal,
    [Nullable[DateTime]]$RunStartedAtUtc,
    [string]$TargetPath,
    [string]$SourceManifestPath,
    [ValidateSet('not_started','not_applied','applied_followup_failed','business_record_written_verification_incomplete','unknown')][string]$PostFailureState,
    [string]$NextAction
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'TestEvidence.Common.ps1')

function Get-TERunnerSummary {
    param([object]$Receipt)
    $summary = Get-TEProperty $Receipt @('data','report','summary','summary')
    if ($null -ne (Get-TEProperty $Receipt @('data'))) {
        $data = Get-TEProperty $Receipt @('data')
        $report = Get-TEProperty $data @('report')
        if ($null -ne $report) { $summary = Get-TEProperty $report @('summary') }
        $execution = Get-TEProperty $data @('execution')
        if ($null -eq $summary -and $null -ne $execution) { $summary = Get-TEProperty $execution @('metrics','summary') }
    }
    return $summary
}

function Get-TERunnerCases {
    param([object]$Receipt)
    $data = Get-TEProperty $Receipt @('data')
    $report = if ($null -ne $data) { Get-TEProperty $data @('report') } else { Get-TEProperty $Receipt @('report') }
    $suites = if ($null -ne $report) { Get-TEArray (Get-TEProperty $report @('suites')) } else { @() }
    $cases = @()
    foreach ($suite in $suites) { $cases += @(Get-TEArray (Get-TEProperty $suite @('cases'))) }
    return @($cases)
}

function Get-TEJUnitData {
    param([string]$Path)
    if ([string]::IsNullOrWhiteSpace($Path)) { return $null }
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { return $null }
    if ((Get-Item -LiteralPath $Path).Length -eq 0) { throw "JUnit report is empty: $Path" }
    try {
        $settings = [Xml.XmlReaderSettings]::new()
        $settings.DtdProcessing = [Xml.DtdProcessing]::Prohibit
        $settings.XmlResolver = $null
        $reader = [Xml.XmlReader]::Create([IO.Path]::GetFullPath($Path), $settings)
        try { $xml = [Xml.XmlDocument]::new(); $xml.XmlResolver = $null; $xml.Load($reader) }
        finally { $reader.Dispose() }
    }
    catch { throw "JUnit report is malformed: $Path. $($_.Exception.Message)" }
    $cases = @($xml.SelectNodes('//testcase'))
    if ($null -eq $cases) { $cases = @() }
    $failed = @($cases | Where-Object { $null -ne (Get-TEProperty $_ @('failure')) -or $null -ne (Get-TEProperty $_ @('error')) }).Count
    $skipped = @($cases | Where-Object { $null -ne (Get-TEProperty $_ @('skipped')) }).Count
    $passed = $cases.Count - $failed - $skipped
    $errors = @($cases | Where-Object { $null -ne (Get-TEProperty $_ @('error')) }).Count
    $names = @($cases | ForEach-Object { if ($_.GetAttribute('classname')) { $_.GetAttribute('classname') + '.' + $_.GetAttribute('name') } else { $_.GetAttribute('name') } })
    [pscustomobject]@{ total=$cases.Count; passed=$passed; failed=$failed; skipped=$skipped; errors=$errors; selection=@($names | Sort-Object -Unique) }
}

function Get-TENumber {
    param([object]$Object,[string]$Name)
    $v = Get-TEProperty $Object @($Name)
    if ($null -eq $v -or ($v -isnot [int] -and $v -isnot [long] -and $v -isnot [double])) { return $null }
    if ([double]$v -lt 0 -or [double]$v -ne [math]::Truncate([double]$v)) { return $null }
    return $v
}

function Get-TECaseNames {
    param([AllowEmptyCollection()][object[]]$Cases)
    foreach ($case in $Cases) {
        $name = [string](Get-TEProperty $case @('name'))
        $class = [string](Get-TEProperty $case @('class_name','className'))
        if ($name) { if ($class) { $class + '.' + $name } else { $name } }
    }
}

function Test-TEExpectedSelection {
    param([string[]]$Expected, [object[]]$Cases)
    if ($Expected.Count -eq 0 -or $Cases.Count -eq 0) { return $false }
    $actual = @(Get-TECaseNames $Cases)
    foreach ($expectedName in $Expected) { if (-not ($actual -contains [string]$expectedName)) { return $false } }
    return ($Cases.Count -eq $Expected.Count)
}

function Get-TEFailureState {
    param([object]$Receipt, [bool]$ReceiptPresent)
    # Missing or failed test evidence cannot establish whether data was changed.
    return 'unknown'
}

if ([string]::IsNullOrWhiteSpace($ReceiptPath) -and [string]::IsNullOrWhiteSpace($JUnitReportPath)) { throw 'ReceiptPath or JUnitPath is required.' }
if ([string]::IsNullOrWhiteSpace($RunId) -or $RunId -notmatch '^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$') { throw 'RunId must be a safe filename token (letters, digits, dot, underscore or hyphen).' }
$expectedFull = [System.IO.Path]::GetFullPath($ExpectedPath)
$expected = Get-TEJsonFile $expectedFull
if ([string]::IsNullOrWhiteSpace($HistoryDirectory)) {
    if ($SummaryOutputPath) { $HistoryDirectory = Join-Path (Split-Path -Parent ([System.IO.Path]::GetFullPath($SummaryOutputPath))) 'history' }
    else { $HistoryDirectory = Join-Path (Get-Location) '.bsl-flow\reports\tests\history' }
}
$history = [System.IO.Path]::GetFullPath($HistoryDirectory)
if ([string]::IsNullOrWhiteSpace($SummaryOutputPath)) { $SummaryOutputPath = Join-Path (Split-Path -Parent $history) 'current.json' }
$summaryFull = [System.IO.Path]::GetFullPath($SummaryOutputPath)
if ([string]::IsNullOrWhiteSpace($EvidenceOutputDirectory)) { $EvidenceOutputDirectory = Join-Path (Split-Path -Parent $history) 'raw' }
$evidenceRoot = [System.IO.Path]::GetFullPath($EvidenceOutputDirectory)
$attemptPath = Join-Path $history ($RunId + '.json')
if (Test-Path -LiteralPath $attemptPath) { throw "Attempt already recorded; immutable history refuses overwrite: $attemptPath" }

$receipt = $null
$receiptPresent = $false
$receiptError = $null
$junit = $null
$junitStatus = 'not_requested'
$junitError = $null
try {
    if ($ReceiptPath) {
        $receiptFull = [System.IO.Path]::GetFullPath($ReceiptPath)
        if (Test-Path -LiteralPath $receiptFull -PathType Leaf) {
            $receiptPresent = $true
            try { $receipt = Get-TEJsonFile $receiptFull } catch { $receiptError = $_.Exception.Message }
        }
    }
    if ($JUnitReportPath) {
        $junitFull = [System.IO.Path]::GetFullPath($JUnitReportPath)
        if (Test-Path -LiteralPath $junitFull -PathType Leaf) {
            try { $junit = Get-TEJUnitData $junitFull; $junitStatus = 'original_present' }
            catch { $junitStatus = 'malformed'; $junitError = $_.Exception.Message }
        }
        else { $junitStatus = 'missing_or_deleted' }
    }
    $expectedSelectionValue = if ($ExpectedSelection) { $ExpectedSelection } else { Get-TEProperty $expected @('selection','tests','expected_selection') }
    $expectedSelection = @(Get-TEStringArray $expectedSelectionValue)
    if ($expectedSelection.Count -eq 0) { throw 'Expected selection is required; a result without exact selection cannot be PASS.' }
    $expectedCounts = Get-TEProperty $expected @('counts','summary','expected_counts')
    if ($null -eq $ExpectedTotal) { $ExpectedTotal = Get-TENumber $expected 'total' }
    if ($null -eq $ExpectedTotal) { $ExpectedTotal = Get-TENumber $expectedCounts 'total' }
    if ($null -eq $ExpectedTotal) { $ExpectedTotal = $expectedSelection.Count }
    if ($ExpectedTotal -le 0) { throw 'Expected test count must be greater than zero.' }

    $runnerSummary = if ($receipt) { Get-TERunnerSummary $receipt } else { $null }
    $runnerCases = @(if ($receipt) { Get-TERunnerCases $receipt })
    $runnerCounts = if ($runnerSummary) { [pscustomobject]@{ total=(Get-TENumber $runnerSummary 'total'); passed=(Get-TENumber $runnerSummary 'passed'); failed=(Get-TENumber $runnerSummary 'failed'); skipped=(Get-TENumber $runnerSummary 'skipped'); errors=(Get-TENumber $runnerSummary 'errors') } } else { $null }
    $actual = if ($junit) { $junit } else { $runnerCounts }
    $actualTotal = if ($actual) { Get-TENumber $actual 'total' } else { $null }
    $actualPassed = if ($actual) { Get-TENumber $actual 'passed' } else { $null }
    $actualFailed = if ($actual) { Get-TENumber $actual 'failed' } else { $null }
    $actualErrors = if ($actual) { Get-TENumber $actual 'errors' } else { $null }
    $actualSkipped = if ($actual) { Get-TENumber $actual 'skipped' } else { $null }
    $checks = New-Object System.Collections.ArrayList
    [void]$checks.Add([pscustomobject]@{name='receipt'; status=if($receipt){'pass'}else{'missing'}; message=if($receiptError){$receiptError}else{'Parsed runner envelope or receipt not available.'}})
    [void]$checks.Add([pscustomobject]@{name='junit'; status=if($junit){'pass'}else{if($junitStatus -eq 'malformed'){'malformed'}elseif($JUnitReportPath){'missing_or_deleted'}else{'not_requested'}}; message=if($junitError){$junitError}else{'Original JUnit is retained separately from a parsed receipt.'}})
    $countsOk = $null -ne $actual -and $actualTotal -eq $ExpectedTotal -and $actualTotal -gt 0 -and $actualPassed -eq $actualTotal -and $actualFailed -eq 0 -and $actualErrors -eq 0 -and $actualSkipped -eq 0
    [void]$checks.Add([pscustomobject]@{name='counts'; status=if($countsOk){'pass'}else{'mismatch'}; message="expected total=$ExpectedTotal; observed=$actualTotal; passed=$actualPassed; failed=$actualFailed; errors=$actualErrors; skipped=$actualSkipped"})
    $runnerSelectionOk = if ($runnerCases.Count -gt 0) { Test-TEExpectedSelection $expectedSelection $runnerCases } else { $false }
    $junitSelectionCases = @(if ($junit) { $junit.selection | ForEach-Object { [pscustomobject]@{name=$_} } })
    $junitSelectionOk = if ($junit) { Test-TEExpectedSelection $expectedSelection $junitSelectionCases } else { $false }
    $selectionOk = (($runnerCases.Count -gt 0) -and $runnerSelectionOk) -and ((-not $junit) -or $junitSelectionOk)
    [void]$checks.Add([pscustomobject]@{name='selection'; status=if($selectionOk){'pass'}else{'mismatch'}; expected=@($expectedSelection); observed_runner=@(Get-TEStringArray $runnerCases); observed_junit=@(Get-TEStringArray $junitSelectionCases)})
    $runnerCaseStatusOk = $true
    if ($runnerCases.Count -gt 0) { $runnerCaseStatusOk = (@($runnerCases | Where-Object { [string](Get-TEProperty $_ @('status','state')) -notin @('PASSED','PASS','SUCCESS','succeeded') }).Count -eq 0) -and ($null -ne $runnerCounts) -and ($runnerCases.Count -eq $runnerCounts.total) }
    [void]$checks.Add([pscustomobject]@{name='case_status'; status=if($runnerCaseStatusOk){'pass'}else{'mismatch'}; message='Individual receipt cases must agree with the aggregate PASS counts.'})
    $receiptOk = if ($receipt) { ConvertTo-TEBoolean (Get-TEProperty $receipt @('ok')) } else { $null }
    $dataObject = if ($receipt) { Get-TEProperty $receipt @('data') } else { $null }
    $dataProperty = if ($dataObject) { $dataObject.PSObject.Properties['ok'] } else { $null }
    $dataOk = if ($dataProperty) { ConvertTo-TEBoolean $dataProperty.Value } else { $null }
    $envelopeOk = $receiptOk -eq $true -and ($null -eq $dataProperty -or $dataOk -eq $true)
    [void]$checks.Add([pscustomobject]@{name='envelope'; status=if($envelopeOk){'pass'}else{'failure_or_missing'}; message='Transport success is not sufficient; counts and selection are checked.'})

    $expectedTarget = Get-TEProperty $expected @('target','database','testTarget','test_target')
    if ($null -eq $expectedTarget) { $expectedTarget = $TargetPath }
    if ($expectedTarget -isnot [string] -and $null -ne $expectedTarget) { $expectedTarget = Get-TEProperty $expectedTarget @('path','database_path','databasePath','name') }
    $observedData = if ($receipt) { Get-TEProperty $receipt @('data') } else { $null }
    $observedTarget = if ($observedData) { Get-TEProperty $observedData @('target','database','testTarget','test_target') } else { $null }
    if ($observedTarget -isnot [string] -and $null -ne $observedTarget) { $observedTarget = Get-TEProperty $observedTarget @('path','database_path','databasePath','name') }
    $targetOk = $null -ne $expectedTarget -and $null -ne $observedTarget -and [string]$expectedTarget -eq [string]$observedTarget
    [void]$checks.Add([pscustomobject]@{name='target'; status=if($targetOk){'pass'}else{'missing_or_mismatch'}; expected=$expectedTarget; observed=$observedTarget})
    $expectedSources = Get-TEProperty $expected @('sources','sourceSet','source_set')
    $observedSources = if ($observedData) { Get-TEProperty $observedData @('sources','sourceSet','source_set','effective_sources','effectiveSources') } else { $null }
    $expectedSourcesNormalized = @(Get-TEStringArray $expectedSources)
    $observedSourcesNormalized = @(Get-TEStringArray $observedSources)
    $sourcesOk = $expectedSourcesNormalized.Count -gt 0 -and $observedSourcesNormalized.Count -gt 0 -and (Compare-TEValue $expectedSourcesNormalized $observedSourcesNormalized)
    [void]$checks.Add([pscustomobject]@{name='sources'; status=if($sourcesOk){'pass'}else{'missing_or_mismatch'}; expected=$expectedSourcesNormalized; observed=$observedSourcesNormalized})
    $expectedVersions = Get-TEProperty $expected @('versions','sourceVersions','source_versions')
    $observedVersions = if ($observedData) { Get-TEProperty $observedData @('versions','sourceVersions','source_versions','effective_versions','effectiveVersions') } else { $null }
    $versionsOk = $null -ne $expectedVersions -and $null -ne $observedVersions -and (Compare-TEValue $expectedVersions $observedVersions)
    [void]$checks.Add([pscustomobject]@{name='versions'; status=if($versionsOk){'pass'}else{'missing_or_mismatch'}; expected=$expectedVersions; observed=$observedVersions})
    if ($junit -and $runnerCounts) {
        $receiptCountsMatch = $junit.total -eq $runnerCounts.total -and $junit.passed -eq $runnerCounts.passed -and $junit.failed -eq $runnerCounts.failed -and $junit.errors -eq $runnerCounts.errors -and $junit.skipped -eq $runnerCounts.skipped
        [void]$checks.Add([pscustomobject]@{name='receipt_junit_consistency'; status=if($receiptCountsMatch){'pass'}else{'mismatch'}; message='Receipt and original JUnit counts must describe the same run.'})
    }

    $stale = $false
    $started = if ($null -ne $RunStartedAtUtc) { ([DateTime]$RunStartedAtUtc).ToUniversalTime() } else { Get-TEUtcTimestamp (Get-TEProperty $expected @('started_at_utc','startedAtUtc')) }
    foreach ($source in @($ReceiptPath,$JUnitReportPath)) { if ($source -and $started -and (Test-Path -LiteralPath $source -PathType Leaf)) { if ((Get-Item -LiteralPath $source).LastWriteTimeUtc.AddSeconds(2) -lt $started) { $stale = $true } } }
    if ($ReceiptPath -and $JUnitReportPath -and (Test-Path -LiteralPath $ReceiptPath -PathType Leaf) -and (Test-Path -LiteralPath $JUnitReportPath -PathType Leaf)) {
        $receiptTime = (Get-Item -LiteralPath $ReceiptPath).LastWriteTimeUtc
        $junitTime = (Get-Item -LiteralPath $JUnitReportPath).LastWriteTimeUtc
        if ([math]::Abs(($receiptTime - $junitTime).TotalMinutes) -gt 5) { $stale = $true }
    }
    [void]$checks.Add([pscustomobject]@{name='freshness'; status=if($null -eq $started){'missing'}elseif(-not $stale){'pass'}else{'stale'}; message='Report timestamps are compared with the declared run start.'})
    $blocked = @($checks | Where-Object status -in @('missing','missing_or_deleted','mismatch','failure_or_missing','stale')).Count -gt 0 -or $receiptError
    $freshnessEvidence = $null -ne $started
    $evidenceBlocked = (-not $receiptPresent) -or [bool]$receiptError -or ($junitStatus -in @('missing_or_deleted','malformed')) -or [bool]$junitError -or $stale -or (-not $freshnessEvidence) -or ($null -eq $actual) -or (-not $targetOk) -or (-not $sourcesOk) -or (-not $versionsOk)
    $resultStatus = if (-not $blocked -and -not $evidenceBlocked -and $junitStatus -ne 'missing_or_deleted') { 'PASS' } elseif ($evidenceBlocked) { 'BLOCKED' } else { 'FAIL' }
    $selectionCases = if ($runnerCases.Count -gt 0) { $runnerCases } else { $junitSelectionCases }
    if ($PostFailureState) { $failureState = $PostFailureState } else { $failureState = if($resultStatus -eq 'PASS'){'not_applicable'}else{Get-TEFailureState $receipt $receiptPresent} }
    $attempt = [ordered]@{
        schema_version=1; kind='bsl-flow.test-attempt'; attempt_id=$RunId; recorded_at_utc=[DateTime]::UtcNow.ToString('o'); status=$resultStatus
        started_at_utc=if($started){$started.ToString('o')}else{$null}; completed_at_utc=if($ReceiptPath -and (Test-Path -LiteralPath $ReceiptPath -PathType Leaf)){(Get-Item -LiteralPath $ReceiptPath).LastWriteTimeUtc.ToString('o')}else{[DateTime]::UtcNow.ToString('o')}
        target=[ordered]@{ declared=$expectedTarget; observed=$observedTarget }
        sources=[ordered]@{ declared=$expectedSourcesNormalized; observed=$observedSourcesNormalized; manifest_sha256=if($SourceManifestPath){Get-TESha256 $SourceManifestPath}else{$null}; manifest_unavailable_reason=if($SourceManifestPath){$null}else{'No source hash manifest supplied; versions do not prove byte identity.'} }
        versions=[ordered]@{ declared=$expectedVersions; observed=$observedVersions }
        expected=[ordered]@{ selection=@($expectedSelection); counts=$expectedCounts; total=$ExpectedTotal }
        observed=[ordered]@{ counts=$actual; receipt_envelope=$runnerCounts; junit=$junit; selection=@(Get-TECaseNames $selectionCases) }
        checks=@($checks); failure_state=$failureState; failure_state_provenance=if($PostFailureState){'caller_declaration_not_independently_verified'}else{'no_state_inference'}; next_action=if($NextAction){$NextAction}else{if($resultStatus -eq 'PASS'){'Use this immutable result as evidence; do not repeat business writes.'}else{'Inspect target state and preserved reports read-only; do not retry, load, or create a document automatically.'}}
        provenance=[ordered]@{ receipt_path=$ReceiptPath; junit_path=$JUnitReportPath; expected_path=$expectedFull; receipt_sha256=if($ReceiptPath){Get-TESha256 $ReceiptPath}else{$null}; junit_sha256=if($JUnitReportPath){Get-TESha256 $JUnitReportPath}else{$null}; junit_status=$junitStatus; parsed_receipt_is_not_original_junit=($junitStatus -eq 'missing_or_deleted') }
    }
    if ($ReceiptPath) { $attempt.provenance.receipt_copy = Copy-TEArtifactNoClobber $ReceiptPath (Join-Path (Join-Path $evidenceRoot $RunId) 'receipt.json') }
    if ($JUnitReportPath) { $attempt.provenance.junit_copy = Copy-TEArtifactNoClobber $JUnitReportPath (Join-Path (Join-Path $evidenceRoot $RunId) 'junit.xml') }
    $attempt.provenance.expected_copy = Copy-TEArtifactNoClobber $expectedFull (Join-Path (Join-Path $evidenceRoot $RunId) 'expected.json')
    if ($SourceManifestPath) { $attempt.provenance.source_manifest_copy = Copy-TEArtifactNoClobber $SourceManifestPath (Join-Path (Join-Path $evidenceRoot $RunId) 'source-manifest.json') }
    Write-TEJsonAtomic $attempt $attemptPath

    $invalidHistoryCount = 0
    $attempts = @(Get-ChildItem -LiteralPath $history -Filter '*.json' -File | ForEach-Object {
        try { Get-TEJsonFile $_.FullName }
        catch { $invalidHistoryCount++; $null }
    } | Where-Object { $null -ne $_ })
    $latest = $attempts | Sort-Object @{Expression={ Get-TEUtcTimestamp (Get-TEProperty $_ @('completed_at_utc','recorded_at_utc')) }; Descending=$true}, @{Expression={ [string](Get-TEProperty $_ @('attempt_id')) }; Descending=$true} | Select-Object -First 1
    $current = [ordered]@{ schema_version=1; kind='bsl-flow.test-summary'; generated_at_utc=[DateTime]::UtcNow.ToString('o'); current_attempt_id=if($latest){Get-TEProperty $latest @('attempt_id')}else{$null}; current_status=if($invalidHistoryCount -gt 0){'BLOCKED'}elseif($latest){Get-TEProperty $latest @('status')}else{'BLOCKED'}; attempts_count=$attempts.Count; invalid_history_count=$invalidHistoryCount; history_directory=$history; current_is_derived_from_history=$true; next_action=if($invalidHistoryCount -gt 0){'Repair or quarantine malformed history entries after preserving them; do not infer a current PASS.'}elseif($latest){Get-TEProperty $latest @('next_action')}else{'Record a test attempt with explicit evidence.'} }
    Write-TEJsonAtomic $current $summaryFull
    $attempt
}
catch {
    throw
}
