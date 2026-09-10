# Native runtime адаптер 1С

Этот документ описывает реализованный контракт первого native-адаптера BSL Flow. Он предназначен для одного разрешённого FILE-target, одного расширения и существующего набора YAxUnit-тестов. 2026-09-10 публичный CLI завершил сквозной пилот BSLFlowPilot в bp1: исходный load/update, контрольное чтение после ошибки пути отчёта, продолжение только тестов, 5/5 PASS и acceptance. Это проверка первого контура; исторический двухтестовый wrapper-пилот сохраняется отдельно.

## Что объявляет criterion

Native-проверка допускается только для `integration` criterion. Обязательны `target` (абсолютный путь к FILE-каталогу с `1Cv8.1CD`), абсолютный `executable`, имя файла которого ровно `1cv8.exe`, непустой `protected_paths`, непустой уникальный список class-qualified `expected_tests` и объект `native_1c`:

```json
{
  "id": "native_yaxunit",
  "kind": "integration",
  "observation": "Два заранее определённых поведения подтверждаются исходным JUnit.",
  "report": ".bsl-flow-worker/native.xml",
  "target": "C:\\REPLACE\\FILE_BASE",
  "executable": "C:\\Program Files\\1cv8\\8.3.REPLACE\\bin\\1cv8.exe",
  "arguments": [],
  "protected_paths": [
    "src/extensions/REPLACE/Tests",
    "src/extensions/REPLACE/Ext/Module.bsl"
  ],
  "expected_tests": [
    "Company.Tests.Sales.OrderTests.Should_post",
    "Company.Tests.Sales.OrderTests.Should_keep_dot_in_classname"
  ],
  "native_1c": {
    "source_root": "src/extensions/REPLACE",
    "extension": "REPLACE",
    "module": "Company_OrderTests",
    "platform_version": "8.3.REPLACE.REPLACE",
    "executable_sha256": "REPLACE_WITH_64_LOWERCASE_HEX_SHA256",
    "authorized_operations": ["inventory", "load", "update", "test"],
    "authorization_reference": "trusted-operator-approval-REPLACE"
  }
}
```

Все значения `REPLACE` и хеш в примере фиктивны. Парсер не принимает произвольные native-аргументы; `authorized_operations` должен иметь ровно порядок `inventory,load,update,test`. `expected_tests` сравниваются с JUnit точно, включая имя класса с точками: идентификатор строится как `classname.name`, поэтому точки внутри `classname` сохраняются.

`protected_paths` — относительные пути внутри worker checkout. Они фиксируют тесты и fixtures, которые реализация не может изменить без пересмотра доверенного test-контракта. Перед native-проверкой исходники проверяются и копируются в отдельный неизменяемый по смыслу snapshot; load использует именно этот snapshot.

## Предварительные проверки и порядок действий

Контроллер проверяет физическую identity FILE-каталога, marker `1Cv8.1CD`, наличие и точный SHA-256 разрешённого `1cv8.exe`, его точную версию `platform_version` и соответствующий `comcntr.dll`. UNC и алиас пути для первого адаптера блокируются. Inventory читается через COM и содержит наблюдаемые свойства расширений: имя, версию, активность, назначение, область действия, UUID и hash sum. Неполное или неподтверждённое свойство не принимается.

Операции выполняются строго так:

1. Снять source snapshot и записать inventory до изменения.
2. Выполнить native `load` из snapshot.
3. Выполнить native `update` для выбранного расширения и повторно проверить inventory до запуска тестов.
4. Выполнить `test` с фильтром объявленного `module`, получить свежий исходный JUnit и проверить точный набор `expected_tests`.
5. Снять inventory после изменения и сравнить переход: выбранное расширение должно иметь версию из XML и быть активно; UUID установленного экземпляра не должен неожиданно измениться; остальные расширения не должны измениться.

Успех требует ненулевого и свежего JUnit с корнем `testsuite` или `testsuites`, без skipped, с согласованными агрегатами и без failure/error. Сохраняются исходный JUnit, логи каждого native шага, inventory до/после, snapshot, request и их хеши. UUID расширения из XML — это identity исходника расширения. UUID экземпляра расширения, прочитанный из native inventory, — отдельная identity установленного объекта; их нельзя смешивать.

## Секреты, авторизация и интерфейс

Публичный интерфейс сохраняет существующие действия очереди (`start`, `status`, `next`, `run`, `update`, `resume`, `cancel`, `record`, `accept`, `deliver`) и `runner run`. Для native execution/recovery используется существующий флаг `--runtime-auth stdin`: контроллер читает одну приватную JSON-строку из redirected stdin с полями `username` и `password`, переводит пароль в `SecureString`/`PSCredential` в памяти и очищает временные значения после работы. Секреты не передаются в CLI-аргументах, файлах или environment variables и не попадают в request hash, журнал или bundle. Значение `authorization_reference` — ссылка на доверенное решение оператора, а не криптографическая identity и не доказательство полномочий.

Отдельный read-only inventory helper получает credential только через stdin дочернего процесса. Worker не получает права самостоятельно запускать native операции, менять состояние контроллера, устанавливать инструменты или обходить этот контракт. Отдельного preview CLI в интерфейсе нет.

Ограничение платформы: сама `1cv8.exe` получает пароль через `/P` в аргументах процесса. Приватный stdin защищает передачу между компонентами контроллера, но не скрывает этот native-аргумент от администратора ОС. Во время native-шагов managed worker не выполняется.

