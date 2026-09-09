Set-StrictMode -Version Latest

$script:BFJsonMaximumBytes = 16MB
$script:BFJsonMaximumDepth = 100
$script:BFJsonMaximumValues = 1000000
if ($null -eq (Get-Variable -Name BFStorageLocks -Scope Script -ErrorAction SilentlyContinue)) {
    $script:BFStorageLocks = [System.Collections.Generic.Dictionary[string,object]]::new([System.StringComparer]::OrdinalIgnoreCase)
}

function New-BFError {
    param([Parameter(Mandatory = $true)][string]$Kind, [Parameter(Mandatory = $true)][string]$Message)
    return [System.InvalidOperationException]::new(($Kind + ': ' + $Message))
}

function ConvertTo-BFJsonString {
    param([Parameter(Mandatory = $true)][AllowEmptyString()][string]$Value)
    $builder = [System.Text.StringBuilder]::new($Value.Length + 2)
    [void]$builder.Append('"')
    for ($index = 0; $index -lt $Value.Length; $index++) {
        $character = $Value[$index]
        $number = [int]$character
        if ([char]::IsHighSurrogate($character)) {
            if (($index + 1) -ge $Value.Length -or -not [char]::IsLowSurrogate($Value[$index + 1])) {
                throw (New-BFError 'BF_INVALID' 'String contains an unpaired UTF-16 surrogate.')
            }
            [void]$builder.Append($character)
            $index++
            [void]$builder.Append($Value[$index])
            continue
        }
        if ([char]::IsLowSurrogate($character)) { throw (New-BFError 'BF_INVALID' 'String contains an unpaired UTF-16 surrogate.') }
        $escaped = $true
        switch ($number) {
            8  { [void]$builder.Append('\b') }
            9  { [void]$builder.Append('\t') }
            10 { [void]$builder.Append('\n') }
            12 { [void]$builder.Append('\f') }
            13 { [void]$builder.Append('\r') }
            34 { [void]$builder.Append('\"') }
            92 { [void]$builder.Append('\\') }
            default { $escaped = $false }
        }
        if ($escaped) { continue }
        if ($number -lt 32) { [void]$builder.Append(('\u{0:x4}' -f $number)) }
        else { [void]$builder.Append($character) }
    }
    [void]$builder.Append('"')
    return $builder.ToString()
}

function ConvertTo-BFCanonicalNumber {
    param([Parameter(Mandatory = $true)][object]$Value)
    $culture = [System.Globalization.CultureInfo]::InvariantCulture
    switch ([System.Type]::GetTypeCode($Value.GetType())) {
        'SByte'   { return ([sbyte]$Value).ToString($culture) }
        'Byte'    { return ([byte]$Value).ToString($culture) }
        'Int16'   { return ([int16]$Value).ToString($culture) }
        'UInt16'  { return ([uint16]$Value).ToString($culture) }
        'Int32'   { return ([int32]$Value).ToString($culture) }
        'UInt32'  { return ([uint32]$Value).ToString($culture) }
        'Int64'   { return ([int64]$Value).ToString($culture) }
        'UInt64'  { return ([uint64]$Value).ToString($culture) }
        'Decimal' { return ([decimal]$Value).ToString('G29', $culture) }
        'Single'  {
            $number = [single]$Value
            if ([single]::IsNaN($number) -or [single]::IsInfinity($number)) { throw (New-BFError 'BF_INVALID' 'Non-finite numbers are not valid JSON values.') }
            return $number.ToString('G9', $culture)
        }
        'Double'  {
            $number = [double]$Value
            if ([double]::IsNaN($number) -or [double]::IsInfinity($number)) { throw (New-BFError 'BF_INVALID' 'Non-finite numbers are not valid JSON values.') }
            return $number.ToString('G17', $culture)
        }
        default { throw (New-BFError 'BF_INVALID' ("Unsupported numeric type: {0}." -f $Value.GetType().FullName)) }
    }
}

