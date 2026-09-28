#Requires -Version 7.0
[CmdletBinding()]
param([string]$PackageRoot)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($PackageRoot)) { $PackageRoot = Split-Path -Parent $PSScriptRoot }

$indexScript = Join-Path $PackageRoot 'global\skills\1c-spec-review\scripts\Get-1CMetadataIndex.ps1'
if (-not (Test-Path -LiteralPath $indexScript -PathType Leaf)) { throw "Missing script: $indexScript" }
$fixtures = Join-Path $PackageRoot 'scripts\fixtures\metadata'
$designerMini = Join-Path $fixtures 'designer-mini'
$designerExt = Join-Path $fixtures 'designer-ext'
$edtMini = Join-Path $fixtures 'edt-mini'

$script:checks = 0
function Assert-Idx([bool]$Condition, [string]$Message) { if (-not $Condition) { throw "ASSERTION FAILED: $Message" }; $script:checks++ }

$probeRoot = Join-Path ([System.IO.Path]::GetTempPath()) ('bsl-flow-metadata-index-' + [guid]::NewGuid().ToString('N'))
try {
    New-Item -ItemType Directory -Path $probeRoot | Out-Null

    # --- Designer format: objects, attributes, tabular sections, dimensions, resources, enum values, module methods.
    $index = & $indexScript -SourceRoot $designerMini
    Assert-Idx ($index.format -eq 'designer') 'Designer format was not detected.'
    Assert-Idx ([bool]$index.objects.PSObject.Properties['Справочник.Номенклатура']) 'Справочник.Номенклатура was not indexed.'
    $catalog = $index.objects.'Справочник.Номенклатура'
    Assert-Idx ('Артикул' -in @($catalog.attributes)) 'Catalog attribute Артикул missing.'
    Assert-Idx ('ЕдиницаИзмерения' -in @($catalog.attributes)) 'Catalog attribute ЕдиницаИзмерения missing.'
    Assert-Idx ([bool]$catalog.tabular_sections.PSObject.Properties['Штрихкоды']) 'Tabular section Штрихкоды missing.'
    Assert-Idx ('Штрихкод' -in @($catalog.tabular_sections.Штрихкоды)) 'Tabular section attribute Штрихкод missing.'
    Assert-Idx ('ФормаЭлемента' -in @($catalog.forms)) 'Catalog form ФормаЭлемента missing.'
    Assert-Idx ($catalog.belonging -eq 'own') 'Catalog belonging should be own in the base configuration.'

    $doc = $index.objects.'Документ.РеализацияТоваровУслуг'
    Assert-Idx ('Контрагент' -in @($doc.attributes)) 'Document attribute Контрагент missing.'
    Assert-Idx ([bool]$doc.tabular_sections.PSObject.Properties['Товары']) 'Document tabular section Товары missing.'
    Assert-Idx ('Количество' -in @($doc.tabular_sections.Товары)) 'Document tabular section attribute Количество missing.'

    $register = $index.objects.'РегистрНакопления.ОстаткиТоваров'
    Assert-Idx ('Номенклатура' -in @($register.dimensions)) 'Register dimension Номенклатура missing.'
    Assert-Idx ('Склад' -in @($register.dimensions)) 'Register dimension Склад missing.'
    Assert-Idx ('Количество' -in @($register.resources)) 'Register resource Количество missing.'

    $enum = $index.objects.'Перечисление.СтавкиНДС'
    Assert-Idx ('НДС20' -in @($enum.enum_values)) 'Enum value НДС20 missing.'
    Assert-Idx ('БезНДС' -in @($enum.enum_values)) 'Enum value БезНДС missing.'

    Assert-Idx ([bool]$index.modules.PSObject.Properties['ОбщийМодуль.ПродажиСервер']) 'Common module was not indexed.'
    $moduleMethods = $index.modules.'ОбщийМодуль.ПродажиСервер'.methods
    Assert-Idx ($moduleMethods.ЗарегистрироватьПродажу.export -eq $true) 'Export procedure flag missing.'
    Assert-Idx ($moduleMethods.ЗарегистрироватьПродажу.kind -eq 'Процедура') 'Export procedure kind wrong.'
    Assert-Idx ($moduleMethods.РассчитатьСумму.export -eq $true) 'Export function flag missing.'
    Assert-Idx ($moduleMethods.РассчитатьСумму.kind -eq 'Функция') 'Export function kind wrong.'
    Assert-Idx ($moduleMethods.ВнутреннийПересчет.export -eq $false) 'Non-export method should not be marked export.'
    Assert-Idx (-not $moduleMethods.PSObject.Properties['ЗакомментированнаяПроцедура']) 'Commented-out procedure must be ignored.'

    # --- Extension: adopted object merge (base attributes + extension attribute) plus a brand-new extension object.
    $merged = & $indexScript -SourceRoot @($designerMini, $designerExt)
    Assert-Idx ($merged.format -eq 'designer') 'Merged designer+extension format detection failed.'
    $mergedDoc = $merged.objects.'Документ.РеализацияТоваровУслуг'
    Assert-Idx ($mergedDoc.belonging -eq 'adopted') 'Adopted document should report belonging=adopted after merge.'
    Assert-Idx ('Контрагент' -in @($mergedDoc.attributes)) 'Merged document lost a base attribute.'
    Assert-Idx ('ПричинаВозврата' -in @($mergedDoc.attributes)) 'Merged document is missing the extension attribute.'
    Assert-Idx ([bool]$merged.objects.PSObject.Properties['Справочник.ПричиныВозврата']) 'Extension-only catalog was not indexed.'

    # --- EDT format.
    $edtIndex = & $indexScript -SourceRoot $edtMini
    Assert-Idx ($edtIndex.format -eq 'edt') 'EDT format was not detected.'
    $edtCatalog = $edtIndex.objects.'Справочник.Номенклатура'
    Assert-Idx ('Артикул' -in @($edtCatalog.attributes)) 'EDT catalog attribute Артикул missing.'
    Assert-Idx ([bool]$edtCatalog.tabular_sections.PSObject.Properties['Штрихкоды']) 'EDT tabular section missing.'
    Assert-Idx ('Штрихкод' -in @($edtCatalog.tabular_sections.Штрихкоды)) 'EDT tabular section attribute missing.'
    $edtModule = $edtIndex.modules.'ОбщийМодуль.ПродажиСервер'.methods
    Assert-Idx ($edtModule.РассчитатьСумму.export -eq $true) 'EDT export function flag missing.'
    Assert-Idx ($edtModule.ВнутреннийПересчет.export -eq $false) 'EDT non-export method should not be marked export.'

    # --- Cache: hit must reproduce the same shape without re-reading fixture files, and reflect an edit after invalidation.
    $cachePath = Join-Path $probeRoot 'metadata-index.json'
    $cacheRoot = Join-Path $probeRoot 'cache-src'
    Copy-Item -LiteralPath $designerMini -Destination $cacheRoot -Recurse
    $cold = & $indexScript -SourceRoot $cacheRoot -CachePath $cachePath
    Assert-Idx (Test-Path -LiteralPath $cachePath -PathType Leaf) 'Cache file was not written.'
    $cacheWriteTime = (Get-Item -LiteralPath $cachePath).LastWriteTimeUtc
    $warm = & $indexScript -SourceRoot $cacheRoot -CachePath $cachePath
    Assert-Idx ((Get-Item -LiteralPath $cachePath).LastWriteTimeUtc -eq $cacheWriteTime) 'Cache hit unexpectedly rewrote the cache file.'
    Assert-Idx ('Артикул' -in @($warm.objects.'Справочник.Номенклатура'.attributes)) 'Cache hit returned an incomplete index.'
    Start-Sleep -Milliseconds 50
    Add-Content -LiteralPath (Join-Path $cacheRoot 'Catalogs\Номенклатура.xml') -Value ' '
    $afterEdit = & $indexScript -SourceRoot $cacheRoot -CachePath $cachePath
    Assert-Idx ((Get-Item -LiteralPath $cachePath).LastWriteTimeUtc -gt $cacheWriteTime) 'Cache was not invalidated after a source edit.'
    Assert-Idx ('Артикул' -in @($afterEdit.objects.'Справочник.Номенклатура'.attributes)) 'Rebuilt index after cache invalidation is incomplete.'

    # --- Performance: synthetic export with 3000 objects. Cold < 30s, warm (cache hit) < 2s.
    $perfRoot = Join-Path $probeRoot 'perf-src'
    New-Item -ItemType Directory -Path (Join-Path $perfRoot 'Catalogs') -Force | Out-Null
    $objectCount = 3000
    $childLines = [System.Collections.Generic.List[string]]::new()
    for ($i = 1; $i -le $objectCount; $i++) {
        $name = "Объект$i"
        $childLines.Add("<Catalog>$name</Catalog>")
        $objectXml = @"
<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:xr="http://v8.1c.ru/8.3/xcf/readable" xmlns:xs="http://www.w3.org/2001/XMLSchema" version="2.20">
	<Catalog uuid="$([guid]::NewGuid())">
		<Properties>
			<Name>$name</Name>
		</Properties>
		<ChildObjects>
			<Attribute uuid="$([guid]::NewGuid())">
				<Properties>
					<Name>Реквизит1</Name>
					<Type><xs:Type>xs:string</xs:Type></Type>
				</Properties>
			</Attribute>
		</ChildObjects>
	</Catalog>
</MetaDataObject>
"@
        [IO.File]::WriteAllText((Join-Path $perfRoot "Catalogs\$name.xml"), $objectXml, [Text.UTF8Encoding]::new($false))
    }
    $perfConfig = @"
<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses" xmlns:xr="http://v8.1c.ru/8.3/xcf/readable" xmlns:xs="http://www.w3.org/2001/XMLSchema" version="2.20">
	<Configuration uuid="$([guid]::NewGuid())">
		<Properties><Name>СинтетическаяКонфигурация</Name></Properties>
		<ChildObjects>
$($childLines -join "`n")
		</ChildObjects>
	</Configuration>
</MetaDataObject>
"@
    [IO.File]::WriteAllText((Join-Path $perfRoot 'Configuration.xml'), $perfConfig, [Text.UTF8Encoding]::new($false))

    $perfCachePath = Join-Path $probeRoot 'perf-cache.json'
    $coldStopwatch = [System.Diagnostics.Stopwatch]::StartNew()
    $perfIndexCold = & $indexScript -SourceRoot $perfRoot -CachePath $perfCachePath
    $coldStopwatch.Stop()
    Assert-Idx (@($perfIndexCold.objects.PSObject.Properties).Count -eq $objectCount) 'Synthetic export did not index every object.'
    Write-Host "Cold index of $objectCount objects: $($coldStopwatch.Elapsed.TotalSeconds.ToString('0.00'))s"
    Assert-Idx ($coldStopwatch.Elapsed.TotalSeconds -lt 30) "Cold index of $objectCount objects exceeded 30s ($($coldStopwatch.Elapsed.TotalSeconds)s)."

    $warmStopwatch = [System.Diagnostics.Stopwatch]::StartNew()
    $perfIndexWarm = & $indexScript -SourceRoot $perfRoot -CachePath $perfCachePath
    $warmStopwatch.Stop()
    Assert-Idx (@($perfIndexWarm.objects.PSObject.Properties).Count -eq $objectCount) 'Warm (cached) index lost objects.'
    Write-Host "Warm (cache hit) index of $objectCount objects: $($warmStopwatch.Elapsed.TotalSeconds.ToString('0.00'))s"
    Assert-Idx ($warmStopwatch.Elapsed.TotalSeconds -lt 2) "Warm cache hit exceeded 2s ($($warmStopwatch.Elapsed.TotalSeconds)s)."

    Write-Host "Metadata index tests passed: $script:checks checks."
}
finally {
    $resolved = [System.IO.Path]::GetFullPath($probeRoot)
    $tempPrefix = [System.IO.Path]::GetFullPath([System.IO.Path]::GetTempPath()).TrimEnd('\', '/') + [System.IO.Path]::DirectorySeparatorChar
    if ($resolved.StartsWith($tempPrefix, [StringComparison]::OrdinalIgnoreCase) -and (Split-Path -Leaf $resolved) -like 'bsl-flow-metadata-index-*') {
        Remove-Item -LiteralPath $resolved -Recurse -Force -ErrorAction SilentlyContinue
    }
}
