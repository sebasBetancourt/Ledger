# ADR-0003: Pessimistic row locks in ID order

## Status

Accepted

## Context

Two concurrent spends of the same balance can both read $100, both pass the funds check, and both debit, which is a double spend. Each balance change also takes the next `seq`, so changes to one account must happen one at a time. PostgreSQL is the only source of truth for money.

## Decision

Inside the database transaction, lock every affected `balances` row with `SELECT … FOR UPDATE`, always in ascending account ID order, then check funds and write.

Taking locks in one global order prevents deadlocks. Without it, crossed transfers wait on each other forever:

| Step | T1: A→B | T2: B→A |
|---|---|---|
| 1 | locks A | locks B |
| 2 | waits for B | waits for A: deadlock |

With ordering, both start with the lower ID. T2 waits at step 1 while holding nothing, T1 finishes, and T2 continues.

## Alternatives considered

- **Optimistic concurrency with `version`:** no held locks, but a popular wallet makes most attempts fail and retry under contention, right when load is highest. Rejected. `balances.version` stays as a change counter only.
- **`SERIALIZABLE` with retry:** the database detects conflicts, so no code can forget a lock. Rejected because every caller needs retry logic, and abort rates climb on hot accounts.
- **Queue per wallet:** one worker per wallet removes contention, but a transfer spans two queues, so it needs a saga, and the API becomes asynchronous. Rejected.
- **Redis lock:** a lock outside the database can expire during a slow transaction, or be lost in a failover, leaving two writers. Rejected: Redis never guards money.

## Consequences

- **Good:** no double spends and no deadlocks between transfers.
- **Good:** easy to explain and to test with `-race`.
- **Bad:** each wallet is limited to one movement at a time.
- **Bad:** `fee_revenue` becomes a hot row in phase 3, since every merchant payment locks it. Load tests in phase 7 must measure this.
- **Bad:** every new code path must follow the ordering rule, so it belongs in one ledger function.
