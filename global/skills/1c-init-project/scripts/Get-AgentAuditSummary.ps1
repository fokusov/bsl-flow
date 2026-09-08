[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)] [string]$ReportRoot,
    [string]$OutputPath
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Get-Prop {
    param([AllowNull()][object]$Object, [Parameter(Mandatory)][string]$Name)
    if ($null -eq $Object) { return $null }
    $p = $Object.PSObject.Properties[$Name]
    if ($null -eq $p) { return $null }
    return $p.Value
}
function Get-Text {
    param([AllowNull()][object]$Object, [Parameter(Mandatory)][string]$Name)
    $v = Get-Prop $Object $Name
    if ($null -eq $v) { return $null }
    $s = ([string]$v).Trim()
    if ([string]::IsNullOrWhiteSpace($s)) { return $null }
    return $s
}
function Get-KeyPart { param([AllowNull()][object]$Value) if ($null -eq $Value) { return 'unknown' } return ([string]$Value).Replace('|', '/') }
function Get-Num { param([AllowNull()][object]$Value) if ($null -eq $Value) { return $null } if ($Value -is [int] -or $Value -is [long] -or $Value -is [double] -or $Value -is [decimal]) { return [double]$Value } return $null }
function Format-Cell { param([AllowNull()][object]$Value) if ($null -eq $Value) { return 'unknown' } return ([string]$Value).Replace('|', '\|').Replace("`r", ' ').Replace("`n", ' ') }

if ([string]::IsNullOrWhiteSpace($OutputPath)) { $OutputPath = Join-Path $ReportRoot 'summary.md' }
$root = [System.IO.Path]::GetFullPath($ReportRoot)
if (-not (Test-Path -LiteralPath $root -PathType Container)) { New-Item -ItemType Directory -Path $root -Force | Out-Null }
$logs = @(Get-ChildItem -LiteralPath $root -Filter '*.jsonl' -File -Force | Sort-Object Name)
$events = [System.Collections.Generic.List[object]]::new()
$eventOrdinal = 0
foreach ($log in $logs) {
    foreach ($line in @(Get-Content -LiteralPath $log.FullName)) {
        if ([string]::IsNullOrWhiteSpace($line)) { continue }
        try { $event = $line | ConvertFrom-Json -ErrorAction Stop } catch { throw "Invalid JSONL in audit log: $($log.Name)" }
        $eventOrdinal++
        $events.Add([pscustomobject]@{ Event = $event; Ordinal = $eventOrdinal; Log = $log.Name })
    }
}

# A row is one attempt, not one event. This prevents repeated snapshots and lifecycle events
# from inflating denominators or token totals.
$attempts = @{}
foreach ($item in $events) {
    $e = $item.Event
    $key = "$(Get-KeyPart (Get-Text $e 'run_id'))/$(Get-KeyPart (Get-Text $e 'task_id'))/$(Get-KeyPart (Get-Text $e 'attempt_id'))"
    if (-not $attempts.ContainsKey($key)) {
        $attempts[$key] = [ordered]@{
            key = $key; run_id = Get-Text $e 'run_id'; task_id = Get-Text $e 'task_id'; attempt_id = Get-Text $e 'attempt_id'
            role = Get-Text $e 'role'; complexity = Get-Text $e 'complexity'; agent_id = Get-Text $e 'agent_id'; parent_agent_id = Get-Text $e 'parent_agent_id'
            events = [System.Collections.Generic.List[object]]::new(); status = $null; completed = $false; acceptance = $null; acceptance_actor = $null
            corrections = 0; escalations = 0; error_classes = [System.Collections.Generic.HashSet[string]]::new()
            requested_model = $null; observed_model = $null; requested_effort = $null; observed_effort = $null; model_mismatch = $false
            usages = [System.Collections.Generic.List[object]]::new(); timings = [System.Collections.Generic.List[object]]::new()
        }
    }
    $a = $attempts[$key]
    $a.events.Add($item)
    $a.status = Get-Text $e 'event_type'
    if ((Get-Text $e 'event_type') -eq 'completed') { $a.completed = $true }
    if ((Get-Text $e 'event_type') -eq 'acceptance') { $a.acceptance = Get-Text $e 'acceptance_state'; $a.acceptance_actor = Get-Text $e 'acceptance_actor' }
    if ((Get-Text $e 'event_type') -eq 'corrected') { $a.corrections++ }
    if ((Get-Text $e 'event_type') -eq 'escalated') { $a.escalations++ }
    $ec = Get-Text $e 'error_class'; if ($null -ne $ec) { [void]$a.error_classes.Add($ec) }
    foreach ($pair in @(@('requested_model','requested_model'), @('observed_model','observed_model'), @('requested_effort','requested_effort'), @('observed_effort','observed_effort'))) {
        $value = Get-Text $e $pair[0]
        if ($null -ne $value) { $a.($pair[1]) = $value }
    }
    if ($null -ne $a.requested_model -and $null -ne $a.observed_model -and $a.requested_model -ne $a.observed_model) { $a.model_mismatch = $true }
    $usage = Get-Prop $e 'usage'; if ($null -ne $usage) { $a.usages.Add([pscustomobject]@{ usage = $usage; ordinal = $item.Ordinal }) }
    $timing = Get-Prop $e 'timing'; if ($null -ne $timing) { $a.timings.Add([pscustomobject]@{ timing = $timing; ordinal = $item.Ordinal }) }
}

