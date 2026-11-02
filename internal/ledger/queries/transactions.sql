-- name: InsertTransaction :one
-- New transactions always start as 'created' (journal_status_guard).
INSERT INTO journal_transactions (
    id, reference, kind, correlation_id, initiated_by, external_ref, metadata, reverses_id
) VALUES (
    @id, @reference, @kind, @correlation_id, @initiated_by, @external_ref, @metadata, @reverses_id
)
RETURNING *;

-- name: UpdateTransactionStatus :execrows
-- Compare-and-set: changes the status only if it is still from_status. Zero
-- rows means someone else changed it first.
UPDATE journal_transactions
SET status = @to_status
WHERE id = @id
  AND status = @from_status;

-- name: SetStatusReason :exec
-- The reason journal_status_audit stores for the next status changes. It lasts
-- until the end of the current database transaction, like SET LOCAL.
SELECT set_config('ledger.status_reason', @reason::text, true);

-- name: InsertOutboxEvent :exec
-- Required for every completed transaction (outbox_required).
INSERT INTO outbox_events (id, transaction_id, event_type, payload)
VALUES (@id, @transaction_id, @event_type, @payload);
