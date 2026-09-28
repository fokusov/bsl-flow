#Requires -Version 7.0
<#
.SYNOPSIS
onec-ops/v1 dispatcher: selects a configured provider for a capability, enforces authorization
for mutating capabilities, validates the provider's result against the op-result contract,
hashes evidence, and writes a durable report under .bsl-flow/reports/onec-ops/.

.DESCRIPTION
See global/skills/1c-verify/references/onec-ops.md for the capability table, provider selection
order and how to add an adapter.

Exit codes: 0 = PASS, 1 = FAIL, 11 = BLOCKED (also used for dispatcher-level refusals such as
"no configured provider" or missing/invalid authorization for a mutating capability).
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$Capability,
    [Parameter(Mandatory)][string]$ProjectPath,
    [object]$Params,
    [string]$Provider,
    [string]$AuthorizationFile,
    [string]$ImportResult
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'OneCOps.Common.ps1')

function Get-OOParam {
    param([object]$Params, [Parameter(Mandatory)][string]$Name)
    if ($null -eq $Params) { return $null }
    if ($Params -is [System.Collections.IDictionary]) { if ($Params.Contains($Name)) { return $Params[$Name] }; return $null }
    $prop = $Params.PSObject.Properties[$Name]
    if ($null -ne $prop) { return $prop.Value }
    return $null
}

function ConvertTo-OOParamsObject {
    param([object]$Params)
    if ($null -eq $Params) { return [pscustomobject]@{} }
    if ($Params -is [string]) {
        if (-not (Test-Path -LiteralPath $Params -PathType Leaf)) { throw "BF_INVALID: Params file was not found: $Params" }
        return (Get-Content -Raw -LiteralPath $Params -Encoding UTF8 | ConvertFrom-Json -ErrorAction Stop)
    }
    if ($Params -is [System.Collections.IDictionary]) { return [pscustomobject]$Params }
    return $Params
}

function Write-OOResult {
    param([Parameter(Mandatory)][hashtable]$Fields, [Parameter(Mandatory)][string]$ProjectFull, [Parameter(Mandatory)][string]$Capability)
    $ordered = [ordered]@{
        schema_version    = 1
        capability        = $Capability
        status            = $Fields.status
        mutating          = [bool]$Fields.mutating
        target            = $Fields.target
        evidence          = @($Fields.evidence)
        raw_output_sha256 = $Fields.raw_output_sha256
        provider          = $Fields.provider
        provider_version  = $Fields.provider_version
        message           = $Fields.message
    }
    if ($Fields.ContainsKey('agent_tool') -and $null -ne $Fields.agent_tool) { $ordered.agent_tool = $Fields.agent_tool }

    $schemaPath = Join-Path $PSScriptRoot 'schemas/op-result.schema.json'
    $schema = Get-Content -Raw -LiteralPath $schemaPath -Encoding UTF8 | ConvertFrom-Json -ErrorAction Stop
    $issues = @(Test-OOJsonSchema -Schema $schema -Instance ([pscustomobject]$ordered))
    if ($issues.Count -gt 0) { throw "BF_INVALID: onec-ops result failed schema validation: $($issues -join '; ')" }

    $timestamp = [DateTime]::UtcNow.ToString('yyyyMMddTHHmmssfffZ')
    $reportsDir = Join-Path $ProjectFull '.bsl-flow/reports/onec-ops'
    $reportPath = Join-Path $reportsDir "$timestamp-$($Capability -replace '[^A-Za-z0-9._-]', '_').json"
    Write-OOJsonAtomic $ordered $reportPath

    $ordered | ConvertTo-Json -Depth 20 | Write-Output
}

$projectFull = [IO.Path]::GetFullPath($ProjectPath)
$yamlText = Get-OOBslFlowYamlText $projectFull
$paramsObj = ConvertTo-OOParamsObject $Params
$adaptersRoot = Join-Path $PSScriptRoot 'adapters'
$providerSchema = Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot 'schemas/provider.schema.json') -Encoding UTF8 | ConvertFrom-Json -ErrorAction Stop

# --- Provider selection order -------------------------------------------------------------
$candidates = [System.Collections.Generic.List[string]]::new()
if (-not [string]::IsNullOrWhiteSpace($Provider)) {
    $candidates.Add($Provider)
}
else {
    $overrides = Get-OOYamlFlatMap $yamlText @('onec', 'overrides')
    if ($overrides.Contains($Capability)) { $candidates.Add([string]$overrides[$Capability]) }
    else {
        foreach ($name in (Get-OOYamlStringList $yamlText @('onec', 'providers'))) { $candidates.Add($name) }
    }
}

