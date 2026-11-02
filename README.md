# Ledger

**A double-entry wallet and payments engine where every transaction sums to zero — always.**

Ledger is the backend core of a digital wallet (think Nequi or Cash App): user and merchant wallets, P2P transfers, merchant payments with fees, holds (authorize → capture / void), refunds, cash-in and cash-out through simulated providers, and signed webhooks.

It is built to prove one guarantee under real-world failure: **money is never created, lost or applied twice** — not with 10,000 concurrent transfers, not with duplicated requests, not when a worker dies mid-payout.

**How:**
- Immutable double-entry ledger; balances are projections rebuilt from it
- Idempotency keys and unique references on every money movement
- Ordered row locking in PostgreSQL — no deadlocks, no double spending
- Transactional outbox → NATS JetStream for reliable event publishing with idempotent consumers
- Sagas with compensation for external payouts
- Nightly reconciliation that proves the books balance

> Simulated money only. No real funds, banks or KYC.

## Architecture

A modular monolith: one codebase, two processes, and PostgreSQL as the only source of truth for money.

```mermaid
flowchart LR
    client([API client]) -->|HTTPS| api[API process]
    api -->|money, in one transaction| pg[(PostgreSQL)]
    api -.->|rate limits, never balances| redis[(Redis)]
    worker[Worker process] -->|outbox, holds, reconciliation| pg
    worker -->|events| nats[[NATS JetStream]]
    worker -->|payouts| providers([Simulated providers])
    worker -->|signed webhooks| merchants([Merchant endpoints])
    providers -->|provider webhooks| api
```

## Status

Phase 1 (accounting core) is complete; there is no HTTP API yet. Today the ledger can:

- Open user and merchant wallets at zero, each with its balance row.
- Post any balanced transaction atomically with `ledger.Service.Post`: validation, ordered row locks, postings, balances and an outbox event, all in one database transaction.
- Page an account's history with an opaque cursor, never `OFFSET`.
- Check all 7 [invariants](docs/design/invariants.md) against the database, and rebuild balances from their postings, with `ledgerctl`.

## Running it

Requires Go and Docker.

```sh
cp .env.example .env
make tools         # pinned migrate, sqlc and golangci-lint in ./bin
make up            # PostgreSQL 16 in Docker
make migrate-up
make test          # go test -race ./..., against real PostgreSQL (testcontainers)
make lint

go run ./cmd/ledgerctl check-invariants              # exit 1 if any invariant fails
go run ./cmd/ledgerctl rebuild-balances --dry-run    # exit 1 if a balance disagrees with its postings
go run ./cmd/ledgerctl rebuild-balances --apply      # rewrite them under lock, logging each fix
```

Tests never mock the database. Every test gets its own migrated copy of PostgreSQL, and the ledger tests check all 7 invariants when they finish. A property-based test ([rapid](https://github.com/flyingmutant/rapid)) runs hundreds of random transfer sequences against an in-memory model.

## How the database enforces correctness

Go validates every request first, but PostgreSQL is the last line of defense: even a bug in Go, or a hand-written `INSERT` in `psql`, cannot store inconsistent money.

| Rule | Mechanism |
|---|---|
| Every transaction sums to zero | A constraint trigger, `DEFERRABLE INITIALLY DEFERRED`, checks each transaction at `COMMIT`. Postings arrive one at a time, so only the finished transaction must balance; if it doesn't, the whole `COMMIT` fails |
| The past is never edited | Triggers reject `UPDATE`, `DELETE` and `TRUNCATE` on postings and status history. Mistakes are fixed with new transactions |
| Balances match the ledger | A deferred trigger compares each balance with the account's last `balance_after` |
| Every posting continues the previous one | `seq` numbers each account's postings, and a self-referencing foreign key forces `balance_before` to equal the previous `balance_after` |
| No mixed currencies | A composite foreign key `(account_id, currency)` rejects a COP posting on a USD account |
| No overdrafts | `CHECK (allow_negative OR available >= 0)`; only system accounts may go negative |
| Explicit status changes | A trigger allows only `created → pending → completed / failed` and `completed → reversed`, and records each change |
| Every confirmed transaction is announced | A deferred trigger requires exactly one outbox event per completed transaction |

For example, this fails at `COMMIT` with `journal_balanced`, even though every statement before it succeeds:

```sql
BEGIN;
-- ... a transaction with a single posting of +5,000,000 and no other side
COMMIT;  -- ERROR: journal transaction ... has 1 postings, needs at least 2
```

See [ADR-0005](docs/adr/0005-database-enforces-invariants.md) for why both layers exist.

## Documentation

**Design**
- [Overview](docs/design/overview.md): what Ledger is and what it is not
- [Invariants](docs/design/invariants.md): the 7 rules that must always hold, and how each is enforced
- [Money flows](docs/design/money-flows.md): every operation as accounting entries
- [Data model](docs/design/data-model.md): schema decisions and ER diagram

**API**
- [OpenAPI contract](api/openapi.yaml): the v0.1 API

**Architecture decisions**
- [ADR-0001](docs/adr/0001-ledger-is-the-source-of-truth.md): the ledger is the source of truth
- [ADR-0002](docs/adr/0002-modular-monolith.md): modular monolith
- [ADR-0003](docs/adr/0003-concurrency-control.md): pessimistic row locks in ID order
- [ADR-0004](docs/adr/0004-money-as-int64-minor-units.md): money as int64 in minor units
- [ADR-0005](docs/adr/0005-database-enforces-invariants.md): the database enforces ledger invariants
