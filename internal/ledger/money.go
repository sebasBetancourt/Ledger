package ledger

import (
	"fmt"
	"math"
)

// Amount is a signed amount in a currency's minor units. COP has 2 decimals,
// so $50,000 is 5_000_000.
type Amount int64

// Currency is an ISO 4217 code such as COP.
type Currency string

// MaxAmount is the largest absolute amount of one entry: 2^53 - 1, the largest
// integer every JSON client reads exactly. postings_amount_limit enforces the
// same bound in the database.
const MaxAmount Amount = 1<<53 - 1

// add returns a + b, or ErrAmountOverflow when the result does not fit in an
// int64. Plain + would wrap around silently.
func add(a, b Amount) (Amount, error) {
	if (b > 0 && a > math.MaxInt64-b) || (b < 0 && a < math.MinInt64-b) {
		return 0, fmt.Errorf("%w: %d + %d", ErrAmountOverflow, a, b)
	}
	return a + b, nil
}
