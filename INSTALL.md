# Установка BSL Flow 0.8.0-dev.3

Установка framework не загружает расширения в базы.

## Запуск задач

Единственный пользовательский вход — PowerShell-контроллер. Зависимости движка перечислены в требованиях ниже.

```powershell
& "$env:USERPROFILE\.agents\skills\1c-task\scripts\Invoke-BSLFlowTask.ps1" -Action Start -ProjectPath C:\PRJ\client\project -InputFile C:\Tasks\request.json
& "$env:USERPROFILE\.agents\skills\1c-task\scripts\Invoke-BSLFlowTask.ps1" -Action Run -ProjectPath C:\PRJ\client\project -TaskId <uuid>
```

Полный [контракт CLI и очереди](global/skills/1c-task/references/task-contract.md) описывает исправления, восстановление и локальную передачу результата. Установщик не регистрирует службу, расписание или автоматическую публикацию. Для разрешённой GitHub-публикации нужен уже установленный и авторизованный GitHub CLI. FILE-публикация использует локальный bare repository без GitHub credentials. Точные ограничения и отдельный входной JSON описаны в [публикации](docs/PUBLICATION_RU.md); native credentials — в [контракте runtime](docs/NATIVE_RUNTIME_RU.md).

## Требования

- PowerShell 7 с машинной установкой `C:\Program Files\PowerShell\7\pwsh.exe`;
- Git;
- Node.js 20.19 или новее;
- OpenSpec CLI;
- Codex;
- OpenCode с подключённым provider для reviewer-модели.

Для полного регрессионного набора пакета дополнительно нужен .NET SDK 5 или новее: тесты компилируют маленький имитатор reviewer и не обращаются к платной модели. Для повседневной работы skills SDK не нужен. Проверки запускай через `scripts/Test-BSLFlowPackage.ps1`; они не запускают 1С и не заменяют приёмку в тестовой базе.

По умолчанию package suite работает offline: использует fake OpenCode provider, не проверяет реальные credentials/model catalog и не делает model calls. Проверки установленного OpenCode, доступных models и effective environment включаются отдельно:

```powershell
.\scripts\Test-BSLFlowPackage.ps1 -PackageRoot .
.\scripts\Test-BSLFlowPackage.ps1 -PackageRoot . -HostChecks
```

`-HostChecks` читает текущую host-конфигурацию и завершается с явной причиной, если CLI, установленная skill, provider или model недоступны. Он также не выполняет платный model call и не запускает 1С.

## Воспроизводимая сборка пакета

```powershell
.\scripts\Build-BSLFlowPackage.ps1 -PackageRoot .
.\scripts\Build-BSLFlowPackage.ps1 -PackageRoot . -Test
```

Build создаёт `outputs\BSL-Flow-0.8.0-dev.3.zip`, внешний файл `.sha256` и внутренний `package-manifest.json` с SHA-256 каждого файла. Пути архива сортируются, timestamps фиксируются; `.git`, `.bsl-flow`, `work` и `outputs` в пакет не входят. Повторная сборка тем же PowerShell runtime должна дать тот же SHA-256. `-Test` повторяет сборку, распаковывает точный ZIP во временный каталог, сверяет manifest и запускает offline package suite из распакованного artifact. Установка в глобальные каталоги при этом не выполняется.

Build entrypoint, установщик, task CLI и offline suite требуют PowerShell 7. Используется стандартная машинная установка `C:\Program Files\PowerShell\7\pwsh.exe`; fallback на Windows PowerShell 5.1 не предусмотрен.

Базовая проверенная комбинация: OpenSpec `1.11.0` и OpenCode `1.18.23`. Результаты текущей сборки — в [VERIFICATION.md](VERIFICATION.md).

Offline package suite требует Git и OpenSpec CLI в `PATH`: bootstrap-проверки вызывают настоящий OpenSpec даже без `-HostChecks`. CI устанавливает OpenSpec `1.11.0` до тестов; suite подготавливает схему из проверяемого пакета во временном каталоге и не зависит от её глобальной установки. Сами offline-проверки не вызывают модели или базу 1С; OpenCode требуется для дополнительных `-HostChecks`.

```powershell
git --version
node --version
openspec --version
opencode --version
```

Если OpenSpec ещё не установлен:

```powershell
npm install -g @fission-ai/openspec@1.11.0
```

Для default reviewer запусти `opencode`, выполни `/connect`, подключи DeepSeek и проверь:

```powershell
opencode models | Select-String deepseek-v4-pro
```

