package ledger_test

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/sebasBetancourt/Ledger/internal/ledger"
)

// No test here touches the database.
func TestValidate(t *testing.T) {
	a, b, c := uuid.New(), uuid.New(), uuid.New()

	tests := []struct {
		name   string
		modify func(*ledger.PostRequest)
		want   error
	}{
		{"valid P2P", func(*ledger.PostRequest) {}, nil},
		{"valid payment with fee", func(r *ledger.PostRequest) {
			r.Kind = ledger.KindMerchantPayment
			r.Entries = entries(a, -10_000, b, 9_800, c, 200)
		}, nil},
		{"amounts at the exact limit", func(r *ledger.PostRequest) {
			r.Entries = entries(a, -ledger.MaxAmount, b, ledger.MaxAmount)
		}, nil},
		{"no entries", func(r *ledger.PostRequest) { r.Entries = nil }, ledger.ErrInvalidRequest},
		{"one entry", func(r *ledger.PostRequest) { r.Entries = entries(a, 100) }, ledger.ErrInvalidRequest},
		{"zero amount", func(r *ledger.PostRequest) {
			r.Entries = entries(a, -100, b, 100, c, 0)
		}, ledger.ErrInvalidRequest},
		{"does not sum to zero", func(r *ledger.PostRequest) {
			r.Entries = entries(a, -100, b, 99)
		}, ledger.ErrUnbalanced},
		{"empty reference", func(r *ledger.PostRequest) { r.Reference = "" }, ledger.ErrInvalidRequest},
		{"blank reference", func(r *ledger.PostRequest) { r.Reference = "   " }, ledger.ErrInvalidRequest},
		{"reference too long", func(r *ledger.PostRequest) {
			r.Reference = strings.Repeat("x", 256)
		}, ledger.ErrInvalidRequest},
		{"unknown kind", func(r *ledger.PostRequest) { r.Kind = "gift" }, ledger.ErrInvalidRequest},
		{"reversal kind", func(r *ledger.PostRequest) { r.Kind = ledger.KindReversal }, ledger.ErrInvalidRequest},
		{"lowercase currency", func(r *ledger.PostRequest) { r.Currency = "cop" }, ledger.ErrInvalidRequest},
		{"missing correlation ID", func(r *ledger.PostRequest) { r.CorrelationID = uuid.Nil }, ledger.ErrInvalidRequest},
		{"missing initiator", func(r *ledger.PostRequest) { r.InitiatedBy = " " }, ledger.ErrInvalidRequest},
		{"invalid metadata", func(r *ledger.PostRequest) { r.Metadata = json.RawMessage(`{`) }, ledger.ErrInvalidRequest},
		{"same account twice", func(r *ledger.PostRequest) {
			r.Entries = entries(a, -100, a, 100)
		}, ledger.ErrInvalidRequest},
		{"missing account ID", func(r *ledger.PostRequest) {
			r.Entries = entries(a, -100, uuid.Nil, 100)
		}, ledger.ErrInvalidRequest},
		// In plain Go, MaxInt64 + MaxInt64 wraps to -2, and -2 + 2 is 0: a naive
		// check would call this balanced and create money from nothing.
		{"positive overflow that wraps to zero", func(r *ledger.PostRequest) {
			r.Entries = entries(a, math.MaxInt64, b, math.MaxInt64, c, 2)
		}, ledger.ErrAmountOverflow},
		{"negative overflow", func(r *ledger.PostRequest) {
			r.Entries = entries(a, math.MinInt64, b, -1, c, 1)
		}, ledger.ErrAmountOverflow},
		{"amount above the limit, even if balanced", func(r *ledger.PostRequest) {
			r.Entries = entries(a, -(ledger.MaxAmount + 1), b, ledger.MaxAmount+1)
		}, ledger.ErrInvalidRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := validRequest(a, b)
			tt.modify(&req)

			err := req.Validate()
			if tt.want == nil {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tt.want) {
				t.Fatalf("Validate() = %v, want %v", err, tt.want)
			}
		})
	}
}

func validRequest(from, to uuid.UUID) ledger.PostRequest {
	return ledger.PostRequest{
		Reference:     "order-1001",
		Kind:          ledger.KindTransfer,
		Currency:      "COP",
		CorrelationID: uuid.New(),
		InitiatedBy:   "test",
		Entries:       entries(from, -5_000_000, to, 5_000_000),
	}
}

// entries builds entries from (account, amount) pairs.
func entries(pairs ...any) []ledger.Entry {
	var out []ledger.Entry
	for len(pairs) >= 2 {
		out = append(out, ledger.Entry{
			AccountID: pairs[0].(uuid.UUID),
			Amount:    toAmount(pairs[1]),
		})
		pairs = pairs[2:]
	}
	return out
}

func toAmount(v any) ledger.Amount {
	switch n := v.(type) {
	case int:
		return ledger.Amount(n)
	case ledger.Amount:
		return n
	}
	panic("unsupported amount type")
}
