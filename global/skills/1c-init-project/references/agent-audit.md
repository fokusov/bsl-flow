# Журнал субагентов

Скрипты этого каталога дают узкий, проектный append-only журнал. Оркестратор сам отправляет события; скрипты не перехватывают вызовы агентов, не сканируют переписку и не создают очередь.

## CLI

```powershell
& "$skillDir\scripts\Write-AgentAuditEvent.ps1" `
  -ProjectPath C:\work\project -RunId run-20260903 `
  -EventPath .\delegated.json
```

Вместо `-EventPath` можно передать `-EventJson` с одним JSON-объектом. В результате создаются/обновляются:

* `.bsl-flow/reports/subagents/<run-id>.jsonl` — одна строка на принятое событие;
* `.bsl-flow/reports/subagents/summary.md` — детерминированная производная сводка всех журналов каталога.

Явный `event_id` делает повтор того же события идемпотентным (`deduplicated`). Тот же ID с другим содержимым отклоняется. Невалидная строка в существующем журнале блокирует запись, чтобы не скрыть повреждение.

## Минимальный объект события

```json
{
  "schema_version": 1,
  "event_id": "evt-001",
  "run_id": "run-20260903",
  "task_id": "task-01",
  "attempt_id": "attempt-01",
  "agent_id": "agent-child-01",
  "parent_agent_id": "agent-parent",
  "role": "implementation",
  "complexity": "M",
  "event_type": "delegated",
  "emitted_at_utc": "2026-09-03T12:00:00Z",
  "requested_model": "gpt-5.6-luna",
  "requested_effort": "high",
  "observed_model": null,
  "observed_model_unavailable_reason": "runtime_not_exposed",
  "observed_effort": null,
  "observed_effort_unavailable_reason": "runtime_not_exposed",
  "model_source": "parent_routing_policy",
  "routing_rule": "project AGENTS.md",
  "routing_reason": "bounded independent implementation",
  "result_ref": null,
  "result_ref_unavailable_reason": "not_terminal"
}
```

`result_ref` и ссылки на проверки должны указывать на уже существующие безопасные доказательства; скрипт не копирует их содержимое. Полный prompt, лог, CoT, пароль, клиентские данные и проценты лимита аккаунта в событие не включаются.

Допустимые `event_type`: `delegated`, `started`, `completed`, `failed`, `interrupted`, `cancelled`, `corrected`, `escalated`, `acceptance`, `task_changed`, `unknown`. `completed` означает заявление/результат дочернего агента, но не принятие родителем. Для решения родителя используется отдельное `acceptance` с `acceptance_actor` и состоянием `accepted`, `rejected`, `blocked` или `cancelled`; после исправления допускается новое решение родителя с новым `event_id`, и в сводке учитывается последнее.

`corrected` требует `correction_of`, `correction_actor`, `correction_action`, `correction_result`; `escalated` — `from_model`, `to_model`, `escalation_reason`; `task_changed` — `task_change_reason`; `unknown` — `unknown_reason`. Ошибка требует `error_class` (`model`, `tool`, `environment`, `decomposition` или `unknown`) и ссылки на свидетельство либо причины недоступности. Изменение требований пользователем не записывается как ошибка субагента.

## Usage и время

Если runtime предоставляет счётчики, добавляется объект:

```json
"usage": {
  "mode": "cumulative",
  "measurement_id": "turn-01",
  "total_tokens": 1234,
  "provenance": "explicit_local_session_file"
}
```

Для `delta` нужен уникальный `measurement_id`; для `cumulative` берётся последний снимок попытки. Сводка не складывает cumulative snapshots и не прибавляет `cached_tokens`/`reasoning_tokens` к `total_tokens`. При отсутствии проверенной привязки — `mode: unknown`, `total_tokens: null` и `unavailable_reason`.

В `timing` допускается только подтверждённый `duration_ms` с `provenance` (например, `event_timestamps` или `parent_observation`). Воображаемое active time не выводится. Сумма длительностей попыток параллельных агентов не объявляется длительностью родительской работы.

## Ограниченный импорт локального runtime

`Import-AgentAuditUsage.ps1` принимает только явно указанный файл session JSONL и проверяет ожидаемые `session_id`, `parent_thread_id` и `agent_path`. Это адаптер наблюдаемого локального формата, а не стабильный публичный API. Разрешены только `session_meta`, `turn_context`, `event_msg` с `token_count`; текстовые сообщения игнорируются. Импорт не сканирует каталог сессий и возвращает partial/unknown при отсутствии однозначного baseline или turn context. Накопительные snapshots переводятся в один cumulative measurement на попытку.

## Сводка

`Get-AgentAuditSummary.ps1 -ReportRoot <dir>` пересоздаёт `summary.md` только из JSONL. Знаменатель — число попыток, а не число событий. Когорты группируются по роли, сложности и подтверждённой (иначе запрошенной) модели; показываются размер и полнота данных. Таблица не является leaderboard и не меняет routing.
