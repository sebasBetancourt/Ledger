-- Undoes 0001_init.up.sql. Dropping a table also drops its triggers.

DROP TABLE IF EXISTS
    outbox_events,
    idempotency_keys,
    transfers,
    transaction_status_history,
    postings,
    balances,
    journal_transactions,
    accounts;

DROP FUNCTION IF EXISTS
    reject_mutation(),
    check_journal_status(),
    record_journal_status(),
    check_posting_target(),
    check_journal_balanced(),
    check_balance_matches_ledger(),
    check_outbox_event();
