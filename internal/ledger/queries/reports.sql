-- name: SumPostingsByCurrency :many
-- Invariant 2: every total must be 0. sum(bigint) returns numeric, hence the cast.
SELECT currency, coalesce(sum(amount), 0)::bigint AS total
FROM postings
GROUP BY currency
ORDER BY currency;

-- name: RecomputeBalances :many
-- What every balance should be, from the postings alone. rebuild-balances and
-- invariant checks compare it with the balances table.
SELECT a.id AS account_id,
       coalesce(sum(p.amount), 0)::bigint AS total,
       coalesce(max(p.seq), 0)::bigint AS last_seq
FROM accounts a
LEFT JOIN postings p ON p.account_id = a.id
GROUP BY a.id
ORDER BY a.id;

-- name: UnbalancedTransactions :many
-- Invariant 1: confirmed transactions need 2+ postings summing to 0 per
-- currency; any other status must have none.
SELECT t.id, t.status
FROM journal_transactions t
WHERE (t.status IN ('completed', 'reversed') AND (
          (SELECT count(*) FROM postings p WHERE p.transaction_id = t.id) < 2
          OR EXISTS (
              SELECT 1 FROM postings p
              WHERE p.transaction_id = t.id
              GROUP BY p.currency
              HAVING sum(p.amount) <> 0
          )))
   OR (t.status NOT IN ('completed', 'reversed')
       AND EXISTS (SELECT 1 FROM postings p WHERE p.transaction_id = t.id))
ORDER BY t.id;

-- name: BalanceMismatches :many
-- Invariant 3: every balance equals its account's postings. has_balance is
-- false for an account that lost its balance row.
SELECT a.id AS account_id,
       (b.account_id IS NOT NULL)::boolean AS has_balance,
       coalesce(b.available + b.locked, 0)::bigint AS projected_total,
       coalesce(b.last_seq, 0)::bigint AS projected_seq,
       coalesce(l.total, 0)::bigint AS ledger_total,
       coalesce(l.last_seq, 0)::bigint AS ledger_seq
FROM accounts a
LEFT JOIN balances b ON b.account_id = a.id
LEFT JOIN (
    SELECT account_id, sum(amount) AS total, max(seq) AS last_seq
    FROM postings
    GROUP BY account_id
) l ON l.account_id = a.id
WHERE b.account_id IS NULL
   OR b.available + b.locked <> coalesce(l.total, 0)
   OR b.last_seq <> coalesce(l.last_seq, 0)
ORDER BY a.id;

-- name: LedgerTotals :many
-- What each account's total and last seq should be, from its postings alone.
SELECT a.id AS account_id,
       coalesce(sum(p.amount), 0)::bigint AS total,
       coalesce(max(p.seq), 0)::bigint AS last_seq
FROM accounts a
LEFT JOIN postings p ON p.account_id = a.id
WHERE a.id = ANY(@account_ids::uuid[])
GROUP BY a.id
ORDER BY a.id;

-- name: BrokenChains :many
-- Invariant 4: each posting starts where the account's previous one ended,
-- seq has no gaps, and the arithmetic holds.
SELECT account_id, seq
FROM (
    SELECT account_id, seq, amount, balance_before, balance_after,
           lag(seq) OVER w AS previous_seq,
           lag(balance_after) OVER w AS previous_after
    FROM postings
    WINDOW w AS (PARTITION BY account_id ORDER BY seq)
) chain
WHERE balance_after <> balance_before + amount
   OR (previous_seq IS NULL AND (seq <> 1 OR balance_before <> 0))
   OR (previous_seq IS NOT NULL AND (seq <> previous_seq + 1 OR balance_before <> previous_after))
ORDER BY account_id, seq;

-- name: NegativeBalances :many
-- Invariant 5: no customer account below zero, and locked never negative.
SELECT account_id, available, locked
FROM balances
WHERE (NOT allow_negative AND available < 0) OR locked < 0
ORDER BY account_id;

-- name: DuplicateEffects :many
-- Invariant 6: a reference, or a reversal target, used more than once.
SELECT 'reference'::text AS field, reference AS value, count(*)::bigint AS uses
FROM journal_transactions
GROUP BY reference
HAVING count(*) > 1
UNION ALL
SELECT 'reverses_id'::text, reverses_id::text, count(*)::bigint
FROM journal_transactions
WHERE reverses_id IS NOT NULL
GROUP BY reverses_id
HAVING count(*) > 1;

-- name: OutboxMismatches :many
-- Invariant 7: every confirmed transaction has exactly one outbox event.
SELECT t.id, count(e.id)::bigint AS events
FROM journal_transactions t
LEFT JOIN outbox_events e ON e.transaction_id = t.id
WHERE t.status IN ('completed', 'reversed')
GROUP BY t.id
HAVING count(e.id) <> 1
ORDER BY t.id;
