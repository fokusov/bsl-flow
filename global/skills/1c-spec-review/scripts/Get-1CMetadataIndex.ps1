#Requires -Version 7.0
<#
.SYNOPSIS
    Builds a metadata index (objects, attributes, tabular sections, dimensions,
    resources, enum values, forms, module methods) over a 1C Designer XML
    export or an EDT project source tree, for use by grounding lint scripts.

.DESCRIPTION
    Real Designer export layout (verified against production exports):
      Configuration.xml            <MetaDataObject><Configuration><ChildObjects>
                                    <Catalog>Name</Catalog>, <CommonModule>Name</CommonModule>, ...
      <Folder>/<Name>.xml          <MetaDataObject><Catalog><Properties><Name>...
                                    <ChildObjects><Attribute><Properties><Name>...
                                    <TabularSection><Properties><Name>...<ChildObjects><Attribute>...
                                    <Dimension>/<Resource>/<EnumValue> follow the same shape.
      <Folder>/<Name>/Ext/Module.bsl              (CommonModules only)
      <Folder>/<Name>/Ext/ObjectModule.bsl
      <Folder>/<Name>/Ext/ManagerModule.bsl
      <Folder>/<Name>/Forms/<Form>/Ext/Form/Module.bsl
      Extension objects mark <Properties><ObjectBelonging>Adopted</ObjectBelonging> when borrowed.

    EDT project layout (best-effort; EDT drops the Ext wrapper Designer uses):
      src/Configuration/Configuration.mdo         <childObjects>Catalog.Name</childObjects>
      src/<Folder>/<Name>/<Name>.mdo               <name>, <attributes><name>, <tabularSections><name><attributes><name>,
                                                    <dimensions><name>, <resources><name>, <enumValues><name>
      src/CommonModules/<Name>/Module.bsl
      src/<Folder>/<Name>/ObjectModule.bsl / ManagerModule.bsl
      src/<Folder>/<Name>/Forms/<Form>/Module.bsl

.PARAMETER SourceRoot
    One or more source roots (configuration plus extensions). Multiple roots are merged;
    an object seen as "adopted" in one root and "own" in another is merged into one entry
    (union of attributes/tabular sections/dimensions/resources/enum values), belonging
    reported as 'adopted' when any root marks it so.

.PARAMETER CachePath
    Optional cache file. Keyed by a hash of the (relative path, length, mtime) manifest
    of every file under all source roots, so unrelated edits never invalidate the cache.
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory)][string[]]$SourceRoot,
    [string]$CachePath
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$Culture = [Globalization.CultureInfo]::InvariantCulture

# English XML tag -> canonical Russian type name, and the on-disk plural folder name.
$script:TypeMap = [ordered]@{
    Catalog                    = @{ Ru = 'Справочник';              Folder = 'Catalogs' }
    Document                   = @{ Ru = 'Документ';                Folder = 'Documents' }
    AccumulationRegister        = @{ Ru = 'РегистрНакопления';        Folder = 'AccumulationRegisters' }
    InformationRegister         = @{ Ru = 'РегистрСведений';          Folder = 'InformationRegisters' }
    AccountingRegister          = @{ Ru = 'РегистрБухгалтерии';       Folder = 'AccountingRegisters' }
    CalculationRegister         = @{ Ru = 'РегистрРасчета';           Folder = 'CalculationRegisters' }
    CommonModule                = @{ Ru = 'ОбщийМодуль';              Folder = 'CommonModules' }
    Enum                        = @{ Ru = 'Перечисление';             Folder = 'Enums' }
    ChartOfCharacteristicTypes  = @{ Ru = 'ПланВидовХарактеристик';   Folder = 'ChartsOfCharacteristicTypes' }
    ChartOfAccounts             = @{ Ru = 'ПланСчетов';               Folder = 'ChartsOfAccounts' }
    DataProcessor               = @{ Ru = 'Обработка';                Folder = 'DataProcessors' }
    Report                      = @{ Ru = 'Отчет';                    Folder = 'Reports' }
    Constant                    = @{ Ru = 'Константа';                Folder = 'Constants' }
    ScheduledJob                = @{ Ru = 'РегламентноеЗадание';      Folder = 'ScheduledJobs' }
    HTTPService                 = @{ Ru = 'HTTPСервис';               Folder = 'HTTPServices' }
    CommonForm                  = @{ Ru = 'ОбщаяФорма';               Folder = 'CommonForms' }
    Role                        = @{ Ru = 'Роль';                     Folder = 'Roles' }
    Subsystem                   = @{ Ru = 'Подсистема';               Folder = 'Subsystems' }
    ExchangePlan                 = @{ Ru = 'ПланОбмена';              Folder = 'ExchangePlans' }
    BusinessProcess              = @{ Ru = 'БизнесПроцесс';           Folder = 'BusinessProcesses' }
    Task                         = @{ Ru = 'Задача';                  Folder = 'Tasks' }
    DocumentJournal              = @{ Ru = 'ЖурналДокументов';        Folder = 'DocumentJournals' }
}

