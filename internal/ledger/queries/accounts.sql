-- name: CreateAccount :one
-- The account needs its balance row (CreateBalance) in the same transaction.
INSERT INTO accounts (id, type, code, currency, allow_negative)
VALUES (@id, @type, @code, @currency, @allow_negative)
RETURNING *;

-- name: CreateBalance :one
INSERT INTO balances (account_id, allow_negative)
VALUES (@account_id, @allow_negative)
RETURNING *;

-- name: GetAccountsByIDs :many
SELECT *
FROM accounts
WHERE id = ANY(@ids::uuid[])
ORDER BY id;

-- name: GetSystemAccountByCode :one
-- Look system accounts up by code and currency, never by their fixed IDs.
SELECT *
FROM accounts
WHERE code = @code::text
  AND currency = @currency;

-- name: GetAccount :one
SELECT *
FROM accounts
WHERE id = @id;
