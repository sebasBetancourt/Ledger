package ledger

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sebasBetancourt/Ledger/internal/ledger/ledgerdb"
)

// Transaction is a confirmed journal transaction and its postings.
type Transaction struct {
	ID            uuid.UUID
	Reference     string
	Kind          Kind
	Status        string
	Currency      Currency
	CorrelationID uuid.UUID
	CreatedAt     time.Time
	// Postings are ordered by account ID, the order their locks were taken.
	Postings []Posting
}

// Posting is one line of a confirmed transaction. BalanceBefore and
// BalanceAfter are the account's total (available + locked).
type Posting struct {
	ID            uuid.UUID
	AccountID     uuid.UUID
	Amount        Amount
	Seq           int64
	BalanceBefore Amount
	BalanceAfter  Amount
}

// Service records transactions in the ledger.
type Service struct {
	pool *pgxpool.Pool
	ids  IDGenerator
}

func NewService(pool *pgxpool.Pool, ids IDGenerator) *Service {
	return &Service{pool: pool, ids: ids}
}

// Post records req as one atomic transaction. An invalid request never reaches
// the database. Any failure rolls back everything, so nothing is half-written.
func (s *Service) Post(ctx context.Context, req PostRequest) (Transaction, error) {
	if err := req.Validate(); err != nil {
		return Transaction{}, err
	}

	var result Transaction
	// READ COMMITTED is enough: LockBalances locks every row this transaction
	// reads before writing it.
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		result, err = s.post(ctx, ledgerdb.New(tx), req)
		return err
	})
	if err != nil {
		return Transaction{}, mapPgError(err)
	}
	return result, nil
}

func (s *Service) post(ctx context.Context, q *ledgerdb.Queries, req PostRequest) (Transaction, error) {
	metadata := req.Metadata
	if metadata == nil {
		metadata = json.RawMessage(`{}`)
	}
	var externalRef *string
	if req.ExternalRef != "" {
		externalRef = &req.ExternalRef
	}

	// A duplicate reference fails here with 23505, before anything else is written.
	txn, err := q.InsertTransaction(ctx, ledgerdb.InsertTransactionParams{
		ID:            s.ids.New(),
		Reference:     req.Reference,
		Kind:          string(req.Kind),
		CorrelationID: req.CorrelationID,
		InitiatedBy:   req.InitiatedBy,
		ExternalRef:   externalRef,
		Metadata:      metadata,
	})
	if err != nil {
		return Transaction{}, fmt.Errorf("insert transaction: %w", err)
	}
	// Postings can only be added while the transaction is pending.
	if err := setStatus(ctx, q, txn.ID, "created", "pending"); err != nil {
		return Transaction{}, err
	}

	amounts := make(map[uuid.UUID]Amount, len(req.Entries))
	accountIDs := make([]uuid.UUID, 0, len(req.Entries))
	for _, e := range req.Entries {
		amounts[e.AccountID] = e.Amount
		accountIDs = append(accountIDs, e.AccountID)
	}

	// Locks come back in account ID order, and every write below follows it.
	balances, err := q.LockBalances(ctx, accountIDs)
	if err != nil {
		return Transaction{}, fmt.Errorf("lock balances: %w", err)
	}
	if err := checkAccounts(ctx, q, accountIDs, len(balances), req.Currency); err != nil {
		return Transaction{}, err
	}

	postings := make([]Posting, 0, len(balances))
	for _, b := range balances {
		amount := amounts[b.AccountID]

		available, err := add(Amount(b.Available), amount)
		if err != nil {
			return Transaction{}, err
		}
		if !b.AllowNegative && available < 0 {
			return Transaction{}, fmt.Errorf("%w: account %s has %d available, needs %d",
				ErrInsufficientFunds, b.AccountID, b.Available, -amount)
		}
		before := Amount(b.Available + b.Locked)
		after, err := add(before, amount)
		if err != nil {
			return Transaction{}, err
		}

		var prevSeq *int64
		if b.LastSeq > 0 {
			prevSeq = &b.LastSeq
		}
		posting, err := q.InsertPosting(ctx, ledgerdb.InsertPostingParams{
			ID:            s.ids.New(),
			TransactionID: txn.ID,
			AccountID:     b.AccountID,
			Currency:      string(req.Currency),
			Amount:        int64(amount),
			Seq:           b.LastSeq + 1,
			PrevSeq:       prevSeq,
			BalanceBefore: int64(before),
			BalanceAfter:  int64(after),
		})
		if err != nil {
			return Transaction{}, fmt.Errorf("insert posting for %s: %w", b.AccountID, err)
		}

		_, err = q.UpdateBalance(ctx, ledgerdb.UpdateBalanceParams{
			AccountID: b.AccountID,
			Available: int64(available),
			Locked:    b.Locked,
			LastSeq:   posting.Seq,
		})
		if err != nil {
			return Transaction{}, fmt.Errorf("update balance of %s: %w", b.AccountID, err)
		}

		postings = append(postings, Posting{
			ID:            posting.ID,
			AccountID:     posting.AccountID,
			Amount:        Amount(posting.Amount),
			Seq:           posting.Seq,
			BalanceBefore: Amount(posting.BalanceBefore),
			BalanceAfter:  Amount(posting.BalanceAfter),
		})
	}

	// journal_status_audit records every status change; Go never writes history.
	if err := setStatus(ctx, q, txn.ID, "pending", "completed"); err != nil {
		return Transaction{}, err
	}

	result := Transaction{
		ID:            txn.ID,
		Reference:     txn.Reference,
		Kind:          req.Kind,
		Status:        "completed",
		Currency:      req.Currency,
		CorrelationID: txn.CorrelationID,
		CreatedAt:     txn.CreatedAt,
		Postings:      postings,
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return Transaction{}, fmt.Errorf("encode event: %w", err)
	}
	// Required by outbox_required: no completed transaction without its event.
	err = q.InsertOutboxEvent(ctx, ledgerdb.InsertOutboxEventParams{
		ID:            s.ids.New(),
		TransactionID: txn.ID,
		EventType:     "transaction.completed",
		Payload:       payload,
	})
	if err != nil {
		return Transaction{}, fmt.Errorf("insert outbox event: %w", err)
	}
	return result, nil
}

