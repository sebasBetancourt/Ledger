package ledger_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sebasBetancourt/Ledger/internal/ledger"
	"github.com/sebasBetancourt/Ledger/internal/platform/pgtest"
)

// newDB returns a fresh migrated database and checks all 7 invariants when the
// test ends, whatever the test did.
func newDB(t *testing.T) *pgxpool.Pool {
	t.Helper()

	pool := pgtest.New(t)
	t.Cleanup(func() { assertInvariants(t, pool) })
	return pool
}

func assertInvariants(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	// t.Context() is already canceled when cleanups run.
	violations, err := ledger.CheckInvariants(context.Background(), pool)
	if err != nil {
		t.Errorf("check invariants: %v", err)
		return
	}
	for _, v := range violations {
		t.Errorf("after the test, %v", v)
	}
}
