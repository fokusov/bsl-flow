#Requires -Version 7.0
<#
.SYNOPSIS
    Grounding lint: checks that every 1C metadata reference in spec.md (and
    design.md, if present) actually exists in the project's real metadata,
    per docs/plans/2026-09-26-remediation-plan.md section Ф5.2.

.DESCRIPTION
    References are extracted from RU/EN, singular/plural forms:
      Тип.Имя | Тип.Имя.Реквизит | Тип.Имя.ТЧ.Реквизит | Тип.Имя.ТабличнаяЧасть.ТЧ.Реквизит
      ОбщийМодуль.Имя.Метод(
    An object/attribute/method that is not found is:
      - an error when the reference sits in a normative section
        (Контекст 1С / 1C context, Требуемое поведение / Required behavior,
        Критерии приёмки / Acceptance criteria);
      - a warning in any other section (e.g. Неопределённости / допущения).
    Calling a non-export common module method is always an error.
    A reference marked "(новый)"/"(new)" right after it, or listed under the
    optional "## Новые объекты метаданных" / "## New metadata objects" section,
    is expected to NOT exist; if it does, that is a "collision" error.
    No source root resolves to an existing directory -> status "unavailable",
    a warning, passed:true (S-sized changes commonly lack local sources).
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$ChangePath,
    [string[]]$SourceRoot,
    [string]$OutputPath,
    [switch]$NoThrow
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'Review.Common.ps1')

$changeRoot = [System.IO.Path]::GetFullPath($ChangePath)
$specPath = Join-Path $changeRoot 'spec.md'
if (-not (Test-Path -LiteralPath $specPath -PathType Leaf)) { throw "spec.md not found: $specPath" }
$designPath = Join-Path $changeRoot 'design.md'

$utf8NoBom = [Text.UTF8Encoding]::new($false)
$documents = [System.Collections.Generic.List[object]]::new()
$documents.Add([pscustomobject]@{ File = 'spec.md'; Text = [IO.File]::ReadAllText($specPath, $utf8NoBom) })
if (Test-Path -LiteralPath $designPath -PathType Leaf) {
    $documents.Add([pscustomobject]@{ File = 'design.md'; Text = [IO.File]::ReadAllText($designPath, $utf8NoBom) })
}

# ---------------------------------------------------------------------------
# Resolve source roots: explicit -SourceRoot, else bsl-flow.yaml source.paths
# relative to the project root (first ancestor of ChangePath containing an
# "openspec" directory), else "src" under that project root.
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

function Find-BSLFlowProjectRoot {
    param([Parameter(Mandatory)][string]$StartPath)
    $current = [System.IO.Path]::GetFullPath($StartPath)
    while ($true) {
        # A bare `openspec` folder is not enough: %LOCALAPPDATA%\openspec holds
        # the global OpenSpec schemas and must not be mistaken for a project.
        if (Test-Path -LiteralPath (Join-Path $current 'openspec/config.yaml') -PathType Leaf) { return $current }
        $parent = Split-Path -Parent $current
        if (-not $parent -or $parent -eq $current) { return $null }
        $current = $parent
    }
}

$projectRoot = Find-BSLFlowProjectRoot -StartPath $changeRoot
$resolvedRoots = [System.Collections.Generic.List[string]]::new()
if ($SourceRoot -and $SourceRoot.Count -gt 0) {
    foreach ($root in $SourceRoot) { $resolvedRoots.Add([System.IO.Path]::GetFullPath($root)) }
}
elseif ($projectRoot) {
    $configPath = Join-Path $projectRoot 'bsl-flow.yaml'
    $paths = @()
    if (Test-Path -LiteralPath $configPath -PathType Leaf) {
        $configText = [IO.File]::ReadAllText($configPath, $utf8NoBom)
        $paths = @(Get-BSLFlowYamlListValues -Text $configText -Path @('source', 'paths'))
    }
    if ($paths.Count -eq 0) { $paths = @('src') }
    foreach ($path in $paths) {
        $candidate = if ([System.IO.Path]::IsPathRooted($path)) { $path } else { Join-Path $projectRoot $path }
        $resolvedRoots.Add([System.IO.Path]::GetFullPath($candidate))
    }
}

$errors = [System.Collections.Generic.List[object]]::new()
$warnings = [System.Collections.Generic.List[object]]::new()

function Add-Finding {
    param([System.Collections.Generic.List[object]]$Sink, [string]$Reference, [string]$Message, [int]$Line, [string]$Suggestion)
    $Sink.Add([ordered]@{ line = $Line; reference = $Reference; message = $Message; suggestion = $Suggestion })
}

