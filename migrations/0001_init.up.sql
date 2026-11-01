-- Ledger schema for v0.1. DRAFT: design artifact, see docs/design/data-model.md.
--
-- IDs are UUIDv7 generated in Go: PostgreSQL 16 has no native uuidv7().
-- Trigger failures carry a constraint name so Go can map them like any other
-- constraint violation.

-- ---------------------------------------------------------------------------
-- Tables
-- ---------------------------------------------------------------------------

CREATE TABLE accounts (
    id          UUID PRIMARY KEY,
    kind        TEXT NOT NULL CONSTRAINT accounts_kind_valid
                    CHECK (kind IN ('user', 'merchant', 'system')),
    status      TEXT NOT NULL DEFAULT 'active' CONSTRAINT accounts_status_valid
                    CHECK (status IN ('active', 'frozen', 'closed')),
    currency    TEXT NOT NULL CONSTRAINT accounts_currency_format
                    CHECK (currency ~ '^[A-Z]{3}$'),
    owner_id    UUID,
    alias       TEXT CONSTRAINT accounts_alias_format
                    CHECK (alias ~ '^[a-z0-9_.-]{3,32}$'),
    system_code TEXT CONSTRAINT accounts_system_code_valid
                    CHECK (system_code IN ('cash_in_clearing', 'cash_out_clearing', 'fee_revenue')),
    metadata    JSONB NOT NULL DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT accounts_identity_by_kind CHECK (
        (kind = 'system' AND system_code IS NOT NULL AND owner_id IS NULL AND alias IS NULL)
        OR
        (kind <> 'system' AND system_code IS NULL AND owner_id IS NOT NULL AND alias IS NOT NULL)
    ),
    CONSTRAINT accounts_alias_unique UNIQUE (alias),
    CONSTRAINT accounts_system_code_currency_unique UNIQUE (system_code, currency),
    CONSTRAINT accounts_owner_wallet_unique UNIQUE (owner_id, kind, currency),
    -- Targets of composite foreign keys.
    CONSTRAINT accounts_id_kind_unique UNIQUE (id, kind),
    CONSTRAINT accounts_id_currency_unique UNIQUE (id, currency)
);

CREATE TABLE balances (
    account_id   UUID PRIMARY KEY,
    -- Copied from accounts because a CHECK cannot read another table. The
    -- composite foreign key keeps the copy honest.
    account_kind TEXT NOT NULL,
    available    BIGINT NOT NULL DEFAULT 0,
    locked       BIGINT NOT NULL DEFAULT 0,
    -- Counts every change to this row, holds included. Not concurrency control.
    version      BIGINT NOT NULL DEFAULT 0,
    -- seq of the latest posting for this account.
    last_seq     BIGINT NOT NULL DEFAULT 0,
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT balances_account_fk FOREIGN KEY (account_id, account_kind)
        REFERENCES accounts (id, kind),
    CONSTRAINT balances_available_non_negative
        CHECK (account_kind = 'system' OR available >= 0),
    CONSTRAINT balances_locked_non_negative CHECK (locked >= 0),
    CONSTRAINT balances_system_never_locked CHECK (account_kind <> 'system' OR locked = 0)
);

CREATE TABLE journal_transactions (
    id                      UUID PRIMARY KEY,
    -- '<client_id>:<client reference>' so two clients cannot collide.
    reference               TEXT NOT NULL,
    origin                  TEXT NOT NULL CONSTRAINT journal_transactions_origin_valid
                                CHECK (origin IN ('transfer', 'reversal', 'cash_in')),
    status                  TEXT NOT NULL DEFAULT 'created' CONSTRAINT journal_transactions_status_valid
                                CHECK (status IN ('created', 'pending', 'completed', 'failed', 'reversed')),
    reverses_transaction_id UUID REFERENCES journal_transactions (id),
    correlation_id          UUID NOT NULL,
    metadata                JSONB NOT NULL DEFAULT '{}',
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT journal_transactions_reference_unique UNIQUE (reference),
    CONSTRAINT journal_transactions_reverses_once UNIQUE (reverses_transaction_id),
    CONSTRAINT journal_transactions_reversal_target CHECK (
        (origin = 'reversal') = (reverses_transaction_id IS NOT NULL)
    )
);

