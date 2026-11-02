-- Ledger core: tables, constraints and immutability. Checks that need the whole
-- transaction run at COMMIT and live in 0002_ledger_triggers.
--
-- IDs are UUIDv7 generated in Go: PostgreSQL 16 has no native uuidv7().
-- Trigger failures carry a constraint name so Go can map them like any other
-- constraint violation.

CREATE TABLE currencies (
    code     TEXT PRIMARY KEY CONSTRAINT currencies_code_format CHECK (code ~ '^[A-Z]{3}$'),
    -- Decimal places of the minor unit. COP has 2, so $50,000 is 5000000.
    exponent SMALLINT NOT NULL CONSTRAINT currencies_exponent_range CHECK (exponent BETWEEN 0 AND 4)
);

INSERT INTO currencies (code, exponent) VALUES
    ('COP', 2),
    ('USD', 2);

CREATE TABLE accounts (
    id             UUID PRIMARY KEY,
    type           TEXT NOT NULL CONSTRAINT accounts_type_valid
                       CHECK (type IN ('user', 'merchant', 'system')),
    code           TEXT CONSTRAINT accounts_code_valid
                       CHECK (code IN ('cash_in_clearing', 'cash_out_clearing', 'fee_revenue')),
    currency       TEXT NOT NULL CONSTRAINT accounts_currency_fk REFERENCES currencies (code),
    status         TEXT NOT NULL DEFAULT 'active' CONSTRAINT accounts_status_valid
                       CHECK (status IN ('active', 'frozen', 'closed')),
    allow_negative BOOLEAN NOT NULL DEFAULT false,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT accounts_code_only_system CHECK ((type = 'system') = (code IS NOT NULL)),
    -- No customer ever gets an overdraft by accident.
    CONSTRAINT accounts_only_system_negative CHECK (NOT allow_negative OR type = 'system'),
    -- One account per system code and currency, so fee_revenue exists in COP and in USD.
    CONSTRAINT accounts_code_currency_unique UNIQUE (code, currency),
    -- Targets of composite foreign keys.
    CONSTRAINT accounts_id_currency_unique UNIQUE (id, currency),
    CONSTRAINT accounts_id_allow_negative_unique UNIQUE (id, allow_negative)
);

CREATE TABLE balances (
    account_id     UUID PRIMARY KEY,
    -- Copied from accounts because a CHECK cannot read another table. The
    -- composite foreign key keeps the copy honest.
    allow_negative BOOLEAN NOT NULL,
    available      BIGINT NOT NULL DEFAULT 0,
    locked         BIGINT NOT NULL DEFAULT 0,
    -- Counts every change to this row, holds included. Not concurrency control.
    version        BIGINT NOT NULL DEFAULT 0,
    -- seq of the latest posting for this account.
    last_seq       BIGINT NOT NULL DEFAULT 0,
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT balances_account_fk FOREIGN KEY (account_id, allow_negative)
        REFERENCES accounts (id, allow_negative),
    CONSTRAINT balances_available_non_negative CHECK (allow_negative OR available >= 0),
    CONSTRAINT balances_locked_non_negative CHECK (locked >= 0),
    CONSTRAINT balances_negative_never_locked CHECK (NOT allow_negative OR locked = 0)
);

CREATE TABLE journal_transactions (
    id             UUID PRIMARY KEY,
    reference      TEXT NOT NULL,
    kind           TEXT NOT NULL CONSTRAINT journal_transactions_kind_valid
                       CHECK (kind IN ('transfer', 'cash_in', 'cash_out', 'merchant_payment',
                                       'hold_capture', 'refund', 'reversal')),
    status         TEXT NOT NULL DEFAULT 'created' CONSTRAINT journal_transactions_status_valid
                       CHECK (status IN ('created', 'pending', 'completed', 'failed', 'reversed')),
    correlation_id UUID NOT NULL,
    -- Who started it: a client, an admin, or 'system' for workers.
    initiated_by   TEXT NOT NULL CONSTRAINT journal_transactions_initiated_by_present
                       CHECK (initiated_by <> ''),
    -- The provider's identifier for cash-in and cash-out.
    external_ref   TEXT,
    metadata       JSONB NOT NULL DEFAULT '{}',
    reverses_id    UUID CONSTRAINT journal_transactions_reverses_fk REFERENCES journal_transactions (id),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT journal_transactions_reference_unique UNIQUE (reference),
    CONSTRAINT journal_transactions_reverses_once UNIQUE (reverses_id),
    CONSTRAINT journal_transactions_reversal_target CHECK ((kind = 'reversal') = (reverses_id IS NOT NULL))
);

