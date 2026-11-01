# ADR-0002: Modular monolith

## Status

Accepted

## Context

Ledger has clear domains: accounts, ledger, payments, risk, outbox, webhooks, providers and reconciliation. They need boundaries so the code stays understandable.

The core guarantee is that a debit and its credit commit together. A transfer also writes its idempotency key, status history and outbox event, and all of it must succeed or fail as one unit.

## Decision

Ledger is one codebase with two binaries, `cmd/api` and `cmd/worker`, sharing one PostgreSQL database. Domains are packages under `internal/`, and the dependency direction is API → product services → ledger → balances.

Anything that touches money runs in a single PostgreSQL transaction.

## Alternatives considered

- **Microservices:** separate wallet, ledger and payment services, each with its own database. Rejected because they break the atomicity of money. If a debit commits in one database and the service holding the credit crashes, the money has left one account and reached no other. Every transfer would then need a saga with compensation, and between its steps the books would be visibly wrong. Sagas are the right tool for an external bank, which Ledger cannot share a transaction with. Using one for two tables it owns would be accepting that failure mode for no benefit.
- **Unstructured monolith:** one package, no boundaries. Rejected because the ledger rules would leak into every handler, and later extraction would be impossible.

## Consequences

- **Good:** a local `COMMIT` gives atomicity, with no distributed transaction.
- **Good:** simple to run, test and debug on one machine.
- **Good:** package boundaries keep a future extraction possible.
- **Bad:** API and worker share a database, so a slow query can affect both.
- **Bad:** domains scale together, even if only one is under load.
- **Bad:** boundaries depend on code review, since one binary lets any package import another inside `internal/`.
