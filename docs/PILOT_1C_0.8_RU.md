# Пилот BSLFlowPilot для ЯЮнит в БП 3

Этот документ описывает минимальный runtime-пилот для файловой базы `C:\BASES\DEMO\bp1`. Пользователь разрешил приведённый native Build/условный Test и дальнейшие тесты в согласованной базе. Временное ограничение durable Unica jobs сохраняется. Контракт подготовки ниже отделён от фактических результатов в разделе «Выполнение native-пилота»; preview и успешная сборка не заменяют результат тестов.

**Итог 2026-09-09: native unit-пилот PASS, 2/2 теста.** Build04 и Test03 выполнены после исправления конфликта UUID; исходные ошибки и обе попытки с нулём тестов сохранены. Это проверка локального native-маршрута и чистой функции, а не готовность публичного managed runtime-адаптера.

## Что подтверждено чтением

| Объект | Наблюдение | Граница вывода |
| --- | --- | --- |
| Цель | `C:\BASES\DEMO\bp1\1Cv8.1CD` существует | Это FILE-база. Не подтверждает пользователя, сеансы, состав расширений или готовность к тесту. |
| Платформа | Найдены `1cv8.exe` и `1cv8c.exe` в `C:\Program Files\1cv8\8.3.27.2074\bin` | Исходное наблюдение наличия файлов; успешные native-запуски зафиксированы ниже. |
| Текущий исходник | `C:\BASES\DEMO\bp1\_cfe_src` — Git-репозиторий расширения `ESImprovement_АвтовыставлениеСчетов` версии `1.0.0.18` | Это существующее прикладное расширение, не тестовый проект и не источник для нового пилота. |
| Локальный YAxUnit | `C:\YAxUnit\YAxUnit-25.12.cfe`, SHA-256 `805a2277c997a3c24be0b0d080696479e91e4a15ed7e27aaf3991a7346522d70` | Артефакт найден; его байты не приравниваются к записи расширения в базе. |
| Локальная Vanessa | EPF и VAExtension найдены в `C:\vanessa-automation` | Для этого пилота не нужны, пока не выбран UI-сценарий. |
| Runtime-конфигурация | Для отдельного fixture созданы `v8project.yaml` и ignored `v8project.local.yaml` без credentials | Dry-run выполнен; applied-аутентификация не проверена. |

Авторизованный read-only COM inventory от `2026-09-09T14:20:51.8385124Z` наблюдал девять расширений. `YAXUNIT` версии `25.12` активен: UUID `10c4124f-1645-11f1-9ec2-78465c3a941f`, native hash `96 B6 B8 06 B3 25 10 D8 55 F6 5A EA 32 ED E4 90 A0 DD B2 82`. Также активен `VAExtension` версии `1.29`. `BSLFlowPilot` в списке отсутствует. Следовательно, переустановка YAxUnit для пилота не требуется; новый CFE и его загрузка остаются отдельной мутацией. Inventory не доказывает совместимость, наличие сеансов или результат тестов.

Ранее сохранённый документ приёмки ESI относится к маю 2026 года и не является текущим доказательством состояния базы. Его нельзя использовать вместо inventory или receipt.

## Выбранная граница пилота

Подготовлен isolated fixture `work/pilot-1c-src/src/BSLFlowPilot` с расширением `BSLFlowPilot` версии `0.8.0`, режимом совместимости `Version8_3_24` и source-set `bsl-flow-pilot`. В нём два серверных общих модуля: `BFP_ПилотЛогики` содержит чистую экспортную функцию `УдвоитьНеотрицательное`, а `BFP_ТестыПилота` регистрирует два YAxUnit-сценария: `ПоложительноеЧислоУдваивается` и `ОтрицательноеЧислоВызываетИсключение`. Первый проверяет `2 -> 4`; второй проверяет ожидаемое исключение для `-1`. Тесты не читают и не записывают прикладные данные.

Имена модулей не получили суффикс `Сервер`: для server context он разрешён только при конфликте имён. Первоначальная диагностика `BSLLS:CommonModuleInvalidType` была вызвана не именем, а неполной комбинацией контекстов, сгенерированной scaffold: `ExternalConnection=false` и `ClientOrdinaryApplication=false`. Оба свойства приведены к server combination (`Server`, `ExternalConnection`, `ClientOrdinaryApplication` включены; `ServerCall` выключен). Публичный `unica.meta.edit` принимает `ExternalConnection`, но отклоняет `ClientOrdinaryApplication` как неизвестное writable property, хотя `unica.meta.info` его читает; поэтому последнее точечное XML-изменение выполнено напрямую.

Первоначальный `unica.cfe.validate` после этой правки завершился без ошибок и предупреждений (`14 checks`). `unica.code.diagnostics` больше не выдаёт `CommonModuleInvalidType`, но сохраняет три `UnresolvedMethodCall` в `CommonModule.BFP_ТестыПилота.Module`: один для `ЮТТесты` и два для `ЮТест`. Source-set fixture не содержит исходников установленного YAxUnit. Структурная проверка поэтому учитывается отдельно от BSL-диагностики и проверки поведения в совместном runtime-сеансе; фактические результаты запусков приведены ниже.

