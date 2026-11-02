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
