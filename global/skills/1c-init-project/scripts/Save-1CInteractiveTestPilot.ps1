#Requires -Version 7.0
[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$SetupReportPath,
    [Parameter(Mandatory)][string]$ObservationPath,
    [Parameter(Mandatory)][string]$RunId,
    [string]$HistoryDirectory
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Get-PropertyValue {
    param([object]$Object, [Parameter(Mandatory)][string]$Name)
    if ($null -eq $Object) { return $null }
    if ($Object -is [Collections.IDictionary] -and $Object.Contains($Name)) { return $Object[$Name] }
    $property = $Object.PSObject.Properties[$Name]
    if ($null -eq $property) { return $null }
    return $property.Value
}

function Assert-KnownProperties {
    param([object]$Object, [string[]]$Allowed, [Parameter(Mandatory)][string]$Context)
    foreach ($property in $Object.PSObject.Properties) {
        if ($Allowed -notcontains $property.Name) { throw "Unknown $Context property: $($property.Name)" }
    }
}

function Resolve-AbsolutePath {
    param([Parameter(Mandatory)][string]$Path, [Parameter(Mandatory)][string]$Name)
    if ($Path -notmatch '^(?:[A-Za-z]:[\\/]|[\\/]{2}[^\\/]+[\\/][^\\/]+(?:[\\/]|$))') { throw "$Name must be an absolute filesystem path." }
    return [IO.Path]::GetFullPath($Path)
}

function Get-Sha256 {
    param([Parameter(Mandatory)][string]$Path)
    return (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
}

function Get-Integer {
    param([object]$Object, [Parameter(Mandatory)][string]$Name)
    $value = Get-PropertyValue $Object $Name
    if ($null -eq $value -or $value -is [bool] -or ($value -isnot [int] -and $value -isnot [long])) { throw "counts.$Name must be a JSON integer." }
    if ([long]$value -lt 0) { throw "counts.$Name must not be negative." }
    return [long]$value
}

function Set-PropertyValue {
    param([object]$Object, [Parameter(Mandatory)][string]$Name, [AllowNull()][object]$Value)
    $Object | Add-Member -NotePropertyName $Name -NotePropertyValue $Value -Force
}

function Write-JsonAtomic {
    param([Parameter(Mandatory)][object]$Value, [Parameter(Mandatory)][string]$Path)
    $directory = Split-Path -Parent $Path
    [IO.Directory]::CreateDirectory($directory) | Out-Null
    $temporary = Join-Path $directory ('.' + [IO.Path]::GetFileName($Path) + '.' + [guid]::NewGuid().ToString('N') + '.tmp')
    try {
        [IO.File]::WriteAllText($temporary, ($Value | ConvertTo-Json -Depth 24) + [Environment]::NewLine, [Text.UTF8Encoding]::new($false))
        Move-Item -LiteralPath $temporary -Destination $Path -Force
    }
    finally {
        if (Test-Path -LiteralPath $temporary) { Remove-Item -LiteralPath $temporary -Force }
    }
}

function Get-LatestProviderPilot {
    param([Parameter(Mandatory)][object]$Providers)
    $latest = $null
    foreach ($providerProperty in $Providers.PSObject.Properties) {
        $candidate = Get-PropertyValue $providerProperty.Value 'latest_pilot'
        if ($null -eq $candidate) { continue }
        $candidateTime = [DateTimeOffset]::MinValue
        if ([DateTimeOffset]::TryParse([string](Get-PropertyValue $candidate 'observed_at_utc'), [Globalization.CultureInfo]::InvariantCulture, [Globalization.DateTimeStyles]::RoundtripKind, [ref]$candidateTime)) {
            $candidateRecord = [pscustomobject]@{provider=$providerProperty.Name;run_id=[string](Get-PropertyValue $candidate 'run_id');result=[string](Get-PropertyValue $candidate 'result');observed_at_utc=$candidateTime}
            if ($null -eq $latest -or $candidateRecord.observed_at_utc -gt $latest.observed_at_utc -or ($candidateRecord.observed_at_utc -eq $latest.observed_at_utc -and [string]::CompareOrdinal($candidateRecord.run_id,$latest.run_id) -gt 0)) { $latest = $candidateRecord }
        }
    }
    return $latest
}

if ($RunId -notmatch '^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$') { throw 'RunId must be a safe filename token.' }
$setupFull = Resolve-AbsolutePath $SetupReportPath 'SetupReportPath'
$observationFull = Resolve-AbsolutePath $ObservationPath 'ObservationPath'
if (-not (Test-Path -LiteralPath $setupFull -PathType Leaf)) { throw "Setup report does not exist: $setupFull" }
if (-not (Test-Path -LiteralPath $observationFull -PathType Leaf)) { throw "Observation does not exist: $observationFull" }

try { $setup = Get-Content -LiteralPath $setupFull -Raw -Encoding UTF8 | ConvertFrom-Json }
catch { throw "Setup report is invalid JSON: $setupFull. $($_.Exception.Message)" }
try { $observation = Get-Content -LiteralPath $observationFull -Raw -Encoding UTF8 | ConvertFrom-Json }
catch { throw "Observation is invalid JSON: $observationFull. $($_.Exception.Message)" }

Assert-KnownProperties $observation @('schema_version','observed_at_utc','provider','target','runner_version','selection','counts','result','installation','test_client_connection_observed','evidence') 'observation'
$schemaVersion = Get-PropertyValue $observation 'schema_version'
if ($schemaVersion -ne 1 -or ($schemaVersion -isnot [int] -and $schemaVersion -isnot [long])) { throw 'Observation schema_version must be JSON integer 1.' }
$provider = [string](Get-PropertyValue $observation 'provider')
if ($provider -notin @('yaxunit','vanessa')) { throw 'Observation provider must be yaxunit or vanessa.' }
$result = [string](Get-PropertyValue $observation 'result')
if ($result -notin @('PASS','FAIL','BLOCKED')) { throw 'Observation result must be PASS, FAIL or BLOCKED.' }
$runnerVersion = [string](Get-PropertyValue $observation 'runner_version')
if ([string]::IsNullOrWhiteSpace($runnerVersion) -or $runnerVersion.Length -gt 128) { throw 'runner_version is required and must be at most 128 characters.' }

$observedText = [string](Get-PropertyValue $observation 'observed_at_utc')
$observedAt = [DateTimeOffset]::MinValue
if (-not [DateTimeOffset]::TryParse($observedText, [Globalization.CultureInfo]::InvariantCulture, [Globalization.DateTimeStyles]::RoundtripKind, [ref]$observedAt)) { throw 'observed_at_utc must be an RFC 3339 timestamp.' }
if ($observedAt -gt [DateTimeOffset]::UtcNow.AddMinutes(5)) { throw 'observed_at_utc is implausibly in the future.' }

$expectedTarget = Resolve-AbsolutePath ([string](Get-PropertyValue (Get-PropertyValue $setup 'target') 'test_db')) 'setup target.test_db'
$observedTarget = Resolve-AbsolutePath ([string](Get-PropertyValue $observation 'target')) 'observation target'
if (-not $expectedTarget.Equals($observedTarget, [StringComparison]::OrdinalIgnoreCase)) { throw "Observation target does not match setup target: $observedTarget" }

$selection = @((Get-PropertyValue $observation 'selection') | ForEach-Object { [string]$_ })
if ($selection.Count -eq 0 -or @($selection | Where-Object { [string]::IsNullOrWhiteSpace($_) }).Count -gt 0) { throw 'Observation selection must contain at least one non-empty test identity.' }
if (@($selection | Select-Object -Unique).Count -ne $selection.Count) { throw 'Observation selection contains duplicate test identities.' }

$counts = Get-PropertyValue $observation 'counts'
if ($null -eq $counts) { throw 'Observation counts are required.' }
Assert-KnownProperties $counts @('total','passed','failed','errors','skipped') 'counts'
$total = Get-Integer $counts 'total'; $passed = Get-Integer $counts 'passed'; $failed = Get-Integer $counts 'failed'; $errors = Get-Integer $counts 'errors'; $skipped = Get-Integer $counts 'skipped'
if ($total -le 0 -or ($passed + $failed + $errors + $skipped) -ne $total) { throw 'Observation counts must describe one non-empty, internally consistent selection.' }
if ($result -eq 'PASS' -and ($passed -ne $total -or $failed -ne 0 -or $errors -ne 0 -or $skipped -ne 0)) { throw 'PASS requires every selected test to pass without failures, errors or skips.' }
if ($result -eq 'FAIL' -and ($failed + $errors) -eq 0) { throw 'FAIL requires at least one failed or errored test.' }
if ($result -eq 'BLOCKED' -and ($passed + $failed + $errors) -gt 0) { throw 'BLOCKED cannot contain executed pass/fail/error counts.' }

$installation = Get-PropertyValue $observation 'installation'
if ($null -eq $installation) { throw 'Observation installation is required.' }
Assert-KnownProperties $installation @('state','version','active') 'installation'
$installationState = [string](Get-PropertyValue $installation 'state')
$expectedInstallationState = if ($provider -eq 'yaxunit') { 'installed_extension' } else { 'external_runner_loaded' }
if ($installationState -ne $expectedInstallationState) { throw "$provider requires installation.state=$expectedInstallationState." }
$installationVersion = [string](Get-PropertyValue $installation 'version')
if ([string]::IsNullOrWhiteSpace($installationVersion)) { throw 'installation.version is required.' }
$installationActive = Get-PropertyValue $installation 'active'
if ($installationActive -isnot [bool] -or -not $installationActive) { throw 'installation.active must be JSON true for a recorded pilot.' }

$testClientObserved = Get-PropertyValue $observation 'test_client_connection_observed'
if ($null -eq $testClientObserved) { $testClientObserved = $false }
if ($testClientObserved -isnot [bool]) { throw 'test_client_connection_observed must be a JSON boolean.' }
if ($provider -eq 'yaxunit' -and $testClientObserved) { throw 'YAxUnit pilot cannot claim a Vanessa TestClient connection.' }

$evidence = Get-PropertyValue $observation 'evidence'
if ($null -eq $evidence) { throw 'Observation evidence is required.' }
Assert-KnownProperties $evidence @('kind','summary','fresh_durable_report') 'evidence'
if ([string](Get-PropertyValue $evidence 'kind') -ne 'interactive_ui_observation') { throw 'This helper accepts only evidence.kind=interactive_ui_observation.' }
$freshDurable = Get-PropertyValue $evidence 'fresh_durable_report'
if ($freshDurable -isnot [bool] -or $freshDurable) { throw 'Interactive pilot evidence must set fresh_durable_report to JSON false; use Save-1CTestResult.ps1 for durable evidence.' }
$summary = [string](Get-PropertyValue $evidence 'summary')
if ([string]::IsNullOrWhiteSpace($summary) -or $summary.Length -gt 500) { throw 'evidence.summary is required and must be at most 500 characters.' }

$providers = Get-PropertyValue $setup 'providers'
$providerState = Get-PropertyValue $providers $provider
if ($null -eq $providerState) { throw "Setup report does not contain provider: $provider" }
$setupHashBefore = Get-Sha256 $setupFull
$observationHash = Get-Sha256 $observationFull
$reportDirectory = Split-Path -Parent $setupFull
if ([string]::IsNullOrWhiteSpace($HistoryDirectory)) { $HistoryDirectory = Join-Path $reportDirectory 'history' }
$historyFull = Resolve-AbsolutePath $HistoryDirectory 'HistoryDirectory'
$reportPrefix = $reportDirectory.TrimEnd([char]'\',[char]'/') + [IO.Path]::DirectorySeparatorChar
if (-not ($historyFull.TrimEnd([char]'\',[char]'/') + [IO.Path]::DirectorySeparatorChar).StartsWith($reportPrefix, [StringComparison]::OrdinalIgnoreCase)) { throw 'HistoryDirectory must stay inside the test-setup report directory.' }
[IO.Directory]::CreateDirectory($historyFull) | Out-Null
$historyPath = Join-Path $historyFull ($RunId + '.json')
$relativeHistory = $historyPath.Substring($reportDirectory.Length).TrimStart([char]'\',[char]'/') -replace '\\','/'
$recordedAt = [DateTimeOffset]::UtcNow.ToString('o')

$normalizedCounts = [ordered]@{total=$total;passed=$passed;failed=$failed;errors=$errors;skipped=$skipped}
$historyRecord = [ordered]@{
    schema_version = 1
    run_id = $RunId
    recorded_at_utc = $recordedAt
    observed_at_utc = $observedAt.ToString('o')
    setup_report_sha256_before = $setupHashBefore
    observation_sha256 = $observationHash
    target = $expectedTarget
    provider = $provider
    runner_version = $runnerVersion
    selection = @($selection)
    counts = $normalizedCounts
    result = $result
    installation = $installation
    test_client_connection_observed = [bool]$testClientObserved
    evidence = [ordered]@{kind='interactive_ui_observation';summary=$summary;fresh_durable_report=$false;automation_readiness_proven=$false}
}

$historyAlreadyExists = Test-Path -LiteralPath $historyPath -PathType Leaf
if ($historyAlreadyExists) {
    $existing = Get-Content -LiteralPath $historyPath -Raw -Encoding UTF8 | ConvertFrom-Json
    if ([string](Get-PropertyValue $existing 'observation_sha256') -ne $observationHash) { throw "Pilot history is immutable and RunId already refers to different evidence: $historyPath" }
}
else { Write-JsonAtomic $historyRecord $historyPath }

$existingLatestPilot = Get-PropertyValue $providerState 'latest_pilot'
if ($historyAlreadyExists -and [string](Get-PropertyValue $existingLatestPilot 'run_id') -eq $RunId) {
    $expectedLatest = Get-LatestProviderPilot $providers
    $currentLatest = Get-PropertyValue $setup 'interactive_pilot'
    if ($null -ne $expectedLatest -and [string](Get-PropertyValue $currentLatest 'latest_run_id') -eq $expectedLatest.run_id) {
        [pscustomobject]@{
            status = $result
            provider = $provider
            provider_state = [string](Get-PropertyValue $providerState 'state')
            setup_status = [string](Get-PropertyValue $setup 'status')
            automation_readiness = 'BLOCKED'
            test_client_connection_observed = [bool]$testClientObserved
            history = $historyPath
            current = $setupFull
            runtime_action_performed = $false
            idempotent_replay = $true
            current_updated = $false
            recorded_not_current = $false
        }
        return
    }
}

$existingProviderObservedAt = [DateTimeOffset]::MinValue
$existingProviderRunId = [string](Get-PropertyValue $existingLatestPilot 'run_id')
$existingProviderObservedText = [string](Get-PropertyValue $existingLatestPilot 'observed_at_utc')
$hasExistingProviderTime = [DateTimeOffset]::TryParse($existingProviderObservedText, [Globalization.CultureInfo]::InvariantCulture, [Globalization.DateTimeStyles]::RoundtripKind, [ref]$existingProviderObservedAt)
$existingProviderWins = $existingProviderRunId -ne $RunId -and $hasExistingProviderTime -and ($existingProviderObservedAt -gt $observedAt -or ($existingProviderObservedAt -eq $observedAt -and [string]::CompareOrdinal($existingProviderRunId,$RunId) -ge 0))
if ($existingProviderWins) {
    [pscustomobject]@{
        status = 'RECORDED'
        provider = $provider
        provider_state = [string](Get-PropertyValue $providerState 'state')
        setup_status = [string](Get-PropertyValue $setup 'status')
        automation_readiness = 'BLOCKED'
        test_client_connection_observed = [bool]$testClientObserved
        history = $historyPath
        current = $setupFull
        runtime_action_performed = $false
        idempotent_replay = $false
        current_updated = $false
        recorded_not_current = $true
    }
    return
}

$passedPilot = $result -eq 'PASS'
$nextProviderState = if ($passedPilot) { 'pilot_passed' } else { 'blocked' }
$installedDatabaseState = if ($provider -eq 'yaxunit') { 'installed_active' } else { 'not_applicable_external_runner' }
$providerBlockedReason = if ($passedPilot) { $null } else { 'interactive_pilot_' + $result.ToLowerInvariant() }
Set-PropertyValue $providerState 'state' $nextProviderState
Set-PropertyValue $providerState 'enabled' ([bool]$passedPilot)
Set-PropertyValue $providerState 'installed_in_database' $installedDatabaseState
Set-PropertyValue $providerState 'pilot' ($result.ToLowerInvariant())
Set-PropertyValue $providerState 'blocked_reason' $providerBlockedReason
$capabilities = if ($provider -eq 'yaxunit') {
    [ordered]@{unit_and_integration_engine=[bool]$passedPilot;unattended_run=$false;fresh_durable_report=$false}
}
else {
    [ordered]@{scenario_runner=[bool]$passedPilot;test_client=[bool]($passedPilot -and $testClientObserved);unattended_run=$false;fresh_durable_report=$false}
}
Set-PropertyValue $providerState 'capabilities' $capabilities
$nextAction = if (-not $passedPilot) {
    'Inspect the recorded failure without replaying database changes; run a new attempt only after the cause and resulting state are understood.'
}
elseif ($provider -eq 'vanessa' -and -not $testClientObserved) {
    'Runner smoke passed. Prove an explicit TestClient connection only when UI verification is required.'
}
else {
    'Interactive engine smoke passed. Use the durable result contract for unattended PASS evidence.'
}
Set-PropertyValue $providerState 'next_action' $nextAction
Set-PropertyValue $providerState 'latest_pilot' ([ordered]@{run_id=$RunId;observed_at_utc=$observedAt.ToString('o');history_path=$relativeHistory;result=$result;evidence_kind='interactive_ui_observation'})

$anyPilotPassed = @($providers.PSObject.Properties | Where-Object { [bool](Get-PropertyValue $_.Value 'enabled') }).Count -gt 0
$globalLatest = Get-LatestProviderPilot $providers
if ($null -eq $globalLatest) { throw 'Current provider state has no valid latest pilot after update.' }
$inventory = Get-PropertyValue $setup 'inventory'
if ($null -ne $inventory) { Set-PropertyValue $inventory 'runtime_verified' ([bool]$anyPilotPassed) }
Set-PropertyValue $setup 'updated_at_utc' $recordedAt
Set-PropertyValue $setup 'runtime_actions' 'interactive_pilot_recorded'
$setupStatus = if ($anyPilotPassed) { 'pilot_passed' } else { 'blocked' }
$setupBlockedReason = if ($anyPilotPassed) { $null } else { 'No recorded interactive pilot currently passes.' }
Set-PropertyValue $setup 'status' $setupStatus
Set-PropertyValue $setup 'blocked_reason' $setupBlockedReason
Set-PropertyValue $setup 'interactive_pilot' ([ordered]@{latest_run_id=$globalLatest.run_id;latest_provider=$globalLatest.provider;latest_result=$globalLatest.result;latest_observed_at_utc=$globalLatest.observed_at_utc.ToString('o');history_directory='history';evidence_boundary='Interactive observation proves the selected live run only; it does not prove unattended execution or a fresh durable report.'})
Set-PropertyValue $setup 'automation_readiness' ([ordered]@{status='blocked';fresh_durable_report=$false;reason='Interactive pilot evidence is intentionally separate from the durable result contract.'})
Write-JsonAtomic $setup $setupFull

[pscustomobject]@{
    status = $result
    provider = $provider
    provider_state = [string](Get-PropertyValue $providerState 'state')
    setup_status = [string](Get-PropertyValue $setup 'status')
    automation_readiness = 'BLOCKED'
    test_client_connection_observed = [bool]$testClientObserved
    history = $historyPath
    current = $setupFull
    runtime_action_performed = $false
    idempotent_replay = $false
    current_updated = $true
    recorded_not_current = $false
}
