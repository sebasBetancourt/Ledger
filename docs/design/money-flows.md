# Money Flows

Every operation written as an accountant would, before any table exists. The schema must be able to represent each of these flows.

## Conventions

- **Sign:** amounts are signed from the account's point of view. `+` increases its balance and `−` reduces it. The postings of every transaction sum to 0.
- **Units:** integers in minor units. COP has 2 decimals, so $50,000 COP = `5,000,000`. Commas here are for reading only; the system stores `5000000`.
- **Total balance:** `total = available + locked`. Postings change the total; holds only move money between `available` and `locked`.
- **Immutability:** a posted transaction is never edited. Corrections are new transactions.

## Accounts used

| Account | Type | Meaning |
|---|---|---|
| `wallet:ana`, `wallet:beto` | user | Customer wallets |
| `wallet:cafe` | merchant | Merchant wallet |
| `cash_in_clearing` | system | Counterpart of money entering Ledger through a provider |
| `cash_out_clearing` | system | Counterpart of money leaving Ledger through a provider |
| `fee_revenue` | system | Fees earned by the platform |

## Operations

### 1. P2P transfer: Ana sends $50,000 to Beto

| Account | Amount |
|---|---|
| `wallet:ana` | −5,000,000 |
| `wallet:beto` | +5,000,000 |
| **Sum** | **0** |

### 2. Cash-in: Ana tops up $100,000

| Account | Amount |
|---|---|
| `cash_in_clearing` | −10,000,000 |
| `wallet:ana` | +10,000,000 |
| **Sum** | **0** |

`cash_in_clearing` goes negative by design: its balance mirrors the money that came from outside Ledger.

### 3. Merchant payment: Ana pays $100,000 with a 2% fee

| Account | Amount |
|---|---|
| `wallet:ana` | −10,000,000 |
| `wallet:cafe` | +9,800,000 |
| `fee_revenue` | +200,000 |
| **Sum** | **0** |

### 4. Hold: reserve $30,000 of Ana's money

No money moves between accounts, so there are no postings.

| Account | Amount |
|---|---|
| **Sum** | **0** |

| Projection change | `available` | `locked` |
|---|---|---|
| `wallet:ana` | −3,000,000 | +3,000,000 |

### 5. Partial capture: capture $20,000 of the $30,000 hold, with a 2% fee

| Account | Amount |
|---|---|
| `wallet:ana` | −2,000,000 |
| `wallet:cafe` | +1,960,000 |
| `fee_revenue` | +40,000 |
| **Sum** | **0** |

The whole hold closes: $20,000 leaves through the postings and the remaining $10,000 returns to `available`.

| Projection change | `available` | `locked` |
|---|---|---|
| `wallet:ana` | +1,000,000 | −3,000,000 |

### 6. Void: cancel a $30,000 hold

No postings, for the same reason as the hold.

| Account | Amount |
|---|---|
| **Sum** | **0** |

| Projection change | `available` | `locked` |
|---|---|---|
| `wallet:ana` | +3,000,000 | −3,000,000 |

### 7. Cash-out: Ana withdraws $40,000

Step 1 places a hold for $40,000 (same as operation 4), so the money cannot be spent while the provider works.

Step 2 depends on the provider's answer:

- **Confirmed:** post the table below and release the hold.
- **Failed:** void the hold (operation 6). The ledger never changed.
- **Unknown (timeout):** do nothing. The hold stays open until the provider's status is known. Retrying blindly could pay twice.

| Account | Amount |
|---|---|
| `wallet:ana` | −4,000,000 |
| `cash_out_clearing` | +4,000,000 |
| **Sum** | **0** |

| Projection change | `available` | `locked` |
|---|---|---|
| `wallet:ana` | 0 | −4,000,000 |

### 8. Reversal: undo the P2P transfer from operation 1

A new transaction that mirrors the original. The original postings stay, and its status changes to `reversed`.

| Account | Amount |
|---|---|
| `wallet:ana` | +5,000,000 |
| `wallet:beto` | −5,000,000 |
| **Sum** | **0** |

If Beto already spent the money, the reversal fails with `insufficient_funds` and the original stays `completed`. Letting Beto's balance go negative would break invariant 5, and collecting that money is a recovery process outside the ledger.

A reversal of a merchant payment mirrors all three postings, so the platform returns its fee. A transaction can be reversed at most once.

### 9. Partial refund: Café returns $30,000 of the payment from operation 3

| Account | Amount |
|---|---|
| `wallet:cafe` | −3,000,000 |
| `wallet:ana` | +3,000,000 |
| **Sum** | **0** |

The fee is not returned, the same policy Stripe uses. This is a business decision: the platform did its work, and a reversal is the tool for undoing a payment completely. If Café lacks the funds, the refund fails with `insufficient_funds`.

## System accounts

This list is the key output of this document. Each account exists once per currency.

| Account | Used by | Normal balance |
|---|---|---|
| `cash_in_clearing` | Cash-in | Negative |
| `cash_out_clearing` | Cash-out | Positive |
| `fee_revenue` | Merchant payment, capture, reversal | Positive |

P2P transfers need no system account, but v0.1 still needs `cash_in_clearing`: the public API has no cash-in endpoint yet, so tests fund wallets through the internal ledger against this account.

Phase 6 will add `manual_adjustments` for audited corrections.

Only user and merchant accounts must stay non-negative. System accounts may go negative, because they represent the other side of the books.