function Get-BFCanonicalJson {
    [CmdletBinding()]
    param([Parameter(Mandatory = $true)][AllowNull()][object]$Value)
    function Convert-Value {
        param([AllowNull()][object]$Item, [int]$Depth)
        if ($Depth -gt $script:BFJsonMaximumDepth) { throw (New-BFError 'BF_INVALID' 'JSON value exceeds the maximum nesting depth.') }
        if ($null -eq $Item) { return 'null' }
        if ($Item -is [string]) { return ConvertTo-BFJsonString $Item }
        if ($Item -is [bool]) { if ($Item) { return 'true' } else { return 'false' } }
        if ($Item -is [char] -or $Item -is [datetime] -or $Item -is [datetimeoffset] -or $Item.GetType().IsEnum) { throw (New-BFError 'BF_INVALID' ("Unsupported JSON value type: {0}." -f $Item.GetType().FullName)) }
        if ($Item -is [System.Numerics.BigInteger]) { return ([System.Numerics.BigInteger]$Item).ToString([System.Globalization.CultureInfo]::InvariantCulture) }
        if ($Item.GetType().IsPrimitive -or $Item -is [decimal]) { return ConvertTo-BFCanonicalNumber $Item }
        if ($Item -is [System.Collections.IDictionary] -or $Item -is [pscustomobject]) {
            $values = [System.Collections.Generic.Dictionary[string,object]]::new([System.StringComparer]::Ordinal)
            $seen = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::OrdinalIgnoreCase)
            if ($Item -is [System.Collections.IDictionary]) {
                foreach ($keyObject in $Item.Keys) {
                    if ($keyObject -isnot [string]) { throw (New-BFError 'BF_INVALID' 'JSON object keys must be strings.') }
                    $key = [string]$keyObject
                    if (-not $seen.Add($key)) { throw (New-BFError 'BF_INVALID' ("Duplicate JSON object key: {0}." -f $key)) }
                    $values.Add($key, $Item[$keyObject])
                }
            }
            else {
                foreach ($property in $Item.PSObject.Properties) {
                    if (-not $seen.Add($property.Name)) { throw (New-BFError 'BF_INVALID' ("Duplicate JSON object key: {0}." -f $property.Name)) }
                    $values.Add($property.Name, $property.Value)
                }
            }
            $keys = [string[]]@($values.Keys)
            [array]::Sort($keys, [System.StringComparer]::Ordinal)
            $parts = [System.Collections.Generic.List[string]]::new()
            foreach ($key in $keys) { $parts.Add((ConvertTo-BFJsonString $key) + ':' + (Convert-Value $values[$key] ($Depth + 1))) }
            return '{' + [string]::Join(',', $parts.ToArray()) + '}'
        }
        if ($Item -is [System.Collections.IEnumerable]) {
            $parts = [System.Collections.Generic.List[string]]::new()
            foreach ($element in $Item) { $parts.Add((Convert-Value $element ($Depth + 1))) }
            return '[' + [string]::Join(',', $parts.ToArray()) + ']'
        }
        throw (New-BFError 'BF_INVALID' ("Unsupported JSON value type: {0}." -f $Item.GetType().FullName))
    }
    return Convert-Value $Value 0
}

function Get-BFHash {
    [CmdletBinding()]
    param([Parameter(Mandatory = $true)][AllowNull()][object]$Value)
    $json = Get-BFCanonicalJson $Value
    $encoding = [System.Text.UTF8Encoding]::new($false, $true)
    $sha = [System.Security.Cryptography.SHA256]::Create()
    try { return ([System.BitConverter]::ToString($sha.ComputeHash($encoding.GetBytes($json))).Replace('-', '').ToLowerInvariant()) }
    finally { $sha.Dispose() }
}

