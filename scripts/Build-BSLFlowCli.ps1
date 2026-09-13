#Requires -Version 7.0
[CmdletBinding()]
param([string]$PackageRoot, [string]$OutputPath, [string]$GoPath, [switch]$Test)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
if (-not $PackageRoot) { $PackageRoot = Split-Path -Parent $PSScriptRoot }
$root = [IO.Path]::GetFullPath($PackageRoot)
$cliRoot = Join-Path $root 'cli'
if (-not $GoPath) {
    $goCommand = Get-Command go -ErrorAction SilentlyContinue
    if ($goCommand) { $GoPath = $goCommand.Source }
    elseif (Test-Path -LiteralPath 'C:\Program Files\Go\bin\go.exe') { $GoPath = 'C:\Program Files\Go\bin\go.exe' }
    else { throw 'Go toolchain not found. Install Go explicitly, then rerun with -GoPath if necessary.' }
}
$GoPath = [IO.Path]::GetFullPath($GoPath)
if (-not $OutputPath) { $OutputPath = Join-Path $cliRoot 'bin\bsl-flow.exe' }
$OutputPath = [IO.Path]::GetFullPath($OutputPath)
$version = [IO.File]::ReadAllText((Join-Path $root 'VERSION')).Trim()
if ($version -notmatch '^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$') { throw 'Invalid VERSION.' }
$resourceRoot = Join-Path $cliRoot 'internal\resources'
[void][IO.Directory]::CreateDirectory($resourceRoot)
[void][IO.Directory]::CreateDirectory((Split-Path -Parent $OutputPath))
$bundlePath = Join-Path $resourceRoot 'bundle.zip'
$utf8 = [Text.UTF8Encoding]::new($false)
[IO.File]::WriteAllText((Join-Path $resourceRoot 'version.txt'), $version + "`n", $utf8)
$files = [string[]]@('VERSION') + [string[]]@(Get-ChildItem -LiteralPath (Join-Path $root 'global') -File -Recurse -Force | ForEach-Object { $_.FullName.Substring($root.Length + 1).Replace('\', '/') })
[Array]::Sort($files, [StringComparer]::Ordinal)
$stream = [IO.File]::Open($bundlePath, [IO.FileMode]::Create, [IO.FileAccess]::ReadWrite, [IO.FileShare]::None)
try {
    $archive = [IO.Compression.ZipArchive]::new($stream, [IO.Compression.ZipArchiveMode]::Create, $true)
    try {
        foreach ($relative in $files) {
            $entry = $archive.CreateEntry($relative, [IO.Compression.CompressionLevel]::NoCompression)
            $entry.LastWriteTime = [DateTimeOffset]::new(2000, 1, 1, 0, 0, 0, [TimeSpan]::Zero)
            $destination = $entry.Open()
            try {
                $source = [IO.File]::OpenRead((Join-Path $root $relative))
                try { $source.CopyTo($destination) } finally { $source.Dispose() }
            } finally { $destination.Dispose() }
        }
    } finally { $archive.Dispose() }
} finally { $stream.Dispose() }

# Build only from this checkout, without module/toolchain downloads or a parent go.work.
$settings = @{ GOOS='windows'; GOARCH='amd64'; CGO_ENABLED='0'; GOTOOLCHAIN='local'; GOPROXY='off'; GOSUMDB='off'; GOWORK='off'; GOENV='off'; GOCACHE=(Join-Path $cliRoot '.cache\build'); GOMODCACHE=(Join-Path $cliRoot '.cache\mod') }
$saved = @{}
foreach ($key in $settings.Keys) { $saved[$key] = [Environment]::GetEnvironmentVariable($key, 'Process'); [Environment]::SetEnvironmentVariable($key, $settings[$key], 'Process') }
Push-Location $cliRoot
try {
    if ($Test) {
        # The public lifecycle integration tests spawn real provider processes
        # and can legitimately exceed Go's 10m per-package default under load.
        & $GoPath test -count=1 -timeout 45m ./...
        if ($LASTEXITCODE -ne 0) { throw "Go tests failed: $LASTEXITCODE" }
    }
    & $GoPath build -trimpath -buildvcs=false '-ldflags=-buildid=' -o $OutputPath .
    if ($LASTEXITCODE -ne 0) { throw "Go build failed: $LASTEXITCODE" }
    if ($Test) {
        $comparisonPath = Join-Path (Split-Path -Parent $OutputPath) ('bsl-flow-repro-' + [guid]::NewGuid().ToString('N') + '.exe')
        try {
            & $GoPath build -trimpath -buildvcs=false '-ldflags=-buildid=' -o $comparisonPath .
            if ($LASTEXITCODE -ne 0) { throw "Reproducibility build failed: $LASTEXITCODE" }
            if ((Get-FileHash -LiteralPath $OutputPath -Algorithm SHA256).Hash -ne (Get-FileHash -LiteralPath $comparisonPath -Algorithm SHA256).Hash) { throw 'Repeated build from the same embedded snapshot produced different bytes.' }
        } finally { if (Test-Path -LiteralPath $comparisonPath -PathType Leaf) { Remove-Item -LiteralPath $comparisonPath -Force } }
    }
} finally {
    Pop-Location
    foreach ($key in $saved.Keys) { [Environment]::SetEnvironmentVariable($key, $saved[$key], 'Process') }
}
$sha = (Get-FileHash -LiteralPath $OutputPath -Algorithm SHA256).Hash.ToLowerInvariant()
[IO.File]::WriteAllText(($OutputPath + '.sha256'), "$sha  $([IO.Path]::GetFileName($OutputPath))`n", $utf8)
[pscustomobject]@{ Version=$version; Executable=$OutputPath; Sha256=$sha; BundleSha256=(Get-FileHash -LiteralPath $bundlePath -Algorithm SHA256).Hash.ToLowerInvariant(); BundleFileCount=$files.Count }