Первоначальный `unica.cfe.init` создал `Configuration.xml` с корневым UUID `00000000-0000-0000-0000-000000000014` и внутренними `ContainedObject.ObjectId` от `...0017` до `...001D`. Read-only helper подтвердил уникальность четырёх атрибутов `uuid` объектов, но не проверил внутренние идентификаторы и их конфликты в базе. Этот пробел проявился в runtime и исправлен описанной ниже UUID-коррекцией. Список расширений также не раскрывает все внутренние UUID.

Регистрация через `ИсполняемыеСценарии`, `ЮТТесты.ДобавитьТестовыйНабор`, `ДобавитьСерверныйТест` и assertion `ЮТест.ОжидаетЧто(...).Равно(...)` сверены с первичными исходниками YAxUnit. Отрицательный сценарий использует прямой вызов функции и проверку исключения; его исправление описано ниже. Установка активного `YAXUNIT 25.12` подтверждена inventory, выполнение набора подтверждается только результатом конкретной попытки.

Успех пилота означает только следующее:

1. выбранная тестовая база приняла ровно один известный тестовый набор;
2. раннер вернул свежий terminal receipt и JUnit-отчёт с согласованными счётчиками;
3. идентичность источника, расширений и цели сохранена вместе с попыткой.

Он не доказывает поведение `ESImprovement_АвтовыставлениеСчетов`, интерфейс, проведение документов, интеграции или пригодность Vanessa/TestClient.

## Read-only inventory до создания или загрузки чего-либо

Первый авторизованный read-only сеанс внешнего соединения с этой FILE-базой уже выполнен. Повтор того же диагностического маршрута к строго нормализованному `C:\BASES\DEMO\bp1` разрешён, пока скрипт сохраняет контракт только чтения: не добавляет, не удаляет, не активирует и не сохраняет расширения. Публичного метода Unica для списка расширений в доступной схеме нет: `operation=extensions` изменяет свойства и не является inventory API.

Платформенная справка подтверждает, что `РасширенияКонфигурации.Получить([Отбор], [Источник])` возвращает массив `РасширениеКонфигурации`, по умолчанию из `БазаДанных`, и доступен на сервере, в толстом клиенте и внешнем соединении (с версии 8.3.6). Для каждого элемента допускается **только чтение** нужного минимального набора: `Имя`, `Версия`, `Активно`, `УникальныйИдентификатор`, `ХешСумма`, `Назначение`, `ОбластьДействия`; справка объекта также перечисляет `БезопасныйРежим` и `ЗащитаОтОпасныхДействий`, но пилот не должен их менять.

`РасширенияКонфигурации.ПроверитьВозможностьПримененияВсех()` пока не вызывался. Платформенная справка определяет результат как массив проблем применимости в текущей области данных и допускает вызов во внешнем соединении (с версии 8.3.9). Если этот отдельный read-only diagnostic будет выбран, его результат сохраняют как наблюдение; пустой массив не заменяет список расширений и не подтверждает YAxUnit.

Локальный скрипт `work/pilot-1c-src/Read-BP1ExtensionInventory.ps1` выполнен по разрешённому COM-маршруту: он требует `PSCredential`, перед соединением сверяет зарегистрированный CLSID `V83.COMConnector` с `C:\Program Files\1cv8\8.3.27.2074\bin\comcntr.dll`, допускает только нормализованный путь `C:\BASES\DEMO\bp1`, читает только список и свойства расширений, а затем освобождает COM-объекты. Пароль поступал через скрытый stdin, не передавался в command line и не выводился. При ошибке скрипт возвращает только sanitised diagnostic и фазу без секретов. Локальные fixture и скрипт находятся в ignored `work/` и не входят в публичный пакет. Этот COM-допуск покрывает только чтение. Позднее пользователь отдельно разрешил точный native Build/Test; mutating `operation=extensions` не применялся.

Inventory списка и свойств уже получен. Для полной оценки готовности базы дополнительно нужны:

- отдельная проверка применимости, если она необходима для выбранной сборки;
- подтверждение совместимости пилота с установленным YAxUnit;
- проверка конфликтующего сеанса/сборки либо последовательное владение базой. Lock пилотного скрипта исключает только конкуренцию его экземпляров, а не сторонний Конфигуратор.

Пароли и строки подключения с учётными данными в inventory, Git и runtime evidence не записываются. Нормализованный FILE target без учётных данных сохраняется: он нужен для проверки выбранной базы.

## Предпосылки отдельного проекта

При переносе исследовательского fixture в обычный проект создаётся отдельный Git-корень вне каталога файловой базы и выполняется bootstrap BSL Flow: собственные `AGENTS.md`, `bsl-flow.yaml`, `.bsl-flow/project.yaml`, `openspec/config.yaml` и отдельный `src/` для расширения. Корень `C:\BASES\DEMO\bp1` не является проектным корнем; локальный fixture пока не выдаётся за готовый проект для поставки.

