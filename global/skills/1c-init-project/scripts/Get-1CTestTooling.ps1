[CmdletBinding()]
param(
    [string]$YaxunitDirectory = 'C:\YAxUnit',
    [string]$VanessaDirectory = 'C:\vanessa-automation',
    [string]$TestDatabasePath
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Get-ArtifactInventory {
    param([string]$Component, [string]$Directory, [string]$Pattern, [string]$Purpose)
    if ([string]::IsNullOrWhiteSpace($Directory) -or $Directory -notmatch '^(?:[A-Za-z]:[\\/]|[\\/]{2}[^\\/]+[\\/][^\\/]+(?:[\\/]|$))') {
        throw "$Component catalog must be an absolute filesystem directory."
    }
    $fullCatalogPath = [System.IO.Path]::GetFullPath($Directory)
    $catalogRoot = [System.IO.Path]::GetPathRoot($fullCatalogPath)
    $catalogPath = if ($fullCatalogPath.TrimEnd([char]'\', [char]'/') -eq $catalogRoot.TrimEnd([char]'\', [char]'/')) {
        $catalogRoot
    }
    else {
        $fullCatalogPath.TrimEnd([char]'\', [char]'/')
    }
    $catalogPresent = Test-Path -LiteralPath $catalogPath -PathType Container
    $candidates = @()
    if ($catalogPresent) {
        # Only top-level, named binary candidates. Never inspect arbitrary local text/secrets.
        $candidates = @(Get-ChildItem -LiteralPath $catalogPath -File -Filter $Pattern |
            Sort-Object Name | ForEach-Object {
                [pscustomobject]@{
                    path = $_.FullName
                    bytes = $_.Length
                    sha256 = (Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
                }
            })
    }
    $status = 'missing'
    if ($candidates.Count -gt 1) { $status = 'ambiguous' }
    elseif ($candidates.Count -eq 1) {
        $status = if ($candidates[0].bytes -gt 0) { 'file_found_unverified' } else { 'empty_file' }
    }
    [pscustomobject]@{
        component = $Component
        purpose = $Purpose
        catalog_path = $catalogPath
        catalog_present = $catalogPresent
        search_scope = 'top_level_only'
        file_pattern = $Pattern
        local_status = $status
        missing_reason = if ($status -ne 'missing') { $null } elseif ($catalogPresent) { 'no_matching_artifact' } else { 'catalog_missing' }
        candidates = $candidates
        installed_in_database = 'unknown'
    }
}

$artifacts = @(
    Get-ArtifactInventory 'yaxunit' $YaxunitDirectory 'YAxUnit*.cfe' 'unit_and_integration_engine'
    Get-ArtifactInventory 'vanessa' $VanessaDirectory 'vanessa-automation*.epf' 'external_scenario_runner'
    Get-ArtifactInventory 'vaextension' $VanessaDirectory 'VAExtension*.cfe' 'client_extension_when_required'
    Get-ArtifactInventory 'client_mcp' $VanessaDirectory 'client_mcp.cfe' 'optional_mcp_profile_only'
)
$databasePath = $null
$fileDatabasePresent = $false
if (-not [string]::IsNullOrWhiteSpace($TestDatabasePath)) {
    # IsPathRooted also accepts C:relative and \relative on Windows; require drive/UNC qualification.
    if ($TestDatabasePath -notmatch '^(?:[A-Za-z]:[\\/]|[\\/]{2}[^\\/]+[\\/][^\\/]+(?:[\\/]|$))') { throw 'TestDatabasePath must be an absolute filesystem directory, not a connection string.' }
    $databasePath = [System.IO.Path]::GetFullPath($TestDatabasePath)
    $fileDatabasePresent = Test-Path -LiteralPath (Join-Path $databasePath '1Cv8.1CD') -PathType Leaf
}

[pscustomobject]@{
    schema_version = 1
    checked_at_utc = [DateTime]::UtcNow.ToString('o')
    check_kind = 'local_files_only'
    artifacts = $artifacts
    database = [pscustomobject]@{
        path = $databasePath
        file_database_present = $fileDatabasePresent
        extensions = 'unknown'
        authentication = 'not_checked'
        active_sessions = 'not_checked'
    }
    runtime_verified = $false
    next_step = 'Inspect the actual database extension list and compatibility through an authorized supported route; do not treat unknown as absent.'
}