// checkAccounts verifies every account exists, is active and uses the
// request's currency. lockedCount is how many balance rows LockBalances found.
func checkAccounts(ctx context.Context, q *ledgerdb.Queries, ids []uuid.UUID, lockedCount int, currency Currency) error {
	accounts, err := q.GetAccountsByIDs(ctx, ids)
	if err != nil {
		return fmt.Errorf("load accounts: %w", err)
	}

	found := make(map[uuid.UUID]ledgerdb.Account, len(accounts))
	for _, a := range accounts {
		found[a.ID] = a
	}
	for _, id := range ids {
		a, ok := found[id]
		switch {
		case !ok:
			return fmt.Errorf("%w: %s", ErrAccountNotFound, id)
		case a.Status != "active":
			return fmt.Errorf("%w: %s is %s", ErrAccountNotActive, id, a.Status)
		case Currency(a.Currency) != currency:
			return fmt.Errorf("%w: %s is in %s, transaction is in %s", ErrCurrencyMismatch, id, a.Currency, currency)
		}
	}
	// An account without a balance row cannot hold money.
	if lockedCount != len(ids) {
		return fmt.Errorf("%w: %d of %d accounts have a balance", ErrAccountNotFound, lockedCount, len(ids))
	}
	return nil
}

// setStatus moves the transaction only if it is still in from.
func setStatus(ctx context.Context, q *ledgerdb.Queries, id uuid.UUID, from, to string) error {
	rows, err := q.UpdateTransactionStatus(ctx, ledgerdb.UpdateTransactionStatusParams{
		ID: id, FromStatus: from, ToStatus: to,
	})
	if err != nil {
		return fmt.Errorf("set status %s: %w", to, err)
	}
	if rows != 1 {
		return fmt.Errorf("set status %s: transaction %s is no longer %s", to, id, from)
	}
	return nil
}
