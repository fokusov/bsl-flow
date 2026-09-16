# Review status: BLOCKED at the deterministic chair gate (2026-09-16)

Независимое ревью (обязательное для L по проектной политике) запускалось дважды через council
multi_role_single_model (deepseek-v4-pro, единственная настроенная модель). Оба прогона упали
fail-closed на детерминированной валидации финала председателя до записи review.json:

1. Прогон 1: `Chair resolution ref does not resolve in the final specification:
   intent_critic:F-001 -> final_design_text: 'PowerShell-скрипты: ...Invoke-1CSpecContractLint.ps1'`
   (устаревший Go-текст design.md; design.md переанкерован 2026-09-16).
2. Прогон 2 (после переанкеровки design.md): `Requirement final ref does not resolve in the final
   specification: REQ-006 -> Форматы файлов / Evidence/T-NNN.json` — председатель дважды
   воспроизводит несуществующий якорь вместо вербатим-фрагмента или `Требуемое поведение / N`,
   хотя промпт председателя это явно требует.

Вывод: конвейер ревью исправен (fail-closed), промпт корректен; настроенная модель-председатель
не проходит детерминированный гейт на данной спеке. Спека реализована и покрыта тестами
(Test-SpecContractLint 100, Test-ExecutionGraphDiscipline 37, оба в Test-BSLFlowPackage);
обязательное независимое ревью остаётся ОТКРЫТЫМ гейтом до появления более сильной
chair-модели в проектном конфиге совета или явного решения владельца.
