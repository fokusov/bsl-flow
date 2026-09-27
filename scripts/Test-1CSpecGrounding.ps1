#Requires -Version 7.0
[CmdletBinding()]
param([string]$PackageRoot)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($PackageRoot)) { $PackageRoot = Split-Path -Parent $PSScriptRoot }

$groundingScript = Join-Path $PackageRoot 'global\skills\1c-spec-review\scripts\Test-1CSpecGrounding.ps1'
$codeGroundingScript = Join-Path $PackageRoot 'global\skills\1c-verify\scripts\Test-1CCodeGrounding.ps1'
foreach ($script in @($groundingScript, $codeGroundingScript)) {
    if (-not (Test-Path -LiteralPath $script -PathType Leaf)) { throw "Missing script: $script" }
}
$designerMini = Join-Path $PackageRoot 'scripts\fixtures\metadata\designer-mini'
$designerExt = Join-Path $PackageRoot 'scripts\fixtures\metadata\designer-ext'

$script:checks = 0
function Assert-Grd([bool]$Condition, [string]$Message) { if (-not $Condition) { throw "ASSERTION FAILED: $Message" }; $script:checks++ }
function Write-FixtureText { param([string]$Path, [string]$Text) $dir = Split-Path -Parent $Path; if (-not (Test-Path -LiteralPath $dir)) { New-Item -ItemType Directory -Path $dir -Force | Out-Null }; [IO.File]::WriteAllText($Path, $Text, [Text.UTF8Encoding]::new($false)) }

$specHeader = @'
# fixture-change

## Классификация
- Сложность: M
- Риск: low

## Цель
Fixture change for the grounding lint.

## Текущее поведение
Факты не требуются для фикстуры.

## Требуемое поведение
1. Первое требование.

'@

$specTail = @'

## Не делать
- Не расширять scope без необходимости.

## Критерии приёмки
- GIVEN некоторое состояние
  WHEN выполняется действие
  THEN наблюдается результат

## Требуемые проверки
- [x] Unit: проверка расчёта суммы.

## Неопределённости / допущения
Нет.
'@

