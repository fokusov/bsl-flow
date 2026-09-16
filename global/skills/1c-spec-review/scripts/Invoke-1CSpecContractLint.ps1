#Requires -Version 7.0
[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$ChangePath,
    [string]$OutputPath,
    [switch]$NoThrow,
    # Internal switch used by the 1c-implement execution graph helpers:
    # returns the parsed and validated model instead of writing the lint JSON.
    [switch]$AsModel
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$changeRoot = [System.IO.Path]::GetFullPath($ChangePath)
if (-not (Test-Path -LiteralPath $changeRoot -PathType Container)) { throw "Change directory not found: $changeRoot" }
$contractPath = Join-Path $changeRoot 'contract.yaml'
$executionPath = Join-Path $changeRoot 'execution.yaml'
$verificationPath = Join-Path $changeRoot 'verification.yaml'
$contractPresent = Test-Path -LiteralPath $contractPath -PathType Leaf
$executionPresent = Test-Path -LiteralPath $executionPath -PathType Leaf
$verificationPresent = Test-Path -LiteralPath $verificationPath -PathType Leaf

$errors = [System.Collections.Generic.List[string]]::new()
$warnings = [System.Collections.Generic.List[string]]::new()
$allowedTaskKinds = @('explore', 'research', 'design', 'implement', 'migrate', 'test', 'review', 'fix', 'document')
$allowedCheckTypes = @('scenario', 'regression', 'static', 'integration')

function Add-LintError([string]$Message) {
    if (-not $errors.Contains($Message)) { $errors.Add($Message) }
}
function Read-LintText {
    param([Parameter(Mandatory)][string]$Path, [Parameter(Mandatory)][string]$FileName)
    try { return [IO.File]::ReadAllText($Path, (New-Object Text.UTF8Encoding($false))) }
    catch { throw "$FileName could not be read: $($_.Exception.Message)" }
}

# Line-based allowlist YAML-subset parser. Supported: mappings, block lists of
# scalars ("- item") and of mappings ("- key: value"), plain and quoted scalars,
# flow lists ("[a, b]"), full-line comments. Everything else (tabs in
# indentation, duplicate keys, bare keys without a value, stray content) fails
# closed with file name and line number.

function Get-LintSignificantLines {
    param([Parameter(Mandatory)][string]$FileName, [Parameter(Mandatory)][AllowEmptyString()][string]$Text)
    $result = [System.Collections.Generic.List[object]]::new()
    $lineNo = 0
    foreach ($raw in ($Text -split "`r?`n")) {
        $lineNo++
        if ($raw -match '^\s*(?:#.*)?$') { continue }
        $leadingLength = $raw.Length - $raw.TrimStart("`t", ' ').Length
        $leading = $raw.Substring(0, $leadingLength)
        if ($leading.Contains("`t")) { throw "$FileName line ${lineNo}: tabs are not supported in indentation." }
        $result.Add([pscustomobject]@{ Indent = $leadingLength; Text = $raw.Substring($leadingLength); Line = $lineNo })
    }
    return $result
}
function ConvertFrom-LintPlainScalar {
    param([Parameter(Mandatory)][string]$FileName, [int]$Line, [Parameter(Mandatory)][string]$Key, [Parameter(Mandatory)][string]$ValueText)
    if ($ValueText.StartsWith('"') -or $ValueText.StartsWith("'")) {
        $quote = [string]$ValueText[0]
        if ($ValueText.Length -lt 2 -or [string]$ValueText[$ValueText.Length - 1] -ne $quote) {
            throw "$FileName line ${Line}: malformed quoted scalar for '$Key'."
        }
        $inner = $ValueText.Substring(1, $ValueText.Length - 2)
        if ($inner.Contains($quote)) { throw "$FileName line ${Line}: malformed quoted scalar for '$Key'." }
        return $inner
    }
    return $ValueText
}
function ConvertFrom-LintScalar {
    param([Parameter(Mandatory)][string]$FileName, [int]$Line, [Parameter(Mandatory)][string]$Key, [Parameter(Mandatory)][string]$ValueText)
    if ($ValueText -match '^\[(?<inner>.*)\]$') {
        $items = [System.Collections.Generic.List[object]]::new()
        $inner = $Matches.inner.Trim()
        if ($inner) {
            foreach ($part in ($inner -split ',')) {
                $trimmed = $part.Trim()
                if (-not $trimmed) { throw "$FileName line ${Line}: empty item in flow list for '$Key'." }
                $items.Add([pscustomobject]@{ Kind = 'Scalar'; Value = (ConvertFrom-LintPlainScalar $FileName $Line $Key $trimmed); Line = $Line })
            }
        }
        return [pscustomobject]@{ Kind = 'List'; Items = @($items); Line = $Line }
    }
    return [pscustomobject]@{ Kind = 'Scalar'; Value = (ConvertFrom-LintPlainScalar $FileName $Line $Key $ValueText); Line = $Line }
}
$script:lintParseLines = $null
$script:lintParsePos = 0
function Get-LintPeekLine {
    if ($null -ne $script:lintParseLines -and $script:lintParsePos -lt $script:lintParseLines.Count) { return $script:lintParseLines[$script:lintParsePos] }
    return $null
}
function Parse-LintMap {
    param([int]$Indent, [Parameter(Mandatory)][string]$FileName)
    $entries = [ordered]@{}
    while ($true) {
        $line = Get-LintPeekLine
        if ($null -eq $line -or $line.Indent -lt $Indent) { break }
        if ($line.Indent -gt $Indent) { throw "$FileName line $($line.Line): unexpected indentation." }
        if ($line.Text.StartsWith('-')) { throw "$FileName line $($line.Line): list item is not allowed inside a mapping." }
        if ($line.Text -notmatch '^(?<key>[A-Za-z0-9_-]+):(?<value>.*)$') {
            throw "$FileName line $($line.Line): expected a 'key: value' mapping entry, got '$($line.Text)'."
        }
        $key = $Matches.key
        if ($entries.Contains($key)) { throw "$FileName line $($line.Line): duplicate key '$key'." }
        $valueText = $Matches.value.Trim()
        $script:lintParsePos++
        if ($valueText) {
            $next = Get-LintPeekLine
            if ($null -ne $next -and $next.Indent -gt $Indent) { throw "$FileName line $($next.Line): unexpected nested content under '$key'." }
            $entries[$key] = ConvertFrom-LintScalar $FileName $line.Line $key $valueText
        }
        else {
            $next = Get-LintPeekLine
            $listValue = ($null -ne $next -and $next.Indent -ge $Indent -and $next.Text.StartsWith('-'))
            if ($null -eq $next -or ($next.Indent -le $Indent -and -not $listValue)) {
                throw "$FileName line $($line.Line): key '$key' has no value and no nested block."
            }
            if ($listValue) { $entries[$key] = Parse-LintList $next.Indent $FileName }
            else { $entries[$key] = Parse-LintBlock $next.Indent $FileName }
        }
    }
    return [pscustomobject]@{ Kind = 'Map'; Entries = $entries }
}
function Parse-LintList {
    param([int]$Indent, [Parameter(Mandatory)][string]$FileName)
    $items = [System.Collections.Generic.List[object]]::new()
    while ($true) {
        $line = Get-LintPeekLine
        if ($null -eq $line -or $line.Indent -lt $Indent) { break }
        if ($line.Indent -gt $Indent) { throw "$FileName line $($line.Line): unexpected indentation." }
        if (-not $line.Text.StartsWith('-')) { break }
        $rest = $line.Text.Substring(1)
        if (-not $rest.StartsWith(' ')) { throw "$FileName line $($line.Line): list item must start with '- '." }
        if ([string]::IsNullOrWhiteSpace($rest)) { throw "$FileName line $($line.Line): list item without content." }
        $itemText = $rest.TrimStart()
        $keyColumn = $Indent + 1 + ($rest.Length - $itemText.Length)
        if ($itemText -match '^(?<key>[A-Za-z0-9_-]+):(?<value>.*)$') {
            $script:lintParseLines[$script:lintParsePos] = [pscustomobject]@{ Indent = $keyColumn; Text = $itemText; Line = $line.Line }
            $items.Add((Parse-LintMap $keyColumn $FileName))
        }
        else {
            $script:lintParsePos++
            $next = Get-LintPeekLine
            if ($null -ne $next -and $next.Indent -gt $Indent) { throw "$FileName line $($next.Line): unexpected nested content under a scalar list item." }
            $items.Add((ConvertFrom-LintScalar $FileName $line.Line '-[]' $itemText))
        }
    }
    return [pscustomobject]@{ Kind = 'List'; Items = @($items) }
}
function Parse-LintBlock {
    param([int]$Indent, [Parameter(Mandatory)][string]$FileName)
    $first = Get-LintPeekLine
    if ($null -eq $first) { throw "${FileName}: expected a value." }
    if ($first.Text.StartsWith('-')) { return Parse-LintList $Indent $FileName }
    return Parse-LintMap $Indent $FileName
}
function Parse-LintYaml {
    param([Parameter(Mandatory)][string]$FileName, [Parameter(Mandatory)][AllowEmptyString()][string]$Text)
    $script:lintParseLines = Get-LintSignificantLines $FileName $Text
    $script:lintParsePos = 0
    if ($script:lintParseLines.Count -eq 0) { throw "$FileName is empty or contains only comments." }
    $root = Parse-LintBlock $script:lintParseLines[0].Indent $FileName
    if ($script:lintParsePos -lt $script:lintParseLines.Count) {
        $line = $script:lintParseLines[$script:lintParsePos]
        throw "$FileName line $($line.Line): unexpected content outside the root mapping."
    }
    return $root
}

# Schema validation helpers. Every error names the file, the entry and the
# broken field or reference.

function Assert-LintMapFields {
    param([Parameter(Mandatory)][string]$FileName, [Parameter(Mandatory)][string]$EntryLabel, [Parameter(Mandatory)][object]$Node, [Parameter(Mandatory)][string[]]$Required, [string[]]$Optional = @())
    foreach ($key in @($Node.Entries.Keys)) {
        if ($key -cnotin $Required -and $key -cnotin $Optional) { Add-LintError "${FileName}: ${EntryLabel}: unknown field '$key'." }
    }
    foreach ($key in $Required) {
        if (-not $Node.Entries.Contains($key)) { Add-LintError "${FileName}: ${EntryLabel}: missing required field '$key'." }
    }
}
function Get-LintScalarField {
    param([Parameter(Mandatory)][string]$FileName, [Parameter(Mandatory)][string]$EntryLabel, [Parameter(Mandatory)][object]$Node, [Parameter(Mandatory)][string]$Key, [switch]$AllowEmpty)
    if (-not $Node.Entries.Contains($Key)) { return $null }
    $node = $Node.Entries[$Key]
    if ($node.Kind -ne 'Scalar') { Add-LintError "${FileName}: ${EntryLabel}: field '$Key' must be a scalar."; return $null }
    if (-not $AllowEmpty -and [string]::IsNullOrWhiteSpace($node.Value)) { Add-LintError "${FileName}: ${EntryLabel}: field '$Key' must not be empty."; return $null }
    return [string]$node.Value
}
function Get-LintListField {
    param([Parameter(Mandatory)][string]$FileName, [Parameter(Mandatory)][string]$EntryLabel, [Parameter(Mandatory)][object]$Node, [Parameter(Mandatory)][string]$Key)
    if (-not $Node.Entries.Contains($Key)) { return $null }
    $node = $Node.Entries[$Key]
    if ($node.Kind -ne 'List') { Add-LintError "${FileName}: ${EntryLabel}: field '$Key' must be a list of scalars."; return $null }
    foreach ($item in @($node.Items)) {
        if ($item.Kind -ne 'Scalar') { Add-LintError "${FileName}: ${EntryLabel}: field '$Key' must contain only scalars."; return $null }
    }
    return @(@($node.Items) | ForEach-Object { [string]$_.Value })
}
function Assert-LintReferenceList {
    param(
        [Parameter(Mandatory)][string]$FileName,
        [Parameter(Mandatory)][string]$EntryLabel,
        [Parameter(Mandatory)][string]$Field,
        [AllowNull()][string[]]$Values,
        [Parameter(Mandatory)][string]$IdPattern,
        [Parameter(Mandatory)][string]$IdPatternName,
        [Parameter(Mandatory)][AllowEmptyCollection()][System.Collections.Generic.HashSet[string]]$KnownIds,
        [Parameter(Mandatory)][string]$TargetFile,
        [Parameter(Mandatory)][string]$TargetState
    )
    if ($null -eq $Values) { return }
    $seen = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::Ordinal)
    foreach ($value in $Values) {
        if ($value -cnotmatch $IdPattern) {
            Add-LintError "${FileName}: ${EntryLabel}:${Field} item '$value' must match $IdPatternName."
            continue
        }
        if (-not $seen.Add($value)) { Add-LintError "${FileName}: ${EntryLabel}: duplicate reference '$value' in ${Field}."; continue }
        if (-not $KnownIds.Contains($value)) {
            $reason = switch ($TargetState) {
                'absent' { "$TargetFile is absent" }
                'invalid' { "$TargetFile is invalid" }
                default { "not defined in $TargetFile" }
            }
            Add-LintError "${FileName}: ${EntryLabel}: dangling ${Field} reference '$value' ($reason)."
        }
    }
}
function Test-LintScopeGlob([string]$Glob) {
    if ([string]::IsNullOrWhiteSpace($Glob)) { return $false }
    if ($Glob -match '^[A-Za-z]:') { return $false }
    if ($Glob.StartsWith('/') -or $Glob.StartsWith('\')) { return $false }
    foreach ($segment in ($Glob -split '[\\/]')) {
        if ($segment -eq '' -or $segment -eq '..') { return $false }
    }
    return $true
}
function Get-LintScopeGlobError {
    param([Parameter(Mandatory)][string]$FileName, [Parameter(Mandatory)][string]$EntryLabel, [Parameter(Mandatory)][string]$Field, [Parameter(Mandatory)][string]$Glob)
    return "${FileName}: ${EntryLabel}: invalid ${Field} glob '$Glob' (must be relative: no drive letter, no leading slash, no '..' or empty segments)."
}
function Get-LintSpecAnchors {
    param([Parameter(Mandatory)][string]$ChangeRoot)
    $specPath = Join-Path $ChangeRoot 'spec.md'
    if (-not (Test-Path -LiteralPath $specPath -PathType Leaf)) {
        Add-LintError "spec.md: file not found; cannot verify contract.yaml spec_ref anchors."
        return $null
    }
    $specText = Read-LintText $specPath 'spec.md'
    $section = [regex]::Match($specText, "(?ims)^##\s+(Требуемое поведение|Required behavior)\s*$\s*(?<body>.*?)(?=^##\s|\z)")
    if (-not $section.Success) {
        Add-LintError "spec.md: section 'Требуемое поведение' not found; cannot verify contract.yaml spec_ref anchors."
        return $null
    }
    $anchors = [System.Collections.Generic.HashSet[int]]::new()
    foreach ($match in [regex]::Matches($section.Groups['body'].Value, '(?m)^[ \t]*([0-9]+)[.)][ \t]')) {
        [void]$anchors.Add([int]$match.Groups[1].Value)
    }
    return $anchors
}

# Cross-artifact reference state: absent | invalid | ok.

$contractState = 'absent'
$verificationState = 'absent'
$executionState = 'absent'
$requirementIds = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::Ordinal)
$checkIds = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::Ordinal)
$taskIds = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::Ordinal)
$requirementModels = [System.Collections.Generic.List[object]]::new()
$taskModels = [System.Collections.Generic.List[object]]::new()
$checkModels = [System.Collections.Generic.List[object]]::new()

