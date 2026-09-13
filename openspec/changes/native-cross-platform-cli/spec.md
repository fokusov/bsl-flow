# native-cross-platform-cli

## Классификация

- Сложность: L
- Риск: high

## Цель

Перенести BSL Flow с Windows-only Go host + PowerShell 7 engine на единый native Go CLI для Windows, macOS и Linux, сохранив одну controller state machine, существующие safety/recovery contracts и чтение исторических задач, а затем удалить PowerShell из runtime, поставки, CI и репозитория после доказанного parity.

## Текущее поведение

- Go CLI содержит четыре Go source files, встраивает `global/` bundle и запускает `Invoke-BSLFlowTask.ps1` через фиксированный machine-wide `pwsh.exe`.
- Runtime, storage, state transitions, gates, adapters, bootstrap и большая часть verification реализованы примерно в 100 `.ps1` files (около 16 тысяч строк).
- CLI host и cache/lock assumptions ориентированы на Windows; schemas местами требуют `.exe` и Windows-style executable contracts.
- Поддерживаемый controller runtime — только PowerShell 7 на Windows. macOS/Linux binary с тем же behavior отсутствует.
- Immutable journals, canonical JSON/hashes, attempts, acceptance, budget, recovery и publication уже являются совместимостными контрактами и не могут быть приблизительно переизобретены.

## Требуемое поведение

