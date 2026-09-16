# Откат изменения (rollback)

Дата: 2026-09-16. Статус: **ROLLED BACK / ОТКЛОНЁН владельцем**.

## Основание

По решению владельца проект не будет выпускать Go-бинарник; версии под Linux/macOS в ближайших релизах не планируются. PowerShell 7 остаётся единственным движком BSL Flow. `spec.md` и `design.md` этого изменения сохранены без правок как история решения.

## Объём отката

- Удалён Go CLI: каталог `cli/` и бинарные помощники; `bsl-flow.exe` больше не является пользовательским входом. Установленный freeze r6 exe также больше не используется.
- Из CI удалены Go-дорожки; проверяется только PowerShell 7.
- SKILL.md возвращён к маршрутизации на PowerShell-скрипты.
- В `Invoke-CouncilReview.ps1` выполнен обратный порт council admission без зависимости от Go councilengine.

Пользовательский вход — `global/skills/1c-task/scripts/Invoke-BSLFlowTask.ps1`. Исторические записи о проверках Go CLI сохранены без изменений в [VERIFICATION.md](../../../VERIFICATION.md), [плане 0.8](../../../docs/PLAN_0.8_RU.md) и [плане завершения](../../../docs/SDLC_COMPLETION_RU.md).

Незакоммиченные Go-исправления на момент отката сохранены в `work/rollback-native-cli-20260916/uncommitted-go-fixes-backup.patch` (ignored path).
