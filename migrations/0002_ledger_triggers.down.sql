-- Undoes 0002_ledger_triggers.up.sql.

DROP TRIGGER IF EXISTS outbox_required ON journal_transactions;
DROP TRIGGER IF EXISTS balance_matches_ledger ON balances;
DROP TRIGGER IF EXISTS balance_matches_ledger ON postings;
DROP TRIGGER IF EXISTS journal_balanced ON journal_transactions;
DROP TRIGGER IF EXISTS journal_balanced ON postings;
DROP TRIGGER IF EXISTS postings_require_pending ON postings;
DROP TRIGGER IF EXISTS journal_status_audit ON journal_transactions;
DROP TRIGGER IF EXISTS journal_status_guard ON journal_transactions;

DROP FUNCTION IF EXISTS
    check_outbox_event(),
    check_balance_matches_ledger(),
    check_journal_balanced(),
    check_posting_target(),
    record_journal_status(),
    check_journal_status();