1. После финального migration milestone `bsl-flow` должен выполнять все пользовательские task/runner/review/bootstrap/package operations без запуска или наличия PowerShell. Go controller является единственной authoritative state machine; shell scripts не являются fallback authority.
2. Миграция выполняется вертикальными этапами: native read/catalog → storage/canonicalization/locks → state transitions/gates → adapters/review/runner/recovery/publication → bootstrap/build/install → default native → runtime PowerShell removal → repository `.ps1` removal. Каждый этап имеет явный compatibility gate и не объявляет последующие возможности готовыми.
3. Во время transition engine выбирается явно versioned configuration/CLI policy `native|legacy-powershell` на Windows. Legacy engine обслуживает только checkout-local v1 tasks/actions из опубликованной compatibility matrix; repository-store planned/controller v2 пишет только native engine. Ошибка native engine не запускает PowerShell автоматически. macOS/Linux принимают только native engine; legacy request возвращает unsupported blocker.
4. Native engine должен читать все поддерживаемые historical task/review/publication artifacts без изменения bytes. Новые revisions сохраняют documented canonical JSON, ordering, number/string/null semantics, UTF-8/no-BOM, SHA-256 links, timestamps и optimistic revision behavior либо используют schema v2 с явным dual-reader/migration contract.
5. Для каждого существующего controller action должны сохраняться observable status/stage/next-action semantics, exit codes, blocker classes, authorization checks, dependency/input hashes, attempt-before-dispatch rule и отсутствие force-pass.
6. Cross-platform filesystem layer должен реализовать safe canonical paths, symlink/reparse defense, atomic file publication, fsync/durability boundary, per-task/repository locks и crash recovery на NTFS, APFS и common Linux filesystems. Неатомарные/сетевые filesystems обнаруживаются и fail closed либо документируются как unsupported.
7. Supported release targets: `windows/amd64`, `darwin/arm64`, `darwin/amd64`, `linux/amd64`. Пользователю не нужны Go, PowerShell, Node, Python или package manager для запуска release binary; Git и явно выбранные external worker/test providers остаются отдельными prerequisites.
8. Paths/config schemas должны принимать platform-native absolute executable/filesystem paths без обязательного `.exe`, сохраняя strict argv execution без shell interpolation. Historical Windows v1 requests остаются readable.
9. Native process runner должен передавать arguments как data, иметь bounded stdout/stderr, timeout/cancellation, terminal receipt и process-tree behavior для каждой OS. Cancellation не объявляется rollback и unknown external effect не превращается в failure/success.
10. Codex и другие поддерживаемые worker adapters должны быть перенесены на native subprocess/structured event parsing с теми же sealed input, tool/sandbox, identity, usage и terminal-result boundaries. Unsupported sandbox/tool capability даёт `BLOCKED`, а не ослабленный запуск.
11. Specification council/API review должен использовать native transport/contracts после завершения `api-specification-council`; legacy OpenCode/PowerShell review не является обязательной зависимостью native path.
12. Repository Task Store и read-команды из `repository-task-registry` должны быть первым native vertical slice и не зависеть от engine selection. Их journal/repository identity становится общей storage boundary для последующего controller migration.
13. Native runner должен сохранять queue ownership, complete journal replay, deduplication, fairness/cursor, recovery-required и no-blind-retry semantics существующего runner contract.
14. Native verification/publication/delivery должны сохранять exact target/authorization, fresh evidence, protected paths/tests, accepted-source handoff, unknown effect и publish-resume contracts. Реализация на другой OS не расширяет разрешение пользователя.
15. 1С platform/runtime adapters остаются capability `windows-only`. На macOS/Linux source/spec/static workflows работают, но criterion, требующий native 1С runtime, возвращает явный `BLOCKED_UNSUPPORTED_PLATFORM`; его нельзя relabel/skip для PASS.
16. Cache/config locations используют OS conventions: trusted Windows local app data, macOS user caches/application support и XDG paths на Linux. Environment-controlled paths не считаются trusted без canonical validation. Versioned embedded assets проверяются полным manifest/hash перед использованием.
17. Bootstrap/project upgrade должны быть native, idempotent и comment-preserving по текущему contract. Они не устанавливают внешние test/runtime tools молча и не меняют credentials.
18. Build/package commands должны выдавать reproducible per-target archives/checksums из одного source revision, проверять embedded inventory и не требовать PowerShell. Release signing/notarization допускаются отдельным release policy, но неподписанный artifact не может называться подписанным.
19. CI должна иметь native OS matrix минимум Windows, macOS и Linux. Contract/golden suites проходят без `pwsh`; Windows-only 1С acceptance остаётся отдельным gate и не требуется для доказательства macOS source-only workflow.
20. До переключения default каждый migrated slice проходит differential/golden trace verification: одинаковые trusted inputs и fixtures дают совместимые envelopes, journals/hashes и decisions в legacy и native engines. Различия классифицируются и утверждаются как schema/behavior change, а не маскируются normalization.
21. Shadow mode может выполнять только read/decision computation и сравнение без writes, model/API calls, tests или external effects. Нельзя дважды запускать side-effecting action ради parity.
22. Native becomes default только после успешного clean-install smoke на каждой target OS, historical journal recovery, crash/fault tests и отсутствия неявных/обязательных runtime calls to PowerShell. Legacy engine после этого может находиться только в отдельном явно устанавливаемом Windows compatibility artifact/window и не входит в default installation/path.
23. Удаление runtime PowerShell считается следующим отдельным milestone: default и compatibility release archives не содержат `.ps1`/legacy executable path, help/docs/install не требуют `pwsh`, process audit не запускает его. Удаление всех `.ps1` из repository выполняется только после переноса build/test/install coverage и green native CI.
24. Existing dirty worktree changes по `ARCHITECTURE_CONTEXT_PLAN_RU.md` и `api-specification-council` не являются автоматически принятыми contracts. Перед implementation эта spec должна быть reconciled с их финальными merged schemas/ADRs и менять только подтверждённые extension points.

## Контекст 1С

- Конфигурация/подсистема: BSL Flow host/controller/tooling; 1С metadata не меняются.
- Затрагиваемые механизмы: весь `global/skills/*/scripts`, CLI host/bundle/cache, task schemas/storage/gates, adapters, tests, installers, packaging и CI.
- Evidence path: native CLI parse → repository/task store → controller transition/gate → native adapter/process → terminal evidence → immutable revision/acceptance. PowerShell исключается из final path.
- Runtime boundary: 1С executable/test base остаются Windows-only и требуют прежнего точного authorization.

## Не делать

