# Fixture Change

## Классификация

- Сложность: XL
- Риск: low

## Цель

Fixture spec for the parity harness lint trace. It deliberately carries unresolved template placeholders (TODO) and speculative future-proof language so both engines report identical findings with identical line numbers.

## Требуемое поведение

1. The harness freezes legacy lint output over these exact bytes.
2. The native lint must reproduce every finding byte for byte.

## Контекст 1С

- Конфигурация/подсистема:

## Не делать

- Do not mask divergences.

## Критерии приёмки

- GIVEN the frozen spec bytes
- THEN both engines agree

## Требуемые проверки

- [x] Static