# --- contract.yaml ---

if ($contractPresent) {
    $contract = $null
    try { $contract = Parse-LintYaml 'contract.yaml' (Read-LintText $contractPath 'contract.yaml') }
    catch { Add-LintError $_.Exception.Message }
    if ($null -ne $contract) {
        if ($contract.Kind -ne 'Map') { Add-LintError 'contract.yaml: root must be a mapping.' }
        else {
            Assert-LintMapFields 'contract.yaml' 'document' $contract @('schema_version', 'requirements')
            $schemaVersion = Get-LintScalarField 'contract.yaml' 'document' $contract 'schema_version'
            if ($null -ne $schemaVersion -and $schemaVersion -cne '1') { Add-LintError "contract.yaml: schema_version must be 1 (got '$schemaVersion')." }
            if ($contract.Entries.Contains('requirements')) {
                $requirementsNode = $contract.Entries['requirements']
                if ($requirementsNode.Kind -ne 'List' -or @($requirementsNode.Items).Count -eq 0) {
                    Add-LintError 'contract.yaml: requirements must be a non-empty list.'
                }
                else {
                    $anchors = $null
                    $entryIndex = 0
                    foreach ($item in @($requirementsNode.Items)) {
                        $entryIndex++
                        $label = "requirement #$entryIndex"
                        if ($item.Kind -ne 'Map') { Add-LintError "contract.yaml: requirement #$entryIndex must be a mapping."; continue }
                        $id = Get-LintScalarField 'contract.yaml' $label $item 'id'
                        if ($null -ne $id) {
                            if ($id -cnotmatch '^R-[0-9]{3}$') { Add-LintError "contract.yaml: $label id must match R-NNN (got '$id')." }
                            elseif (-not $requirementIds.Add($id)) { Add-LintError "contract.yaml: duplicate requirement id $id." }
                            else { $label = "requirement $id" }
                        }
                        Assert-LintMapFields 'contract.yaml' $label $item @('id', 'spec_ref') @('constraints')
                        $specRef = Get-LintScalarField 'contract.yaml' $label $item 'spec_ref'
                        $specRefValue = $null
                        if ($null -ne $specRef) {
                            if ($specRef -notmatch '^[0-9]+$' -or [int]$specRef -lt 1) {
                                Add-LintError "contract.yaml: $label spec_ref must be a positive integer (got '$specRef')."
                            }
                            else {
                                if ($null -eq $anchors) { $anchors = Get-LintSpecAnchors $changeRoot }
                                if ($null -ne $anchors -and -not $anchors.Contains([int]$specRef)) {
                                    Add-LintError "contract.yaml: $label spec_ref $specRef does not match any numbered item in the spec.md section 'Требуемое поведение'."
                                }
                                $specRefValue = [int]$specRef
                            }
                        }
                        $constraints = [ordered]@{}
                        if ($item.Entries.Contains('constraints')) {
                            $constraintsNode = $item.Entries['constraints']
                            if ($constraintsNode.Kind -ne 'Map') { Add-LintError "contract.yaml: $label constraints must be a flat scalar key/value mapping." }
                            else {
                                foreach ($constraintKey in @($constraintsNode.Entries.Keys)) {
                                    $constraintValue = $constraintsNode.Entries[$constraintKey]
                                    if ($constraintValue.Kind -ne 'Scalar' -or [string]::IsNullOrWhiteSpace($constraintValue.Value)) {
                                        Add-LintError "contract.yaml: $label constraints.$constraintKey must be a non-empty scalar."
                                    }
                                    else { $constraints[$constraintKey] = [string]$constraintValue.Value }
                                }
                            }
                        }
                        $requirementModels.Add([pscustomobject]@{ Id = $id; SpecRef = $specRefValue; Constraints = $constraints })
                    }
                }
            }
        }
    }
    $contractState = if ($requirementIds.Count -gt 0 -and -not @($errors | Where-Object { $_ -like 'contract.yaml*' }).Count) { 'ok' } else { 'invalid' }
}

