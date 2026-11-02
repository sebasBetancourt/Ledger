# Invariants

The rules that must hold at all times, whatever fails. If something is only guaranteed by good intentions, it is not guaranteed. Every mechanism below names an object in [`migrations/`](../../migrations/).

| # | Invariant | Why it matters | How it is guaranteed | How it is tested |
|---|---|---|---|---|
| 1 | The postings of each transaction sum to 0 | Otherwise money is created or disappears | Validation in Go, plus deferred trigger `journal_balanced` (at least 2 postings, sum 0 per currency), `postings_amount_non_zero`, `postings_account_currency_fk` and `postings_require_pending` | Integration test inserts an unbalanced transaction and expects an error at `COMMIT`; another completes a transaction with no postings; another adds postings to a completed transaction |
| 2 | All postings in the system sum to 0, per currency | It is the one number that proves the books balance as a whole | Follows from invariant 1, because `postings_immutable` and `postings_no_truncate` stop anyone from changing a balanced transaction afterwards. Internal reconciliation checks it in phase 6 | Property test with `rapid`: after random operations, `SUM(amount)` per currency is 0 |
| 3 | A balance projection equals the sum of the account's postings, and `rebuild-balances` matches it | Balances are what customers see and spend; a wrong one is wrong money | Deferred trigger `balance_matches_ledger` compares each changed balance to the last posting, and `balances_no_delete` keeps every account in scope | Integration test edits a balance and expects an error at `COMMIT`; a test runs `rebuild-balances` and compares every account |
| 4 | Each posting's `balance_before` equals the previous posting's `balance_after` | "Why did this balance change?" needs an unbroken history | `postings_chain_fk`, `postings_chain_anchor`, `postings_account_seq_unique`, `postings_chain_start` and `postings_balance_math` | Integration tests insert a broken chain, a repeated `seq`, a skipped `seq` and wrong arithmetic, and expect each to fail |
| 5 | No user or merchant account has a negative available balance, and locked never exceeds the total | A negative balance means spending money that does not exist | `balances_available_non_negative`, `balances_locked_non_negative`, `balances_negative_never_locked` and `balances_account_fk` (which ties each balance to its account's `allow_negative`), plus a funds check in Go after `SELECT … FOR UPDATE` | Integration test overdraws and expects `insufficient_funds`; concurrency test with `-race` runs two spends of the same balance and exactly one wins |
| 6 | A reference or idempotency key produces at most one effect | Retries are normal, and a retry must not charge twice | `journal_transactions_reference_unique` and `journal_transactions_reverses_once`. Phase 2 adds `idempotency_keys` with a unique `(client_id, key)`, committed together with the money | Integration test sends the same request concurrently and asserts one transaction and identical responses; another reuses a reference after the key expires; another reverses twice |
| 7 | Every confirmed transaction has exactly one outbox event | A consumer that misses an event, or sees it twice, disagrees with the books | `outbox_events_one_per_transaction` allows at most one and deferred trigger `outbox_required` requires at least one, in the same commit as the money | Integration test confirms a transaction with no event and expects an error at `COMMIT`; another inserts a second event and expects an error |

## How to read them

- **Invariant 5:** the total is `available + locked`, so locked can only exceed the total if `available` goes negative. The `CHECK`s on those two columns are what enforce it. Only system accounts may have `allow_negative` (`accounts_only_system_negative`), because `cash_in_clearing` is negative by design, and they cannot hold locked money.
- **Invariant 7:** it applies per journal transaction, not per posting. A two-posting transfer produces one event.
- **Invariant 2:** this is the only invariant with no direct database object. It is a mathematical consequence of invariant 1 plus immutability, which is why the property test and reconciliation exist to confirm it independently.

## Beyond the schema

Two rules protect these guarantees from outside the database:

- The application must not connect as the table owner, because an owner can disable triggers. That role is still pending: until it exists, every connection is the owner, so it must land before the API serves traffic in phase 2.
- Holds arrive in phase 3, adding one rule to invariant 3: `locked` must equal the sum of open holds.
