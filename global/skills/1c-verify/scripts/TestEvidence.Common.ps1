#Requires -Version 7.0
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Get-TEProperty {
    param([object]$Object, [Parameter(Mandatory)][string[]]$Names)
    if ($null -eq $Object) { return $null }
    foreach ($name in $Names) {
        $property = $Object.PSObject.Properties[$name]
        if ($null -ne $property) { return $property.Value }
    }
    return $null
}

function Get-TEJsonFile {
    param([Parameter(Mandatory)][string]$Path)
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { throw "Evidence file was not found: $Path" }
    $text = Get-Content -Raw -LiteralPath $Path
    if ([string]::IsNullOrWhiteSpace($text)) { throw "Evidence file is empty: $Path" }
    try { return ($text | ConvertFrom-Json -ErrorAction Stop) }
    catch { throw "Evidence file is not valid JSON: $Path. $($_.Exception.Message)" }
}

function Get-TEArray {
    param([object]$Value)
    if ($null -eq $Value) { return @() }
    if ($Value -is [System.Array]) { return @($Value) }
    return @($Value)
}

function ConvertTo-TEBoolean {
    param([object]$Value)
    if ($Value -is [bool]) { return $Value }
    return $null
}

function ConvertTo-TECanonicalJson {
    param([object]$Value)
    if ($null -eq $Value) { return 'null' }
    if ($Value -is [System.Array]) { return ('[' + ((@($Value) | ForEach-Object { ConvertTo-TECanonicalJson $_ }) -join ',') + ']') }
    if ($Value -is [string] -or $Value.GetType().IsValueType) { return ($Value | ConvertTo-Json -Depth 5 -Compress) }
    $properties = @($Value.PSObject.Properties)
    if ($Value -is [System.Collections.IDictionary]) {
        $parts = @()
        foreach ($key in @($Value.Keys | Sort-Object)) { $parts += ('"' + ([string]$key).Replace('\','\\').Replace('"','\"') + '":' + (ConvertTo-TECanonicalJson $Value[$key])) }
        return ('{' + ($parts -join ',') + '}')
    }
    if ($properties.Count -gt 0) {
        $parts = @()
        foreach ($property in @($Value.PSObject.Properties | Sort-Object Name)) { $parts += ('"' + $property.Name.Replace('\','\\').Replace('"','\"') + '":' + (ConvertTo-TECanonicalJson $property.Value)) }
        return ('{' + ($parts -join ',') + '}')
    }
    return ($Value | ConvertTo-Json -Depth 5 -Compress)
}

function Get-TESha256 {
    param([Parameter(Mandatory)][string]$Path)
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { return $null }
    return ((Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash).ToLowerInvariant()
}

function Write-TEJsonAtomic {
    param([Parameter(Mandatory)][object]$Value, [Parameter(Mandatory)][string]$Path)
    $full = [System.IO.Path]::GetFullPath($Path)
    $parent = Split-Path -Parent $full
    if (-not (Test-Path -LiteralPath $parent -PathType Container)) { New-Item -ItemType Directory -Path $parent -Force | Out-Null }
    $temp = Join-Path $parent ('.' + [System.IO.Path]::GetFileName($full) + '.' + [guid]::NewGuid().ToString('N') + '.tmp')
    try {
        $Value | ConvertTo-Json -Depth 30 | Set-Content -LiteralPath $temp -Encoding UTF8
        Move-Item -LiteralPath $temp -Destination $full -Force
    }
    finally { if (Test-Path -LiteralPath $temp) { Remove-Item -LiteralPath $temp -Force -ErrorAction SilentlyContinue } }
}

function Compare-TEValue {
    param([object]$Expected, [object]$Observed)
    if ($null -eq $Expected -and $null -eq $Observed) { return $true }
    if ($null -eq $Expected -or $null -eq $Observed) { return $false }
    return ((ConvertTo-TECanonicalJson $Expected) -eq (ConvertTo-TECanonicalJson $Observed))
}

function Test-TEHasProperty {
    param([object]$Object, [Parameter(Mandatory)][string]$Name)
    return ($null -ne $Object -and $null -ne $Object.PSObject.Properties[$Name])
}

function Test-TESourceManifest {
    param(
        [Parameter(Mandatory)][object]$Manifest,
        [Parameter(Mandatory)][string[]]$ExpectedSources
    )
    $issues = @()
    $schemaVersion = Get-TEProperty $Manifest @('schema_version')
    if ($schemaVersion -ne 1) { $issues += 'schema_version must be 1.' }
    if ([string](Get-TEProperty $Manifest @('kind')) -ne 'bsl-flow.source-manifest') { $issues += 'kind must be bsl-flow.source-manifest.' }
    if ((ConvertTo-TEBoolean (Get-TEProperty $Manifest @('complete'))) -ne $true) { $issues += 'complete must be the JSON boolean true.' }

    $manifestSources = @(Get-TEStringArray (Get-TEProperty $Manifest @('sources','source_set','sourceSet')))
    if ($manifestSources.Count -eq 0 -or -not (Compare-TEValue $ExpectedSources $manifestSources)) { $issues += 'sources must exactly match the declared source-set.' }

    $coverage = Get-TEProperty $Manifest @('coverage')
    foreach ($name in @('tracked','untracked','deleted','generated_exclusions')) {
        if ($null -eq $coverage -or (ConvertTo-TEBoolean (Get-TEProperty $coverage @($name))) -ne $true) { $issues += "coverage.$name must be the JSON boolean true." }
    }
    $generatedExclusionsProperty = if ($null -ne $Manifest) { $Manifest.PSObject.Properties['generated_exclusions'] } else { $null }
    if ($null -eq $generatedExclusionsProperty -or $generatedExclusionsProperty.Value -isnot [System.Array]) { $issues += 'generated_exclusions must be explicitly declared as an array, even when empty.' }

    $files = @(Get-TEArray (Get-TEProperty $Manifest @('files')))
    if ($files.Count -eq 0) { $issues += 'files must contain at least one source entry.' }
    $seenPaths = @{}
    foreach ($file in $files) {
        $path = [string](Get-TEProperty $file @('path'))
        $state = [string](Get-TEProperty $file @('state'))
        $sha256 = [string](Get-TEProperty $file @('sha256'))
        if ([string]::IsNullOrWhiteSpace($path)) { $issues += 'Every source entry must have a path.' }
        elseif ($seenPaths.ContainsKey($path)) { $issues += "Duplicate source path: $path" }
        else { $seenPaths[$path] = $true }
        if ($state -notin @('tracked','untracked','deleted')) { $issues += "Invalid source state for ${path}: $state" }
        if ($sha256 -notmatch '^[0-9a-fA-F]{64}$') { $issues += "Missing or invalid SHA-256 for source path: $path" }
    }
    [pscustomobject]@{ valid=($issues.Count -eq 0); issues=@($issues); sources=@($manifestSources); files_count=$files.Count }
}

function Get-TEStringArray {
    param([object]$Value)
    $result = @()
    foreach ($item in (Get-TEArray $Value)) {
        if ($null -eq $item) { continue }
        if ($item -is [string]) { $text = $item }
        else { $text = [string](Get-TEProperty $item @('class_name','className','name','test')) }
        if (-not [string]::IsNullOrWhiteSpace($text)) { $result += $text }
    }
    return @($result | Sort-Object -Unique)
}

function Get-TEUtcTimestamp {
    param([object]$Value)
    if ($null -eq $Value -or [string]::IsNullOrWhiteSpace([string]$Value)) { return $null }
    try { return ([DateTime]::Parse([string]$Value, [Globalization.CultureInfo]::InvariantCulture, [Globalization.DateTimeStyles]::AssumeUniversal)).ToUniversalTime() }
    catch { return $null }
}

function Copy-TEArtifactNoClobber {
    param([Parameter(Mandatory)][string]$Source, [Parameter(Mandatory)][string]$Destination)
    $sourceFull = [System.IO.Path]::GetFullPath($Source)
    $destinationFull = [System.IO.Path]::GetFullPath($Destination)
    if (-not (Test-Path -LiteralPath $sourceFull -PathType Leaf)) { return [pscustomobject]@{ status='missing'; source=$sourceFull; path=$null; sha256=$null } }
    $parent = Split-Path -Parent $destinationFull
    if (-not (Test-Path -LiteralPath $parent -PathType Container)) { New-Item -ItemType Directory -Path $parent -Force | Out-Null }
    if (Test-Path -LiteralPath $destinationFull) { throw "Refusing to overwrite preserved evidence: $destinationFull" }
    Copy-Item -LiteralPath $sourceFull -Destination $destinationFull -Force:$false
    return [pscustomobject]@{ status='copied'; source=$sourceFull; path=$destinationFull; sha256=(Get-TESha256 $destinationFull) }
}
