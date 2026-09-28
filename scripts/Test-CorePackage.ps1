#Requires -Version 7.0
# Offline extracted-package boundaries, review gates, API transport and isolated installers.
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
    $coreBuild = & $buildScript -PackageRoot $root -Package core -OutputPath $coreZip -Test
    $managedBuild = & $buildScript -PackageRoot $root -Package managed -OutputPath $managedZip -Test
    Assert-True ($coreBuild.Package -eq 'core') 'Core build did not report Package=core.'
    Assert-True ($managedBuild.Package -eq 'managed') 'Managed build did not report Package=managed.'

    $coreFiles = Get-BFZipEntryNames -ZipPath $coreZip | Where-Object { $_ -ne 'package-manifest.json' }
    $managedFiles = Get-BFZipEntryNames -ZipPath $managedZip | Where-Object { $_ -ne 'package-manifest.json' }
    Assert-True ($coreFiles.Count -gt 0) 'Core package is empty.'
    Assert-True ($managedFiles.Count -gt 0) 'Managed package is empty.'

    $allowedSharedFiles = @('LICENSE', 'VERSION', 'README.md', 'README.en.md', 'scripts/Install.Package.ps1')
    $overlap = @($coreFiles | Where-Object { $managedFiles -contains $_ })
    $unexpectedOverlap = @($overlap | Where-Object { $_ -notin $allowedSharedFiles })
    Assert-True ($unexpectedOverlap.Count -eq 0) "Core and Managed packages duplicate files outside LICENSE/VERSION/README: $($unexpectedOverlap -join ', ')"

    $councilInCore = @($coreFiles | Where-Object { $_ -like '*/Council.*.ps1' -or $_ -like '*/Invoke-CouncilReview.ps1' -or $_ -like '*/Test-Council*.ps1' -or $_ -like '*/council-*' })
    Assert-True ($councilInCore.Count -eq 0) "Core package must not contain Council files: $($councilInCore -join ', ')"

    $taskInCore = @($coreFiles | Where-Object { $_ -like 'global/skills/1c-task/*' -and $_ -notin @('global/skills/1c-task/references/stage-contract.md','global/skills/1c-task/references/task-contract.md') })
    Assert-True ($taskInCore.Count -eq 0) "Core package must not contain the 1c-task skill: $($taskInCore -join ', ')"

    $managedManifest = Get-BFZipJson -ZipPath $managedZip -EntryName 'package-manifest.json'
    Assert-True ([string]$managedManifest.requires_core -eq $version) "Managed manifest requires_core ('$($managedManifest.requires_core)') must equal VERSION ('$version')."
    $coreManifest = Get-BFZipJson -ZipPath $coreZip -EntryName 'package-manifest.json'
    Assert-True ($null -eq $coreManifest.requires_core) "Core manifest requires_core must be null, was: $($coreManifest.requires_core)"

    # --- Ф2.2: L/high-risk review must fail closed, not crash, when Council is absent ---
    $extractRoot = Join-Path $testRoot 'core-extract'
    [IO.Compression.ZipFile]::ExtractToDirectory($coreZip, $extractRoot)
    $installedSkillRoot=Join-Path $extractRoot 'global/skills'
    foreach ($file in Get-ChildItem $installedSkillRoot -File -Recurse -Filter '*.md') {
        foreach ($match in [regex]::Matches((Get-Content -Raw $file.FullName),'\]\(([^)\s]+)\)')) {
            $target=$match.Groups[1].Value.Split('#')[0]
            if (-not $target -or $target -match '^(https?|mailto):') { continue }
            $resolved=[IO.Path]::GetFullPath((Join-Path $file.DirectoryName $target))
            Assert-True ($resolved.StartsWith($installedSkillRoot+[IO.Path]::DirectorySeparatorChar,[StringComparison]::OrdinalIgnoreCase) -and (Test-Path $resolved -PathType Leaf)) "Broken Core skill link: $($file.Name) -> $target"
        }
    }
    $reviewScript = Join-Path $extractRoot 'global\skills\1c-spec-review\scripts\Invoke-1CSpecReview.ps1'
    Assert-True (Test-Path -LiteralPath $reviewScript -PathType Leaf) "Extracted Core package is missing Invoke-1CSpecReview.ps1: $reviewScript"

    $fixtureRoot = Join-Path $testRoot 'fixture-project'
    New-Item -ItemType Directory -Path (Join-Path $fixtureRoot 'src') -Force | Out-Null
    New-Item -ItemType Directory -Path (Join-Path $fixtureRoot 'openspec\changes\demo-l-change') -Force | Out-Null