# --- verification.yaml ---

if ($verificationPresent) {
    $verification = $null
    try { $verification = Parse-LintYaml 'verification.yaml' (Read-LintText $verificationPath 'verification.yaml') }
    catch { Add-LintError $_.Exception.Message }
    if ($null -ne $verification) {
        if ($verification.Kind -ne 'Map') { Add-LintError 'verification.yaml: root must be a mapping.' }
        else {
            Assert-LintMapFields 'verification.yaml' 'document' $verification @('schema_version', 'checks')
            $schemaVersion = Get-LintScalarField 'verification.yaml' 'document' $verification 'schema_version'
            if ($null -ne $schemaVersion -and $schemaVersion -cne '1') { Add-LintError "verification.yaml: schema_version must be 1 (got '$schemaVersion')." }
            if ($verification.Entries.Contains('checks')) {
                $checksNode = $verification.Entries['checks']
                if ($checksNode.Kind -ne 'List' -or @($checksNode.Items).Count -eq 0) {
                    Add-LintError 'verification.yaml: checks must be a non-empty list.'
                }
                else {
                    $entryIndex = 0
                    foreach ($item in @($checksNode.Items)) {
                        $entryIndex++
                        $label = "check #$entryIndex"
                        if ($item.Kind -ne 'Map') { Add-LintError "verification.yaml: check #$entryIndex must be a mapping."; continue }
                        $id = Get-LintScalarField 'verification.yaml' $label $item 'id'
                        if ($null -ne $id) {
                            if ($id -cnotmatch '^V-[0-9]{3}$') { Add-LintError "verification.yaml: $label id must match V-NNN (got '$id')." }
                            elseif (-not $checkIds.Add($id)) { Add-LintError "verification.yaml: duplicate check id $id." }
                            else { $label = "check $id" }
                        }
                        Assert-LintMapFields 'verification.yaml' $label $item @('id', 'requirement', 'type', 'expect')
                        $requirement = Get-LintScalarField 'verification.yaml' $label $item 'requirement'
                        if ($null -ne $requirement -and $requirement -cnotmatch '^R-[0-9]{3}$') {
                            Add-LintError "verification.yaml: $label requirement must match R-NNN (got '$requirement')."
                            $requirement = $null
                        }
                        if ($null -ne $requirement -and -not $requirementIds.Contains($requirement)) {
                            $reason = switch ($contractState) {
                                'absent' { 'contract.yaml is absent' }
                                'invalid' { 'contract.yaml is invalid' }
                                default { "not defined in contract.yaml" }
                            }
                            Add-LintError "verification.yaml: $label dangling requirement reference '$requirement' ($reason)."
                        }
                        $type = Get-LintScalarField 'verification.yaml' $label $item 'type'
                        if ($null -ne $type -and $type -cnotin $allowedCheckTypes) {
                            Add-LintError "verification.yaml: $label type must be one of: $($allowedCheckTypes -join ', ') (got '$type')."
                        }
                        $expectCount = 0
                        if ($item.Entries.Contains('expect')) {
                            $expectNode = $item.Entries['expect']
                            if ($expectNode.Kind -ne 'Map') { Add-LintError "verification.yaml: $label expect must be a mapping." }
                            else {
                                foreach ($expectKey in @($expectNode.Entries.Keys)) {
                                    $expectValue = $expectNode.Entries[$expectKey]
                                    if ($expectValue.Kind -ne 'Scalar' -or [string]::IsNullOrWhiteSpace($expectValue.Value)) {
                                        Add-LintError "verification.yaml: $label expect.$expectKey must be a non-empty scalar."
                                    }
                                    else { $expectCount++ }
                                }
                                if ($expectCount -eq 0) {
                                    Add-LintError "verification.yaml: $label expect must contain at least one observable field."
                                }
                            }
                        }
                        $checkModels.Add([pscustomobject]@{ Id = $id; Requirement = $requirement; Type = $type })
                    }
                }
            }
        }
    }
    $verificationState = if ($checkIds.Count -gt 0 -and -not @($errors | Where-Object { $_ -like 'verification.yaml*' }).Count) { 'ok' } else { 'invalid' }
}

