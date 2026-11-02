package ledger_test

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sebasBetancourt/Ledger/internal/ledger"
	"github.com/sebasBetancourt/Ledger/internal/ledger/ledgerdb"
)

func TestPostTransfer(t *testing.T) {
	pool := newDB(t)
	ids := &sequenceIDs{}
	svc := ledger.NewService(pool, ids)
	q := ledgerdb.New(pool)
	ana, beto := newWallet(t, q), newWallet(t, q)

	funding, err := svc.Post(t.Context(), request("fund", ledger.KindCashIn, cashInCOP, -100_000, ana, 100_000))
	if err != nil {
		t.Fatalf("fund: %v", err)
	}
	// The first ID the service asks for is the transaction's.
	if funding.ID != sequenceID(1) {
		t.Errorf("transaction ID = %v, want %v", funding.ID, sequenceID(1))
	}

	got, err := svc.Post(t.Context(), request("p2p", ledger.KindTransfer, ana, -30_000, beto, 30_000))
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	if got.Status != "completed" || got.Reference != "p2p" || len(got.Postings) != 2 {
		t.Fatalf("transaction = %+v, want completed p2p with 2 postings", got)
	}

	postings := map[uuid.UUID]ledger.Posting{}
	for _, p := range got.Postings {
		postings[p.AccountID] = p
	}
	wantPostings := map[uuid.UUID]ledger.Posting{
		ana:  {AccountID: ana, Amount: -30_000, Seq: 2, BalanceBefore: 100_000, BalanceAfter: 70_000},
		beto: {AccountID: beto, Amount: 30_000, Seq: 1, BalanceBefore: 0, BalanceAfter: 30_000},
	}
	for account, want := range wantPostings {
		p := postings[account]
		p.ID = uuid.Nil
		if p != want {
			t.Errorf("posting for %v = %+v, want %+v", account, p, want)
		}
	}

	balances := lock(t, pool, ana, beto)
	if balances[ana].Available != 70_000 || balances[beto].Available != 30_000 {
		t.Errorf("balances: ana %d, beto %d, want 70000 and 30000", balances[ana].Available, balances[beto].Available)
	}

	history := statusHistory(t, pool, got.ID)
	if fmt.Sprint(history) != "[created pending completed]" {
		t.Errorf("history = %v, want [created pending completed]", history)
	}
	if n := count(t, pool, `SELECT count(*) FROM outbox_events WHERE transaction_id = $1`, got.ID); n != 1 {
		t.Errorf("outbox events = %d, want 1", n)
	}
}

func TestPostInvalidRequestWritesNothing(t *testing.T) {
	pool := newDB(t)
	svc := ledger.NewService(pool, ledger.UUIDv7{})

	_, err := svc.Post(t.Context(), request("bad", ledger.KindTransfer, uuid.New(), -100, uuid.New(), 99))
	if !errors.Is(err, ledger.ErrUnbalanced) {
		t.Fatalf("Post = %v, want ErrUnbalanced", err)
	}
	if n := count(t, pool, `SELECT count(*) FROM journal_transactions`); n != 0 {
		t.Errorf("journal transactions = %d, want 0", n)
	}
}

func TestPostDuplicateReference(t *testing.T) {
	pool, svc, ana, beto := funded(t, 100_000)

	_, err := svc.Post(t.Context(), request("fund-ana", ledger.KindTransfer, ana, -10, beto, 10))
	if !errors.Is(err, ledger.ErrDuplicateReference) {
		t.Fatalf("Post = %v, want ErrDuplicateReference", err)
	}
	if b := lock(t, pool, ana)[ana]; b.Available != 100_000 {
		t.Errorf("ana available = %d, want 100000", b.Available)
	}
}

