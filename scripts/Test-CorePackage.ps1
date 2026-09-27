#Requires -Version 7.0
# Offline contract test for the Core/Managed package split (docs/plans/2026-09-26-remediation-plan.md
# Ф2, ADR-12 in docs/ARCHITECTURE_RU.md). Builds both packages with
# scripts/Build-BSLFlowPackage.ps1 -Package core|managed and asserts the package boundary:
# no file duplication beyond LICENSE/VERSION/README, Core carries no Council engine and no
# 1c-task, and Managed's manifest declares requires_core equal to the repo VERSION.
#
# It also reproduces the L/high-risk-without-Council path from an extracted Core-only package
# (task Ф2.2 "L does not degrade"): Invoke-1CSpecReview.ps1 must fail closed with a clean
# 'BF_BLOCKED: ...' message when the Council engine is absent, not a raw PowerShell error from a
# missing dot-sourced file. See the final report for the exact gap found here and the recommended
# fix to Invoke-1CSpecReview.ps1 (not edited by this change; that script is out of scope here).
[CmdletBinding()]
param([string]$PackageRoot)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($PackageRoot)) { $PackageRoot = Split-Path -Parent $PSScriptRoot }
$root = [IO.Path]::GetFullPath($PackageRoot).TrimEnd('\', '/')
$buildScript = Join-Path $root 'scripts\Build-BSLFlowPackage.ps1'
if (-not (Test-Path -LiteralPath $buildScript -PathType Leaf)) { throw "Missing build script: $buildScript" }

function Assert-True { param([Parameter(Mandatory)][bool]$Condition, [Parameter(Mandatory)][string]$Message) if (-not $Condition) { throw "ASSERTION FAILED: $Message" } }

Add-Type -AssemblyName System.IO.Compression.FileSystem

function Get-BFZipEntryNames {
    param([Parameter(Mandatory)][string]$ZipPath)
    $zip = [IO.Compression.ZipFile]::OpenRead($ZipPath)
    try { return [string[]]@($zip.Entries | ForEach-Object { $_.FullName }) }
    finally { $zip.Dispose() }
}

function Get-BFZipJson {
    param([Parameter(Mandatory)][string]$ZipPath, [Parameter(Mandatory)][string]$EntryName)
    $zip = [IO.Compression.ZipFile]::OpenRead($ZipPath)
    try {
        $entry = $zip.Entries | Where-Object { $_.FullName -eq $EntryName } | Select-Object -First 1
        if (-not $entry) { throw "Zip entry not found: $EntryName" }
        $reader = New-Object IO.StreamReader($entry.Open())
        try { return ($reader.ReadToEnd() | ConvertFrom-Json -ErrorAction Stop) }
        finally { $reader.Dispose() }
    }
    finally { $zip.Dispose() }
}

$testRoot = Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-core-package-test-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $testRoot -Force | Out-Null
try {
    $version = (Get-Content -Raw -LiteralPath (Join-Path $root 'VERSION')).Trim()
    $coreZip = Join-Path $testRoot 'core.zip'
    $managedZip = Join-Path $testRoot 'managed.zip'
    $coreBuild = & $buildScript -PackageRoot $root -Package core -OutputPath $coreZip
    $managedBuild = & $buildScript -PackageRoot $root -Package managed -OutputPath $managedZip
    Assert-True ($coreBuild.Package -eq 'core') 'Core build did not report Package=core.'
    Assert-True ($managedBuild.Package -eq 'managed') 'Managed build did not report Package=managed.'

    $coreFiles = Get-BFZipEntryNames -ZipPath $coreZip | Where-Object { $_ -ne 'package-manifest.json' }
    $managedFiles = Get-BFZipEntryNames -ZipPath $managedZip | Where-Object { $_ -ne 'package-manifest.json' }
    Assert-True ($coreFiles.Count -gt 0) 'Core package is empty.'
    Assert-True ($managedFiles.Count -gt 0) 'Managed package is empty.'

    $allowedSharedFiles = @('LICENSE', 'VERSION', 'README.md', 'README.en.md')
    $overlap = @($coreFiles | Where-Object { $managedFiles -contains $_ })
    $unexpectedOverlap = @($overlap | Where-Object { $_ -notin $allowedSharedFiles })
    Assert-True ($unexpectedOverlap.Count -eq 0) "Core and Managed packages duplicate files outside LICENSE/VERSION/README: $($unexpectedOverlap -join ', ')"

    $councilInCore = @($coreFiles | Where-Object { $_ -like '*/Council.*.ps1' -or $_ -like '*/Invoke-CouncilReview.ps1' -or $_ -like '*/Test-Council*.ps1' -or $_ -like '*/council-*' })
    Assert-True ($councilInCore.Count -eq 0) "Core package must not contain Council files: $($councilInCore -join ', ')"

    $taskInCore = @($coreFiles | Where-Object { $_ -like 'global/skills/1c-task/*' })
    Assert-True ($taskInCore.Count -eq 0) "Core package must not contain the 1c-task skill: $($taskInCore -join ', ')"

    $managedManifest = Get-BFZipJson -ZipPath $managedZip -EntryName 'package-manifest.json'
    Assert-True ([string]$managedManifest.requires_core -eq $version) "Managed manifest requires_core ('$($managedManifest.requires_core)') must equal VERSION ('$version')."
    $coreManifest = Get-BFZipJson -ZipPath $coreZip -EntryName 'package-manifest.json'
    Assert-True ($null -eq $coreManifest.requires_core) "Core manifest requires_core must be null, was: $($coreManifest.requires_core)"

    # --- Ф2.2: L/high-risk review must fail closed, not crash, when Council is absent ---
    $extractRoot = Join-Path $testRoot 'core-extract'
    [IO.Compression.ZipFile]::ExtractToDirectory($coreZip, $extractRoot)
    $reviewScript = Join-Path $extractRoot 'global\skills\1c-spec-review\scripts\Invoke-1CSpecReview.ps1'
    Assert-True (Test-Path -LiteralPath $reviewScript -PathType Leaf) "Extracted Core package is missing Invoke-1CSpecReview.ps1: $reviewScript"

    $fixtureRoot = Join-Path $testRoot 'fixture-project'
    New-Item -ItemType Directory -Path (Join-Path $fixtureRoot 'src') -Force | Out-Null
    New-Item -ItemType Directory -Path (Join-Path $fixtureRoot 'openspec\changes\demo-l-change') -Force | Out-Null
    Set-Content -LiteralPath (Join-Path $fixtureRoot 'bsl-flow.yaml') -Value "source:`n  paths:`n    - src`n" -Encoding utf8
    Set-Content -LiteralPath (Join-Path $fixtureRoot 'src\Module.bsl') -Value 'Процедура Тест() КонецПроцедуры' -Encoding utf8
    $specText = @'
## Classification
- Complexity: L
- Risk: high

## Goal
Do a big risky thing.

## Required behavior
Something important happens across the whole configuration.

## 1C context
Touches core catalogs.

## Non-goals
n/a

## Acceptance criteria
- GIVEN a WHEN b THEN c happens reliably

## Required verification
- [x] Static: checks the specific catalog change end to end
'@
    Set-Content -LiteralPath (Join-Path $fixtureRoot 'openspec\changes\demo-l-change\spec.md') -Value $specText -Encoding utf8
    Set-Content -LiteralPath (Join-Path $fixtureRoot 'openspec\changes\demo-l-change\original-task.md') -Value 'Original task: do the big risky thing.' -Encoding utf8

    $previous = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    $caught = $null
    try {
        & $reviewScript -ProjectPath $fixtureRoot -ChangeName demo-l-change -ForceReview 2>$null | Out-Null
    }
    catch { $caught = $_ }
    finally { $ErrorActionPreference = $previous }

    Assert-True ($null -ne $caught) 'L/high-risk review without Council must fail; it returned a result instead.'
    $message = $caught.Exception.Message
    $isCleanBlocked = $message -match '^BF_BLOCKED: L/high-risk (?:specification )?review requires Council'
    if (-not $isCleanBlocked) {
        throw "GAP CONFIRMED (Ф2.2, see report): Core-only L/high-risk review did not fail with a clean BF_BLOCKED message. Actual error type: $($caught.Exception.GetType().FullName); message: $message. Fix needed in global/skills/1c-spec-review/scripts/Invoke-1CSpecReview.ps1: guard the 'Council.Profile.ps1' dot-source (and the 'Invoke-CouncilReview.ps1' dot-source for reviewMode -eq 'council') with a file-existence check and throw 'BF_BLOCKED: L/high-risk review requires Council (install bsl-flow-managed) or an owner override recorded in review-reconciliation.json' when the Council engine files are not installed."
    }
}
finally {
    if (Test-Path -LiteralPath $testRoot) { Remove-Item -LiteralPath $testRoot -Recurse -Force -ErrorAction SilentlyContinue }
}

Write-Host 'Test-CorePackage: OK'
