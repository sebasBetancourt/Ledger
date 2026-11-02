package accounts_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sebasBetancourt/Ledger/internal/accounts"
	"github.com/sebasBetancourt/Ledger/internal/ledger"
	"github.com/sebasBetancourt/Ledger/internal/platform/pgtest"
)

func TestCreateAccount(t *testing.T) {
	for _, accountType := range []accounts.Type{accounts.TypeUser, accounts.TypeMerchant} {
		t.Run(string(accountType), func(t *testing.T) {
			t.Parallel()
			svc := accounts.NewService(pgtest.New(t), ledger.UUIDv7{})

			created, err := svc.CreateAccount(t.Context(), accountType, "COP")
			if err != nil {
				t.Fatalf("CreateAccount: %v", err)
			}
			if created.Type != accountType || created.Currency != "COP" || created.Status != "active" {
				t.Errorf("account = %+v, want an active COP %s", created, accountType)
			}
			if created.AllowNegative {
				t.Error("a customer account must never allow a negative balance")
			}

			got, err := svc.GetAccount(t.Context(), created.ID)
			if err != nil || got != created {
				t.Errorf("GetAccount = %+v, %v, want %+v", got, err, created)
			}

			// The balance row was created in the same transaction, starting at zero.
			balance, err := svc.GetBalance(t.Context(), created.ID)
			if err != nil {
				t.Fatalf("GetBalance: %v", err)
			}
			if balance.Available != 0 || balance.Locked != 0 || balance.Total != 0 || balance.Currency != "COP" {
				t.Errorf("balance = %+v, want zero in COP", balance)
			}
		})
	}
}

func TestCreateAccountRejects(t *testing.T) {
	t.Run("system type", func(t *testing.T) {
		t.Parallel()
		svc := accounts.NewService(pgtest.New(t), ledger.UUIDv7{})

		_, err := svc.CreateAccount(t.Context(), "system", "COP")
		if !errors.Is(err, accounts.ErrInvalidType) {
			t.Errorf("CreateAccount = %v, want ErrInvalidType", err)
		}
	})

	t.Run("unknown currency writes nothing", func(t *testing.T) {
		t.Parallel()
		pool := pgtest.New(t)
		svc := accounts.NewService(pool, ledger.UUIDv7{})

		_, err := svc.CreateAccount(t.Context(), accounts.TypeUser, "EUR")
		if !errors.Is(err, accounts.ErrUnknownCurrency) {
			t.Fatalf("CreateAccount = %v, want ErrUnknownCurrency", err)
		}
		var n int
		if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM accounts WHERE type <> 'system'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("customer accounts = %d, want 0", n)
		}
	})
}

func TestGetMissingAccount(t *testing.T) {
	t.Parallel()
	svc := accounts.NewService(pgtest.New(t), ledger.UUIDv7{})
	missing := uuid.New()

	if _, err := svc.GetAccount(t.Context(), missing); !errors.Is(err, ledger.ErrAccountNotFound) {
		t.Errorf("GetAccount = %v, want ErrAccountNotFound", err)
	}
	if _, err := svc.GetBalance(t.Context(), missing); !errors.Is(err, ledger.ErrAccountNotFound) {
		t.Errorf("GetBalance = %v, want ErrAccountNotFound", err)
	}
	if _, err := svc.GetSystemAccount(t.Context(), "fee_revenue", "EUR"); !errors.Is(err, ledger.ErrAccountNotFound) {
		t.Errorf("GetSystemAccount = %v, want ErrAccountNotFound", err)
	}
}

func TestGetSystemAccount(t *testing.T) {
	t.Parallel()
	svc := accounts.NewService(pgtest.New(t), ledger.UUIDv7{})

	got, err := svc.GetSystemAccount(t.Context(), "cash_in_clearing", "COP")
	if err != nil {
		t.Fatalf("GetSystemAccount: %v", err)
	}
	if got.ID != uuid.MustParse("01900000-0000-7000-8000-000000000001") ||
		got.Type != accounts.TypeSystem || got.Code != "cash_in_clearing" || !got.AllowNegative {
		t.Errorf("got %+v, want the seeded COP cash_in_clearing", got)
	}
}

// There is no way to create an account with money: it starts at zero and is
// funded with a journal entry from a system account, so every peso has a source.
func TestInitialFundsArriveThroughTheLedger(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	svc := accounts.NewService(pool, ledger.UUIDv7{})

	ana, err := svc.CreateAccount(t.Context(), accounts.TypeUser, "COP")
	if err != nil {
		t.Fatal(err)
	}
	fund(t, pool, svc, ana.ID, 50_000)

	balance, err := svc.GetBalance(t.Context(), ana.ID)
	if err != nil {
		t.Fatal(err)
	}
	if balance.Available != 50_000 || balance.Total != 50_000 {
		t.Errorf("balance = %+v, want 50000", balance)
	}
}

func fund(t *testing.T, pool *pgxpool.Pool, svc *accounts.Service, to uuid.UUID, amount ledger.Amount) {
	t.Helper()

	clearing, err := svc.GetSystemAccount(t.Context(), "cash_in_clearing", "COP")
	if err != nil {
		t.Fatal(err)
	}
	_, err = ledger.NewService(pool, ledger.UUIDv7{}).Post(t.Context(), ledger.PostRequest{
		Reference:     "opening-" + to.String(),
		Kind:          ledger.KindCashIn,
		Currency:      "COP",
		CorrelationID: uuid.New(),
		InitiatedBy:   "test",
		Entries: []ledger.Entry{
			{AccountID: clearing.ID, Amount: -amount},
			{AccountID: to, Amount: amount},
		},
	})
	if err != nil {
		t.Fatalf("fund: %v", err)
	}
}