Подготовлен isolated fixture `work/pilot-1c-src`: его основной `v8project.yaml` задаёт source-set `bsl-flow-pilot` типа `EXTENSION` в `src/BSLFlowPilot`, `DESIGNER` и безопасную фиктивную FILE-цель `build/ib`. Этот файл служит только статическому source contract и не указывает на `bp1`. Локальные значения реальной `infobase`, `workPath`, `tools` и `tests` живут только в ignored `v8project.local.yaml`. Для этой машины локальная конфигурация закрепляет одну платформу `8.3.27.2074`, путь к ней и строгую проверку платформы.

Pinned `v8-runner 0.5.1` передаёт runtime auth из `infobase.user` и `infobase.password` local overlay в platform `/N` и `/P`. README описывает `--user`/`--password` только для `bootstrap`, который записывает их в local YAML; для обычных `build` и `test` CLI/env credential override в source/help не найден. Значит текущий credential-free overlay достаточен для dry-run admission, но не доказывает аутентифицированную applied build/test и не должен получать пароль автоматически.

До applied-шага сохраняют source manifest с хешами файлов `BSLFlowPilot`, Git revision и manifest идентичности расширений. Если установленной совместимой CFE нет, следующий шаг — отдельное точное разрешение на загрузку CFE; он не включён в разрешение на тест.

## Публичные Unica операции

Ниже указаны формы аргументов, которые реально присутствуют в доступной схеме `unica.runtime.execute`. Угловые скобки означают значение, которое ещё должно быть наблюдено или утверждено; это не готовые команды для копирования. Поле `config` всегда указывает основной `v8project.yaml`, а не `v8project.local.yaml`.

### 1. Проверка admission без эффектов

После появления проекта и точного module name первый запрос должен быть только preview:

```json
{
  "cwd": "<абсолютный корень BSLFlowPilot>",
  "config": "v8project.yaml",
  "operation": "test",
  "testRunner": "yaxunit",
  "testScope": "module",
  "module": "<подтверждённый тестовый модуль>",
  "dryRun": true
}
```

Это проверяет нормализацию публичного маршрута, но не запускает тест. Raw preview сохраняется отдельно. Его `effective`-часть должна подтверждать цель, source-set, test selection, версии, путь отчёта и capabilities, если эти значения вообще выдаёт provider. Если provider не выдаёт эти данные в структурированном виде, preflight остаётся `BLOCKED`; желаемые значения из запроса не копируются как наблюдённые.

### 2. Сборка исходников в базу

Сборка изменяет конфигурацию базы и имеет непрерываемую фазу. Её нельзя считать частью предварительной проверки и нельзя запускать до отдельного точного разрешения:

```json
{
  "cwd": "<абсолютный корень BSLFlowPilot>",
  "config": "v8project.yaml",
  "operation": "build",
  "sourceSet": "<подтверждённое имя source-set BSLFlowPilot>",
  "dryRun": false
}
```

Сначала должен быть идентичный запрос с `dryRun: true`. Схема поддерживает `fullRebuild`, но пилот не задаёт его заранее: это не настройка по умолчанию и не замена анализа неудачи.

### 3. Загрузка CFE только при отдельной необходимости

Установка YAxUnit или другого CFE — самостоятельная мутация. Публичная схема поддерживает только CF/CFE путь через `load`; EPF/ERF этим маршрутом не загружаются.

```json
{
  "cwd": "<абсолютный корень BSLFlowPilot>",
  "config": "v8project.yaml",
  "operation": "load",
  "mode": "load",
  "path": "<относительный путь к проверенному CFE>",
  "extension": "<проверенное внутреннее имя расширения>",
  "dryRun": false
}
```

Этот шаг допускается только после preview, inventory и отдельного разрешения на конкретный артефакт. Имя из файла CFE не является внутренним именем. После applied load требуется повторный read-only inventory; отсутствие ошибки транспорта не доказывает установку.

### 4. Один тестовый запуск

После успешного preview и необходимой, подтверждённой композиции базы applied-тест имеет ту же форму, что preview, но с `dryRun: false`. Публичная документация Unica указывает, что тест сначала выполняет build. Поэтому `testScope: module` ограничивает тесты, но не является доказательством `noBuild`.

У `unica.runtime.execute` нет отдельного аргумента `reportPath` или `junitPath`, но это не означает неопределённый output. Пиннутый `v8-runner 0.5.1` сам генерирует конфигурацию YAxUnit с `reportFormat: "jUnit"` и `reportPath` в новом каталоге запуска: `<workPath>/temp/yaxunit/runs/<utc-millis>-<pid>-<uuid>/report.xml`. При текущем primary `workPath: build` это путь под `build/temp/yaxunit/runs/.../report.xml`. UUID в имени каждого run directory исключает повторное использование старого отчёта; runner также создаёт `run.inprogress`, `config.json`, `runner.log` и `enterprise.out.log`.

