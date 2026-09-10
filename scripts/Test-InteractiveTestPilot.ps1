#Requires -Version 7.0
[CmdletBinding()]
param([string]$PackageRoot)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
function Assert-Pilot([bool]$Condition,[string]$Message){if(-not$Condition){throw "ASSERTION FAILED: $Message"}}
if([string]::IsNullOrWhiteSpace($PackageRoot)){$PackageRoot=Split-Path -Parent $PSScriptRoot}
$save=Join-Path ([IO.Path]::GetFullPath($PackageRoot)) 'global\skills\1c-init-project\scripts\Save-1CInteractiveTestPilot.ps1'
$root=Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-interactive-pilot-test-'+[guid]::NewGuid().ToString('N'))
try{
    $db=Join-Path $root 'db';$reportDir=Join-Path $root 'project\.bsl-flow\reports\test-setup';New-Item -ItemType Directory -Path $db,$reportDir -Force|Out-Null
    $current=Join-Path $reportDir 'current.json'
    $baseObserved=[DateTimeOffset]::UtcNow
    $setup=[ordered]@{schema_version=1;checked_at_utc=[DateTimeOffset]::UtcNow.AddMinutes(-5).ToString('o');project_id='fixture';target=[ordered]@{development_db=$db;test_db=$db};inventory=[ordered]@{runtime_verified=$false};providers=[ordered]@{yaxunit=[ordered]@{state='files_found';enabled=$false;installed_in_database='unknown';pilot='not_run';blocked_reason='pilot_required';next_action='pilot'};vanessa=[ordered]@{state='files_found';enabled=$false;installed_in_database='not_applicable_external_runner';pilot='not_run';blocked_reason='pilot_required';next_action='pilot'}};runtime_actions='not_run';status='blocked';blocked_reason='pilot required'}
    $setup|ConvertTo-Json -Depth 12|Set-Content -LiteralPath $current -Encoding UTF8
    $yaxObs=Join-Path $root 'yaxunit.json'
    [ordered]@{schema_version=1;observed_at_utc=$baseObserved.ToString('o');provider='yaxunit';target=$db;runner_version='25.12';selection=@('Pilot.Add');counts=[ordered]@{total=1;passed=1;failed=0;errors=0;skipped=0};result='PASS';installation=[ordered]@{state='installed_extension';version='25.12';active=$true};test_client_connection_observed=$false;evidence=[ordered]@{kind='interactive_ui_observation';summary='One selected test passed in the target FILE base.';fresh_durable_report=$false}}|ConvertTo-Json -Depth 10|Set-Content -LiteralPath $yaxObs -Encoding UTF8
    $first=& $save -SetupReportPath $current -ObservationPath $yaxObs -RunId 'yax-001'
    $afterYax=Get-Content -LiteralPath $current -Raw|ConvertFrom-Json
    Assert-Pilot ($first.status-eq'PASS' -and $afterYax.status-eq'pilot_passed') 'Passing interactive pilot did not update setup status.'
    Assert-Pilot ($afterYax.providers.yaxunit.state-eq'pilot_passed' -and $afterYax.providers.yaxunit.enabled) 'YAxUnit provider was not promoted to pilot_passed.'
    Assert-Pilot (-not$afterYax.providers.yaxunit.capabilities.unattended_run -and $afterYax.automation_readiness.status-eq'blocked') 'Interactive evidence incorrectly proved unattended readiness.'
    Assert-Pilot (Test-Path -LiteralPath (Join-Path $reportDir 'history\yax-001.json')) 'Immutable pilot history was not written.'
    $currentHash=(Get-FileHash -LiteralPath $current).Hash
    $replay=& $save -SetupReportPath $current -ObservationPath $yaxObs -RunId 'yax-001'
    Assert-Pilot ($replay.idempotent_replay -and (Get-FileHash -LiteralPath $current).Hash-eq$currentHash) 'Idempotent replay rewrote current state.'
    Assert-Pilot ((Get-Content -LiteralPath (Join-Path $reportDir 'history\yax-001.json') -Raw|ConvertFrom-Json).observation_sha256-eq(Get-FileHash -LiteralPath $yaxObs).Hash.ToLowerInvariant()) 'Idempotent replay changed pilot evidence.'

    $wrong=Get-Content -LiteralPath $yaxObs -Raw|ConvertFrom-Json;$wrong.target=Join-Path $root 'other';$wrongPath=Join-Path $root 'wrong.json';$wrong|ConvertTo-Json -Depth 10|Set-Content -LiteralPath $wrongPath -Encoding UTF8
    $wrongRejected=$false;try{& $save -SetupReportPath $current -ObservationPath $wrongPath -RunId 'wrong-target'|Out-Null}catch{$wrongRejected=$_.Exception.Message-match'does not match setup target'}
    Assert-Pilot $wrongRejected 'Mismatched target was accepted.'
    Assert-Pilot (-not(Test-Path -LiteralPath (Join-Path $reportDir 'history\wrong-target.json'))) 'Rejected target created history.'

    $vaObs=Join-Path $root 'vanessa.json'
    [ordered]@{schema_version=1;observed_at_utc=$baseObserved.AddSeconds(2).ToString('o');provider='vanessa';target=$db;runner_version='1.2.043.28';selection=@('pilot.feature::Arithmetic');counts=[ordered]@{total=1;passed=1;failed=0;errors=0;skipped=0};result='PASS';installation=[ordered]@{state='external_runner_loaded';version='1.2.043.28';active=$true};test_client_connection_observed=$false;evidence=[ordered]@{kind='interactive_ui_observation';summary='One local scenario and its steps completed without errors.';fresh_durable_report=$false}}|ConvertTo-Json -Depth 10|Set-Content -LiteralPath $vaObs -Encoding UTF8
    & $save -SetupReportPath $current -ObservationPath $vaObs -RunId 'va-001'|Out-Null
    $afterVa=Get-Content -LiteralPath $current -Raw|ConvertFrom-Json
    Assert-Pilot ($afterVa.providers.vanessa.capabilities.scenario_runner -and -not$afterVa.providers.vanessa.capabilities.test_client) 'Vanessa runner and TestClient capabilities were conflated.'
    Assert-Pilot ($afterVa.interactive_pilot.latest_run_id-eq'va-001') ("Latest pilot pointer was not updated: " + ($afterVa.interactive_pilot|ConvertTo-Json -Compress))

    $oldFailure=Get-Content -LiteralPath $yaxObs -Raw|ConvertFrom-Json;$oldFailure.observed_at_utc=$baseObserved.AddDays(-1).ToString('o');$oldFailure.result='FAIL';$oldFailure.counts.passed=0;$oldFailure.counts.failed=1;$oldFailure.evidence.summary='An older failed attempt arrived after the newer passing attempt.';$oldPath=Join-Path $root 'old-failure.json';$oldFailure|ConvertTo-Json -Depth 10|Set-Content -LiteralPath $oldPath -Encoding UTF8
    $oldResult=& $save -SetupReportPath $current -ObservationPath $oldPath -RunId 'yax-old'
    $afterOld=Get-Content -LiteralPath $current -Raw|ConvertFrom-Json
    Assert-Pilot ($oldResult.recorded_not_current -and $afterOld.providers.yaxunit.state-eq'pilot_passed') 'Late older evidence replaced a newer provider result.'
    Assert-Pilot (Test-Path -LiteralPath (Join-Path $reportDir 'history\yax-old.json')) 'Late older evidence was not preserved in history.'

    $badPass=Get-Content -LiteralPath $yaxObs -Raw|ConvertFrom-Json;$badPass.counts.passed=0;$badPass.counts.failed=1;$badPath=Join-Path $root 'bad-pass.json';$badPass|ConvertTo-Json -Depth 10|Set-Content -LiteralPath $badPath -Encoding UTF8
    $badRejected=$false;try{& $save -SetupReportPath $current -ObservationPath $badPath -RunId 'bad-pass'|Out-Null}catch{$badRejected=$_.Exception.Message-match'PASS requires'}
    Assert-Pilot $badRejected 'Contradictory PASS counts were accepted.'
    Write-Host 'Interactive test-pilot evidence contracts passed; no 1C process was started.'
}
finally{
    $resolved=[IO.Path]::GetFullPath($root);$temp=[IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd([char]'\',[char]'/')+[IO.Path]::DirectorySeparatorChar
    if($resolved.StartsWith($temp,[StringComparison]::OrdinalIgnoreCase)-and(Split-Path -Leaf $resolved)-like'bsl-flow-interactive-pilot-test-*'){Remove-Item -LiteralPath $resolved -Recurse -Force}
}
