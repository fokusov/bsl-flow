#Requires -Version 7.0
<#
.SYNOPSIS
Shared helpers for the bench harness: a purpose-built parser for bench/tasks/<id>/task.yaml
(the fixed shape in bench/schemas/task.schema.json - not a general YAML engine), plus small
glob/JSON utilities reused by the runner, the aggregator and the test suite.
#>
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function ConvertFrom-BTScalar {
    param([string]$Raw)
    $value = $Raw.Trim()
    if ($value -match '^#') { return $null }
    $value = ($value -replace '(?<=\s)#.*$', '').TrimEnd()
    if ($value.Length -ge 2 -and $value.StartsWith('"') -and $value.EndsWith('"')) { return $value.Substring(1, $value.Length - 2) }
    if ($value.Length -ge 2 -and $value.StartsWith("'") -and $value.EndsWith("'")) { return $value.Substring(1, $value.Length - 2) }
    if ($value -eq 'true') { return $true }
    if ($value -eq 'false') { return $false }
    if ($value -match '^-?\d+$') { return [int]$value }
    return $value
}

function ConvertFrom-BTFlowSequence {
    param([Parameter(Mandatory)][string]$Raw)
    $inner = $Raw.Trim()
    if (-not ($inner.StartsWith('[') -and $inner.EndsWith(']'))) { throw "Expected a flow sequence like [a, b], got: $Raw" }
    $inner = $inner.Substring(1, $inner.Length - 2).Trim()
    $result = New-Object Collections.Generic.List[object]
    if (-not [string]::IsNullOrWhiteSpace($inner)) {
        foreach ($part in ($inner -split ',')) { $result.Add((ConvertFrom-BTScalar $part)) }
    }
    # PowerShell unrolls a single-element array onto the pipeline when a function returns it
    # via `return`/implicit output; the extra unary comma here re-wraps it so a one-item
    # flow sequence (e.g. `[only.bsl]`) still comes back as an array, not a bare scalar.
    return , $result.ToArray()
}

