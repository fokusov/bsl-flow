# Миграция на Core / Managed 0.9

Core содержит assisted-скиллы, OpenSpec, статические проверки, single reviewer и onec-ops. Managed добавляет Windows-контроллер и Council. Версии пакетов должны совпадать точно; `Install-BSLFlow.ps1` по-прежнему устанавливает полную поставку.

## Установка

Нужны PowerShell 7, Git и OpenSpec в PATH. Установщик Core не требует SDK .NET, CLI модельного провайдера или API-ключа.

```powershell
pwsh -NoProfile -File scripts/Install-BSLFlowCore.ps1 -Host codex -WhatIf
pwsh -NoProfile -File scripts/Install-BSLFlowCore.ps1 -Host codex
# Только Windows, после Core той же версии:
pwsh -NoProfile -File scripts/Install-BSLFlowManaged.ps1
```

Хосты: `codex`, `claude`, `opencode`, `agents`. Core копирует скиллы в `~/.agents/skills` (Claude: `~/.claude/skills`), схему в путь `openspec schema which bsl-flow`, bootstrap в AGENTS.md хоста. Для первой установки используются платформенные OpenSpec config defaults. Claude получает offline-скиллы, субагентов и hooks в `~/.claude/bsl-flow/hooks`; установщик идемпотентно дополняет `settings.json`, сохраняя остальные ключи и пользовательские hooks. Файл настроек входит в backup и rollback. OpenCode использует packaged reviewer config внутри скилла; глобальная пользовательская конфигурация провайдера сохраняется.

`~/.bsl-flow/installed-core.json` содержит версию и реальные каталоги установки. Managed читает `requires_core` своего ZIP, сверяет receipt и наличие Core-скиллов до записи. Поддельный или устаревший receipt не доказывает готовность runtime.

## Установка поверх 0.8

Core распознаёт старый полный набор по `1c-task/SKILL.md` без нового receipt. Старый bootstrap заменяется внутри маркеров, внешний пользовательский текст сохраняется. Изменяемые файлы, включая прежний receipt, сохраняются в `~/.bsl-flow/backups/<id>/`; `restore.json` перечисляет точные пути и наличие файлов до установки. Извлечённый пакет до записи проверяется по inventory и SHA-256 `package-manifest.json`; изменённый пакет отклоняется. Установка из исходного checkout без package-manifest разрешена. Сбой после записи автоматически восстанавливает прежние байты и удаляет созданные файлы. Пустые созданные каталоги могут остаться.

Старые Managed-файлы и пользовательские дополнительные файлы не удаляются: после миграции установи Managed совпадающей версии перед запуском контроллера. Core не запускает старый контроллер и не подтверждает его совместимость. Для полного отката успешной миграции используй `restore.json`: верни существовавшие файлы из указанного backup, удали только файлы с `existed: false`. Не смешивай backup разных установок.

## L / high-risk в Core

Без Council запрос блокируется с `BF_BLOCKED`. Владелец может явно принять риск без Council через `review-reconciliation.json`:

```json
{
  "owner_override": {
    "owner": "Имя владельца",
    "reason": "Обоснование решения",
    "accepted_risks": "Какие риски приняты без Council",
    "spec_sha256": "SHA-256 текущего spec.md в нижнем регистре",
    "original_task_sha256": "SHA-256 текущего original-task.md",
    "design_sha256": null
  }
}
```

Если `design.md` существует, его SHA-256 обязателен вместо `null`. После изменения входов прежнее решение недействительно. `Invoke-1CSpecReview.ps1` и final-validator проверяют lint и свежий grounding; итог не выше `PASS_WITH_LIMITATIONS`. Override не имитирует независимое ревью, не заменяет незавершённую публикацию Council и не авторизует действия с базой.

## Границы проверки платформ

Core CI задаёт Windows / Ubuntu / macOS. Локальные результаты на Windows подтверждают только эту ОС; зелёная конфигурация YAML не означает выполненный CI. Общий file transaction использует `IO.Path`, host paths и pwsh из PATH; reparse/symlink в целевом пути отклоняется до записи. Native 1C, COM, установленный провайдер и runtime onec-ops имеют собственные ограничения и требуют отдельного разрешённого smoke. Managed остаётся Windows-only.
