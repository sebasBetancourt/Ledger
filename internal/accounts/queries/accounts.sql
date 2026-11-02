-- name: GetAccount :one
SELECT id, kind, status, currency, owner_id, alias, system_code, metadata, created_at, updated_at
FROM accounts
WHERE id = $1;
