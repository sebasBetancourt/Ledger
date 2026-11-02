package ledger

import "errors"

// Callers compare these with errors.Is. Every error the package returns wraps
// one of them with context.
var (
	ErrInvalidRequest     = errors.New("ledger: invalid request")
	ErrUnbalanced         = errors.New("ledger: entries do not sum to zero")
	ErrAmountOverflow     = errors.New("ledger: amount overflows int64")
	ErrDuplicateReference = errors.New("ledger: duplicate reference")
	ErrInsufficientFunds  = errors.New("ledger: insufficient funds")
	ErrAccountNotFound    = errors.New("ledger: account not found")
	ErrAccountNotActive   = errors.New("ledger: account not active")
	ErrCurrencyMismatch   = errors.New("ledger: currency mismatch")
)
