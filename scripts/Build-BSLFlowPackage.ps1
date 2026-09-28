#Requires -Version 7.0
[CmdletBinding()]
param(
    [string]$PackageRoot,
    [string]$OutputPath,
    [ValidateSet('full', 'core', 'managed')]
    [string]$Package = 'full',
    [switch]$Test
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Test-BFGlobMatch {
    param([Parameter(Mandatory)][string]$RelativePath, [Parameter(Mandatory)][string]$Pattern)
    # ** behaves like * here because $RelativePath is already a flat, forward-slash
    # relative file path (no directory traversal is being matched against).
    $wildcard = $Pattern -replace '\*\*', '*'
    return $RelativePath -like $wildcard
}

function Select-BFPackageManifestFiles {
    param(
        [Parameter(Mandatory)][string[]]$RelativePaths,
        [Parameter(Mandatory)][string]$ManifestPath
    )
    if (-not (Test-Path -LiteralPath $ManifestPath -PathType Leaf)) { throw "Package manifest not found: $ManifestPath" }
    $definition = Get-Content -Raw -LiteralPath $ManifestPath | ConvertFrom-Json -ErrorAction Stop
    $includePatterns = [string[]]@($definition.include)
    $excludePatterns = [string[]]@($definition.exclude)
    if ($includePatterns.Count -eq 0) { throw "Package manifest declares no include patterns: $ManifestPath" }
    $selected = foreach ($relative in $RelativePaths) {
        $included = $false
        foreach ($pattern in $includePatterns) { if (Test-BFGlobMatch -RelativePath $relative -Pattern $pattern) { $included = $true; break } }
        if (-not $included) { continue }
        $excluded = $false
        foreach ($pattern in $excludePatterns) { if (Test-BFGlobMatch -RelativePath $relative -Pattern $pattern) { $excluded = $true; break } }
        if ($excluded) { continue }
        $relative
    }
    return [pscustomobject]@{ Paths = [string[]]@($selected); Definition = $definition }
}

function Remove-PackageTestTree {
    param([Parameter(Mandatory)][string]$Path)
    $resolved = [IO.Path]::GetFullPath($Path).TrimEnd('\', '/')
    $temp = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\', '/')
    if (-not $resolved.StartsWith($temp + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase) -or (Split-Path -Leaf $resolved) -notmatch '^bfp-[0-9a-f]{16}$') {
        throw "Unsafe package test cleanup target: $resolved"
    }
    if (Test-Path -LiteralPath $resolved) { Remove-Item -LiteralPath $resolved -Recurse -Force -ErrorAction SilentlyContinue }
}

if ([string]::IsNullOrWhiteSpace($PackageRoot)) { $PackageRoot = Split-Path -Parent $PSScriptRoot }
$root = [IO.Path]::GetFullPath($PackageRoot).TrimEnd('\', '/')
if (-not (Test-Path -LiteralPath $root -PathType Container)) { throw "Package root not found: $root" }
$versionPath = Join-Path $root 'VERSION'
if (-not (Test-Path -LiteralPath $versionPath -PathType Leaf)) { throw "VERSION not found: $versionPath" }
$version = (Get-Content -Raw -LiteralPath $versionPath).Trim()
if ($version -notmatch '^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$') { throw "Invalid package VERSION: $version" }

# Stage A: the ADR index is validated and bound into the package identity.
# A damaged index link fails the build before any archive is produced.
$architectureScripts = Join-Path $root 'global/skills/1c-task/scripts'
. (Join-Path $architectureScripts 'Task.Storage.ps1')
. (Join-Path $architectureScripts 'Task.Architecture.ps1')
$adrIndexRelative = 'docs/architecture/adr-index.json'
$adrSchemaRelative = 'docs/architecture/adr-index.schema.json'
$adrSourceRelative = 'docs/ARCHITECTURE_RU.md'
foreach ($relative in @($adrIndexRelative, $adrSchemaRelative, $adrSourceRelative)) {
    if (-not (Test-Path -LiteralPath (Join-Path $root $relative) -PathType Leaf)) { throw "Missing architecture file: $relative" }
}
$adrIndex = Read-BFArchitectureIndex $root
Assert-BFADRIndex $adrIndex $root | Out-Null
$adrIndexCanonical = Get-BFArchitectureIndexHash $adrIndex
if ([string]::IsNullOrWhiteSpace($OutputPath)) {
    $defaultName = if ($Package -eq 'full') { "BSL-Flow-$version.zip" } else { "BSL-Flow-$Package-$version.zip" }
    $OutputPath = Join-Path $root "outputs\$defaultName"
}
$zipPath = [IO.Path]::GetFullPath($OutputPath)
$zipHashPath = $zipPath + '.sha256'
$zipParent = Split-Path -Parent $zipPath
New-Item -ItemType Directory -Path $zipParent -Force | Out-Null

$excludedRootSegments = @('.bsl-flow', '.build', '.claude', 'work', 'outputs')
$excludedRootFiles = @()
$packageFiles = foreach ($entry in (Get-ChildItem -LiteralPath $root -Force)) {
    if ($entry.PSIsContainer) {
        if ($entry.Name -in @($excludedRootSegments + '.git')) { continue }
        Get-ChildItem -LiteralPath $entry.FullName -File -Recurse -Force
    } else {
        $entry
    }
}
$relativePaths = [string[]]@($packageFiles | Where-Object {
    $relative = $_.FullName.Substring($root.Length + 1)
    $segments = @($relative -split '[\\/]')
    $generatedRootArtifact = $segments.Count -eq 1 -and ($segments[0] -eq 'package-manifest.json' -or $segments[0] -like '*.zip' -or $segments[0] -like '*.zip.sha256')
    $_.FullName -ne $zipPath -and $_.FullName -ne $zipHashPath -and -not $generatedRootArtifact -and $segments[0] -notin $excludedRootFiles -and $segments[0] -notin $excludedRootSegments -and '.git' -notin $segments
} | ForEach-Object { $_.FullName.Substring($root.Length + 1).Replace('\', '/') })
if ($relativePaths.Count -eq 0) { throw 'No package files selected.' }
[Array]::Sort($relativePaths, [StringComparer]::Ordinal)

$packageManifestDefinition = $null
if ($Package -ne 'full') {
    $manifestDefinitionPath = Join-Path $root "packaging\$Package.json"
    $selection = Select-BFPackageManifestFiles -RelativePaths $relativePaths -ManifestPath $manifestDefinitionPath
    $relativePaths = $selection.Paths
    $packageManifestDefinition = $selection.Definition
    if ($relativePaths.Count -eq 0) { throw "No files matched the $Package package manifest: $manifestDefinitionPath" }
    [Array]::Sort($relativePaths, [StringComparer]::Ordinal)
}

$manifestFiles = foreach ($relative in $relativePaths) {
    $file = Get-Item -LiteralPath (Join-Path $root $relative) -Force
    [ordered]@{ path = $relative; size_bytes = [int64]$file.Length; sha256 = (Get-FileHash -LiteralPath $file.FullName -Algorithm SHA256).Hash.ToLowerInvariant() }
}
$includesArchitecture = ($relativePaths -contains $adrIndexRelative) -and ($relativePaths -contains $adrSchemaRelative) -and ($relativePaths -contains $adrSourceRelative)
if ($Package -ne 'managed' -and -not $includesArchitecture) {
    throw 'Architecture files are missing from the package file inventory.'
}
$manifest = [ordered]@{
    schema_version = 1
    package = $(if ($Package -eq 'full') { 'bsl-flow' } else { $Package })
    version = $version
}
if ($includesArchitecture) {
    $manifest.architecture = [ordered]@{
        adr_index_path = $adrIndexRelative
        adr_index_sha256 = (Get-FileHash -LiteralPath (Join-Path $root $adrIndexRelative) -Algorithm SHA256).Hash.ToLowerInvariant()
        adr_index_canonical_sha256 = $adrIndexCanonical
        adr_schema_path = $adrSchemaRelative
        adr_schema_sha256 = (Get-FileHash -LiteralPath (Join-Path $root $adrSchemaRelative) -Algorithm SHA256).Hash.ToLowerInvariant()
        adr_source_path = $adrSourceRelative
        adr_source_sha256 = (Get-FileHash -LiteralPath (Join-Path $root $adrSourceRelative) -Algorithm SHA256).Hash.ToLowerInvariant()
    }
}
if ($Package -eq 'managed') { $manifest.requires_core = $version }
elseif ($Package -eq 'core') { $manifest.requires_core = $null }
$manifest.files = @($manifestFiles)
$utf8 = [Text.UTF8Encoding]::new($false)
$manifestBytes = $utf8.GetBytes(($manifest | ConvertTo-Json -Depth 8 -Compress) + "`n")
$fixedTime = [DateTimeOffset]::new(2000, 1, 1, 0, 0, 0, [TimeSpan]::Zero)
$archivePaths = [string[]]@($relativePaths + 'package-manifest.json')
[Array]::Sort($archivePaths, [StringComparer]::Ordinal)

Add-Type -AssemblyName System.IO.Compression
Add-Type -AssemblyName System.IO.Compression.FileSystem
if (Test-Path -LiteralPath $zipPath -PathType Leaf) { Remove-Item -LiteralPath $zipPath -Force }
$stream = [IO.File]::Open($zipPath, [IO.FileMode]::CreateNew, [IO.FileAccess]::ReadWrite, [IO.FileShare]::None)
try {
    $archive = [IO.Compression.ZipArchive]::new($stream, [IO.Compression.ZipArchiveMode]::Create, $true)
    try {
        foreach ($archivePath in $archivePaths) {
            $entry = $archive.CreateEntry($archivePath, [IO.Compression.CompressionLevel]::NoCompression)
            $entry.LastWriteTime = $fixedTime
            $entryStream = $entry.Open()
            try {
                if ($archivePath -eq 'package-manifest.json') {
                    $entryStream.Write($manifestBytes, 0, $manifestBytes.Length)
                }
                else {
                    $source = [IO.File]::OpenRead((Join-Path $root $archivePath))
                    try { $source.CopyTo($entryStream) } finally { $source.Dispose() }
                }
            }
            finally { $entryStream.Dispose() }
        }
    }
    finally { $archive.Dispose() }
}
finally { $stream.Dispose() }

$zipHash = (Get-FileHash -LiteralPath $zipPath -Algorithm SHA256).Hash.ToLowerInvariant()
$hashPath = $zipHashPath
[IO.File]::WriteAllText($hashPath, "$zipHash  $([IO.Path]::GetFileName($zipPath))`n", $utf8)

if ($Test) {
    # Git for Windows still bounds some internal worktree paths even with core.longpaths.
    # Leave room for suites that create their own repositories beneath the extracted package.
    $testRoot = Join-Path ([IO.Path]::GetTempPath()) ('bfp-' + [guid]::NewGuid().ToString('N').Substring(0, 16))
    try {
        $comparisonZip = Join-Path $testRoot 'rebuild.zip'
        New-Item -ItemType Directory -Path $testRoot -Force | Out-Null
        $null = & $MyInvocation.MyCommand.Path -PackageRoot $root -OutputPath $comparisonZip -Package $Package
        $comparisonHash = (Get-FileHash -LiteralPath $comparisonZip -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($comparisonHash -ne $zipHash) { throw 'Reproducible rebuild produced a different ZIP SHA-256.' }
        Remove-Item -LiteralPath $comparisonZip, ($comparisonZip + '.sha256') -Force
        [IO.Compression.ZipFile]::ExtractToDirectory($zipPath, $testRoot)
        $extractedManifestPath = Join-Path $testRoot 'package-manifest.json'
        $extractedManifest = Get-Content -Raw -LiteralPath $extractedManifestPath | ConvertFrom-Json -ErrorAction Stop
        if ($extractedManifest.version -ne $version) { throw 'Extracted package manifest version mismatch.' }
        $expectedPaths = [string[]]@($extractedManifest.files | ForEach-Object { [string]$_.path })
        $actualPaths = [string[]]@(Get-ChildItem -LiteralPath $testRoot -File -Recurse -Force | Where-Object { $_.FullName -ne $extractedManifestPath } | ForEach-Object { $_.FullName.Substring($testRoot.Length + 1).Replace('\', '/') })
        [Array]::Sort($expectedPaths, [StringComparer]::Ordinal)
        [Array]::Sort($actualPaths, [StringComparer]::Ordinal)
        if ([bool](Compare-Object $expectedPaths $actualPaths)) { throw 'Extracted package file inventory differs from package-manifest.json.' }
        foreach ($record in @($extractedManifest.files)) {
            $path = Join-Path $testRoot ([string]$record.path)
            if ((Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant() -ne $record.sha256) { throw "Extracted package hash mismatch: $($record.path)" }
        }
        if ($Package -eq 'full') {
            & (Join-Path $testRoot 'scripts\Test-BSLFlowPackage.ps1') -PackageRoot $testRoot
        }
    }
    finally {
        Remove-PackageTestTree -Path $testRoot
    }
}

[pscustomobject]@{ Package = $Package; Version = $version; ZipPath = $zipPath; Sha256 = $zipHash; Sha256Path = $hashPath; FileCount = $relativePaths.Count }
