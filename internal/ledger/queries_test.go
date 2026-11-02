package ledger_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sebasBetancourt/Ledger/internal/ledger/ledgerdb"
)

// The generated Queries must satisfy the generated interface.
var _ ledgerdb.Querier = (*ledgerdb.Queries)(nil)

// Seeded by migrations/0003_system_accounts.up.sql.
var cashInCOP = uuid.MustParse("01900000-0000-7000-8000-000000000001")

func TestCreateAndGetAccounts(t *testing.T) {
	q := ledgerdb.New(newDB(t))
	first, second := newWallet(t, q), newWallet(t, q)

	got, err := q.GetAccountsByIDs(t.Context(), []uuid.UUID{second, first})
	if err != nil {
		t.Fatalf("GetAccountsByIDs: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d accounts, want 2", len(got))
	}
	if !isSorted(got[0].ID, got[1].ID) {
		t.Errorf("accounts not ordered by ID: %v, %v", got[0].ID, got[1].ID)
	}
	for _, a := range got {
		if a.Type != "user" || a.Currency != "COP" || a.Status != "active" || a.AllowNegative {
			t.Errorf("account %v = %+v, want an active COP user wallet", a.ID, a)
		}
		if a.Code != nil {
			t.Errorf("Code = %q, want nil for a user wallet", *a.Code)
		}
		if a.CreatedAt.IsZero() {
			t.Error("CreatedAt is zero")
		}
	}
}

func TestGetSystemAccountByCode(t *testing.T) {
	q := ledgerdb.New(newDB(t))

	got, err := q.GetSystemAccountByCode(t.Context(), ledgerdb.GetSystemAccountByCodeParams{
		Code: "cash_in_clearing", Currency: "COP",
	})
	if err != nil {
		t.Fatalf("GetSystemAccountByCode: %v", err)
	}
	if got.ID != cashInCOP || !got.AllowNegative || got.Code == nil || *got.Code != "cash_in_clearing" {
		t.Errorf("got %+v, want the seeded COP cash_in_clearing", got)
	}

	_, err = q.GetSystemAccountByCode(t.Context(), ledgerdb.GetSystemAccountByCodeParams{
		Code: "cash_in_clearing", Currency: "EUR",
	})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("unknown currency: error = %v, want pgx.ErrNoRows", err)
	}
}

// Rows are inserted in descending ID order, so the table's physical order is
// the opposite of the expected one. Without ORDER BY this test fails.
func TestLockBalancesReturnsAccountOrder(t *testing.T) {
	pool := newDB(t)
	q := ledgerdb.New(pool)

	want := []uuid.UUID{uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())}
	slices.SortFunc(want, func(x, y uuid.UUID) int { return bytes.Compare(x[:], y[:]) })
	for _, id := range slices.Backward(want) {
		createWallet(t, q, id)
	}

	tx := begin(t, pool)
	got, err := q.WithTx(tx).LockBalances(t.Context(), []uuid.UUID{want[1], want[2], want[0]})
	if err != nil {
		t.Fatalf("LockBalances: %v", err)
	}

	ids := make([]uuid.UUID, len(got))
	for i, balance := range got {
		ids[i] = balance.AccountID
	}
	if !slices.Equal(ids, want) {
		t.Errorf("locked %v, want %v", ids, want)
	}
}

// Crossed transfers (A→B and B→A) ask for the same accounts in opposite
// order. LockBalances must still take the locks in one order. This test alone
// would not catch a missing ORDER BY, because one query happens to lock in the
// table's physical order; TestLockBalancesReturnsAccountOrder does.
func TestLockBalancesCrossedNeverDeadlocks(t *testing.T) {
	pool := newDB(t)
	q := ledgerdb.New(pool)
	a, b := newWallet(t, q), newWallet(t, q)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	for range 50 {
		var wg sync.WaitGroup
		errs := make(chan error, 2)
		for _, order := range [][]uuid.UUID{{a, b}, {b, a}} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs <- lockAndHold(ctx, pool, order)
			}()
		}
		wg.Wait()
		close(errs)

		for err := range errs {
			if err != nil {
				t.Fatalf("crossed lock failed: %v", err)
			}
		}
	}
}

