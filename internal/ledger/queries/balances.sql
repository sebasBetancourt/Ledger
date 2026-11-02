-- name: LockBalances :many
-- Locks the rows until the transaction ends. ORDER BY account_id makes every
-- transaction take its locks in the same order, so crossed transfers
-- (A→B and B→A) queue instead of deadlocking.
SELECT *
FROM balances
WHERE account_id = ANY(@account_ids::uuid[])
ORDER BY account_id
FOR UPDATE;

-- name: UpdateBalance :one
-- Stores values computed under LockBalances. version counts changes.
UPDATE balances
SET available  = @available,
    locked     = @locked,
    last_seq   = @last_seq,
    version    = version + 1,
    updated_at = now()
WHERE account_id = @account_id
RETURNING *;

-- name: GetBalance :one
-- A plain read, without locking: for showing a balance, never for moving money.
SELECT b.account_id, a.currency, b.available, b.locked, b.version, b.updated_at
FROM balances b
JOIN accounts a ON a.id = b.account_id
WHERE b.account_id = @account_id;
