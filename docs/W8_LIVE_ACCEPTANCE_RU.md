# Живая приёмка W8: native 1C runtime adapter

Дата шаблона: 2026-09-14. Статус: **PAUSED по решению владельца** (2026-09-14 вечер, «тестирование проведём попозже на реальной 1С»). Подготовка завершена: платформа/цель/креденшелы подтверждены, фикстура расширения собрана и загружена в базу (`Ext / 1.0.0`, тест-модуль `FixtureModule`), применена `/UpdateDBCfg -Extension Ext`. Незакрытый технический блокер: batch-запуск ENTERPRISE с `/DisableStartupDialogs` падает с «Запрещено использование окон» (стартовое окно БСП — гипотеза: модал проверки обновлений). Полный чекпойнт, идентичности окружения, фикстура и план возобновления — в [work/w8-live-acceptance-20260914/checkpoint.md](../../work/w8-live-acceptance-20260914/checkpoint.md).

## Проверяемое требование

Спека `native-cross-platform-cli` req 15 + карточка W8: критерий `native_1c` исполняется native-движком (verify-ветка stagehost, без pwsh), с полным контрактом: inventory до/после, load/update через DESIGNER, тесты через ENTERPRISE с `RunUnitTests`, JUnit-гейт, journal-защёлка, terminal-рецепты. Offline-часть уже закрыта дифференциалом и юнит-тестами; этот прогон — первый авторизованный живой цикл реальной 1С через native-адаптер.

## Предусловия

1. Собранный бинарник: `scripts/Build-BSLFlowCli.ps1` (без `-Test`, затем `scripts/Test-BSLFlowCli.ps1` как smoke).
2. Платформа 1С: каталог с `1cv8.exe` и `comcntr.dll`; COM-коннектор `V83.COMConnector` зарегистрирован ровно на этот `comcntr.dll` (проверка адаптера — `HKEY_CLASSES_ROOT\CLSID\{181E893D-73A4-4722-B61D-D604B3D67D47}\InprocServer32`).
3. Отдельная тестовая FILE-база (НЕ рабочая): каталог-цель с маркером `1Cv8.1CD`, не алиас/UNC (адаптер отвергает подставные пути).
4. Креденшелы базы — только через `--runtime-auth stdin` (одна приватная JSON-строка `{"username":"...","password":"..."}`), не в аргументах и не в файлах.
5. Фикстура расширения: каталог `<worker>/src/ext` с `Configuration.xml` (uuid ≠ 0000…), версия `8.3.x.y` критерия должна совпадать с `FileVersion` `1cv8.exe`, модуль `FixtureModule` с тестом `FixtureModule.ExactCase`.

## Вычисление идентичностей (одноразово)

```powershell
$exe = "<путь>\1cv8.exe"
(Get-FileHash -LiteralPath $exe -Algorithm SHA256).Hash.ToLowerInvariant()
(Get-Item -LiteralPath $exe).VersionInfo.FileVersion
(Get-Item -LiteralPath (Join-Path (Split-Path $exe -Parent) 'comcntr.dll') | Get-FileHash -Algorithm SHA256).Hash.ToLowerInvariant()
```

## Запрос задачи (trusted request)

`request.json` — mode `implement` (либо `analysis_only` для run-без-implement), критерий:

```json
{
  "id": "native",
  "kind": "integration",
  "observation": "Живой цикл W8: загрузка и тест расширения на отдельной тестовой базе.",
  "executable": "<путь>\\1cv8.exe",
  "arguments": [],
  "protected_paths": ["tests/FixtureModule"],
  "target": "<путь к тестовой базе>",
  "expected_tests": ["FixtureModule.ExactCase"],
  "native_1c": {
    "source_root": "src/ext",
    "extension": "Ext",
    "module": "FixtureModule",
    "platform_version": "8.3.<N>.<M>",
    "executable_sha256": "<sha256 из шага выше>",
    "authorized_operations": ["inventory", "load", "update", "test"],
    "authorization_reference": "live-w8-<дата>"
  }
}
```

## Прогон

```powershell
# создание/активация (native repository task):
bsl-flow task create --project <repo> --input task-card.json
bsl-flow task activate --project <repo> --task <uuid> --input request.json
# исполнение с приватными креденшелами:
'{"username":"<user>","password":"<pass>"}' | bsl-flow task run --project <repo> --task <uuid> --runtime-auth stdin
```

## Контрольный лист приёмки

- [ ] verify-стадия завершилась `PASS`; `observations` содержат criterion `native` с `tests=["FixtureModule.ExactCase"]`.
- [ ] В raw-каталоге попытки: `source-snapshot/`, `inventory-before/loaded/after.json`, `runtime-request.json`, `steps/load|update|test/{prepared,process,started,terminal}.json`, `original.junit.xml`, `runtime-success.json`.
- [ ] Journal `%LOCALAPPDATA%\BSLFlow\runtime\<key>\history\<attempt>.success.json` создан; `pending.json` снят.
- [ ] Inventory-переход: версия расширения совпала с `Configuration.xml`, `active=true` после загрузки.
- [ ] Process audit (procmon/Process Explorer): в дереве процесса `bsl-flow` отсутствует `pwsh`; только `1cv8.exe` и сама база.
- [ ] Повторный `resume` не перезапускает операции с базой (recovered/saved-observation ветка либо replay через record).

## Границы доказательства

Один авторизованный цикл на одной тестовой базе; не бизнес-пилот и не нагрузочный тест. `reuse_load_attempt` (test-only продолжение) и recovery-путь после реального обрыва — отдельные прогоны; их offline-контракты уже покрыты `scripts/Test-NativeRecovery.ps1` (PS) и `cli/internal/repository/native1c_recovery_test.go` (Go).
