// Package accounts opens wallets and reads them. Money never enters here: an
// account starts at zero and is funded with a journal entry through the ledger.
package accounts

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sebasBetancourt/Ledger/internal/ledger"
	"github.com/sebasBetancourt/Ledger/internal/ledger/ledgerdb"
)

// Type is who owns an account. System accounts come from migrations only.
type Type string

const (
	TypeUser     Type = "user"
	TypeMerchant Type = "merchant"
	TypeSystem   Type = "system"
)

var (
	ErrInvalidType     = errors.New("accounts: only user and merchant accounts can be created")
	ErrUnknownCurrency = errors.New("accounts: unknown currency")
)

type Account struct {
	ID       uuid.UUID
	Type     Type
	Code     string // system accounts only
	Currency ledger.Currency
	Status   string
	// AllowNegative is true only for system accounts.
	AllowNegative bool
	CreatedAt     time.Time
}

type Balance struct {
	AccountID uuid.UUID
	Currency  ledger.Currency
	Available ledger.Amount
	Locked    ledger.Amount
	// Total is Available + Locked.
	Total     ledger.Amount
	Version   int64
	UpdatedAt time.Time
}

type Service struct {
	pool *pgxpool.Pool
	ids  ledger.IDGenerator
}

func NewService(pool *pgxpool.Pool, ids ledger.IDGenerator) *Service {
	return &Service{pool: pool, ids: ids}
}

// CreateAccount opens a user or merchant account at zero, with its balance row
// in the same transaction. It takes no opening balance on purpose: money
// arrives with a journal entry from a system account.
func (s *Service) CreateAccount(ctx context.Context, accountType Type, currency ledger.Currency) (Account, error) {
	if accountType != TypeUser && accountType != TypeMerchant {
		return Account{}, fmt.Errorf("%w: got %q", ErrInvalidType, accountType)
	}

	var created ledgerdb.Account
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := ledgerdb.New(tx)

		var err error
		created, err = q.CreateAccount(ctx, ledgerdb.CreateAccountParams{
			ID:            s.ids.New(),
			Type:          string(accountType),
			Currency:      string(currency),
			AllowNegative: false,
		})
		if err != nil {
			return fmt.Errorf("insert account: %w", err)
		}
		_, err = q.CreateBalance(ctx, ledgerdb.CreateBalanceParams{
			AccountID:     created.ID,
			AllowNegative: created.AllowNegative,
		})
		if err != nil {
			return fmt.Errorf("insert balance: %w", err)
		}
		return nil
	})

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.ConstraintName == "accounts_currency_fk" {
		return Account{}, fmt.Errorf("%w: %q", ErrUnknownCurrency, currency)
	}
	if err != nil {
		return Account{}, err
	}
	return toAccount(created), nil
}

func (s *Service) GetAccount(ctx context.Context, id uuid.UUID) (Account, error) {
	a, err := ledgerdb.New(s.pool).GetAccount(ctx, id)
	if err != nil {
		return Account{}, notFound(err, id.String())
	}
	return toAccount(a), nil
}

// GetBalance reads a balance without locking it, for display only.
func (s *Service) GetBalance(ctx context.Context, id uuid.UUID) (Balance, error) {
	b, err := ledgerdb.New(s.pool).GetBalance(ctx, id)
	if err != nil {
		return Balance{}, notFound(err, id.String())
	}
	return Balance{
		AccountID: b.AccountID,
		Currency:  ledger.Currency(b.Currency),
		Available: ledger.Amount(b.Available),
		Locked:    ledger.Amount(b.Locked),
		Total:     ledger.Amount(b.Available + b.Locked),
		Version:   b.Version,
		UpdatedAt: b.UpdatedAt,
	}, nil
}

// GetSystemAccount finds a system account by code and currency: there is one
// fee_revenue per currency, so the code alone is not enough.
func (s *Service) GetSystemAccount(ctx context.Context, code string, currency ledger.Currency) (Account, error) {
	a, err := ledgerdb.New(s.pool).GetSystemAccountByCode(ctx, ledgerdb.GetSystemAccountByCodeParams{
		Code: code, Currency: string(currency),
	})
	if err != nil {
		return Account{}, notFound(err, code+" "+string(currency))
	}
	return toAccount(a), nil
}

func notFound(err error, what string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: %s", ledger.ErrAccountNotFound, what)
	}
	return err
}

func toAccount(a ledgerdb.Account) Account {
	var code string
	if a.Code != nil {
		code = *a.Code
	}
	return Account{
		ID:            a.ID,
		Type:          Type(a.Type),
		Code:          code,
		Currency:      ledger.Currency(a.Currency),
		Status:        a.Status,
		AllowNegative: a.AllowNegative,
		CreatedAt:     a.CreatedAt,
	}
}