function Get-BSLFlowSha256Text {
    param([Parameter(Mandatory)][string]$Text)
    $sha = [System.Security.Cryptography.SHA256]::Create()
    try { return ([BitConverter]::ToString($sha.ComputeHash([Text.Encoding]::UTF8.GetBytes($Text)))).Replace('-', '').ToLowerInvariant() }
    finally { $sha.Dispose() }
}

function New-BSLFlowXmlReader {
    param([Parameter(Mandatory)][string]$Path)
    $settings = [System.Xml.XmlReaderSettings]::new()
    $settings.IgnoreComments = $true
    $settings.IgnoreWhitespace = $true
    $settings.IgnoreProcessingInstructions = $true
    $settings.DtdProcessing = [System.Xml.DtdProcessing]::Prohibit
    $settings.XmlResolver = $null
    return [System.Xml.XmlReader]::Create($Path, $settings)
}

function Read-DesignerMethods {
    # Parses "(Процедура|Функция|Procedure|Function) Имя(...) [Экспорт|Export]" declarations,
    # ignoring lines that are commented out (the leading '//' preempts the keyword match).
    param([Parameter(Mandatory)][string]$Path)
    $methods = [ordered]@{}
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { return $methods }
    $text = [IO.File]::ReadAllText($Path, [Text.UTF8Encoding]::new($false))
    $pattern = '(?im)^[ \t]*(?<kind>Процедура|Функция|Procedure|Function)[ \t]+(?<name>[A-Za-zА-Яа-яЁё0-9_]+)[ \t]*\([^\)]*\)[ \t]*(?<export>Экспорт|Export)?'
    foreach ($match in [regex]::Matches($text, $pattern)) {
        $kind = if ($match.Groups['kind'].Value -in @('Процедура', 'Procedure')) { 'Процедура' } else { 'Функция' }
        $name = $match.Groups['name'].Value
        $export = $match.Groups['export'].Success
        $methods[$name] = [ordered]@{ export = $export; kind = $kind }
    }
    return $methods
}

function Merge-BSLFlowUniqueList {
    param([string[]]$Existing, [string[]]$New)
    $set = [System.Collections.Generic.List[string]]::new()
    if ($Existing) { foreach ($item in $Existing) { if ($item -notin $set) { $set.Add($item) } } }
    if ($New) { foreach ($item in $New) { if ($item -notin $set) { $set.Add($item) } } }
    return @($set)
}

# ---------------------------------------------------------------------------
# Designer format
# ---------------------------------------------------------------------------

