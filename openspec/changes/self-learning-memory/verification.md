# Verification: self-learning-memory

Дата: 2026-09-12. Проверяемый объём — локальный controller, PowerShell 7.6.6. Модели и база 1С не запускались.

## Текущий результат

Независимое критическое ревью: ACCEPT в согласованном объёме. Адресный `Test-TaskMemory.ps1` на полном изолированном снимке завершился `TASK_MEMORY_OK checks=146; model/runtime/database=0`; лог — `work/spec-completion-20260912/memory-final.log`. После этого согласована совместимость JSON Schema исторического v1 event с тремя прежними fingerprints; адресно проверены эта схема и regression assertion. Применимость и promotion по-прежнему требуют все пять текущих fingerprints.

Обязательный offline-набор объединённого пакета завершён: Memory — 146 PASS, остальные suites и package fixtures — PASS. Проверка продолжалась в коротком пути после ограничения Git; исходный отказ сохранён. Точные артефакты, журналы и границы — в [итоговом отчёте](../../../docs/THREE_SPEC_REMEDIATION_2026-09-12_RU.md).

## Проверенные контракты

- Promotable procedural experience создаётся закрытым шаблоном из acceptance/recovery receipt контроллера. Произвольный worker text получает audit rejection и не становится знанием.
- Promotion требует три согласованных подтверждения минимум из двух задач, отсутствие противоречий и совпадение fingerprints; повтор одного receipt идемпотентен.
- Четыре успешные задачи проходят существующий controller workflow; следующий attempt получает принятый memory bundle и Resume Capsule без смены disposition.
- Три независимых отрицательных запуска проходят `inspect PASS → implement PASS → verify FAIL` через настоящий `Invoke-BFVerification`. Они остаются failed без acceptance и procedural confirmations; повторённая ошибка остаётся zero-confirmation diagnostic.
- Разные typed error signatures сохраняются отдельно; нетипизированный worker failure не выдаётся за доказательство verifier.
- Project isolation, schema/toolchain/controller fingerprints, stage/task-kind/path/error selection и ограничение размера bundle проверяются детерминированно. В выбранной записи сохраняется ограниченная evidence reference.
- Производный index проверяет собственный hash, metadata и authoritative head, сохраняя фактический event count; обычное чтение не перечитывает содержимое всех событий.
- Удалённый/повреждённый index восстанавливается. Complete invalid/unknown event блокирует память; visibly truncated tail оставляет последний валидный advisory prefix согласно design. Append после неразрешённого torn tail блокируется до записи.
- Старые записи и задачи читаются; исторические fingerprints не дают права применять старое знание в новом окружении.

План архитектурного контекста сохранён без изменения. Runtime 1С и устойчивость Windows при отключении питания не являются подтверждёнными результатами этой проверки.