# Verification: native-task-activation-adoption

## Приёмка инкремента, 2026-09-13

Статус: **ACCEPT** для согласованного offline/public-process среза: активация planned-задач в controller lifecycle, adopt (preview/apply) legacy-задач с сохранением UUID/истории/доказательств, rebind того же UUID и controller-owned маршруты S/M/repair. Все обязательные offline-проверки завершены на замороженном source-inventory (freeze r6, snapshot `bfn-4983a515`, 306 файлов). Это приёмка offline-контуров, не release и не внешняя приёмка; границы перечислены ниже.

`review-advisory.md` (2026-09-12) определил пять условий acceptance кода. Их закрытие:

1. **Independent code review — закрыто.** Два независимых review нативного контроллера: первый вердикт REVISE с семью P1 (`native-controller-review-1.md`) — все семь устранены и подтверждены регрессиями; checkpoint core review (`native-core-review-20260912.md`) подтвердил фиксы бюджета, canonical bytes, memory hooks и provider dependencies. Независимый review Council v2 validator (2026-09-13): ACCEPT-WITH-FIXES, единственный P2 закрыт новым негативным набором из 13 подслучаев; логика признана чистой без P1/P2 дефектов кода.
2. **Process/static boundary checks — закрыто.** `go vet ./...` без замечаний; закрытые migration/request контракты (`TestMigrationMetadataIsClosed`, `TestStrictMigrationDocumentRejectsDuplicateKeys`, `TestV1RequestRequiredFieldsAreChecked`), path/redaction (`TestProjectionRedactsCredentialFieldsAsWholeValues`, `TestMetadataRejectsSecretsAndControlCharacters`, `TestLegacyTimestampCredentialIsBlockedWithoutLeak`), fencing legacy-писателей (storage-fence в реальных PS-наборах `Test-NativeReuse`, `Test-TaskRuntimePin`, `Test-TaskBudgetGate`, `Test-NativeRecovery`; `TestLegacyDiscoveryIsReadOnly`).
3. **Real sandbox denial proof — частично: см. границы.** Worker-sandbox canonical-store deny и fail-closed границы процесса доказаны настоящим provider/lifecycle процессом: негативный public-тест `TestNativeControllerPublicLifecycleFailsClosedAfterImplement` (реальный dispatch, implement PASS → verify FAIL, блокировка acceptance/memory). Полный real Codex sandbox setup/backend denial остаётся environmental BLOCKED.
4. **Public controller integration — закрыто.** Шесть интеграционных тестов через реальный dispatch boundary (внешний PowerShell + внутренний worker process): S+ accept (`TestNativeControllerPublicLifecycleUsesSeparateProviderProcess`), S− FAIL-CLOSED, M через реальные PS validators (`TestNativeControllerPublicMediumLifecycleRunsSpecReviewThroughValidators`: реальный `Test-1CSpec.ps1` lint, profiled critic, `Complete-BSLFlowReview`, `Test-1CSpecFinal.ps1`, ровно 5 reservation/outcome пар бюджета), repair lifecycle с memory handoff (7 попыток), broken memory bridge (advisory-семантика), journal-ассерты. Свежие прогоны 2026-09-13 (вечер, это дерево): S+ 116.99s PASS, S− 78.29s PASS.
5. **Package checks — закрыто.** Freeze r6, все три lane exit=0: package (воспроизводимая сборка ZIP + полный набор, failures=0), extras (9/9 наборов), cli (`go test -timeout 45m` + сборка exe + smoke 52 проверки). Полный `go test ./... -count=1` на финальном дереве — PASS дважды. Артефакты: `BSL-Flow-native-activation-adoption.zip` (SHA-256 `0fccb6277677dfbc91f9bdb6578b2473a3e758c5b2e432ae99ad45af97b78060`) и `bsl-flow.exe` + `.sha256` в `work/blocked-completion-20260912/final-offline-20260913/output/`.

Контрактный дефект инкремента найден и исправлен при подготовке freeze: `legacyManifestFile` в `cli/internal/repository/adoption.go` писал `canonical_rel = source_rel`, тогда как migration-contract и apply-валидатор требуют `legacy/artifacts/<source-relative-path>` — preview генерировал план, который собственный apply отклонял. Исправлено по контракту; adoption-тесты (×3) детерминированы и проходят.

## Покрытие критериев приёмки

