# Известные проблемы восстановления Council review

Статус на 2026-09-21: подтверждено двумя независимыми воспроизведениями; исправлено в текущем worktree, ожидает коммита и установки обновлённого пакета.

## Область

Проблемы относятся к high-risk/L маршруту `1c-spec-review`, сохранению terminal attempts и восстановлению после ответа chair, который был получен провайдером, но не прошёл финальную схему или invariant validation.

Дефекты были независимо воспроизведены в двух локальных проектах. Имена проектов, клиентов, локальные пути и внешние raw responses не являются частью репозитория BSL Flow. В репозитории фиксируются только обезличенные симптомы, подтверждение по коду и критерии исправления.

## CRR-001: финально невалидный chair attempt повторно используется как `completed`

### Симптом

После того как provider вернул JSON, chair attempt сохраняется с `status=completed`. Если последующая финальная проверка отклоняет payload, повторный запуск с `-ForceReplaceReview` повторно использует тот же terminal result и воспроизводит ошибку без нового dispatch.

Наблюдавшиеся ошибки:

- `chair.final_spec_text covers fewer material requirements than the reviewed draft manifest`;
- `Unresolved member questions require a needs_input verdict`.

### Подтверждение до исправления

- `Invoke-CouncilReview.ps1` регистрирует chair как `completed` до сборки и финальной проверки Council review.
- При наличии retained chair со статусом `completed` payload безусловно используется повторно.
- `ForceReplaceReview` управляет заменой `review.json`, но не инвалидирует retained attempt.
- Перечень terminal statuses не содержит отдельного состояния `validation_failed`.

### Влияние

Run root становится невосстановимым штатным параметром команды. Практический обходной путь — архивировать весь `<change>.council` и повторно запустить все роли, что теряет экономию от уже успешных member results.

## CRR-002: нет ограниченного chair-only retry после исправимого ответа

### Симптом

Ответ chair может пройти transport/JSON parsing, но не пройти role-specific schema или последующие final invariants, например из-за запрещённого provenance-поля, несовместимой пары `verdict`/`questions` или неполного `final_spec_text`. Такой результат не публикует валидный `review.json`, однако bounded repair/retry до исправления отсутствовал.

### Подтверждение до исправления

- Role-specific schema проверяется при регистрации; при её отклонении новый chair attempt для той же member aggregate раньше не создавался.
- Final invariants проверяются позже; поэтому отклонённый ими payload уже мог остаться retained `completed` result.
- Сохранённые успешные member roles нельзя штатно переиспользовать вместе с новым chair attempt после validation failure.

### Влияние

Исправимая ошибка формата либо согласованности вердикта требует полного нового Council cycle и повторной оплаты member roles.

## CRR-003: dry-run routing preview неполон для диагностики admission

### Симптом

Council корректно блокирует весь цикл, если обязательная роль не имеет credential и допустимого fallback. Однако пользователь может обнаружить оставшуюся привязку chair к другому provider только при запуске review.

### Подтверждение до исправления

`Invoke-CouncilReview.ps1 -DryRun` уже возвращает `role`, `attempt_id`, `route` и `credential_source`, но не показывает:

- effective provider;
- effective model и effort;
- имя `token_env` без значения секрета;
- настроенный fallback и результат fallback admission;
- effective endpoint identity без credential;
- причины блокировки в виде полной role-to-provider matrix.

### Влияние

Dry-run недостаточен для уверенной проверки смешанного routing до сетевого вызова и не объясняет полную effective-конфигурацию Council одной командой.

## Критерии исправления

1. Payload, не прошедший role-specific schema или final invariant validation, не может оставаться повторно используемым `completed` result.
2. Исходный response, usage, diagnostic и attempt id сохраняются неизменными как историческое evidence.
3. Один явно разрешённый chair-only retry создаёт новый attempt и переиспользует только успешные member roles с теми же input hashes и aggregate hash.
4. `unknown_after_dispatch` по-прежнему запрещает слепой повтор платного запроса.
5. `ForceReplaceReview` документирует отдельно замену опубликованного `review.json` и политику retained attempts; он не должен неявно означать повтор provider call.
6. Dry-run показывает для каждой enabled роли: role, provider, model, effort, route, credential source, token-env name, fallback, admission result и обезличенный endpoint identity.
7. Regression-тест воспроизводит validation failure, доказывает новый chair attempt без повторного member dispatch и проверяет неизменность старого evidence.

## Реализованное исправление

- Intended final spec/design проходят отдельный deterministic preflight до создания prepared publication и до изменения живых файлов.
- Семантическая ошибка chair сохраняется как immutable `validation-failed-<sequence>.json`, связанный с attempt id и binding hash; raw response, usage и terminal result не переписываются.
- Для того же binding разрешён ровно один новый chair attempt. Успешные member roles с прежним aggregate hash переиспользуются без нового dispatch.
- После двух validation failures следующий запуск останавливается до provider dispatch с исчерпанным retry budget.
- `unknown_after_dispatch` не переводится в validation retry и по-прежнему требует отдельной операторской сверки.
- Dry-run route matrix дополнена provider, model, effort, token-env name, fallback, admission и обезличенным endpoint identity.
- `Test-CouncilCycle.ps1` проверяет отсутствие публикации плохого результата, неизменность живой спецификации, chair-only retry, переиспользование member roles и остановку после исчерпания budget.

## Временный безопасный порядок восстановления

Для установленных версий без этого исправления:

1. Не считать provider success успешным review без опубликованного и прошедшего final validation `review.json`.
2. Не повторять `unknown_after_dispatch`.
3. При финально невалидном `completed` chair сохранять весь run root под новым архивным именем.
4. Новый полный Council cycle запускать только как отдельное явное решение, учитывая повторный расход провайдеров.
