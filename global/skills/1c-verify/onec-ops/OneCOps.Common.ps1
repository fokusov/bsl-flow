#Requires -Version 7.0
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

# Shared helpers for the onec-ops dispatcher and its adapters. Deliberately self-contained
# (no dot-sourcing across skill directories) so onec-ops keeps working if sibling skills change
# their own internal helpers. The YAML reading style mirrors Get-BSLFlowYamlValue in
# global/skills/1c-spec-review/scripts/Review.Common.ps1: indentation-based, no external parser.

function Get-OOYamlValue {
    # Reads a single scalar leaf value at an exact dotted key Path within $Text.
    param(
        [Parameter(Mandatory)][AllowEmptyString()][string]$Text,
        [Parameter(Mandatory)][string[]]$Path,
        [string]$Default
    )
    $stack = [System.Collections.Generic.List[object]]::new()
    $found = [System.Collections.Generic.List[string]]::new()
    foreach ($line in ($Text -split "`r?`n")) {
        if ($line -match '^\s*(?:#.*)?$') { continue }
        if ($line -notmatch '^(?<indent>\s*)(?<key>[A-Za-z0-9_-]+):(?:\s*(?<value>.*?))?\s*$') { continue }
        if ($Matches.indent.Contains("`t")) { throw 'BF_INVALID: tabs are not supported in bsl-flow.yaml indentation.' }
        $indent = $Matches.indent.Length
        while ($stack.Count -gt 0 -and $stack[$stack.Count - 1].Indent -ge $indent) { $stack.RemoveAt($stack.Count - 1) }
        $keys = @($stack | ForEach-Object { $_.Key }) + @($Matches.key)
        $value = $Matches.value.Trim()
        if ($value -and (($keys -join '/') -eq ($Path -join '/'))) { $found.Add($value.Trim('"', "'")) }
        if (-not $value) { $stack.Add([pscustomobject]@{ Indent = $indent; Key = $Matches.key }) }
    }
    if ($found.Count -gt 1) { throw "BF_INVALID: duplicate YAML value: $($Path -join '.')" }
    if ($found.Count -eq 1) { return $found[0] }
    return $Default
}

function Get-OOYamlStringList {
    # Reads a YAML block sequence of scalars nested directly under the dotted key Path.
    param(
        [Parameter(Mandatory)][AllowEmptyString()][string]$Text,
        [Parameter(Mandatory)][string[]]$Path
    )
    $stack = [System.Collections.Generic.List[object]]::new()
    $result = [System.Collections.Generic.List[string]]::new()
    $inTarget = $false
    $targetIndent = -1
    foreach ($line in ($Text -split "`r?`n")) {
        if ($line -match '^\s*(?:#.*)?$') { continue }
        if ($line -notmatch '^(?<indent>\s*)(?<rest>.*)$') { continue }
        $indent = $Matches.indent.Length
        $rest = $Matches.rest
        if ($inTarget) {
            if ($indent -le $targetIndent) { break }
            if ($rest -match '^-\s*(?<val>.+?)\s*$') { $result.Add($Matches.val.Trim('"', "'")); continue }
            break
        }
        while ($stack.Count -gt 0 -and $stack[$stack.Count - 1].Indent -ge $indent) { $stack.RemoveAt($stack.Count - 1) }
        if ($rest -match '^(?<key>[A-Za-z0-9_-]+):\s*$') {
            $keys = @($stack | ForEach-Object { $_.Key }) + @($Matches.key)
            if (($keys -join '/') -eq ($Path -join '/')) { $inTarget = $true; $targetIndent = $indent; continue }
            $stack.Add([pscustomobject]@{ Indent = $indent; Key = $Matches.key })
        }
    }
    return @($result)
}