function Get-DesignerChildObjects {
    param([Parameter(Mandatory)][string]$ConfigurationXmlPath)
    $result = [System.Collections.Generic.List[object]]::new()
    $reader = New-BSLFlowXmlReader -Path $ConfigurationXmlPath
    try {
        $ancestors = [System.Collections.Generic.List[string]]::new()
        # ReadElementContentAsString() already advances the reader past the element it
        # consumes, so when it is used the loop must NOT call Read() again before
        # processing the node the reader now sits on - otherwise that node is skipped
        # and the ancestor stack desyncs for the rest of the document.
        $hasNode = $reader.Read()
        while ($hasNode) {
            $consumed = $false
            if ($reader.NodeType -eq [System.Xml.XmlNodeType]::Element) {
                $name = $reader.LocalName
                $isEmpty = $reader.IsEmptyElement
                if ($ancestors.Count -eq 3 -and $ancestors[2] -eq 'ChildObjects' -and $script:TypeMap.Contains($name)) {
                    $text = $reader.ReadElementContentAsString()
                    if ($text) { $result.Add([pscustomobject]@{ Tag = $name; Name = $text }) }
                    $consumed = $true
                }
                elseif (-not $isEmpty) { $ancestors.Add($name) }
            }
            elseif ($reader.NodeType -eq [System.Xml.XmlNodeType]::EndElement -and $ancestors.Count -gt 0) {
                $ancestors.RemoveAt($ancestors.Count - 1)
            }
            $hasNode = if ($consumed) { -not $reader.EOF } else { $reader.Read() }
        }
    }
    finally { $reader.Dispose() }
    return $result
}

function Get-DesignerObjectDetail {
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][string]$ExpectedName
    )
    $detail = [ordered]@{
        name             = $ExpectedName
        belonging        = 'own'
        attributes       = [System.Collections.Generic.List[string]]::new()
        tabular_sections = [ordered]@{}
        dimensions       = [System.Collections.Generic.List[string]]::new()
        resources        = [System.Collections.Generic.List[string]]::new()
        enum_values      = [System.Collections.Generic.List[string]]::new()
    }
    $currentTabularSection = $null
    $reader = New-BSLFlowXmlReader -Path $Path
    try {
        $ancestors = [System.Collections.Generic.List[string]]::new()
        # See the comment in Get-DesignerChildObjects: ReadElementContentAsString()
        # already advances the reader, so we must not call Read() again on the same
        # iteration, or the node it lands on gets skipped and the ancestor stack desyncs.
        $hasNode = $reader.Read()
        while ($hasNode) {
            $consumed = $false
            if ($reader.NodeType -eq [System.Xml.XmlNodeType]::Element) {
                $name = $reader.LocalName
                $isEmpty = $reader.IsEmptyElement
                $count = $ancestors.Count

                if ($count -eq 3 -and $ancestors[2] -eq 'Properties' -and $name -eq 'Name') {
                    $ownName = $reader.ReadElementContentAsString()
                    if ($ownName) { $detail.name = $ownName }
                    $consumed = $true
                }
                elseif ($count -eq 3 -and $ancestors[2] -eq 'Properties' -and $name -eq 'ObjectBelonging') {
                    $belonging = $reader.ReadElementContentAsString()
                    if ($belonging -eq 'Adopted') { $detail.belonging = 'adopted' }
                    $consumed = $true
                }
                elseif ($count -eq 5 -and $ancestors[4] -eq 'Properties' -and $name -eq 'Name') {
                    $kind = $ancestors[3]
                    $childName = $reader.ReadElementContentAsString()
                    switch ($kind) {
                        'Attribute' { $detail.attributes.Add($childName) }
                        'Dimension' { $detail.dimensions.Add($childName) }
                        'Resource' { $detail.resources.Add($childName) }
                        'EnumValue' { $detail.enum_values.Add($childName) }
                        'TabularSection' {
                            $currentTabularSection = $childName
                            if (-not $detail.tabular_sections.Contains($childName)) {
                                $detail.tabular_sections[$childName] = [System.Collections.Generic.List[string]]::new()
                            }
                        }
                    }
                    $consumed = $true
                }
                elseif ($count -eq 7 -and $ancestors[3] -eq 'TabularSection' -and $ancestors[4] -eq 'ChildObjects' -and $ancestors[5] -eq 'Attribute' -and $ancestors[6] -eq 'Properties' -and $name -eq 'Name') {
                    $childName = $reader.ReadElementContentAsString()
                    if ($currentTabularSection -and $detail.tabular_sections.Contains($currentTabularSection)) {
                        $detail.tabular_sections[$currentTabularSection].Add($childName)
                    }
                    $consumed = $true
                }
                elseif (-not $isEmpty) { $ancestors.Add($name) }
            }
            elseif ($reader.NodeType -eq [System.Xml.XmlNodeType]::EndElement -and $ancestors.Count -gt 0) {
                $ancestors.RemoveAt($ancestors.Count - 1)
            }
            $hasNode = if ($consumed) { -not $reader.EOF } else { $reader.Read() }
        }
    }
    finally { $reader.Dispose() }
    $detail.attributes = @($detail.attributes)
    $detail.dimensions = @($detail.dimensions)
    $detail.resources = @($detail.resources)
    $detail.enum_values = @($detail.enum_values)
    $flatTabular = [ordered]@{}
    foreach ($key in $detail.tabular_sections.Keys) { $flatTabular[$key] = @($detail.tabular_sections[$key]) }
    $detail.tabular_sections = $flatTabular
    return $detail
}

