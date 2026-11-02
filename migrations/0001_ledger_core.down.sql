-- Undoes 0001_ledger_core.up.sql. Dropping a table also drops its triggers.

DROP TABLE IF EXISTS
    outbox_events,
    transaction_status_history,
    postings,
    journal_transactions,
    balances,
    accounts,
    currencies;

DROP FUNCTION IF EXISTS reject_mutation();
