package ledger

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Kind is the operation a transaction records. The values match the CHECK on
// journal_transactions.kind.
type Kind string

const (
	KindTransfer        Kind = "transfer"
	KindCashIn          Kind = "cash_in"
	KindCashOut         Kind = "cash_out"
	KindMerchantPayment Kind = "merchant_payment"
	KindHoldCapture     Kind = "hold_capture"
	KindRefund          Kind = "refund"
	KindReversal        Kind = "reversal"
)

// postableKinds are the kinds Post accepts. A reversal needs the transaction
// it reverses, so it arrives with the reversal flow.
var postableKinds = map[Kind]bool{
	KindTransfer:        true,
	KindCashIn:          true,
	KindCashOut:         true,
	KindMerchantPayment: true,
	KindHoldCapture:     true,
	KindRefund:          true,
}

const maxReferenceLength = 255

var currencyFormat = regexp.MustCompile(`^[A-Z]{3}$`)

// Entry is one line of a transaction: money in (positive) or out (negative)
// of one account.
type Entry struct {
	AccountID uuid.UUID
	Amount    Amount
}

// PostRequest asks the ledger to record one balanced transaction in a single
// currency.
type PostRequest struct {
	Reference     string
	Kind          Kind
	Currency      Currency
	CorrelationID uuid.UUID
	InitiatedBy   string
	// ExternalRef is the provider's identifier; empty means none.
	ExternalRef string
	// Metadata must be valid JSON; nil stores {}.
	Metadata json.RawMessage
	Entries  []Entry
}

// Validate checks the request without touching the database. Entries are
// summed with overflow checks before the per-entry limit is applied, so a sum
// that would wrap around is always reported as ErrAmountOverflow.
func (r PostRequest) Validate() error {
	switch {
	case strings.TrimSpace(r.Reference) == "":
		return fmt.Errorf("%w: reference is required", ErrInvalidRequest)
	case utf8.RuneCountInString(r.Reference) > maxReferenceLength:
		return fmt.Errorf("%w: reference is longer than %d characters", ErrInvalidRequest, maxReferenceLength)
	case !postableKinds[r.Kind]:
		return fmt.Errorf("%w: kind %q cannot be posted", ErrInvalidRequest, r.Kind)
	case !currencyFormat.MatchString(string(r.Currency)):
		return fmt.Errorf("%w: currency %q is not a 3-letter code", ErrInvalidRequest, r.Currency)
	case r.CorrelationID == uuid.Nil:
		return fmt.Errorf("%w: correlation ID is required", ErrInvalidRequest)
	case strings.TrimSpace(r.InitiatedBy) == "":
		return fmt.Errorf("%w: initiator is required", ErrInvalidRequest)
	case r.Metadata != nil && !json.Valid(r.Metadata):
		return fmt.Errorf("%w: metadata is not valid JSON", ErrInvalidRequest)
	case len(r.Entries) < 2:
		return fmt.Errorf("%w: needs at least 2 entries, has %d", ErrInvalidRequest, len(r.Entries))
	}

	seen := make(map[uuid.UUID]bool, len(r.Entries))
	var sum Amount
	for i, e := range r.Entries {
		switch {
		case e.AccountID == uuid.Nil:
			return fmt.Errorf("%w: entry %d has no account", ErrInvalidRequest, i)
		case e.Amount == 0:
			return fmt.Errorf("%w: entry %d has amount 0", ErrInvalidRequest, i)
		case seen[e.AccountID]:
			return fmt.Errorf("%w: account %s appears more than once", ErrInvalidRequest, e.AccountID)
		}
		seen[e.AccountID] = true

		var err error
		if sum, err = add(sum, e.Amount); err != nil {
			return err
		}
	}
	if sum != 0 {
		return fmt.Errorf("%w: entries sum to %d", ErrUnbalanced, sum)
	}

	for i, e := range r.Entries {
		if e.Amount > MaxAmount || e.Amount < -MaxAmount {
			return fmt.Errorf("%w: entry %d exceeds the maximum amount %d", ErrInvalidRequest, i, MaxAmount)
		}
	}
	return nil
}
