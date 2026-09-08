[CmdletBinding()]
param(
    [string]$ProjectRoot = (Get-Location).Path,
    [Parameter(Mandatory)][ValidateNotNullOrEmpty()][string[]]$SourceRoots,
    [Alias('Output')][string]$OutputPath,
    [switch]$NoThrow
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Add-EIResult {
    param([System.Collections.ArrayList]$Results,[string]$Name,[string]$Status,[string]$Message)
    [void]$Results.Add([pscustomobject]@{name=$Name;status=$Status;message=$Message})
}

$root = [System.IO.Path]::GetFullPath($ProjectRoot)
$results = New-Object System.Collections.ArrayList
$identities = @{}
$files = @()
$selectedRoots = @{}
$errors = New-Object System.Collections.ArrayList
try {
    foreach ($relative in $SourceRoots) {
        if ([string]::IsNullOrWhiteSpace($relative)) { continue }
        $source = if ([System.IO.Path]::IsPathRooted($relative)) { [System.IO.Path]::GetFullPath($relative) } else { Join-Path $root $relative }
        if (-not (Test-Path -LiteralPath $source -PathType Container)) { [void]$errors.Add("Selected extension source root is missing: $source"); continue }
        $selected = @(Get-ChildItem -LiteralPath $source -Recurse -File -Filter '*.xml' | Where-Object { $_.Name -ne 'ConfigDumpInfo.xml' } | Sort-Object FullName)
        if ($selected.Count -eq 0) { [void]$errors.Add("Selected extension source root contains no owned metadata XML: $source"); continue }
        $files += $selected
        $selectedRoots[$source] = @($selected.FullName)
        Add-EIResult $results $relative 'selected' "$($selected.Count) metadata XML file(s); ConfigDumpInfo.xml excluded as generated sidecar."
    }
    if ($files.Count -eq 0) { throw 'No selected extension source files were available; zero-files is not a PASS.' }
    if ($errors.Count -gt 0) { throw 'Not every selected extension root could be checked.' }
    $files = @($files | Sort-Object FullName -Unique)
    $ownedFiles = @{}
    foreach ($file in $files) {
        try {
            $settings = [Xml.XmlReaderSettings]::new()
            $settings.DtdProcessing = [Xml.DtdProcessing]::Prohibit
            $settings.XmlResolver = $null
            $reader = [Xml.XmlReader]::Create($file.FullName, $settings)
            try { $xml = [Xml.XmlDocument]::new(); $xml.XmlResolver = $null; $xml.Load($reader) }
            finally { $reader.Dispose() }
        }
        catch { throw "Malformed metadata XML: $($file.FullName). $($_.Exception.Message)" }
        # Only metadata object's own uuid attributes are inspected. References such as
        # extended-configuration mappings are not rewritten and are not treated as owned IDs.
        if ($xml.DocumentElement.LocalName -ne 'MetaDataObject') { continue }
        $metadataNamespace = $xml.DocumentElement.NamespaceURI
        $ownedNodes = @($xml.SelectNodes('//*[@uuid]') | Where-Object { $_.NamespaceURI -eq $metadataNamespace })
        if ($ownedNodes.Count -gt 0) { $ownedFiles[$file.FullName] = $true }
        foreach ($node in $ownedNodes) {
            $uuid = [string]$node.GetAttribute('uuid')
            $guid = [guid]::Empty
            if (-not [guid]::TryParseExact($uuid, 'D', [ref]$guid) -or $guid -eq [guid]::Empty) { throw "Invalid owned metadata UUID in $($file.FullName)" }
            $normalized = $uuid.ToLowerInvariant()
            if ($identities.ContainsKey($normalized)) { throw "Duplicate owned metadata UUID $normalized in $($file.FullName) and $($identities[$normalized])" }
            $identities[$normalized] = $file.FullName
        }
    }
    foreach ($source in $selectedRoots.Keys) {
        if (@($selectedRoots[$source] | Where-Object { $ownedFiles.ContainsKey($_) }).Count -eq 0) {
            throw "Selected extension root contains no recognised owned metadata UUIDs: $source"
        }
    }
    $result = [ordered]@{
        schema_version=1; kind='bsl-flow.extension-identities'; checked_at_utc=[DateTime]::UtcNow.ToString('o'); status='PASS'
        project_root=$root; selected_source_roots=@($SourceRoots); files_checked=$files.Count; owned_uuid_count=$identities.Count
        generated_sidecars_excluded=@('ConfigDumpInfo.xml'); rewritten_files=@(); errors=@($errors)
        message="PASS: $($identities.Count) owned metadata UUIDs are unique across the selected extension sources."
    }
    if ($OutputPath) { $parent=Split-Path -Parent ([System.IO.Path]::GetFullPath($OutputPath)); if(-not(Test-Path -LiteralPath $parent)){New-Item -ItemType Directory -Path $parent -Force|Out-Null}; $result | ConvertTo-Json -Depth 10 | Set-Content -LiteralPath $OutputPath -Encoding UTF8 }
    $result
}
catch {
    $errorResult = [ordered]@{ schema_version=1; kind='bsl-flow.extension-identities'; checked_at_utc=[DateTime]::UtcNow.ToString('o'); status='BLOCKED'; project_root=$root; selected_source_roots=@($SourceRoots); files_checked=$files.Count; owned_uuid_count=$identities.Count; generated_sidecars_excluded=@('ConfigDumpInfo.xml'); rewritten_files=@(); errors=@($errors + $_.Exception.Message); message='Identity check did not produce a PASS; no source was rewritten.' }
    if ($OutputPath) { $parent=Split-Path -Parent ([System.IO.Path]::GetFullPath($OutputPath)); if(-not(Test-Path -LiteralPath $parent)){New-Item -ItemType Directory -Path $parent -Force|Out-Null}; $errorResult | ConvertTo-Json -Depth 10 | Set-Content -LiteralPath $OutputPath -Encoding UTF8 }
    if ($NoThrow) { $errorResult } else { throw }
}
