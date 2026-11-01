# ADR-0004: Money as int64 in minor units

## Status

Accepted

## Context

Ledger must add, subtract and compare amounts exactly: a sum that is off by one minor unit breaks the zero-sum invariant. The representation has to be exact across Go, PostgreSQL and JSON.

## Decision

Amounts are `int64` in Go and `BIGINT` in PostgreSQL, in the currency's minor units. COP has 2 decimals, so $50,000 COP is `5000000`.

JSON sends the same integer with a `currency` field. The maximum absolute amount is `9007199254740991` (2^53 − 1), enforced by `postings_amount_limit`, because JSON clients that parse numbers as doubles lose precision above it. That is about 90 trillion COP per movement, far beyond any wallet transaction.

Percentages, like the 2% fee, are calculated with integer arithmetic. The rounding rule is defined in phase 3.

## Alternatives considered

- **`float64`:** binary floating point cannot represent most decimal fractions. In Go, with `a, b := 0.1, 0.2`, the sum `a + b` prints `0.30000000000000004` and `a + b == 0.3` is `false`. Small errors like this accumulate until a transaction no longer sums to zero. Rejected.
- **Arbitrary-precision decimal** (`NUMERIC` with a decimal library): exact, and it handles sub-unit amounts. Rejected because Ledger never needs fractions of a minor unit, and it would add a dependency, slower arithmetic, and a way to store values with too many decimals. Every operation would also need a defined precision.

## Consequences

- **Good:** exact arithmetic, using native types in Go and PostgreSQL.
- **Good:** an invalid amount, like half a cent, cannot be represented.
- **Good:** PostgreSQL raises an error on `BIGINT` overflow instead of wrapping around.
- **Bad:** Go `int64` overflow wraps silently, so the ledger must check additions.
- **Bad:** each currency's minor unit must be known. This matters when phase 8 adds multi-currency, since JPY has 0 decimals and KWD has 3.
- **Bad:** the `amount` in the API is not human-readable, so clients must format it.
