#Requires -Version 7.0
[CmdletBinding()]
param(
    [Alias('PlanPath','DeclarationPath')][Parameter(Mandatory)][string]$RequestPath,
    [Alias('EvidencePath','PreviewPath','ObservedPath')][Parameter(Mandatory)][string]$ObservedEvidencePath,
    [Alias('Profile')][string]$ProfilePath,
    [Alias('Output')][string]$OutputPath,
    [switch]$NoThrow
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$common = Join-Path $PSScriptRoot 'TestEvidence.Common.ps1'
. $common

function Find-TEPath {
    param([object]$Object, [string[]]$Paths)
    foreach ($path in $Paths) {
        $value = $Object
        $found = $true
        foreach ($part in ($path -split '\.')) {
            $value = Get-TEProperty $value @($part)
            if ($null -eq $value) { $found = $false; break }
        }
        if ($found) { return $value }
    }
    return $null
}

function Add-TECheck {
    param([System.Collections.ArrayList]$Checks, [string]$Name, [bool]$Passed, [string]$Message, [bool]$Required = $true)
    [void]$Checks.Add([pscustomobject]@{ name=$Name; status=if($Passed){'pass'}else{if($Required){'blocker'}else{'gap'}}; required=$Required; message=$Message })
}

function Get-TESelection {
    param([object]$Object)
    $value = Find-TEPath $Object @('selection.tests','selection','tests','filter.tests','filter')
    return @(Get-TEStringArray $value)
}

function Get-TESources {
    param([object]$Object)
    $value = Find-TEPath $Object @('sources','sourceSet','source_set','project.sources','test.sources','extensions')
    if ($null -ne $value -and -not ($value -is [string]) -and $null -ne (Get-TEProperty $value @('name','path'))) { $value = @($value) }
    return @(Get-TEStringArray $value)
}

function Get-TETarget {
    param([object]$Object)
    $value = Find-TEPath $Object @('target','database','testTarget','test_target','workspace')
    if ($null -eq $value) { return $null }
    if ($value -is [string]) { return $value }
    $path = Get-TEProperty $value @('path','database_path','databasePath','connection','name')
    if ($null -ne $path) { return $path }
    return $value
}

function Get-TEExpectedMap {
    param([object]$Request)
    $map = Find-TEPath $Request @('versions','sourceVersions','source_versions')
    if ($null -eq $map) { return $null }
    return $map
}

function Get-TEEffectiveEvidence {
    param([object]$Observed)
    $effective = Find-TEPath $Observed @('effective','effective_runtime','applied','runtime','data.effective','data.effective_runtime')
    if ($null -eq $effective) { return [pscustomobject]@{} }
    return $effective
}

function Compare-TEOptional {
    param([object]$Expected, [object]$Observed)
    if ($null -eq $Expected) { return $true }
    if ($null -eq $Observed) { return $false }
    if ($Expected -is [string] -or $Observed -is [string]) { return ([string]$Expected -eq [string]$Observed) }
    return (Compare-TEValue $Expected $Observed)
}

$checks = New-Object System.Collections.ArrayList
$gaps = New-Object System.Collections.ArrayList
$request = $null
$observed = $null
$status = 'BLOCKED'
$requestResolved = $null
$observedResolved = $null
try {
    $requestResolved = [System.IO.Path]::GetFullPath($RequestPath)
    $observedResolved = [System.IO.Path]::GetFullPath($ObservedEvidencePath)
    $request = Get-TEJsonFile $requestResolved
    $observed = Get-TEJsonFile $observedResolved
    $effectiveObserved = Get-TEEffectiveEvidence $observed

    $expectedRunner = Find-TEPath $request @('runner','runner.name','tool','tool.name','engine')
    $observedRunner = Find-TEPath $observed @('runner','runner.name','tool','tool.name','data.runner','diagnostics.tool')
    Add-TECheck $checks 'runner' ($null -ne $expectedRunner -and $null -ne $observedRunner -and (Compare-TEOptional $expectedRunner $observedRunner)) "declared=$expectedRunner; observed=$observedRunner"

    $expectedOperation = Find-TEPath $request @('operation','command')
    if ($null -eq $expectedOperation) { $expectedOperation = 'test' }
    $observedOperation = Find-TEPath $observed @('operation','command','data.operation')
    Add-TECheck $checks 'operation' ($null -ne $observedOperation -and [string]$expectedOperation -eq [string]$observedOperation) "declared=$expectedOperation; observed=$observedOperation"
    $observedRootOkProperty = $observed.PSObject.Properties['ok']
    $observedDataObject = Get-TEProperty $observed @('data')
    $observedDataOkProperty = if ($observedDataObject) { $observedDataObject.PSObject.Properties['ok'] } else { $null }
    $observedRootOk = if ($observedRootOkProperty) { ConvertTo-TEBoolean $observedRootOkProperty.Value } else { $null }
    $observedDataOk = if ($observedDataOkProperty) { ConvertTo-TEBoolean $observedDataOkProperty.Value } else { $null }
    $observedOk = ($observedRootOk -eq $true) -and ($null -eq $observedDataOkProperty -or $observedDataOk -eq $true)
    Add-TECheck $checks 'observed_result' $observedOk 'The saved preview/receipt itself reports success; an error envelope cannot authorize a run.'

    $expectedSchema = Find-TEPath $request @('schema','schema_version','schemaVersion','contract')
    $observedSchema = Find-TEPath $observed @('schema','schema_version','schemaVersion','contract')
    $schemaShape = ($null -ne (Get-TEProperty $observed @('ok','command','data','steps')))
    if ($null -eq $expectedSchema -and $schemaShape) { $observedSchema = 'v8-runner-command-envelope' }
    Add-TECheck $checks 'schema' ($null -ne $observedSchema -and (Compare-TEOptional $expectedSchema $observedSchema)) "declared=$expectedSchema; observed=$observedSchema"

    $expectedTarget = Get-TETarget $request
    $observedTarget = Get-TETarget $effectiveObserved
    Add-TECheck $checks 'target' ($null -ne $expectedTarget -and $null -ne $observedTarget -and (Compare-TEOptional $expectedTarget $observedTarget)) 'Exact FILE target is declared and observed.'

    $expectedSources = @(Get-TESources $request)
    $observedSources = @(Get-TESources $effectiveObserved)
    $sourcesOk = $expectedSources.Count -gt 0 -and $observedSources.Count -gt 0 -and (Compare-TEValue $expectedSources $observedSources)
    Add-TECheck $checks 'sources' $sourcesOk 'Selected source-set/extension identities are compared, not inferred from a test-module filter.'

    $expectedSelection = @(Get-TESelection $request)
    $observedSelection = @(Get-TESelection $effectiveObserved)
    $selectionOk = $expectedSelection.Count -gt 0 -and $observedSelection.Count -gt 0 -and (Compare-TEValue $expectedSelection $observedSelection)
    Add-TECheck $checks 'selection' $selectionOk 'The exact requested and observed test selection match.'

    $expectedVersions = Get-TEExpectedMap $request
    $observedVersions = Find-TEPath $effectiveObserved @('versions','sourceVersions','source_versions')
    $versionsOk = $null -ne $expectedVersions -and $null -ne $observedVersions -and (Compare-TEValue $expectedVersions $observedVersions)
    Add-TECheck $checks 'versions' $versionsOk 'Declared source/extension versions are matched with observed evidence.'

    $steps = @(Get-TEArray (Find-TEPath $observed @('steps','data.steps')))
    foreach ($operation in @('build','load')) {
        $flagName = if ($operation -eq 'build') { 'noBuild' } else { 'noLoad' }
        $flagAliases = if ($operation -eq 'build') { @('noBuild','no_build','test.noBuild','test.no_build') } else { @('noLoad','no_load','test.noLoad','test.no_load') }
        $effectiveAliases = if ($operation -eq 'build') { @('noBuild','no_build') } else { @('noLoad','no_load') }
        $declaredRaw = Find-TEPath $request $flagAliases
        $effectiveRaw = Find-TEPath $effectiveObserved $effectiveAliases
        $declaredFlag = ConvertTo-TEBoolean $declaredRaw
        $effectiveFlag = ConvertTo-TEBoolean $effectiveRaw
        $declarationRequired = ($operation -eq 'build') -or ($null -ne $declaredRaw)
        $declarationValid = (-not $declarationRequired) -or ($null -ne $declaredFlag)
        Add-TECheck $checks ("no_{0}_declaration" -f $operation) $declarationValid "$flagName must be a JSON boolean when required or declared."

        $effectiveValid = ($null -eq $effectiveRaw) -or ($null -ne $effectiveFlag)
        $flagsAgree = $effectiveValid -and ($null -eq $declaredFlag -or $null -eq $effectiveFlag -or $declaredFlag -eq $effectiveFlag)
        $operationSteps = @($steps | Where-Object { [string](Get-TEProperty $_ @('name','operation')) -ieq $operation })
        $skipRequired = ($declaredFlag -eq $true) -or ($effectiveFlag -eq $true)
        $explicitSkippedSteps = @($operationSteps | Where-Object {
            [string](Get-TEProperty $_ @('status','state')) -ieq 'skipped' -and
            ([string](Get-TEProperty $_ @('message','reason')) -match ("(?i)no.?{0}|{0}.*skip" -f $operation))
        })
        $stepsAgree = (-not $skipRequired) -or ($operationSteps.Count -gt 0 -and $explicitSkippedSteps.Count -eq $operationSteps.Count)
        Add-TECheck $checks ("no_{0}" -f $operation) ($flagsAgree -and $stepsAgree) "Effective $flagName and every observed $operation step must agree; missing, executed, mixed, or unknown required steps block preflight."
    }

    $expectedReportPath = Find-TEPath $request @('reportPath','report_path','durableReportPath','durable_report_path')
    $observedReportPath = Find-TEPath $effectiveObserved @('reportPath','report_path','durableReportPath','durable_report_path')
    $reportOk = $null -ne $expectedReportPath -and $null -ne $observedReportPath -and [string]$expectedReportPath -eq [string]$observedReportPath
    Add-TECheck $checks 'durable_report' $reportOk 'The selected route declares and observes one durable report location.'
    $expectedCapabilities = Find-TEPath $request @('capabilities','requiredCapabilities','required_capabilities')
    $observedCapabilities = Find-TEPath $effectiveObserved @('capabilities','supportedCapabilities','supported_capabilities')
    $capabilityOk = $null -ne $expectedCapabilities -and $null -ne $observedCapabilities -and (Compare-TEValue $expectedCapabilities $observedCapabilities)
    Add-TECheck $checks 'capabilities' $capabilityOk 'Selected runner capabilities are evidence-backed, not inferred from a module filter.'

    $declaredProfile = if ($ProfilePath) { [System.IO.Path]::GetFullPath($ProfilePath) } else { Find-TEPath $request @('profile','testClientProfile','test_client_profile') }
    if ($null -ne $declaredProfile) {
        $profileEvidence = Find-TEPath $effectiveObserved @('profile','testClientProfile','test_client_profile')
        $declaredProfileHash = if ($ProfilePath -and (Test-Path -LiteralPath $ProfilePath -PathType Leaf)) { Get-TESha256 $ProfilePath } else { $null }
        $observedProfilePath = if ($profileEvidence) { Get-TEProperty $profileEvidence @('path','profile_path','profilePath') } else { $null }
        $observedProfileHash = if ($profileEvidence) { Get-TEProperty $profileEvidence @('sha256','hash') } else { $null }
        $profileOk = $null -ne $profileEvidence -and $null -ne $observedProfilePath -and [string]$observedProfilePath -eq [string]$declaredProfile -and (($null -eq $declaredProfileHash) -or [string]$declaredProfileHash -eq [string]$observedProfileHash)
        Add-TECheck $checks 'profile' $profileOk 'The effective TestClient/profile evidence is explicit and is not taken from a saved user profile.'
    }
    else { [void]$gaps.Add('No TestClient profile was requested; a profile-specific claim is outside this preflight.') }

    $evidenceKind = if ((Find-TEPath $observed @('dryRun','dry_run')) -eq $true -or [string](Find-TEPath $observed @('phase','kind')) -match '(?i)preview') { 'preview' } elseif ([string](Find-TEPath $observed @('phase','kind')) -match '(?i)applied|executed') { 'applied' } else { 'unknown' }
    Add-TECheck $checks 'evidence_kind' ($evidenceKind -ne 'unknown') 'Preview and applied evidence are distinct; an unknown phase cannot authorize a run.'
    if ($evidenceKind -eq 'preview') { [void]$gaps.Add('Preview proves route admission only; it does not prove applied execution or business state.') }
    $failed = @($checks | Where-Object status -eq 'blocker')
    if ($failed.Count -eq 0) { $status = 'PASS' }
    else { $status = 'BLOCKED' }
    $result = [ordered]@{
        schema_version = 1
        kind = 'bsl-flow.test-preflight'
        checked_at_utc = [DateTime]::UtcNow.ToString('o')
        status = $status
        safe_to_run = ($status -eq 'PASS' -and $evidenceKind -ne 'unknown')
        launch_performed = $false
        inputs = [ordered]@{ request = $requestResolved; observed = $observedResolved; profile = if($ProfilePath){[System.IO.Path]::GetFullPath($ProfilePath)}else{$null} }
        declared_vs_observed = [ordered]@{
            runner = [ordered]@{ declared=$expectedRunner; observed=$observedRunner }
            operation = [ordered]@{ declared=$expectedOperation; observed=$observedOperation }
            schema = [ordered]@{ declared=$expectedSchema; observed=$observedSchema }
            target = [ordered]@{ declared=$expectedTarget; observed=$observedTarget }
            sources = [ordered]@{ declared=$expectedSources; observed=$observedSources }
            selection = [ordered]@{ declared=$expectedSelection; observed=$observedSelection }
            versions = [ordered]@{ declared=$expectedVersions; observed=$observedVersions }
        }
        checks = @($checks)
        known_gaps = @($gaps)
        evidence_kind = $evidenceKind
        applied_success_proven = $false
        next_action = if ($status -eq 'PASS') { 'Run only the declared safe test operation; retain its receipt and reports.' } else { 'Resolve the listed evidence blockers with a read-only supported check; do not launch, load, retry, or create business data.' }
    }
    if ($OutputPath) { Write-TEJsonAtomic $result $OutputPath }
    $result
    if ($status -ne 'PASS' -and -not $NoThrow) { throw "Test preflight blocked: $(@($failed | ForEach-Object message) -join '; ')" }
}
catch {
    if ($NoThrow) {
        [pscustomobject]@{ schema_version=1; kind='bsl-flow.test-preflight'; status='BLOCKED'; safe_to_run=$false; launch_performed=$false; error=$_.Exception.Message; next_action='Fix the input evidence and rerun this offline preflight; no runtime operation was started.' }
    }
    else { throw }
}