function Get-DesignerForms {
    param([Parameter(Mandatory)][string]$FormsDir)
    if (-not (Test-Path -LiteralPath $FormsDir -PathType Container)) { return @() }
    return @(Get-ChildItem -LiteralPath $FormsDir -Filter '*.xml' -File | ForEach-Object { $_.BaseName } | Sort-Object)
}

function Add-DesignerRoot {
    param(
        [Parameter(Mandatory)][string]$Root,
        [Parameter(Mandatory)][System.Collections.Specialized.OrderedDictionary]$Objects,
        [Parameter(Mandatory)][System.Collections.Specialized.OrderedDictionary]$Modules
    )
    $configPath = Join-Path $Root 'Configuration.xml'
    $children = Get-DesignerChildObjects -ConfigurationXmlPath $configPath

    foreach ($child in $children) {
        if (-not $script:TypeMap.Contains($child.Tag)) { continue }
        $typeInfo = $script:TypeMap[$child.Tag]
        $ruType = $typeInfo.Ru
        $folder = $typeInfo.Folder
        $key = "$ruType.$($child.Name)"
        $objectXmlPath = Join-Path $Root (Join-Path $folder ($child.Name + '.xml'))
        $detail = $null
        if (Test-Path -LiteralPath $objectXmlPath -PathType Leaf) {
            $detail = Get-DesignerObjectDetail -Path $objectXmlPath -ExpectedName $child.Name
        }
        else {
            $detail = [ordered]@{ name = $child.Name; belonging = 'own'; attributes = @(); tabular_sections = [ordered]@{}; dimensions = @(); resources = @(); enum_values = @() }
        }

        $formsDir = Join-Path $Root (Join-Path $folder (Join-Path $child.Name 'Forms'))
        $forms = Get-DesignerForms -FormsDir $formsDir

        if ($Objects.Contains($key)) {
            $existing = $Objects[$key]
            $existing.attributes = Merge-BSLFlowUniqueList -Existing $existing.attributes -New $detail.attributes
            $existing.dimensions = Merge-BSLFlowUniqueList -Existing $existing.dimensions -New $detail.dimensions
            $existing.resources = Merge-BSLFlowUniqueList -Existing $existing.resources -New $detail.resources
            $existing.enum_values = Merge-BSLFlowUniqueList -Existing $existing.enum_values -New $detail.enum_values
            foreach ($tsName in $detail.tabular_sections.Keys) {
                $tsAttrs = $detail.tabular_sections[$tsName]
                if ($existing.tabular_sections.Contains($tsName)) {
                    $existing.tabular_sections[$tsName] = Merge-BSLFlowUniqueList -Existing $existing.tabular_sections[$tsName] -New $tsAttrs
                }
                else { $existing.tabular_sections[$tsName] = $tsAttrs }
            }
            $existing.forms = Merge-BSLFlowUniqueList -Existing $existing.forms -New $forms
            if ($detail.belonging -eq 'adopted') { $existing.belonging = 'adopted' }
        }
        else {
            $Objects[$key] = [ordered]@{
                type             = $ruType
                name             = $detail.name
                belonging        = $detail.belonging
                attributes       = $detail.attributes
                tabular_sections = $detail.tabular_sections
                dimensions       = $detail.dimensions
                resources        = $detail.resources
                enum_values      = $detail.enum_values
                forms            = $forms
            }
        }

        # Modules for this object.
        if ($child.Tag -eq 'CommonModule') {
            $modulePath = Join-Path $Root (Join-Path $folder (Join-Path $child.Name 'Ext\Module.bsl'))
            if (Test-Path -LiteralPath $modulePath -PathType Leaf) {
                $Modules["$ruType.$($child.Name)"] = [ordered]@{ methods = Read-DesignerMethods -Path $modulePath }
            }
        }
        else {
            $objectModulePath = Join-Path $Root (Join-Path $folder (Join-Path $child.Name 'Ext\ObjectModule.bsl'))
            if (Test-Path -LiteralPath $objectModulePath -PathType Leaf) {
                $Modules["$key.МодульОбъекта"] = [ordered]@{ methods = Read-DesignerMethods -Path $objectModulePath }
            }
            $managerModulePath = Join-Path $Root (Join-Path $folder (Join-Path $child.Name 'Ext\ManagerModule.bsl'))
            if (Test-Path -LiteralPath $managerModulePath -PathType Leaf) {
                $Modules["$key.МодульМенеджера"] = [ordered]@{ methods = Read-DesignerMethods -Path $managerModulePath }
            }
            foreach ($formName in $forms) {
                $formModulePath = Join-Path $Root (Join-Path $folder (Join-Path $child.Name (Join-Path 'Forms' (Join-Path $formName 'Ext\Form\Module.bsl'))))
                if (Test-Path -LiteralPath $formModulePath -PathType Leaf) {
                    $Modules["$key.Форма.$formName"] = [ordered]@{ methods = Read-DesignerMethods -Path $formModulePath }
                }
            }
        }
    }
}

