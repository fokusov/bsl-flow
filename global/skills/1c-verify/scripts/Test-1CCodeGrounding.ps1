#Requires -Version 7.0
<#
.SYNOPSIS
    Grounding lint over changed BSL code, per docs/plans/2026-09-26-remediation-plan.md
    section Ф5.2 ("Grounding по diff"). Finds Справочники.X, Документы.X,
    РегистрыСведений.X, РегистрыНакопления.X, Перечисления.X.Y, Константы.X,
    Метаданные.<Тип>.X and <ИмяОбщегоМодуля>.Метод( references in every changed
    or untracked .bsl file and fails when a referenced name does not exist in
    the real metadata index.

.PARAMETER ProjectPath
    Project root (a git working tree). Changed files are discovered via
    `git diff --name-only <BaseRef>` (staged + unstaged against BaseRef) plus
    untracked files (`git ls-files --others --exclude-standard`).

.PARAMETER BaseRef
    Git ref to diff against. Defaults to HEAD.
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$ProjectPath,
    [string]$BaseRef = 'HEAD',
    [string]$OutputPath,
    [switch]$NoThrow
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot '..\..\1c-spec-review\scripts\Review.Common.ps1')

$projectRoot = [System.IO.Path]::GetFullPath($ProjectPath)
if (-not (Test-Path -LiteralPath $projectRoot -PathType Container)) { throw "ProjectPath not found: $projectRoot" }

function Invoke-BSLFlowGit {
    param([Parameter(Mandatory)][string[]]$Arguments)
    # Git diagnostics must never enter the file list: a global excludes-file warning used to be
    # parsed as a changed path. Keep stdout/stderr separate and retain non-ASCII paths verbatim.
    $psi = [Diagnostics.ProcessStartInfo]::new()
    $psi.FileName = 'git'
    $psi.WorkingDirectory = $projectRoot
    $psi.RedirectStandardOutput = $true
    $psi.RedirectStandardError = $true
    $psi.UseShellExecute = $false
    foreach ($argument in @('-c', 'core.quotepath=false', '-C', $projectRoot) + $Arguments) { [void]$psi.ArgumentList.Add($argument) }
    $process = [Diagnostics.Process]::Start($psi)
    $stdoutTask = $process.StandardOutput.ReadToEndAsync()
    $stderrTask = $process.StandardError.ReadToEndAsync()
    $process.WaitForExit()
    $stdout = $stdoutTask.GetAwaiter().GetResult()
    $stderr = $stderrTask.GetAwaiter().GetResult()
    $exitCode = $process.ExitCode
    $process.Dispose()
    if ($exitCode -ne 0) { throw "git $($Arguments -join ' ') failed (exit $exitCode): $stderr" }
    return @($stdout -split "`r?`n" | Where-Object { $_ -ne '' })
}

$changedFiles = [System.Collections.Generic.List[string]]::new()
try {
    foreach ($line in (Invoke-BSLFlowGit -Arguments @('diff', '--name-only', '--diff-filter=ACMR', $BaseRef, '--', '*.bsl'))) {
        if ($line) { $changedFiles.Add($line) }
    }
    foreach ($line in (Invoke-BSLFlowGit -Arguments @('ls-files', '--others', '--exclude-standard', '--', '*.bsl'))) {
        if ($line) { $changedFiles.Add($line) }
    }
}
catch {
    $message = "BF_BLOCKED: unable to enumerate changed BSL files: $($_.Exception.Message)"
    $result = [ordered]@{ schema_version = 1; verdict = 'BLOCKED'; passed = $false; errors = @(); files_checked = 0; references_checked = 0; index = $null; message = $message }
    if (-not $OutputPath) { $OutputPath = Join-Path $projectRoot '.bsl-flow\cache\code-grounding.json' }
    Write-BSLFlowJsonAtomic -Value $result -Path $OutputPath
    if (-not $NoThrow) { throw $message }
    return [pscustomobject]$result
}
$changedFiles = @($changedFiles | Select-Object -Unique | Where-Object { Test-Path -LiteralPath (Join-Path $projectRoot $_) -PathType Leaf })

# ---------------------------------------------------------------------------
# Resolve metadata source roots and build the index.
# ---------------------------------------------------------------------------