function Get-AttemptUsage {
    param([Parameter(Mandatory)][object]$Attempt)
    $deltas = @{}; $cumulative = $null; $unknown = $false
    foreach ($entry in @($Attempt.usages | Sort-Object ordinal)) {
        $u = $entry.usage; $mode = Get-Text $u 'mode'; $tokens = Get-Num (Get-Prop $u 'total_tokens')
        $completeness = Get-Text $u 'completeness'
        if ($completeness -eq 'partial') { $unknown = $true }
        if ($mode -eq 'cumulative') {
            if ($null -ne $tokens) { $cumulative = $tokens }
            else { $unknown = $true }
        } elseif ($mode -eq 'delta') {
            $measurement = Get-Text $u 'measurement_id'
            if ($null -eq $measurement -or $null -eq $tokens) { $unknown = $true } elseif (-not $deltas.ContainsKey($measurement)) { $deltas[$measurement] = $tokens }
        } else { $unknown = $true }
    }
    if ($null -ne $cumulative) { return [pscustomobject]@{ tokens = $cumulative; known = (-not $unknown); mode = 'cumulative' } }
    if ($deltas.Count -gt 0 -and -not $unknown) { return [pscustomobject]@{ tokens = (@($deltas.Values) | Measure-Object -Sum).Sum; known = $true; mode = 'delta' } }
    return [pscustomobject]@{ tokens = $null; known = $false; mode = 'unknown' }
}
function Get-AttemptDuration {
    param([Parameter(Mandatory)][object]$Attempt)
    $latest = @($Attempt.timings | Sort-Object ordinal | Select-Object -Last 1)
    if ($latest.Count -eq 0) { return $null }
    $d = Get-Num (Get-Prop $latest[0].timing 'duration_ms')
    if ($null -eq $d) { return $null }
    return $d
}

$acceptedFirst = 0; $acceptedAfter = 0; $completedNoAcceptance = 0; $accepted = 0; $rejected = 0; $blocked = 0; $cancelled = 0
$usageKnown = 0; $usagePartial = 0; $tokensTotal = 0.0; $durationKnown = 0; $durationTotal = 0.0; $errorCounts = @{}
$cohorts = @{}
foreach ($a in @($attempts.Values | Sort-Object key)) {
    if ($a.acceptance -eq 'accepted') { $accepted++; if ($a.corrections -eq 0) { $acceptedFirst++ } else { $acceptedAfter++ } }
    elseif ($a.acceptance -eq 'rejected') { $rejected++ } elseif ($a.acceptance -eq 'blocked') { $blocked++ } elseif ($a.acceptance -eq 'cancelled') { $cancelled++ }
    if ($a.completed -and $null -eq $a.acceptance) { $completedNoAcceptance++ }
    foreach ($ec in @($a.error_classes)) { if (-not $errorCounts.ContainsKey($ec)) { $errorCounts[$ec] = 0 }; $errorCounts[$ec]++ }
    $u = Get-AttemptUsage $a; if ($u.known) { $usageKnown++; $tokensTotal += $u.tokens } elseif ($a.usages.Count -gt 0) { $usagePartial++ }
    $duration = Get-AttemptDuration $a; if ($null -ne $duration) { $durationKnown++; $durationTotal += $duration }
    $model = if ($null -ne $a.observed_model) { $a.observed_model } else { $a.requested_model }
    $cohortKey = "$(Get-KeyPart $a.role)|$(Get-KeyPart $a.complexity)|$(Get-KeyPart $model)"
    if (-not $cohorts.ContainsKey($cohortKey)) { $cohorts[$cohortKey] = [ordered]@{ key = $cohortKey; n = 0; first = 0; after = 0; rejected = 0; cancelled = 0; unknown = 0; complete = 0 } }
    $c = $cohorts[$cohortKey]; $c.n++
    if ($a.acceptance -eq 'accepted' -and $a.corrections -eq 0) { $c.first++ } elseif ($a.acceptance -eq 'accepted') { $c.after++ } elseif ($a.acceptance -eq 'rejected' -or $a.acceptance -eq 'blocked') { $c.rejected++ } elseif ($a.acceptance -eq 'cancelled') { $c.cancelled++ } else { $c.unknown++ }
    if ($u.known -and $null -ne (Get-AttemptDuration $a)) { $c.complete++ }
}