# ---------------------------------------------------------------------------
# EDT format
# ---------------------------------------------------------------------------

function Get-EdtChildObjects {
    param([Parameter(Mandatory)][string]$ConfigurationMdoPath)
    $result = [System.Collections.Generic.List[object]]::new()
    $reader = New-BSLFlowXmlReader -Path $ConfigurationMdoPath
    try {
        $ancestors = [System.Collections.Generic.List[string]]::new()
        # See Get-DesignerChildObjects: ReadElementContentAsString() already advances
        # past the element, so the loop must process the node it lands on without an
        # extra Read() first, or the ancestor stack desyncs for the rest of the file.
        $hasNode = $reader.Read()
        while ($hasNode) {
            $consumed = $false
            if ($reader.NodeType -eq [System.Xml.XmlNodeType]::Element) {
                $name = $reader.LocalName
                $isEmpty = $reader.IsEmptyElement
                if ($ancestors.Count -eq 1 -and $name -eq 'childObjects') {
                    $text = $reader.ReadElementContentAsString()
                    if ($text -match '^(?<tag>[A-Za-z]+)\.(?<name>.+)$') {
                        $result.Add([pscustomobject]@{ Tag = $Matches.tag; Name = $Matches.name })
                    }
                    $consumed = $true
                }
                elseif (-not $isEmpty) { $ancestors.Add($name) }
            }
            elseif ($reader.NodeType -eq [System.Xml.XmlNodeType]::EndElement -and $ancestors.Count -gt 0) {
                $ancestors.RemoveAt($ancestors.Count - 1)
            }
            $hasNode = if ($consumed) { -not $reader.EOF } else { $reader.Read() }
        }
    }
    finally { $reader.Dispose() }
    return $result
}

