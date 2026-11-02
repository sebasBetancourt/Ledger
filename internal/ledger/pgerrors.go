package ledger

import (
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
)

// constraintErrors maps a constraint or trigger name from the migrations to its
// domain error. Names are stable; messages and even SQLSTATE codes are not:
// our triggers raise 23000, not 23514.
//
// postings_account_currency_fk covers both a missing account and a currency
// mismatch. Post checks existence first, so by then only the currency can fail.
var constraintErrors = map[string]error{
	"journal_transactions_reference_unique": ErrDuplicateReference,
	"balances_available_non_negative":       ErrInsufficientFunds,
	"postings_account_currency_fk":          ErrCurrencyMismatch,
	"balances_account_fk":                   ErrAccountNotFound,
	"journal_balanced":                      ErrUnbalanced,
}

// mapPgError turns a known constraint violation into its domain error, keeping
// the original reachable with errors.As. Anything else passes through.
func mapPgError(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	domain, ok := constraintErrors[pgErr.ConstraintName]
	if !ok {
		return err
	}
	return fmt.Errorf("%w: %w", domain, err)
}