function Assert-BFSafePath {
    [CmdletBinding()]
    param([Parameter(Mandatory = $true)][string]$Path)
    if ([string]::IsNullOrWhiteSpace($Path) -or -not [System.IO.Path]::IsPathRooted($Path)) { throw (New-BFError 'BF_INVALID' 'Path must be an absolute filesystem path.') }
    if ($Path.StartsWith('\\?\') -or $Path.StartsWith('\\.\') -or $Path -match '^[^:]+::') { throw (New-BFError 'BF_INVALID' 'Device and provider-qualified paths are not allowed.') }
    try { $fullPath = [System.IO.Path]::GetFullPath($Path) }
    catch { throw (New-BFError 'BF_INVALID' ("Path is invalid: {0}" -f $_.Exception.Message)) }
    $root = [System.IO.Path]::GetPathRoot($fullPath)
    if ($fullPath.Substring($root.Length).Contains(':')) { throw (New-BFError 'BF_INVALID' 'Alternate data stream paths are not allowed.') }
    $cursor = $fullPath
    while (-not [string]::IsNullOrEmpty($cursor)) {
        if ([System.IO.File]::Exists($cursor) -or [System.IO.Directory]::Exists($cursor)) {
            try { $item = Get-Item -LiteralPath $cursor -Force -ErrorAction Stop }
            catch { throw (New-BFError 'BF_INVALID' ("Cannot inspect path: {0}" -f $cursor)) }
            if (($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) { throw (New-BFError 'BF_INVALID' ("Path contains a reparse point: {0}" -f $cursor)) }
        }
        $parent = [System.IO.Path]::GetDirectoryName($cursor.TrimEnd([System.IO.Path]::DirectorySeparatorChar, [System.IO.Path]::AltDirectorySeparatorChar))
        if ([string]::IsNullOrEmpty($parent) -or $parent -eq $cursor) { break }
        $cursor = $parent
    }
    return $fullPath
}

function Get-BFFileHash {
    [CmdletBinding()]
    param([Parameter(Mandatory = $true)][string]$Path)
    $fullPath = Assert-BFSafePath $Path
    if (-not [System.IO.File]::Exists($fullPath)) { throw (New-BFError 'BF_INVALID' ("File does not exist: {0}" -f $fullPath)) }
    $stream = [System.IO.File]::Open($fullPath, [System.IO.FileMode]::Open, [System.IO.FileAccess]::Read, [System.IO.FileShare]::Read)
    $sha = [System.Security.Cryptography.SHA256]::Create()
    try { return ([System.BitConverter]::ToString($sha.ComputeHash($stream)).Replace('-', '').ToLowerInvariant()) }
    finally { $sha.Dispose(); $stream.Dispose() }
}

function Test-BFJsonSyntax {
    param([Parameter(Mandatory = $true)][string]$Text)
    $state = [pscustomobject]@{ Text = $Text; Index = 0; Values = 0 }
    $length = $Text.Length
    $numberPattern = [regex]::new('\G-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?')
    $skipWhitespace = { while ($state.Index -lt $length -and " `t`r`n".IndexOf($state.Text[$state.Index]) -ge 0) { $state.Index++ } }
    $parseString = {
        if ($state.Index -ge $length -or $state.Text[$state.Index] -ne '"') { throw 'Expected a JSON string.' }
        $state.Index++; $builder = [System.Text.StringBuilder]::new()
        while ($state.Index -lt $length) {
            $character = $state.Text[$state.Index]; $state.Index++
            if ($character -eq '"') { return $builder.ToString() }
            if ([int]$character -lt 32) { throw 'Unescaped control character in JSON string.' }
            if ($character -ne '\') { [void]$builder.Append($character); continue }
            if ($state.Index -ge $length) { throw 'Incomplete JSON escape.' }
            $escape = $state.Text[$state.Index]; $state.Index++
            switch ($escape) {
                '"' { [void]$builder.Append('"') }
                '\' { [void]$builder.Append('\') }
                '/' { [void]$builder.Append('/') }
                'b' { [void]$builder.Append([char]8) }
                'f' { [void]$builder.Append([char]12) }
                'n' { [void]$builder.Append([char]10) }
                'r' { [void]$builder.Append([char]13) }
                't' { [void]$builder.Append([char]9) }
                'u' {
                    if (($state.Index + 4) -gt $length) { throw 'Incomplete JSON Unicode escape.' }
                    $hex = $state.Text.Substring($state.Index, 4)
                    if ($hex -notmatch '^[0-9A-Fa-f]{4}$') { throw 'Invalid JSON Unicode escape.' }
                    [void]$builder.Append([char][convert]::ToInt32($hex, 16)); $state.Index += 4
                }
                default { throw 'Invalid JSON escape.' }
            }
        }
        throw 'Unterminated JSON string.'
    }
    $parseValue = $null
    $parseValue = {
        param([int]$Depth)
        if ($Depth -gt $script:BFJsonMaximumDepth) { throw 'JSON exceeds the maximum nesting depth.' }
        $state.Values++; if ($state.Values -gt $script:BFJsonMaximumValues) { throw 'JSON contains too many values.' }
        & $skipWhitespace
        if ($state.Index -ge $length) { throw 'Expected a JSON value.' }
        $character = $state.Text[$state.Index]
        if ($character -eq '"') { [void](& $parseString); return 'scalar' }
        if ($character -eq '{') {
            $state.Index++; & $skipWhitespace
            $keys = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::OrdinalIgnoreCase)
            if ($state.Index -lt $length -and $state.Text[$state.Index] -eq '}') { $state.Index++; return 'object' }
            while ($true) {
                & $skipWhitespace; $key = & $parseString
                if (-not $keys.Add($key)) { throw ("Duplicate JSON object key: {0}." -f $key) }
                & $skipWhitespace
                if ($state.Index -ge $length -or $state.Text[$state.Index] -ne ':') { throw 'Expected a colon after JSON object key.' }
                $state.Index++; [void](& $parseValue ($Depth + 1)); & $skipWhitespace
                if ($state.Index -lt $length -and $state.Text[$state.Index] -eq ',') { $state.Index++; continue }
                if ($state.Index -lt $length -and $state.Text[$state.Index] -eq '}') { $state.Index++; return 'object' }
                throw 'Expected comma or closing brace in JSON object.'
            }
        }
        if ($character -eq '[') {
            $state.Index++; & $skipWhitespace
            if ($state.Index -lt $length -and $state.Text[$state.Index] -eq ']') { $state.Index++; return 'array' }
            while ($true) {
                [void](& $parseValue ($Depth + 1)); & $skipWhitespace
                if ($state.Index -lt $length -and $state.Text[$state.Index] -eq ',') { $state.Index++; continue }
                if ($state.Index -lt $length -and $state.Text[$state.Index] -eq ']') { $state.Index++; return 'array' }
                throw 'Expected comma or closing bracket in JSON array.'
            }
        }
        foreach ($literal in @('true', 'false', 'null')) {
            if (($state.Index + $literal.Length) -le $length -and $state.Text.Substring($state.Index, $literal.Length) -ceq $literal) { $state.Index += $literal.Length; return 'scalar' }
        }
        $match = $numberPattern.Match($state.Text, $state.Index)
        if (-not $match.Success) { throw 'Invalid JSON value.' }
        $state.Index += $match.Length; return 'scalar'
    }
    try {
        $kind = & $parseValue 0; & $skipWhitespace
        if ($state.Index -ne $length) { throw 'Unexpected data after JSON value.' }
        return $kind
    }
    catch { throw (New-BFError 'BF_INVALID' $_.Exception.Message) }
}

function Read-BFJson {
    [CmdletBinding()]
    param([Parameter(Mandatory = $true)][string]$Path)
    $fullPath = Assert-BFSafePath $Path
    if (-not [System.IO.File]::Exists($fullPath)) { throw (New-BFError 'BF_INVALID' ("JSON file does not exist: {0}" -f $fullPath)) }
    $info = [System.IO.FileInfo]::new($fullPath)
    if ($info.Length -gt $script:BFJsonMaximumBytes) { throw (New-BFError 'BF_INVALID' 'JSON file exceeds the maximum allowed size.') }
    try { $bytes = [System.IO.File]::ReadAllBytes($fullPath) } catch { throw (New-BFError 'BF_INVALID' ("Cannot read JSON file: {0}" -f $_.Exception.Message)) }
    try { $text = [System.Text.UTF8Encoding]::new($false, $true).GetString($bytes) } catch { throw (New-BFError 'BF_INVALID' 'JSON file is not valid UTF-8.') }
    if ($text.Length -gt 0 -and $text[0] -eq [char]0xFEFF) { $text = $text.Substring(1) }
    $kind = Test-BFJsonSyntax $text
    if ($kind -ne 'object') { throw (New-BFError 'BF_INVALID' 'Top-level JSON value must be an object.') }
    try {
        $convertCommand = Get-Command ConvertFrom-Json -ErrorAction Stop
        if ($convertCommand.Parameters.ContainsKey('DateKind')) { return ConvertFrom-Json -InputObject $text -DateKind String -ErrorAction Stop }
        return ConvertFrom-Json -InputObject $text -ErrorAction Stop
    }
    catch { throw (New-BFError 'BF_INVALID' ("Cannot materialize JSON object: {0}" -f $_.Exception.Message)) }
}

function Invoke-BFAtomicReplace {
    param([Parameter(Mandatory = $true)][string]$Source, [Parameter(Mandatory = $true)][string]$Destination)
    if ($null -eq ('BFNativeFile' -as [type])) {
        Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
public static class BFNativeFile {
    [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
    public static extern bool MoveFileEx(string existingName, string newName, int flags);
}
'@
    }
    # Write-BFJson validated ordinary absolute paths. Only the native boundary
    # needs extended paths; its temporary filename can exceed MAX_PATH.
    $nativeSource = if ($Source.StartsWith('\\')) { '\\?\UNC\' + $Source.Substring(2) } else { '\\?\' + $Source }
    $nativeDestination = if ($Destination.StartsWith('\\')) { '\\?\UNC\' + $Destination.Substring(2) } else { '\\?\' + $Destination }
    if (-not [BFNativeFile]::MoveFileEx($nativeSource, $nativeDestination, 9)) {
        $errorCode = [Runtime.InteropServices.Marshal]::GetLastWin32Error()
        throw [System.ComponentModel.Win32Exception]::new($errorCode)
    }
}

function Write-BFJson {
    [CmdletBinding()]
    param([Parameter(Mandatory = $true)][string]$Path, [Parameter(Mandatory = $true)][AllowNull()][object]$Value, [switch]$Replace)
    $fullPath = Assert-BFSafePath $Path
    $parent = [System.IO.Path]::GetDirectoryName($fullPath)
    if ([string]::IsNullOrEmpty($parent)) { throw (New-BFError 'BF_INVALID' 'JSON path must have a parent directory.') }
    try { [void][System.IO.Directory]::CreateDirectory($parent) }
    catch { throw (New-BFError 'BF_INVALID' ("Cannot create JSON parent directory: {0}" -f $_.Exception.Message)) }
    [void](Assert-BFSafePath $parent)
    if (-not $Replace -and [System.IO.File]::Exists($fullPath)) { throw (New-BFError 'BF_CONFLICT' ("Refusing to overwrite JSON file: {0}" -f $fullPath)) }
    $bytes = [System.Text.UTF8Encoding]::new($false, $true).GetBytes((Get-BFCanonicalJson $Value))
    $temporary = [System.IO.Path]::Combine($parent, ('.' + [System.IO.Path]::GetFileName($fullPath) + '.' + [guid]::NewGuid().ToString('N') + '.tmp'))
    try {
        $stream = [System.IO.FileStream]::new($temporary, [System.IO.FileMode]::CreateNew, [System.IO.FileAccess]::Write, [System.IO.FileShare]::None)
        try { $stream.Write($bytes, 0, $bytes.Length); $stream.Flush($true) } finally { $stream.Dispose() }
        if ($Replace -and [System.IO.File]::Exists($fullPath)) { Invoke-BFAtomicReplace $temporary $fullPath } else { [System.IO.File]::Move($temporary, $fullPath) }
    }
    catch [System.IO.IOException] { throw (New-BFError 'BF_CONFLICT' ("Could not publish JSON file: {0}" -f $_.Exception.Message)) }
    finally {
        if ([System.IO.File]::Exists($temporary)) { [System.IO.File]::Delete($temporary) }
    }
}

function Enter-BFLock {
    [CmdletBinding()]
    param([Parameter(Mandatory = $true)][string]$Directory)
    $fullDirectory = Assert-BFSafePath $Directory
    if ([System.IO.File]::Exists($fullDirectory)) { throw (New-BFError 'BF_INVALID' 'Lock path is a file, not a directory.') }
    try { [void][System.IO.Directory]::CreateDirectory($fullDirectory) }
    catch { throw (New-BFError 'BF_INVALID' ("Cannot create lock directory: {0}" -f $_.Exception.Message)) }
    $fullDirectory = Assert-BFSafePath $fullDirectory
    if (-not [System.IO.Directory]::Exists($fullDirectory)) { throw (New-BFError 'BF_INVALID' 'Lock path is not a directory.') }
    if ($script:BFStorageLocks.ContainsKey($fullDirectory)) {
        $owned = $script:BFStorageLocks[$fullDirectory]
        if ($null -ne $owned -and $owned.CanWrite) { throw (New-BFError 'BF_CONFLICT' 'Writer lock is already held.') }
        [void]$script:BFStorageLocks.Remove($fullDirectory)
    }
    try { $stream = [System.IO.FileStream]::new([System.IO.Path]::Combine($fullDirectory, '.writer.lock'), [System.IO.FileMode]::OpenOrCreate, [System.IO.FileAccess]::ReadWrite, [System.IO.FileShare]::None) }
    catch [System.IO.IOException] { throw (New-BFError 'BF_CONFLICT' 'Writer lock is held by another controller.') }
    $script:BFStorageLocks[$fullDirectory] = $stream
    return $stream
}

function Get-BFObjectProperty {
    param([Parameter(Mandatory = $true)][object]$Object, [Parameter(Mandatory = $true)][string]$Name)
    if ($Object -is [System.Collections.IDictionary]) { foreach ($key in $Object.Keys) { if ([string]$key -ieq $Name) { return $Object[$key] } }; return $null }
    $property = $Object.PSObject.Properties[$Name]
    if ($null -eq $property) { return $null }
    return $property.Value
}

function Test-BFObjectProperty {
    param([Parameter(Mandatory = $true)][object]$Object, [Parameter(Mandatory = $true)][string]$Name)
    if ($Object -is [System.Collections.IDictionary]) { foreach ($key in $Object.Keys) { if ([string]$key -ieq $Name) { return $true } }; return $false }
    return $null -ne $Object.PSObject.Properties[$Name]
}

function Read-BFJournal {
    [CmdletBinding()]
    param([Parameter(Mandatory = $true)][string]$Directory)
    $fullDirectory = Assert-BFSafePath $Directory
    if (-not [System.IO.Directory]::Exists($fullDirectory)) { return $null }
    $revisionDirectory = [System.IO.Path]::Combine($fullDirectory, 'revisions')
    if (-not [System.IO.Directory]::Exists($revisionDirectory)) { return $null }
    [void](Assert-BFSafePath $revisionDirectory)
    $revisionFiles = @()
    try { $allRevisionFiles = @(Get-ChildItem -LiteralPath $revisionDirectory -File -Force -ErrorAction Stop) }
    catch { throw (New-BFError 'BF_BLOCKED' ("Cannot enumerate revision journal: {0}" -f $_.Exception.Message)) }
    foreach ($file in $allRevisionFiles) {
        if ($file.Name -match '\.tmp$') { continue }
        if ($file.Name -notmatch '^[0-9]{6}\.json$') { if ($file.Extension -ieq '.json') { throw (New-BFError 'BF_BLOCKED' ("Unexpected revision filename: {0}" -f $file.Name)) }; continue }
        $revisionFiles += $file
    }
    $revisionFiles = @($revisionFiles | Sort-Object Name)
    if ($revisionFiles.Count -eq 0) { return $null }
    $expectedNumber = 1; $previousHash = $null; $taskId = $null; $latest = $null
    foreach ($file in $revisionFiles) {
        $fileNumber = [int]$file.BaseName
        if ($fileNumber -ne $expectedNumber) { throw (New-BFError 'BF_BLOCKED' ("Revision chain has a gap before {0}." -f $file.Name)) }
        try { $state = Read-BFJson $file.FullName } catch { throw (New-BFError 'BF_BLOCKED' ("Corrupt revision {0}: {1}" -f $file.Name, $_.Exception.Message)) }
        foreach ($requiredField in @('revision', 'previous_sha256', 'task_id')) {
            if (-not (Test-BFObjectProperty $state $requiredField)) { throw (New-BFError 'BF_BLOCKED' ("Required chain field {0} is missing in {1}." -f $requiredField, $file.Name)) }
        }
        $revision = Get-BFObjectProperty $state 'revision'
        if (($revision -isnot [int] -and $revision -isnot [long]) -or [int64]$revision -ne $fileNumber) { throw (New-BFError 'BF_BLOCKED' ("Filename and revision disagree in {0}." -f $file.Name)) }
        $currentTaskId = Get-BFObjectProperty $state 'task_id'
        if ($currentTaskId -isnot [string] -or [string]::IsNullOrWhiteSpace($currentTaskId)) { throw (New-BFError 'BF_BLOCKED' ("task_id is missing in {0}." -f $file.Name)) }
        if ($null -eq $taskId) { $taskId = $currentTaskId } elseif ($currentTaskId -cne $taskId) { throw (New-BFError 'BF_BLOCKED' ("task_id changed in {0}." -f $file.Name)) }
        $linkedHash = Get-BFObjectProperty $state 'previous_sha256'
        if ($expectedNumber -eq 1) { if ($null -ne $linkedHash) { throw (New-BFError 'BF_BLOCKED' 'First revision must have null previous_sha256.') } }
        elseif ($linkedHash -isnot [string] -or $linkedHash -cne $previousHash) { throw (New-BFError 'BF_BLOCKED' ("Revision hash chain is broken at {0}." -f $file.Name)) }
        $previousHash = Get-BFHash $state; $latest = $state; $expectedNumber++
    }
    return $latest
}

function Write-BFRevision {
    [CmdletBinding()]
    param([Parameter(Mandatory = $true)][string]$Directory, [Parameter(Mandatory = $true)][object]$State, [Parameter(Mandatory = $true)][int64]$ExpectedRevision)
    if ($ExpectedRevision -lt 0) { throw (New-BFError 'BF_INVALID' 'ExpectedRevision cannot be negative.') }
    $fullDirectory = Assert-BFSafePath $Directory
    if (-not $script:BFStorageLocks.ContainsKey($fullDirectory)) { throw (New-BFError 'BF_CONFLICT' 'Write-BFRevision requires the caller to hold the writer lock.') }
    $lock = $script:BFStorageLocks[$fullDirectory]
    if ($null -eq $lock -or -not $lock.CanWrite) { [void]$script:BFStorageLocks.Remove($fullDirectory); throw (New-BFError 'BF_CONFLICT' 'Writer lock is no longer held.') }
    [void](Get-BFCanonicalJson $State)
    if ($State -isnot [System.Collections.IDictionary] -and $State -isnot [pscustomobject]) { throw (New-BFError 'BF_INVALID' 'Revision state must be an object.') }
    $latest = Read-BFJournal $fullDirectory
    $actualRevision = if ($null -eq $latest) { 0L } else { [int64](Get-BFObjectProperty $latest 'revision') }
    if ($actualRevision -ne $ExpectedRevision) { throw (New-BFError 'BF_CONFLICT' ("Expected revision {0}, actual revision {1}." -f $ExpectedRevision, $actualRevision)) }
    if ($actualRevision -ge 999999) { throw (New-BFError 'BF_BLOCKED' 'Revision journal reached its supported limit.') }
    $newState = [ordered]@{}
    if ($State -is [System.Collections.IDictionary]) { foreach ($keyObject in $State.Keys) { $key = [string]$keyObject; if ($key -ieq 'revision' -or $key -ieq 'previous_sha256') { continue }; $newState[$key] = $State[$keyObject] } }
    else { foreach ($property in $State.PSObject.Properties) { if ($property.Name -ieq 'revision' -or $property.Name -ieq 'previous_sha256') { continue }; $newState[$property.Name] = $property.Value } }
    $newTaskId = Get-BFObjectProperty $newState 'task_id'
    if ($newTaskId -isnot [string] -or [string]::IsNullOrWhiteSpace($newTaskId)) { throw (New-BFError 'BF_INVALID' 'Revision state requires a non-empty task_id.') }
    if ($null -ne $latest -and $newTaskId -cne (Get-BFObjectProperty $latest 'task_id')) { throw (New-BFError 'BF_CONFLICT' 'task_id cannot change within a revision journal.') }
    $newState['revision'] = $actualRevision + 1
    $newState['previous_sha256'] = if ($null -eq $latest) { $null } else { Get-BFHash $latest }
    $revisionDirectory = [System.IO.Path]::Combine($fullDirectory, 'revisions')
    [void][System.IO.Directory]::CreateDirectory($revisionDirectory); [void](Assert-BFSafePath $revisionDirectory)
    $revisionPath = [System.IO.Path]::Combine($revisionDirectory, ('{0:D6}.json' -f ($actualRevision + 1)))
    [void](Write-BFJson -Path $revisionPath -Value $newState)
    $persisted = Read-BFJson $revisionPath
    [void](Write-BFJson -Path ([System.IO.Path]::Combine($fullDirectory, 'current.json')) -Value ([ordered]@{ revision = $actualRevision + 1; sha256 = Get-BFHash $persisted }) -Replace)
    return $persisted
}
