# Data Model

The ledger core schema. The migrations in [`migrations/`](../../migrations/) are the reference for exact columns and constraints:

| Migration | Contents |
|---|---|
| `0001_ledger_core` | Tables, `CHECK`, `UNIQUE`, foreign keys, seeded currencies and append-only rules |
| `0002_ledger_triggers` | State machine, status history and the checks deferred to `COMMIT` |
| `0003_system_accounts` | System accounts and their balance rows |

`transfers` and `idempotency_keys` are not part of the core. They arrive with phase 2, together with the `owner_id` and `alias` columns on `accounts`.

A migration that has run anywhere is never edited; changes go in a new migration.

## Decisions

| Decision | Choice | Why |
|---|---|---|
| ID type | UUIDv7, generated in Go | Time-ordered, which is good for indexes. PostgreSQL 16 has no native `uuidv7()`, so columns have no default. A UUIDv7 reveals its creation time; that is acceptable for these resources. |
| Amounts in SQL | `BIGINT` + `CHECK (amount <> 0)` on postings | Matches `int64` in Go. No `NUMERIC` with decimals and no `REAL`. |
| Amounts in JSON | Integer in minor units plus a `currency` field | Same as Stripe. Maximum absolute amount is `9007199254740991` (2^53 − 1), the largest integer every JSON client reads exactly. |
| Currencies | A `currencies` table with each `exponent` | Decimal places are data, not an assumption in code: JPY has 0. |
| Dates | `TIMESTAMPTZ`, always UTC | No time zone ambiguity. |
| Statuses | `TEXT` + `CHECK (status IN (...))` | Easier to migrate than a PostgreSQL `ENUM`. |
| Idempotency | Unique per `(client_id, key)`, valid for 24 h | Industry standard. Arrives in phase 2. |

## Diagram

```mermaid
erDiagram
    currencies ||--o{ accounts : "denominates"
    accounts ||--|| balances : "projected by"
    accounts ||--o{ postings : "receives"
    journal_transactions ||--o{ postings : "contains"
    journal_transactions ||--o{ transaction_status_history : "tracks"
    journal_transactions ||--o| journal_transactions : "reverses"
    journal_transactions ||--o| outbox_events : "publishes"

    currencies {
        text code PK
        smallint exponent
    }
    accounts {
        uuid id PK
        text type
        text code
        text currency FK
        text status
        boolean allow_negative
        timestamptz created_at
    }
    balances {
        uuid account_id PK,FK
        boolean allow_negative FK
        bigint available
        bigint locked
        bigint version
        bigint last_seq
        timestamptz updated_at
    }
    journal_transactions {
        uuid id PK
        text reference UK
        text kind
        text status
        uuid correlation_id
        text initiated_by
        text external_ref
        jsonb metadata
        uuid reverses_id FK,UK
        timestamptz created_at
    }
    postings {
        uuid id PK
        uuid transaction_id FK
        uuid account_id FK
        text currency FK
        bigint amount
        bigint seq
        bigint prev_seq FK
        bigint balance_before FK
        bigint balance_after
        timestamptz created_at
    }
    transaction_status_history {
        bigint id PK
        uuid transaction_id FK
        text from_status
        text to_status
        text reason
        timestamptz created_at
    }
    outbox_events {
        uuid id PK
        uuid transaction_id FK,UK
        text event_type
        jsonb payload
        timestamptz created_at
        timestamptz published_at
    }
```

## Tables

### `currencies`

Seeded with `COP` and `USD`, both with `exponent` 2. `accounts_currency_fk` rejects any account in a currency that is not here.

### `accounts`

A place that holds money: a user or merchant wallet, or a system account.

- **`accounts_code_only_system`:** system accounts have a `code`, and no other account does.
- **`accounts_only_system_negative`:** only system accounts may have `allow_negative`, so a bug cannot give a customer an overdraft.
- **`accounts_code_currency_unique`:** one account per system code and currency, so `fee_revenue` exists once in COP and once in USD.
- **Composite targets:** `(id, currency)` and `(id, allow_negative)` exist so other tables can reference them.

### `balances`

The stored projection, and the row locked with `SELECT … FOR UPDATE` before any change.

- **`available` + `locked`** is the account's total.
- **`allow_negative`** is a copy of the account's value, kept correct by `balances_account_fk`. A `CHECK` cannot read another table, and the sign rule depends on it.
- **`version`** counts changes, holds included. It is not concurrency control ([ADR-0003](../adr/0003-concurrency-control.md)).
- **Checks:** `balances_available_non_negative`, `balances_locked_non_negative`, `balances_negative_never_locked`.