Set-Content -LiteralPath (Join-Path $fixtureRoot 'openspec\config.yaml') -Value 'schema: bsl-flow' -Encoding utf8
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

## Uncertainties / assumptions
The fixture has no business uncertainty.
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

    # An explicit owner decision is bound to these exact inputs and remains limited.
    $change = Join-Path $fixtureRoot 'openspec/changes/demo-l-change'
    $override = @{owner_override=@{owner='Fixture owner';reason='Offline package boundary test';accepted_risks='No Council review';spec_sha256=(Get-FileHash (Join-Path $change 'spec.md')).Hash.ToLowerInvariant();original_task_sha256=(Get-FileHash (Join-Path $change 'original-task.md')).Hash.ToLowerInvariant();design_sha256=$null}}
    $override | ConvertTo-Json -Depth 5 | Set-Content (Join-Path $change 'review-reconciliation.json')
    $limited = & $reviewScript -ProjectPath $fixtureRoot -ChangeName demo-l-change
    Assert-True ($limited.Verdict -eq 'PASS_WITH_LIMITATIONS') 'Owner override produced an unlimited PASS.'
    $final = Get-Content -Raw (Join-Path $change 'final-validation.json') | ConvertFrom-Json
    Assert-True ($final.limitations -contains 'owner_override_without_council') 'Override limitation missing from final receipt.'
    Copy-Item -Path (Join-Path $root 'scripts/fixtures/metadata/designer-mini/*') -Destination (Join-Path $fixtureRoot 'src') -Recurse -Force
    Set-Content (Join-Path $change 'spec.md') ($specText.Replace('Touches core catalogs.','Touches Справочник.Наменклатура.'))
    $override.owner_override.spec_sha256=(Get-FileHash (Join-Path $change 'spec.md')).Hash.ToLowerInvariant()
    $override | ConvertTo-Json -Depth 5 | Set-Content (Join-Path $change 'review-reconciliation.json')
    $groundingFailure=$null
    try { & $reviewScript -ProjectPath $fixtureRoot -ChangeName demo-l-change | Out-Null } catch { $groundingFailure=$_.Exception.Message }
    $failedFinal=Get-Content -Raw (Join-Path $change 'final-validation.json') | ConvertFrom-Json
    Assert-True ($null -ne $groundingFailure -and -not $failedFinal.passed -and -not $failedFinal.grounding.passed) 'Fresh grounding did not block a typo after earlier PASS.'
    Add-Content (Join-Path $change 'spec.md') 'Changed after owner decision.'
    $staleMessage = $null
    try { & $reviewScript -ProjectPath $fixtureRoot -ChangeName demo-l-change | Out-Null } catch { $staleMessage=$_.Exception.Message }
    Assert-True ($staleMessage -like '*not bound to current spec.md*') 'Stale owner override was accepted.'

    # S lint works in the extracted Core without any Council module.
    $sSpec=$specText.Replace('Complexity: L','Complexity: S').Replace('Risk: high','Risk: low')
    Set-Content (Join-Path $change 'spec.md') $sSpec
    $small=& $reviewScript -ProjectPath $fixtureRoot -ChangeName demo-l-change
    Assert-True ($small.LintPassed -and $small.ReviewMode -eq 'lint') 'Core S lint is not standalone.'

    # The API provider must work using only extracted Core files (mocked transport).
    . (Join-Path $extractRoot 'global/skills/1c-spec-review/scripts/Review.Providers.ps1')
    $routing=@{allow_local_http=$false;providers=@{fixture=@{protocol='openai_compatible';token_env='BSL_FLOW_CORE_TEST_TOKEN';endpoint=@{scheme='https';host='example.invalid';port=443;base_path='/'}}};models=@{'review-fast'=@{provider='fixture';model='fixture';effort='low'}}}
    $previousToken=$env:BSL_FLOW_CORE_TEST_TOKEN
    try {
        $env:BSL_FLOW_CORE_TEST_TOKEN='fixture-token'
        $mockSend={ param($u,$b,$t,$to,$m,$c) [pscustomobject]@{status=200;body='{"model":"fixture","choices":[{"finish_reason":"stop","message":{"content":"{\"reviewer_verdict\":\"PASS\"}"}}]}'}}
        $api=Invoke-BSLFlowApiSingleReview -ProjectRoot $fixtureRoot -ContextEnvelope 'fixture' -Model 'review-fast' -CouncilRouting $routing -TimeoutSeconds 5 -MaxOutputBytes 4096 -RawResponsePath (Join-Path $testRoot 'api.txt') -AttemptDir (Join-Path $testRoot 'api') -HttpSend $mockSend
        Assert-True (-not $api.Failed -and $api.RawReview.reviewer_verdict -eq 'PASS') 'Core API provider needs missing Council files.'
    }
    finally { $env:BSL_FLOW_CORE_TEST_TOKEN=$previousToken }

    $managedExtract=Join-Path $testRoot 'managed-extract'
    [IO.Compression.ZipFile]::ExtractToDirectory($managedZip,$managedExtract)
    & (Join-Path $root 'scripts/Test-InstallCore.ps1') -PackageRoot $root -CorePackageRoot $extractRoot -ManagedPackageRoot $managedExtract

    $tamperTarget=Join-Path $extractRoot 'global/skills/1c-spec/SKILL.md'
    Add-Content $tamperTarget 'tampered'
    $blockedTarget=Join-Path $testRoot 'tamper-install'
    $tamperMessage=$null
    try { & (Join-Path $extractRoot 'scripts/Install-BSLFlowCore.ps1') -Host agents -SkillsRoot (Join-Path $blockedTarget 'skills') -MarkerPath (Join-Path $blockedTarget 'installed-core.json') -OpenSpecSchemaRoot (Join-Path $blockedTarget 'schema') -SkipCliValidation | Out-Null } catch { $tamperMessage=$_.Exception.Message }
    Assert-True ($tamperMessage -like '*Package integrity mismatch*' -and -not (Test-Path $blockedTarget)) 'Tampered Core wrote installation state.'

    $fullOne=& $buildScript -PackageRoot $root -OutputPath (Join-Path $testRoot 'full-default.zip')
    $fullTwo=& $buildScript -PackageRoot $root -Package full -OutputPath (Join-Path $testRoot 'full-explicit.zip')
    Assert-True ($fullOne.Sha256 -eq $fullTwo.Sha256) 'Default and explicit full packages differ.'
}
finally {
    $resolved=[IO.Path]::GetFullPath($testRoot)
    $tempPrefix=[IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\','/')+[IO.Path]::DirectorySeparatorChar
    if (-not $resolved.StartsWith($tempPrefix,[StringComparison]::OrdinalIgnoreCase) -or (Split-Path $resolved -Leaf) -notlike 'bsl-flow-core-package-test-*') { throw "Unsafe package-test cleanup: $resolved" }
    if (Test-Path -LiteralPath $resolved) { Remove-Item -LiteralPath $resolved -Recurse -Force -ErrorAction SilentlyContinue }
}

Write-Host 'Test-CorePackage: OK'
