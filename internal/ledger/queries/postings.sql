-- name: InsertPosting :one
INSERT INTO postings (
    id, transaction_id, account_id, currency, amount, seq, prev_seq, balance_before, balance_after
) VALUES (
    @id, @transaction_id, @account_id, @currency, @amount, @seq, @prev_seq, @balance_before, @balance_after
)
RETURNING *;

-- name: ListPostingsByAccount :many
-- Newest first. Pass the last seq of the previous page as before_seq, or NULL
-- for the first page. Pages by seq, not created_at: created_at is the start
-- time of a transaction that may commit later, so a date cursor can skip rows.
SELECT *
FROM postings
WHERE account_id = @account_id
  AND (sqlc.narg(before_seq)::bigint IS NULL OR seq < sqlc.narg(before_seq)::bigint)
ORDER BY seq DESC
LIMIT @page_size;