Every account needs its row in the same transaction that creates it; a posting to an account without one fails `balance_matches_ledger`.

### `journal_transactions`

One money movement. Only `status` can change, along these paths:

```text
created → pending → completed → reversed
              └───→ failed
```

- **`kind`:** the operations in [money flows](money-flows.md) that produce postings: `transfer`, `cash_in`, `cash_out`, `merchant_payment`, `hold_capture`, `refund`, `reversal`. Holds and voids move no money between accounts, so they are not journal transactions.
- **Unique:** `journal_transactions_reference_unique` on `reference`. A failed transaction keeps its reference, so a new attempt needs a new one.
- **Reversals:** `journal_transactions_reversal_target` makes every reversal point to its original, and `journal_transactions_reverses_once` limits each transaction to one reversal.
- **`initiated_by`:** who started it, required. **`external_ref`:** the provider's identifier for cash-in and cash-out.

### `postings`

One signed line on one account. Insert-only.

- **Checks:** `postings_amount_non_zero`, `postings_amount_limit`, `postings_balance_math`, `postings_chain_start`.
- **Foreign keys:** `postings_account_currency_fk` keeps the currency equal to the account's, and `postings_chain_fk` links each posting to the previous one.
- **Unique:** `postings_account_seq_unique` and `postings_chain_anchor`.
- **Indexes:** `postings_account_created_idx` on `(account_id, created_at, id)` and `postings_transaction_idx`.

**The chain.** `seq` numbers an account's postings 1, 2, 3… and `prev_seq` always equals `seq − 1`. `postings_chain_fk` then requires that `(account_id, prev_seq, balance_before)` matches an existing `(account_id, seq, balance_after)`. A posting that does not start exactly where the previous one ended cannot be inserted. The cost is one extra unique index on the busiest table.

**History uses `seq` as its cursor, not `created_at`.** `now()` returns the time the database transaction started. If T1 starts first but waits for a lock, it commits after T2 with an earlier `created_at`, and a date cursor already past that point skips T1's posting forever. `seq` is assigned under the lock, so it has no gaps and no reordering.

### `transaction_status_history`

Append-only log of every status change, written by `journal_status_audit`. `reason` comes from `SET LOCAL ledger.status_reason = '…'` in the same database transaction, and stays `NULL` when it is not set. It is the one table with a `BIGINT` identity key, because a trigger has no UUIDv7 generator.

### `outbox_events`

`outbox_events_one_per_transaction` allows at most one event per transaction, and `outbox_required` requires at least one. A reversal is its own transaction, so it has its own event. Phase 4 adds the publisher.

## System accounts

`0003_system_accounts` creates `cash_in_clearing`, `cash_out_clearing` and `fee_revenue` for every currency, with `allow_negative` and a balance row. Their IDs are fixed so every environment matches, but code looks them up by `(code, currency)`.

## Triggers

| Trigger | When | What it does |
|---|---|---|
| `postings_immutable`, `postings_no_truncate` | Before update, delete, truncate | Rejects any change to `postings` |
| `transaction_status_history_immutable`, `transaction_status_history_no_truncate` | Before update, delete, truncate | Rejects any change to the history |
| `journal_transactions_no_delete`, `journal_transactions_no_truncate` | Before delete, truncate | Keeps every transaction |
| `balances_no_delete`, `balances_no_truncate` | Before delete, truncate | Keeps every account visible to `balance_matches_ledger` |
| `journal_status_guard` | Before insert, update | New rows start as `created`; only `status` changes, and only along valid paths; a reversal cannot be reversed |
| `journal_status_audit` | After insert, update | Writes `transaction_status_history` |
| `postings_require_pending` | Before insert | Postings can only be added to a `pending` transaction |
| `journal_balanced` | Deferred to `COMMIT` | `completed` or `reversed` needs at least 2 postings summing to 0 per currency; any other status needs 0 postings |
| `balance_matches_ledger` | Deferred to `COMMIT` | `available + locked` and `last_seq` must match the account's last posting |
| `outbox_required` | Deferred to `COMMIT` | A confirmed transaction must have its event |

`journal_balanced` fires on both `postings` and `journal_transactions`. A trigger on `postings` alone would never run for a transaction completed with no postings.

`balance_matches_ledger` is O(1). The chain starts at 0 and each link adds its amount, so the last `balance_after` is already the sum of the account's postings.

## Known limits

- **Database role:** triggers only protect against the application if it does not own the tables, because an owner can disable them. The application role arrives before Go code writes money.
- **Reversal content:** the schema guarantees a reversal is balanced and happens once, but not that it mirrors the original. That rule lives in Go and needs its own test.
