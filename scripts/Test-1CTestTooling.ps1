[CmdletBinding()]
param([string]$PackageRoot)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($PackageRoot)) { $PackageRoot = Split-Path -Parent $PSScriptRoot }
$inventoryScript = Join-Path $PackageRoot 'global\skills\1c-init-project\scripts\Get-1CTestTooling.ps1'
$probeRoot = Join-Path ([System.IO.Path]::GetTempPath()) ('bsl-flow-tooling-test-' + [guid]::NewGuid().ToString('N'))
function Assert-Tooling([bool]$Condition, [string]$Message) { if (-not $Condition) { throw $Message } }
try {
    New-Item -ItemType Directory -Path $probeRoot | Out-Null
    $yax = Join-Path $probeRoot 'yax'
    $va = Join-Path $probeRoot 'va'
    $db = Join-Path $probeRoot 'ib'
    $empty = & $inventoryScript -YaxunitDirectory $yax -VanessaDirectory $va -TestDatabasePath $db
    Assert-Tooling (@($empty.artifacts | Where-Object local_status -ne 'missing').Count -eq 0) 'Missing catalogs were not reported.'
    Assert-Tooling (@($empty.artifacts | Where-Object { $_.catalog_present -or $_.missing_reason -ne 'catalog_missing' }).Count -eq 0) 'Missing catalog diagnostics are incomplete.'
    Assert-Tooling ($empty.artifacts[0].catalog_path -eq [IO.Path]::GetFullPath($yax)) 'Inventory did not report the effective YAxUnit catalog.'
    Assert-Tooling ($empty.artifacts[1].catalog_path -eq [IO.Path]::GetFullPath($va)) 'Inventory did not report the effective Vanessa catalog.'
    Assert-Tooling (-not (Test-Path -LiteralPath $yax)) 'Inventory created a missing directory.'
    $driveRoot = [IO.Path]::GetPathRoot($probeRoot)
    $rootProbe = & $inventoryScript -YaxunitDirectory $driveRoot -VanessaDirectory $va
    Assert-Tooling ($rootProbe.artifacts[0].catalog_path -eq $driveRoot) 'Drive root catalog was changed into a drive-relative path.'
    foreach ($folder in @($yax,$va,$db)) { New-Item -ItemType Directory -Path $folder | Out-Null }
    Set-Content -LiteralPath (Join-Path $yax 'Smoke-25.12.cfe') -Value 'not the test engine' -Encoding UTF8
    Set-Content -LiteralPath (Join-Path $va 'private.txt') -Value 'must not be inventoried' -Encoding UTF8
    $nested = Join-Path $yax 'nested-release'; New-Item -ItemType Directory -Path $nested | Out-Null
    Set-Content -LiteralPath (Join-Path $nested 'YAxUnit-nested.cfe') -Value 'must not be selected recursively' -Encoding UTF8
    $smokeOnly = & $inventoryScript -YaxunitDirectory $yax -VanessaDirectory $va
    Assert-Tooling ($smokeOnly.artifacts[0].local_status -eq 'missing') 'Smoke mistaken for YAxUnit engine.'
    Assert-Tooling ($smokeOnly.artifacts[0].catalog_present -and $smokeOnly.artifacts[0].missing_reason -eq 'no_matching_artifact') 'Present catalog without a top-level engine was misclassified.'
    Assert-Tooling ($smokeOnly.artifacts[0].search_scope -eq 'top_level_only' -and $smokeOnly.artifacts[0].file_pattern -eq 'YAxUnit*.cfe') 'Inventory did not disclose its bounded search contract.'
    Set-Content -LiteralPath (Join-Path $yax 'YAxUnit-25.12.cfe') -Value 'fixture only, not valid CFE' -Encoding UTF8
    Set-Content -LiteralPath (Join-Path $va 'vanessa-automation.epf') -Value 'fixture EPF' -Encoding UTF8
    Set-Content -LiteralPath (Join-Path $va 'VAExtension.1.29.cfe') -Value 'fixture CFE' -Encoding UTF8
    Set-Content -LiteralPath (Join-Path $va 'client_mcp.cfe') -Value 'fixture CFE' -Encoding UTF8
    New-Item -ItemType File -Path (Join-Path $db '1Cv8.1CD') | Out-Null
    $before = @(Get-ChildItem -LiteralPath $probeRoot -Recurse -File | ForEach-Object { $_.FullName + '|' + (Get-FileHash -LiteralPath $_.FullName).Hash } | Sort-Object)
    $found = & $inventoryScript -YaxunitDirectory $yax -VanessaDirectory $va -TestDatabasePath $db
    $again = & $inventoryScript -YaxunitDirectory $yax -VanessaDirectory $va -TestDatabasePath $db
    $after = @(Get-ChildItem -LiteralPath $probeRoot -Recurse -File | ForEach-Object { $_.FullName + '|' + (Get-FileHash -LiteralPath $_.FullName).Hash } | Sort-Object)
    Assert-Tooling (-not [bool](Compare-Object $before $after)) 'Inventory changed inputs.'
    Assert-Tooling (@($found.artifacts | Where-Object local_status -ne 'file_found_unverified').Count -eq 0) 'Existing binaries not inventoried.'
    Assert-Tooling (@($found.artifacts | Where-Object installed_in_database -ne 'unknown').Count -eq 0) 'Local files asserted database installation.'
    Assert-Tooling ($found.database.file_database_present -and $found.database.extensions -eq 'unknown' -and -not $found.runtime_verified) 'File marker asserted runtime readiness.'
    Assert-Tooling (($found.artifacts | ConvertTo-Json -Depth 8) -eq ($again.artifacts | ConvertTo-Json -Depth 8)) 'Repeated inventory was not stable.'
    Assert-Tooling (($found | ConvertTo-Json -Depth 8) -notmatch 'private.txt|must not be inventoried') 'Unrelated text leaked into inventory.'
    Set-Content -LiteralPath (Join-Path $yax 'YAxUnit-26.01.cfe') -Value 'second candidate' -Encoding UTF8
    $multiple = & $inventoryScript -YaxunitDirectory $yax -VanessaDirectory $va
    Assert-Tooling ($multiple.artifacts[0].local_status -eq 'ambiguous') 'Multiple versions were silently selected.'
    Clear-Content -LiteralPath (Join-Path $va 'vanessa-automation.epf')
    $zero = & $inventoryScript -YaxunitDirectory $yax -VanessaDirectory $va
    Assert-Tooling ($zero.artifacts[1].local_status -eq 'empty_file') 'Empty artifact accepted.'
    foreach ($relativePath in @('relative-ib', 'C:relative-ib', '\relative-ib')) {
        $relativeRejected = $false
        try { & $inventoryScript -YaxunitDirectory $yax -VanessaDirectory $va -TestDatabasePath $relativePath | Out-Null }
        catch { $relativeRejected = $_.Exception.Message -match 'absolute filesystem' }
        Assert-Tooling $relativeRejected 'Relative database target accepted.'
    }
    foreach ($relativeCatalog in @('relative-tools', 'C:relative-tools', '\relative-tools')) {
        $relativeRejected = $false
        try { & $inventoryScript -YaxunitDirectory $relativeCatalog -VanessaDirectory $va | Out-Null }
        catch { $relativeRejected = $_.Exception.Message -match 'absolute filesystem' }
        Assert-Tooling $relativeRejected 'Relative tool catalog accepted.'
    }
    Write-Host 'Test-tooling inventory contracts passed; no 1C process was started.'
}
finally {
    $resolved = [System.IO.Path]::GetFullPath($probeRoot)
    $tempPrefix = [System.IO.Path]::GetFullPath([System.IO.Path]::GetTempPath()).TrimEnd('\','/') + [System.IO.Path]::DirectorySeparatorChar
    if ($resolved.StartsWith($tempPrefix, [StringComparison]::OrdinalIgnoreCase) -and (Split-Path -Leaf $resolved) -like 'bsl-flow-tooling-test-*') {
        Remove-Item -LiteralPath $resolved -Recurse -Force
    }
}