// The bad practice LockBalances avoids: locking accounts one by one in the
// order the request names them. Crossed transfers then wait on each other
// until PostgreSQL kills one with a deadlock error.
func TestLockingOneByOneDeadlocks(t *testing.T) {
	pool := newDB(t)
	q := ledgerdb.New(pool)
	a, b := newWallet(t, q), newWallet(t, q)

	var firstLocked sync.WaitGroup
	firstLocked.Add(2)
	errs := make(chan error, 2)
	for _, order := range [][]uuid.UUID{{a, b}, {b, a}} {
		go func() {
			errs <- func() error {
				tx, err := pool.Begin(t.Context())
				if err != nil {
					return err
				}
				defer func() { _ = tx.Rollback(context.Background()) }()

				lock := `SELECT 1 FROM balances WHERE account_id = $1 FOR UPDATE`
				if _, err := tx.Exec(t.Context(), lock, order[0]); err != nil {
					return err
				}
				// Both transactions now hold their first account.
				firstLocked.Done()
				firstLocked.Wait()
				if _, err := tx.Exec(t.Context(), lock, order[1]); err != nil {
					return err
				}
				return tx.Commit(t.Context())
			}()
		}()
	}

	var deadlocks int
	for range 2 {
		var pgErr *pgconn.PgError
		if err := <-errs; errors.As(err, &pgErr) && pgErr.Code == "40P01" {
			deadlocks++
		}
	}
	if deadlocks != 1 {
		t.Errorf("deadlocks = %d, want 1", deadlocks)
	}
}

func TestTransferWithQueries(t *testing.T) {
	pool := newDB(t)
	q := ledgerdb.New(pool)
	ana := newWallet(t, q)

	move(t, pool, cashInCOP, ana, 5_000_000)

	balances := lock(t, pool, ana, cashInCOP)
	if balances[ana].Available != 5_000_000 || balances[ana].LastSeq != 1 || balances[ana].Version != 1 {
		t.Errorf("ana balance = %+v, want available 5000000 at seq 1, version 1", balances[ana])
	}
	if balances[cashInCOP].Available != -5_000_000 {
		t.Errorf("cash_in_clearing available = %d, want -5000000", balances[cashInCOP].Available)
	}
}

func TestUpdateTransactionStatusIsCompareAndSet(t *testing.T) {
	q := ledgerdb.New(newDB(t))
	id := insertTransaction(t, q)

	rows, err := q.UpdateTransactionStatus(t.Context(), ledgerdb.UpdateTransactionStatusParams{
		ID: id, FromStatus: "pending", ToStatus: "completed",
	})
	if err != nil {
		t.Fatalf("UpdateTransactionStatus: %v", err)
	}
	if rows != 0 {
		t.Errorf("updated %d rows from a status the transaction is not in, want 0", rows)
	}
}

func TestSetStatusReason(t *testing.T) {
	pool := newDB(t)
	tx := begin(t, pool)
	q := ledgerdb.New(pool).WithTx(tx)

	id := insertTransaction(t, q)
	setStatus(t, q, id, "created", "pending")
	if err := q.SetStatusReason(t.Context(), "insufficient_funds"); err != nil {
		t.Fatalf("SetStatusReason: %v", err)
	}
	setStatus(t, q, id, "pending", "failed")
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatalf("commit: %v", err)
	}

	var reason *string
	err := pool.QueryRow(t.Context(), `
		SELECT reason FROM transaction_status_history
		WHERE transaction_id = $1 AND to_status = 'failed'`, id).Scan(&reason)
	if err != nil {
		t.Fatal(err)
	}
	if reason == nil || *reason != "insufficient_funds" {
		t.Errorf("reason = %v, want insufficient_funds", reason)
	}
}

