# Advisory review reconciliation

Все findings приняты и внесены одним targeted revision:

| Finding | Решение |
|---|---|
| `CROSS-01` | Activation заблокирована до native controller capability; legacy writes ограничены checkout-local v1. |
| `REG-01` | Graph validation + dependency revision commit защищены repository graph lock/generation-CAS; добавлен встречный concurrency test. |
| `REG-02` | Migration receipt связывает verified source prefix с canonical continuation; повторный discovery не создаёт ложный conflict. |
| `REG-03` | Adoption получает portable artifact manifest/copy/resolve contract и разделяет historical completed от current freshness. |
| `REG-04` | Durable commit требует directory/metadata sync; post-rename uncertainty reconciles exact target без blind retry. |

Protected direction сохранён. Второй модельный review loop не выполнялся.