function Get-EdtObjectDetail {
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][string]$ExpectedName
    )
    $detail = [ordered]@{
        name             = $ExpectedName
        belonging        = 'own'
        attributes       = [System.Collections.Generic.List[string]]::new()
        tabular_sections = [ordered]@{}
        dimensions       = [System.Collections.Generic.List[string]]::new()
        resources        = [System.Collections.Generic.List[string]]::new()
        enum_values      = [System.Collections.Generic.List[string]]::new()
    }
    $currentTabularSection = $null
    $reader = New-BSLFlowXmlReader -Path $Path
    try {
        $ancestors = [System.Collections.Generic.List[string]]::new()
        # See Get-DesignerChildObjects for why this cannot be a plain `while ($reader.Read())`.
        $hasNode = $reader.Read()
        while ($hasNode) {
            $consumed = $false
            if ($reader.NodeType -eq [System.Xml.XmlNodeType]::Element) {
                $name = $reader.LocalName
                $isEmpty = $reader.IsEmptyElement
                $count = $ancestors.Count

                if ($count -eq 1 -and $name -eq 'name') {
                    $ownName = $reader.ReadElementContentAsString()
                    if ($ownName) { $detail.name = $ownName }
                    $consumed = $true
                }
                elseif ($count -eq 1 -and $name -eq 'objectBelonging') {
                    $belonging = $reader.ReadElementContentAsString()
                    if ($belonging -eq 'Adopted') { $detail.belonging = 'adopted' }
                    $consumed = $true
                }
                elseif ($count -eq 2 -and $name -eq 'name') {
                    $kind = $ancestors[1]
                    $childName = $reader.ReadElementContentAsString()
                    switch ($kind) {
                        'attributes' { $detail.attributes.Add($childName) }
                        'dimensions' { $detail.dimensions.Add($childName) }
                        'resources' { $detail.resources.Add($childName) }
                        'enumValues' { $detail.enum_values.Add($childName) }
                        'tabularSections' {
                            $currentTabularSection = $childName
                            if (-not $detail.tabular_sections.Contains($childName)) {
                                $detail.tabular_sections[$childName] = [System.Collections.Generic.List[string]]::new()
                            }
                        }
                    }
                    $consumed = $true
                }
                elseif ($count -eq 3 -and $name -eq 'name' -and $ancestors[1] -eq 'tabularSections' -and $ancestors[2] -eq 'attributes') {
                    $childName = $reader.ReadElementContentAsString()
                    if ($currentTabularSection -and $detail.tabular_sections.Contains($currentTabularSection)) {
                        $detail.tabular_sections[$currentTabularSection].Add($childName)
                    }
                    $consumed = $true
                }
                elseif (-not $isEmpty) { $ancestors.Add($name) }
            }
            elseif ($reader.NodeType -eq [System.Xml.XmlNodeType]::EndElement -and $ancestors.Count -gt 0) {
                $ancestors.RemoveAt($ancestors.Count - 1)
            }
            $hasNode = if ($consumed) { -not $reader.EOF } else { $reader.Read() }
        }
    }
    finally { $reader.Dispose() }
    $detail.attributes = @($detail.attributes)
    $detail.dimensions = @($detail.dimensions)
    $detail.resources = @($detail.resources)
    $detail.enum_values = @($detail.enum_values)
    $flatTabular = [ordered]@{}
    foreach ($key in $detail.tabular_sections.Keys) { $flatTabular[$key] = @($detail.tabular_sections[$key]) }
    $detail.tabular_sections = $flatTabular
    return $detail
}

function Get-EdtForms {
    param([Parameter(Mandatory)][string]$FormsDir)
    if (-not (Test-Path -LiteralPath $FormsDir -PathType Container)) { return @() }
    return @(Get-ChildItem -LiteralPath $FormsDir -Directory | ForEach-Object { $_.Name } | Sort-Object)
}

