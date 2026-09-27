# BSL Flow в Claude Code

Этот документ описывает поддержку Claude Code: установку плагина, что делают хуки, как настроить модель ревьюера и честные ограничения текущей реализации.

## Установка

### Локальный плагин до публикации 0.9

В сессии Claude Code:

```text
/plugin marketplace add C:/path/to/bsl-flow
/plugin install bsl-flow@bsl-flow
```

Плагин `bsl-flow` собран из корня этого репозитория (`.claude-plugin/plugin.json` и `.claude-plugin/marketplace.json` живут в корне, `source` в `marketplace.json` — `./`). Он подключает:

- 8 скиллов из `global/skills/`, включая `1c-task` и `1c-estimate`, под неймспейсом `bsl-flow:1c-spec`, `bsl-flow:1c-verify` и так далее — те же файлы, что и в остальных хостах, без копий;
- два read-only subagent'а из `hosts/claude-code/agents/`: `bsl-flow-spec-reviewer` (`Read, Grep, Glob`) и `bsl-flow-code-reviewer` (`Read, Grep, Glob`);
- хуки из `hosts/claude-code/hooks/hooks.json`.

Проверить локально до публикации: `claude plugin validate .` из корня репозитория. После публикации 0.9 локальный путь можно заменить на `fokusov/bsl-flow`; текущая удалённая ветка ещё не подтверждает наличие новой версии.

### Офлайн-альтернатива (без marketplace)

Из распакованной поставки 0.9 используй `pwsh -NoProfile -File scripts/Install-BSLFlowCore.ps1 -Host claude`: установщик показывает план; примени его с `-Apply`. Core содержит 6 основных assisted-скиллов, `1c-estimate` подключается отдельно. Установщик управляет путями, сохранением существующей конфигурации и откатом. Точный состав и параметры описаны в `INSTALL.md`. Managed устанавливается отдельно и доступен на Windows.

## Что делают хуки

Все хуки — обычные `pwsh`-скрипты в `hosts/claude-code/hooks/`, читающие JSON из stdin и решающие через exit code / JSON вывод, без обращения к сети.

- **`SessionStart.ps1`.** Определяет, является ли текущий каталог 1С-проектом (`Configuration.xml`, `*.mdo` в `src/`, `bsl-flow.yaml` или `.bsl-flow/project.yaml`; поиск ограничен по глубине, бюджет времени ~1 секунда). Вне 1С-проекта в контекст сессии попадает 0 байт. Внутри — печатает до 8 строк: маршрут S/M/L, инициализирован ли проект, активный change и статус его final validation.
- **`PreToolUse-EditGate.ps1`** (matcher `Edit|Write|MultiEdit`). Если путь не входит в `source.paths` из `bsl-flow.yaml` — пропускает. Если нет активного change — пропускает (S-маршрут). Если активный change M/L или high-risk и у него нет прошедшей `final-validation.json` с совпадающим хешем текущего `spec.md` — отказывает (`permissionDecision: deny`) с объяснением, что запустить (`1c-spec-review`).
- **`PreToolUse-EvidenceGuard.ps1`** (тот же matcher, выполняется первым). Отказывает в прямой правке файлов, которые пишут только скрипты: `openspec/changes/*/{spec-lint,review,final-validation}.json`, `.bsl-flow/evidence/**`, `.bsl-flow/active-change.json`. `review-reconciliation.json` явно разрешён — его по контракту пишет агент, а схему и хеши проверяет `Test-1CSpecFinal.ps1`.

Понятие «активный change»: `1c-spec` после создания M/L или high-risk спеки должен вызвать `global/skills/1c-spec/scripts/Set-1CActiveChange.ps1 -ProjectPath <project> -ChangeName <change>`, который читает `Сложность`/`Complexity` и `Риск`/`Risk` из `spec.md` и записывает `.bsl-flow/active-change.json`. `1c-verify` снимает его после финального вердикта: `Set-1CActiveChange.ps1 -ProjectPath <project> -Clear`.

## Настройка модели ревьюера

`bsl-flow-spec-reviewer` — независимый критик специфицированного поведения (M-маршрут single review). Его модель задаётся явно в `hosts/claude-code/agents/bsl-flow-spec-reviewer.md` (поле `model:` во frontmatter), а не наследуется от текущей сессии. Это сделано намеренно: если модель ревьюера совпадёт с моделью, которая писала спецификацию, независимость обзора теряется. Чтобы сменить модель ревьюера, отредактируйте `model:` в этом файле (в вашей копии плагина/skills, не в исходном репозитории BSL Flow, если вы не собираетесь предлагать это изменение вверх по потоку).

Публикация результата ревью: сессия Claude Code вызывает subagent, сохраняет его сырой JSON-ответ в `.bsl-flow/reports/spec-review/<change>.subagent/raw.json`, затем запускает `Invoke-1CSpecReview.ps1 -ChangeName <change> -ImportRaw <тот путь>`, который валидирует ответ по `global/skills/1c-spec-review/references/review-schema.json`, пересчитывает метрики детерминированно и публикует `review.json`.

`bsl-flow-code-reviewer` — аналогичный read-only ревьюер реализации для L-задач (или явно запрошенного code review); он не имеет фиксированного вывода в файл — его findings идут в стадийный контракт вызывающей стороны.

## Честная граница

