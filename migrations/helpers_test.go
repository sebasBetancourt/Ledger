package migrations_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sebasBetancourt/Ledger/internal/platform/pgtest"
)

// Fixture accounts. Every test gets a fresh database, so their chain state is
// known: after fixture, ana is at seq 1 with 10,000,000 and beto at seq 0 with 0.
const (
	ana         = "0190a000-0000-7000-8000-00000000000a"
	beto        = "0190a000-0000-7000-8000-00000000000b"
	funding     = "0190a000-0000-7000-8000-0000000000f0"
	correlation = "0190a000-0000-7000-8000-0000000000cc"
	txn         = "0190a000-0000-7000-8000-000000000100"

	anaFunds int64 = 10_000_000
)

// fixture returns a database with two COP wallets, ana funded from
// cash_in_clearing, and the clearing account's ID.
func fixture(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()
	pool := pgtest.New(t)

	commit(t, pool,
		wallet(ana, "user"),
		wallet(beto, "user"),
	)
	clearing := systemAccount(t, pool, "cash_in_clearing", "COP")
	commit(t, pool, transfer(funding, clearing, ana, anaFunds, 1, 0, 1, 0)...)
	return pool, clearing
}

// systemAccount returns the account for code and currency, creating it when
// no migration has seeded it yet.
func systemAccount(t *testing.T, pool *pgxpool.Pool, code, currency string) string {
	t.Helper()

	var id string
	err := pool.QueryRow(t.Context(), `
		INSERT INTO accounts (id, type, code, currency, allow_negative)
		VALUES (gen_random_uuid(), 'system', $1, $2, true)
		ON CONFLICT (code, currency) DO NOTHING
		RETURNING id`, code, currency).Scan(&id)
	if err == nil {
		commit(t, pool, fmt.Sprintf(`INSERT INTO balances (account_id, allow_negative) VALUES ('%s', true)`, id))
		return id
	}

	err = pool.QueryRow(t.Context(), `SELECT id FROM accounts WHERE code = $1 AND currency = $2`, code, currency).Scan(&id)
	if err != nil {
		t.Fatalf("system account %s %s: %v", code, currency, err)
	}
	return id
}

func wallet(id, accountType string) string {
	return fmt.Sprintf(`
		INSERT INTO accounts (id, type, currency) VALUES ('%[1]s', '%[2]s', 'COP');
		INSERT INTO balances (account_id, allow_negative) VALUES ('%[1]s', false);`, id, accountType)
}

func newTransaction(id, kind string) string {
	return fmt.Sprintf(`
		INSERT INTO journal_transactions (id, reference, kind, correlation_id, initiated_by)
		VALUES ('%s', 'ref:%[1]s', '%s', '%s', 'test')`, id, kind, correlation)
}

func setStatus(id, status string) string {
	return fmt.Sprintf(`UPDATE journal_transactions SET status = '%s' WHERE id = '%s'`, status, id)
}

// posting inserts the next link of an account's chain: seq follows the
// previous one and balance_before is where that one ended.
func posting(transaction, account string, amount, seq, before int64) string {
	prev := "NULL"
	if seq > 1 {
		prev = fmt.Sprint(seq - 1)
	}
	return fmt.Sprintf(`
		INSERT INTO postings (id, transaction_id, account_id, currency, amount, seq, prev_seq, balance_before, balance_after)
		VALUES (gen_random_uuid(), '%s', '%s', 'COP', %d, %d, %s, %d, %d)`,
		transaction, account, amount, seq, prev, before, before+amount)
}

func moveBalance(account string, delta, seq int64) string {
	return fmt.Sprintf(`
		UPDATE balances SET available = available + %d, version = version + 1, last_seq = %d
		WHERE account_id = '%s'`, delta, seq, account)
}

func outboxEvent(transaction string) string {
	return fmt.Sprintf(`
		INSERT INTO outbox_events (id, transaction_id, event_type, payload)
		VALUES (gen_random_uuid(), '%s', 'transaction.completed', '{}')`, transaction)
}

// transfer returns every statement of a valid, completed transfer.
func transfer(id, from, to string, amount, fromSeq, fromBefore, toSeq, toBefore int64) []string {
	return []string{
		newTransaction(id, "transfer"),
		setStatus(id, "pending"),
		posting(id, from, -amount, fromSeq, fromBefore),
		posting(id, to, amount, toSeq, toBefore),
		moveBalance(from, -amount, fromSeq),
		moveBalance(to, amount, toSeq),
		setStatus(id, "completed"),
		outboxEvent(id),
	}
}

// commit runs statements in one transaction and fails the test on any error.
func commit(t *testing.T, pool *pgxpool.Pool, statements ...string) {
	t.Helper()
	if err := run(t, pool, statements); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// rejected expects the transaction to fail on the named constraint or trigger,
// at any statement or at COMMIT.
func rejected(t *testing.T, pool *pgxpool.Pool, constraint string, statements ...string) {
	t.Helper()
	wantConstraint(t, run(t, pool, statements), constraint)
}

func run(t *testing.T, pool *pgxpool.Pool, statements []string) error {
	t.Helper()

	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()

	for _, statement := range statements {
		if _, err := tx.Exec(t.Context(), statement); err != nil {
			return err
		}
	}
	return tx.Commit(t.Context())
}

func wantConstraint(t *testing.T, err error, constraint string) {
	t.Helper()

	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("error = %v, want violation of %s", err, constraint)
	}
	if pgErr.ConstraintName != constraint {
		t.Fatalf("violated %q (%s), want %q", pgErr.ConstraintName, pgErr.Message, constraint)
	}
}

func queryInt(t *testing.T, pool *pgxpool.Pool, query string) int64 {
	t.Helper()

	var n int64
	if err := pool.QueryRow(t.Context(), query).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}
