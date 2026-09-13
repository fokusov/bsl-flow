#Requires -Version 7.0
Set-StrictMode -Version Latest

function Get-BFToolsetCanonicalExistingDirectory {
    param([Parameter(Mandatory)][string]$Path, [Parameter(Mandatory)][string]$Label)
    if (-not (Test-Path -LiteralPath $Path -PathType Container)) { throw "$Label must be an existing directory: $Path" }
    return (Get-Item -LiteralPath $Path -Force).FullName.TrimEnd('\', '/')
}

function Get-BFToolsetCanonicalNewDirectory {
    param([Parameter(Mandatory)][string]$Path)
    $full = [IO.Path]::GetFullPath($Path).TrimEnd('\', '/')
    if (Test-Path -LiteralPath $full) { throw "OutputDirectory must be new and must not overwrite existing data: $full" }
    $parent = Split-Path -Parent $full
    if ([string]::IsNullOrWhiteSpace($parent) -or -not (Test-Path -LiteralPath $parent -PathType Container)) {
        throw "OutputDirectory parent must already exist: $parent"
    }
    return $full
}

function Assert-BFToolsetNoReparseAncestors {
    param([Parameter(Mandatory)][string]$Path, [Parameter(Mandatory)][string]$Label)
    $cursor = Get-Item -LiteralPath $Path -Force
    while ($null -ne $cursor) {
        if (($cursor.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw "$Label contains a reparse-point ancestor: $($cursor.FullName)" }
        $parent = $cursor.Parent
        if ($null -eq $parent -or $parent.FullName -eq $cursor.FullName) { break }
        $cursor = $parent
    }
}

function Test-BFToolsetIsNestedPath {
    param([Parameter(Mandatory)][string]$Candidate, [Parameter(Mandatory)][string]$Parent)
    $prefix = $Parent.TrimEnd('\', '/') + [IO.Path]::DirectorySeparatorChar
    return $Candidate.StartsWith($prefix, [StringComparison]::OrdinalIgnoreCase)
}

function ConvertTo-BFToolsetSafeRelativePath {
    param([Parameter(Mandatory)][string]$FullName, [Parameter(Mandatory)][string]$Root)
    $relative = $FullName.Substring($Root.Length).TrimStart('\', '/').Replace('\', '/')
    $unsafeSegment = @($relative.Split('/') | Where-Object { $_ -eq '.' -or $_ -eq '..' -or $_.Length -eq 0 }).Count -ne 0
    if ([string]::IsNullOrWhiteSpace($relative) -or $relative.StartsWith('/') -or $relative.Contains('//') -or $unsafeSegment) {
        throw "Unsafe relative path: $relative"
    }
    return $relative
}

function Test-BFToolsetForbiddenAssetName {
    param([Parameter(Mandatory)][string]$RelativePath)
    $leaf = [IO.Path]::GetFileName($RelativePath)
    return $leaf -match '(?i)^(?:\.env(?:\..*)?|id_(?:rsa|dsa|ecdsa|ed25519)|.*(?:private[-_]?key|credential|secret).*|(?:auth|oauth|credentials?)\.(?:json|ya?ml|ini|config|txt))$'
}

function Get-BFToolsetSkillFiles {
    param([Parameter(Mandatory)][string]$SkillDirectory)
    Assert-BFToolsetNoReparseAncestors -Path $SkillDirectory -Label 'Skill directory'
    foreach ($directory in Get-ChildItem -LiteralPath $SkillDirectory -Directory -Force -Recurse) {
        if (($directory.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw "Skill tree contains a reparse-point directory: $($directory.FullName)" }
    }
    $files = @(Get-ChildItem -LiteralPath $SkillDirectory -File -Force -Recurse)
    foreach ($file in $files) {
        if (($file.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw "Skill tree contains a reparse-point file: $($file.FullName)" }
        $relative = ConvertTo-BFToolsetSafeRelativePath -FullName $file.FullName -Root $SkillDirectory
        if (Test-BFToolsetForbiddenAssetName -RelativePath $relative) { throw "Refusing obvious secret or authentication asset: $relative" }
    }
    return @($files | Sort-Object { (ConvertTo-BFToolsetSafeRelativePath -FullName $_.FullName -Root $SkillDirectory) })
}

function Get-BFToolsetMcpReferences {
    param([Parameter(Mandatory)][object[]]$Files, [Parameter(Mandatory)][string]$SkillDirectory)
    $found = @{}
    foreach ($file in $Files) {
        if ($file.Extension -notin @('.md', '.ps1', '.txt', '.json', '.yaml', '.yml')) { continue }
        $text = [IO.File]::ReadAllText($file.FullName)
        foreach ($match in [regex]::Matches($text, '(?i)\b(?:mcp__[a-z0-9_]+|unica\.[a-z0-9][a-z0-9.-]*)\b')) {
            $tool = $match.Value.ToLowerInvariant()
            if (-not $found.ContainsKey($tool)) { $found[$tool] = [Collections.Generic.List[string]]::new() }
            $relative = ConvertTo-BFToolsetSafeRelativePath -FullName $file.FullName -Root $SkillDirectory
            if (-not $found[$tool].Contains($relative)) { [void]$found[$tool].Add($relative) }
        }
    }
    return @($found.Keys | Sort-Object | ForEach-Object { [ordered]@{ name = $_; evidence_files = @($found[$_] | Sort-Object) } })
}

function Get-BFToolsetAggregateHash {
    param([Parameter(Mandatory)][object[]]$Skills)
    $builder = [Text.StringBuilder]::new()
    $lines = [Collections.Generic.List[string]]::new()
    foreach ($skill in $Skills) { foreach ($file in $skill.files) { [void]$lines.Add("$($skill.name)/$($file.path)`0$($file.sha256)`n") } }
    $lines.Sort([StringComparer]::Ordinal)
    foreach ($line in $lines) { [void]$builder.Append($line) }
    $bytes = [Text.UTF8Encoding]::new($false).GetBytes($builder.ToString())
    return ([Convert]::ToHexString([Security.Cryptography.SHA256]::HashData($bytes))).ToLowerInvariant()
}

function Assert-BFToolsetExactProperties {
    param([Parameter(Mandatory)]$Object, [Parameter(Mandatory)][string[]]$Names, [Parameter(Mandatory)][string]$Label)
    $actual = if ($Object -is [Collections.IDictionary]) { @($Object.Keys | ForEach-Object { [string]$_ } | Sort-Object) } else { @($Object.PSObject.Properties.Name | Sort-Object) }
    if ((Compare-Object -ReferenceObject ($Names | Sort-Object) -DifferenceObject $actual)) { throw "$Label has an unsupported or missing property." }
}

function Assert-BFToolsetSafeManifestPath {
    param([Parameter(Mandatory)][string]$Path)
    $segments = $Path.Split('/')
    if ($Path -notmatch '^[^/\\]+(?:/[^/\\]+)*$' -or @($segments | Where-Object { $_ -eq '.' -or $_ -eq '..' }).Count -ne 0) { throw "Manifest contains unsafe path: $Path" }
}

function Assert-BFToolsetSafeSkillName {
    param([Parameter(Mandatory)][string]$Name)
    if ([string]::IsNullOrWhiteSpace($Name) -or $Name -in @('.', '..') -or $Name -notmatch '^[^<>:"/\\|?*\x00-\x1F]+$' -or $Name -match '[. ]$' -or $Name -match '^(?i:(con|prn|aux|nul|com[1-9]|lpt[1-9]))$') {
        throw "Manifest contains unsafe skill name: $Name"
    }
}

function Test-BFToolsetSnapshot {
    param([Parameter(Mandatory)][string]$Root, [Parameter(Mandatory)][string]$ExpectedToolset)
    Assert-BFToolsetNoReparseAncestors -Path $Root -Label 'Snapshot'
    $manifestPath = Join-Path $Root 'toolset-manifest.json'
    if (-not (Test-Path -LiteralPath $manifestPath -PathType Leaf)) { throw 'Snapshot manifest is missing.' }
    try {
        $manifestText = Get-Content -LiteralPath $manifestPath -Raw -Encoding UTF8
        if ((Test-BFJsonSyntax $manifestText) -ne 'object') { throw 'Top-level JSON value must be an object.' }
        $manifest = $manifestText | ConvertFrom-Json -AsHashtable -Depth 16
    }
    catch { throw "Snapshot manifest is not valid JSON: $($_.Exception.Message)" }
    Assert-BFToolsetExactProperties $manifest @('schema_version', 'toolset_name', 'source', 'skills', 'aggregate_sha256') 'Manifest'
    if ((($manifest.schema_version -isnot [int]) -and ($manifest.schema_version -isnot [long])) -or $manifest.schema_version -ne 1 -or $manifest.toolset_name -ne $ExpectedToolset -or $manifest.aggregate_sha256 -notmatch '^[0-9a-f]{64}$') { throw 'Snapshot manifest has an invalid identity or aggregate hash.' }
    Assert-BFToolsetExactProperties $manifest.source @('identity', 'path') 'Manifest source'
    if ($manifest.source.identity -ne 'local-private' -or [string]::IsNullOrWhiteSpace([string]$manifest.source.path)) { throw 'Snapshot manifest source is invalid.' }
    if ($manifest.skills -isnot [object[]] -or $manifest.skills.Count -eq 0) { throw 'Snapshot manifest skills must be a non-empty array.' }
    $expected = [Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
    foreach ($skill in @($manifest.skills)) {
        Assert-BFToolsetExactProperties $skill @('name', 'files', 'sha256', 'mcp_references') 'Manifest skill'
        Assert-BFToolsetSafeSkillName ([string]$skill.name)
        if ($skill.sha256 -notmatch '^[0-9a-f]{64}$' -or $skill.files -isnot [object[]] -or $skill.files.Count -eq 0 -or $skill.mcp_references -isnot [object[]]) { throw "Snapshot manifest contains an invalid skill: $($skill.name)." }
        foreach ($file in $skill.files) {
            Assert-BFToolsetExactProperties $file @('path', 'sha256') 'Manifest file'
            Assert-BFToolsetSafeManifestPath ([string]$file.path)
            if ($file.sha256 -notmatch '^[0-9a-f]{64}$' -or (Test-BFToolsetForbiddenAssetName ([string]$file.path))) { throw 'Snapshot manifest contains an invalid or forbidden asset.' }
            $treePath = "$($skill.name)/$($file.path)"; if (-not $expected.Add($treePath)) { throw "Snapshot manifest repeats file $treePath" }
        }
        foreach ($reference in $skill.mcp_references) {
            Assert-BFToolsetExactProperties $reference @('name', 'evidence_files') 'MCP reference'
            if ($reference.name -notmatch '^(?:mcp__[a-z0-9_]+|unica\.[a-z0-9][a-z0-9.-]*)$' -or $reference.evidence_files -isnot [object[]] -or $reference.evidence_files.Count -eq 0) { throw 'Snapshot manifest contains invalid MCP reference.' }
            foreach ($evidence in $reference.evidence_files) { Assert-BFToolsetSafeManifestPath ([string]$evidence) }
        }
        $skillHash = Get-BFToolsetAggregateHash -Skills @([ordered]@{ name = $skill.name; files = $skill.files })
        if ($skillHash -ne $skill.sha256) { throw "Snapshot skill hash differs: $($skill.name)" }
    }
    $actual = [Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
    foreach ($directory in Get-ChildItem -LiteralPath $Root -Directory -Force -Recurse) {
        if (($directory.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw "Snapshot contains a reparse-point directory: $($directory.FullName)" }
    }
    foreach ($file in Get-ChildItem -LiteralPath $Root -File -Force -Recurse) {
        if (($file.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw "Snapshot contains a reparse-point file: $($file.FullName)" }
        $relative = ConvertTo-BFToolsetSafeRelativePath -FullName $file.FullName -Root $Root
        if ($relative -eq 'toolset-manifest.json') { continue }
        [void]$actual.Add($relative)
    }
    if ((Compare-Object -ReferenceObject @($expected | Sort-Object) -DifferenceObject @($actual | Sort-Object))) { throw 'Snapshot tree differs from its manifest.' }
    $rebuilt = @()
    foreach ($skill in $manifest.skills) {
        $files = @()
        foreach ($file in $skill.files) {
            $path = Join-Path (Join-Path $Root $skill.name) ($file.path -replace '/', [IO.Path]::DirectorySeparatorChar)
            if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { throw "Snapshot file is missing: $($skill.name)/$($file.path)" }
            $hash = (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant()
            if ($hash -ne $file.sha256) { throw "Snapshot file hash differs: $($skill.name)/$($file.path)" }
            $files += [ordered]@{ path = $file.path; sha256 = $hash }
        }
        $mcp = Get-BFToolsetMcpReferences -Files @(Get-BFToolsetSkillFiles (Join-Path $Root $skill.name)) -SkillDirectory (Join-Path $Root $skill.name)
        if ((ConvertTo-Json @($mcp) -Compress -Depth 8) -cne (ConvertTo-Json @($skill.mcp_references) -Compress -Depth 8)) { throw "Snapshot MCP text inventory differs: $($skill.name)" }
        $rebuilt += [ordered]@{ name = $skill.name; files = $files }
    }
    if ((Get-BFToolsetAggregateHash -Skills $rebuilt) -ne $manifest.aggregate_sha256) { throw 'Snapshot aggregate hash differs.' }
    return [pscustomobject]@{ verified = $true; toolset_name = $ExpectedToolset; snapshot = $Root; aggregate_sha256 = $manifest.aggregate_sha256; skills = @($manifest.skills).Count }
}

