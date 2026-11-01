# ADR-0001: The ledger is the source of truth; balances are projections

## Status

Accepted

## Context

A wallet must answer two questions at all times: how much money does this account have, and why? The system must also prove that money is never created, lost or applied twice, even after bugs and crashes.

A stored balance alone answers only the first question. Once a value is overwritten, the reason for it is gone.

## Decision

Every money movement is an immutable journal transaction whose postings sum to zero. Postings are insert-only, enforced by database triggers.

Balances live in a separate `balances` table as a projection, updated in the same database transaction as the postings. `balance_matches_ledger` rejects any commit where the two disagree, and `rebuild-balances` can recreate the table from postings.

Corrections are new transactions, never edits.

## Alternatives considered

- **Mutable balance column with no history:** the simplest option, and a single `UPDATE` moves money. Rejected because it cannot explain a balance, cannot detect a wrong one, and has nothing to reconcile against. One bad deploy would corrupt balances with no way to recover them.
- **Mutable balance plus an audit log:** adds history, but the log is a side effect, not the truth. A code path that forgets to write it leaves the two silently different. Rejected because the guarantee would rest on discipline.
- **Computing balances from postings on every read:** no projection can drift, but reads get slower as history grows, and each spend must lock something anyway. Rejected as the main path; `rebuild-balances` uses this calculation instead.

## Consequences

- **Good:** every balance can be explained with one query, and reconciliation has a real source to compare against.
- **Good:** a corrupted projection is recoverable, because it holds no unique information.
- **Bad:** each movement writes several rows and more indexes than a single `UPDATE`.
- **Bad:** `postings` only grows, so phase 8 may need partitioning.
- **Bad:** corrections are more work, since they need a new balanced transaction.