<#
.SYNOPSIS
Parses one bench/tasks/<id>/task.yaml file into an ordered hashtable matching
bench/schemas/task.schema.json. Understands exactly the subset of YAML the schema needs:
top-level scalars, a `fixture` mapping, `expected_scope` as a block sequence of scalars,
`acceptance` as a block sequence of mappings, and `notes` as either a scalar or a folded
(`>`) block scalar.
#>
function Read-BenchTaskYaml {
    param([Parameter(Mandatory)][string]$Path)

    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { throw "task.yaml not found: $Path" }
    $lines = [regex]::Split((Get-Content -Raw -LiteralPath $Path -Encoding UTF8), '\r?\n')

    $task = [ordered]@{}
    $i = 0
    while ($i -lt $lines.Count) {
        $line = $lines[$i]
        if ($line -match '^\s*(?:#.*)?$') { $i++; continue }
        $m = [regex]::Match($line, '^([A-Za-z_][A-Za-z0-9_]*):(?:\s*(.*))?$')
        if (-not $m.Success) { throw "$Path : unsupported top-level line $($i + 1): $line" }
        $key = $m.Groups[1].Value
        $rest = $m.Groups[2].Value

        switch ($key) {
            'fixture' {
                $map = [ordered]@{}
                $i++
                while ($i -lt $lines.Count -and $lines[$i] -match '^\s{2}([A-Za-z_][A-Za-z0-9_]*):\s*(.*)$') {
                    $map[$Matches[1]] = ConvertFrom-BTScalar $Matches[2]
                    $i++
                }
                $task['fixture'] = $map
                continue
            }
            'expected_scope' {
                $items = New-Object Collections.Generic.List[string]
                $i++
                while ($i -lt $lines.Count -and $lines[$i] -match '^\s{2}-\s+(.*)$') {
                    $items.Add((ConvertFrom-BTScalar $Matches[1]))
                    $i++
                }
                $task['expected_scope'] = $items.ToArray()
                continue
            }
            'acceptance' {
                $items = New-Object Collections.Generic.List[object]
                $i++
                $current = $null
                while ($i -lt $lines.Count) {
                    if ($lines[$i] -match '^\s{2}-\s+([A-Za-z_][A-Za-z0-9_]*):\s*(.*)$') {
                        if ($null -ne $current) { $items.Add([pscustomobject]$current) }
                        $current = [ordered]@{}
                        $k = $Matches[1]; $v = $Matches[2]
                        if ($v.Trim().StartsWith('[')) { $current[$k] = ConvertFrom-BTFlowSequence $v } else { $current[$k] = ConvertFrom-BTScalar $v }
                        $i++
                    }
                    elseif ($null -ne $current -and $lines[$i] -match '^\s{4}([A-Za-z_][A-Za-z0-9_]*):\s*(.*)$') {
                        $k = $Matches[1]; $v = $Matches[2]
                        if ($v.Trim().StartsWith('[')) { $current[$k] = ConvertFrom-BTFlowSequence $v } else { $current[$k] = ConvertFrom-BTScalar $v }
                        $i++
                    }
                    elseif ($lines[$i] -match '^\s*(?:#.*)?$') { $i++ }
                    else { break }
                }
                if ($null -ne $current) { $items.Add([pscustomobject]$current) }
                $task['acceptance'] = $items.ToArray()
                continue
            }
            default {
                if ([string]::IsNullOrWhiteSpace($rest)) { throw "$Path : key '$key' at line $($i + 1) has no scalar value and is not a handled block key." }
                if ($rest.Trim() -eq '>') {
                    $folded = New-Object Collections.Generic.List[string]
                    $i++
                    while ($i -lt $lines.Count -and $lines[$i] -match '^\s{2}(.*)$') {
                        $folded.Add($Matches[1])
                        $i++
                    }
                    $task[$key] = (($folded -join ' ').Trim())
                    continue
                }
                $task[$key] = ConvertFrom-BTScalar $rest
                $i++
                continue
            }
        }
    }
    return $task
}

<#
.SYNOPSIS
Converts a task.yaml-style glob (relative to $Root, `**` matches any depth, `*` matches one
path segment) into a compiled regex anchored to the whole relative path.
#>
function ConvertTo-BTGlobRegex {
    param([Parameter(Mandatory)][string]$Glob)
    $normalized = $Glob -replace '\\', '/'
    $escaped = [regex]::Escape($normalized) -replace '/', '/'
    $pattern = $escaped -replace '\\\*\\\*', '§DOUBLESTAR§' -replace '\\\*', '[^/]*'
    $pattern = $pattern -replace '§DOUBLESTAR§', '.*'
    return [regex]::new('^' + $pattern + '$')
}

function Test-BTPathMatchesAnyGlob {
    param([Parameter(Mandatory)][string]$RelativePath, [Parameter(Mandatory)][string[]]$Globs)
    $normalized = $RelativePath -replace '\\', '/'
    foreach ($glob in $Globs) {
        if ((ConvertTo-BTGlobRegex $glob).IsMatch($normalized)) { return $true }
    }
    return $false
}

function Write-BTJsonAtomic {
    param([Parameter(Mandatory)]$Object, [Parameter(Mandatory)][string]$Path)
    $dir = Split-Path -Parent $Path
    if ($dir -and -not (Test-Path -LiteralPath $dir)) { New-Item -ItemType Directory -Path $dir -Force | Out-Null }
    $json = $Object | ConvertTo-Json -Depth 20
    $tmp = "$Path.tmp-$([Guid]::NewGuid().ToString('N'))"
    [IO.File]::WriteAllText($tmp, $json, [Text.UTF8Encoding]::new($false))
    Move-Item -LiteralPath $tmp -Destination $Path -Force
}