function Get-BSLFlowYamlListValues {
    param([Parameter(Mandatory)][AllowEmptyString()][string]$Text, [Parameter(Mandatory)][string[]]$Path)
    $stack = [System.Collections.Generic.List[object]]::new()
    $inTargetList = $false
    $targetIndent = -1
    $results = [System.Collections.Generic.List[string]]::new()
    foreach ($line in ($Text -split "`r?`n")) {
        if ($line -match '^\s*(?:#.*)?$') { continue }
        if ($line -match '^(?<indent>\s*)-\s+(?<value>.+?)\s*$' -and $inTargetList) {
            $indent = $Matches.indent.Length
            if ($indent -gt $targetIndent) { $results.Add($Matches.value.Trim('"', "'")); continue }
            $inTargetList = $false
        }
        if ($line -notmatch '^(?<indent>\s*)(?<key>[A-Za-z0-9_-]+):(?:\s*(?<value>.*?))?\s*$') { continue }
        $indent = $Matches.indent.Length
        while ($stack.Count -gt 0 -and $stack[$stack.Count - 1].Indent -ge $indent) { $stack.RemoveAt($stack.Count - 1) }
        $keys = @($stack | ForEach-Object { $_.Key }) + @($Matches.key)
        $value = $Matches.value.Trim()
        if (-not $value -and (($keys -join '/') -eq ($Path -join '/'))) { $inTargetList = $true; $targetIndent = $indent }
        else { $inTargetList = $false }
        if (-not $value) { $stack.Add([pscustomobject]@{ Indent = $indent; Key = $Matches.key }) }
    }
    return @($results)
}

$configPath = Join-Path $projectRoot 'bsl-flow.yaml'
$sourcePaths = @()
if (Test-Path -LiteralPath $configPath -PathType Leaf) {
    $configText = [IO.File]::ReadAllText($configPath, [Text.UTF8Encoding]::new($false))
    $sourcePaths = @(Get-BSLFlowYamlListValues -Text $configText -Path @('source', 'paths'))
}
if ($sourcePaths.Count -eq 0) { $sourcePaths = @('src') }
$sourceRoots = @($sourcePaths | ForEach-Object {
        $candidate = if ([System.IO.Path]::IsPathRooted($_)) { $_ } else { Join-Path $projectRoot $_ }
        [System.IO.Path]::GetFullPath($candidate)
    } | Where-Object { Test-Path -LiteralPath $_ -PathType Container })

$metadataIndexScript = Join-Path $PSScriptRoot '..\..\1c-spec-review\scripts\Get-1CMetadataIndex.ps1'
if (-not (Test-Path -LiteralPath $metadataIndexScript -PathType Leaf)) { throw "Missing Get-1CMetadataIndex.ps1: $metadataIndexScript" }
if ($sourceRoots.Count -gt 0) {
    $cachePath = Join-Path $projectRoot '.bsl-flow\cache\metadata-index.json'
    $index = & $metadataIndexScript -SourceRoot $sourceRoots -CachePath $cachePath
}
else {
    $index = [pscustomobject]@{ schema_version = 1; format = 'unknown'; objects = [pscustomobject]@{}; modules = [pscustomobject]@{} }
}

# ---------------------------------------------------------------------------
# Reference extraction over changed .bsl files.
# ---------------------------------------------------------------------------

$pluralToCanonical = [ordered]@{
    'Справочники'        = 'Справочник'
    'Документы'          = 'Документ'
    'РегистрыСведений'   = 'РегистрСведений'
    'РегистрыНакопления' = 'РегистрНакопления'
    'РегистрыБухгалтерии' = 'РегистрБухгалтерии'
    'РегистрыРасчета'    = 'РегистрРасчета'
    'Перечисления'        = 'Перечисление'
    'Константы'           = 'Константа'
}
$identPattern = '[A-Za-zА-Яа-яЁё0-9_]+'
$pluralAlternation = ($pluralToCanonical.Keys | ForEach-Object { [regex]::Escape($_) }) -join '|'

