-- Checks that cannot be a CHECK or a foreign key: the state machine, the
-- status history, and the checks that need the whole transaction at COMMIT.

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

    IF NEW.status = 'reversed' AND OLD.kind = 'reversal' THEN
        RAISE EXCEPTION 'reversal % cannot be reversed', OLD.id
            USING ERRCODE = 'integrity_constraint_violation', CONSTRAINT = TG_NAME;
    END IF;

    RETURN NEW;
END;
$$;

CREATE TRIGGER journal_status_guard
    BEFORE INSERT OR UPDATE ON journal_transactions
    FOR EACH ROW EXECUTE FUNCTION check_journal_status();

-- The reason comes from SET LOCAL ledger.status_reason, which lasts only until
-- the end of the database transaction. Without it, reason stays NULL.
CREATE FUNCTION record_journal_status() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    previous_status TEXT;
BEGIN
    IF TG_OP = 'UPDATE' THEN
        previous_status := OLD.status;
    END IF;

    INSERT INTO transaction_status_history (transaction_id, from_status, to_status, reason)
    VALUES (NEW.id, previous_status, NEW.status,
            NULLIF(current_setting('ledger.status_reason', true), ''));

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
    WHERE id = NEW.transaction_id;

    IF target_status IS DISTINCT FROM 'pending' THEN
        RAISE EXCEPTION 'journal transaction % is not pending', NEW.transaction_id
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
    target_id      UUID;
    current_status TEXT;
    posting_count  BIGINT;
BEGIN
    IF TG_TABLE_NAME = 'postings' THEN
        target_id := NEW.transaction_id;
    ELSE
        target_id := NEW.id;
    END IF;

    SELECT status INTO current_status
    FROM journal_transactions
    WHERE id = target_id;

    SELECT count(*) INTO posting_count
    FROM postings
    WHERE transaction_id = target_id;

    IF current_status IN ('completed', 'reversed') THEN
        IF posting_count < 2 THEN
            RAISE EXCEPTION 'journal transaction % has % postings, needs at least 2',
                target_id, posting_count
                USING ERRCODE = 'integrity_constraint_violation', CONSTRAINT = 'journal_balanced';
        END IF;

        IF EXISTS (
            SELECT 1
            FROM postings
            WHERE transaction_id = target_id
            GROUP BY currency
            HAVING sum(amount) <> 0
        ) THEN
            RAISE EXCEPTION 'journal transaction % does not sum to zero', target_id
                USING ERRCODE = 'integrity_constraint_violation', CONSTRAINT = 'journal_balanced';
        END IF;
    ELSIF posting_count > 0 THEN
        RAISE EXCEPTION 'journal transaction % is % but has postings', target_id, current_status
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
        SELECT 1 FROM outbox_events WHERE transaction_id = NEW.id
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
