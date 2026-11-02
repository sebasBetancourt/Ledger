package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sebasBetancourt/Ledger/internal/ledger"
	"github.com/sebasBetancourt/Ledger/internal/ledger/ledgerdb"
	"github.com/sebasBetancourt/Ledger/internal/platform/pgtest"
)

// Seeded by migrations/0003_system_accounts.up.sql.
var cashInCOP = uuid.MustParse("01900000-0000-7000-8000-000000000001")

func TestLedgerctl(t *testing.T) {
	pool := pgtest.New(t)
	t.Setenv("DATABASE_URL", pool.Config().ConnString())
	ana := fundedWallet(t, pool, 100_000)

	expect(t, 0, "all 7 invariants hold", "check-invariants")
	expect(t, 0, "no differences", "rebuild-balances", "--dry-run")

	corruptBalance(t, pool, ana, 5)
	expect(t, 1, "invariant 3", "check-invariants")
	expect(t, 1, ana.String(), "rebuild-balances", "--dry-run")
	// A dry run changes nothing.
	expect(t, 1, "invariant 3", "check-invariants")

	expect(t, 0, "rebuilt 1 balance", "rebuild-balances", "--apply")
	expect(t, 0, "all 7 invariants hold", "check-invariants")
}

func TestLedgerctlUsage(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://unused")

	expect(t, 2, "usage", "")
	expect(t, 2, "unknown command", "reticulate-splines")
	expect(t, 2, "exactly one of --dry-run or --apply", "rebuild-balances")
	expect(t, 2, "exactly one of --dry-run or --apply", "rebuild-balances", "--dry-run", "--apply")
}

func TestLedgerctlWithoutDatabaseURL(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	expect(t, 2, "DATABASE_URL", "check-invariants")
}

// expect runs ledgerctl with args and checks the exit code and that the output
// mentions want. An empty first arg means no arguments at all.
func expect(t *testing.T, code int, want string, args ...string) {
	t.Helper()

	if len(args) == 1 && args[0] == "" {
		args = nil
	}
	var stdout, stderr bytes.Buffer
	got := run(context.Background(), args, &stdout, &stderr)
	output := stdout.String() + stderr.String()
	if got != code || !strings.Contains(output, want) {
		t.Fatalf("ledgerctl %v: exit %d, want %d with %q\n%s", args, got, code, want, output)
	}
}

func fundedWallet(t *testing.T, pool *pgxpool.Pool, amount ledger.Amount) uuid.UUID {
	t.Helper()

	q := ledgerdb.New(pool)
	id := uuid.Must(uuid.NewV7())
	if _, err := q.CreateAccount(t.Context(), ledgerdb.CreateAccountParams{ID: id, Type: "user", Currency: "COP"}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.CreateBalance(t.Context(), ledgerdb.CreateBalanceParams{AccountID: id}); err != nil {
		t.Fatal(err)
	}
	_, err := ledger.NewService(pool, ledger.UUIDv7{}).Post(t.Context(), ledger.PostRequest{
		Reference: "fund", Kind: ledger.KindCashIn, Currency: "COP",
		CorrelationID: uuid.New(), InitiatedBy: "test",
		Entries: []ledger.Entry{{AccountID: cashInCOP, Amount: -amount}, {AccountID: id, Amount: amount}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// corruptBalance skips the balance_matches_ledger trigger, as an owner bypassing
// the defenses would.
func corruptBalance(t *testing.T, pool *pgxpool.Pool, account uuid.UUID, delta int64) {
	t.Helper()

	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	for _, statement := range []string{
		`ALTER TABLE balances DISABLE TRIGGER balance_matches_ledger`,
		`UPDATE balances SET available = available + $1 WHERE account_id = $2`,
		`ALTER TABLE balances ENABLE TRIGGER balance_matches_ledger`,
	} {
		var args []any
		if strings.HasPrefix(statement, "UPDATE") {
			args = []any{delta, account}
		}
		if _, err := tx.Exec(t.Context(), statement, args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
}
