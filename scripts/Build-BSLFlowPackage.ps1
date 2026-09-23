#Requires -Version 7.0
[CmdletBinding()]
param(
    [string]$PackageRoot,
    [string]$OutputPath,
    [switch]$Test
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Remove-PackageTestTree {
    param([Parameter(Mandatory)][string]$Path)
    $resolved = [IO.Path]::GetFullPath($Path).TrimEnd('\', '/')
    $temp = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\', '/')
    if (-not $resolved.StartsWith($temp + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase) -or (Split-Path -Leaf $resolved) -notlike 'bsl-flow-package-extract-*') {
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
if ([string]::IsNullOrWhiteSpace($OutputPath)) { $OutputPath = Join-Path $root "outputs\BSL-Flow-$version.zip" }
$zipPath = [IO.Path]::GetFullPath($OutputPath)
$zipHashPath = $zipPath + '.sha256'
$zipParent = Split-Path -Parent $zipPath
New-Item -ItemType Directory -Path $zipParent -Force | Out-Null

$excludedRootSegments = @('.bsl-flow', '.build', 'work', 'outputs')
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

$manifestFiles = foreach ($relative in $relativePaths) {
    $file = Get-Item -LiteralPath (Join-Path $root $relative)
    [ordered]@{ path = $relative; size_bytes = [int64]$file.Length; sha256 = (Get-FileHash -LiteralPath $file.FullName -Algorithm SHA256).Hash.ToLowerInvariant() }
}
if ($relativePaths -notcontains $adrIndexRelative -or $relativePaths -notcontains $adrSchemaRelative -or $relativePaths -notcontains $adrSourceRelative) {
    throw 'Architecture files are missing from the package file inventory.'
}
$manifest = [ordered]@{
    schema_version = 1
    package = 'bsl-flow'
    version = $version
    architecture = [ordered]@{
        adr_index_path = $adrIndexRelative
        adr_index_sha256 = (Get-FileHash -LiteralPath (Join-Path $root $adrIndexRelative) -Algorithm SHA256).Hash.ToLowerInvariant()
        adr_index_canonical_sha256 = $adrIndexCanonical
        adr_schema_path = $adrSchemaRelative
        adr_schema_sha256 = (Get-FileHash -LiteralPath (Join-Path $root $adrSchemaRelative) -Algorithm SHA256).Hash.ToLowerInvariant()
        adr_source_path = $adrSourceRelative
        adr_source_sha256 = (Get-FileHash -LiteralPath (Join-Path $root $adrSourceRelative) -Algorithm SHA256).Hash.ToLowerInvariant()
    }
    files = @($manifestFiles)
}
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
    $testRoot = Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-package-extract-' + [guid]::NewGuid().ToString('N'))
    try {
        $comparisonZip = Join-Path $testRoot 'rebuild.zip'
        New-Item -ItemType Directory -Path $testRoot -Force | Out-Null
        $null = & $MyInvocation.MyCommand.Path -PackageRoot $root -OutputPath $comparisonZip
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
        & (Join-Path $testRoot 'scripts\Test-BSLFlowPackage.ps1') -PackageRoot $testRoot
    }
    finally {
        Remove-PackageTestTree -Path $testRoot
    }
}

[pscustomobject]@{ Version = $version; ZipPath = $zipPath; Sha256 = $zipHash; Sha256Path = $hashPath; FileCount = $relativePaths.Count }