CREATE TABLE postings (
    id             UUID PRIMARY KEY,
    transaction_id UUID NOT NULL CONSTRAINT postings_transaction_fk REFERENCES journal_transactions (id),
    account_id     UUID NOT NULL,
    currency       TEXT NOT NULL,
    amount         BIGINT NOT NULL,
    -- Position in this account's chain: 1, 2, 3... with no gaps. API cursors use it.
    seq            BIGINT NOT NULL CONSTRAINT postings_seq_positive CHECK (seq >= 1),
    prev_seq       BIGINT,
    -- Total balance (available + locked) around this posting.
    balance_before BIGINT NOT NULL,
    balance_after  BIGINT NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT postings_amount_non_zero CHECK (amount <> 0),
    -- 2^53 - 1: the largest integer every JSON client reads exactly.
    CONSTRAINT postings_amount_limit CHECK (amount BETWEEN -9007199254740991 AND 9007199254740991),
    CONSTRAINT postings_balance_math CHECK (balance_after = balance_before + amount),
    CONSTRAINT postings_chain_start CHECK (
        (seq = 1 AND prev_seq IS NULL AND balance_before = 0)
        OR
        (seq > 1 AND prev_seq = seq - 1)
    ),
    -- A posting in COP cannot land on an account in USD.
    CONSTRAINT postings_account_currency_fk FOREIGN KEY (account_id, currency)
        REFERENCES accounts (id, currency),
    CONSTRAINT postings_account_seq_unique UNIQUE (account_id, seq),
    CONSTRAINT postings_chain_anchor UNIQUE (account_id, seq, balance_after),
    -- A posting must start exactly where the account's previous posting ended.
    -- The first posting has a NULL prev_seq, so this key does not apply to it.
    CONSTRAINT postings_chain_fk FOREIGN KEY (account_id, prev_seq, balance_before)
        REFERENCES postings (account_id, seq, balance_after)
);

-- Date-range queries. History pagination uses postings_account_seq_unique.
CREATE INDEX postings_account_created_idx ON postings (account_id, created_at, id);
CREATE INDEX postings_transaction_idx ON postings (transaction_id);

CREATE TABLE transaction_status_history (
    -- Written by a trigger, which has no UUIDv7 generator available.
    id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    transaction_id UUID NOT NULL CONSTRAINT transaction_status_history_transaction_fk
                       REFERENCES journal_transactions (id),
    from_status    TEXT,
    to_status      TEXT NOT NULL,
    reason         TEXT,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX transaction_status_history_transaction_idx
    ON transaction_status_history (transaction_id, id);

-- Minimal on purpose: phase 4 adds the publisher. It lives here so invariant 7
-- holds from the first transaction and history needs no backfill.
CREATE TABLE outbox_events (
    id             UUID PRIMARY KEY,
    transaction_id UUID NOT NULL CONSTRAINT outbox_events_transaction_fk
                       REFERENCES journal_transactions (id),
    event_type     TEXT NOT NULL CONSTRAINT outbox_events_event_type_valid
                       CHECK (event_type IN ('transaction.completed')),
    payload        JSONB NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at   TIMESTAMPTZ,

    CONSTRAINT outbox_events_one_per_transaction UNIQUE (transaction_id)
);

-- ---------------------------------------------------------------------------
-- Append-only: the past is never edited
-- ---------------------------------------------------------------------------

CREATE FUNCTION reject_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION '% on % is not allowed', TG_OP, TG_TABLE_NAME
        USING ERRCODE = 'integrity_constraint_violation', CONSTRAINT = TG_NAME;
END;
$$;

CREATE TRIGGER postings_immutable
    BEFORE UPDATE OR DELETE ON postings
    FOR EACH ROW EXECUTE FUNCTION reject_mutation();

CREATE TRIGGER postings_no_truncate
    BEFORE TRUNCATE ON postings
    FOR EACH STATEMENT EXECUTE FUNCTION reject_mutation();

CREATE TRIGGER transaction_status_history_immutable
    BEFORE UPDATE OR DELETE ON transaction_status_history
    FOR EACH ROW EXECUTE FUNCTION reject_mutation();

CREATE TRIGGER transaction_status_history_no_truncate
    BEFORE TRUNCATE ON transaction_status_history
    FOR EACH STATEMENT EXECUTE FUNCTION reject_mutation();

CREATE TRIGGER journal_transactions_no_delete
    BEFORE DELETE ON journal_transactions
    FOR EACH ROW EXECUTE FUNCTION reject_mutation();

CREATE TRIGGER journal_transactions_no_truncate
    BEFORE TRUNCATE ON journal_transactions
    FOR EACH STATEMENT EXECUTE FUNCTION reject_mutation();

-- Deleting a row would hide an account from balance_matches_ledger.
CREATE TRIGGER balances_no_delete
    BEFORE DELETE ON balances
    FOR EACH ROW EXECUTE FUNCTION reject_mutation();

CREATE TRIGGER balances_no_truncate
    BEFORE TRUNCATE ON balances
    FOR EACH STATEMENT EXECUTE FUNCTION reject_mutation();
