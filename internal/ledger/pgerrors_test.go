package ledger

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// Post's own checks catch these cases first, so a real database rarely
// returns them. The mapping is the safety net for a bug in Go.
func TestMapPgError(t *testing.T) {
	tests := []struct {
		code       string
		constraint string
		want       error
	}{
		{"23505", "journal_transactions_reference_unique", ErrDuplicateReference},
		{"23514", "balances_available_non_negative", ErrInsufficientFunds},
		{"23503", "postings_account_currency_fk", ErrCurrencyMismatch},
		{"23503", "balances_account_fk", ErrAccountNotFound},
		{"23000", "journal_balanced", ErrUnbalanced},
	}

	for _, tt := range tests {
		t.Run(tt.constraint, func(t *testing.T) {
			pgErr := &pgconn.PgError{Code: tt.code, ConstraintName: tt.constraint, Message: "from postgres"}
			got := mapPgError(fmt.Errorf("insert: %w", pgErr))

			if !errors.Is(got, tt.want) {
				t.Errorf("mapPgError = %v, want %v", got, tt.want)
			}
			// The original stays reachable for logs and debugging.
			var original *pgconn.PgError
			if !errors.As(got, &original) || original != pgErr {
				t.Errorf("original PgError lost: %v", got)
			}
		})
	}

	t.Run("unknown constraint passes through", func(t *testing.T) {
		err := &pgconn.PgError{Code: "23505", ConstraintName: "something_else"}
		got := mapPgError(err)
		if !errors.Is(got, err) || errors.Is(got, ErrDuplicateReference) {
			t.Errorf("mapPgError = %v, want the original error unmapped", got)
		}
	})
	t.Run("non-database error passes through", func(t *testing.T) {
		err := errors.New("network down")
		if got := mapPgError(err); !errors.Is(got, err) {
			t.Errorf("mapPgError = %v, want the original error", got)
		}
	})
}