func TestListPostingsByAccountPaginatesBySeq(t *testing.T) {
	pool := newDB(t)
	q := ledgerdb.New(pool)
	ana := newWallet(t, q)
	for range 5 {
		move(t, pool, cashInCOP, ana, 1_000)
	}

	var pages [][]int64
	var before *int64
	for {
		page, err := q.ListPostingsByAccount(t.Context(), ledgerdb.ListPostingsByAccountParams{
			AccountID: ana, BeforeSeq: before, PageSize: 2,
		})
		if err != nil {
			t.Fatalf("ListPostingsByAccount: %v", err)
		}
		if len(page) == 0 {
			break
		}
		var seqs []int64
		for _, p := range page {
			seqs = append(seqs, p.Seq)
		}
		pages = append(pages, seqs)
		before = &page[len(page)-1].Seq
	}

	want := [][]int64{{5, 4}, {3, 2}, {1}}
	if !slices.EqualFunc(pages, want, slices.Equal) {
		t.Errorf("pages = %v, want %v", pages, want)
	}
}

func TestInvariantReports(t *testing.T) {
	pool := newDB(t)
	q := ledgerdb.New(pool)
	ana, beto := newWallet(t, q), newWallet(t, q)
	move(t, pool, cashInCOP, ana, 10_000_000)
	move(t, pool, ana, beto, 3_000_000)

	sums, err := q.SumPostingsByCurrency(t.Context())
	if err != nil {
		t.Fatalf("SumPostingsByCurrency: %v", err)
	}
	if len(sums) != 1 || sums[0].Currency != "COP" || sums[0].Total != 0 {
		t.Errorf("sums = %+v, want COP 0", sums)
	}

	recomputed, err := q.RecomputeBalances(t.Context())
	if err != nil {
		t.Fatalf("RecomputeBalances: %v", err)
	}
	ids := make([]uuid.UUID, len(recomputed))
	for i, r := range recomputed {
		ids[i] = r.AccountID
	}
	stored := lock(t, pool, ids...)
	for _, r := range recomputed {
		b := stored[r.AccountID]
		if r.Total != b.Available+b.Locked || r.LastSeq != b.LastSeq {
			t.Errorf("account %v: ledger says %d at seq %d, balance says %d at seq %d",
				r.AccountID, r.Total, r.LastSeq, b.Available+b.Locked, b.LastSeq)
		}
	}
}

// newWallet creates a COP user account and its balance row.
func newWallet(t *testing.T, q *ledgerdb.Queries) uuid.UUID {
	t.Helper()

	id := uuid.Must(uuid.NewV7())
	createWallet(t, q, id)
	return id
}