На момент сборки exact model ID: `deepseek/deepseek-v4-pro`. Установщик не делает платный model-run и не меняет credentials.

## Автоматическая установка

```powershell
Set-ExecutionPolicy -Scope Process Bypass
.\scripts\Install-BSLFlow.ps1
```

Установщик:

- проверит packaged OpenSpec schema;
- проверит эффективные права bounded read/file listing и sealed agents через `opencode debug agent`, включая запрет unrestricted grep;
- установит единственную копию семи skills в общий `%USERPROFILE%\.agents\skills` и после backup удалит управляемые дубликаты из `%CODEX_HOME%\skills`;
- установит глобальную schema `bsl-flow`;
- заменит старый managed bootstrap-блок новым bsl-flow-блоком и удалит после backup старую OpenSpec schema;
- создаст backup и выполнит rollback при ошибке;
- создаст пустой `~/.bsl-flow/evals/spec-runs.jsonl`, только если файла ещё нет;
- никогда не перезапишет накопленные metrics.

Предварительный просмотр:

```powershell
.\scripts\Install-BSLFlow.ps1 -WhatIf
```

После установки перезапусти Codex.

Для работы непосредственно из OpenCode установи отдельный адаптер по [инструкции](OPENCODE_SETUP_RU.md). Он использует ту же копию skills в `%USERPROFILE%\.agents\skills`, а в OpenCode-каталог добавляет только managed rules и manifest. Адаптер использует plan/`-Apply`, не меняет `opencode.json` и отдельно проверяет effective model routing.

Установка не настраивает тестовые базы, Unica, YaXUnit или Vanessa. Для этого используй отдельное [руководство по тестовому окружению](TEST_ENVIRONMENT_GUIDE_RU.md). Сначала подготовь разрешённую файловую копию; глобальное обновление skills не является разрешением на build/test или загрузку расширений.

### Каталоги тестовых инструментов

При первой настройке создай workstation profile. Без параметров каталогов проверяются `C:\YAxUnit` и `C:\vanessa-automation`. Для любого другого расположения передай абсолютные пути; они сохранятся вне Git в `%USERPROFILE%\.bsl-flow\workstation.json` и будут переиспользоваться проектами:

```powershell
& "$env:USERPROFILE\.agents\skills\1c-init-project\scripts\Enable-BSLFlowWorkstationProfile.ps1" `
  -DevelopmentDatabasePath "C:\BASES\DEMO\bp1" `
  -PlatformBin "C:\Program Files\1cv8\8.3.27.2074\bin" `
  -YaxunitDirectory "D:\1c-tools\YAxUnit" `
  -VanessaDirectory "D:\1c-tools\vanessa-automation"
```

Скрипт не ищет инструменты в других местах и не создаёт отсутствующие каталоги. Чтобы перенести общие инструменты, повторно выполни команду с новыми путями и хотя бы одной зарегистрированной базой. `Get-1CTestTooling.ps1` различает отсутствующий каталог и отсутствие подходящего файла; setup оставляет provider в `not_configured`. Несколько подходящих версий блокируют автоматический выбор — нужную поставку следует разложить в отдельный однозначный каталог.

Отсутствие YAxUnit или Vanessa не мешает установить BSL Flow. Оно мешает только доказать требования, для которых выбран соответствующий provider: такая проверка остаётся `BLOCKED`, пока точный локальный релиз не выбран и не проверен. Автоматического скачивания из интернета нет. Vanessa Automation запускается как EPF и не устанавливается расширением в каждую базу; загрузка YAxUnit CFE или необязательного `VAExtension` допустима только в явно разрешённую базу после read-only инвентаризации фактического состава и через поддержанный runtime-маршрут.

## Настройка проекта

Новые проекты получают review-блок автоматически. Основные значения:

```yaml
review:
  enabled: true
  routing:
    s_default: optional
    m_default: required
    l_default: required
    high_risk_override: required
  reviewer:
    provider: opencode
    agent: bsl-flow-spec-reviewer
    model: deepseek/deepseek-v4-pro
    variant: high
  permissions:
    project_read_mode: read_search
    edit: false
    shell: false
    subagents: false
    web: false
    external_directory: false
  runtime:
    timeout_seconds: 600
```

Для sealed review без чтения проекта установи `project_read_mode: attached_only`. В стандартном `read_search` reviewer может читать релевантные исходники, но не должен обходить всё дерево; служебные каталоги `.git`, `.bsl-flow` и бинарные артефакты закрыты permissions. Не пытайся включить запрещённые permissions: wrapper завершится ошибкой. Для другой модели меняй только `model` и при необходимости `variant`; silent fallback не выполняется. Таймаут 600 секунд выбран для `deepseek-v4-pro/high`; уменьшай его только после измеренного пилота выбранной модели.