$probeRoot = Join-Path ([System.IO.Path]::GetTempPath()) ('bsl-flow-spec-grounding-' + [guid]::NewGuid().ToString('N'))
try {
    New-Item -ItemType Directory -Path $probeRoot | Out-Null

    # --- Correct reference: object, attribute, tabular section (short form), export method call.
    $changeOk = Join-Path $probeRoot 'change-ok'
    $contextOk = @'
## Контекст 1С
- Конфигурация/подсистема: Продажи
- Затрагиваемые объекты: Справочник.Номенклатура, Документ.РеализацияТоваровУслуг
- Клиент/сервер: Сервер
- Расширение или основная конфигурация: Основная конфигурация
- Существующие точки расширения/механизмы: ОбщийМодуль.ПродажиСервер.РассчитатьСумму(Количество, Цена)
- Существенные ограничения: Использовать Документ.РеализацияТоваровУслуг.Товары.Количество и Справочник.Номенклатура.Штрихкоды.Штрихкод.
'@
    Write-FixtureText -Path (Join-Path $changeOk 'spec.md') -Text ($specHeader + $contextOk + $specTail)
    $resultOk = & $groundingScript -ChangePath $changeOk -SourceRoot $designerMini -NoThrow
    Assert-Grd ($resultOk.status -eq 'checked') 'Correct-reference case did not reach checked status.'
    Assert-Grd ($resultOk.passed) "Correct-reference case unexpectedly failed: $($resultOk.errors | ConvertTo-Json -Depth 5)"
    Assert-Grd ($resultOk.references_checked -gt 0) 'No references were counted for the correct-reference case.'
    Assert-Grd ($resultOk.index.format -eq 'designer') 'Index format missing from correct-reference result.'

    # --- Typo in object name -> error with suggestion, in a normative section.
    $changeTypo = Join-Path $probeRoot 'change-typo'
    Write-FixtureText -Path (Join-Path $changeTypo 'spec.md') -Text ($specHeader + @'
## Контекст 1С
- Конфигурация/подсистема: Продажи
- Затрагиваемые объекты: Справочник.Наменклатура
- Клиент/сервер: Сервер
- Расширение или основная конфигурация: Основная конфигурация
- Существующие точки расширения/механизмы: нет.
- Существенные ограничения: нет.
'@ + $specTail)
    $resultTypo = & $groundingScript -ChangePath $changeTypo -SourceRoot $designerMini -NoThrow
    Assert-Grd (-not $resultTypo.passed) 'Typo in object name should fail grounding.'
    $typoError = @($resultTypo.errors | Where-Object { $_.reference -match 'Наменклатура' })
    Assert-Grd ($typoError.Count -eq 1) 'Typo error for Наменклатура was not reported exactly once.'
    Assert-Grd ($typoError[0].suggestion -eq 'Номенклатура') "Typo suggestion was not 'Номенклатура': $($typoError[0].suggestion)"

    # --- Unknown attribute -> error with suggestion.
    $changeAttr = Join-Path $probeRoot 'change-attr'
    Write-FixtureText -Path (Join-Path $changeAttr 'spec.md') -Text ($specHeader + @'
## Контекст 1С
- Конфигурация/подсистема: Продажи
- Затрагиваемые объекты: Справочник.Номенклатура.Артикель
- Клиент/сервер: Сервер
- Расширение или основная конфигурация: Основная конфигурация
- Существующие точки расширения/механизмы: нет.
- Существенные ограничения: нет.
'@ + $specTail)
    $resultAttr = & $groundingScript -ChangePath $changeAttr -SourceRoot $designerMini -NoThrow
    Assert-Grd (-not $resultAttr.passed) 'Unknown attribute should fail grounding.'
    $attrError = @($resultAttr.errors | Where-Object { $_.message -match 'Unknown attribute' })
    Assert-Grd ($attrError.Count -eq 1) 'Unknown attribute error was not reported.'
    Assert-Grd ($attrError[0].suggestion -eq 'Артикул') "Attribute typo suggestion was not 'Артикул': $($attrError[0].suggestion)"

    # --- Tabular section attribute: valid long form + invalid short form.
    $changeTs = Join-Path $probeRoot 'change-ts'
    Write-FixtureText -Path (Join-Path $changeTs 'spec.md') -Text ($specHeader + @'
## Контекст 1С
- Конфигурация/подсистема: Продажи
- Затрагиваемые объекты: Документ.РеализацияТоваровУслуг.ТабличнаяЧасть.Товары.Цена
- Клиент/сервер: Сервер
- Расширение или основная конфигурация: Основная конфигурация
- Существующие точки расширения/механизмы: Документ.РеализацияТоваровУслуг.Товары.Скидка
- Существенные ограничения: нет.
'@ + $specTail)
    $resultTs = & $groundingScript -ChangePath $changeTs -SourceRoot $designerMini -NoThrow
    Assert-Grd (-not $resultTs.passed) 'Unknown tabular section attribute should fail grounding.'
    $tsError = @($resultTs.errors | Where-Object { $_.message -match 'tabular section attribute' })
    Assert-Grd ($tsError.Count -eq 1) 'Unknown tabular section attribute (short form) error was not reported.'
    Assert-Grd (-not (@($resultTs.errors) | Where-Object { $_.reference -match 'ТабличнаяЧасть\.Товары\.Цена' })) 'Valid long-form tabular section reference was incorrectly flagged.'

    # --- Non-export method call -> always an error, even outside a normative section.
    $changeExport = Join-Path $probeRoot 'change-export'
    Write-FixtureText -Path (Join-Path $changeExport 'spec.md') -Text ($specHeader + '## Контекст 1С' + "`n- Конфигурация/подсистема: Продажи`n- Затрагиваемые объекты: нет.`n- Клиент/сервер: Сервер`n- Расширение или основная конфигурация: Основная конфигурация`n- Существующие точки расширения/механизмы: нет.`n- Существенные ограничения: нет.`n" + $specTail + "`n`n## Неопределённости / допущения`nВызов ОбщийМодуль.ПродажиСервер.ВнутреннийПересчет(Документ) как гипотеза.`n")
    $resultExport = & $groundingScript -ChangePath $changeExport -SourceRoot $designerMini -NoThrow
    Assert-Grd (-not $resultExport.passed) 'Non-export method call must fail regardless of section.'
    $exportError = @($resultExport.errors | Where-Object { $_.message -match 'non-export common module method' })
    Assert-Grd ($exportError.Count -eq 1) 'Non-export method call error was not reported.'

    # --- Declared-new object: no collision when the object truly is new.
    $changeNewOk = Join-Path $probeRoot 'change-new-ok'
    Write-FixtureText -Path (Join-Path $changeNewOk 'spec.md') -Text ($specHeader + @'
## Контекст 1С
- Конфигурация/подсистема: Продажи
- Затрагиваемые объекты: Справочник.ПричиныОтказа (новый)
- Клиент/сервер: Сервер
- Расширение или основная конфигурация: Основная конфигурация
- Существующие точки расширения/механизмы: нет.
- Существенные ограничения: нет.
'@ + $specTail)
    $resultNewOk = & $groundingScript -ChangePath $changeNewOk -SourceRoot $designerMini -NoThrow
    Assert-Grd ($resultNewOk.passed) "Declared-new object that truly does not exist should pass: $($resultNewOk.errors | ConvertTo-Json -Depth 5)"

    # --- Collision: declared new but the object already exists.
    $changeCollision = Join-Path $probeRoot 'change-collision'
    Write-FixtureText -Path (Join-Path $changeCollision 'spec.md') -Text ($specHeader + @'
## Контекст 1С
- Конфигурация/подсистема: Продажи
- Затрагиваемые объекты: Справочник.Номенклатура (новый)
- Клиент/сервер: Сервер
- Расширение или основная конфигурация: Основная конфигурация
- Существующие точки расширения/механизмы: нет.
- Существенные ограничения: нет.
'@ + $specTail)
    $resultCollision = & $groundingScript -ChangePath $changeCollision -SourceRoot $designerMini -NoThrow
    Assert-Grd (-not $resultCollision.passed) 'Collision with an existing object must fail.'
    Assert-Grd (@($resultCollision.errors | Where-Object { $_.message -match 'Collision' }).Count -eq 1) 'Collision error was not reported.'

    # --- No source roots resolve -> unavailable, passed:true.
    $changeUnavailable = Join-Path $probeRoot 'change-unavailable'
    Write-FixtureText -Path (Join-Path $changeUnavailable 'spec.md') -Text ($specHeader + $specTail)
    $missingRoot = Join-Path $probeRoot 'does-not-exist'
    $resultUnavailable = & $groundingScript -ChangePath $changeUnavailable -SourceRoot $missingRoot -NoThrow
    Assert-Grd ($resultUnavailable.status -eq 'unavailable') 'Missing source root should report status=unavailable.'
    Assert-Grd ($resultUnavailable.passed) 'Unavailable sources must not fail the lint (passed should stay true).'
    Assert-Grd (@($resultUnavailable.warnings).Count -eq 1) 'Unavailable sources should emit exactly one warning.'

    # --- English syntax forms resolve to the same canonical objects.
    $changeEn = Join-Path $probeRoot 'change-en'
    Write-FixtureText -Path (Join-Path $changeEn 'spec.md') -Text ($specHeader + @'
## Контекст 1С
- Конфигурация/подсистема: Sales
- Затрагиваемые объекты: Catalog.Номенклатура, Catalogs.Номенклатура
- Клиент/сервер: Сервер
- Расширение или основная конфигурация: Основная конфигурация
- Существующие точки расширения/механизмы: CommonModule.ПродажиСервер.РассчитатьСумму(1, 2)
- Существенные ограничения: нет.
'@ + $specTail)
    $resultEn = & $groundingScript -ChangePath $changeEn -SourceRoot $designerMini -NoThrow
    Assert-Grd ($resultEn.passed) "English syntax forms should resolve without error: $($resultEn.errors | ConvertTo-Json -Depth 5)"

    # --- Extension adopted object: merged index sees both base and extension attributes.
    $changeExt = Join-Path $probeRoot 'change-ext'
    Write-FixtureText -Path (Join-Path $changeExt 'spec.md') -Text ($specHeader + @'
## Контекст 1С
- Конфигурация/подсистема: Возвраты
- Затрагиваемые объекты: Документ.РеализацияТоваровУслуг.ПричинаВозврата, Справочник.ПричиныВозврата
- Клиент/сервер: Сервер
- Расширение или основная конфигурация: Расширение
- Существующие точки расширения/механизмы: нет.
- Существенные ограничения: нет.
'@ + $specTail)
    $resultExt = & $groundingScript -ChangePath $changeExt -SourceRoot @($designerMini, $designerExt) -NoThrow
    Assert-Grd ($resultExt.passed) "Extension-adopted object reference should pass: $($resultExt.errors | ConvertTo-Json -Depth 5)"

    # --- Cache: metadata index cache is reused across grounding calls on the same project.
    $cacheProjectRoot = Join-Path $probeRoot 'cache-project'
    New-Item -ItemType Directory -Path (Join-Path $cacheProjectRoot 'openspec\changes\demo') -Force | Out-Null
    Copy-Item -LiteralPath $designerMini -Destination (Join-Path $cacheProjectRoot 'src') -Recurse
    Write-FixtureText -Path (Join-Path $cacheProjectRoot 'bsl-flow.yaml') -Text "source:`n  paths:`n    - src`n"
    Write-FixtureText -Path (Join-Path $cacheProjectRoot 'openspec\changes\demo\spec.md') -Text ($specHeader + @'
## Контекст 1С
- Конфигурация/подсистема: Продажи
- Затрагиваемые объекты: Справочник.Номенклатура.Артикул
- Клиент/сервер: Сервер
- Расширение или основная конфигурация: Основная конфигурация
- Существующие точки расширения/механизмы: нет.
- Существенные ограничения: нет.
'@ + $specTail)
    $resultColdCache = & $groundingScript -ChangePath (Join-Path $cacheProjectRoot 'openspec\changes\demo') -NoThrow
    Assert-Grd ($resultColdCache.passed) "Project-resolved source root case should pass: $($resultColdCache.errors | ConvertTo-Json -Depth 5)"
    $cacheFilePath = Join-Path $cacheProjectRoot '.bsl-flow\cache\metadata-index.json'
    Assert-Grd (Test-Path -LiteralPath $cacheFilePath -PathType Leaf) 'Metadata index cache file was not created for the resolved project root.'
    $cacheWriteTime = (Get-Item -LiteralPath $cacheFilePath).LastWriteTimeUtc
    $resultWarmCache = & $groundingScript -ChangePath (Join-Path $cacheProjectRoot 'openspec\changes\demo') -NoThrow
    Assert-Grd ($resultWarmCache.passed) 'Warm cache-hit grounding call should also pass.'
    Assert-Grd ((Get-Item -LiteralPath $cacheFilePath).LastWriteTimeUtc -eq $cacheWriteTime) 'Cache hit unexpectedly rewrote the metadata index cache.'

    # --- Code grounding over changed .bsl files: PASS and FAIL cases.
    $codeProjectRoot = Join-Path $probeRoot 'code-project'
    New-Item -ItemType Directory -Path $codeProjectRoot -Force | Out-Null
    Copy-Item -LiteralPath $designerMini -Destination (Join-Path $codeProjectRoot 'src') -Recurse
    Write-FixtureText -Path (Join-Path $codeProjectRoot 'bsl-flow.yaml') -Text "source:`n  paths:`n    - src`n"
    Push-Location $codeProjectRoot
    try {
        & git init -q .
        & git config user.email 'fixture@example.com'
        & git config user.name 'Fixture'
        & git add -A
        & git commit -q -m 'baseline'
    }
    finally { Pop-Location }

    Write-FixtureText -Path (Join-Path $codeProjectRoot 'src\CommonModules\ПродажиСервер\Ext\NewFeature.bsl') -Text (@'
Процедура Пример() Экспорт
	Товар = Справочники.Номенклатура.НайтиПоКоду("1");
	Сумма = ПродажиСервер.РассчитатьСумму(1, 2);
КонецПроцедуры
'@)
    $codeResultPass = & $codeGroundingScript -ProjectPath $codeProjectRoot -NoThrow
    Assert-Grd ($codeResultPass.passed) "Code grounding should pass for a known object and an exported method: $($codeResultPass.errors | ConvertTo-Json -Depth 5)"
    Assert-Grd ($codeResultPass.verdict -eq 'PASS') 'Code grounding verdict should be PASS.'

    Write-FixtureText -Path (Join-Path $codeProjectRoot 'src\CommonModules\ПродажиСервер\Ext\NewFeature.bsl') -Text (@'
Процедура Пример() Экспорт
	Товар = Справочники.Номенклатура2.НайтиПоКоду("1");
	ПродажиСервер.ВнутреннийПересчет(Товар);
КонецПроцедуры
'@)
    $codeResultFail = & $codeGroundingScript -ProjectPath $codeProjectRoot -NoThrow
    Assert-Grd (-not $codeResultFail.passed) 'Code grounding should fail for an unknown object and a non-export method call.'
    Assert-Grd ($codeResultFail.verdict -eq 'FAIL') 'Code grounding verdict should be FAIL.'
    Assert-Grd (@($codeResultFail.errors | Where-Object { $_.message -match 'Unknown metadata object: Справочник.Номенклатура2' }).Count -eq 1) 'Unknown metadata object was not reported by code grounding.'
    Assert-Grd (@($codeResultFail.errors | Where-Object { $_.message -match 'non-export common module method' }).Count -eq 1) 'Non-export module method call was not reported by code grounding.'

    # An incomplete export (such as the HTTP-service bench fixture) has no metadata modules.
    # Empty PSObject.Properties must not be dereferenced as .Name under StrictMode, and a
    # caller-owned cache/output must not become an agent scope change.
    $httpFixtureProject = Join-Path $probeRoot 'http-service-without-metadata'
    Write-FixtureText -Path (Join-Path $httpFixtureProject 'bsl-flow.yaml') -Text "source:`n  paths:`n    - src`n"
    $httpModulePath = Join-Path $httpFixtureProject 'src\HTTPServices\ОбменДанными\Ext\Module.bsl'
    Write-FixtureText -Path $httpModulePath -Text "Функция ПолучитьСтатус(Запрос) Экспорт`nКонецФункции`n"
    Push-Location $httpFixtureProject
    try {
        & git init -q .
        & git config user.email 'fixture@example.com'
        & git config user.name 'Fixture'
        & git add -A
        & git commit -q -m 'http-service baseline'
    }
    finally { Pop-Location }
    Write-FixtureText -Path $httpModulePath -Text "Функция ПолучитьСтатус(Запрос) Экспорт`n`tВозврат Неопределено;`nКонецФункции`n"
    $httpGroundingOutput = Join-Path $probeRoot 'http-grounding.json'
    $httpMetadataCache = Join-Path $probeRoot 'http-metadata-index.json'
    $httpGroundingResult = & $codeGroundingScript -ProjectPath $httpFixtureProject -OutputPath $httpGroundingOutput -MetadataCachePath $httpMetadataCache -NoThrow
    Assert-Grd ($httpGroundingResult.verdict -eq 'PASS') 'Empty metadata modules must be handled without a StrictMode Name error.'
    Assert-Grd ((Test-Path -LiteralPath $httpGroundingOutput) -and (Test-Path -LiteralPath $httpMetadataCache)) 'Caller-owned grounding artifacts were not written.'
    Assert-Grd (-not (Test-Path -LiteralPath (Join-Path $httpFixtureProject '.bsl-flow'))) 'Grounding created generated evidence inside the staged fixture.'

    $codeResultInvalidBase = & $codeGroundingScript -ProjectPath $codeProjectRoot -BaseRef 'definitely-not-a-commit' -NoThrow
    Assert-Grd ($codeResultInvalidBase.verdict -eq 'BLOCKED' -and -not $codeResultInvalidBase.passed) 'Invalid BaseRef must produce a BLOCKED code-grounding result.'
    Assert-Grd ($codeResultInvalidBase.message -match 'unable to enumerate changed BSL files') 'Invalid BaseRef BLOCKED result must preserve Git enumeration evidence.'

    Write-Host "Spec grounding lint tests passed: $script:checks checks."
}
finally {
    $resolved = [System.IO.Path]::GetFullPath($probeRoot)
    $tempPrefix = [System.IO.Path]::GetFullPath([System.IO.Path]::GetTempPath()).TrimEnd('\', '/') + [System.IO.Path]::DirectorySeparatorChar
    if ($resolved.StartsWith($tempPrefix, [StringComparison]::OrdinalIgnoreCase) -and (Split-Path -Leaf $resolved) -like 'bsl-flow-spec-grounding-*') {
        Remove-Item -LiteralPath $resolved -Recurse -Force -ErrorAction SilentlyContinue
    }
}