Хук перехватывает только вызовы инструментов `Edit`/`Write`/`MultiEdit` самого Claude Code. Запись мимо них — через `Bash` (`Set-Content`, `echo >`, сторонний редактор, другой процесс) — хук не видит вообще. Поэтому хуки поднимают нижнюю планку для типичного пути редактирования внутри сессии, а не создают sandbox и не заменяют процесс.

Настоящая, агент-независимая гарантия — post-hoc `global/skills/1c-verify/scripts/Test-1CChangeGate.ps1 -ProjectPath <project> [-BaseRef HEAD]`. Он сравнивает изменённые файлы (tracked-диф + untracked) с `source.paths`, и если активный M/L/high-risk change не имеет пройденной, не устаревшей по хешу `final-validation.json` с mtime не позже самого раннего изменённого исходника — возвращает `FAIL: process_violation`. Это работает в любом хосте, независимо от того, поддерживает ли он хуки, и его нужно запускать из `1c-verify` перед итоговым вердиктом. Подробности и обоснование — ADR-13 в [docs/ARCHITECTURE_RU.md](../ARCHITECTURE_RU.md).

## Аварийный выключатель

`BSL_FLOW_GATES=off` (переменная окружения) заставляет `PreToolUse-EditGate.ps1` пропускать редактирование даже при непройденной final validation, но обязательно дописывает запись в `.bsl-flow/reports/gate-overrides.jsonl` (время, файл, change, причина). `Test-1CChangeGate.ps1` читает этот журнал и всегда возвращает его содержимое как `limitations` в своём JSON-вердикте, независимо от итогового `verdict`, — используйте это в `verification.md`, а не замалчивайте включённый выключатель.

## Managed (Windows, experimental)

Контроллер поддерживает два маршрута. Оба сохраняют действующие gates авторизации, источников и приёмки; запуск и изменение информационной базы требуют отдельного разрешения.

**Текущая сессия:** зарегистрируй доверенный request без `execution_profile` через `Start`. Затем вызови установленный `1c-task/scripts/Invoke-BSLFlowTask.ps1`:

```powershell
pwsh -NoProfile -File <controller> -Action Next -Format Prompt -ProjectPath <project> -TaskId <id>
pwsh -NoProfile -File <controller> -Action Submit -ProjectPath <project> -TaskId <id> -Stage <stage> -DispatchId <dispatch_id> -ResultFile <result.json>
```

`Next` возвращает JSON-конверт с `dispatch.prompt`, `dispatch_id`, путём схемы и рабочей копией `worker_path`. Выполняй задание именно в этой копии; результат сохрани вне неё. Поля результата: `schema_version: 1`, `status`, `summary`, `payload_json` (строка с JSON). Повторный `Next` показывает уже выданный dispatch; второй `Submit` с использованным идентификатором отклоняется. Неправильная стадия, схема, устаревшие входы и изменение controller state блокируют приёмку. Стадии `verify`, `spec_review`, `code_review` и `acceptance` остаются у контроллера: следуй его `next_action`. Для `Run` с одними `file_assertion` отдельный хост не требуется; другие проверки и review требуют настроенных исполнителей.

Receipt и итоговая приёмка явно содержат `isolation: current_agent` и перечень стадий без изоляции. Это журнал с проверками результата; текущая сессия сохраняет собственные полномочия, независимость ревью и OS sandbox этим режимом не обеспечиваются.

Обязательное независимое code review для L/high-risk или `require_code_review` нельзя выполнить через текущую сессию: `Next -Format Prompt` не выдаёт ей эту стадию, а `Submit` отклоняет такой результат. Нужен настроенный независимый исполнитель через контроллер; без него приёмка остаётся `BLOCKED`.

**Headless Claude Code — dispatch BLOCKED:** приватные read-denied пути ещё не защищены превентивно. Общий controller останавливает Claude до preflight, резервирования бюджета и запуска процесса; прямой вызов worker также блокируется. Проверки пути по stream-json выполняются слишком поздно, чтобы защищать данные от модельного провайдера. Офлайн capability probe и parser доступны для подготовки; разблокировка требует подтверждённой превентивной изоляции путей и отдельного пилота. Следующий контракт описывает подготовленную, но заблокированную ветку реализации.

В доверенном request укажи `execution_profile.provider: claude-code`, абсолютный путь и SHA-256 `claude.exe`, существующий контракт sandbox/toolset и явный бюджет. Модели задаются полными `claude-*` id, effort — `low|medium|high`. Sandbox в профиле нужен для проверок, запускаемых контроллером; сам Claude worker имеет `permission_rules`, без OS sandbox. Манифест `1c-task/adapters/claude-code.adapter.json` хранит allowlist версий. `--version`/`--help` проверяются до модельного вызова; неизвестная версия или отсутствующий флаг дают `BLOCKED`.

Worker запускается с `-p --output-format stream-json`; на стадиях чтения доступны `Read,Grep,Glob`, на `implement` также `Edit,Write`. Bash, веб, субагенты и MCP выключены, пользовательские/project settings не загружаются. Контроллер проверяет наблюдаемую модель, поток событий, целостность своего состояния и manifest исходников. Частичный запуск сохраняется для reconciliation без автоматического повторения платного вызова. Receipt фиксирует `permission_rules`, а не `os_sandbox`. Статус останется `experimental` до реальных пилотов S и M; офлайн-фикстуры не подтверждают живую изоляцию CLI.