Однако успешный `v8-runner` после разбора JUnit удаляет весь run directory, включая `report.xml`. При неуспехе он сохраняет каталог и публикует пути `run_dir`, `config_json`, `junit_xml`, `yaxunit_log`, `platform_log` и `sentinel` в terminal result. Поэтому raw JUnit можно зафиксировать после failed attempt, но не после successful attempt через текущий public route. Успешный terminal result несёт распарсенные summary/cases/metrics, но не durable raw XML. Поиск по pinned source, CLI/schema и docs не нашёл supported `keep-artifacts` или `report-export`. Поэтому отсутствие raw XML оставляет результат `BLOCKED`: parsed receipt сам по себе не даёт PASS.

### 5. Отдельный native-маршрут с durable raw JUnit

Для пилота отдельно разрешены native source-set build и `1cv8.exe ENTERPRISE` в `C:\BASES\DEMO\bp1` с сохранением оригинального JUnit. Подготовленный, но не запускавшийся preview `work/pilot-1c-src/Preview-NativeYAxUnitRun.ps1` требует `PSCredential` и `attempt_id`, принимает только этот FILE target и pinned `C:\Program Files\1cv8\8.3.27.2074\bin\1cv8.exe`, не создаёт файлов и не запускает процесс. Его JSON показывает точный YAxUnit payload и redacted argv.

Payload воспроизводит сериализацию `v8-runner 0.5.1`: `filter.modules=["BFP_ТестыПилота"]`, `reportFormat="jUnit"`, выделенный immutable `reportPath`, `closeAfterTests=true`, `showReport=false`, `logging.file`, `logging.console=false`, `logging.level="info"`. Source runner передаёт этот файл двумя отдельными argv `/C`, `RunUnitTests=<config-path-со-slash>`; `EnterpriseDsl` также формирует `ENTERPRISE`, `/DisableStartupDialogs`, соединение, `/C` и `/Out` отдельными аргументами. Preview для новой native операции фиксирует `/F C:\BASES\DEMO\bp1`, `/N <redacted>`, `/P <redacted>`, `/C RunUnitTests=<config-path>` и `/Out <platform-log>`. Значения credential нигде не сериализуются и не выводятся. При applied запуске платформа всё же получает пароль как process argument; это отдельный локальный риск, который должен входить в точное разрешение.

В отличие от public runner default thin-client, этот маршрут закрепляет разрешённый `1cv8.exe`. Payload подтверждён исходником runner, но native controller ещё не является exported public provider API. Полученное разрешение называет executable, target, предшествующую source-set build и требования evidence. Module filter закреплён как `BFP_ТестыПилота`; точные два JUnit ID подтверждены по исходнику YAxUnit 25.12 и приведены ниже. Контроллер сохраняет `report.xml`, `runner.log`, `enterprise.out.log` и immutable receipt. Для последующей приёмки нужны terminal exit `0`, свежий непустой XML, total `2`, passed `2`, failed/errors/skipped `0` и совпадение IDs. Timeout или неполное evidence не допускают автоматического повтора. В отличие от termination policy runner, локальный wrapper не завершает процесс по timeout: сохраняет неизвестное состояние для проверки.

Для предшествующей native source-set build pinned runner использует две отдельные `1cv8.exe DESIGNER` операции, обе помечены в source как `CriticalNonAbortable`: сначала `DESIGNER /DisableStartupDialogs /DisableStartupMessages /F <bp1> /N <redacted> /P <redacted> /Out <load-log> -NoTruncate /LoadConfigFromFiles <fixture-source-root> -updateConfigDumpInfo -Extension <verified-extension-name>`, затем тот же base argv с `/UpdateDBCfg -Extension <verified-extension-name>`. Эти две операции входят в полученное точное разрешение и выполнены пилотным wrapper. Credential остаётся только в переданном `PSCredential` контроллера и не пишется в YAML, preview или logs; платформа при этом получит `/P` в argv, что пользователь явно принял при разрешении.

Локальный `work/pilot-1c-src/Invoke-BP1NativePilot.ps1` подготовлен как applied-capable controller только для PS7. Его default preview не требует credential, не создаёт файлов и не запускает native process. `-Execute` требует `PSCredential` и точный `ExpectedPreviewSha256`: preview связывает argv/config с SHA самого скрипта, storage helper, executable, source manifest и свежего inventory. Канонический source manifest `{path, sha256}` использует относительные пути с `/`, ordinal order и исключает generated configdump. Хеш первоначального fixture — `3ab24914bb9c6ccf223dc4778321cebe1c7acd4d7034e1c9b8c7a1a0ec9bc6dc`; после исправления связи языка — `54f1acc70162c55346d2d7bc25392ac74709b448830cf6a8fd62ba441b8f3c6e`.

Скрипт использует OS writer lock и уникальный attempt ID, сохраняет prepared receipt до старта процесса, затем PID/start time и terminal receipt. Любая незавершённая, повреждённая, неуспешная или уже использованная попытка блокирует дальнейший запуск. Перед допуском проверяются связанные receipts и raw native logs: ожидаемый путь, наличие и SHA-256. Build сохраняет проверенную копию source и после load exit `0` допускает только отдельный update; его финал — `build_completed_pending_inventory`. Test требует inventory после Build, активного `BSLFlowPilot 0.8.0`, неизменности остальных расширений и исходников/executable; не строит и не загружает. Даже exit `0` оставляет Test в `completed_pending_evidence_review` и требует независимой проверки evidence. Timeout каждого шага — 300 секунд; процесс не завершается принудительно и операция не повторяется.

