-- Undoes 0003_system_accounts.up.sql.
--
-- balances_no_delete is paused only for this delete. If any of these accounts
-- already has postings, the foreign keys stop the delete, which is correct: a
-- down migration must never destroy money.

ALTER TABLE balances DISABLE TRIGGER balances_no_delete;

DELETE FROM balances
WHERE account_id IN (
    '01900000-0000-7000-8000-000000000001',
    '01900000-0000-7000-8000-000000000002',
    '01900000-0000-7000-8000-000000000003',
    '01900000-0000-7000-8000-000000000004',
    '01900000-0000-7000-8000-000000000005',
    '01900000-0000-7000-8000-000000000006'
);

ALTER TABLE balances ENABLE TRIGGER balances_no_delete;

DELETE FROM accounts
WHERE id IN (
    '01900000-0000-7000-8000-000000000001',
    '01900000-0000-7000-8000-000000000002',
    '01900000-0000-7000-8000-000000000003',
    '01900000-0000-7000-8000-000000000004',
    '01900000-0000-7000-8000-000000000005',
    '01900000-0000-7000-8000-000000000006'
);
