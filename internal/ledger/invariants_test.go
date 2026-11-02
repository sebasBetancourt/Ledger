package ledger_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sebasBetancourt/Ledger/internal/ledger"
)

func TestCheckInvariantsOnHealthyLedger(t *testing.T) {
	t.Parallel()
	pool, svc, ana, beto := funded(t, 100_000)
	if _, err := svc.Post(t.Context(), request("p2p", ledger.KindTransfer, ana, -40_000, beto, 40_000)); err != nil {
		t.Fatal(err)
	}

	violations, err := ledger.CheckInvariants(t.Context(), pool)
	if err != nil {
		t.Fatalf("CheckInvariants: %v", err)
	}
	if len(violations) != 0 {
		t.Errorf("violations = %v, want none", violations)
	}
}

// The trigger balance_matches_ledger refuses a wrong balance, so the test
// disables it for one statement, as an owner skipping the defenses would.
// That is exactly the case rebuild-balances exists for.
func TestRebuildBalancesDetectsCorruption(t *testing.T) {
	t.Parallel()
	pool, _, ana, _ := funded(t, 100_000)
	corruptBalance(t, pool, ana, 1)

	violations, err := ledger.CheckInvariants(t.Context(), pool)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 1 || violations[0].Invariant != 3 {
		t.Fatalf("violations = %v, want one of invariant 3", violations)
	}

	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))

	diffs, err := ledger.RebuildBalances(t.Context(), pool, false, logger)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	want := ledger.BalanceDiff{AccountID: ana, Projected: 100_001, ProjectedSeq: 1, Ledger: 100_000, LedgerSeq: 1}
	if len(diffs) != 1 || diffs[0] != want {
		t.Fatalf("dry run diffs = %+v, want [%+v]", diffs, want)
	}
	if available, _ := readBalance(t, pool, ana); available != 100_001 {
		t.Errorf("dry run changed the balance to %d", available)
	}

	if _, err := ledger.RebuildBalances(t.Context(), pool, true, logger); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if available, version := readBalance(t, pool, ana); available != 100_000 || version != 2 {
		t.Errorf("after apply: available %d at version %d, want 100000 at version 2", available, version)
	}
	if !strings.Contains(logs.String(), `"msg":"balance rebuilt"`) || !strings.Contains(logs.String(), ana.String()) {
		t.Errorf("no log of the correction: %s", logs.String())
	}

	violations, err = ledger.CheckInvariants(t.Context(), pool)
	if err != nil || len(violations) != 0 {
		t.Errorf("after apply: violations = %v, %v, want none", violations, err)
	}
}

func TestRebuildBalancesOnHealthyLedgerChangesNothing(t *testing.T) {
	t.Parallel()
	pool := newDB(t)

	diffs, err := ledger.RebuildBalances(t.Context(), pool, true, slog.New(slog.DiscardHandler))
	if err != nil || len(diffs) != 0 {
		t.Errorf("diffs = %v, %v, want none", diffs, err)
	}
}

// readBalance reads without locking. The lock helper keeps its row locked until
// the test ends, which would make RebuildBalances wait forever.
func readBalance(t *testing.T, pool *pgxpool.Pool, account uuid.UUID) (available, version int64) {
	t.Helper()

	err := pool.QueryRow(t.Context(),
		`SELECT available, version FROM balances WHERE account_id = $1`, account).Scan(&available, &version)
	if err != nil {
		t.Fatal(err)
	}
	return available, version
}

func corruptBalance(t *testing.T, pool *pgxpool.Pool, account uuid.UUID, delta int64) {
	t.Helper()

	_, err := pool.Exec(context.Background(), `
		ALTER TABLE balances DISABLE TRIGGER balance_matches_ledger;
		UPDATE balances SET available = available + `+itoa(delta)+` WHERE account_id = '`+account.String()+`';
		ALTER TABLE balances ENABLE TRIGGER balance_matches_ledger;`)
	if err != nil {
		t.Fatalf("corrupt balance: %v", err)
	}
}
