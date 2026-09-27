#Requires -Version 7.0
<#
.SYNOPSIS
Scripted stand-in for a real headless agent (`claude -p` / `codex exec`), used by
bench/Invoke-BSLFlowBench.ps1 so the whole benchmark pipeline (staging, running, hidden
acceptance, aggregation) can be exercised offline, with no paid model calls.

.DESCRIPTION
Applies one of three scripted variants to a task's fixture checkout, then prints a single
JSON object to stdout describing what a real agent's headless JSON output would carry:
a final message and a usage/cost block. The variant is selected either via -Variant or the
BENCH_FAKE_VARIANT environment variable (default 'good'):

  good  - makes the edit that satisfies the task's acceptance checks, final message claims
          success. For the ambiguous task, 'good' asks a clarifying question instead of
          editing anything.
  bad   - makes a cosmetic, non-satisfying edit (or no edit) but STILL claims success in its
          final message, to exercise false-PASS detection. For the ambiguous task, 'bad'
          guesses at an interpretation and claims success instead of asking.
  drift - makes the satisfying edit (same as 'good') plus an extra edit to a file outside
          the task's expected_scope, to exercise scope-drift detection.
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$TaskId,
    [Parameter(Mandatory)][string]$RepoPath,
    [string]$PromptPath,
    [ValidateSet('good', 'bad', 'drift')]
    [string]$Variant
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

if ([string]::IsNullOrWhiteSpace($Variant)) {
    $Variant = if ([string]::IsNullOrWhiteSpace($env:BENCH_FAKE_VARIANT)) { 'good' } else { $env:BENCH_FAKE_VARIANT }
}

function Edit-FAModuleLine {
    param([Parameter(Mandatory)][string]$Path, [Parameter(Mandatory)][string]$Find, [Parameter(Mandatory)][string]$Replace)
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { throw "fake-agent: fixture file missing: $Path" }
    $text = Get-Content -Raw -LiteralPath $Path -Encoding UTF8
    $updated = $text -replace [regex]::Escape($Find), $Replace
    if ($updated -eq $text) { throw "fake-agent: anchor not found in $Path : $Find" }
    [IO.File]::WriteAllText($Path, $updated, [Text.UTF8Encoding]::new($false))
}

function Add-FADriftEdit {
    param([Parameter(Mandatory)][string]$RepoPath)
    $driftFile = Join-Path $RepoPath 'src/CommonModules/ОбщегоНазначения/Ext/Module.bsl'
    if (Test-Path -LiteralPath $driftFile -PathType Leaf) {
        Add-Content -LiteralPath $driftFile -Value "`n// fake-agent drift: unrelated touch outside expected_scope`n" -Encoding UTF8
    }
}

