# ADR-0005: The database enforces ledger invariants

## Status

Accepted

## Context

Go validates every posting request, but Go is not the only writer: a bug, a migration, a support script or a hand-written `INSERT` in `psql` can all reach the tables. A rule that lives only in application code holds only as long as every writer goes through that code.

## Decision

Two layers check the same rules. Go validates first, for clear and fast domain errors. PostgreSQL rejects anything that still breaks a rule.

- **Sign convention:** amounts are signed from the account's point of view (`+` increases its balance, `−` reduces it), so "sums to zero" is one `SUM`.
- **Deferred constraint triggers** check, at `COMMIT`, that each transaction sums to zero per currency, that balances match the ledger, and that each confirmed transaction has its outbox event.
- **A composite foreign key** `(account_id, currency)` makes a posting in the wrong currency impossible, with no code.
- **A chain by `seq`:** a self-referencing foreign key forces each posting to start where the account's previous one ended.
- **Append-only triggers** reject `UPDATE`, `DELETE` and `TRUNCATE` on postings and status history.

Errors are mapped to domain errors by constraint name, never by message text.

## Alternatives considered

- **Validation in the application only:** simpler, with one place to read the rules. Rejected because any writer that skips the code, or a single bug in it, can store unbalanced money, and nothing would notice until reconciliation.
- **Immediate triggers instead of deferred ones:** rejected because postings arrive one at a time; an immediate check would reject the first posting of every valid transfer.

## Consequences

- **Good:** a whole class of bugs fails loudly at `COMMIT` instead of corrupting balances.
- **Good:** `ledgerctl check-invariants` can verify the same rules on stored data at any time.
- **Bad:** the rules exist twice, in Go and in SQL, and must change together.
- **Bad:** every posting pays for extra indexes and trigger work.
- **Bad:** an owner of the tables can disable triggers, so the application must use a role that does not own them.
