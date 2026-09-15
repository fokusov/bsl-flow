#Requires -Version 7.0
[CmdletBinding()]
param()
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

# Self-test suite for 1c-estimate covering the spec's verification levels:
# Static (closed schema on reference valid/negative documents), Unit (staleness
# null-semantics, rounding steps, conservative sums, timebox shape, anchor
# divergence explanation) and Integration (validator over a copy of a real
# finalized change, including the stale transition).

$skillRoot = Split-Path -Parent $PSScriptRoot
$validatorPath = Join-Path $PSScriptRoot 'Test-1CEstimate.ps1'
$schemaPath = Join-Path $skillRoot 'references\estimate-schema.json'
$repoRoot = Split-Path -Parent (Split-Path -Parent (Split-Path -Parent $skillRoot))
$sourceChange = Join-Path $repoRoot 'openspec\changes\task-estimation-skill'

$passed = 0
$failed = [System.Collections.Generic.List[string]]::new()
function Assert-Case {
    param([Parameter(Mandatory)][string]$Name, [Parameter(Mandatory)][scriptblock]$Body)
    try {
        & $Body
        $script:passed++
        Write-Host "PASS $Name" -ForegroundColor Green
    }
    catch {
        $script:failed.Add($Name)
        Write-Host "FAIL $Name :: $($_.Exception.Message)" -ForegroundColor Red
    }
}
function Get-Sha256Text {
    param([Parameter(Mandatory)][string]$Text)
    $sha = [System.Security.Cryptography.SHA256]::Create()
    try { return ([BitConverter]::ToString($sha.ComputeHash([Text.UTF8Encoding]::new($false).GetBytes($Text)))).Replace('-', '').ToLowerInvariant() }
    finally { $sha.Dispose() }
}
function New-TestChange {
    param([Parameter(Mandatory)][string]$Name, [string]$Complexity = 'M', [string]$Risk = 'medium', [switch]$WithFinalValidation, [switch]$WithLint)
    $dir = Join-Path ([IO.Path]::GetTempPath()) ("estimate-suite-$Name-" + [guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $dir -Force | Out-Null
    $spec = "# $Name`n`n## Классификация`n`n- Сложность: $Complexity`n- Риск: $Risk`n`n## Цель`n`nЦель изменения для тестовой фикстуры.`n`n## Требуемое поведение`n`n1. Требование.`n`n## Контекст 1С`n`n- Контекст.`n`n## Не делать`n`n- Ничего.`n`n## Критерии приёмки`n`n- GIVEN условие WHEN действие THEN результат наблюдается объективно.`n`n## Требуемые проверки`n`n- [x] Unit — проверка доказывает работоспособность.`n`n## Неопределённости / допущения`n`n- Допущение фикстуры.`n"
    [IO.File]::WriteAllText((Join-Path $dir 'spec.md'), $spec, [Text.UTF8Encoding]::new($false))
    [IO.File]::WriteAllText((Join-Path $dir 'original-task.md'), "# Исходный запрос`n`nТестовая фикстура.`n", [Text.UTF8Encoding]::new($false))
    if ($WithFinalValidation) { [IO.File]::WriteAllText((Join-Path $dir 'final-validation.json'), '{"schema_version":2,"passed":true}', [Text.UTF8Encoding]::new($false)) }
    if ($WithLint) { [IO.File]::WriteAllText((Join-Path $dir 'spec-lint.json'), '{"schema_version":1,"passed":true,"errors":[]}', [Text.UTF8Encoding]::new($false)) }
    return $dir
}
function New-BaseEstimate {
    param([Parameter(Mandatory)][string]$ChangeDir, [string]$ChangeName = '', [string]$Kind = 'estimate')
    $specSha = Get-Sha256Text ([IO.File]::ReadAllText((Join-Path $ChangeDir 'spec.md')))
    $originalSha = Get-Sha256Text ([IO.File]::ReadAllText((Join-Path $ChangeDir 'original-task.md')))
    # Identity must equal the change directory leaf; derive it, never guess.
    $derivedName = [IO.Path]::GetFileName([IO.Path]::GetFullPath($ChangeDir).TrimEnd('\', '/'))
    return [ordered]@{
        schema_version = 1
        change = $derivedName
        created = '2026-09-14T12:00:00Z'
        kind = $Kind
        status = 'final'
        inputs = [ordered]@{ spec_sha256 = $specSha; design_sha256 = $null; original_task_sha256 = $originalSha }
        classification = [ordered]@{ complexity = 'M'; risk = 'medium' }
        baseline = 'middle_3y'
        ai_basis = [ordered]@{ model = 'gpt-6-astra'; effort = 'medium'; attempts = [ordered]@{ min = 1; max = 4 } }
        blocks = @(
            [ordered]@{ id = 'B-01'; title = 'Блок один'; human = [ordered]@{ min = 8; max = 20 }; ai = [ordered]@{ min = 1.25; max = 3 }; justification = 'Обоснование первого блока оценки.' }
            [ordered]@{ id = 'B-02'; title = 'Блок два'; human = [ordered]@{ min = 6; max = 16 }; ai = [ordered]@{ min = 1; max = 2.75 }; justification = 'Обоснование второго блока оценки.' }
        )
        totals = [ordered]@{ human = [ordered]@{ min = 14; max = 36 }; ai = [ordered]@{ min = 2.25; max = 5.75 } }
        exclusions = @()
        confidence = 'medium'
        assumptions = @('Тестовое допущение первое.', 'Тестовое допущение второе.')
        fork_drivers = @('Тестовый драйвер вилки.')
    }
}
function Write-EstimateFiles {
    param([Parameter(Mandatory)]$Estimate, [Parameter(Mandatory)][string]$ChangeDir, [string]$ExtraMd = '')
    $json = $Estimate | ConvertTo-Json -Depth 12
    [IO.File]::WriteAllText((Join-Path $ChangeDir 'estimate.json'), $json, [Text.UTF8Encoding]::new($false))
    $md = "# Оценка`n`nСгенерировано: $($Estimate.created)`n`n- spec_sha256: $($Estimate.inputs.spec_sha256)`n- original_task_sha256: $($Estimate.inputs.original_task_sha256)`n`nИтог: human $($Estimate.totals.human.min)-$($Estimate.totals.human.max) ч, ai $($Estimate.totals.ai.min)-$($Estimate.totals.ai.max) ч.`n$ExtraMd"
    [IO.File]::WriteAllText((Join-Path $ChangeDir 'estimate.md'), $md, [Text.UTF8Encoding]::new($false))
}

# ---------- Static: closed schema on reference documents ----------
Assert-Case 'schema accepts reference estimate document' {
    $doc = (New-BaseEstimate -ChangeDir (New-TestChange -Name 'static-ok') -ChangeName 'static-ok') | ConvertTo-Json -Depth 12
    if (-not (Test-Json -Json $doc -SchemaFile $schemaPath -ErrorAction SilentlyContinue)) { throw 'valid document rejected' }
}
foreach ($case in @(
    @{ Name = 'schema rejects unknown top-level property'; Mutate = { param($e) $e.extra_field = 'x' } },
    @{ Name = 'schema rejects missing required field'; Mutate = { param($e) $e.Remove('confidence') } },
    @{ Name = 'schema rejects wrong status'; Mutate = { param($e) $e.status = 'preliminary' } },
    @{ Name = 'schema rejects estimate without totals'; Mutate = { param($e) $e.Remove('totals'); $e.Remove('blocks'); $e.Remove('exclusions'); $e.Remove('timebox_hours') } },
    @{ Name = 'schema rejects malformed hash'; Mutate = { param($e) $e.inputs.spec_sha256 = 'zz' } },
    @{ Name = 'schema rejects empty assumptions'; Mutate = { param($e) $e.assumptions = @() } }
)) {
    Assert-Case $case.Name {
        $dir = New-TestChange -Name 'static-neg'
        $doc = New-BaseEstimate -ChangeDir $dir -ChangeName 'static-neg'
        & $case.Mutate $doc
        if (Test-Json -Json ($doc | ConvertTo-Json -Depth 12) -SchemaFile $schemaPath -ErrorAction SilentlyContinue) { throw 'negative document accepted' }
    }
}
Assert-Case 'schema accepts timebox and rejects timebox with blocks' {
    $dir = New-TestChange -Name 'static-timebox'
    $timebox = New-BaseEstimate -ChangeDir $dir -ChangeName 'static-timebox' -Kind 'timebox'
    $timebox.Remove('blocks'); $timebox.Remove('totals'); $timebox.Remove('exclusions')
    $timebox.timebox_hours = 6
    if (-not (Test-Json -Json ($timebox | ConvertTo-Json -Depth 12) -SchemaFile $schemaPath -ErrorAction SilentlyContinue)) { throw 'clean timebox rejected' }
    $timebox.blocks = @([ordered]@{ id = 'B-01'; title = 'Блок'; human = [ordered]@{ min = 1; max = 2 }; ai = [ordered]@{ min = 0.5; max = 1 }; justification = 'Лишний блок для таймбокса.' })
    if (Test-Json -Json ($timebox | ConvertTo-Json -Depth 12) -SchemaFile $schemaPath -ErrorAction SilentlyContinue) { throw 'timebox with blocks accepted' }
}

# ---------- Unit: validator over synthetic fixtures ----------
Assert-Case 'validator passes a well-formed estimate' {
    $dir = New-TestChange -Name 'unit-ok' -WithFinalValidation
    $estimate = New-BaseEstimate -ChangeDir $dir -ChangeName 'unit-ok'
    Write-EstimateFiles $estimate $dir
    $result = & $validatorPath -ChangePath $dir -NoThrow
    if (-not $result.passed) { throw "expected pass, got: $($result.errors -join ' | ')" }
}
Assert-Case 'gate refuses M change without final validation' {
    $dir = New-TestChange -Name 'unit-gate-m' -WithLint
    $estimate = New-BaseEstimate -ChangeDir $dir -ChangeName 'unit-gate-m'
    Write-EstimateFiles $estimate $dir
    $result = & $validatorPath -ChangePath $dir -NoThrow
    if ($result.passed) { throw 'M/medium with lint only must be refused' }
}
Assert-Case 'gate accepts S low change with passed lint only' {
    $dir = New-TestChange -Name 'unit-gate-s' -Complexity 'S' -Risk 'low' -WithLint
    $estimate = New-BaseEstimate -ChangeDir $dir -ChangeName 'unit-gate-s'
    $estimate.classification = [ordered]@{ complexity = 'S'; risk = 'low' }
    $estimate.blocks = @([ordered]@{ id = 'B-01'; title = 'Блок'; human = [ordered]@{ min = 2; max = 5 }; ai = [ordered]@{ min = 0.5; max = 1.25 }; justification = 'Обоснование блока S-изменения.' })
    $estimate.totals = [ordered]@{ human = [ordered]@{ min = 2; max = 5 }; ai = [ordered]@{ min = 0.5; max = 1.25 } }
    Write-EstimateFiles $estimate $dir
    $result = & $validatorPath -ChangePath $dir -NoThrow
    if (-not $result.passed -or $result.gate -cne 'lint') { throw "expected lint gate pass, got: $($result.errors -join ' | ')" }
}
Assert-Case 'staleness triggers on changed spec and on null-to-file transition' {
    $dir = New-TestChange -Name 'unit-stale' -WithFinalValidation
    $estimate = New-BaseEstimate -ChangeDir $dir -ChangeName 'unit-stale'
    Write-EstimateFiles $estimate $dir
    $fresh = & $validatorPath -ChangePath $dir -NoThrow
    if (-not $fresh.passed -or $fresh.stale) { throw 'fresh estimate reported stale' }
    [IO.File]::AppendAllText((Join-Path $dir 'spec.md'), "`nДополнительный текст.", [Text.UTF8Encoding]::new($false))
    $stale = & $validatorPath -ChangePath $dir -NoThrow
    if ($stale.passed -or -not $stale.stale) { throw 'changed spec must mark the estimate stale' }
    [IO.File]::WriteAllText((Join-Path $dir 'design.md'), '# Design', [Text.UTF8Encoding]::new($false))
    $staleDesign = & $validatorPath -ChangePath $dir -NoThrow
    $designError = @($staleDesign.errors | Where-Object { $_ -like '*design_sha256*' })
    if ($designError.Count -eq 0) { throw 'design appearing over null must be reported as a change' }
}
Assert-Case 'rounding steps are enforced (0.5 human, 0.25 ai)' {
    $dir = New-TestChange -Name 'unit-round' -WithFinalValidation
    $estimate = New-BaseEstimate -ChangeDir $dir -ChangeName 'unit-round'
    $estimate.blocks = @([ordered]@{ id = 'B-01'; title = 'Блок'; human = [ordered]@{ min = 8.3; max = 20 }; ai = [ordered]@{ min = 1.25; max = 3.1 }; justification = 'Обоснование с неверным округлением.' })
    $estimate.totals = [ordered]@{ human = [ordered]@{ min = 8.3; max = 20 }; ai = [ordered]@{ min = 1.25; max = 3.1 } }
    Write-EstimateFiles $estimate $dir
    $result = & $validatorPath -ChangePath $dir -NoThrow
    $roundErrors = @($result.errors | Where-Object { $_ -like '*rounding step*' })
    if ($roundErrors.Count -lt 2) { throw "expected rounding errors for human and ai, got: $($result.errors -join ' | ')" }
}
Assert-Case 'conservative sums are enforced' {
    $dir = New-TestChange -Name 'unit-sum' -WithFinalValidation
    $estimate = New-BaseEstimate -ChangeDir $dir -ChangeName 'unit-sum'
    $estimate.totals.human.min = 15
    Write-EstimateFiles $estimate $dir
    $result = & $validatorPath -ChangePath $dir -NoThrow
    if (@($result.errors | Where-Object { $_ -like '*conservative sum*' }).Count -eq 0) { throw 'sum mismatch must be reported' }
}
Assert-Case 'anchor divergence above 30% requires explanation section' {
    $dir = New-TestChange -Name 'unit-anchor' -WithFinalValidation
    $estimate = New-BaseEstimate -ChangeDir $dir -ChangeName 'unit-anchor'
    $estimate.blocks = @([ordered]@{ id = 'B-01'; title = 'Крупный блок'; human = [ordered]@{ min = 20; max = 60 }; ai = [ordered]@{ min = 2; max = 5.75 }; justification = 'Обоснование крупного блока.' })
    $estimate.totals = [ordered]@{ human = [ordered]@{ min = 20; max = 60 }; ai = [ordered]@{ min = 2; max = 5.75 } }
    Write-EstimateFiles $estimate $dir
    $without = & $validatorPath -ChangePath $dir -NoThrow
    if ($without.passed -or @($without.errors | Where-Object { $_ -like '*Расхождение с якорем*' }).Count -eq 0) { throw 'divergence without explanation must fail' }
    Write-EstimateFiles $estimate $dir -ExtraMd "`n## Расхождение с якорем`n`n- human_min: +66.7% — декомпозиция даёт консервативную нижнюю границу выше якоря.`n- human_max: +50% — монолитная задача с одним блоком реализации.`n"
    $with = & $validatorPath -ChangePath $dir -NoThrow
    if (-not $with.passed) { throw "divergence with per-boundary explanation must pass, got: $($with.errors -join ' | ')" }
}
Assert-Case 'anchor divergence section must state each divergent boundary with its percent' {
    $dir = New-TestChange -Name 'unit-anchor-numbers' -WithFinalValidation
    $estimate = New-BaseEstimate -ChangeDir $dir -ChangeName 'unit-anchor-numbers'
    $estimate.blocks = @([ordered]@{ id = 'B-01'; title = 'Крупный блок'; human = [ordered]@{ min = 20; max = 60 }; ai = [ordered]@{ min = 2; max = 5.75 }; justification = 'Обоснование крупного блока.' })
    $estimate.totals = [ordered]@{ human = [ordered]@{ min = 20; max = 60 }; ai = [ordered]@{ min = 2; max = 5.75 } }
    Write-EstimateFiles $estimate $dir -ExtraMd "`n## Расхождение с якорем`n`nОстальные границы в пределах 30%, итог консервативен.`n"
    $vague = & $validatorPath -ChangePath $dir -NoThrow
    if ($vague.passed -or @($vague.errors | Where-Object { $_ -like '*human_min*' }).Count -eq 0) { throw 'a section omitting the divergent boundary must fail' }
    Write-EstimateFiles $estimate $dir -ExtraMd "`n## Расхождение с якорем`n`n- human_min: +10% — небольшое превышение якоря.`n- human_max: +50% — монолитная задача.`n"
    $wrong = & $validatorPath -ChangePath $dir -NoThrow
    if ($wrong.passed -or @($wrong.errors | Where-Object { $_ -like '*states human_min*' }).Count -eq 0) { throw 'a stated percent that contradicts the computation must fail' }
}
Assert-Case 'external_artifact driver raises the AI anchor before divergence' {
    $dir = New-TestChange -Name 'unit-anchor-mod' -WithFinalValidation
    $estimate = New-BaseEstimate -ChangeDir $dir -ChangeName 'unit-anchor-mod'
    $estimate.fork_drivers = @('Внешний артефакт: external_artifact — парсер формата выгрузки.')
    $estimate.blocks = @([ordered]@{ id = 'B-01'; title = 'Парсер внешнего формата'; human = [ordered]@{ min = 14; max = 36 }; ai = [ordered]@{ min = 4; max = 10 }; justification = 'Обоснование блока с парсером внешнего формата.' })
    $estimate.totals = [ordered]@{ human = [ordered]@{ min = 14; max = 36 }; ai = [ordered]@{ min = 4; max = 10 } }
    Write-EstimateFiles $estimate $dir
    $result = & $validatorPath -ChangePath $dir -NoThrow
    if (-not $result.passed) { throw "flag-adjusted anchor must absorb the conservative AI fork without a divergence section, got: $($result.errors -join ' | ')" }
    if (@($result.anchor_flags_applied) -cnotcontains 'external_artifact') { throw "external_artifact flag not recorded: $($result.anchor_flags_applied -join ', ')" }
    if ([double]$result.anchor_effective_ai.max -ne 12 -or [double]$result.anchor_effective_ai.min -ne 4) { throw "unexpected effective AI anchor: $($result.anchor_effective_ai | ConvertTo-Json -Compress)" }
}
Assert-Case 'timebox validation: single value, no decomposition, gate applies' {
    $dir = New-TestChange -Name 'unit-timebox' -WithFinalValidation
    $estimate = New-BaseEstimate -ChangeDir $dir -ChangeName 'unit-timebox' -Kind 'timebox'
    $estimate.Remove('blocks'); $estimate.Remove('totals'); $estimate.Remove('exclusions')
    $estimate.timebox_hours = 6
    $json = $estimate | ConvertTo-Json -Depth 12
    [IO.File]::WriteAllText((Join-Path $dir 'estimate.json'), $json, [Text.UTF8Encoding]::new($false))
    $md = "# Оценка (timebox)`n`nСгенерировано: $($estimate.created)`n`n- spec_sha256: $($estimate.inputs.spec_sha256)`n- original_task_sha256: $($estimate.inputs.original_task_sha256)`n`nTimebox: 6 ч.`n"
    [IO.File]::WriteAllText((Join-Path $dir 'estimate.md'), $md, [Text.UTF8Encoding]::new($false))
    $result = & $validatorPath -ChangePath $dir -NoThrow
    if (-not $result.passed) { throw "timebox must pass, got: $($result.errors -join ' | ')" }
    if ($result.stats.blocks -ne 0) { throw 'timebox must not count blocks' }
}

# ---------- Integration: copy of a real finalized change ----------
if (-not (Test-Path -LiteralPath $sourceChange -PathType Container)) {
    Write-Host 'SKIP integration: source change not found' -ForegroundColor Yellow
}
else {
    Assert-Case 'integration: real finalized change passes end-to-end, then goes stale' {
        $parent = Join-Path ([IO.Path]::GetTempPath()) ('estimate-suite-integration-' + [guid]::NewGuid().ToString('N'))
        $dir = Join-Path $parent 'task-estimation-skill'
        New-Item -ItemType Directory -Path $dir -Force | Out-Null
        foreach ($file in @('spec.md', 'original-task.md', 'final-validation.json')) {
            Copy-Item -LiteralPath (Join-Path $sourceChange $file) -Destination $dir
        }
        $estimate = New-BaseEstimate -ChangeDir $dir
        Write-EstimateFiles $estimate $dir
        $result = & $validatorPath -ChangePath $dir -NoThrow
        if (-not $result.passed -or $result.gate -cne 'final-validation') { throw "real change must pass with final-validation gate, got: $($result.errors -join ' | ')" }
        [IO.File]::AppendAllText((Join-Path $dir 'spec.md'), ' ', [Text.UTF8Encoding]::new($false))
        $stale = & $validatorPath -ChangePath $dir -NoThrow
        if ($stale.passed -or -not $stale.stale) { throw 'spec edit must flip the copy to stale' }
        Remove-Item -LiteralPath $dir -Recurse -Force
    }
}

Write-Host ''
if ($failed.Count -gt 0) {
    Write-Host "ESTIMATE_SUITE_FAILED=$($failed.Count)" -ForegroundColor Red
    foreach ($name in $failed) { Write-Host "  failed: $name" }
    exit 1
}
Write-Host "ALL_ESTIMATE_SUITE_PASSED=$passed" -ForegroundColor Green
exit 0
