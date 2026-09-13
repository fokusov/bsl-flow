# Advisory review reconciliation

Все findings приняты и внесены одним targeted revision:

| Finding | Решение |
|---|---|
| `NATIVE-01` | Разделены legacy canonical-state chain hash и raw byte hash; добавлен noncanonical-whitespace/key-order fixture. |
| `CROSS-01` | Добавлена engine/store/action compatibility matrix; activation начинается только с native controller slice. |
| `REG-04` | Native filesystem contract и registry protocol требуют confirmed directory durability и unknown-outcome reconciliation. |
| `NATIVE-02` | Native default, optional explicit compatibility artifact и полное runtime-PS removal получили разные milestones/criteria. |

Protected direction сохранён. Второй модельный review loop не выполнялся.
