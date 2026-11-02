package migrations_test

import (
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestZeroSumAtCommit(t *testing.T) {
	pool, _ := fixture(t)

	t.Run("unbalanced transaction", func(t *testing.T) {
		rejectedAtCommit(t, pool, "journal_balanced",
			newTransaction(txn, "transfer"),
			setStatus(txn, "pending"),
			posting(txn, ana, -5, 2, anaFunds),
			posting(txn, beto, 4, 1, 0),
			moveBalance(ana, -5, 2),
			moveBalance(beto, 4, 1),
			setStatus(txn, "completed"),
			outboxEvent(txn))
	})
	t.Run("completed with no postings", func(t *testing.T) {
		rejectedAtCommit(t, pool, "journal_balanced",
			newTransaction(txn, "transfer"),
			setStatus(txn, "pending"),
			setStatus(txn, "completed"),
			outboxEvent(txn))
	})
	t.Run("failed with postings", func(t *testing.T) {
		rejectedAtCommit(t, pool, "journal_balanced",
			newTransaction(txn, "transfer"),
			setStatus(txn, "pending"),
			posting(txn, ana, -5, 2, anaFunds),
			moveBalance(ana, -5, 2),
			setStatus(txn, "failed"))
	})
}

func TestPostingsOnlyWhilePending(t *testing.T) {
	pool, _ := fixture(t)

	rejected(t, pool, "postings_require_pending", posting(funding, ana, -5, 2, anaFunds))
}

func TestBalanceMatchesLedger(t *testing.T) {
	pool, _ := fixture(t)

	t.Run("projection edited by hand", func(t *testing.T) {
		rejectedAtCommit(t, pool, "balance_matches_ledger",
			fmt.Sprintf(`UPDATE balances SET available = available + 1 WHERE account_id = '%s'`, ana))
	})
	t.Run("postings without projection update", func(t *testing.T) {
		rejectedAtCommit(t, pool, "balance_matches_ledger",
			newTransaction(txn, "transfer"),
			setStatus(txn, "pending"),
			posting(txn, ana, -5, 2, anaFunds),
			posting(txn, beto, 5, 1, 0),
			setStatus(txn, "completed"),
			outboxEvent(txn))
	})
}

func TestOutboxRequired(t *testing.T) {
	pool, _ := fixture(t)

	statements := transfer(txn, ana, beto, 5, 2, anaFunds, 1, 0)
	rejectedAtCommit(t, pool, "outbox_required", statements[:len(statements)-1]...)
}

func TestStateMachine(t *testing.T) {
	pool, _ := fixture(t)

	tests := []struct {
		name      string
		statement string
	}{
		{"insert not created", fmt.Sprintf(`
			INSERT INTO journal_transactions (id, reference, kind, status, correlation_id, initiated_by)
			VALUES ('%s', 'ref:x', 'transfer', 'completed', '%s', 'test')`, txn, correlation)},
		{"completed back to pending", setStatus(funding, "pending")},
		{"completed to failed", setStatus(funding, "failed")},
		{"edit a column other than status", fmt.Sprintf(`
			UPDATE journal_transactions SET reference = 'edited' WHERE id = '%s'`, funding)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rejected(t, pool, "journal_status_guard", tt.statement)
		})
	}
}

func TestReversal(t *testing.T) {
	pool, _ := fixture(t)
	const reversal = "0190a000-0000-7000-8000-000000000201"

	commit(t, pool, transfer(txn, ana, beto, 5_000_000, 2, anaFunds, 1, 0)...)
	commit(t, pool,
		fmt.Sprintf(`
			INSERT INTO journal_transactions (id, reference, kind, correlation_id, initiated_by, reverses_id)
			VALUES ('%s', 'ref:%[1]s', 'reversal', '%s', 'test', '%s')`, reversal, correlation, txn),
		setStatus(reversal, "pending"),
		posting(reversal, ana, 5_000_000, 3, 5_000_000),
		posting(reversal, beto, -5_000_000, 2, 5_000_000),
		moveBalance(ana, 5_000_000, 3),
		moveBalance(beto, -5_000_000, 2),
		setStatus(reversal, "completed"),
		setStatus(txn, "reversed"),
		outboxEvent(reversal))

	if got := queryInt(t, pool, fmt.Sprintf(`SELECT available FROM balances WHERE account_id = '%s'`, ana)); got != anaFunds {
		t.Errorf("ana available = %d, want %d", got, anaFunds)
	}

	t.Run("a reversal cannot be reversed", func(t *testing.T) {
		rejected(t, pool, "journal_status_guard", setStatus(reversal, "reversed"))
	})
}

func TestFailedTransactionCommits(t *testing.T) {
	pool, _ := fixture(t)

	commit(t, pool,
		newTransaction(txn, "transfer"),
		setStatus(txn, "pending"),
		`SET LOCAL ledger.status_reason = 'insufficient_funds'`,
		setStatus(txn, "failed"))

	var history []string
	rows, err := pool.Query(t.Context(), fmt.Sprintf(`
		SELECT coalesce(from_status, '-') || '>' || to_status || ':' || coalesce(reason, '-')
		FROM transaction_status_history WHERE transaction_id = '%s' ORDER BY id`, txn))
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var entry string
		if err := rows.Scan(&entry); err != nil {
			t.Fatal(err)
		}
		history = append(history, entry)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	want := []string{"->created:-", "created>pending:-", "pending>failed:insufficient_funds"}
	if fmt.Sprint(history) != fmt.Sprint(want) {
		t.Errorf("history = %v, want %v", history, want)
	}
}

// rejectedAtCommit expects every statement to succeed and COMMIT to fail.
// This is what proves a check is deferred.
func rejectedAtCommit(t *testing.T, pool *pgxpool.Pool, constraint string, statements ...string) {
	t.Helper()

	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()

	for _, statement := range statements {
		if _, err := tx.Exec(t.Context(), statement); err != nil {
			t.Fatalf("statement failed before COMMIT: %v\n%s", err, statement)
		}
	}
	wantConstraint(t, tx.Commit(t.Context()), constraint)
}
