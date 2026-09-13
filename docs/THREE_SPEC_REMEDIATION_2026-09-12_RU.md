# Доработка трёх спецификаций, 12 сентября 2026

Состояние: **PASS для локальной source/controller/CLI поставки**. Все обязательные offline-проверки завершены. Живой fallback текущего Astra-хоста остаётся BLOCKED; native activation/adoption реестра остаются отложенным этапом спецификации.

Объём: `self-learning-memory`, `api-specification-council`, `repository-task-registry`. Проверен рабочий каталог поверх `9ab6710`, включая ранее внесённые агентами изменения. Реализация делегировалась с запрошенными `gpt-5.6-luna / max`, независимое критическое ревью — `gpt-6-astra / high`. Атрибутируемых данных для расчёта стоимости нет.

## Что изменено

### Память контроллера

Произвольный текст worker больше не становится принятым знанием по меткам `procedural/low`. Promotable observations создаются закрытыми шаблонами из acceptance/recovery receipts контроллера. Promotion требует три согласованных подтверждения минимум из двух задач; повтор receipt идемпотентен.

Проверяются проект, актуальные fingerprints, stage/task-kind/path/error selection и границы bundle. Обычное чтение использует проверенный производный index без перечитывания содержимого всего журнала. Complete invalid/unknown event блокирует память; visibly truncated tail сохраняет допустимый advisory prefix согласно design, но блокирует новый append. Старые events читаются, сохраняя запрет применять неподтверждённые исторические записи.

Три отрицательных публичных сценария проходят настоящий `implement PASS → verify FAIL`: нет acceptance, procedural confirmations или promotion. Разные typed error signatures сохраняются отдельно; нетипизированный worker failure не выдаётся за verifier evidence.

### Council

Устранена регрессия ordinary workers: запуск с `--ephemeral` больше не требует отсутствующий persisted rollout. Строгий Council fallback использует отдельный persisted context и проверяет terminal observed identity. Capability связывается с фактическими executable/sandbox/catalog/skills и текущим host receipt, а не с утверждениями project config. Сохранённый исторический turn проверяется отдельно от текущей модели/effort.

Публичный и managed входы используют общий fallback adapter. Исправлены два PowerShell scope-дефекта callbacks: исчезающие функции после выхода loader и недоступный HTTP body reader. Публичный HTTP-тест проходит в отдельном `pwsh -NoProfile`, подтверждает четыре роли, выбранный ADR с точным excerpt в JSON payload, review v2 и final validation.

Prepared publication восстанавливается до snapshot/нового dispatch. Intended/live draft/final/original/policy проверяются до записи. Final validation предшествует completed receipt; UTF-8 BOM не создаёт ложный policy drift. Admission и reservation бюджета атомарны; concurrent negative case исключает oversubscription.

Transport проверен реальным loopback HTTP: terminal provider envelopes, потоковые ограничения размера и общий deadline, отсутствие redirect/credential forwarding. Responses использует `text.format`, согласно [контракту OpenAI](https://developers.openai.com/api/docs/guides/migrate-to-responses); deadline охватывает body после [ResponseHeadersRead](https://learn.microsoft.com/en-us/dotnet/api/system.net.http.httpcompletionoption?view=net-9.0).

### Реестр задач

Завершён planned/read-only slice. Canonical/legacy UUID-конфликты и повреждённые siblings блокируются; read-команды не создают repository identity. Git environment не перенаправляет выбор репозитория, origin worktree сохраняется, существующий evidence-файл не считается отсутствующим каталогом.

Public JSON не раскрывает credential-like значения и PEM bodies. Projected timestamps валидируются как RFC3339Nano без эха отклонённого значения; фильтры и сортировка сравнивают моменты времени. Default list ограничен 100 строками, overview использует полный набор.

## Проверка

Зафиксированы 256 файлов; до обновления только отчётных Markdown-файлов рабочее дерево полностью совпадало со снимком. Код ZIP и все 107 entries встроенного CLI bundle совпадают побайтно. Все три направления получили независимое ревью ACCEPT в указанном объёме.

| Проверка | Результат |
|---|---|
| Memory в общем пакете | 146 PASS |
| Council validation / engine / transport / fallback | 15 / 25 / 58 / 8 PASS |
| Council public routing / cycle / lifecycle | 16 / 30 / 31 PASS |
| Managed review / host capability | 13 / 7 PASS |
| Дополнительные CI-suites | Все 9 PASS; Profiled adapter — 97 PASS |
| Go tests / vet | PASS; итоговая сборка повторяет все Go tests |
| CLI reproducible build / exact executable smoke | PASS / 28 PASS |
| Остальные обязательные package/controller suites | PASS, включая repair 36 и task publication 39 |

Начальный `Build-BSLFlowPackage -Test` прошёл воспроизводимость ZIP, manifest/hash/extraction и suites до delivery включительно, затем остановился в runner на `Git: '$GIT_DIR' too big` из-за длинного стандартного TEMP/extract пути. Аналогично первый CLI smoke остановился на длинном пути временного snapshot. Эти отказы сохранены, не посчитаны PASS и не скрыты.

CLI smoke повторён с тем же executable и frozen script из штатного короткого корня. Package suite продолжен с runner в короткой изолированной копии с теми же SHA-256: десять оставшихся suites и все последующие package fixtures прошли. Уже пройденные suites не повторялись. Continuation harness меняет только список dispatch уже завершённых suites и подпись итогового сообщения; assertions и последующие fixtures сохранены. Таким образом, обязательный набор закрыт совокупностью двух журналов, а исходный единый wrapper остаётся зарегистрированным неуспешным запуском из-за пути Git.

Основные evidence-файлы в `work/spec-completion-20260912`: `final-inventory.json`, `frozen-source-check.json`, `final-package.log`, `final-package-remaining.log`, `package-resume-plan.json`, `package-resume-harness.diff`, `package-remaining-result.json`, `extras-results.json`, `final-cli-build.log`, `final-cli-smoke-shortpath.log`, `package-cli-source-parity.json`.

## Артефакты и границы

[Пакет](../outputs/spec-completion-20260912/BSL-Flow-spec-completion.zip), [CLI](../outputs/spec-completion-20260912/bsl-flow.exe), [receipt с SHA-256 и evidence](../outputs/spec-completion-20260912/acceptance-receipt.json). Финальный ZIP отличается от проверенного снимка только четырьмя отчётными Markdown-файлами; все entry hashes и неизменность кода проверяются при переупаковке. Проверенный исходный ZIP сохранён отдельно.

В рамках транспортных и runtime-проверок новые внешние provider/model вызовы не выполнялись. Прежний DeepSeek smoke остаётся только историческим live evidence. Current-agent fallback текущего Astra-хоста блокируется до dispatch: фактическая поддержка этого native host/model не доказана. Для закрытия этой границы нужна отдельная native host acceptance; fixture capability не заменяет её.

`activate`/`adopt` остаются staged BLOCKED до совместимого native controller write slice, как предусмотрено спецификацией. База 1С, Unica runtime jobs и глобальная установка не затрагивались. Устойчивость Windows при отключении питания не заявляется. План архитектурного контекста и прежние deliverables сохранены; В рабочем репозитории commit/push не выполнялись; Git-публикация тестировалась только в изолированных локальных fixtures.