function Add-EdtRoot {
    param(
        [Parameter(Mandatory)][string]$Root,
        [Parameter(Mandatory)][System.Collections.Specialized.OrderedDictionary]$Objects,
        [Parameter(Mandatory)][System.Collections.Specialized.OrderedDictionary]$Modules
    )
    $configPath = Join-Path $Root 'src\Configuration\Configuration.mdo'
    $children = Get-EdtChildObjects -ConfigurationMdoPath $configPath

    foreach ($child in $children) {
        if (-not $script:TypeMap.Contains($child.Tag)) { continue }
        $typeInfo = $script:TypeMap[$child.Tag]
        $ruType = $typeInfo.Ru
        $folder = $typeInfo.Folder
        $key = "$ruType.$($child.Name)"
        $objectMdoPath = Join-Path $Root (Join-Path 'src' (Join-Path $folder (Join-Path $child.Name ($child.Name + '.mdo'))))
        $detail = $null
        if (Test-Path -LiteralPath $objectMdoPath -PathType Leaf) {
            $detail = Get-EdtObjectDetail -Path $objectMdoPath -ExpectedName $child.Name
        }
        else {
            $detail = [ordered]@{ name = $child.Name; belonging = 'own'; attributes = @(); tabular_sections = [ordered]@{}; dimensions = @(); resources = @(); enum_values = @() }
        }

        $formsDir = Join-Path $Root (Join-Path 'src' (Join-Path $folder (Join-Path $child.Name 'Forms')))
        $forms = Get-EdtForms -FormsDir $formsDir

        if ($Objects.Contains($key)) {
            $existing = $Objects[$key]
            $existing.attributes = Merge-BSLFlowUniqueList -Existing $existing.attributes -New $detail.attributes
            $existing.dimensions = Merge-BSLFlowUniqueList -Existing $existing.dimensions -New $detail.dimensions
            $existing.resources = Merge-BSLFlowUniqueList -Existing $existing.resources -New $detail.resources
            $existing.enum_values = Merge-BSLFlowUniqueList -Existing $existing.enum_values -New $detail.enum_values
            foreach ($tsName in $detail.tabular_sections.Keys) {
                $tsAttrs = $detail.tabular_sections[$tsName]
                if ($existing.tabular_sections.Contains($tsName)) {
                    $existing.tabular_sections[$tsName] = Merge-BSLFlowUniqueList -Existing $existing.tabular_sections[$tsName] -New $tsAttrs
                }
                else { $existing.tabular_sections[$tsName] = $tsAttrs }
            }
            $existing.forms = Merge-BSLFlowUniqueList -Existing $existing.forms -New $forms
            if ($detail.belonging -eq 'adopted') { $existing.belonging = 'adopted' }
        }
        else {
            $Objects[$key] = [ordered]@{
                type             = $ruType
                name             = $detail.name
                belonging        = $detail.belonging
                attributes       = $detail.attributes
                tabular_sections = $detail.tabular_sections
                dimensions       = $detail.dimensions
                resources        = $detail.resources
                enum_values      = $detail.enum_values
                forms            = $forms
            }
        }

        if ($child.Tag -eq 'CommonModule') {
            $modulePath = Join-Path $Root (Join-Path 'src' (Join-Path $folder (Join-Path $child.Name 'Module.bsl')))
            if (Test-Path -LiteralPath $modulePath -PathType Leaf) {
                $Modules["$ruType.$($child.Name)"] = [ordered]@{ methods = Read-DesignerMethods -Path $modulePath }
            }
        }
        else {
            $objectModulePath = Join-Path $Root (Join-Path 'src' (Join-Path $folder (Join-Path $child.Name 'ObjectModule.bsl')))
            if (Test-Path -LiteralPath $objectModulePath -PathType Leaf) {
                $Modules["$key.МодульОбъекта"] = [ordered]@{ methods = Read-DesignerMethods -Path $objectModulePath }
            }
            $managerModulePath = Join-Path $Root (Join-Path 'src' (Join-Path $folder (Join-Path $child.Name 'ManagerModule.bsl')))
            if (Test-Path -LiteralPath $managerModulePath -PathType Leaf) {
                $Modules["$key.МодульМенеджера"] = [ordered]@{ methods = Read-DesignerMethods -Path $managerModulePath }
            }
            foreach ($formName in $forms) {
                $formModulePath = Join-Path $Root (Join-Path 'src' (Join-Path $folder (Join-Path $child.Name (Join-Path 'Forms' (Join-Path $formName 'Module.bsl')))))
                if (Test-Path -LiteralPath $formModulePath -PathType Leaf) {
                    $Modules["$key.Форма.$formName"] = [ordered]@{ methods = Read-DesignerMethods -Path $formModulePath }
                }
            }
        }
    }
}

# ---------------------------------------------------------------------------
# Format detection + cache
# ---------------------------------------------------------------------------

function Get-BSLFlowSourceRootFormat {
    param([Parameter(Mandatory)][string]$Root)
    if (Test-Path -LiteralPath (Join-Path $Root 'Configuration.xml') -PathType Leaf) { return 'designer' }
    if (Test-Path -LiteralPath (Join-Path $Root 'src\Configuration\Configuration.mdo') -PathType Leaf) { return 'edt' }
    return 'unknown'
}