- Не выполнять механический line-by-line translation без behavioral fixtures.
- Не держать две writable state machine после native default.
- Не использовать shell command strings вместо argv.
- Не делать automatic fallback с native на PowerShell.
- Не повторять side effects для differential testing.
- Не объявлять macOS/Linux поддержку на основании cross-compilation без native smoke.
- Не встраивать Git, Codex, 1С platform либо credentials в binary.
- Не ослаблять sandbox, path, authorization, acceptance или unknown-effect gates ради portability.
- Не включать Qwen Code либо общий unrestricted AgentRuntime.
- Не создавать `tasks.md`.

## Критерии приёмки

- GIVEN clean machine каждого target OS без PowerShell
  WHEN устанавливается release archive и выполняются help/version, project bootstrap, planned task create/list/show и source-only controller lifecycle
  THEN команды работают native, process audit не содержит `pwsh`, outputs проходят общие schemas.
- GIVEN historical v1 journals/review/publication fixtures
  WHEN native CLI выполняет read/status/context/recovery decisions
  THEN original bytes неизменны, hashes/decisions совместимы либо documented schema migration явно блокирует write.
- GIVEN одинаковый side-effect-free trace в legacy и native engines
  WHEN сравниваются canonical outputs
  THEN mismatch даёт failed compatibility gate с field-level diagnostic.
- GIVEN modifying action уже выполнился legacy либо native engine
  WHEN запускается parity verification
  THEN второй engine не повторяет action; сравнение использует frozen receipt/state.
- GIVEN process timeout/cancel after possible dispatch
  WHEN native controller восстанавливается
  THEN effect остаётся unknown, attempt не повторяется автоматически и acceptance не создаётся.
- GIVEN malicious path, symlink/reparse swap или shell metacharacters на каждой OS
  WHEN native CLI строит path/process invocation
  THEN escape/command execution блокируются, arguments остаются data.
- GIVEN macOS/Linux request с native 1С integration criterion
  WHEN вычисляется next action/run
  THEN возвращается `BLOCKED_UNSUPPORTED_PLATFORM`, model/test не вызываются и criterion не помечается PASS.
- GIVEN native default release
  WHEN package inventory, help/docs и runtime process trace проверяются
  THEN default installation не содержит обязательного/неявного PowerShell path, не запускает `pwsh`, а optional Windows compatibility artifact устанавливается и выбирается только явно.
- GIVEN runtime-PowerShell-removal milestone
  WHEN проверяются default и compatibility release inventories
  THEN `.ps1`, `pwsh` prerequisite и executable legacy engine отсутствуют в обеих поставках.
- GIVEN финальный zero-PowerShell milestone
  WHEN repository inventory и OS CI выполняются
  THEN `.ps1` files отсутствуют, native replacements покрывают бывшие build/test/install contracts, matrix зелёная.
- GIVEN unsupported filesystem atomicity/locking
  WHEN task write запрашивается
  THEN CLI блокирует write до revision и не оставляет ложный current/index.

## Требуемые проверки

- [x] Static — schemas, dependency inventory, no-shell/no-pwsh scan release path, documentation/platform matrix.
- [x] Unit — canonical JSON/hash parity, paths, locks, argv, process events, status/gates, redaction, cache manifests и OS config paths.
- [x] Integration — task lifecycle, runner, council, recovery, publication, bootstrap и repository registry на Windows/macOS/Linux temporary repos.
- [x] Differential — frozen side-effect-free traces legacy/native на Windows до retirement; no dual execution of external effects.
- [x] Fault injection — crash around every authoritative write/attempt/publication boundary and process cancellation.
- [x] Packaging — reproducible archives/checksums and clean-machine smoke on every release target.
- [x] Windows-only acceptance — retained focused native 1С evidence for supported profile; absence on macOS/Linux is expected blocker, not PASS.
- [ ] UI — отдельного UI не требуется.

## Неопределённости / допущения

- Go остаётся implementation language существующего CLI; смена языка не рассматривается.
- Git CLI остаётся runtime dependency на всех OS; libgit2/JGit embedding не требуется.
- Network filesystem support не обещается до отдельного evidence spike.
- Exact release distribution channel (GitHub Releases/Homebrew/winget) находится вне core migration; artifacts/checksums обязательны.
- Implementation начинается после фиксации финальных contracts параллельных architecture-context и API-council changes.
