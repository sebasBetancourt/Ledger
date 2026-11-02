-- name: GetAccount :one
SELECT id, type, code, currency, status, allow_negative, created_at
FROM accounts
WHERE id = $1;