62 offline-проверки wrapper прошли, включая аргументы запуска, незавершённые попытки, удалённые/изменённые журналы и ограниченные reconciliation после ошибки привязки языка и нулевого набора тестов. Native/COM запусков в этих проверках — ноль. Независимый reviewer подтвердил допуск. Wrapper остаётся локальным пилотным скриптом и не является принятым публичным runtime-адаптером.

## Receipt, результат и восстановление

До запуска создаётся request/expected manifest с уникальным `attempt_id`, точным FILE target, source manifest, выбранным тестом, ожидаемым числом тестов, версиями платформы и расширений, а также ожидаемым report path. После terminal результата:

1. сохраняются без изменения raw response provider и свежий JUnit;
2. `Save-1CTestResult.ps1` получает receipt, JUnit, expected manifest, уникальный run ID, `history` и `current` в ignored `.bsl-flow/reports/tests`;
3. helper копирует raw-файлы без перезаписи, пишет неизменяемую `history/<run-id>.json` и пересобирает `current.json` по времени фактического завершения;
4. PASS возможен только при совпадении цели, источников, версий, selection, ожидаемого total и JUnit/receipt counts без failed, errors или skipped.

`Save-1CTestResult.ps1` сопоставляет case ID как `classname.name`, если `classname` присутствует, иначе как `name`. Исходники YAxUnit **25.12**, commit `15f7ae557d17b59bd80daad503efd8a3114690e5`, подтверждают: `classname` равен `module.method`, а `name` без пользовательского представления и параметров равен имени метода. Для текущего fixture ожидаются ровно два ID:

- `BFP_ТестыПилота.ПоложительноеЧислоУдваивается.ПоложительноеЧислоУдваивается`
- `BFP_ТестыПилота.ОтрицательноеЧислоВызываетИсключение.ОтрицательноеЧислоВызываетИсключение`