# --- execution.yaml ---

if ($executionPresent) {
    $execution = $null
    try { $execution = Parse-LintYaml 'execution.yaml' (Read-LintText $executionPath 'execution.yaml') }
    catch { Add-LintError $_.Exception.Message }
    if ($null -ne $execution) {
        if ($execution.Kind -ne 'Map') { Add-LintError 'execution.yaml: root must be a mapping.' }
        else {
            Assert-LintMapFields 'execution.yaml' 'document' $execution @('schema_version', 'tasks')
            $schemaVersion = Get-LintScalarField 'execution.yaml' 'document' $execution 'schema_version'
            if ($null -ne $schemaVersion -and $schemaVersion -cne '1') { Add-LintError "execution.yaml: schema_version must be 1 (got '$schemaVersion')." }
            if ($execution.Entries.Contains('tasks')) {
                $tasksNode = $execution.Entries['tasks']
                if ($tasksNode.Kind -ne 'List' -or @($tasksNode.Items).Count -eq 0) {
                    Add-LintError 'execution.yaml: tasks must be a non-empty list.'
                }
                else {
                    $entryIndex = 0
                    foreach ($item in @($tasksNode.Items)) {
                        $entryIndex++
                        $label = "task #$entryIndex"
                        if ($item.Kind -ne 'Map') { Add-LintError "execution.yaml: task #$entryIndex must be a mapping."; continue }
                        $id = Get-LintScalarField 'execution.yaml' $label $item 'id'
                        if ($null -ne $id) {
                            if ($id -cnotmatch '^T-[0-9]{3}$') { Add-LintError "execution.yaml: $label id must match T-NNN (got '$id')." }
                            elseif (-not $taskIds.Add($id)) { Add-LintError "execution.yaml: duplicate task id $id." }
                            else { $label = "task $id" }
                        }
                        Assert-LintMapFields 'execution.yaml' $label $item @('id', 'kind', 'goal', 'depends_on', 'satisfies', 'verify', 'allowed_scope', 'forbidden', 'mutation')
                        $kind = Get-LintScalarField 'execution.yaml' $label $item 'kind'
                        if ($null -ne $kind -and $kind -cnotin $allowedTaskKinds) {
                            Add-LintError "execution.yaml: $label unknown kind '$kind' (allowed: $($allowedTaskKinds -join ', '))."
                        }
                        $goal = Get-LintScalarField 'execution.yaml' $label $item 'goal'
                        $mutation = Get-LintScalarField 'execution.yaml' $label $item 'mutation'
                        if ($null -ne $mutation -and $mutation -cnotin @('allowed', 'forbidden')) {
                            Add-LintError "execution.yaml: $label mutation must be 'allowed' or 'forbidden' (got '$mutation')."
                        }
                        $dependsOn = Get-LintListField 'execution.yaml' $label $item 'depends_on'
                        if ($null -ne $dependsOn) {
                            foreach ($ref in $dependsOn) {
                                if ($ref -cnotmatch '^T-[0-9]{3}$') { Add-LintError "execution.yaml: $label depends_on item '$ref' must match T-NNN." }
                                elseif ($id -and $ref -ceq $id) { Add-LintError "execution.yaml: $label self-reference in depends_on ('$ref')." }
                            }
                        }
                        $allowedScope = Get-LintListField 'execution.yaml' $label $item 'allowed_scope'
                        $forbidden = Get-LintListField 'execution.yaml' $label $item 'forbidden'
                        foreach ($scopeEntry in @(@( 'allowed_scope', $allowedScope), @('forbidden', $forbidden))) {
                            if ($null -ne $scopeEntry[1]) {
                                foreach ($glob in $scopeEntry[1]) {
                                    if (-not (Test-LintScopeGlob $glob)) {
                                        Add-LintError (Get-LintScopeGlobError 'execution.yaml' $label $scopeEntry[0] $glob)
                                    }
                                }
                            }
                        }
                        $satisfies = Get-LintListField 'execution.yaml' $label $item 'satisfies'
                        $verify = Get-LintListField 'execution.yaml' $label $item 'verify'
                        $taskModels.Add([pscustomobject]@{
                            Id = $id; Kind = $kind; Goal = $goal
                            DependsOn = $dependsOn; Satisfies = $satisfies; Verify = $verify
                            AllowedScope = $allowedScope
                            Forbidden = $forbidden
                            Mutation = $mutation
                        })
                    }
                }
            }
        }
    }
    $executionState = if ($taskIds.Count -gt 0 -and -not @($errors | Where-Object { $_ -like 'execution.yaml*' }).Count) { 'ok' } else { 'invalid' }
}

