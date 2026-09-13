# Независимое ревью design, 12 сентября 2026

Область: core/provider increment; migration/adopt рассматривается отдельно после фиксации контракта.

Запрошенный reviewer: native Codex subagent `gpt-5.6-luna`, reasoning `max`, согласно явному выбору пользователя. Observed model/usage runtime не предоставлены. Это независимое advisory review, не Council live acceptance и не controller authorization receipt.

Первый verdict: REVISE. Приняты и устранены замечания:

- закрытый nested controller schema, v1 view mapping, whole-outer canonical hash и validation-before-append;
- canonical store deny в worker sandbox, отсутствие широкого `.git` reopen и обязательный настоящий no-model denial probe;
- отдельный stateless source-only seam без legacy state actions/journal;
- canonical directory reserves UUID, включая corrupt/unsupported state; отсутствие fallback;
- Go parent-attempt admission и проверяемые reservation/outcome artifacts для nested provider calls;
- отдельный migration contract до реализации adopt.

Повторный verdict core/provider: ACCEPT на уровне design, с двумя уточнениями перед реализацией. Root зафиксировал один точный provider ID во всех полях (`bsl-flow.native-provider.windows-ps.v1`) и явную матрицу native host/provider/worker/legacy permissions. Дополнительно уточнены provisional pre-activation view и привязка process receipts к настоящему runner, без выдуманного exit code.

Реализация core/provider разрешена пользователем и начата после исправлений. Acceptance кода остаётся открытой: нужны independent code review, process/static boundary checks, real sandbox denial proof, public controller integration и соответствующие package checks. Migration phase пока не входит в этот ACCEPT.

Migration design review: несколько REVISE выявили недостающие точные формулы/ordering, сохранение approved plan, closed N+1 mapping, sibling bindings, исторические dependencies, eligibility types и уточнение graph-lock crash scope. Они устранены в migration-contract.md. Reviewer подтвердил закрытие этих пунктов и последним замечанием потребовал явно включить exact committed target в target_available и разрешить committed repeat после удаления source. Root добавил эти точные условия и проверил их совместимость с immutable-prefix idempotence. Prefix semantic identity оставлена равной H(полностью проверенной terminal v1 revision): существующие previous_sha256 links транзитивно связывают историю. Root принял migration design с устранённым последним замечанием; implementation разрешена. Это не acceptance кода или доказательство crash/runtime поведения.