$existingRoots = @($resolvedRoots | Where-Object { Test-Path -LiteralPath $_ -PathType Container })
if ($existingRoots.Count -eq 0) {
    $result = [ordered]@{
        schema_version     = 1
        status             = 'unavailable'
        passed             = $true
        errors             = @()
        warnings           = @([ordered]@{ line = 0; reference = $null; message = 'No metadata source root is available; grounding checks were skipped.'; suggestion = $null })
        references_checked = 0
        index              = [ordered]@{ format = 'unknown'; objects = 0 }
    }
    if (-not $OutputPath) { $OutputPath = Join-Path $changeRoot 'spec-grounding.json' }
    Write-BSLFlowJsonAtomic -Value $result -Path $OutputPath
    return [pscustomobject]$result
}

# Without a project there is no project-owned place for the cache.
$cachePath = if ($projectRoot) { Join-Path $projectRoot '.bsl-flow/cache/metadata-index.json' } else { $null }
$index = & (Join-Path $PSScriptRoot 'Get-1CMetadataIndex.ps1') -SourceRoot $existingRoots -CachePath $cachePath

# ---------------------------------------------------------------------------
# Reference extraction
# ---------------------------------------------------------------------------

$typeAliases = [ordered]@{
    'Справочник'                = @('Справочник', 'Справочники', 'Catalog', 'Catalogs')
    'Документ'                  = @('Документ', 'Документы', 'Document', 'Documents')
    'РегистрНакопления'         = @('РегистрНакопления', 'РегистрыНакопления', 'AccumulationRegister', 'AccumulationRegisters')
    'РегистрСведений'           = @('РегистрСведений', 'РегистрыСведений', 'InformationRegister', 'InformationRegisters')
    'РегистрБухгалтерии'        = @('РегистрБухгалтерии', 'РегистрыБухгалтерии', 'AccountingRegister', 'AccountingRegisters')
    'РегистрРасчета'            = @('РегистрРасчета', 'РегистрыРасчета', 'CalculationRegister', 'CalculationRegisters')
    'ОбщийМодуль'                = @('ОбщийМодуль', 'ОбщиеМодули', 'CommonModule', 'CommonModules')
    'Перечисление'               = @('Перечисление', 'Перечисления', 'Enum', 'Enums')
    'ПланВидовХарактеристик'    = @('ПланВидовХарактеристик', 'ПланыВидовХарактеристик', 'ChartOfCharacteristicTypes', 'ChartsOfCharacteristicTypes')
    'ПланСчетов'                 = @('ПланСчетов', 'ПланыСчетов', 'ChartOfAccounts', 'ChartsOfAccounts')
    'Обработка'                  = @('Обработка', 'Обработки', 'DataProcessor', 'DataProcessors')
    'Отчет'                      = @('Отчет', 'Отчёт', 'Отчеты', 'Отчёты', 'Report', 'Reports')
    'Константа'                  = @('Константа', 'Константы', 'Constant', 'Constants')
    'РегламентноеЗадание'       = @('РегламентноеЗадание', 'РегламентныеЗадания', 'ScheduledJob', 'ScheduledJobs')
    'HTTPСервис'                 = @('HTTPСервис', 'HTTPСервисы', 'HTTPService', 'HTTPServices')
    'ОбщаяФорма'                 = @('ОбщаяФорма', 'ОбщиеФормы', 'CommonForm', 'CommonForms')
    'Роль'                       = @('Роль', 'Роли', 'Role', 'Roles')
    'Подсистема'                 = @('Подсистема', 'Подсистемы', 'Subsystem', 'Subsystems')
    'ПланОбмена'                 = @('ПланОбмена', 'ПланыОбмена', 'ExchangePlan', 'ExchangePlans')
    'БизнесПроцесс'              = @('БизнесПроцесс', 'БизнесПроцессы', 'BusinessProcess', 'BusinessProcesses')
    'Задача'                     = @('Задача', 'Задачи', 'Task', 'Tasks')
    'ЖурналДокументов'          = @('ЖурналДокументов', 'ЖурналыДокументов', 'DocumentJournal', 'DocumentJournals')
}
$aliasToCanonical = [ordered]@{}
foreach ($canonical in $typeAliases.Keys) { foreach ($alias in $typeAliases[$canonical]) { $aliasToCanonical[$alias] = $canonical } }
$typeAlternation = ($aliasToCanonical.Keys | Sort-Object -Property Length -Descending | ForEach-Object { [regex]::Escape($_) }) -join '|'
$identPattern = '[A-Za-zА-Яа-яЁё0-9_]+'

