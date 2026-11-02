package ledger_test

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"testing"

	"github.com/google/uuid"
	"pgregory.net/rapid"

	"github.com/sebasBetancourt/Ledger/internal/ledger"
	"github.com/sebasBetancourt/Ledger/internal/ledger/ledgerdb"
)

// rapid draws hundreds of random sequences of transfers, valid and invalid:
// amounts above the balance, zero, the same account twice, reused references.
// An in-memory model predicts each outcome, and after every sequence the
// database must match the model and all 7 invariants must hold.
func TestPostProperties(t *testing.T) {
	t.Parallel()
	pool := newDB(t)
	svc := ledger.NewService(pool, ledger.UUIDv7{})
	q := ledgerdb.New(pool)
	ctx := context.Background()

	rapid.Check(t, func(rt *rapid.T) {
		model := map[uuid.UUID]ledger.Amount{}
		var references []string

		wallets := make([]uuid.UUID, rapid.IntRange(2, 4).Draw(rt, "wallets"))
		for i := range wallets {
			id := uuid.Must(uuid.NewV7())
			if err := openWallet(ctx, q, id); err != nil {
				rt.Fatalf("open wallet: %v", err)
			}
			wallets[i] = id

			opening := ledger.Amount(rapid.Int64Range(0, 1_000_000).Draw(rt, "opening"))
			if opening == 0 {
				continue
			}
			ref := uuid.NewString()
			if _, err := svc.Post(ctx, request(ref, ledger.KindCashIn, cashInCOP, -opening, id, opening)); err != nil {
				rt.Fatalf("fund: %v", err)
			}
			model[id] = opening
			references = append(references, ref)
		}

		operations := rapid.IntRange(1, 20).Draw(rt, "operations")
		for range operations {
			from := rapid.SampledFrom(wallets).Draw(rt, "from")
			to := rapid.SampledFrom(wallets).Draw(rt, "to")
			amount := ledger.Amount(rapid.Int64Range(0, 1_500_000).Draw(rt, "amount"))
			ref := uuid.NewString()
			reused := len(references) > 0 && rapid.IntRange(0, 9).Draw(rt, "reuse") == 0
			if reused {
				ref = rapid.SampledFrom(references).Draw(rt, "reused reference")
			}

			// The order matches Post: validation, then the reference, then funds.
			var want error
			switch {
			case from == to, amount == 0:
				want = ledger.ErrInvalidRequest
			case reused:
				want = ledger.ErrDuplicateReference
			case model[from] < amount:
				want = ledger.ErrInsufficientFunds
			}

			_, err := svc.Post(ctx, request(ref, ledger.KindTransfer, from, -amount, to, amount))
			if want != nil {
				if !errors.Is(err, want) {
					rt.Fatalf("Post(%d) = %v, want %v", amount, err, want)
				}
				continue
			}
			if err != nil {
				rt.Fatalf("Post(%d) = %v, want success", amount, err)
			}
			model[from] -= amount
			model[to] += amount
			references = append(references, ref)
		}

		for _, id := range wallets {
			var available int64
			if err := pool.QueryRow(ctx, `SELECT available FROM balances WHERE account_id = $1`, id).Scan(&available); err != nil {
				rt.Fatal(err)
			}
			if ledger.Amount(available) != model[id] {
				rt.Fatalf("wallet %v has %d, model says %d", id, available, model[id])
			}
		}
		violations, err := ledger.CheckInvariants(ctx, pool)
		if err != nil || len(violations) != 0 {
			rt.Fatalf("invariants: %v, %v", violations, err)
		}
	})
}

// 100 goroutines move random amounts among the same 3 accounts. Every pair of
// transfers can cross, so this fails with a deadlock if lock order ever slips.
func TestPostConcurrentAmongThreeAccounts(t *testing.T) {
	t.Parallel()
	pool := newDB(t)
	svc := ledger.NewService(pool, ledger.UUIDv7{})
	q := ledgerdb.New(pool)

	accounts := []uuid.UUID{newWallet(t, q), newWallet(t, q), newWallet(t, q)}
	for i, a := range accounts {
		fund(t, svc, a, fmt.Sprintf("open-%d", i), 1_000_000)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 100)
	for i := range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := rand.New(rand.NewPCG(uint64(i), 0)) //nolint:gosec // test data, not security
			from := r.IntN(3)
			to := (from + 1 + r.IntN(2)) % 3
			amount := ledger.Amount(1 + r.Int64N(10_000))
			ref := fmt.Sprintf("move-%d", i)
			_, err := svc.Post(t.Context(), request(ref, ledger.KindTransfer, accounts[from], -amount, accounts[to], amount))
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil && !errors.Is(err, ledger.ErrInsufficientFunds) {
			t.Errorf("transfer failed: %v", err)
		}
	}

	var total int64
	err := pool.QueryRow(t.Context(),
		`SELECT sum(available)::bigint FROM balances WHERE account_id = ANY($1)`, accounts).Scan(&total)
	if err != nil {
		t.Fatal(err)
	}
	if total != 3_000_000 {
		t.Errorf("the 3 accounts hold %d, want 3000000", total)
	}
}

func openWallet(ctx context.Context, q *ledgerdb.Queries, id uuid.UUID) error {
	if _, err := q.CreateAccount(ctx, ledgerdb.CreateAccountParams{ID: id, Type: "user", Currency: "COP"}); err != nil {
		return err
	}
	_, err := q.CreateBalance(ctx, ledgerdb.CreateBalanceParams{AccountID: id})
	return err
}