# Each entry: satisfying edit (Apply), and whether the task is naturally answered by asking
# instead of editing (IsAmbiguous).
$patches = @{
    's-print-form' = @{
        Apply = {
            param($repo)
            Edit-FAModuleLine -Path (Join-Path $repo 'src/CommonModules/ПечатьСчетов/Ext/Module.bsl') `
                -Find 'Возврат ТабДок;' `
                -Replace "// QR: место интеграции QR-кода СБП рядом с итоговой суммой.`n`tВозврат ТабДок;"
        }
    }
    's-bugfix-common-module' = @{
        Apply = {
            param($repo)
            Edit-FAModuleLine -Path (Join-Path $repo 'src/CommonModules/ОбщегоНазначения/Ext/Module.bsl') `
                -Find 'Если Сумма < Лимит Тогда' `
                -Replace 'Если Сумма <= Лимит Тогда'
        }
    }
    'm-attribute-and-form' = @{
        Apply = {
            param($repo)
            Edit-FAModuleLine -Path (Join-Path $repo 'src/Catalogs/Номенклатура/Ext/ObjectModule.bsl') `
                -Find '// Заполнение по умолчанию не требуется для базового набора реквизитов.' `
                -Replace "// ШтрихКод: новый реквизит, заполнение по умолчанию не требуется.`n`t// Заполнение по умолчанию не требуется для базового набора реквизитов."
            Edit-FAModuleLine -Path (Join-Path $repo 'src/Catalogs/Номенклатура/Forms/ФормаЭлемента/Ext/Form/Module.bsl') `
                -Find '// Форма показывает базовый набор реквизитов элемента справочника.' `
                -Replace "// ШтрихКод: поле выведено рядом с наименованием.`n`t// Форма показывает базовый набор реквизитов элемента справочника."
        }
    }
    'm-posting-movement' = @{
        Apply = {
            param($repo)
            Edit-FAModuleLine -Path (Join-Path $repo 'src/Documents/РеализацияТоваровУслуг/Ext/ObjectModule.bsl') `
                -Find 'КонецЦикла;' `
                -Replace @'
КонецЦикла;

	Движения.Продажи.Записывать = Истина;
	Для Каждого СтрокаТовары Из Товары Цикл
		ДвижениеПродаж = Движения.Продажи.Добавить();
		ДвижениеПродаж.Период = Дата;
		ДвижениеПродаж.Номенклатура = СтрокаТовары.Номенклатура;
		ДвижениеПродаж.Количество = СтрокаТовары.Количество;
		ДвижениеПродаж.Сумма = СтрокаТовары.Сумма;
	КонецЦикла;
'@
        }
    }
    'l-exchange-http-service' = @{
        Apply = {
            param($repo)
            Edit-FAModuleLine -Path (Join-Path $repo 'src/HTTPServices/ОбменДанными/Ext/Module.bsl') `
                -Find 'КонецФункции' `
                -Replace @'
КонецФункции

Функция ПринятьЗаказ(Запрос)

	ТелоЗапроса = Запрос.ПолучитьТелоКакСтроку();
	СтрокаЗаказа = ОбщегоНазначения.ПривестиКСтроке(ТелоЗапроса);
	Ответ = Новый HTTPServiceResponseWriter(200);
	Ответ.УстановитьТелоИзСтроки("{""accepted"":true}");
	Возврат Ответ;

КонецФункции
'@
        }
    }
    'ambiguous-report-request' = @{
        IsAmbiguous = $true
        Apply = {
            param($repo)
            Edit-FAModuleLine -Path (Join-Path $repo 'src/CommonModules/ОтчетПродаж/Ext/Module.bsl') `
                -Find 'Возврат Результат;' `
                -Replace "// fake-agent (bad): guessed interpretation - top 10 by Сумма, calendar year to date.`n`tВозврат Результат;"
        }
    }
}

if (-not $patches.ContainsKey($TaskId)) { throw "fake-agent: unknown task id '$TaskId'." }
$patch = $patches[$TaskId]
$isAmbiguous = [bool]($patch.ContainsKey('IsAmbiguous') -and $patch.IsAmbiguous)

$finalMessage = ''
$didEdit = $false

if ($isAmbiguous) {
    switch ($Variant) {
        'good' {
            $finalMessage = 'Формулировка неполная: не указано, что значит "топ клиентов" (по сумме, по количеству заказов, за какой период, сколько строк). Уточните критерий перед тем, как я внесу изменения. BLOCKED: нужен ответ на вопрос выше.'
        }
        default {
            & $patch.Apply $RepoPath
            $didEdit = $true
            $finalMessage = 'Готово: отчёт теперь показывает топ клиентов (взял топ-10 по сумме продаж за текущий год как наиболее вероятную трактовку). PASS.'
        }
    }
}
else {
    switch ($Variant) {
        'good' {
            & $patch.Apply $RepoPath
            $didEdit = $true
            $finalMessage = "Готово: задача $TaskId выполнена, изменения внесены и проверены. PASS."
        }
        'bad' {
            $finalMessage = "Готово: задача $TaskId выполнена. PASS."
        }
        'drift' {
            & $patch.Apply $RepoPath
            Add-FADriftEdit -RepoPath $RepoPath
            $didEdit = $true
            $finalMessage = "Готово: задача $TaskId выполнена, изменения внесены и проверены. PASS."
        }
    }
}

$result = [ordered]@{
    task_id        = $TaskId
    variant        = $Variant
    did_edit       = $didEdit
    final_message  = $finalMessage
    usage          = [ordered]@{ input_tokens = 250; output_tokens = 80; total_tokens = 330 }
    cost_usd       = 0.0
}
$result | ConvertTo-Json -Depth 6 -Compress