func TestPostInsufficientFunds(t *testing.T) {
	pool, svc, ana, beto := funded(t, 100)

	_, err := svc.Post(t.Context(), request("too-much", ledger.KindTransfer, ana, -101, beto, 101))
	if !errors.Is(err, ledger.ErrInsufficientFunds) {
		t.Fatalf("Post = %v, want ErrInsufficientFunds", err)
	}
	// Only the funding transaction exists: the failed one rolled back entirely.
	if n := count(t, pool, `SELECT count(*) FROM journal_transactions`); n != 1 {
		t.Errorf("journal transactions = %d, want 1", n)
	}
	if b := lock(t, pool, ana)[ana]; b.Available != 100 || b.LastSeq != 1 {
		t.Errorf("ana = %+v, want 100 at seq 1", b)
	}
}

func TestPostAccountChecks(t *testing.T) {
	pool, svc, ana, beto := funded(t, 100)
	q := ledgerdb.New(pool)

	t.Run("account not found", func(t *testing.T) {
		_, err := svc.Post(t.Context(), request("ghost", ledger.KindTransfer, ana, -10, uuid.New(), 10))
		if !errors.Is(err, ledger.ErrAccountNotFound) {
			t.Fatalf("Post = %v, want ErrAccountNotFound", err)
		}
	})

	t.Run("frozen account", func(t *testing.T) {
		if _, err := pool.Exec(t.Context(), `UPDATE accounts SET status = 'frozen' WHERE id = $1`, beto); err != nil {
			t.Fatal(err)
		}
		_, err := svc.Post(t.Context(), request("to-frozen", ledger.KindTransfer, ana, -10, beto, 10))
		if !errors.Is(err, ledger.ErrAccountNotActive) {
			t.Fatalf("Post = %v, want ErrAccountNotActive", err)
		}
	})

	t.Run("account in another currency", func(t *testing.T) {
		usd := uuid.New()
		if _, err := q.CreateAccount(t.Context(), ledgerdb.CreateAccountParams{ID: usd, Type: "user", Currency: "USD"}); err != nil {
			t.Fatal(err)
		}
		if _, err := q.CreateBalance(t.Context(), ledgerdb.CreateBalanceParams{AccountID: usd}); err != nil {
			t.Fatal(err)
		}
		_, err := svc.Post(t.Context(), request("to-usd", ledger.KindTransfer, ana, -10, usd, 10))
		if !errors.Is(err, ledger.ErrCurrencyMismatch) {
			t.Fatalf("Post = %v, want ErrCurrencyMismatch", err)
		}
	})
}

func TestPostMerchantPaymentWithFee(t *testing.T) {
	pool, svc, ana, _ := funded(t, 100_000)
	q := ledgerdb.New(pool)
	cafe := newWallet(t, q)
	fees, err := q.GetSystemAccountByCode(t.Context(), ledgerdb.GetSystemAccountByCodeParams{Code: "fee_revenue", Currency: "COP"})
	if err != nil {
		t.Fatal(err)
	}

	req := request("coffee", ledger.KindMerchantPayment, ana, -100_000, cafe, 98_000)
	req.Entries = append(req.Entries, ledger.Entry{AccountID: fees.ID, Amount: 2_000})
	if _, err := svc.Post(t.Context(), req); err != nil {
		t.Fatalf("Post: %v", err)
	}

	got := lock(t, pool, ana, cafe, fees.ID, cashInCOP)
	want := map[uuid.UUID]int64{ana: 0, cafe: 98_000, fees.ID: 2_000, cashInCOP: -100_000}
	for account, available := range want {
		if got[account].Available != available {
			t.Errorf("account %v available = %d, want %d", account, got[account].Available, available)
		}
	}
}