## Lock, timeout, cancel и восстановление

Для одного target все native-попытки текущего пользователя используют общий журнал в LocalAppData, адресованный хешем физической identity target. На время операции удерживается lock. До первой потенциально изменяющей операции записывается `pending.json` с task/attempt/criterion, source и request hash. Наличие unresolved pending блокирует автоматический повтор и новую запись в тот же target.

Timeout ограничивает ожидание каждого native шага и общий wall-time задачи. После dispatch native процесс считается непрерываемым: `cancel` не убивает его и не объявляет rollback. Cancel запрещает новый dispatch и оставляет состояние потенциально изменённым; после него требуется осмотр и явное восстановление.

Если результат пропал после dispatch, отсутствие receipt не трактуется как «не запускалось». Control-read recovery проверяет, что контроллер и дочерние процессы завершены, сверяет точные attempt/target/source identity и повторно читает inventory. Recovery даёт только `RECONCILED_NO_PASS`; оно не создаёт PASS. Для native recovery требуется явное разрешение retry на конкретный unresolved attempt. Автоматический повтор load/update запрещён.

Для задачи с единственным native criterion `Resume` сначала проверяет сохранённые raw evidence, request hash, snapshot, логи load/update/test, JUnit и inventory. Если исходный native успех полностью сохранён, он импортируется и закрывает pending-latch без повторного load, update или test. Для смешанного набора критериев автоматическое восстановление общего результата verify пока не реализовано. Неполный receipt или изменившиеся входы переводят задачу в BLOCKED для осмотра.

Если загрузка и update доказанно завершились, а получение отчёта тестов не удалось, разрешено отдельное продолжение без повторной загрузки. После записанного control-read recovery доверенный `scope_change` задаёт `native_1c.reuse_load_attempt` — UUID исходной попытки этой же задачи — и `authorized_operations: ["inventory", "test"]`. Исходные load/update receipts и их логи должны оставаться целыми; текущий полный manifest, платформа, модуль и тесты должны совпадать. Ссылка `authorization_reference` также сохраняется: она входит в связанный platform dependency. Цепочки ссылок на другие продолжения запрещены.

Контроллер сверяет recovery inventory с исходным составом до загрузки и с новым чтением базы, сохраняет `loaded-proof.json`, создаёт собственный pending и выполняет только test. После тестов сравнивается весь состав расширений, включая hash выбранного. Прежняя неудача остаётся в истории; новый результат не подменяет её receipts. Windows-путь `reportPath` передаётся YAxUnit с обратными слешами: реальный пилот показал, что вариант `C:/...` не создаёт ожидаемый исходный JUnit.

## Review и границы доказательств

Для критичного маршрута независимое code review принудительно контроллером; reviewer получает полный текущий diff и спецификацию, работает в режиме criticism-only и не редактирует код. Принятые findings допускают одну адресную correction round с новым независимым review. Это отдельный gate и не заменяется сообщением worker о завершении.

Наличие XML, статическая диагностика, native build, round-trip и исторические тестовые прогоны не доказывают реальную работу этого адаптера. Пока не выполнены реальный FILE-пилот с точным `1cv8.exe`, native load/update/test, inventory transition и recovery evidence, production rollback, UI-приёмка, EPF-приёмка и «полная автономность» не заявляются. Durable Unica jobs запрещены и этим контрактом не используются.

Source-only автоматический repair остаётся существующим маршрутом только для уже поддержанного source-level случая. Runtime-сбой, неопределённый эффект или несовпадение фактического состояния требуют inspection/control-read и не исправляются автоматически.

## Свидетельства пилота 2026-09-10

- Задача `ceadcf65-04c6-43cc-a8a0-c5f4f334b430`, acceptance revision 21: `fcfcdfe347f78233b724d3dc1b1ad4ca701639a2043575b0337117788480a8a0`.
- Исходный native attempt `2e6b2c3f-da2e-4bf7-868a-b9695f65a0cd`: load/update завершились; JUnit оказался в ошибочном файле `C` в рабочем каталоге. Его позднее сохранили и независимо проверили: 5 PASS, SHA-256 `1e7fb55e53b9490c8e51d6a0ad601461f1288e79c7b9c5fc216dc1b56a85d50e`. Исходный BLOCKED не переписан.
- Продолжение `e997027b-11bd-4e36-b3d0-d4913b280a58` содержит только шаг `test`, без load/update. Оригинальный JUnit в каталоге контроллера: 5 PASS, SHA-256 `84699cdeaa8c063edc6563271f6121c6c70f19091ef0b387ebb1aab3720e4a5a`.
- Полный source manifest `4a9b83f229f18ce2351116c50214e57f17bcad4c2fa2e79f5b05ff2c9aa41d48`; snapshot расширения `a90f5468aea99bfbb3fe4cd6caad8d0fdbfe6e3871ced064cf296b6585b30bc0`.
- Inventory всех 10 расширений до и после продолжения совпал: `6216813abff673c22eadf144c77a10fd27aef40e97c78ac9674ea6e6ae4b8f73`; pending отсутствует. Локальный handoff создан из принятого manifest.

Реальное восстановление после ошибки доставки отчёта дополняют offline fault-тесты прерывания, таймаута, отмены и сбоя между receipt и снятием latch. Принудительное прерывание обновления реальной базы не выполнялось.