CREATE TABLE postings (
    id                     UUID PRIMARY KEY,
    journal_transaction_id UUID NOT NULL REFERENCES journal_transactions (id),
    account_id             UUID NOT NULL,
    currency               TEXT NOT NULL,
    amount                 BIGINT NOT NULL,
    -- Position in this account's chain: 1, 2, 3... with no gaps. API cursors use it.
    seq                    BIGINT NOT NULL CONSTRAINT postings_seq_positive CHECK (seq >= 1),
    prev_seq               BIGINT,
    -- Total balance (available + locked) around this posting.
    balance_before         BIGINT NOT NULL,
    balance_after          BIGINT NOT NULL,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT postings_amount_non_zero CHECK (amount <> 0),
    -- 2^53 - 1: the largest integer every JSON client reads exactly.
    CONSTRAINT postings_amount_limit
        CHECK (amount BETWEEN -9007199254740991 AND 9007199254740991),
    CONSTRAINT postings_balance_math CHECK (balance_after = balance_before + amount),
    CONSTRAINT postings_chain_start CHECK (
        (seq = 1 AND prev_seq IS NULL AND balance_before = 0)
        OR
        (seq > 1 AND prev_seq = seq - 1)
    ),
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
CREATE INDEX postings_journal_transaction_idx ON postings (journal_transaction_id);

CREATE TABLE transaction_status_history (
    -- Written by a trigger, which has no UUIDv7 generator available.
    id                     BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    journal_transaction_id UUID NOT NULL REFERENCES journal_transactions (id),
    from_status            TEXT,
    to_status              TEXT NOT NULL,
    changed_at             TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX transaction_status_history_journal_transaction_idx
    ON transaction_status_history (journal_transaction_id, id);

CREATE TABLE transfers (
    id                     UUID PRIMARY KEY,
    -- Status lives on the journal transaction; a second copy could drift.
    journal_transaction_id UUID NOT NULL REFERENCES journal_transactions (id),
    client_id              TEXT NOT NULL,
    from_account_id        UUID NOT NULL,
    to_account_id          UUID NOT NULL,
    currency               TEXT NOT NULL,
    amount                 BIGINT NOT NULL,
    failure_code           TEXT CONSTRAINT transfers_failure_code_valid
                               CHECK (failure_code IN ('insufficient_funds', 'account_frozen')),
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT transfers_journal_transaction_unique UNIQUE (journal_transaction_id),
    CONSTRAINT transfers_distinct_accounts CHECK (from_account_id <> to_account_id),
    CONSTRAINT transfers_amount_positive
        CHECK (amount > 0 AND amount <= 9007199254740991),
    CONSTRAINT transfers_from_account_fk FOREIGN KEY (from_account_id, currency)
        REFERENCES accounts (id, currency),
    CONSTRAINT transfers_to_account_fk FOREIGN KEY (to_account_id, currency)
        REFERENCES accounts (id, currency)
);

CREATE INDEX transfers_from_account_idx ON transfers (from_account_id, created_at, id);
CREATE INDEX transfers_to_account_idx ON transfers (to_account_id, created_at, id);

CREATE TABLE idempotency_keys (
    id                     UUID PRIMARY KEY,
    client_id              TEXT NOT NULL,
    key                    TEXT NOT NULL CONSTRAINT idempotency_keys_key_length
                               CHECK (char_length(key) BETWEEN 1 AND 255),
    -- SHA-256 of the canonical request, to catch a key reused with another body.
    request_hash           BYTEA NOT NULL CONSTRAINT idempotency_keys_request_hash_length
                               CHECK (octet_length(request_hash) = 32),
    -- Filled before COMMIT, in the same transaction that moves the money. The
    -- row is inserted first so a concurrent duplicate waits on the unique key.
    response_status        INTEGER,
    response_body          JSONB,
    journal_transaction_id UUID REFERENCES journal_transactions (id),
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at             TIMESTAMPTZ NOT NULL,

    CONSTRAINT idempotency_keys_client_key_unique UNIQUE (client_id, key),
    CONSTRAINT idempotency_keys_expiry_after_creation CHECK (expires_at > created_at)
);

CREATE INDEX idempotency_keys_expires_idx ON idempotency_keys (expires_at);

-- Minimal on purpose: phase 4 adds the publisher. It lives here so invariant 7
-- holds from the first transaction and history needs no backfill.
CREATE TABLE outbox_events (
    id                     UUID PRIMARY KEY,
    journal_transaction_id UUID NOT NULL REFERENCES journal_transactions (id),
    event_type             TEXT NOT NULL CONSTRAINT outbox_events_event_type_valid
                               CHECK (event_type IN ('transaction.completed')),
    payload                JSONB NOT NULL,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at           TIMESTAMPTZ,

    CONSTRAINT outbox_events_one_per_transaction UNIQUE (journal_transaction_id)
);

-- ---------------------------------------------------------------------------
-- Append-only tables
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

-- ---------------------------------------------------------------------------
-- Transaction state machine
-- ---------------------------------------------------------------------------

CREATE FUNCTION check_journal_status() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.status <> 'created' THEN
            RAISE EXCEPTION 'journal transaction % must start as created', NEW.id
                USING ERRCODE = 'integrity_constraint_violation', CONSTRAINT = TG_NAME;
        END IF;
        RETURN NEW;
    END IF;

    IF to_jsonb(NEW) - 'status' IS DISTINCT FROM to_jsonb(OLD) - 'status' THEN
        RAISE EXCEPTION 'journal transaction % only allows status changes', OLD.id
            USING ERRCODE = 'integrity_constraint_violation', CONSTRAINT = TG_NAME;
    END IF;

    IF (OLD.status, NEW.status) NOT IN (
        ('created', 'pending'),
        ('pending', 'completed'),
        ('pending', 'failed'),
        ('completed', 'reversed')
    ) THEN
        RAISE EXCEPTION 'journal transaction % cannot go from % to %', OLD.id, OLD.status, NEW.status
            USING ERRCODE = 'integrity_constraint_violation', CONSTRAINT = TG_NAME;
    END IF;

    IF NEW.status = 'reversed' AND OLD.origin = 'reversal' THEN
        RAISE EXCEPTION 'reversal % cannot be reversed', OLD.id
            USING ERRCODE = 'integrity_constraint_violation', CONSTRAINT = TG_NAME;
    END IF;

    RETURN NEW;
END;
$$;

CREATE TRIGGER journal_status_guard
    BEFORE INSERT OR UPDATE ON journal_transactions
    FOR EACH ROW EXECUTE FUNCTION check_journal_status();

CREATE FUNCTION record_journal_status() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    previous_status TEXT;
BEGIN
    IF TG_OP = 'UPDATE' THEN
        previous_status := OLD.status;
    END IF;

    INSERT INTO transaction_status_history (journal_transaction_id, from_status, to_status)
    VALUES (NEW.id, previous_status, NEW.status);

    RETURN NULL;
END;
$$;

CREATE TRIGGER journal_status_audit
    AFTER INSERT OR UPDATE ON journal_transactions
    FOR EACH ROW EXECUTE FUNCTION record_journal_status();

-- Once a transaction leaves pending, nobody can add postings to it, not even
-- balanced ones from a later database transaction.
CREATE FUNCTION check_posting_target() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    target_status TEXT;
BEGIN
    SELECT status INTO target_status
    FROM journal_transactions
    WHERE id = NEW.journal_transaction_id;

    IF target_status IS DISTINCT FROM 'pending' THEN
        RAISE EXCEPTION 'journal transaction % is not pending', NEW.journal_transaction_id
            USING ERRCODE = 'integrity_constraint_violation', CONSTRAINT = TG_NAME;
    END IF;

    RETURN NEW;
END;
$$;

CREATE TRIGGER postings_require_pending
    BEFORE INSERT ON postings
    FOR EACH ROW EXECUTE FUNCTION check_posting_target();

-- ---------------------------------------------------------------------------
-- Deferred checks: these run at COMMIT, when the whole transaction is visible
-- ---------------------------------------------------------------------------

CREATE FUNCTION check_journal_balanced() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    transaction_id UUID;
    current_status TEXT;
    posting_count  BIGINT;
BEGIN
    IF TG_TABLE_NAME = 'postings' THEN
        transaction_id := NEW.journal_transaction_id;
    ELSE
        transaction_id := NEW.id;
    END IF;

    SELECT status INTO current_status
    FROM journal_transactions
    WHERE id = transaction_id;

    SELECT count(*) INTO posting_count
    FROM postings
    WHERE journal_transaction_id = transaction_id;

    IF current_status IN ('completed', 'reversed') THEN
        IF posting_count < 2 THEN
            RAISE EXCEPTION 'journal transaction % has % postings, needs at least 2',
                transaction_id, posting_count
                USING ERRCODE = 'integrity_constraint_violation', CONSTRAINT = 'journal_balanced';
        END IF;

        IF EXISTS (
            SELECT 1
            FROM postings
            WHERE journal_transaction_id = transaction_id
            GROUP BY currency
            HAVING sum(amount) <> 0
        ) THEN
            RAISE EXCEPTION 'journal transaction % does not sum to zero', transaction_id
                USING ERRCODE = 'integrity_constraint_violation', CONSTRAINT = 'journal_balanced';
        END IF;
    ELSIF posting_count > 0 THEN
        RAISE EXCEPTION 'journal transaction % is % but has postings', transaction_id, current_status
            USING ERRCODE = 'integrity_constraint_violation', CONSTRAINT = 'journal_balanced';
    END IF;

    RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER journal_balanced
    AFTER INSERT ON postings
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION check_journal_balanced();

-- Also fires on the transaction itself: a trigger on postings alone would never
-- run for a transaction completed with no postings.
CREATE CONSTRAINT TRIGGER journal_balanced
    AFTER INSERT OR UPDATE ON journal_transactions
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION check_journal_balanced();

-- O(1) by construction: the chain starts at 0 and each link adds its amount, so
-- the last balance_after is already the sum of the account's postings.
CREATE FUNCTION check_balance_matches_ledger() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    projected_total BIGINT;
    projected_seq   BIGINT;
    ledger_total    BIGINT := 0;
    ledger_seq      BIGINT := 0;
BEGIN
    SELECT available + locked, last_seq INTO projected_total, projected_seq
    FROM balances
    WHERE account_id = NEW.account_id;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'account % has no balance row', NEW.account_id
            USING ERRCODE = 'integrity_constraint_violation', CONSTRAINT = 'balance_matches_ledger';
    END IF;

    SELECT balance_after, seq INTO ledger_total, ledger_seq
    FROM postings
    WHERE account_id = NEW.account_id
    ORDER BY seq DESC
    LIMIT 1;

    IF NOT FOUND THEN
        ledger_total := 0;
        ledger_seq := 0;
    END IF;

    IF projected_total <> ledger_total OR projected_seq <> ledger_seq THEN
        RAISE EXCEPTION 'balance of account % is % at seq %, ledger says % at seq %',
            NEW.account_id, projected_total, projected_seq, ledger_total, ledger_seq
            USING ERRCODE = 'integrity_constraint_violation', CONSTRAINT = 'balance_matches_ledger';
    END IF;

    RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER balance_matches_ledger
    AFTER INSERT ON postings
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION check_balance_matches_ledger();

CREATE CONSTRAINT TRIGGER balance_matches_ledger
    AFTER INSERT OR UPDATE ON balances
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION check_balance_matches_ledger();

CREATE FUNCTION check_outbox_event() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    current_status TEXT;
BEGIN
    SELECT status INTO current_status
    FROM journal_transactions
    WHERE id = NEW.id;

    IF current_status IN ('completed', 'reversed') AND NOT EXISTS (
        SELECT 1 FROM outbox_events WHERE journal_transaction_id = NEW.id
    ) THEN
        RAISE EXCEPTION 'journal transaction % has no outbox event', NEW.id
            USING ERRCODE = 'integrity_constraint_violation', CONSTRAINT = 'outbox_required';
    END IF;

    RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER outbox_required
    AFTER INSERT OR UPDATE ON journal_transactions
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION check_outbox_event();