// Ana has 100 and 20 requests try to spend 10 each at the same time. Without
// the row lock, several would read 100 and all succeed.
func TestPostPreventsDoubleSpend(t *testing.T) {
	pool, svc, ana, beto := funded(t, 100)

	var wg sync.WaitGroup
	results := make(chan error, 20)
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.Post(t.Context(), request(fmt.Sprintf("spend-%d", i), ledger.KindTransfer, ana, -10, beto, 10))
			results <- err
		}()
	}
	wg.Wait()
	close(results)

	var ok, insufficient int
	for err := range results {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ledger.ErrInsufficientFunds):
			insufficient++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if ok != 10 || insufficient != 10 {
		t.Errorf("succeeded %d, insufficient %d, want 10 and 10", ok, insufficient)
	}
	if b := lock(t, pool, ana)[ana]; b.Available != 0 {
		t.Errorf("ana available = %d, want 0", b.Available)
	}
}

func TestPostCrossedTransfersNeverDeadlock(t *testing.T) {
	pool, svc, ana, beto := funded(t, 1_000)
	if _, err := svc.Post(t.Context(), request("fund-beto", ledger.KindCashIn, cashInCOP, -1_000, beto, 1_000)); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 100)
	for i := range 50 {
		for _, pair := range [][2]uuid.UUID{{ana, beto}, {beto, ana}} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ref := fmt.Sprintf("cross-%d-%s", i, pair[0])
				_, err := svc.Post(t.Context(), request(ref, ledger.KindTransfer, pair[0], -1, pair[1], 1))
				errs <- err
			}()
		}
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("crossed transfer failed: %v", err)
		}
	}
	sums, err := ledgerdb.New(pool).SumPostingsByCurrency(t.Context())
	if err != nil || len(sums) != 1 || sums[0].Total != 0 {
		t.Errorf("sums = %+v, %v, want COP 0", sums, err)
	}
	balances := lock(t, pool, ana, beto)
	if balances[ana].Available != 1_000 || balances[beto].Available != 1_000 {
		t.Errorf("ana %d, beto %d, want 1000 each", balances[ana].Available, balances[beto].Available)
	}
}

// funded returns a service, two wallets, and ana funded from cash_in_clearing
// with the reference "fund-ana".
func funded(t *testing.T, amount ledger.Amount) (*pgxpool.Pool, *ledger.Service, uuid.UUID, uuid.UUID) {
	t.Helper()

	pool := newDB(t)
	svc := ledger.NewService(pool, ledger.UUIDv7{})
	q := ledgerdb.New(pool)
	ana, beto := newWallet(t, q), newWallet(t, q)
	if _, err := svc.Post(t.Context(), request("fund-ana", ledger.KindCashIn, cashInCOP, -amount, ana, amount)); err != nil {
		t.Fatalf("fund: %v", err)
	}
	return pool, svc, ana, beto
}

func request(reference string, kind ledger.Kind, from uuid.UUID, out ledger.Amount, to uuid.UUID, in ledger.Amount) ledger.PostRequest {
	return ledger.PostRequest{
		Reference:     reference,
		Kind:          kind,
		Currency:      "COP",
		CorrelationID: uuid.New(),
		InitiatedBy:   "test",
		Entries:       []ledger.Entry{{AccountID: from, Amount: out}, {AccountID: to, Amount: in}},
	}
}

func statusHistory(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) []string {
	t.Helper()

	rows, err := pool.Query(t.Context(),
		`SELECT to_status FROM transaction_status_history WHERE transaction_id = $1 ORDER BY id`, id)
	if err != nil {
		t.Fatal(err)
	}
	var statuses []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		statuses = append(statuses, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return statuses
}

func count(t *testing.T, pool *pgxpool.Pool, query string, args ...any) int64 {
	t.Helper()

	var n int64
	if err := pool.QueryRow(t.Context(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// sequenceIDs returns 1, 2, 3... as UUIDs, so tests know every ID in advance.
type sequenceIDs struct{ n atomic.Uint64 }

func (s *sequenceIDs) New() uuid.UUID { return sequenceID(s.n.Add(1)) }

func sequenceID(n uint64) uuid.UUID {
	var id uuid.UUID
	binary.BigEndian.PutUint64(id[8:], n)
	return id
}