# --- cross-artifact references and DAG acyclicity ---

foreach ($task in @($taskModels)) {
    if ($null -eq $task.Id) { continue }
    $label = "task $($task.Id)"
    Assert-LintReferenceList 'execution.yaml' $label 'depends_on' $task.DependsOn '^T-[0-9]{3}$' 'T-NNN' $taskIds 'execution.yaml' $executionState
    Assert-LintReferenceList 'execution.yaml' $label 'satisfies' $task.Satisfies '^R-[0-9]{3}$' 'R-NNN' $requirementIds 'contract.yaml' $contractState
    Assert-LintReferenceList 'execution.yaml' $label 'verify' $task.Verify '^V-[0-9]{3}$' 'V-NNN' $checkIds 'verification.yaml' $verificationState
}

# Deterministic Kahn's algorithm; on a cycle the error reports the cycle path.
if ($taskModels.Count -gt 0 -and $taskIds.Count -eq $taskModels.Count) {
    $indegree = @{}
    $dependents = @{}
    foreach ($task in @($taskModels)) {
        $indegree[$task.Id] = 0
        $dependents[$task.Id] = [System.Collections.Generic.List[string]]::new()
    }
    foreach ($task in @($taskModels)) {
        foreach ($dep in @($task.DependsOn | Where-Object { $null -ne $_ })) {
            if (-not $indegree.ContainsKey($dep)) { continue }
            $indegree[$task.Id]++
            $dependents[$dep].Add($task.Id)
        }
    }
    $ready = [System.Collections.Generic.List[string]]::new()
    foreach ($taskId in @($indegree.Keys | Where-Object { $indegree[$_] -eq 0 })) { $ready.Add($taskId) }
    $order = [System.Collections.Generic.List[string]]::new()
    while ($ready.Count -gt 0) {
        $ready.Sort([System.StringComparer]::Ordinal)
        $current = $ready[0]
        $ready.RemoveAt(0)
        $order.Add($current)
        foreach ($dependent in $dependents[$current]) {
            $indegree[$dependent]--
            if ($indegree[$dependent] -eq 0) { $ready.Add($dependent) }
        }
    }
    if ($order.Count -lt $taskModels.Count) {
        $remaining = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::Ordinal)
        foreach ($task in @($taskModels)) { if (-not $order.Contains($task.Id)) { [void]$remaining.Add($task.Id) } }
        $path = [System.Collections.Generic.List[string]]::new()
        $position = @{}
        $current = @($remaining | Sort-Object { $_ })[0]
        while ($null -ne $current -and -not $position.ContainsKey($current)) {
            $position[$current] = $path.Count
            $path.Add($current)
            $next = $null
            foreach ($candidate in @(($taskModels | Where-Object { $_.Id -ceq $current })[0].DependsOn)) {
                if ($remaining.Contains($candidate)) { $next = $candidate; break }
            }
            $current = $next
        }
        if ($null -ne $current) {
            $cycleStart = $position[$current]
            $cyclePath = @($path | Select-Object -Skip $cycleStart) + @($current)
            Add-LintError "execution.yaml: dependency cycle detected: $($cyclePath -join ' -> ')."
        }
    }
}