function Get-OOYamlFlatMap {
    # Reads scalar key: value pairs nested exactly one level under the dotted key Path.
    param(
        [Parameter(Mandatory)][AllowEmptyString()][string]$Text,
        [Parameter(Mandatory)][string[]]$Path
    )
    $stack = [System.Collections.Generic.List[object]]::new()
    $result = [ordered]@{}
    $inTarget = $false
    $targetIndent = -1
    foreach ($line in ($Text -split "`r?`n")) {
        if ($line -match '^\s*(?:#.*)?$') { continue }
        if ($line -notmatch '^(?<indent>\s*)(?<rest>.*)$') { continue }
        $indent = $Matches.indent.Length
        $rest = $Matches.rest
        if ($inTarget) {
            if ($indent -le $targetIndent) { break }
            if ($rest -match '^(?<key>[^:\s][^:]*?):\s*(?<val>.+?)\s*$') { $result[$Matches.key] = $Matches.val.Trim('"', "'") }
            continue
        }
        while ($stack.Count -gt 0 -and $stack[$stack.Count - 1].Indent -ge $indent) { $stack.RemoveAt($stack.Count - 1) }
        if ($rest -match '^(?<key>[A-Za-z0-9_-]+):\s*$') {
            $keys = @($stack | ForEach-Object { $_.Key }) + @($Matches.key)
            if (($keys -join '/') -eq ($Path -join '/')) { $inTarget = $true; $targetIndent = $indent; continue }
            $stack.Add([pscustomobject]@{ Indent = $indent; Key = $Matches.key })
        }
    }
    return $result
}

function Get-OOProperty {
    param([object]$Object, [Parameter(Mandatory)][string[]]$Names)
    if ($null -eq $Object) { return $null }
    foreach ($name in $Names) {
        $property = $Object.PSObject.Properties[$name]
        if ($null -ne $property) { return $property.Value }
    }
    return $null
}

function Get-OOSha256Bytes {
    param([Parameter(Mandatory)][AllowEmptyCollection()][byte[]]$Bytes)
    return ([BitConverter]::ToString([Security.Cryptography.SHA256]::HashData($Bytes)) -replace '-', '').ToLowerInvariant()
}

function Get-OOSha256Text {
    param([Parameter(Mandatory)][AllowEmptyString()][string]$Text)
    return (Get-OOSha256Bytes ([Text.UTF8Encoding]::new($false).GetBytes($Text)))
}

