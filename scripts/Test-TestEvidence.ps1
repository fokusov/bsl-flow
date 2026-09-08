[CmdletBinding()]
param([string]$PackageRoot)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Assert-TE {
    param([bool]$Condition,[string]$Message)
    if (-not $Condition) { throw "ASSERTION FAILED: $Message" }
}

if ([string]::IsNullOrWhiteSpace($PackageRoot)) { $PackageRoot = Split-Path -Parent $PSScriptRoot }
$package = [System.IO.Path]::GetFullPath($PackageRoot)
$skill = Join-Path $package 'global\skills\1c-verify'
$preflight = Join-Path $skill 'scripts\Test-1CTestPreflight.ps1'
$save = Join-Path $skill 'scripts\Save-1CTestResult.ps1'
$identity = Join-Path $skill 'scripts\Test-ExtensionIdentities.ps1'
$testRoot = Join-Path ([System.IO.Path]::GetTempPath()) ('bsl-flow-evidence-test-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $testRoot -Force | Out-Null
try {
    foreach ($file in @($preflight,$save,$identity)) { Assert-TE (Test-Path -LiteralPath $file -PathType Leaf) "Missing evidence script: $file" }
    $request = [ordered]@{
        runner='v8-runner'; operation='test'; schema='v8-runner-command-envelope'; target='C:\test\bp1';
        sources=@('BP1Tests','BP1InvoiceImport'); selection=@('Pilot.Add','Pilot.Subtract');
        versions=[ordered]@{ platform='8.3.27.2074'; extension='1.0.0.1' }; noBuild=$true; reportPath='reports\pilot.json'; capabilities=@('test_no_build','durable_report')
    }
    $observed = [ordered]@{
        ok=$true; command='test'; phase='preview'; runner='v8-runner'; schema='v8-runner-command-envelope'; noBuild=$true; target='C:\test\bp1'; sources=@('BP1Tests','BP1InvoiceImport');
        selection=@('Pilot.Add','Pilot.Subtract'); versions=[ordered]@{ platform='8.3.27.2074'; extension='1.0.0.1' };
        effective=[ordered]@{ target='C:\test\bp1'; sources=@('BP1Tests','BP1InvoiceImport'); selection=@('Pilot.Add','Pilot.Subtract'); versions=[ordered]@{ platform='8.3.27.2074'; extension='1.0.0.1' }; noBuild=$true; reportPath='reports\pilot.json'; capabilities=@('test_no_build','durable_report') };
        reportPath='reports\pilot.json'; capabilities=@('test_no_build','durable_report'); data=[ordered]@{ ok=$true }; steps=@([ordered]@{name='build';status='skipped';message='build prerequisite explicitly skipped by --no-build'})
    }
    $requestPath = Join-Path $testRoot 'request.json'; $observedPath = Join-Path $testRoot 'preview.json'
    $request | ConvertTo-Json -Depth 10 | Set-Content -LiteralPath $requestPath -Encoding UTF8
    $observed | ConvertTo-Json -Depth 10 | Set-Content -LiteralPath $observedPath -Encoding UTF8
    $preflightOut = Join-Path $testRoot 'preflight.json'
    $pf = & $preflight -RequestPath $requestPath -ObservedPath $observedPath -OutputPath $preflightOut
    Assert-TE ($pf.status -eq 'PASS' -and $pf.safe_to_run -and -not $pf.launch_performed) 'Complete offline preflight did not pass.'
    Assert-TE (Test-Path -LiteralPath $preflightOut -PathType Leaf) 'Preflight result was not retained.'

    $filterOnly = $observed.PSObject.Copy(); $filterOnly.steps = @(); $filterOnly.noBuild = $null; $filterOnly.effective.noBuild = $null
    $filterPath = Join-Path $testRoot 'filter-only.json'; $filterOnly | ConvertTo-Json -Depth 10 | Set-Content -LiteralPath $filterPath -Encoding UTF8
    $blockedPf = & $preflight -RequestPath $requestPath -ObservedPath $filterPath -NoThrow
    Assert-TE ($blockedPf.status -eq 'BLOCKED' -and -not $blockedPf.safe_to_run) 'Module filter without no-build evidence passed preflight.'
    $failedObserved = $observed.PSObject.Copy(); $failedObserved.ok = $false
    $failedPath = Join-Path $testRoot 'failed-preview.json'; $failedObserved | ConvertTo-Json -Depth 12 | Set-Content -LiteralPath $failedPath -Encoding UTF8
    $failedPf = & $preflight -RequestPath $requestPath -ObservedPath $failedPath -NoThrow
    Assert-TE ($failedPf.status -eq 'BLOCKED' -and -not $failedPf.safe_to_run) 'Error preview was mistaken for a usable route.'
    $badRequest = $request.PSObject.Copy(); $badRequest.noBuild = 'false'
    $badRequestPath = Join-Path $testRoot 'bad-request.json'; $badRequest | ConvertTo-Json -Depth 12 | Set-Content -LiteralPath $badRequestPath -Encoding UTF8
    $badDeclarationPf = & $preflight -RequestPath $badRequestPath -ObservedPath $observedPath -NoThrow
    Assert-TE ($badDeclarationPf.status -eq 'BLOCKED') 'String noBuild declaration was treated as a JSON boolean.'

    $receipt = [ordered]@{
        ok=$true; command='test'; data=[ordered]@{ ok=$true; target='C:\test\bp1'; sources=@('BP1Tests','BP1InvoiceImport'); versions=[ordered]@{ platform='8.3.27.2074'; extension='1.0.0.1' }; report=[ordered]@{ summary=[ordered]@{total=2;passed=2;failed=0;skipped=0;errors=0}; suites=@([ordered]@{name='BP1';cases=@(
            [ordered]@{name='Add';class_name='Pilot';status='PASSED'},
            [ordered]@{name='Subtract';class_name='Pilot';status='PASSED'})}) } }; steps=@([ordered]@{name='build';status='skipped';message='--no-build'})
    }
    $receiptPath = Join-Path $testRoot 'receipt.json'; $receipt | ConvertTo-Json -Depth 15 | Set-Content -LiteralPath $receiptPath -Encoding UTF8
    $junitPath = Join-Path $testRoot 'junit.xml'
    @'
<?xml version="1.0" encoding="UTF-8"?>
<testsuites><testsuite name="pilot" tests="2" failures="0" errors="0" skipped="0"><testcase name="Add" classname="Pilot"/><testcase name="Subtract" classname="Pilot"/></testsuite></testsuites>
'@ | Set-Content -LiteralPath $junitPath -Encoding UTF8
    $expected = [ordered]@{selection=@('Pilot.Add','Pilot.Subtract');total=2;target='C:\test\bp1';sources=@('BP1Tests','BP1InvoiceImport');versions=[ordered]@{ platform='8.3.27.2074'; extension='1.0.0.1' };started_at_utc=[DateTime]::UtcNow.AddMinutes(-1).ToString('o')}
    $expectedPath = Join-Path $testRoot 'expected.json'; $expected | ConvertTo-Json -Depth 10 | Set-Content -LiteralPath $expectedPath -Encoding UTF8
    $history = Join-Path $testRoot 'history'; $summary = Join-Path $testRoot 'current.json'; $raw = Join-Path $testRoot 'raw'
    $saved = & $save -ReceiptPath $receiptPath -JUnitPath $junitPath -ExpectedPath $expectedPath -RunId 'new-run' -HistoryDirectory $history -SummaryPath $summary -EvidenceDirectory $raw
    Assert-TE ($saved.status -eq 'PASS') 'Valid receipt/JUnit evidence did not pass.'
    Push-Location $testRoot
    try { $defaults = & $save -ReceiptPath $receiptPath -JUnitPath $junitPath -ExpectedPath $expectedPath -RunId 'default-paths' }
    finally { Pop-Location }
    Assert-TE ($defaults.status -eq 'PASS') 'Omitted optional output paths failed.'
    $prelaunchError = Join-Path $testRoot 'prelaunch-error.json'
    '{"ok":false,"changes":[],"errors":["EXTENSION source-set requires CONFIGURATION"],"diagnostics":{"exit_code":2}}' | Set-Content -LiteralPath $prelaunchError -Encoding UTF8
    Push-Location $testRoot
    try { $noCases = & $save -ReceiptPath $prelaunchError -ExpectedPath $expectedPath -RunId 'prelaunch-no-cases' }
    finally { Pop-Location }
    Assert-TE ($noCases.status -eq 'BLOCKED' -and $noCases.failure_state -eq 'unknown') 'Realistic prelaunch error without cases was not durably blocked.'
    Assert-TE (@(Get-ChildItem -LiteralPath $history -Filter '*.json' -File).Count -eq 1) 'Attempt history was not immutable/persisted.'
    Assert-TE ((Get-Content -Raw $summary | ConvertFrom-Json).current_attempt_id -eq 'new-run') 'Current summary does not point to the accepted attempt.'
    Assert-TE (Test-Path -LiteralPath (Join-Path $raw 'new-run\receipt.json')) 'Raw receipt was not preserved.'

    $duplicateRejected = $false
    try { & $save -ReceiptPath $receiptPath -JUnitPath $junitPath -ExpectedPath $expectedPath -RunId 'new-run' -HistoryDirectory $history -SummaryPath $summary -EvidenceDirectory $raw | Out-Null } catch { $duplicateRejected = $_.Exception.Message -match 'immutable|already recorded' }
    Assert-TE $duplicateRejected 'Duplicate run ID was allowed to overwrite an attempt.'

    $oldReceiptPath = Join-Path $testRoot 'old-receipt.json'; $receipt | ConvertTo-Json -Depth 15 | Set-Content -LiteralPath $oldReceiptPath -Encoding UTF8
    (Get-Item -LiteralPath $oldReceiptPath).LastWriteTimeUtc = [DateTime]::UtcNow.AddMinutes(-20)
    $oldJunitPath = Join-Path $testRoot 'old-junit.xml'; Copy-Item -LiteralPath $junitPath -Destination $oldJunitPath
    $old = & $save -ReceiptPath $oldReceiptPath -JUnitPath $oldJunitPath -ExpectedPath $expectedPath -RunId 'old-late-run' -HistoryDirectory $history -SummaryPath $summary -EvidenceDirectory $raw
    Assert-TE ((Get-Content -Raw $summary | ConvertFrom-Json).current_attempt_id -eq 'new-run') 'Late old attempt overwrote a newer current summary.'
    Assert-TE (@(Get-ChildItem -LiteralPath $history -Filter '*.json' -File).Count -eq 2) 'Late old attempt was not retained in history.'

    $missing = Join-Path $testRoot 'missing-junit.xml'; $missingResult = & $save -ReceiptPath $receiptPath -JUnitPath $missing -ExpectedPath $expectedPath -RunId 'missing-junit' -HistoryDirectory $history -SummaryPath $summary -EvidenceDirectory $raw
    Assert-TE ($missingResult.status -eq 'BLOCKED' -and $missingResult.provenance.junit_status -eq 'missing_or_deleted' -and $missingResult.provenance.parsed_receipt_is_not_original_junit) 'Deleted original JUnit was mistaken for a PASS.'
    Assert-TE (Test-Path -LiteralPath (Join-Path $raw 'missing-junit\receipt.json')) 'Receipt was not retained when JUnit was missing.'
    $badJunit = Join-Path $testRoot 'bad-junit.xml'; '<not-junit>' | Set-Content -LiteralPath $badJunit -Encoding UTF8
    $badJunitResult = & $save -ReceiptPath $receiptPath -JUnitPath $badJunit -ExpectedPath $expectedPath -RunId 'malformed-junit' -HistoryDirectory $history -SummaryPath $summary -EvidenceDirectory $raw
    Assert-TE ($badJunitResult.status -eq 'BLOCKED' -and $badJunitResult.provenance.junit_status -eq 'malformed' -and (Test-Path -LiteralPath (Join-Path $history 'malformed-junit.json'))) 'Malformed JUnit was not durably recorded as BLOCKED.'
    '{broken history' | Set-Content -LiteralPath (Join-Path $history 'corrupt.json') -Encoding UTF8
    $historyResult = & $save -ReceiptPath $receiptPath -JUnitPath $junitPath -ExpectedPath $expectedPath -RunId 'history-check' -HistoryDirectory $history -SummaryPath $summary -EvidenceDirectory $raw
    Assert-TE ((Get-Content -Raw $summary | ConvertFrom-Json).invalid_history_count -eq 1 -and (Get-Content -Raw $summary | ConvertFrom-Json).current_status -eq 'BLOCKED') 'Malformed history was silently ignored by current summary.'
    $noStartExpected = [ordered]@{selection=@('Pilot.Add','Pilot.Subtract');total=2;target='C:\test\bp1';sources=@('BP1Tests','BP1InvoiceImport');versions=[ordered]@{ platform='8.3.27.2074'; extension='1.0.0.1' }}
    $noStartPath = Join-Path $testRoot 'expected-no-start.json'; $noStartExpected | ConvertTo-Json -Depth 10 | Set-Content -LiteralPath $noStartPath -Encoding UTF8
    $noStartResult = & $save -ReceiptPath $receiptPath -JUnitPath $junitPath -ExpectedPath $noStartPath -RunId 'missing-start' -HistoryDirectory $history -SummaryPath $summary -EvidenceDirectory $raw
    Assert-TE ($noStartResult.status -eq 'BLOCKED') 'Result without a declared run start was accepted.'

    $extA = Join-Path $testRoot 'ext-a'; $extB = Join-Path $testRoot 'ext-b'; New-Item -ItemType Directory -Path $extA,$extB -Force | Out-Null
    '<MetaDataObject><CommonModule uuid="11111111-1111-1111-1111-111111111111"/></MetaDataObject>' | Set-Content -LiteralPath (Join-Path $extA 'A.xml') -Encoding UTF8
    '<MetaDataObject><CommonModule uuid="22222222-2222-2222-2222-222222222222"/></MetaDataObject>' | Set-Content -LiteralPath (Join-Path $extB 'B.xml') -Encoding UTF8
    '<ConfigDumpInfo xmlns="http://v8.1c.ru/8.3/xcf/dumpinfo"/>' | Set-Content -LiteralPath (Join-Path $extB 'ConfigDumpInfo.xml') -Encoding UTF8
    $idPass = & $identity -ProjectRoot $testRoot -SourceRoots @('ext-a','ext-b')
    Assert-TE ($idPass.status -eq 'PASS' -and $idPass.owned_uuid_count -eq 2) 'Unique extension identities did not pass.'
    '<MetaDataObject><CommonModule uuid="11111111-1111-1111-1111-111111111111"/></MetaDataObject>' | Set-Content -LiteralPath (Join-Path $extB 'duplicate.xml') -Encoding UTF8
    $idDup = & $identity -ProjectRoot $testRoot -SourceRoots @('ext-a','ext-b') -NoThrow
    Assert-TE ($idDup.status -eq 'BLOCKED' -and $idDup.rewritten_files.Count -eq 0) 'Duplicate identity was accepted or source rewrite was attempted.'
    $empty = Join-Path $testRoot 'empty'; New-Item -ItemType Directory -Path $empty | Out-Null
    $idEmpty = & $identity -ProjectRoot $testRoot -SourceRoots @('empty') -NoThrow
    Assert-TE ($idEmpty.status -eq 'BLOCKED') 'Zero-file identity selection returned PASS.'
    Write-Host 'Test evidence contracts passed; no 1C process, database, retry, load or document creation was used.'
}
finally {
    $resolved = [System.IO.Path]::GetFullPath($testRoot)
    $tempRoot = [System.IO.Path]::GetFullPath([System.IO.Path]::GetTempPath())
    if ($resolved.StartsWith($tempRoot, [StringComparison]::OrdinalIgnoreCase) -and (Split-Path -Leaf $resolved) -like 'bsl-flow-evidence-test-*') { Remove-Item -LiteralPath $resolved -Recurse -Force -ErrorAction SilentlyContinue }
}