# --- result ---

$artifacts = [ordered]@{
    contract = $contractPresent
    execution = $executionPresent
    verification = $verificationPresent
}
if (-not $contractPresent -and -not $executionPresent -and -not $verificationPresent) {
    $summary = 'artifacts: none'
}
else {
    $parts = [System.Collections.Generic.List[string]]::new()
    if ($contractPresent) { $parts.Add("contract: $($requirementIds.Count) requirements") }
    if ($executionPresent) { $parts.Add("execution: $($taskIds.Count) tasks") }
    if ($verificationPresent) { $parts.Add("verification: $($checkIds.Count) checks") }
    $summary = $parts -join '; '
}

if ($AsModel) {
    return [pscustomobject]@{
        Passed = ($errors.Count -eq 0)
        Errors = @($errors)
        Artifacts = $artifacts
        Requirements = @($requirementModels)
        Tasks = @($taskModels)
        Checks = @($checkModels)
    }
}

$result = [ordered]@{
    schema_version = 1
    checked_at_utc = [DateTime]::UtcNow.ToString('o')
    passed = ($errors.Count -eq 0)
    summary = $summary
    errors = @($errors)
    warnings = @($warnings)
    artifacts = $artifacts
    stats = [ordered]@{ requirements = $requirementIds.Count; tasks = $taskIds.Count; checks = $checkIds.Count }
}
if (-not $OutputPath) { $OutputPath = Join-Path $changeRoot 'contract-lint.json' }
$directory = Split-Path -Parent $OutputPath
if (-not (Test-Path -LiteralPath $directory -PathType Container)) { New-Item -ItemType Directory -Path $directory -Force | Out-Null }
$tempPath = Join-Path $directory ('.' + [System.IO.Path]::GetFileName($OutputPath) + '.' + [guid]::NewGuid().ToString('N') + '.tmp')
try {
    $result | ConvertTo-Json -Depth 20 | Set-Content -LiteralPath $tempPath -Encoding utf8
    Move-Item -LiteralPath $tempPath -Destination $OutputPath -Force
}
finally {
    if (Test-Path -LiteralPath $tempPath -PathType Leaf) { Remove-Item -LiteralPath $tempPath -Force -ErrorAction SilentlyContinue }
}
if (-not $result.passed -and -not $NoThrow) { throw "Specification contract lint failed. See: $OutputPath" }
return [pscustomobject]$result