$lines = [System.Collections.Generic.List[string]]::new()
$lines.Add('# Сводка наблюдаемости субагентов')
$lines.Add('')
$lines.Add('Производная сводка из JSONL; пересоздаётся из событий и не является рейтингом моделей или расчётом экономии.')
$lines.Add('')
$lines.Add("- Журналов: $($logs.Count); событий: $($events.Count); попыток: $($attempts.Count)")
$lines.Add("- Принято: $accepted (без исправлений в этой попытке: $acceptedFirst; после исправлений: $acceptedAfter); завершено без решения родителя: $completedNoAcceptance")
$lines.Add("- Отклонено: $rejected; заблокировано: $blocked; отменено: $cancelled")
$lines.Add("- Измерение токенов: $usageKnown попыток полное/однозначное, $usagePartial частичное; сумма известных totals: $(if ($usageKnown -gt 0) { [math]::Round($tokensTotal, 2) } else { 'unknown' }) (cached/reasoning не прибавляются)")
$lines.Add("- Измеренная длительность попыток: $durationKnown; сумма длительностей попыток: $(if ($durationKnown -gt 0) { [math]::Round($durationTotal, 2) } else { 'unknown' }) мс; это не active time и не wall-time параллельной работы")
$lines.Add("<!-- metrics: accepted_first=$acceptedFirst accepted_after_correction=$acceptedAfter rejected=$rejected cancelled=$cancelled attempts=$($attempts.Count) -->")
$lines.Add('')
$lines.Add('## Когорты (описательно, с явным знаменателем)')
$lines.Add('')
$lines.Add('| Роль | Сложность | Подтверждённая/запрошенная модель | Попытки (n) | Без исправлений в попытке | После исправлений | Отклонено/заблокировано | Отменено | Без решения | Полнота usage+duration |')
$lines.Add('| --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |')
foreach ($c in @($cohorts.Values | Sort-Object key)) {
    $parts = $c.key -split '\|', 3
    $lines.Add("| $(Format-Cell $parts[0]) | $(Format-Cell $parts[1]) | $(Format-Cell $parts[2]) | $($c.n) | $($c.first) | $($c.after) | $($c.rejected) | $($c.cancelled) | $($c.unknown) | $($c.complete)/$($c.n) |")
}
if ($cohorts.Count -eq 0) { $lines.Add('| unknown | unknown | unknown | 0 | 0 | 0 | 0 | 0 | 0 | 0/0 |') }
$lines.Add('')
$lines.Add('## Ошибки и исправления')
$lines.Add('')
if ($errorCounts.Count -eq 0) { $lines.Add('События с классифицированной ошибкой отсутствуют.') }
else { foreach ($name in @($errorCounts.Keys | Sort-Object)) { $lines.Add("- $(Format-Cell $name): $($errorCounts[$name]) попыток") } }
$lines.Add('')
$lines.Add('## Ограничения измерения')
$lines.Add('')
$lines.Add('- Значения без проверенного источника остаются `unknown`/`null`; проценты общих лимитов аккаунта не используются как usage подзадачи.')
$lines.Add('- Нативный runtime usage учитывается только при явном событии с provenance и привязкой к attempt; накопительные snapshots не суммируются, cached/reasoning не добавляются к total_tokens.')
$lines.Add('- Полные промпты, логи, CoT, секреты и данные аккаунта в сводку не копируются. Изменение задачи пользователем не классифицируется как ошибка агента.')
$lines.Add('- Когорты показывают знаменатель и полноту данных; общего leaderboard и автоматической перенастройки маршрутизации нет.')
$lines.Add('- Принято без исправлений относится к одной попытке, не означает успех всей задачи с первой попытки. Предыдущие попытки задачи не исчезают из знаменателя.')

$parent = Split-Path -Parent $OutputPath
New-Item -ItemType Directory -Path $parent -Force | Out-Null
$temp = Join-Path $parent ('.summary.' + [guid]::NewGuid().ToString('N') + '.tmp')
try {
    [IO.File]::WriteAllText($temp, ($lines -join "`n") + "`n", [Text.UTF8Encoding]::new($false))
    Move-Item -LiteralPath $temp -Destination $OutputPath -Force
} finally { if (Test-Path -LiteralPath $temp) { Remove-Item -LiteralPath $temp -Force -ErrorAction SilentlyContinue } }
[pscustomobject]@{ output_path = $OutputPath; event_count = $events.Count; attempt_count = $attempts.Count; log_count = $logs.Count }