Повтор имени метода намеренный. Это проверенное по исходнику ожидание, а не уже полученный runtime-результат: фактический XML обязан совпасть с ним. Правило задают [JUnit writer](https://github.com/bia-technologies/yaxunit/blob/15f7ae557d17b59bd80daad503efd8a3114690e5/exts/yaxunit/src/CommonModules/ЮТОтчетJUnitСлужебный/Module.bsl#L97-L105) и [фабрика тестов](https://github.com/bia-technologies/yaxunit/blob/15f7ae557d17b59bd80daad503efd8a3114690e5/exts/yaxunit/src/CommonModules/ЮТФабрикаСлужебный/Module.bsl#L231-L242). Нормализованный native receipt должен сохранять исходный receipt рядом и опираться на реальные observations, а не копировать request как evidence.

Потерянный terminal receipt, обрыв вызова, неполный JUnit, неизвестное состояние базы или ошибка после завершившейся записи — не повод повторять build/load/test. Записывается один из состояний `unknown`, `not_applied`, `applied_followup_failed` или `business_record_written_verification_incomplete` по реальным данным попытки. Следующий шаг — read-only inspection базы и сохранённых материалов; автоматические retry, reload и бизнес-запись запрещены.

Durable jobs обычно являются маршрутом для долгих операций, но до снятия временного ограничения `unica.runtime.job.start` и `unica.runtime.job.cancel` не используются. Наличие `status`, `wait` и `logs` не создаёт право запускать job. Синхронный `runtime.execute` при потере receipt не имеет допустимого fallback-маршрута.

## Решение перед запуском

### Фактическая попытка инвентаризации 2026-09-09

Пользователь разрешил один COM-вызов только для чтения списка/свойств расширений `bp1`. Вызов выполнен через проверенный COM DLL 8.3.27.2074; пароль вводился через скрытый stdin и не записывался в командную строку или файлы. Метод `Connect` вернул соединение, затем скрипт остановился в фазе `read_extensions`. Список расширений не получен. Локальный receipt: `work/pilot-1c-src/inventory-bp1-attempt1.json`. Он имеет `state: blocked`, не является доказательством готовности YAxUnit и не должен удаляться при повторе.

Для диагностических повторов добавлены отдельные фазы чтения, безопасные тип/HResult/номер строки ошибки, обработка native array и явное преобразование Generic List. Последующие read-only повторы этого же строго ограниченного COM inventory разрешены; build/load/test получили отдельный native-допуск позднее. Первые три неполные попытки сохранены. Успешный `inventory-bp1-attempt4.json` от `2026-09-09T14:20:51.8385124Z` содержит все девять расширений; SHA-256 — `662d213fbb3146a208b4bdc50d9115ecdcc2453a5529ff699cf121278083db62`. Native wrapper требует inventory не старше 60 минут и новый inventory после сборки.

Перед первым native-запуском были подготовлены следующие сведения для точного разрешения:

| Условие | Требуемое доказательство |
| --- | --- |
| Источник | Отдельный BSLFlowPilot, source-set, Git revision и source manifest |
| Цель | `C:\BASES\DEMO\bp1`, актуальное время inventory и отсутствие конфликта владельцев |
| Тестовый движок | Внутреннее имя, версия, UUID/хеш, active-состояние и совместимость YAxUnit |
| Маршрут | Raw dry-run с согласованным `effective` либо явный BLOCKED provider gap |
| Набор тестов | Один фактический module/test name и ожидаемый total |
| Evidence | Выделенные immutable attempt ID, raw terminal receipt и свежий durable JUnit XML |
| Риск | Отдельно согласованы build/test и, при необходимости, load CFE; временное ограничение Unica снято либо для точной операции есть явное исключение |

Эта таблица описывает допуск к выполнению. Текущий результат тестов определяется сохранёнными попытками ниже.

## Captured dry-run для bp1

В local fixture добавлен только документированный overlay `v8project.local.yaml`: `infobase.connection` равен `File="C:\BASES\DEMO\bp1"`, а `tools.platform` закреплён на `8.3.27.2074`, `C:\Program Files\1cv8\8.3.27.2074\bin`, `strict: true`. В нём нет credentials. Это точная схема локального overlay из установленной справки Unica; `source-set`, `format`, `builder` и `execution_timeout` остаются в основном `v8project.yaml`.

Выполнены только два публичных `unica.runtime.execute` с `dryRun: true`; raw JSON сохранён локально в ignored fixture, не является частью публичного пакета:

| Preview | Typed arguments | Наблюдённая команда | Вывод |
| --- | --- | --- | --- |
| Build | `operation=build`, `sourceSet=bsl-flow-pilot`, `dryRun=true` | `v8-runner --json-message --config <fixture>\\v8project.yaml build --source-set bsl-flow-pilot` | Provider сообщил `no files changed because dryRun is true`. Applied build изменит конфигурацию базы и содержит непрерываемую фазу. |
| YAxUnit module | `operation=test`, `testRunner=yaxunit`, `testScope=module`, `module=BFP_ТестыПилота`, `dryRun=true` | `v8-runner --config v8project.yaml test yaxunit module BFP_ТестыПилота` | Provider сообщил `no files changed because dryRun is true`. Документация runner указывает, что applied test сначала выполняет build, следовательно этот запуск тоже потенциально меняет базу. |

Provider не включил в raw preview effective connection, имя целевой базы, отчётный путь, JUnit path или terminal receipt schema. В видимой команде нет local overlay. Однако фактический контракт `v8-runner 0.5.1` загружает `v8project.local.yaml` автоматически рядом с primary config и допускает в нём local `infobase`, `tools`, `tests` и `mcp`; source topology менять там нельзя. Поэтому локальный проверяемый preflight может доказать admission без обращения к базе: фиксирует SHA-256 primary config и overlay, разбирает `infobase.connection` как FILE path, нормализует её и сравнивает с `inventory-bp1.json.target`, а также сверяет `tools.platform` с inventory COM platform. Для текущего fixture эти значения сходятся на `C:\BASES\DEMO\bp1` и `8.3.27.2074`. Это доказательство конфигурационной привязки будущего запроса, а не evidence выполненной операции в базе.

`v8-runner 0.5.1 test yaxunit module --help` подтверждает фактический argv `test yaxunit module <NAME>`, а также флаг `--no-build`: напрямую запущенный runner мог бы тестировать уже подготовленную базу без build. Публичная typed schema `unica.runtime.execute` не предоставляет `noBuild`; поэтому допустимый public test route выполняет build первым. В fixture один source-set, `bsl-flow-pilot`, но module filter ограничивает только выбор тестов и не является ограничителем build scope.

В публичной typed schema нет аргумента конкретного test name: она допускает только module filter. Имена `ПоложительноеЧислоУдваивается` и `ОтрицательноеЧислоВызываетИсключение` известны из исходника, но preview не доказывает их discovery runner-ом и не даёт expected total. `tests.yaxunit` в runner 0.5.1 поддерживает только `timeouts`; `reportPath` туда не добавляется. Нельзя выдумывать иной `tests` key.

Точная будущая public операция после отдельного разрешения остаётся `unica.runtime.execute` с `operation=test`, `testRunner=yaxunit`, `testScope=module`, `module=BFP_ТестыПилота`, `dryRun=false`, основным config `v8project.yaml` и неизменным sibling overlay. CLI-only `--no-build` для подготовленной базы не имеет typed MCP аналога. Admission до запуска: SHA primary config/overlay и source tree, нормализованный FILE target против свежего inventory, strict platform pin и raw preview. Receipt acceptance по public route всё равно остаётся `BLOCKED`, пока после success не будет durable raw JUnit XML; parsed provider receipt сохраняется неизменно, но не заменяет XML. Отдельный native route выше получил разрешение и сохраняет raw XML; он не устраняет ограничение public runner и не является публичным managed-адаптером.

## Выполнение native-пилота

Первая попытка `bp1-native-build-20260909-01` загрузила исходники (exit `0`), но `/UpdateDBCfg` завершился `101`: `Язык.Русский.ОбъектРасширяемойКонфигурации` не совпал с базовой конфигурацией. Причина — нулевой UUID в scaffold. Post-failure COM inventory подтвердил новый пустой `BSLFlowPilot`: active `true`, версия пустая, native hash нулевой, instance UUID `879d4a2e-ac60-11f1-9ef1-78465c3a941f`. Все семь наблюдавшихся свойств остальных девяти расширений остались прежними. Тест в этой попытке не запускался.

В исходнике изменена только ссылка `Languages/Русский.xml`: нулевой `ExtendedConfigurationObject` заменён на `db4a9ccb-9ef5-4b3c-8577-b6fe5db1b62e` из уже имеющейся выгрузки ESI этой базы. Это первоначально был кандидат по сохранённой связи, а не новый COM-факт; его соответствие затем проверила платформа при успешном update. Дополнительный native partial dump не выполнялся.

Восстановление оформлено отдельным immutable reconciliation: SHA-256 двенадцати файлов первой попытки, точные load/update exit codes, ошибка, завершённые PID, post-failure inventory и ровно одна замена XML-ссылки. Старые файлы не изменялись. Только после проверки этих условий разрешён correction Build `bp1-native-build-20260909-02`. Его load и update завершились exit `0`, последний — `2026-09-09T15:24:06.6101441Z`. Новый inventory подтвердил активный `BSLFlowPilot 0.8.0`, тот же instance UUID и native hash `6F 89 8A 8C 26 B3 5C 86 92 D3 23 0A 3C 01 F0 EB 54 FC 99 10`; остальные расширения не изменились.

Test `bp1-native-test-20260909-01` завершился с exit `0`, но JUnit содержал ноль testcase вместо двух. Журнал YAxUnit подтвердил ноль загруженных сценариев. Попытка сохранена через `Save-1CTestResult.ps1` как `BLOCKED`; успешный exit code не превратился в PASS.

После анализа исходников YAxUnit исправлен отрицательный тест: прямой вызов проверяемой функции внутри `Попытка/Исключение`, повторное возбуждение неожидаемой ошибки и проверка флага после catch. Регистрация и положительный тест сохранены. Это исправляет конструкцию fixture, но гипотеза о причине нулевого discovery не подтверждена: повторный Test02 также обнаружил ноль тестов.

Отдельный immutable reconciliation связал предыдущие Build/Test receipts, исходный JUnit и изменение единственного BSL-файла. Build `bp1-native-build-20260909-03` выполнил load/update с exit `0`, завершился `2026-09-09T15:48:20.5671721Z`. Source manifest: `8315ac22c44bbaa5207438c2b24c5a2fbab0145abddf1838668789bc1e347f06`. Новый inventory подтвердил прежний instance UUID, версию `0.8.0`, native hash `D0 BF 49 A7 3A 1C BD A8 0C 7B 57 25 7C 99 D4 02 DF 07 55 06` и неизменность семи наблюдавшихся свойств остальных девяти расширений.

Test `bp1-native-test-20260909-02` завершился с exit `0`, сохранил оригинальный JUnit без testcase и также записан `BLOCKED`. `evidence/tests/current.json` выводится из двух сохранённых попыток; `attempts_count=2`, `invalid_history_count=0`. Проверка поведения функции не выполнена. Исходники YAxUnit 25.12 показывают, что discovery перехватывает ошибки динамического вызова и признаёт метод существующим лишь по ошибке лишних параметров; сам журнал не раскрывает причину пропуска модуля. Следующим диагностическим шагом стала штатная проверка компиляции установленного расширения, без очередной загрузки по гипотезе.

Штатный `/CheckConfig -Server -ThickClientManagedApplication -Extension BSLFlowPilot` завершился `101`: по три сообщения о неопределённых `ЮТТесты`/`ЮТест` в каждом контексте. Журнал сохранён с SHA-256 `002a721183af90f55e204254dc99e55711fa1c79f030c390f91259bac2d40b3d`. Проверка одного расширения сама по себе не доказывает недоступность API соседнего YAxUnit в совместном runtime-сеансе. Синтаксис команды подтверждён [документацией платформы](https://kb.1ci.com/1C_Enterprise_Platform/Guides/Administrator_Guides/1C_Enterprise_8.3.22_Administrator_Guide/Appendix_7._The_parameters_of_the_command_line_to_launch_1C_Enterprise/7.4._Running_Designer_in_batch_mode/7.4.5._Checking_configurations_and_extensions/).

Затем чтение файла журнала регистрации выявило реальный отказ применения: `Конфликт внутренних идентификаторов у объекта BSLFlowPilot`. Пять записей сохранены отдельно; события `18:26:01` и `18:50:11` попадают внутрь Test01 и Test02. Наблюдение SHA-256 `0cb3dc94e716d8062472b410e5636382be4ed65f01640721574ed15310585b74`. Таким образом, `active=true` в списке расширений не доказывает применение расширения в сеансе. Первоначальная структурная проверка четырёх UUID не охватила внутренние идентификаторы и их совместимость с базой.

В текущих исходниках девять шаблонных UUID заменены на новые GUID: корень, семь внутренних `ObjectId` и собственный UUID языка. `ClassId`, привязка языка к БП, BSL, UUID общих модулей и параметры безопасности сохранены. Исходный UTF-8 BOM также сохранён. Новый source manifest — `461b1f5c4ea8bd42af2187b205c3f2ad989665407ebe7018492696d062877861`; immutable reconciliation — `0f5e0302bd9943ea7f51eb77e3676576263e38c9911d2893125807694d0e2d74`. Конкретный конфликтующий UUID журнал не называет; результат исправления проверен отдельными Build04/Test03. Предыдущие неуспешные попытки не удаляются.

UUID-коррекция выполнена отдельным ограниченным wrapper: 20 offline-проверок и независимое review, один Build04 и один Test03, проверка всех семи наблюдаемых свойств расширений, запрет повторов после неопределённого результата. Исходный wrapper и его receipts сохранены. Build04 завершил load/update с exit `0` в `2026-09-09T16:13:07.8064620Z`; result SHA-256 `6dedf2bc753db4842de886390a63f12cc2cf075f7c1ca77915707f6295833200`. Post-build inventory сохранил instance UUID `879d4a2e-ac60-11f1-9ef1-78465c3a941f` и подтвердил native hash `88 81 64 FD 76 FA E5 88 B3 AB 2D 54 8B 00 99 18 55 EE 3F 1C`. Остальные девять расширений сохранили все семь наблюдавшихся свойств.

Test03 завершён `2026-09-09T16:15:16.0953782Z`, exit `0`. Исходный JUnit содержит ровно два ожидаемых case ID, `passed=2`, `failed=errors=skipped=0`; SHA-256 `7aead1b4a8065ddb750235b88442b1a36e1a24086dadcc7a0c9691b3246899c3`. Журнал подтверждает запуск и завершение обоих методов в `BSLFlowPilot.BFP_ТестыПилота`. Локальная нормализация и `Save-1CTestResult.ps1` приняли результат как PASS; `current.json` содержит `attempts_count=3`, `invalid_history_count=0`. Test01/Test02 остались BLOCKED в неизменённой истории. Повторного теста ради исправления receipt не потребовалось.

Проверена БП `3.0.191.38`, платформа `8.3.27.2074`, YAxUnit `25.12`, толстый управляемый клиент FILE-базы. Доказано поведение удвоения положительного числа и отказа для отрицательного. Проведение, обмен, права, UI, серверная ИБ и recovery после прерывания записи этим набором не проверялись. Публичный managed adapter ещё предстоит реализовать по полученным контрактам.

Нормализатор evidence сохраняет native receipts рядом и явно указывает, что counts получены из исходного JUnit. Он проверяет не только testcase failures, но и ошибки на уровне suite/root, disabled/skipped и согласованность aggregate counts. Время производного receipt соответствует наблюдённому native completion; время нормализации сохранено отдельно, исходные файлы не меняются. Это локальная нормализация, не ответ публичного Unica API и не свидетельство принятия managed runtime-адаптера.

## Источники, использованные для контракта

- Public tool schema активного `mcp__unica__unica_runtime_execute`.
- `C:\Users\ifokusov\.codex\plugins\cache\unica\unica\0.12.3\skills\v8-runner\SKILL.md` и `references/testing.md`, `config-and-backends.md`.
- Read-only CLI help фактически установленного `C:\Users\ifokusov\.codex\unica\runtimes\0.12.3\win-x64\bin\win-x64\v8-runner.exe` (`v8-runner 0.5.1`, `test yaxunit module --help`, `build --help`) и `references\tooling\v8project.md`.
- Пиннутый manifest runtime: `v8-runner` SHA-256 `191a3d7c930007377238dda0543d1e42cc1a1bd4b209736d54fd41c0ffaac32e`, upstream `alkoleft/v8-runner-rust` commit [`7ce1b062843d86644fe55741dbe0ee79f7ca767d`](https://github.com/alkoleft/v8-runner-rust/tree/7ce1b062843d86644fe55741dbe0ee79f7ca767d); primary checkout `C:\Users\ifokusov\AppData\Local\Temp\v8-runner-rust-7ce1b06`, HEAD этого commit. Проверены `src/use_cases/run_tests.rs`, `src/use_cases/run_tests/coordinator.rs`, `src/platform/enterprise.rs`, `src/platform/connection.rs`, schemas и snapshots CLI test; supported keep/export отсутствует в этих местах.
- `C:\DEV\BSL Flow\global\skills\1c-verify\references\test-evidence.md` и `scripts\Save-1CTestResult.ps1`.
- Platform syntax help: `МенеджерРасширенийКонфигурации.Получить`, `РасширениеКонфигурации`, `МенеджерРасширенийКонфигурации.ПроверитьВозможностьПримененияВсех`.
- Первичная документация YAxUnit: [регистрация тестов](https://github.com/bia-technologies/yaxunit/blob/develop/documentation/docs/features/test-registration.md) и [утверждения](https://github.com/bia-technologies/yaxunit/blob/develop/documentation/docs/features/assertions/assertions.md).