function ConvertTo-BSLFlowNormalizedName {
    param([string]$Name)
    if ($null -eq $Name) { return '' }
    return $Name.ToLowerInvariant().Replace('ё', 'е')
}

function Get-BSLFlowLevenshteinDistance {
    param([string]$A, [string]$B)
    $a = ConvertTo-BSLFlowNormalizedName $A
    $b = ConvertTo-BSLFlowNormalizedName $B
    $lenA = $a.Length; $lenB = $b.Length
    if ($lenA -eq 0) { return $lenB }
    if ($lenB -eq 0) { return $lenA }
    $previous = 0..$lenB
    for ($i = 1; $i -le $lenA; $i++) {
        $current = [int[]]::new($lenB + 1)
        $current[0] = $i
        for ($j = 1; $j -le $lenB; $j++) {
            $cost = if ($a[$i - 1] -eq $b[$j - 1]) { 0 } else { 1 }
            $deletion = $previous[$j] + 1
            $insertion = $current[$j - 1] + 1
            $substitution = $previous[$j - 1] + $cost
            $current[$j] = [Math]::Min([Math]::Min($deletion, $insertion), $substitution)
        }
        $previous = $current
    }
    return $previous[$lenB]
}

function Find-BSLFlowClosestName {
    param([string]$Name, [string[]]$Candidates)
    $best = $null
    $bestDistance = [int]::MaxValue
    foreach ($candidate in ($Candidates | Sort-Object)) {
        if ((ConvertTo-BSLFlowNormalizedName $candidate) -eq (ConvertTo-BSLFlowNormalizedName $Name)) { continue }
        $distance = Get-BSLFlowLevenshteinDistance -A $Name -B $candidate
        if ($distance -lt $bestDistance) { $bestDistance = $distance; $best = $candidate }
    }
    if ($best -and $bestDistance -le 2) { return $best }
    return $null
}

function Get-BSLFlowLineNumber {
    param([string]$Text, [int]$Offset)
    if ($Offset -lt 0) { return 1 }
    return ([regex]::Matches($Text.Substring(0, [Math]::Min($Offset, $Text.Length)), "`n").Count + 1)
}

function Get-BSLFlowSectionSpans {
    # Returns ordered list of {Name, Start, End, Kind} for '## ...' sections.
    param([string]$Text)
    $spans = [System.Collections.Generic.List[object]]::new()
    $headingMatches = [regex]::Matches($Text, '(?m)^##\s+(?<title>[^\r\n]+?)\s*$')
    for ($i = 0; $i -lt $headingMatches.Count; $i++) {
        $start = $headingMatches[$i].Index
        $end = if ($i + 1 -lt $headingMatches.Count) { $headingMatches[$i + 1].Index } else { $Text.Length }
        $title = $headingMatches[$i].Groups['title'].Value.Trim()
        $spans.Add([pscustomobject]@{ Title = $title; Start = $start; End = $end })
    }
    return $spans
}

$normativeTitles = @('Контекст 1С', '1C context', 'Требуемое поведение', 'Required behavior', 'Критерии приёмки', 'Критерии приемки', 'Acceptance criteria')
$newObjectsTitles = @('Новые объекты метаданных', 'New metadata objects')

function Get-BSLFlowSectionKind {
    param([string]$Title)
    foreach ($candidate in $normativeTitles) { if ($Title -ieq $candidate) { return 'normative' } }
    foreach ($candidate in $newObjectsTitles) { if ($Title -ieq $candidate) { return 'new-objects' } }
    return 'non-normative'
}

$referencesChecked = 0
$declaredNewKeys = [System.Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)