func createWallet(t *testing.T, q *ledgerdb.Queries, id uuid.UUID) {
	t.Helper()

	account, err := q.CreateAccount(t.Context(), ledgerdb.CreateAccountParams{
		ID: id, Type: "user", Currency: "COP",
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	balance, err := q.CreateBalance(t.Context(), ledgerdb.CreateBalanceParams{
		AccountID: id, AllowNegative: account.AllowNegative,
	})
	if err != nil {
		t.Fatalf("CreateBalance: %v", err)
	}
	if balance.Available != 0 || balance.Version != 0 {
		t.Fatalf("new balance = %+v, want zero", balance)
	}
}

// move commits a two-posting transfer using only generated queries. It is test
// scaffolding, not the ledger: step 3 builds the real one.
func move(t *testing.T, pool *pgxpool.Pool, from, to uuid.UUID, amount int64) {
	t.Helper()

	tx := begin(t, pool)
	q := ledgerdb.New(pool).WithTx(tx)

	id := insertTransaction(t, q)
	setStatus(t, q, id, "created", "pending")

	locked, err := q.LockBalances(t.Context(), []uuid.UUID{from, to})
	if err != nil {
		t.Fatalf("LockBalances: %v", err)
	}
	for _, b := range locked {
		delta := amount
		if b.AccountID == from {
			delta = -amount
		}

		var prev *int64
		if b.LastSeq > 0 {
			prev = &b.LastSeq
		}
		before := b.Available + b.Locked
		_, err := q.InsertPosting(t.Context(), ledgerdb.InsertPostingParams{
			ID: uuid.Must(uuid.NewV7()), TransactionID: id, AccountID: b.AccountID, Currency: "COP",
			Amount: delta, Seq: b.LastSeq + 1, PrevSeq: prev,
			BalanceBefore: before, BalanceAfter: before + delta,
		})
		if err != nil {
			t.Fatalf("InsertPosting: %v", err)
		}
		_, err = q.UpdateBalance(t.Context(), ledgerdb.UpdateBalanceParams{
			AccountID: b.AccountID, Available: b.Available + delta, Locked: b.Locked, LastSeq: b.LastSeq + 1,
		})
		if err != nil {
			t.Fatalf("UpdateBalance: %v", err)
		}
	}

	setStatus(t, q, id, "pending", "completed")
	err = q.InsertOutboxEvent(t.Context(), ledgerdb.InsertOutboxEventParams{
		ID: uuid.Must(uuid.NewV7()), TransactionID: id,
		EventType: "transaction.completed", Payload: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("InsertOutboxEvent: %v", err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func insertTransaction(t *testing.T, q *ledgerdb.Queries) uuid.UUID {
	t.Helper()

	id := uuid.Must(uuid.NewV7())
	got, err := q.InsertTransaction(t.Context(), ledgerdb.InsertTransactionParams{
		ID: id, Reference: "test:" + id.String(), Kind: "transfer",
		CorrelationID: uuid.Must(uuid.NewV7()), InitiatedBy: "test", Metadata: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("InsertTransaction: %v", err)
	}
	if got.Status != "created" || got.ReversesID != nil || got.ExternalRef != nil {
		t.Fatalf("new transaction = %+v, want created with no reversal or external ref", got)
	}
	return id
}

func setStatus(t *testing.T, q *ledgerdb.Queries, id uuid.UUID, from, to string) {
	t.Helper()

	rows, err := q.UpdateTransactionStatus(t.Context(), ledgerdb.UpdateTransactionStatusParams{
		ID: id, FromStatus: from, ToStatus: to,
	})
	if err != nil || rows != 1 {
		t.Fatalf("status %s → %s: rows %d, error %v", from, to, rows, err)
	}
}

// lock reads balances through LockBalances in a throwaway transaction.
func lock(t *testing.T, pool *pgxpool.Pool, ids ...uuid.UUID) map[uuid.UUID]ledgerdb.Balance {
	t.Helper()

	tx := begin(t, pool)
	rows, err := ledgerdb.New(pool).WithTx(tx).LockBalances(t.Context(), ids)
	if err != nil {
		t.Fatalf("LockBalances: %v", err)
	}
	balances := make(map[uuid.UUID]ledgerdb.Balance, len(rows))
	for _, b := range rows {
		balances[b.AccountID] = b
	}
	return balances
}

func lockAndHold(ctx context.Context, pool *pgxpool.Pool, order []uuid.UUID) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	// Fail fast instead of hanging if the order is ever wrong.
	if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '5s'`); err != nil {
		return err
	}
	if _, err := ledgerdb.New(pool).WithTx(tx).LockBalances(ctx, order); err != nil {
		return err
	}
	time.Sleep(time.Millisecond)
	return tx.Commit(ctx)
}

func begin(t *testing.T, pool *pgxpool.Pool) pgx.Tx {
	t.Helper()

	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	return tx
}

func isSorted(a, b uuid.UUID) bool { return bytes.Compare(a[:], b[:]) < 0 }
