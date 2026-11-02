package migrations_test

import (
	"testing"

	"github.com/sebasBetancourt/Ledger/internal/platform/pgtest"
)

func TestSystemAccountsSeeded(t *testing.T) {
	pool := pgtest.New(t)

	got := queryInt(t, pool, `
		SELECT count(*)
		FROM accounts a
		JOIN balances b ON b.account_id = a.id
		WHERE a.type = 'system'
		  AND a.allow_negative AND b.allow_negative
		  AND b.available = 0 AND b.locked = 0
		  AND a.code IN ('cash_in_clearing', 'cash_out_clearing', 'fee_revenue')
		  AND a.currency IN ('COP', 'USD')`)
	if got != 6 {
		t.Errorf("seeded system accounts = %d, want 6 (3 codes x 2 currencies)", got)
	}
}
