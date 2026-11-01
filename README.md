# Ledger

**A double-entry wallet and payments engine where every transaction sums to zero — always.**

Ledger is the backend core of a digital wallet (think Nequi or Cash App): user and merchant wallets, P2P transfers, merchant payments with fees, holds (authorize → capture / void), refunds, cash-in and cash-out through simulated providers, and signed webhooks.

It is built to prove one guarantee under real-world failure: **money is never created, lost or applied twice** — not with 10,000 concurrent transfers, not with duplicated requests, not when a worker dies mid-payout.

**How:**
- Immutable double-entry ledger; balances are projections rebuilt from it
- Idempotency keys and unique references on every money movement
- Ordered row locking in PostgreSQL — no deadlocks, no double spending
- Transactional outbox → NATS JetStream for exactly-once event publishing
- Sagas with compensation for external payouts
- Nightly reconciliation that proves the books balance

> Simulated money only. No real funds, banks or KYC.