Новые поля существующей секции `policy`:

```yaml
policy:
  test_selection: smallest_sufficient
  computer_use: justified_only
  test_database_mode: file_preferred
```

Это декларативные настройки для skills, не новый API runner-а и не исполняемый запрет tool calls. Если полей нет, действуют эти defaults из `1c-verify`. Не добавляй второй `policy:` поверх существующего: при обновлении объедини поля. Существующие `verification.*.enabled: false` означают неготовность provider-а, а не отказ от необходимых тестов. После фактической настройки укажи доступные проверки; реальные пути/секреты держи в локальном runtime-конфиге вне Git.

### Конфиг совета в профиле пользователя

Привязки «роль → модель», профили моделей и провайдеров совета можно хранить вне проекта — в файле профиля `%USERPROFILE%\.bsl-flow\config.yaml`. Файл не создаётся автоматически; его отсутствие — штатное состояние, поведение совета при этом не меняется. Приоритет конфигурации: **профиль → проект → локальный оверлей** (`.bsl-flow/providers.local.yaml` сохраняет высший приоритет для `token`/`base_url`).

Профиль может определять только провайдеров, профили моделей и привязки ролей:

```yaml
llm:
  providers:
    personal:
      protocol: openai_compatible
      base_url: https://api.example.com/v1
      token_env: MY_PERSONAL_TOKEN
  models:
    personal-high:
      provider: personal
      model: vendor/model-x
      effort: high
review:
  council:
    roles:
      chair:
        model: personal-high
```

Проектный `bsl-flow.yaml` переопределяет профиль по каждому именованному провайдеру, профилю модели и роли; сущности, заданные только в профиле, дополняют набор. Остальные ключи совета (`review.council.enabled`, `budget`, `review.reviewer.*`, `review.routing` и другие) в профиле запрещены: неизвестный ключ или литеральный токен отклоняются fail-closed ошибкой с именем файла и ключа, совет не стартует. Credential резолвится только через `token_env` провайдера или локальный оверлей. Для тестов и CI путь переопределяется переменной окружения `BSL_FLOW_USER_CONFIG` (полный путь к файлу; если переменная задана, а файла нет — ошибка).

## Ручной bootstrap проекта

```powershell
& "$env:USERPROFILE\.agents\skills\1c-init-project\scripts\Initialize-BSLFlowProject.ps1" `
  -ProjectPath "C:\PRJ\client\project" `
  -Explicit1CProject
```

## Ручная установка

1. Скопируй `global/skills/*` в общий `%USERPROFILE%\.agents\skills\`.
2. Скопируй schema в `%LOCALAPPDATA%\openspec\schemas\bsl-flow\`.
3. Добавь `global/AGENTS.bootstrap.md` в глобальный `%USERPROFILE%\.codex\AGENTS.md`.
4. Создай `%USERPROFILE%\.bsl-flow\evals\spec-runs.jsonl`, если его ещё нет.
5. Проверь schema и agents.

```powershell
openspec schema validate bsl-flow
$env:OPENCODE_CONFIG = "$env:USERPROFILE\.agents\skills\1c-spec-review\reviewer\opencode-reviewer.json"
$env:OPENCODE_DISABLE_PROJECT_CONFIG = "1"
opencode debug agent bsl-flow-spec-reviewer
opencode debug agent bsl-flow-spec-reviewer-sealed
```

После проверки закрой терминал или удали временные `OPENCODE_*` переменные из процесса.

## Ошибки внешнего review

Для обязательного route отсутствие OpenCode, credentials, модели, корректного JSON или допустимой политики данных является blocker. Framework не подменяет модель и не пропускает review автоматически. Невалидный output не записывается в `review.json`.

Project config может усилить routing, но не отключить обязательный review для M/L/high-risk. `review.enabled: false` допустим только там, где review и так необязателен; попытка обойти обязательный gate завершается ошибкой.

OpenCode `run` создаёт локальную session/log/cache даже у sealed agent. Read-only гарантирует отсутствие project/tool mutations, но не полное отсутствие локального служебного состояния OpenCode.

В OpenCode 1.18.23 глобальный `AGENTS.md` нельзя отключить отдельным флагом, поэтому считай его доверенной локальной границей и не помещай туда недоверенные инструкции. Изолированный reviewer config отключает project config и legacy Claude instructions, но не эту глобальную границу.