$knownModuleNames = [System.Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
foreach ($moduleKey in @($index.modules.PSObject.Properties.Name)) {
    if ($moduleKey -match '^ОбщийМодуль\.(?<name>.+)$') { [void]$knownModuleNames.Add($Matches.name) }
}

$errors = [System.Collections.Generic.List[object]]::new()
$referencesChecked = 0

function Add-CodeGroundingError {
    param([System.Collections.Generic.List[object]]$Sink, [string]$File, [int]$Line, [string]$Reference, [string]$Message)
    $Sink.Add([ordered]@{ file = $File; line = $Line; reference = $Reference; message = $Message })
}

function Get-BSLFlowLineNumberFromOffset {
    param([string]$Text, [int]$Offset)
    if ($Offset -lt 0) { return 1 }
    return ([regex]::Matches($Text.Substring(0, [Math]::Min($Offset, $Text.Length)), "`n").Count + 1)
}

foreach ($relativePath in $changedFiles) {
    $fullPath = Join-Path $projectRoot $relativePath
    $text = [IO.File]::ReadAllText($fullPath, [Text.UTF8Encoding]::new($false))

    # Метаданные.<PluralType>.Имя[.Значение] and bare <PluralType>.Имя[.Значение]
    $pattern = "\b(?:Метаданные\.)?(?<plural>$pluralAlternation)\.(?<name>$identPattern)(?:\.(?<extra>$identPattern))?"
    foreach ($match in [regex]::Matches($text, $pattern)) {
        $referencesChecked++
        $canonical = $pluralToCanonical[$match.Groups['plural'].Value]
        $objName = $match.Groups['name'].Value
        $objectKey = "$canonical.$objName"
        $line = Get-BSLFlowLineNumberFromOffset -Text $text -Offset $match.Index
        if (-not $index.objects.PSObject.Properties[$objectKey]) {
            Add-CodeGroundingError -Sink $errors -File $relativePath -Line $line -Reference $match.Value -Message "Unknown metadata object: $objectKey"
            continue
        }
        if ($canonical -eq 'Перечисление' -and $match.Groups['extra'].Success) {
            $enumValues = @($index.objects.$objectKey.enum_values)
            $valueName = $match.Groups['extra'].Value
            if ($valueName -notin $enumValues) {
                Add-CodeGroundingError -Sink $errors -File $relativePath -Line $line -Reference $match.Value -Message "Unknown enum value: $objectKey.$valueName"
            }
        }
    }

    # <ИмяОбщегоМодуля>.Метод( where ИмяОбщегоМодуля is a known common module.
    $callPattern = "\b(?<mod>$identPattern)\.(?<method>$identPattern)\s*\("
    foreach ($match in [regex]::Matches($text, $callPattern)) {
        $modName = $match.Groups['mod'].Value
        if (-not $knownModuleNames.Contains($modName)) { continue }
        $referencesChecked++
        $methodName = $match.Groups['method'].Value
        $line = Get-BSLFlowLineNumberFromOffset -Text $text -Offset $match.Index
        $methods = $index.modules.("ОбщийМодуль.$modName").methods
        if (-not $methods.PSObject.Properties[$methodName]) {
            Add-CodeGroundingError -Sink $errors -File $relativePath -Line $line -Reference $match.Value -Message "Unknown method: ОбщийМодуль.$modName.$methodName"
            continue
        }
        if (-not $methods.$methodName.export) {
            Add-CodeGroundingError -Sink $errors -File $relativePath -Line $line -Reference $match.Value -Message "Call to a non-export common module method: ОбщийМодуль.$modName.$methodName"
        }
    }
}

$verdict = if ($errors.Count -eq 0) { 'PASS' } else { 'FAIL' }
$result = [ordered]@{
    schema_version      = 1
    verdict              = $verdict
    passed               = ($verdict -eq 'PASS')
    errors               = @($errors)
    files_checked        = @($changedFiles).Count
    references_checked   = $referencesChecked
    index                = [ordered]@{ format = $index.format; objects = @($index.objects.PSObject.Properties).Count }
}
if (-not $OutputPath) { $OutputPath = Join-Path $projectRoot '.bsl-flow\cache\code-grounding.json' }
Write-BSLFlowJsonAtomic -Value $result -Path $OutputPath
if ($verdict -eq 'FAIL' -and -not $NoThrow) { throw "Code grounding failed. See: $OutputPath" }
return [pscustomobject]$result