$selectedProvider = $null
$selectedManifest = $null
$selectedCapEntry = $null
foreach ($candidateName in $candidates) {
    if ($candidateName -cnotmatch '^[a-z][a-z0-9-]*$') { throw 'BF_INVALID: invalid provider name' }
    $manifestPath = Join-Path $adaptersRoot "$candidateName/provider.json"
    if (-not (Test-Path -LiteralPath $manifestPath -PathType Leaf)) { continue }
    $manifest = Get-Content -Raw -LiteralPath $manifestPath -Encoding UTF8 | ConvertFrom-Json -ErrorAction Stop
    $manifestIssues = @(Test-OOJsonSchema -Schema $providerSchema -Instance $manifest)
    if ($manifestIssues.Count -gt 0) { throw "BF_INVALID: adapter manifest failed schema validation ($candidateName): $($manifestIssues -join '; ')" }
    $capEntry = Get-OOProperty $manifest.capabilities @($Capability)
    if ($null -eq $capEntry) { continue }
    $adapterDir = Join-Path $adaptersRoot $candidateName
    $detectPath = Join-Path $adapterDir $manifest.detect
    $detectOk = $false
    if (Test-Path -LiteralPath $detectPath -PathType Leaf) {
        try {
            $detectOutput = & $detectPath -ProjectPath $projectFull -AdapterDir $adapterDir
            $last = @($detectOutput) | Select-Object -Last 1
            $detectOk = ("$last" -eq 'true')
        }
        catch { $detectOk = $false }
    }
    if ($detectOk) { $selectedProvider = $candidateName; $selectedManifest = $manifest; $selectedCapEntry = $capEntry; break }
}

if ($null -eq $selectedProvider) {
    Write-OOResult -Fields @{
        status = 'BLOCKED'; mutating = $false; target = (Get-OOParam $paramsObj 'target'); evidence = @()
        raw_output_sha256 = $null; provider = 'none'; provider_version = $null
        message = "BF_BLOCKED: capability $Capability has no configured provider"
    } -ProjectFull $projectFull -Capability $Capability
    exit 11
}

$mutating = ($Capability -in @('extension.load', 'config.update', 'test.yaxunit', 'test.vanessa')) -or ((Get-OOProperty $selectedCapEntry @('requires_authorization')) -eq $true)
$requestTarget = Get-OOParam $paramsObj 'target'

if ($mutating) {
    $failure = Get-OOAuthorizationFailure -Capability $Capability -Target $requestTarget -AuthorizationFile $AuthorizationFile
    if ($null -ne $failure) {
        Write-OOResult -Fields @{
            status = 'BLOCKED'; mutating = $true; target = $requestTarget; evidence = @()
            raw_output_sha256 = $null; provider = $selectedProvider; provider_version = (Get-OOProperty $selectedManifest @('provider_version'))
            message = $failure.message
        } -ProjectFull $projectFull -Capability $Capability
        exit 11
    }
}

# --- Invoke the provider's entry script ---------------------------------------------------
$adapterDir = Join-Path $adaptersRoot $selectedProvider
$entryPath = Join-Path $adapterDir $selectedCapEntry.entry
if (-not (Test-Path -LiteralPath $entryPath -PathType Leaf)) { throw "BF_INVALID: adapter entry script is missing: $entryPath" }

$adapterResult = & $entryPath -ProjectPath $projectFull -Params $paramsObj -AdapterDir $adapterDir -Capability $Capability -AuthorizationFile $AuthorizationFile -ImportResult $ImportResult
if ($adapterResult -is [array]) { $adapterResult = $adapterResult | Select-Object -Last 1 }
if ($null -eq $adapterResult) { throw "BF_INVALID: adapter $selectedProvider returned no result for $Capability" }

$status = [string](Get-OOProperty $adapterResult @('status'))
if ($status -notin @('PASS', 'FAIL', 'BLOCKED')) { throw "BF_INVALID: adapter $selectedProvider returned an invalid status: $status" }

$evidenceIn = @(Get-OOProperty $adapterResult @('evidence'))
$evidenceOut = @()
foreach ($item in $evidenceIn) {
    $relOrAbs = if ($item -is [string]) { $item } else { [string](Get-OOProperty $item @('path')) }
    $fullPath = if ([IO.Path]::IsPathRooted($relOrAbs)) { $relOrAbs } else { Join-Path $projectFull $relOrAbs }
    $evidenceOut += [ordered]@{ path = $relOrAbs; sha256 = (Get-OOSha256File $fullPath) }
}

$rawOutputText = Get-OOProperty $adapterResult @('raw_output')
$rawOutputPath = Get-OOProperty $adapterResult @('raw_output_path')
$rawOutputSha256 = $null
if (-not [string]::IsNullOrEmpty($rawOutputText)) { $rawOutputSha256 = Get-OOSha256Text ([string]$rawOutputText) }
elseif (-not [string]::IsNullOrWhiteSpace([string]$rawOutputPath)) {
    $rawFull = if ([IO.Path]::IsPathRooted([string]$rawOutputPath)) { [string]$rawOutputPath } else { Join-Path $projectFull ([string]$rawOutputPath) }
    $rawOutputSha256 = Get-OOSha256File $rawFull
}

$resultTarget = Get-OOProperty $adapterResult @('target')
if ($null -eq $resultTarget) { $resultTarget = $requestTarget }

$adapterMutating = Get-OOProperty $adapterResult @('mutating')
$effectiveMutating = $mutating -or ($adapterMutating -eq $true)

$fields = @{
    status            = $status
    mutating           = $effectiveMutating
    target             = $resultTarget
    evidence           = $evidenceOut
    raw_output_sha256  = $rawOutputSha256
    provider           = $selectedProvider
    provider_version   = (Get-OOProperty $adapterResult @('provider_version'))
    message            = Get-OOProperty $adapterResult @('message')
}
$agentTool = Get-OOProperty $adapterResult @('agent_tool')
if ($null -ne $agentTool) { $fields.agent_tool = $agentTool }

Write-OOResult -Fields $fields -ProjectFull $projectFull -Capability $Capability
switch ($status) {
    'PASS' { exit 0 }
    'FAIL' { exit 1 }
    'BLOCKED' { exit 11 }
}