function Get-BSLFlowCacheManifestHash {
    param([Parameter(Mandatory)][string[]]$Roots)
    $entries = [System.Collections.Generic.List[string]]::new()
    foreach ($root in $Roots) {
        $fullRoot = [System.IO.Path]::GetFullPath($root)
        if (-not (Test-Path -LiteralPath $fullRoot -PathType Container)) { $entries.Add("MISSING:$fullRoot"); continue }
        $files = Get-ChildItem -LiteralPath $fullRoot -Recurse -File -ErrorAction SilentlyContinue
        foreach ($file in ($files | Sort-Object FullName)) {
            $relative = $file.FullName.Substring($fullRoot.Length).Replace('\', '/')
            $entries.Add("$relative|$($file.Length)|$($file.LastWriteTimeUtc.Ticks)")
        }
    }
    return Get-BSLFlowSha256Text -Text ($entries -join "`n")
}

function Build-1CMetadataIndex {
    param([Parameter(Mandatory)][string[]]$Roots)
    $objects = [ordered]@{}
    $modules = [ordered]@{}
    $formats = [System.Collections.Generic.List[string]]::new()

    foreach ($root in $Roots) {
        $fullRoot = [System.IO.Path]::GetFullPath($root)
        if (-not (Test-Path -LiteralPath $fullRoot -PathType Container)) { continue }
        $format = Get-BSLFlowSourceRootFormat -Root $fullRoot
        if ($format -eq 'designer') { Add-DesignerRoot -Root $fullRoot -Objects $objects -Modules $modules; $formats.Add('designer') }
        elseif ($format -eq 'edt') { Add-EdtRoot -Root $fullRoot -Objects $objects -Modules $modules; $formats.Add('edt') }
    }

    $overallFormat = 'unknown'
    $distinctFormats = @($formats | Select-Object -Unique)
    if ($distinctFormats.Count -eq 1) { $overallFormat = $distinctFormats[0] }
    elseif ($distinctFormats.Count -gt 1) { $overallFormat = 'mixed' }

    return [ordered]@{
        schema_version = 1
        format         = $overallFormat
        objects        = $objects
        modules        = $modules
    }
}

function ConvertTo-BSLFlowNormalizedIndex {
    # Round-trips through JSON so every nested hashtable becomes a PSCustomObject,
    # regardless of whether the caller hit the cache (already JSON-shaped) or built
    # a fresh index (still nested [ordered] hashtables). Callers rely on PSObject.Properties
    # lookups, so both code paths must return the exact same shape.
    param([Parameter(Mandatory)]$Index)
    return ($Index | ConvertTo-Json -Depth 30 | ConvertFrom-Json)
}

$resolvedRoots = @($SourceRoot | ForEach-Object { $_ })

if ($CachePath) {
    $manifestHash = Get-BSLFlowCacheManifestHash -Roots $resolvedRoots
    if (Test-Path -LiteralPath $CachePath -PathType Leaf) {
        try {
            $cached = Get-Content -Raw -LiteralPath $CachePath | ConvertFrom-Json -ErrorAction Stop
            if ($cached.manifest_hash -eq $manifestHash -and $cached.index) {
                return $cached.index
            }
        }
        catch { }
    }
    $index = Build-1CMetadataIndex -Roots $resolvedRoots
    $cacheDir = Split-Path -Parent $CachePath
    if ($cacheDir -and -not (Test-Path -LiteralPath $cacheDir -PathType Container)) { New-Item -ItemType Directory -Path $cacheDir -Force | Out-Null }
    $tempPath = Join-Path (Split-Path -Parent $CachePath) ('.' + [IO.Path]::GetFileName($CachePath) + '.' + [guid]::NewGuid().ToString('N') + '.tmp')
    [ordered]@{ manifest_hash = $manifestHash; index = $index } | ConvertTo-Json -Depth 30 | Set-Content -LiteralPath $tempPath -Encoding utf8
    Move-Item -LiteralPath $tempPath -Destination $CachePath -Force
    return ConvertTo-BSLFlowNormalizedIndex -Index $index
}

return ConvertTo-BSLFlowNormalizedIndex -Index (Build-1CMetadataIndex -Roots $resolvedRoots)
