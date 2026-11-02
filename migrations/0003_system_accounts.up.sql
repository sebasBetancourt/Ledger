-- System accounts from docs/design/money-flows.md, one per code and currency.
-- They may go negative: cash_in_clearing mirrors money that came from outside.
--
-- The IDs are fixed so every environment has the same ones. Code should still
-- look accounts up by (code, currency).

INSERT INTO accounts (id, type, code, currency, allow_negative) VALUES
    ('01900000-0000-7000-8000-000000000001', 'system', 'cash_in_clearing',  'COP', true),
    ('01900000-0000-7000-8000-000000000002', 'system', 'cash_out_clearing', 'COP', true),
    ('01900000-0000-7000-8000-000000000003', 'system', 'fee_revenue',       'COP', true),
    ('01900000-0000-7000-8000-000000000004', 'system', 'cash_in_clearing',  'USD', true),
    ('01900000-0000-7000-8000-000000000005', 'system', 'cash_out_clearing', 'USD', true),
    ('01900000-0000-7000-8000-000000000006', 'system', 'fee_revenue',       'USD', true);

INSERT INTO balances (account_id, allow_negative)
SELECT id, allow_negative
FROM accounts
WHERE id IN (
    '01900000-0000-7000-8000-000000000001',
    '01900000-0000-7000-8000-000000000002',
    '01900000-0000-7000-8000-000000000003',
    '01900000-0000-7000-8000-000000000004',
    '01900000-0000-7000-8000-000000000005',
    '01900000-0000-7000-8000-000000000006'
);
