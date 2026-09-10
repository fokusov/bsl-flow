#Requires -Version 7.0
[CmdletBinding()]
param([string]$PackageRoot)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($PackageRoot)) { $PackageRoot = Split-Path -Parent $PSScriptRoot }
$generator = Join-Path $PackageRoot 'global\skills\1c-verify\scripts\New-1CTestStarter.ps1'
function Assert-True([bool]$Condition, [string]$Message) { if (-not $Condition) { throw $Message } }
$root = Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-starter-test-' + [guid]::NewGuid().ToString('N'))
try {
    $project = Join-Path $root 'project'; $database = Join-Path $root 'file-base'; $report = Join-Path $root 'reports'; $epf = Join-Path $root 'tools\vanessa-automation.epf'
    New-Item -ItemType Directory -Path $project,$database,(Split-Path -Parent $epf) -Force | Out-Null
    New-Item -ItemType File -Path $epf | Out-Null
    $first = & $generator -ProjectPath $project -TestClientFileDatabasePath $database -TestClientUser 'test user' -TestClientPort 48123 -VanessaEpfPath $epf -ReportPath $report
    Assert-True ($first.status -eq 'runtime_unverified') 'Generator asserted runtime verification.'
    $manifestPath = Join-Path $project '.bsl-flow\local\test-starter\vanessa\va-run.json'; $manifest = Get-Content $manifestPath -Raw -Encoding UTF8 | ConvertFrom-Json
    $pilotModule = Get-Content -LiteralPath (Join-Path $project 'tests\bsl-flow-starter\yaxunit\ТестПилотДвижка.bsl') -Raw -Encoding UTF8
    $rollbackRecipe = Get-Content -LiteralPath (Join-Path $project 'tests\bsl-flow-starter\yaxunit\ИнтеграцияСОткатом.recipe.bsl') -Raw -Encoding UTF8
    Assert-True ($pilotModule -match 'ЮТТесты\.ДобавитьТест\("ПоложительныйКонтрольДвижка"\)') 'Positive YAxUnit registration is missing.'
    Assert-True ($pilotModule -notmatch 'ДобавитьТест\("ОтрицательныйКонтрольОтчета"\)') 'Negative YAxUnit control was registered by default.'
    Assert-True (($rollbackRecipe -match 'НачатьТранзакцию\(\)') -and (($rollbackRecipe | Select-String -AllMatches 'ОтменитьТранзакцию\(\)').Matches.Count -ge 2)) 'Rollback recipe does not cover success and exception rollback.'
    Assert-True ($manifest.launcher.command_parameters -match 'DisableLoadTestClientsTable=true') 'Fresh TestClient profile does not disable saved table.'
    Assert-True ($manifest.launcher.command_parameters -match 'ДанныеКлиентовТестирования=') 'Fresh TestClient data is absent.'
    Assert-True ($manifest.launcher.command_parameters -match '\\;') 'Connection-string delimiter was not escaped.'
    Assert-True ($manifest.launcher.command_parameters -notmatch '/P') 'Generated manifest contains a password parameter.'
    Assert-True (([string]$manifest.va_params).StartsWith((Join-Path $project '.bsl-flow\local'))) 'Machine-specific Vanessa config was written outside ignored local state.'
    $profileJson = $manifest.launcher.command_parameters -replace '^.*ДанныеКлиентовТестирования=', '' -replace '\\;', ';'
    Assert-True ($profileJson.TrimStart().StartsWith('[')) 'Generated TestClient JSON is not a top-level array.'
    $second = & $generator -ProjectPath $project -TestClientFileDatabasePath $database -TestClientUser 'test user' -TestClientPort 48123 -VanessaEpfPath $epf -ReportPath $report
    Assert-True (@($second.artifacts | Where-Object status -eq 'existing_verified').Count -eq @($second.artifacts).Count) 'Second generation was not idempotent.'
    Add-Content -LiteralPath (Join-Path $project 'tests\bsl-flow-starter\vanessa\pilot-engine.feature') -Value '# conflict' -Encoding UTF8
    $conflictRejected=$false; try { & $generator -ProjectPath $project -TestClientFileDatabasePath $database -TestClientUser 'test user' -TestClientPort 48123 -VanessaEpfPath $epf -ReportPath $report | Out-Null } catch { $conflictRejected=$_.Exception.Message -match 'Refusing conflicting' }
    Assert-True $conflictRejected 'Conflicting output was overwritten.'
    $relativeRejected=$false; try { & $generator -ProjectPath $project -TestClientFileDatabasePath 'relative' -TestClientUser x -TestClientPort 48123 -VanessaEpfPath $epf -ReportPath $report | Out-Null } catch { $relativeRejected=$_.Exception.Message -match 'absolute filesystem' }
    Assert-True $relativeRejected 'Relative path was accepted.'
    $semicolonProject = Join-Path $root 'project;unsafe'; New-Item -ItemType Directory -Path $semicolonProject | Out-Null
    $semicolonRejected=$false; try { & $generator -ProjectPath $semicolonProject -TestClientFileDatabasePath $database -TestClientUser x -TestClientPort 48124 -VanessaEpfPath $epf -ReportPath $report | Out-Null } catch { $semicolonRejected=$_.Exception.Message -match "VAParams path containing ';'" }
    Assert-True $semicolonRejected 'Semicolon in VAParams path was accepted.'
    Write-Host 'Test starter generation contracts passed; no 1C process was started.'
}
finally { if (Test-Path -LiteralPath $root) { Remove-Item -LiteralPath $root -Recurse -Force } }