function Get-OOSha256File {
    param([Parameter(Mandatory)][string]$Path)
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { throw "BF_BLOCKED: evidence file was not found: $Path" }
    return (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
}

function Write-OOJsonAtomic {
    param([Parameter(Mandatory)]$Value, [Parameter(Mandatory)][string]$Path, [int]$Depth = 20)
    $full = [IO.Path]::GetFullPath($Path)
    $parent = Split-Path -Parent $full
    if (-not (Test-Path -LiteralPath $parent -PathType Container)) { New-Item -ItemType Directory -Path $parent -Force | Out-Null }
    $temp = Join-Path $parent ('.' + [IO.Path]::GetFileName($full) + '.' + [guid]::NewGuid().ToString('N') + '.tmp')
    try {
        $Value | ConvertTo-Json -Depth $Depth | Set-Content -LiteralPath $temp -Encoding utf8
        Move-Item -LiteralPath $temp -Destination $full -Force
    }
    finally { if (Test-Path -LiteralPath $temp) { Remove-Item -LiteralPath $temp -Force -ErrorAction SilentlyContinue } }
}

function Test-OOJsonSchema {
    # Minimal structural JSON-Schema (draft-07 subset) validator: type, const, enum, pattern,
    # required, properties, additionalProperties, items, minProperties. Returns an array of
    # human-readable violation strings (empty array == valid). Not a general-purpose validator;
    # covers exactly the constructs used by onec-ops/v1 schemas.
    param([Parameter(Mandatory)]$Schema, $Instance, [string]$At = '$')

    $issues = [System.Collections.Generic.List[string]]::new()

    function Test-OOType {
        param($Instance, $Type)
        switch ($Type) {
            'object' { return ($null -ne $Instance -and $Instance -isnot [string] -and $Instance -isnot [System.Collections.IEnumerable]) -or ($Instance -is [System.Collections.IDictionary]) -or ($Instance -is [pscustomobject]) }
            'array' { return ($Instance -is [System.Array]) -or ($Instance -is [System.Collections.IList] -and $Instance -isnot [string]) }
            'string' { return ($Instance -is [string]) }
            'boolean' { return ($Instance -is [bool]) }
            'null' { return ($null -eq $Instance) }
            default { return $true }
        }
    }

    if ($null -ne (Get-OOProperty $Schema @('const'))) {
        $const = Get-OOProperty $Schema @('const')
        if ("$Instance" -ne "$const") { $issues.Add("$At must equal '$const'.") }
        return @($issues)
    }
    $enum = Get-OOProperty $Schema @('enum')
    if ($null -ne $enum) {
        if (@($enum) -notcontains $Instance) { $issues.Add("$At must be one of: $(@($enum) -join ', ')") }
        return @($issues)
    }

    $typesRaw = Get-OOProperty $Schema @('type')
    $types = New-Object System.Collections.Generic.List[object]
    if ($null -ne $typesRaw) { foreach ($t in @($typesRaw)) { $types.Add($t) } }
    if ($types.Count -gt 0 -and $types[0] -is [array]) { $types = New-Object System.Collections.Generic.List[object]; foreach ($t in @($typesRaw[0])) { $types.Add($t) } }
    if ($types.Count -gt 0) {
        $ok = $false
        foreach ($t in $types) { if (Test-OOType $Instance $t) { $ok = $true; break } }
        if (-not $ok) { $issues.Add("$At has wrong type; expected one of: $($types -join ', ')") }
    }

    $pattern = Get-OOProperty $Schema @('pattern')
    if ($pattern -and $Instance -is [string] -and ($Instance -notmatch $pattern)) { $issues.Add("$At does not match pattern: $pattern") }

    $props = Get-OOProperty $Schema @('properties')
    $additional = Get-OOProperty $Schema @('additionalProperties')
    $requiredRaw = Get-OOProperty $Schema @('required')
    $required = New-Object System.Collections.Generic.List[string]
    if ($null -ne $requiredRaw) { foreach ($r in @($requiredRaw)) { $required.Add([string]$r) } }
    $minProps = Get-OOProperty $Schema @('minProperties')

    $isObjectLike = ($Instance -is [pscustomobject]) -or ($Instance -is [System.Collections.IDictionary])
    if ($isObjectLike -or ($null -ne $props) -or $required.Count -gt 0) {
        $propNames = New-Object System.Collections.Generic.List[string]
        if ($Instance -is [System.Collections.IDictionary]) { foreach ($k in $Instance.Keys) { $propNames.Add([string]$k) } }
        elseif ($null -ne $Instance) { foreach ($p in $Instance.PSObject.Properties) { $propNames.Add([string]$p.Name) } }
        foreach ($req in $required) { if ($propNames -notcontains $req) { $issues.Add("$At is missing required property: $req") } }
        if ($null -ne $minProps -and $propNames.Count -lt [int]$minProps) { $issues.Add("$At must have at least $minProps properties.") }
        if ($null -ne $props) {
            foreach ($name in $propNames) {
                $childSchema = Get-OOProperty $props @($name)
                if ($null -ne $childSchema) {
                    if ($Instance -is [System.Collections.IDictionary]) { $childValue = $Instance[$name] } else { $childValue = $Instance.$name }
                    $issues.AddRange([string[]]@(Test-OOJsonSchema -Schema $childSchema -Instance $childValue -At "$At.$name"))
                }
                elseif ($additional -is [bool] -and $additional -eq $false) {
                    $issues.Add("$At has unexpected property: $name")
                }
                elseif ($null -ne $additional -and $additional -isnot [bool]) {
                    if ($Instance -is [System.Collections.IDictionary]) { $childValue = $Instance[$name] } else { $childValue = $Instance.$name }
                    $issues.AddRange([string[]]@(Test-OOJsonSchema -Schema $additional -Instance $childValue -At "$At.$name"))
                }
            }
        }
    }

    $items = Get-OOProperty $Schema @('items')
    if ($null -ne $items -and ($Instance -is [System.Array] -or ($Instance -is [System.Collections.IList]))) {
        $i = 0
        foreach ($item in @($Instance)) { $issues.AddRange([string[]]@(Test-OOJsonSchema -Schema $items -Instance $item -At "$At[$i]")); $i++ }
    }

    return @($issues)
}

function Get-OOBslFlowYamlText {
    param([Parameter(Mandatory)][string]$ProjectPath)
    $path = Join-Path $ProjectPath 'bsl-flow.yaml'
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { return '' }
    return (Get-Content -Raw -LiteralPath $path -Encoding UTF8)
}