| Критерий (GIVEN/WHEN/THEN) | Результат | Evidence |
| --- | --- | --- |
| 1. activate + lifecycle, тот же UUID, один canonical journal, без checkout-local journal | PASS | S+ public integration (accept); `TestNativeControllerLifecycleUsesBoundProviderAndAcceptance`; бюджет: полные reservation/outcome пары |
| 2. invalid request / wrong worktree / stale revision → нет ready revision и dispatch | PASS | `TestRunOnPlannedRepositoryTaskIsActivationBlocker`, `TestExpectedRevisionConflict`, `TestV1RequestRequiredFieldsAreChecked` |
| 3. analysis-only / обязательные M/L reviews → маршрут без пропуска gates | PASS | M public integration через реальные PS validators (5 попыток, spec_review обязательна) |
| 4. implement PASS → verify FAIL → нет acceptance и memory promotion | PASS | S− FAIL-CLOSED public integration; repair lifecycle; `TestNativeAcceptanceRejectsTamperedTerminalEvidence` |
| 5. resume: верифицированный terminal receipt переиспользуется, unknown требует reconciliation | PASS | `TestNativeCompletedAcceptanceRechecksCurrentSource`, `TestNativeCancelPreservesRegisteredActiveAttempt`, repair lifecycle resume |
| 6. одинаковые legacy copies → одна история; divergent/corrupt → диагностируемый конфликт | PASS | `TestLegacyDuplicateDeduplicatedAndDivergenceIsConflict`, `TestDivergentLegacyHistoriesBlockDetailReads`, `TestCanonicalInvalidIdentityBlocksHealthyLegacyFallback`, `TestBrokenLegacyJournalBlocksShowAndHistory` |
| 7. adopt preview/apply: idempotence, изменённый source/чужой target не перезаписываются | PASS | `TestAdoptionPreviewApplyPreservesPrefixAndRepeats`, `TestMigrationMetadataIsClosed` (manifest с исходным/canonical расположением и SHA-256) |
| 8. migrated completed + удалённый worktree → доказательства доступны, новый запуск требует свежего binding | PASS | `TestStoreSurvivesWorktreeDeletion`, `TestStorePreservesDeletedOriginWorktree`, adoption prefix-preservation |
| 9. legacy writer после adoption не меняет legacy history, указывает canonical owner | PASS | storage-fence наборы (реальные PS), `TestLegacyDiscoveryIsReadOnly`, `TestSyntheticV1StateIsRejected` |

## Границы приёмки (BLOCKED / не заявляется)

- **Real Codex sandbox setup/backend denial — environmental BLOCKED.** Установленный Codex 0.154.0 не подтверждает работоспособную sandbox setup/backend конфигурацию; изменение security/backend настроек не разрешено. Синтетические capability fixtures не засчитываются как замена. Worker-sandbox canonical-store deny в provider seam доказан реальным процессом (см. условие 3).
- **Живой Astra council — вне этой приёмки** (контракт `api-specification-council`). Доступная capability evidence от 2026-09-12: no-model preflight `preflight_ok`, `gpt-6-astra`/`high`, точный executable SHA-256, 0 модельных вызовов. Повторный запуск REAL-harness preflight 2026-09-13 из внешней (не Codex) среды корректно остановился fail-closed: `CODEX_SESSION_ID is required` — harness требует идентичность настоящего Codex-хоста. Бюджет live-приёмки 0 из 4 не расходован.
- **Runtime 1С — не требуется и не разрешён** (требование 14 спеки).
- **Windows power-loss durability — не заявляется.**
- **Multi-OS и zero-PowerShell — не заявляются.** Поставка — Windows-only: Go controller + stateless Windows PowerShell provider; миграция дальше — отдельные milestone'ы `native-cross-platform-cli`.
- **Временный запрет Unica durable runtime jobs сохраняется.**
- **Рабочий репозиторий: commit/push не выполнялись.** Freeze r6 — воспроизводимый source-only snapshot, не публикация.

## Выполненные проверки

Свежие прогоны 2026-09-13 (вечер, текущее дерево после freeze):

- `go vet ./...` — без замечаний; `go test ./... -run '^$'` — компиляция PASS.
- Targeted repository: `TestMigrationMetadataIsClosed`, `TestAdoptionPreviewApplyPreservesPrefixAndRepeats`, `TestStrictMigrationDocumentRejectsDuplicateKeys`, `TestNativeControllerLifecycleUsesBoundProviderAndAcceptance`, `TestNativeAttemptBindsTrustedExecutablePath`, `TestNativeCompletedAcceptanceRechecksCurrentSource`, `TestNativeAcceptanceRejectsTamperedTerminalEvidence`, `TestLegacyDiscoveryIsReadOnly`, `TestRunOnPlannedRepositoryTaskIsActivationBlocker`, `TestCanonicalInvalidIdentityBlocksHealthyLegacyFallback` — 10/10 PASS (23.9s).
- Public S lifecycle: S+ (116.99s) и S− (78.29s) — PASS через реальный dispatch boundary.

Записанные прогоны 2026-09-13 (freeze r6, то же дерево):

- Полный `go test ./... -count=1` — PASS дважды (cli 415.6s/455s, repository 60.6s/53s).
- Шесть native controller интеграционных тестов (S+/S−/M/repair/broken-memory/journal) — PASS.
- PS-наборы: `MANAGED_REVIEW_OK checks=15`, `ALL_STAGE_CYCLE_PASSED=30`, native memory bridge 88 checks, `TASK_RUNTIME_PIN_OK checks=24`, `TASK_BUDGET_GATE_OK checks=22`, `Test-NativeRecovery` 31 check/0 failures, `MIXED_ROUTE_OK checks=23`, `PROFILED_CODEX_OK checks=105`.
- Freeze lanes: package 1/1 failures=0, extras 9/9, cli 2/2 (build + smoke 52) — все exit=0.

Environmental заметки воспроизведения: package lane запускать с `TMP=TEMP=C:\bfn-tmp` (длинный TEMP-путь ломает `git worktree add`: `'$GIT_DIR' too big`); не-ASCII имена в ассертах требуют UTF-8 console.

## Заключение

Все девять критериев приёмки, проверяемые в этой среде, закрыты положительно с реальными процесс-границами и полным бюджетным/каноническим контролем. Незакрытыми остаются только средовые границы: настоящий Codex sandbox denial, живой Astra council (0/4, требует запуска из Codex-хоста) и всё, что относится к runtime 1С и публикации. Отчёт сессии: `work/blocked-completion-20260912/PROGRESS-2026-09-13.md`.