foreach ($document in $documents) {
    $text = $document.Text
    $filePrefix = if ($document.File -eq 'spec.md') { '' } else { "$($document.File): " }
    $sections = Get-BSLFlowSectionSpans -Text $text

    function Get-SectionKindAt {
        param([int]$Offset)
        foreach ($section in $sections) {
            if ($Offset -ge $section.Start -and $Offset -lt $section.End) { return (Get-BSLFlowSectionKind -Title $section.Title) }
        }
        return 'non-normative'
    }

    # Pass A: common module method calls.
    $methodCallPattern = "(?<type>$(($typeAliases['ОбщийМодуль'] | ForEach-Object { [regex]::Escape($_) }) -join '|'))\.(?<mod>$identPattern)\.(?<method>$identPattern)\s*\("
    foreach ($match in [regex]::Matches($text, $methodCallPattern)) {
        $referencesChecked++
        $modName = $match.Groups['mod'].Value
        $methodName = $match.Groups['method'].Value
        $reference = "ОбщийМодуль.$modName.$methodName("
        $line = Get-BSLFlowLineNumber -Text $text -Offset $match.Index
        $isNew = ($text.Substring($match.Index + $match.Length) -match '^[^\r\n]{0,3}\((новый|new)\)')
        $objectKey = "ОбщийМодуль.$modName"
        if ($isNew) { [void]$declaredNewKeys.Add($objectKey); continue }
        $kind = Get-SectionKindAt -Offset $match.Index
        if (-not $index.modules.PSObject.Properties[$objectKey]) {
            $existingModuleNames = @($index.modules.PSObject.Properties.Name | Where-Object { $_ -match '^ОбщийМодуль\.' } | ForEach-Object { $_.Substring('ОбщийМодуль.'.Length) })
            $suggestion = Find-BSLFlowClosestName -Name $modName -Candidates $existingModuleNames
            $message = "$($filePrefix)Unknown common module: ОбщийМодуль.$modName"
            if ($kind -eq 'normative') { Add-Finding -Sink $errors -Reference $reference -Message $message -Line $line -Suggestion $suggestion }
            else { Add-Finding -Sink $warnings -Reference $reference -Message $message -Line $line -Suggestion $suggestion }
            continue
        }
        $methods = $index.modules.$objectKey.methods
        if (-not $methods.PSObject.Properties[$methodName]) {
            $suggestion = Find-BSLFlowClosestName -Name $methodName -Candidates @($methods.PSObject.Properties.Name)
            $message = "$($filePrefix)Unknown method: ОбщийМодуль.$modName.$methodName"
            if ($kind -eq 'normative') { Add-Finding -Sink $errors -Reference $reference -Message $message -Line $line -Suggestion $suggestion }
            else { Add-Finding -Sink $warnings -Reference $reference -Message $message -Line $line -Suggestion $suggestion }
            continue
        }
        if (-not $methods.$methodName.export) {
            Add-Finding -Sink $errors -Reference $reference -Message "$($filePrefix)Call to a non-export common module method: ОбщийМодуль.$modName.$methodName" -Line $line -Suggestion $null
        }
    }

    # Pass B: object / attribute / tabular-section references.
    # No trailing negative lookahead here: a lookahead after a greedy identifier group
    # makes the regex engine backtrack the LAST identifier by one character to satisfy
    # the lookahead, silently truncating names like "...Сумму(" into "...Сумм" + "у(".
    # Method calls are excluded by a plain post-match check instead.
    $refPattern = "(?<type>$typeAlternation)\.(?<seg1>$identPattern)(?:\.(?<seg2>$identPattern)(?:\.(?<seg3>$identPattern)(?:\.(?<seg4>$identPattern))?)?)?"
    foreach ($match in [regex]::Matches($text, $refPattern)) {
        $tailStart = $match.Index + $match.Length
        $tail = $text.Substring($tailStart, [Math]::Min(4, $text.Length - $tailStart))
        # A call attaches "(" directly to the identifier (Метод(); the "(новый)"/"(new)"
        # marker always has a preceding space or punctuation, so requiring NO leading
        # whitespace here keeps that marker visible to the isNew check below.
        if ($tail -match '^\(') { continue }
        $rawType = $match.Groups['type'].Value
        $canonicalType = $aliasToCanonical[$rawType]
        if (-not $canonicalType) { continue }
        $objName = $match.Groups['seg1'].Value
        $seg2 = if ($match.Groups['seg2'].Success) { $match.Groups['seg2'].Value } else { $null }
        $seg3 = if ($match.Groups['seg3'].Success) { $match.Groups['seg3'].Value } else { $null }
        $seg4 = if ($match.Groups['seg4'].Success) { $match.Groups['seg4'].Value } else { $null }
        $objectKey = "$canonicalType.$objName"
        $referencesChecked++
        $line = Get-BSLFlowLineNumber -Text $text -Offset $match.Index
        $reference = $match.Value
        $tailOffset = $match.Index + $match.Length
        $isNew = ($text.Substring($tailOffset, [Math]::Min(24, $text.Length - $tailOffset)) -match '^[^\r\n]{0,3}\((новый|new)\)')
        $kind = Get-SectionKindAt -Offset $match.Index
        if ($kind -eq 'new-objects') { $isNew = $true }
        if ($isNew) { [void]$declaredNewKeys.Add($objectKey); continue }

        $objectExists = [bool]$index.objects.PSObject.Properties[$objectKey]
        if (-not $objectExists) {
            $existingNamesOfType = @($index.objects.PSObject.Properties.Name | Where-Object { $_ -match "^$([regex]::Escape($canonicalType))\." } | ForEach-Object { $_.Substring($canonicalType.Length + 1) })
            $suggestion = Find-BSLFlowClosestName -Name $objName -Candidates $existingNamesOfType
            $message = "$($filePrefix)Unknown metadata object: $canonicalType.$objName"
            if ($kind -eq 'normative') { Add-Finding -Sink $errors -Reference $reference -Message $message -Line $line -Suggestion $suggestion }
            else { Add-Finding -Sink $warnings -Reference $reference -Message $message -Line $line -Suggestion $suggestion }
            continue
        }
        if (-not $seg2) { continue }

        $object = $index.objects.$objectKey
        if ($seg2 -in @('ТабличнаяЧасть', 'TabularSection') -and $seg3) {
            # Long form: Тип.Имя.ТабличнаяЧасть.ТЧ.Реквизит
            $tsName = $seg3
            $attrName = $seg4
            $tsExists = [bool]$object.tabular_sections.PSObject.Properties[$tsName]
            if (-not $tsExists) {
                $suggestion = Find-BSLFlowClosestName -Name $tsName -Candidates @($object.tabular_sections.PSObject.Properties.Name)
                $message = "$($filePrefix)Unknown tabular section: $objectKey.$tsName"
                if ($kind -eq 'normative') { Add-Finding -Sink $errors -Reference $reference -Message $message -Line $line -Suggestion $suggestion }
                else { Add-Finding -Sink $warnings -Reference $reference -Message $message -Line $line -Suggestion $suggestion }
                continue
            }
            if ($attrName) {
                $tsAttrs = @($object.tabular_sections.$tsName)
                if ($attrName -notin $tsAttrs) {
                    $suggestion = Find-BSLFlowClosestName -Name $attrName -Candidates $tsAttrs
                    $message = "$($filePrefix)Unknown tabular section attribute: $objectKey.$tsName.$attrName"
                    if ($kind -eq 'normative') { Add-Finding -Sink $errors -Reference $reference -Message $message -Line $line -Suggestion $suggestion }
                    else { Add-Finding -Sink $warnings -Reference $reference -Message $message -Line $line -Suggestion $suggestion }
                }
            }
            continue
        }

        if ($seg3 -and $object.tabular_sections.PSObject.Properties[$seg2]) {
            # Short form: Тип.Имя.ТЧ.Реквизит
            $tsAttrs = @($object.tabular_sections.($seg2))
            if ($seg3 -notin $tsAttrs) {
                $suggestion = Find-BSLFlowClosestName -Name $seg3 -Candidates $tsAttrs
                $message = "$($filePrefix)Unknown tabular section attribute: $objectKey.$seg2.$seg3"
                if ($kind -eq 'normative') { Add-Finding -Sink $errors -Reference $reference -Message $message -Line $line -Suggestion $suggestion }
                else { Add-Finding -Sink $warnings -Reference $reference -Message $message -Line $line -Suggestion $suggestion }
            }
            continue
        }

        # Plain attribute/dimension/resource/enum-value reference: Тип.Имя.Реквизит
        $known = @($object.attributes) + @($object.dimensions) + @($object.resources) + @($object.enum_values)
        if ($seg2 -notin $known) {
            $suggestion = Find-BSLFlowClosestName -Name $seg2 -Candidates $known
            $message = "$($filePrefix)Unknown attribute: $objectKey.$seg2"
            if ($kind -eq 'normative') { Add-Finding -Sink $errors -Reference $reference -Message $message -Line $line -Suggestion $suggestion }
            else { Add-Finding -Sink $warnings -Reference $reference -Message $message -Line $line -Suggestion $suggestion }
        }
    }
}

foreach ($declaredKey in $declaredNewKeys) {
    if ($index.objects.PSObject.Properties[$declaredKey]) {
        Add-Finding -Sink $errors -Reference $declaredKey -Message "Collision: '$declaredKey' is declared as a new metadata object but already exists." -Line 0 -Suggestion $null
    }
}

$result = [ordered]@{
    schema_version     = 1
    status             = 'checked'
    passed             = ($errors.Count -eq 0)
    errors             = @($errors)
    warnings           = @($warnings)
    references_checked = $referencesChecked
    index              = [ordered]@{ format = $index.format; objects = @($index.objects.PSObject.Properties).Count }
}
if (-not $OutputPath) { $OutputPath = Join-Path $changeRoot 'spec-grounding.json' }
Write-BSLFlowJsonAtomic -Value $result -Path $OutputPath
if (-not $result.passed -and -not $NoThrow) { throw "Specification grounding lint failed. See: $OutputPath" }
return [pscustomobject]$result
